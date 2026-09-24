// Package desktop raises the terminal window of this process on Hyprland.
package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultTimeout = 2 * time.Second

	MaxOutputBytes = 1 << 20

	// maxAncestors bounds the walk up the process tree. A terminal is a direct
	// parent or a few hops away through a shell.
	maxAncestors = 16
)

// ErrUnsupported reports that no Hyprland instance is reachable. Callers treat
// it as "nothing to focus" rather than as a failure.
var ErrUnsupported = errors.New("desktop: no Hyprland instance")

type commandRunner func(context.Context, int, ...string) ([]byte, error)

// Focuser moves the Hyprland focus to the window hosting this process.
type Focuser struct {
	timeout        time.Duration
	maxOutputBytes int
	run            commandRunner
	pid            int
	parentOf       func(int) (int, error)
	instance       func() string
}

// NewFocuser creates a focuser driving the hyprctl command line.
func NewFocuser() *Focuser {
	return &Focuser{
		timeout:        DefaultTimeout,
		maxOutputBytes: MaxOutputBytes,
		run:            runCommand,
		pid:            os.Getpid(),
		parentOf:       parentPID,
		instance:       func() string { return os.Getenv("HYPRLAND_INSTANCE_SIGNATURE") },
	}
}

type client struct {
	Address string `json:"address"`
	PID     int    `json:"pid"`
}

// Focus switches to the workspace of this process's window and focuses it.
// It returns ErrUnsupported outside Hyprland and when no window belongs to
// this process, for example when GoSipTea runs on a bare TTY.
func (f *Focuser) Focus(ctx context.Context) error {
	if f == nil {
		return errors.New("desktop: nil focuser")
	}
	if f.instance() == "" {
		return ErrUnsupported
	}

	ctx, cancel := context.WithTimeout(ctx, f.timeout)
	defer cancel()

	output, err := f.run(ctx, f.maxOutputBytes, "-j", "clients")
	if err != nil {
		return fmt.Errorf("desktop: list clients: %w", err)
	}
	var clients []client
	if err := json.Unmarshal(output, &clients); err != nil {
		return fmt.Errorf("desktop: decode clients: %w", err)
	}

	address, err := f.ownWindow(clients)
	if err != nil {
		return err
	}
	return f.dispatchFocus(ctx, address)
}

// dispatchFocus prefers the Lua dispatcher of current Hyprland releases and
// falls back to the plain dispatcher syntax of older ones. The address passed
// validAddress, so embedding it in the Lua expression is safe.
func (f *Focuser) dispatchFocus(ctx context.Context, address string) error {
	lua := fmt.Sprintf(`hl.dsp.focus({ window = "address:%s" })`, address)
	if _, err := f.run(ctx, f.maxOutputBytes, "dispatch", lua); err == nil {
		return nil
	}
	if _, err := f.run(ctx, f.maxOutputBytes, "dispatch", "focuswindow", "address:"+address); err != nil {
		return fmt.Errorf("desktop: focus window: %w", err)
	}
	return nil
}

// ownWindow returns the address of the closest ancestor process that owns a
// window. The closest ancestor is the terminal actually displaying the TUI.
func (f *Focuser) ownWindow(clients []client) (string, error) {
	byPID := make(map[int]string, len(clients))
	for _, c := range clients {
		if c.PID > 0 && validAddress(c.Address) {
			byPID[c.PID] = c.Address
		}
	}

	pid := f.pid
	for hop := 0; hop < maxAncestors && pid > 1; hop++ {
		if address, ok := byPID[pid]; ok {
			return address, nil
		}
		parent, err := f.parentOf(pid)
		if err != nil {
			return "", fmt.Errorf("desktop: read parent of %d: %w", pid, err)
		}
		if parent == pid {
			break
		}
		pid = parent
	}
	return "", ErrUnsupported
}

func validAddress(address string) bool {
	if !strings.HasPrefix(address, "0x") || len(address) > 32 {
		return false
	}
	_, err := strconv.ParseUint(address[2:], 16, 64)
	return err == nil
}

// parentPID reads the parent process id from /proc. The comm field may contain
// spaces and parentheses, so parsing starts after its final closing bracket.
func parentPID(pid int) (int, error) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, err
	}
	end := strings.LastIndex(string(raw), ")")
	if end < 0 {
		return 0, errors.New("malformed stat entry")
	}
	fields := strings.Fields(string(raw)[end+1:])
	if len(fields) < 2 {
		return 0, errors.New("stat entry without parent id")
	}
	return strconv.Atoi(fields[1])
}
