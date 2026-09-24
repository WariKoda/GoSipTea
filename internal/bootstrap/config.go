package bootstrap

import (
	"bufio"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

//go:embed config.tmpl
var defaultConfig []byte

// EnsureConfig creates a minimal baresip configuration when one does not
// already exist. Existing configuration is never replaced.
func EnsureConfig(dir string) error {
	if dir == "" {
		return errors.New("bootstrap: empty configuration directory")
	}
	dir = filepath.Clean(dir)

	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create baresip directory: %w", err)
		}
		info, err = os.Lstat(dir)
	}
	if err != nil {
		return fmt.Errorf("inspect baresip directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("bootstrap: baresip directory must be a real directory: %s", dir)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("secure baresip directory: %w", err)
	}

	path := filepath.Join(dir, "config")
	info, err = os.Lstat(path)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("bootstrap: refusing non-regular config: %s", path)
		}
		return ValidateConfig(path)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect baresip config: %w", err)
	}

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create baresip config: %w", err)
	}
	remove := true
	defer func() {
		_ = file.Close()
		if remove {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(defaultConfig); err != nil {
		return fmt.Errorf("write baresip config: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync baresip config: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close baresip config: %w", err)
	}
	remove = false

	directory, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open baresip directory: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync baresip directory: %w", err)
	}
	return ValidateConfig(path)
}

// ValidateConfig rejects control modules that expose unauthenticated local
// endpoints and requires the session D-Bus control module used by this app.
func ValidateConfig(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open baresip config: %w", err)
	}
	defer file.Close()

	limited := io.LimitReader(file, 1<<20)
	scanner := bufio.NewScanner(limited)
	hasDBus := false
	hasSessionBus := false
	unsafe := map[string]bool{
		"ctrl_tcp.so": true,
		"httpd.so":    true,
		"cons.so":     true,
		"mqtt.so":     true,
	}
	for scanner.Scan() {
		line := strings.TrimSpace(strings.SplitN(scanner.Text(), "#", 2)[0])
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "module", "module_app":
			module := filepath.Base(fields[1])
			if unsafe[module] {
				return fmt.Errorf("bootstrap: unsafe baresip control module is enabled: %s", module)
			}
			if module == "ctrl_dbus.so" {
				hasDBus = true
			}
		case "ctrl_dbus_use":
			hasSessionBus = fields[1] == "session"
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read baresip config: %w", err)
	}
	if !hasDBus || !hasSessionBus {
		return errors.New("bootstrap: baresip config must enable ctrl_dbus.so on the session bus")
	}
	return nil
}
