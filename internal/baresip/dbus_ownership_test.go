package baresip

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

func privateSessionBus(t *testing.T) {
	t.Helper()
	binary, err := exec.LookPath("dbus-daemon")
	if err != nil {
		t.Skip("dbus-daemon is required for isolated ownership tests")
	}
	dir, err := os.MkdirTemp("", "gosiptea-test-bus-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	address := "unix:path=" + filepath.Join(dir, "bus")
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", address)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	command := exec.CommandContext(ctx, binary, "--session", "--nofork", "--nopidfile", "--print-address=1", "--address="+address)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); _ = command.Wait() })
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("private dbus-daemon startup: %v", err)
	}
	if !strings.HasPrefix(line, address) {
		t.Fatalf("unexpected private bus address: %q", line)
	}
}

func ownershipService(t *testing.T, response string) (*dbus.Conn, *atomic.Int64) {
	t.Helper()
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	calls := &atomic.Int64{}
	if err := conn.ExportMethodTable(map[string]any{
		"invoke": func(string) (string, *dbus.Error) { calls.Add(1); return response, nil },
	}, dbus.ObjectPath(ObjectPath), InterfaceName); err != nil {
		t.Fatal(err)
	}
	return conn, calls
}

func takeServiceName(t *testing.T, conn *dbus.Conn) {
	t.Helper()
	reply, err := conn.RequestName(ServiceName, dbus.NameFlagDoNotQueue)
	if err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("RequestName = %v, %v", reply, err)
	}
}

func TestOwnershipRejectsForeignBusOwnerBeforeCommands(t *testing.T) {
	privateSessionBus(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	manager := NewProcessManager(ProcessOptions{Path: "/bin/sleep", Args: []string{"30"}, StopTimeout: time.Second})
	if err := manager.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopOwnedTestProcess(t, manager) })
	// A foreign process wins the service name after the preflight owner check.
	foreign, calls := ownershipService(t, "foreign")
	takeServiceName(t, foreign)
	client, err := NewClient(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.Command(ctx, "quit", ""); !errors.Is(err, ErrOwnerUnverified) {
		t.Fatalf("unverified Command = %v", err)
	}
	for _, pid := range []int{0, -1, manager.PID()} {
		if err := client.VerifyOwner(ctx, pid); !errors.Is(err, ErrOwnerMismatch) {
			t.Fatalf("VerifyOwner(%d) = %v, want PID mismatch", pid, err)
		}
	}
	if _, err := client.Command(ctx, "reginfo", ""); !errors.Is(err, ErrOwnerUnverified) {
		t.Fatalf("Command after failed verification = %v", err)
	}
	if calls.Load() != 0 {
		t.Fatal("foreign owner received a command")
	}
}

func TestOwnershipCommandsStayPinnedAcrossNameHandoff(t *testing.T) {
	privateSessionBus(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	original, originalCalls := ownershipService(t, "original")
	replacement, replacementCalls := ownershipService(t, "replacement")
	takeServiceName(t, original)
	client, err := NewClient(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.VerifyOwner(ctx, os.Getpid()); err != nil {
		t.Fatal(err)
	}
	if got, err := client.Command(ctx, "reginfo", ""); err != nil || got != "original" {
		t.Fatalf("verified Command = %q, %v", got, err)
	}
	// Delay owner-change handling deterministically. Commands must be safe even
	// before the event loop notices the handoff, not merely after it closes.
	client.conn.RemoveSignal(client.signals)
	if _, err := original.ReleaseName(ServiceName); err != nil {
		t.Fatal(err)
	}
	takeServiceName(t, replacement)
	if got, err := client.Command(ctx, "quit", ""); err != nil || got != "original" {
		t.Fatalf("Command after handoff = %q, %v, want original", got, err)
	}
	if originalCalls.Load() != 2 || replacementCalls.Load() != 0 {
		t.Fatalf("calls original=%d replacement=%d", originalCalls.Load(), replacementCalls.Load())
	}
}

func TestOwnershipExistingOwnerStillRejectsStartup(t *testing.T) {
	privateSessionBus(t)
	foreign, calls := ownershipService(t, "foreign")
	takeServiceName(t, foreign)
	manager := NewProcessManager(ProcessOptions{Path: "/must/not/start"})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := manager.Start(ctx); !errors.Is(err, ErrServiceOwned) {
		t.Fatalf("Start = %v, want existing owner rejection", err)
	}
	lock, err := AcquireStartupLock(ctx)
	if err != nil {
		t.Fatalf("lock leaked on existing owner: %v", err)
	}
	_ = lock.Close()
	if calls.Load() != 0 {
		t.Fatal("existing owner received a command")
	}
}
