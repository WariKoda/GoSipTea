package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/nibra/gosiptea/internal/app"
)

func renderAt(t *testing.T, snapshot Snapshot, width, height int, keys ...string) string {
	t.Helper()
	model := New(snapshot, nil)
	model, _ = updateModel(model, tea.WindowSizeMsg{Width: width, Height: height})
	for _, value := range keys {
		model, _ = updateModel(model, key(value))
	}
	return model.View()
}

// Every view must fill the terminal exactly, so the help line stays on the
// last line and nothing scrolls out of the alternate screen.
func TestLayoutFillsTerminal(t *testing.T) {
	sizes := []struct{ width, height int }{{44, 12}, {60, 20}, {80, 12}, {80, 24}, {120, 40}}
	for _, size := range sizes {
		for _, view := range []string{"1", "2", "3", "4", "5"} {
			rendered := renderAt(t, testSnapshot(), size.width, size.height, view)
			if got := lipgloss.Height(rendered); got != size.height {
				t.Errorf("%dx%d view %s: height = %d\n%s", size.width, size.height, view, got, rendered)
			}
			if got := lipgloss.Width(rendered); got > size.width {
				t.Errorf("%dx%d view %s: width = %d\n%s", size.width, size.height, view, got, rendered)
			}
			lines := strings.Split(rendered, "\n")
			if last := lines[len(lines)-1]; !strings.Contains(last, "q quit") {
				t.Errorf("%dx%d view %s: last line = %q", size.width, size.height, view, last)
			}
		}
	}
}

func TestStatusBarShowsPhoneStateInEveryView(t *testing.T) {
	snapshot := testSnapshot()
	snapshot.Phone.CallState = app.CallActive
	snapshot.Phone.Peer = "Alice"
	snapshot.Phone.CallStartedAt = snapshot.Now.Add(-65 * time.Second)
	snapshot.Phone.Muted = true
	snapshot.Phone.DND = true

	for _, view := range []string{"1", "2", "3", "4", "5"} {
		rendered := renderAt(t, snapshot, 100, 30, view)
		for _, text := range []string{"Registered 101", "Active 01:05 Alice", "Muted", "DND on"} {
			if !strings.Contains(rendered, text) {
				t.Errorf("view %s: status bar does not contain %q:\n%s", view, text, rendered)
			}
		}
	}
}

// On the minimum terminal the bar drops details but keeps state and duration.
func TestStatusBarShortensOnNarrowTerminals(t *testing.T) {
	snapshot := testSnapshot()
	snapshot.Phone.CallState = app.CallActive
	snapshot.Phone.Peer = "Alice Wonderland"
	snapshot.Phone.CallStartedAt = snapshot.Now.Add(-65 * time.Second)
	snapshot.Phone.DND = true

	rendered := renderAt(t, snapshot, 44, 12, "2")
	for _, text := range []string{"Active 01:05", "DND on"} {
		if !strings.Contains(rendered, text) {
			t.Errorf("narrow status bar does not contain %q:\n%s", text, rendered)
		}
	}
	if strings.Contains(rendered, "Wonderland") {
		t.Errorf("narrow status bar kept the peer:\n%s", rendered)
	}
}

func TestSidebarDependsOnWidthOnly(t *testing.T) {
	for _, size := range []struct{ width, height int }{{80, 12}, {120, 40}} {
		if rendered := renderAt(t, testSnapshot(), size.width, size.height, "2"); !strings.Contains(rendered, "› 2 Contacts") {
			t.Errorf("%dx%d: sidebar missing:\n%s", size.width, size.height, rendered)
		}
	}
	for _, width := range []int{44, 79} {
		rendered := renderAt(t, testSnapshot(), width, 30, "2")
		if strings.Contains(rendered, "GoSipTea") || !strings.Contains(rendered, "2 Con") {
			t.Errorf("width %d: want tab row instead of sidebar:\n%s", width, rendered)
		}
	}
}

func TestPhoneControlsFitMinimumTerminal(t *testing.T) {
	if rendered := renderAt(t, testSnapshot(), 44, 12, "1"); !strings.Contains(rendered, "[i] Enter number") {
		t.Fatalf("phone controls cut off:\n%s", rendered)
	}
}

func TestIncomingCallInStatusBar(t *testing.T) {
	snapshot := testSnapshot()
	snapshot.Phone.CallState = app.CallIncoming
	snapshot.Phone.Peer = "Alice"

	for _, view := range []string{"2", "3", "4", "5"} {
		if rendered := renderAt(t, snapshot, 100, 30, view); !strings.Contains(rendered, "Incoming: Alice  [1] answer") {
			t.Errorf("view %s: incoming call missing:\n%s", view, rendered)
		}
	}
	// The phone view shows the answer key itself.
	rendered := renderAt(t, snapshot, 100, 30, "1")
	if !strings.Contains(rendered, "Incoming: Alice") || strings.Contains(rendered, "[1] answer") {
		t.Errorf("phone view status bar:\n%s", rendered)
	}
	if rendered := renderAt(t, testSnapshot(), 100, 30, "2"); strings.Contains(rendered, "Incoming") {
		t.Errorf("incoming call shown while idle:\n%s", rendered)
	}
}
