package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/nibra/gosiptea/internal/app"
)

func TestModelRendersAllViews(t *testing.T) {
	snapshot := testSnapshot()
	tests := []struct {
		key  string
		view View
		text []string
	}{
		{"1", ViewPhone, []string{"Phone", "Registered", "Idle", "Dial"}},
		{"2", ViewContacts, []string{"Contacts", "Alice", "sip:alice@example.com"}},
		{"3", ViewAudio, []string{"Audio", "Output", "Input", "System default", "Desk speakers"}},
		{"4", ViewAccount, []string{"Account", "Server", "User", "Password", "TLS and SRTP"}},
		{"5", ViewHistory, []string{"Call history", "Alice", "01:05"}},
	}
	for _, test := range tests {
		t.Run(test.key, func(t *testing.T) {
			model := New(snapshot, nil)
			model, _ = updateModel(model, key(test.key))
			if model.ActiveView() != test.view {
				t.Fatalf("active view = %v, want %v", model.ActiveView(), test.view)
			}
			rendered := model.View()
			for _, text := range test.text {
				if !strings.Contains(rendered, text) {
					t.Errorf("view does not contain %q:\n%s", text, rendered)
				}
			}
		})
	}
}

func TestHistoryNavigationAndRedial(t *testing.T) {
	var actions []Action
	model := New(testSnapshot(), func(action Action) tea.Cmd {
		actions = append(actions, action)
		return nil
	})
	model, _ = updateModel(model, key("5"))
	model, _ = updateModel(model, key("down"))
	model, _ = updateModel(model, key("enter"))
	if model.ActiveView() != ViewHistory || model.historyCursor != 1 {
		t.Fatalf("history selection: view=%v cursor=%d", model.ActiveView(), model.historyCursor)
	}
	if len(actions) != 1 || actions[0].Kind != ActionDial || actions[0].Target != "sip:bob@example.com" {
		t.Fatalf("history actions = %#v", actions)
	}
}

func TestHistoryKeepsSelectionWhenNewCallIsPrepended(t *testing.T) {
	var actions []Action
	model := New(testSnapshot(), func(action Action) tea.Cmd {
		actions = append(actions, action)
		return nil
	})
	model, _ = updateModel(model, key("5"))
	model, _ = updateModel(model, key("down"))

	updated := model.Snapshot()
	newCall := app.CallHistoryEntry{
		Direction: app.CallDirectionIncoming,
		Outcome:   app.CallOutcomeMissed,
		Peer:      "Carol",
		Target:    "sip:carol@example.com",
		StartedAt: updated.Now,
		EndedAt:   updated.Now,
	}
	updated.History.Calls = append([]app.CallHistoryEntry{newCall}, updated.History.Calls...)
	model, _ = updateModel(model, SnapshotMsg{Snapshot: updated})
	model, _ = updateModel(model, key("enter"))

	if model.historyCursor != 2 || len(actions) != 1 || actions[0].Target != "sip:bob@example.com" {
		t.Fatalf("selection after prepend: cursor=%d actions=%#v", model.historyCursor, actions)
	}
}

func TestPhoneActionsAndDuration(t *testing.T) {
	started := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	snapshot := testSnapshot()
	snapshot.Phone.CallState = app.CallActive
	snapshot.Phone.Peer = "Alice"
	snapshot.Phone.CallStartedAt = started
	snapshot.Now = started.Add(65 * time.Second)

	var actions []Action
	model := New(snapshot, func(action Action) tea.Cmd {
		actions = append(actions, action)
		return nil
	})
	if rendered := model.View(); !strings.Contains(rendered, "01:05") || !strings.Contains(rendered, "Alice") {
		t.Fatalf("active call view missing duration or peer:\n%s", rendered)
	}

	model, _ = updateModel(model, key("m"))
	model, _ = updateModel(model, key("h"))
	if len(actions) != 2 || actions[0].Kind != ActionToggleMute || actions[1].Kind != ActionHangup {
		t.Fatalf("actions = %#v", actions)
	}
}

func TestTypingDoesNotTriggerPhoneHotkeys(t *testing.T) {
	snapshot := testSnapshot()
	snapshot.Phone.CallState = app.CallActive
	var actions []Action
	model := New(snapshot, func(action Action) tea.Cmd {
		actions = append(actions, action)
		return nil
	})

	model, _ = updateModel(model, key("i"))
	model, _ = updateModel(model, runeKey("mqn"))
	if len(actions) != 0 {
		t.Fatalf("typing dispatched actions: %#v", actions)
	}
	if model.quitPending {
		t.Fatal("typing opened quit confirmation")
	}
	if got := model.dial.Value(); got != "mqn" {
		t.Fatalf("dial value = %q, want mqn", got)
	}
}

