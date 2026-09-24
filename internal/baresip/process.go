package baresip

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

const DefaultStopTimeout = 3 * time.Second

var (
	ErrAlreadyRunning = errors.New("baresip process is already running")
	ErrNotRunning     = errors.New("baresip process is not running")
	ErrServiceOwned   = errors.New("another process owns com.github.Baresip")
)

// OwnerChecker checks whether another process already exports the baresip service.
type OwnerChecker interface {
	HasOwner(ctx context.Context) (bool, error)
}

// OwnerCheckFunc adapts a function to OwnerChecker.
type OwnerCheckFunc func(ctx context.Context) (bool, error)

func (check OwnerCheckFunc) HasOwner(ctx context.Context) (bool, error) {
	return check(ctx)
}

type sessionOwnerChecker struct{}

func (sessionOwnerChecker) HasOwner(ctx context.Context) (bool, error) {
	return ServiceHasOwner(ctx)
}

// ProcessOptions configures a baresip child process owned by this application.
type ProcessOptions struct {
	Path         string
	Args         []string
	Dir          string
	Env          []string
	Stdin        io.Reader
	Stdout       io.Writer
	Stderr       io.Writer
	StopTimeout  time.Duration
	OwnerChecker OwnerChecker
}

// ProcessManager starts and stops only the child it created. It never invokes systemd.
type ProcessManager struct {
	options ProcessOptions

	mu       sync.RWMutex
	cmd      *exec.Cmd
	done     chan struct{}
	waitErr  error
	running  bool
	stopping bool
}

// NewProcessManager constructs a manager. The default executable is "baresip".
func NewProcessManager(options ProcessOptions) *ProcessManager {
	if options.Path == "" {
		options.Path = "baresip"
	}
	if options.StopTimeout <= 0 {
		options.StopTimeout = DefaultStopTimeout
	}
	if options.OwnerChecker == nil {
		options.OwnerChecker = sessionOwnerChecker{}
	}
	options.Args = append([]string(nil), options.Args...)
	if options.Env != nil {
		options.Env = append([]string{}, options.Env...)
	}
	return &ProcessManager{options: options}
}

// Start rejects an existing D-Bus owner before starting a new process group.
func (manager *ProcessManager) Start(ctx context.Context) error {
	if ctx == nil {
		return errors.New("nil context")
	}

	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.running || manager.stopping {
		return ErrAlreadyRunning
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	hasOwner, err := manager.options.OwnerChecker.HasOwner(ctx)
	if err != nil {
		return fmt.Errorf("check existing baresip owner: %w", err)
	}
	if hasOwner {
		return ErrServiceOwned
	}

	done := make(chan struct{})
	cmd := exec.CommandContext(ctx, manager.options.Path, manager.options.Args...)
	cmd.Dir = manager.options.Dir
	cmd.Env = manager.options.Env
	cmd.Stdin = manager.options.Stdin
	cmd.Stdout = manager.options.Stdout
	cmd.Stderr = manager.options.Stderr
	configureChildProcess(cmd)

	cmd.Cancel = func() error {
		signalErr := signalProcessGroup(cmd.Process, terminationSignal())
		go killProcessGroupAfter(done, cmd.Process, manager.options.StopTimeout)
		return normalizeSignalError(signalErr)
	}
	cmd.WaitDelay = manager.options.StopTimeout

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start baresip: %w", err)
	}

	manager.cmd = cmd
	manager.done = done
	manager.waitErr = nil
	manager.running = true
	go manager.wait(cmd, done)
	return nil
}

// Stop sends SIGTERM to the child process group and escalates to SIGKILL.
func (manager *ProcessManager) Stop(ctx context.Context) error {
	if ctx == nil {
		return errors.New("nil context")
	}

	manager.mu.Lock()
	if !manager.running {
		manager.mu.Unlock()
		return nil
	}
	cmd := manager.cmd
	done := manager.done
	timeout := manager.options.StopTimeout
	if manager.stopping {
		manager.mu.Unlock()
		select {
		case <-done:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	manager.stopping = true
	manager.mu.Unlock()
	defer manager.finishStopping(done)

	if err := normalizeSignalError(signalProcessGroup(cmd.Process, terminationSignal())); err != nil {
		return fmt.Errorf("terminate baresip process group: %w", err)
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return nil
	case <-timer.C:
		if err := normalizeSignalError(signalProcessGroup(cmd.Process, killSignal())); err != nil {
			return fmt.Errorf("kill baresip process group: %w", err)
		}
	case <-ctx.Done():
		_ = normalizeSignalError(signalProcessGroup(cmd.Process, killSignal()))
		return ctx.Err()
	}

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Wait waits for the current child and returns its process error.
func (manager *ProcessManager) Wait(ctx context.Context) error {
	if ctx == nil {
		return errors.New("nil context")
	}

	manager.mu.RLock()
	done := manager.done
	manager.mu.RUnlock()
	if done == nil {
		return ErrNotRunning
	}

	select {
	case <-done:
		return manager.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (manager *ProcessManager) Running() bool {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return manager.running
}

// Done is nil before the first successful Start.
func (manager *ProcessManager) Done() <-chan struct{} {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return manager.done
}

func (manager *ProcessManager) Err() error {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return manager.waitErr
}

func (manager *ProcessManager) finishStopping(done <-chan struct{}) {
	manager.mu.Lock()
	if manager.done == done {
		manager.stopping = false
	}
	manager.mu.Unlock()
}

func (manager *ProcessManager) wait(cmd *exec.Cmd, done chan struct{}) {
	err := cmd.Wait()
	_ = normalizeSignalError(signalProcessGroup(cmd.Process, killSignal()))

	manager.mu.Lock()
	if manager.cmd == cmd {
		manager.waitErr = err
		manager.running = false
		manager.cmd = nil
	}
	close(done)
	manager.mu.Unlock()
}

func killProcessGroupAfter(done <-chan struct{}, process *os.Process, timeout time.Duration) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return
	case <-timer.C:
		_ = normalizeSignalError(signalProcessGroup(process, killSignal()))
	}
}

func normalizeSignalError(err error) error {
	if err == nil || errors.Is(err, os.ErrProcessDone) || isProcessGone(err) {
		return nil
	}
	return err
}
