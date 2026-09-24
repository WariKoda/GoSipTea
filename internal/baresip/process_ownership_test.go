package baresip

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type inertStartupLock struct{}

func (inertStartupLock) Close() error                      { return nil }
func (inertStartupLock) Done() <-chan struct{}             { return nil }
func fakeStartupLock(context.Context) (StartupLock, error) { return inertStartupLock{}, nil }

func stopOwnedTestProcess(t *testing.T, manager *ProcessManager) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := manager.Stop(ctx); err != nil {
		t.Errorf("Stop: %v", err)
	}
}

func TestOwnershipConcurrentIndependentStartup(t *testing.T) {
	privateSessionBus(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var checks atomic.Int64
	checker := OwnerCheckFunc(func(ctx context.Context) (bool, error) {
		checks.Add(1)
		return ServiceHasOwner(ctx)
	})
	managers := []*ProcessManager{
		NewProcessManager(ProcessOptions{Path: "/bin/sleep", Args: []string{"30"}, StopTimeout: time.Second, OwnerChecker: checker}),
		NewProcessManager(ProcessOptions{Path: "/bin/sleep", Args: []string{"30"}, StopTimeout: time.Second, OwnerChecker: checker}),
	}
	for _, manager := range managers {
		t.Cleanup(func() { stopOwnedTestProcess(t, manager) })
	}
	start := make(chan struct{})
	type result struct {
		index int
		err   error
	}
	results := make(chan result, 2)
	for index, manager := range managers {
		go func() {
			<-start
			results <- result{index, manager.Start(ctx)}
		}()
	}
	close(start)
	winner, loser := -1, -1
	for range managers {
		got := <-results
		switch {
		case got.err == nil:
			if winner >= 0 {
				t.Fatal("both independent managers started children")
			}
			winner = got.index
		case errors.Is(got.err, ErrStartupLocked):
			loser = got.index
		default:
			t.Fatalf("Start: %v", got.err)
		}
	}
	if winner < 0 || loser < 0 {
		t.Fatalf("winner=%d loser=%d", winner, loser)
	}
	if checks.Load() != 1 {
		t.Fatalf("owner check ran %d times, want only the lock holder", checks.Load())
	}
	if managers[winner].PID() <= 0 || managers[loser].PID() != 0 {
		t.Fatal("unexpected owned child PIDs")
	}
	// The child deliberately never exports Baresip. The lock must cover this
	// entire interval, including attempts made by another OS process.
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	probe := exec.CommandContext(ctx, binary, "-test.run=^TestOwnershipLockProbe$")
	probe.Env = append(os.Environ(), "GOSIPTEA_LOCK_PROBE=1")
	if output, err := probe.CombinedOutput(); err != nil {
		t.Fatalf("independent app lock probe: %v\n%s", err, output)
	}
	stopOwnedTestProcess(t, managers[winner])
	if err := managers[loser].Start(ctx); err != nil {
		t.Fatalf("start after previous child reaped: %v", err)
	}
}

func TestOwnershipLockProbe(t *testing.T) {
	if os.Getenv("GOSIPTEA_LOCK_PROBE") != "1" {
		return
	}
	if !strings.Contains(os.Getenv("DBUS_SESSION_BUS_ADDRESS"), "gosiptea-test-bus") {
		t.Fatal("lock probe requires the private test bus")
	}
	manager := NewProcessManager(ProcessOptions{Path: "/must/not/start"})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := manager.Start(ctx); !errors.Is(err, ErrStartupLocked) {
		t.Fatalf("independent process Start = %v, want startup lock rejection", err)
	}
}

func TestOwnershipStartupFailureReleasesLock(t *testing.T) {
	privateSessionBus(t)
	checkErr := errors.New("owner check failed")
	for _, test := range []struct {
		name    string
		checker OwnerChecker
		want    error
	}{
		{"existing owner", OwnerCheckFunc(func(context.Context) (bool, error) { return true, nil }), ErrServiceOwned},
		{"failed check", OwnerCheckFunc(func(context.Context) (bool, error) { return false, checkErr }), checkErr},
		{"failed exec", OwnerCheckFunc(func(context.Context) (bool, error) { return false, nil }), os.ErrNotExist},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager := NewProcessManager(ProcessOptions{Path: "/must/not/exist/baresip", OwnerChecker: test.checker})
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := manager.Start(ctx); !errors.Is(err, test.want) {
				t.Fatalf("Start = %v, want %v", err, test.want)
			}
			if manager.PID() != 0 {
				t.Fatal("failed startup has a PID")
			}
			lock, err := AcquireStartupLock(ctx)
			if err != nil {
				t.Fatalf("lock leaked after startup failure: %v", err)
			}
			_ = lock.Close()
		})
	}
}

func TestOwnershipChildExitReleasesLock(t *testing.T) {
	privateSessionBus(t)
	manager := NewProcessManager(ProcessOptions{Path: "/bin/true"})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := manager.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopOwnedTestProcess(t, manager) })
	if err := manager.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if manager.PID() != 0 {
		t.Fatal("exited child retains PID")
	}
	lock, err := AcquireStartupLock(ctx)
	if err != nil {
		t.Fatalf("lock leaked after child exit: %v", err)
	}
	_ = lock.Close()
}

func TestOwnershipLockLossStopsChild(t *testing.T) {
	privateSessionBus(t)
	var acquired StartupLock
	manager := NewProcessManager(ProcessOptions{
		Path: "/bin/sleep", Args: []string{"30"}, StopTimeout: time.Second,
		AcquireLock: func(ctx context.Context) (StartupLock, error) {
			var err error
			acquired, err = AcquireStartupLock(ctx)
			return acquired, err
		},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := manager.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopOwnedTestProcess(t, manager) })
	_ = acquired.Close()
	select {
	case <-manager.Done():
	case <-ctx.Done():
		t.Fatal("child survived loss of startup lock")
	}
	if manager.PID() != 0 {
		t.Fatal("child still running after lock loss")
	}
}