func TestQuitRequiresConfirmationDuringCall(t *testing.T) {
	snapshot := testSnapshot()
	snapshot.Phone.CallState = app.CallIncoming
	var actions []Action
	model := New(snapshot, func(action Action) tea.Cmd {
		actions = append(actions, action)
		return nil
	})

	var cmd tea.Cmd
	model, cmd = updateModel(model, key("q"))
	if cmd != nil {
		t.Fatal("first q returned a quit command")
	}
	if !model.quitPending || !strings.Contains(model.View(), "A call is in progress. Quit anyway?") {
		t.Fatalf("quit confirmation not shown:\n%s", model.View())
	}

	model, _ = updateModel(model, key("n"))
	if model.quitPending {
		t.Fatal("n did not cancel quit confirmation")
	}
	model, _ = updateModel(model, key("q"))
	model, cmd = updateModel(model, key("y"))
	if cmd == nil {
		t.Fatal("confirmed quit returned no command")
	}
	if len(actions) != 1 || actions[0].Kind != ActionQuit {
		t.Fatalf("quit actions = %#v", actions)
	}
}

func TestPasswordIsAlwaysMasked(t *testing.T) {
	model := New(testSnapshot(), nil)
	model, _ = updateModel(model, key("4"))
	const secret = "s3cr3t-value"
	model.accountFields[4].SetValue(secret)
	model.accountFocus = 4
	_ = model.focusAccountField()

	model, _ = updateModel(model, ErrorMsg{Err: errors.New("invalid password " + secret)})
	rendered := model.View()
	if strings.Contains(rendered, secret) {
		t.Fatalf("password rendered in clear text:\n%s", rendered)
	}
	if !strings.Contains(rendered, "••••") {
		t.Fatalf("masked password not rendered:\n%s", rendered)
	}
}

func TestContactsFuzzySearchAndActions(t *testing.T) {
	var actions []Action
	model := New(testSnapshot(), func(action Action) tea.Cmd {
		actions = append(actions, action)
		return nil
	})
	model, _ = updateModel(model, key("2"))
	model, _ = updateModel(model, key("/"))
	model, _ = updateModel(model, runeKey("bob"))
	if rendered := model.View(); !strings.Contains(rendered, "Bob Support") || strings.Contains(rendered, "Alice") {
		t.Fatalf("fuzzy contact view is wrong:\n%s", rendered)
	}
	model, _ = updateModel(model, key("esc"))
	model, _ = updateModel(model, key("enter"))
	if len(actions) != 1 || actions[0].Kind != ActionDial || actions[0].Target != "sip:bob@example.com" {
		t.Fatalf("dial actions = %#v", actions)
	}

	model, _ = updateModel(model, key("x"))
	if len(actions) != 2 || actions[1].Kind != ActionRemoveContact || actions[1].Target != "sip:bob@example.com" {
		t.Fatalf("remove actions = %#v", actions)
	}
}

func TestContactAddDispatchesTypedValues(t *testing.T) {
	var actions []Action
	model := New(testSnapshot(), func(action Action) tea.Cmd {
		actions = append(actions, action)
		return nil
	})
	model, _ = updateModel(model, key("2"))
	model, _ = updateModel(model, key("a"))
	model, _ = updateModel(model, runeKey("Carol"))
	model, _ = updateModel(model, key("enter"))
	model, _ = updateModel(model, runeKey("sip:carol@example.com"))
	model, _ = updateModel(model, key("enter"))
	if len(actions) != 1 || actions[0].Kind != ActionAddContact {
		t.Fatalf("add actions = %#v", actions)
	}
	if actions[0].Contact != (ContactInput{Name: "Carol", URI: "sip:carol@example.com"}) {
		t.Fatalf("contact payload = %#v", actions[0].Contact)
	}
}

