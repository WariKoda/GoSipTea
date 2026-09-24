package baresip

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"
)

func TestProcessManagerRejectsExistingOwner(t *testing.T) {
	manager := NewProcessManager(ProcessOptions{
		Path: "/path/that/must/not/be/run",
		OwnerChecker: OwnerCheckFunc(func(context.Context) (bool, error) {
			return true, nil
		}),
	})

	err := manager.Start(context.Background())
	if !errors.Is(err, ErrServiceOwned) {
		t.Fatalf("Start() error = %v, want %v", err, ErrServiceOwned)
	}
	if manager.Running() {
		t.Fatal("manager reports a running process")
	}
}

func TestProcessManagerReportsOwnerCheckFailure(t *testing.T) {
	checkErr := errors.New("check failed")
	manager := NewProcessManager(ProcessOptions{
		OwnerChecker: OwnerCheckFunc(func(context.Context) (bool, error) {
			return false, checkErr
		}),
	})

	err := manager.Start(context.Background())
	if !errors.Is(err, checkErr) {
		t.Fatalf("Start() error = %v, want wrapped %v", err, checkErr)
	}
}

func TestProcessManagerStartAndStop(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("process-group behavior is Linux-specific")
	}

	manager := NewProcessManager(ProcessOptions{
		Path:        "/bin/sleep",
		Args:        []string{"30"},
		StopTimeout: time.Second,
		OwnerChecker: OwnerCheckFunc(func(context.Context) (bool, error) {
			return false, nil
		}),
	})
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	if !manager.Running() {
		t.Fatal("manager does not report its running child")
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := manager.Stop(stopCtx); err != nil {
		t.Fatalf("Stop() error: %v", err)
	}
	if manager.Running() {
		t.Fatal("manager still reports a running child")
	}
}

func TestProcessManagerWaitBeforeStart(t *testing.T) {
	manager := NewProcessManager(ProcessOptions{
		OwnerChecker: OwnerCheckFunc(func(context.Context) (bool, error) {
			return false, nil
		}),
	})
	if err := manager.Wait(context.Background()); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("Wait() error = %v, want %v", err, ErrNotRunning)
	}
}
