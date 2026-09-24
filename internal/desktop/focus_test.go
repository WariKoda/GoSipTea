package desktop

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type recordedCall struct {
	args []string
}

func newTestFocuser(clients string, parents map[int]int, pid int) (*Focuser, *[]recordedCall) {
	var calls []recordedCall
	focuser := &Focuser{
		timeout:        DefaultTimeout,
		maxOutputBytes: MaxOutputBytes,
		pid:            pid,
		instance:       func() string { return "hypr-test" },
		parentOf: func(p int) (int, error) {
			parent, ok := parents[p]
			if !ok {
				return 0, errors.New("no such process")
			}
			return parent, nil
		},
	}
	focuser.run = func(_ context.Context, _ int, args ...string) ([]byte, error) {
		calls = append(calls, recordedCall{args: args})
		if len(args) > 0 && args[0] == "-j" {
			return []byte(clients), nil
		}
		return nil, nil
	}
	return focuser, &calls
}

func TestFocusUsesClosestAncestorWindow(t *testing.T) {
	t.Parallel()
	clients := `[{"address":"0x55a1","pid":100},{"address":"0x55b2","pid":300}]`
	focuser, calls := newTestFocuser(clients, map[int]int{500: 400, 400: 300, 300: 100, 100: 1}, 500)

	if err := focuser.Focus(context.Background()); err != nil {
		t.Fatalf("Focus() error = %v", err)
	}
	if len(*calls) != 2 {
		t.Fatalf("hyprctl calls = %d, want 2", len(*calls))
	}
	got := strings.Join((*calls)[1].args, " ")
	if got != `dispatch hl.dsp.focus({ window = "address:0x55b2" })` {
		t.Fatalf("dispatch args = %q", got)
	}
}

func TestFocusFallsBackToPlainDispatcher(t *testing.T) {
	t.Parallel()
	clients := `[{"address":"0x55b2","pid":500}]`
	focuser, calls := newTestFocuser(clients, map[int]int{500: 1}, 500)
	list := focuser.run
	focuser.run = func(ctx context.Context, limit int, args ...string) ([]byte, error) {
		output, _ := list(ctx, limit, args...)
		if len(args) == 2 && strings.HasPrefix(args[1], "hl.dsp") {
			return nil, errors.New("unknown dispatcher")
		}
		return output, nil
	}

	if err := focuser.Focus(context.Background()); err != nil {
		t.Fatalf("Focus() error = %v", err)
	}
	if len(*calls) != 3 {
		t.Fatalf("hyprctl calls = %d, want 3", len(*calls))
	}
	got := strings.Join((*calls)[2].args, " ")
	if got != "dispatch focuswindow address:0x55b2" {
		t.Fatalf("fallback args = %q", got)
	}
}

func TestFocusWithoutHyprlandIsUnsupported(t *testing.T) {
	t.Parallel()
	focuser, calls := newTestFocuser(`[]`, nil, 500)
	focuser.instance = func() string { return "" }

	if err := focuser.Focus(context.Background()); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Focus() error = %v, want ErrUnsupported", err)
	}
	if len(*calls) != 0 {
		t.Fatalf("hyprctl calls = %d, want 0", len(*calls))
	}
}

func TestFocusWithoutOwnWindowIsUnsupported(t *testing.T) {
	t.Parallel()
	clients := `[{"address":"0x55a1","pid":900}]`
	focuser, calls := newTestFocuser(clients, map[int]int{500: 1}, 500)

	if err := focuser.Focus(context.Background()); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Focus() error = %v, want ErrUnsupported", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("hyprctl calls = %d, want 1", len(*calls))
	}
}

func TestFocusRejectsMalformedAddress(t *testing.T) {
	t.Parallel()
	clients := `[{"address":"; rm -rf /","pid":500}]`
	focuser, _ := newTestFocuser(clients, map[int]int{500: 1}, 500)

	if err := focuser.Focus(context.Background()); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Focus() error = %v, want ErrUnsupported", err)
	}
}

func TestFocusStopsAtProcessTreeRoot(t *testing.T) {
	t.Parallel()
	focuser, _ := newTestFocuser(`[]`, map[int]int{500: 500}, 500)

	if err := focuser.Focus(context.Background()); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Focus() error = %v, want ErrUnsupported", err)
	}
}
