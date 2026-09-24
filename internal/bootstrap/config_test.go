package bootstrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureConfigCreatesSecureFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "baresip")
	if err := EnsureConfig(dir); err != nil {
		t.Fatal(err)
	}

	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("directory mode = %o, want 700", got)
	}

	data, err := os.ReadFile(filepath.Join(dir, "config"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "module_app              ctrl_dbus.so") {
		t.Fatal("generated config does not enable ctrl_dbus")
	}
	info, err := os.Stat(filepath.Join(dir, "config"))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("config mode = %o, want 600", got)
	}
}

func TestEnsureConfigPreservesExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	custom := "# custom\nmodule_app ctrl_dbus.so\nctrl_dbus_use session\n"
	if err := os.WriteFile(path, []byte(custom), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := EnsureConfig(dir); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != custom {
		t.Fatalf("existing config changed to %q", data)
	}
}

func TestValidateConfigRejectsUnsafeControlModules(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	config := "module_app ctrl_dbus.so\nctrl_dbus_use session\nmodule ctrl_tcp.so\n"
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateConfig(path); err == nil || !strings.Contains(err.Error(), "ctrl_tcp.so") {
		t.Fatalf("ValidateConfig() error = %v", err)
	}
}

func TestValidateConfigRequiresSessionDBus(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	if err := os.WriteFile(path, []byte("module_app ctrl_dbus.so\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateConfig(path); err == nil {
		t.Fatal("ValidateConfig accepted a config without session D-Bus")
	}
}

func TestEnsureConfigRejectsSymlinks(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "baresip")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := EnsureConfig(link); err == nil {
		t.Fatal("EnsureConfig accepted a symlink directory")
	}
}