func TestAudioSelectionIncludesDefaultAndDiscoveredNodes(t *testing.T) {
	var actions []Action
	model := New(testSnapshot(), func(action Action) tea.Cmd {
		actions = append(actions, action)
		return nil
	})
	model, _ = updateModel(model, key("3"))
	model, _ = updateModel(model, key("down"))
	model, _ = updateModel(model, key("enter"))
	if len(actions) != 1 || actions[0].Kind != ActionSetAudio || actions[0].Audio != (AudioSelection{Field: AudioOutput, Name: "desk.output"}) {
		t.Fatalf("audio actions = %#v", actions)
	}

	model, _ = updateModel(model, key("up"))
	model, _ = updateModel(model, key("enter"))
	if len(actions) != 2 || actions[1].Audio != (AudioSelection{Field: AudioOutput}) {
		t.Fatalf("default audio action = %#v", actions)
	}
}

// Selecting an output and then an input while the session still publishes the
// old output must not send that old output back.
func TestAudioSelectionSendsOnlyChangedField(t *testing.T) {
	var actions []Action
	model := New(testSnapshot(), func(action Action) tea.Cmd {
		actions = append(actions, action)
		return nil
	})
	model, _ = updateModel(model, key("3"))
	model, _ = updateModel(model, key("down"))
	model, _ = updateModel(model, key("enter"))

	intermediate := testSnapshot()
	model, _ = updateModel(model, SnapshotMsg{Snapshot: intermediate})

	model, _ = updateModel(model, key("tab"))
	model, _ = updateModel(model, key("down"))
	model, _ = updateModel(model, key("enter"))
	want := []AudioSelection{
		{Field: AudioOutput, Name: "desk.output"},
		{Field: AudioInput, Name: "desk.input"},
	}
	if len(actions) != len(want) {
		t.Fatalf("audio actions = %#v", actions)
	}
	for i, action := range actions {
		if action.Kind != ActionSetAudio || action.Audio != want[i] {
			t.Fatalf("audio action %d = %#v, want %#v", i, action.Audio, want[i])
		}
	}
}

func TestAudioCursorSurvivesUnrelatedSnapshots(t *testing.T) {
	model := New(testSnapshot(), nil)
	model, _ = updateModel(model, key("3"))
	model, _ = updateModel(model, key("down"))
	if model.outputCursor != 1 {
		t.Fatalf("output cursor = %d, want 1", model.outputCursor)
	}

	model, _ = updateModel(model, SnapshotMsg{Snapshot: testSnapshot()})
	if model.outputCursor != 1 {
		t.Fatalf("unrelated snapshot moved the output cursor to %d", model.outputCursor)
	}

	selected := testSnapshot()
	selected.Audio.SelectedInput = "desk.input"
	model, _ = updateModel(model, SnapshotMsg{Snapshot: selected})
	if model.inputCursor != 1 || model.outputCursor != 1 {
		t.Fatalf("cursors after input selection = input %d, output %d", model.inputCursor, model.outputCursor)
	}
}

func TestAccountViewStartsInBrowseMode(t *testing.T) {
	var actions []Action
	model := New(testSnapshot(), func(action Action) tea.Cmd {
		actions = append(actions, action)
		return nil
	})
	model, _ = updateModel(model, key("4"))

	model, _ = updateModel(model, runeKey("x"))
	if model.isTyping() || model.accountFields[0].Value() != "pbx.example.com" {
		t.Fatalf("account view started typing: %q", model.accountFields[0].Value())
	}

	model, _ = updateModel(model, key("down"))
	if model.accountFocus != 1 || model.isTyping() {
		t.Fatalf("navigation focused a field: focus=%d typing=%v", model.accountFocus, model.isTyping())
	}

	model.accountFields[1].SetValue("")
	model, _ = updateModel(model, key("enter"))
	if !model.isTyping() {
		t.Fatal("enter did not start editing the selected field")
	}
	model, _ = updateModel(model, runeKey("7"))
	if model.accountFields[1].Value() != "7" {
		t.Fatalf("edited value = %q", model.accountFields[1].Value())
	}
	if !model.accountDirty {
		t.Fatal("edited field did not mark the form dirty")
	}

	model, _ = updateModel(model, key("esc"))
	if model.isTyping() {
		t.Fatal("esc did not return to browse mode")
	}
	model, _ = updateModel(model, key("1"))
	model, _ = updateModel(model, key("4"))
	if model.ActiveView() != ViewAccount || model.isTyping() {
		t.Fatalf("re-entering the account view started typing: view=%v", model.ActiveView())
	}
	if len(actions) != 0 {
		t.Fatalf("unexpected actions = %#v", actions)
	}
}

