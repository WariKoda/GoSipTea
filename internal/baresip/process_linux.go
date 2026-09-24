//go:build linux

package baresip

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func configureChildProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid:   true,
		Pdeathsig: syscall.SIGKILL,
	}
}

func terminationSignal() os.Signal {
	return syscall.SIGTERM
}

func killSignal() os.Signal {
	return syscall.SIGKILL
}

func signalProcessGroup(process *os.Process, signal os.Signal) error {
	if process == nil {
		return os.ErrProcessDone
	}
	unixSignal, ok := signal.(syscall.Signal)
	if !ok {
		return errors.New("unsupported process signal")
	}
	return syscall.Kill(-process.Pid, unixSignal)
}

func isProcessGone(err error) bool {
	return errors.Is(err, syscall.ESRCH)
}
