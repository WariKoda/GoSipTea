package session

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nibra/gosiptea/internal/baresip"
)

type ownershipClient struct {
	*fakeClient
	verify func(context.Context, int) error
}

func (c *ownershipClient) VerifyOwner(ctx context.Context, pid int) error {
	c.order.add("client.verify")
	return c.verify(ctx, pid)
}

func TestOwnershipSessionRejectsForeignClientAndStopsOnlyChild(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	client := newFakeClient(h.order)
	attempts := 0
	h.session.deps.NewClient = func(context.Context) (baresip.Client, error) {
		attempts++
		return &ownershipClient{fakeClient: client, verify: func(ctx context.Context, pid int) error {
			if pid != h.processes[0].PID() || pid <= 0 {
				t.Fatalf("verification PID = %d, want owned child PID", pid)
			}
			if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > h.session.config.CommandTimeout {
				t.Fatal("owner verification lacks the configured timeout")
			}
			return baresip.ErrOwnerMismatch
		}}, nil
	}
	if err := h.session.Start(context.Background()); !errors.Is(err, baresip.ErrOwnerMismatch) {
		t.Fatalf("Start = %v, want foreign owner rejection", err)
	}
	if attempts != 1 {
		t.Fatalf("retried a foreign owner %d times", attempts)
	}
	if got := client.commandsCopy(); len(got) != 0 {
		t.Fatalf("foreign client received commands: %v", got)
	}
	order := h.order.copy()
	verify := indexOf(order, "client.verify")
	closed := indexOf(order, "client.close")
	stopped := indexOf(order, "process.stop")
	if verify < 0 || closed <= verify || stopped <= closed {
		t.Fatalf("ownership failure cleanup order = %v", order)
	}
	if h.session.Snapshot().Running || h.processes[0].PID() != 0 {
		t.Fatal("failed ownership left the runtime or child running")
	}
}

func TestOwnershipSessionVerifiesBeforeFirstCommand(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	ctx := context.Background()
	if err := h.session.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer h.session.Stop(ctx)
	order := h.order.copy()
	verify := indexOf(order, "client.verify")
	registration := indexOf(order, "client.command reginfo")
	if verify < 0 || registration <= verify {
		t.Fatalf("startup commands preceded ownership verification: %v", order)
	}
}

func TestOwnershipSessionVerificationFailureCleanup(t *testing.T) {
	for _, failure := range []string{"verification timeout", "child exited before verification", "child exited during verification"} {
		t.Run(failure, func(t *testing.T) {
			h := newHarness(t)
			client := newFakeClient(h.order)
			h.session.config.CommandTimeout = 5 * time.Millisecond
			want := baresip.ErrNotRunning
			if failure == "verification timeout" {
				want = context.DeadlineExceeded
			}
			h.session.deps.NewClient = func(context.Context) (baresip.Client, error) {
				if failure == "child exited before verification" {
					_ = h.processes[0].Stop(context.Background())
				}
				return &ownershipClient{fakeClient: client, verify: func(ctx context.Context, pid int) error {
					if failure == "verification timeout" {
						<-ctx.Done()
						return ctx.Err()
					}
					if failure == "child exited before verification" {
						t.Fatal("attempted verification without a live owned child")
					}
					_ = h.processes[0].Stop(context.Background())
					return nil
				}}, nil
			}
			if err := h.session.Start(context.Background()); !errors.Is(err, want) {
				t.Fatalf("Start = %v, want %v", err, want)
			}
			if got := client.commandsCopy(); len(got) != 0 {
				t.Fatalf("commands = %v", got)
			}
			select {
			case <-client.Done():
			default:
				t.Fatal("client not closed")
			}
			if h.processes[0].PID() != 0 {
				t.Fatal("owned child not stopped")
			}
		})
	}
}