func TestAccountSaveDispatchesSecretWithoutRenderingIt(t *testing.T) {
	var actions []Action
	model := New(testSnapshot(), func(action Action) tea.Cmd {
		actions = append(actions, action)
		return nil
	})
	model, _ = updateModel(model, key("4"))
	const secret = "new-secret"
	model.accountFields[4].SetValue(secret)
	model.accountFocus = 6
	_ = model.focusAccountField()
	model, _ = updateModel(model, key("enter"))
	if len(actions) != 1 || actions[0].Kind != ActionSaveAccount || actions[0].Account.Password != secret {
		t.Fatalf("account actions = %#v", actions)
	}
	model, _ = updateModel(model, ErrorMsg{Err: errors.New("rejected " + secret)})
	if rendered := model.View(); strings.Contains(rendered, secret) {
		t.Fatalf("submitted password leaked after an error:\n%s", rendered)
	}
}

func TestSnapshotUpdateErrorAndResizeMessages(t *testing.T) {
	model := New(Snapshot{}, nil)
	phone := PhoneSnapshot{Registration: app.RegistrationRegistered, Registered: true, CallState: app.CallOutgoing, Peer: "Support"}
	now := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	model, _ = updateModel(model, UpdateMsg{Phone: &phone, Now: &now})
	if model.Snapshot().Phone.Peer != "Support" || !model.Snapshot().Now.Equal(now) {
		t.Fatalf("updated snapshot = %#v", model.Snapshot())
	}

	model, _ = updateModel(model, ErrorMsg{Err: errors.New("adapter failed")})
	if !strings.Contains(model.View(), "adapter failed") {
		t.Fatalf("error is missing:\n%s", model.View())
	}
	model, _ = updateModel(model, tea.WindowSizeMsg{Width: 30, Height: 8})
	if !strings.Contains(model.View(), "Terminal too small") {
		t.Fatalf("small-terminal fallback is missing:\n%s", model.View())
	}
}

func testSnapshot() Snapshot {
	return Snapshot{
		Phone: PhoneSnapshot{
			Registered:         true,
			Registration:       app.RegistrationRegistered,
			RegistrationDetail: "Registered as 101@example.com",
			CallState:          app.CallIdle,
		},
		Contacts: ContactsSnapshot{Contacts: []ContactSnapshot{
			{Name: "Alice", URI: "sip:alice@example.com"},
			{Name: "Bob Support", URI: "sip:bob@example.com"},
		}},
		Audio: AudioSnapshot{
			Outputs: []AudioNodeSnapshot{{Name: "desk.output", Description: "Desk speakers", Default: true}},
			Inputs:  []AudioNodeSnapshot{{Name: "desk.input", Description: "Desk microphone", Default: true}},
		},
		Account: AccountSnapshot{
			Configured:  true,
			Server:      "pbx.example.com",
			Username:    "101",
			Domain:      "example.com",
			Login:       "101",
			HasPassword: true,
			Secure:      true,
		},
		History: CallHistorySnapshot{Calls: []app.CallHistoryEntry{
			{
				Direction:   app.CallDirectionIncoming,
				Outcome:     app.CallOutcomeConnected,
				Peer:        "Alice",
				Target:      "sip:alice@example.com",
				StartedAt:   time.Date(2026, 1, 2, 3, 3, 0, 0, time.Local),
				ConnectedAt: time.Date(2026, 1, 2, 3, 4, 0, 0, time.Local),
				EndedAt:     time.Date(2026, 1, 2, 3, 5, 5, 0, time.Local),
			},
			{
				Direction: app.CallDirectionOutgoing,
				Outcome:   app.CallOutcomeNotConnected,
				Peer:      "Bob",
				Target:    "sip:bob@example.com",
				StartedAt: time.Date(2026, 1, 1, 9, 0, 0, 0, time.Local),
				EndedAt:   time.Date(2026, 1, 1, 9, 0, 5, 0, time.Local),
			},
		}},
		Now: time.Date(2026, 1, 2, 12, 0, 0, 0, time.Local),
	}
}

func updateModel(model Model, msg tea.Msg) (Model, tea.Cmd) {
	updated, cmd := model.Update(msg)
	return updated.(Model), cmd
}

func key(value string) tea.KeyMsg {
	switch value {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	default:
		return runeKey(value)
	}
}

func runeKey(value string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(value)}
}
