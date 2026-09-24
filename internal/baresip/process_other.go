//go:build !linux

package baresip

import (
	"os"
	"os/exec"
)

func configureChildProcess(_ *exec.Cmd) {}

func terminationSignal() os.Signal {
	return os.Interrupt
}

func killSignal() os.Signal {
	return os.Kill
}

func signalProcessGroup(process *os.Process, signal os.Signal) error {
	if process == nil {
		return os.ErrProcessDone
	}
	return process.Signal(signal)
}

func isProcessGone(_ error) bool {
	return false
}
