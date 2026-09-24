//go:build linux

package baresip

import (
	"os/exec"
	"syscall"
	"testing"
)

func TestConfigureChildProcessKillsChildWhenParentDies(t *testing.T) {
	command := exec.Command("true")
	configureChildProcess(command)
	if command.SysProcAttr == nil {
		t.Fatal("SysProcAttr is nil")
	}
	if !command.SysProcAttr.Setpgid {
		t.Fatal("child does not get its own process group")
	}
	if command.SysProcAttr.Pdeathsig != syscall.SIGKILL {
		t.Fatalf("Pdeathsig = %v, want SIGKILL", command.SysProcAttr.Pdeathsig)
	}
}
