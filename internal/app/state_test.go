package app

import (
	"errors"
	"testing"
	"time"
)

func TestReducerIncomingCallAndMissedNotification(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	state := NewState()
	incoming := Reduce(state, CallEvent{
		Type:            CallEventIncoming,
		ID:              "call-1",
		PeerURI:         "sip:201@pbx",
		PeerDisplayName: "Provider",
		Contacts:        []Contact{testContact("sip:201@pbx")},
		At:              at,
	})
	if incoming.State.CallState != CallIncoming || incoming.State.CallID != "call-1" || incoming.State.Peer != "Anna" {
		t.Fatalf("incoming state = %#v", incoming.State)
	}
	if !incoming.State.CallStateChangedAt.Equal(at) {
		t.Fatalf("state change time = %v, want %v", incoming.State.CallStateChangedAt, at)
	}
	assertNotifications(t, incoming.Notifications, Notification{Kind: NotificationIncoming, Body: "Anna"})

	closedAt := at.Add(10 * time.Second)
	closed := Reduce(incoming.State, CallEvent{Type: CallEventClosed, ID: "call-1", At: closedAt})
	if closed.State.CallState != CallIdle || closed.State.Peer != "" || closed.State.LastCallID != "call-1" {
		t.Fatalf("closed state = %#v", closed.State)
	}
	if !closed.State.CallEndedAt.Equal(closedAt) {
		t.Fatalf("end time = %v, want %v", closed.State.CallEndedAt, closedAt)
	}
	assertNotifications(t, closed.Notifications, Notification{Kind: NotificationMissed, Body: "Anna"})
	assertHistory(t, closed.History, CallHistoryEntry{
		Direction: CallDirectionIncoming,
		Outcome:   CallOutcomeMissed,
		Peer:      "Anna",
		Target:    "sip:201@pbx",
		StartedAt: at,
		EndedAt:   closedAt,
	})
}

func TestReducerRejectsIncomingCallWhenDNDIsOn(t *testing.T) {
	state := NewState()
	state.DND = true
	result := Reduce(state, CallEvent{
		Type:     CallEventIncoming,
		ID:       "call-dnd",
		PeerURI:  "sip:201@pbx",
		Contacts: []Contact{testContact("sip:201@pbx")},
	})
	if result.State.CallState != CallIdle {
		t.Fatalf("DND changed call state to %q", result.State.CallState)
	}
	assertCommands(t, result.Commands, Command{Kind: CommandHangup})
	assertNotifications(t, result.Notifications, Notification{Kind: NotificationRejectedDND, Body: "Anna"})
	assertHistory(t, result.History, CallHistoryEntry{
		Direction: CallDirectionIncoming,
		Outcome:   CallOutcomeRejectedDND,
		Peer:      "Anna",
		Target:    "sip:201@pbx",
	})
}

func TestReducerTracksRejectedCallIDsWithoutClosingCurrentCall(t *testing.T) {
	at := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	state := NewState()
	state.CallState = CallActive
	state.CallID = "current"
	state.CallDirection = CallDirectionIncoming
	state.CallTarget = "sip:201@pbx"
	state.Peer = "Anna"
	state.CallCreatedAt = at
	state.CallStartedAt = at
	state.DND = true

	rejected := Reduce(state, CallEvent{Type: CallEventIncoming, ID: "rejected", PeerURI: "sip:202@pbx", At: at.Add(time.Second)})
	assertCommands(t, rejected.Commands, Command{Kind: CommandHangup, Parameter: "rejected"})
	if rejected.State.CallState != CallActive || len(rejected.History) != 1 || rejected.History[0].Outcome != CallOutcomeRejectedDND {
		t.Fatalf("DND while active result = %#v", rejected)
	}
	duplicate := Reduce(rejected.State, CallEvent{Type: CallEventIncoming, ID: "rejected", PeerURI: "sip:202@pbx", At: at.Add(2 * time.Second)})
	if len(duplicate.Commands) != 0 || len(duplicate.History) != 0 {
		t.Fatalf("duplicate rejected call was handled again: %#v", duplicate)
	}

	closed := Reduce(rejected.State, CallEvent{Type: CallEventClosed, ID: "current", At: at.Add(3 * time.Second)})
	closed.State.Registered = true
	closed.State.DND = false
	outgoing := Reduce(closed.State, ActionEvent{Type: ActionDial, Target: "203", At: at.Add(4 * time.Second)})
	stale := Reduce(outgoing.State, CallEvent{Type: CallEventClosed, ID: "rejected", At: at.Add(5 * time.Second)})
	if stale.State.CallState != CallOutgoing || len(stale.History) != 0 {
		t.Fatalf("stale rejected close changed outgoing call: %#v", stale)
	}
}

func TestReducerRejectsSecondCallWithoutChangingActiveCall(t *testing.T) {
	started := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	state := NewState()
	state.CallState = CallActive
	state.CallID = "current"
	state.Peer = "Anna"
	state.Muted = true
	state.CallStartedAt = started

	result := Reduce(state, CallEvent{
		Type:     CallEventIncoming,
		ID:       "second",
		PeerURI:  "sip:202@pbx",
		Contacts: []Contact{testContact("sip:202@pbx", "Bob")},
	})
	if result.State.CallState != state.CallState || result.State.CallID != state.CallID || result.State.Peer != state.Peer || result.State.Muted != state.Muted {
		t.Fatalf("busy event changed active call: got %#v, want %#v", result.State, state)
	}
	assertCommands(t, result.Commands, Command{Kind: CommandHangup, Parameter: "second"})
	assertNotifications(t, result.Notifications, Notification{Kind: NotificationRejectedBusy, Body: "Bob"})
	assertHistory(t, result.History, CallHistoryEntry{
		Direction: CallDirectionIncoming,
		Outcome:   CallOutcomeRejectedBusy,
		Peer:      "Bob",
		Target:    "sip:202@pbx",
	})
}

func TestReducerEstablishesAndClosesCall(t *testing.T) {
	incomingAt := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	establishedAt := incomingAt.Add(2 * time.Second)
	closedAt := establishedAt.Add(time.Minute)

	incoming := Reduce(NewState(), CallEvent{Type: CallEventIncoming, ID: "call-1", PeerURI: "sip:201@pbx", At: incomingAt})
	established := Reduce(incoming.State, CallEvent{Type: CallEventEstablished, ID: "call-1", At: establishedAt})
	if established.State.CallState != CallActive || established.State.Muted {
		t.Fatalf("established state = %#v", established.State)
	}
	if !established.State.CallStartedAt.Equal(establishedAt) || !established.State.CallStateChangedAt.Equal(establishedAt) {
		t.Fatalf("established timestamps = %#v", established.State)
	}

	muted := Reduce(established.State, ActionEvent{Type: ActionToggleMute})
	if !muted.State.Muted {
		t.Fatal("mute action did not set Muted")
	}
	assertCommands(t, muted.Commands, Command{Kind: CommandMute})

	closed := Reduce(muted.State, CallEvent{Type: CallEventClosed, ID: "call-1", At: closedAt})
	if closed.State.CallState != CallIdle || closed.State.Muted || closed.State.Peer != "" {
		t.Fatalf("closed state = %#v", closed.State)
	}
	if !closed.State.CallStartedAt.Equal(establishedAt) || !closed.State.CallEndedAt.Equal(closedAt) {
		t.Fatalf("closed timestamps = %#v", closed.State)
	}
	if len(closed.Notifications) != 0 {
		t.Fatalf("active close notifications = %#v, want none", closed.Notifications)
	}
	assertHistory(t, closed.History, CallHistoryEntry{
		Direction:   CallDirectionIncoming,
		Outcome:     CallOutcomeConnected,
		Peer:        "201",
		Target:      "sip:201@pbx",
		StartedAt:   incomingAt,
		ConnectedAt: establishedAt,
		EndedAt:     closedAt,
	})
}

func TestReducerRecordsRejectedAndCanceledCalls(t *testing.T) {
	at := time.Date(2026, 4, 5, 6, 7, 8, 0, time.UTC)

	incoming := Reduce(NewState(), CallEvent{Type: CallEventIncoming, ID: "incoming", PeerURI: "sip:201@pbx", At: at})
	reject := Reduce(incoming.State, ActionEvent{Type: ActionHangup, At: at.Add(time.Second)})
	closed := Reduce(reject.State, CallEvent{Type: CallEventClosed, ID: "incoming", At: at.Add(2 * time.Second)})
	if len(closed.History) != 1 || closed.History[0].Outcome != CallOutcomeRejected {
		t.Fatalf("rejected history = %#v", closed.History)
	}

	state := NewState()
	state.Registered = true
	outgoing := Reduce(state, ActionEvent{Type: ActionDial, Target: "202", At: at})
	cancel := Reduce(outgoing.State, ActionEvent{Type: ActionHangup, At: at.Add(time.Second)})
	closed = Reduce(cancel.State, CallEvent{Type: CallEventClosed, At: at.Add(2 * time.Second)})
	if len(closed.History) != 1 || closed.History[0].Outcome != CallOutcomeCanceled || closed.History[0].Target != "202" {
		t.Fatalf("canceled history = %#v", closed.History)
	}
}

func TestReducerIgnoresOutOfOrderAndStaleCallEvents(t *testing.T) {
	state := NewState()
	state.CallState = CallActive
	state.CallID = "current"
	state.Peer = "Anna"

	wrongClose := Reduce(state, CallEvent{Type: CallEventClosed, ID: "stale"})
	if wrongClose.State != state {
		t.Fatalf("wrong close changed state: %#v", wrongClose.State)
	}
	wrongEstablished := Reduce(state, CallEvent{Type: CallEventEstablished, ID: "stale"})
	if wrongEstablished.State != state {
		t.Fatalf("wrong established changed state: %#v", wrongEstablished.State)
	}

	closed := Reduce(state, CallEvent{Type: CallEventClosed, ID: "current"})
	lateEstablished := Reduce(closed.State, CallEvent{Type: CallEventEstablished, ID: "current"})
	if lateEstablished.State.CallState != CallIdle {
		t.Fatalf("late established changed state to %q", lateEstablished.State.CallState)
	}
	duplicateIncoming := Reduce(closed.State, CallEvent{Type: CallEventIncoming, ID: "current", PeerURI: "sip:201@pbx"})
	if duplicateIncoming.State.CallState != CallIdle || len(duplicateIncoming.Commands) != 0 || len(duplicateIncoming.Notifications) != 0 {
		t.Fatalf("duplicate incoming was not ignored: %#v", duplicateIncoming)
	}
}

func TestReducerDialPreservesOneCallAndReturnsCommand(t *testing.T) {
	state := NewState()
	state.Registered = true
	result := Reduce(state, ActionEvent{Type: ActionDial, Target: "alice@example.com"})
	if result.Err != nil || result.State.CallState != CallOutgoing || result.State.Peer != "alice" {
		t.Fatalf("dial result = %#v", result)
	}
	assertCommands(t, result.Commands, Command{Kind: CommandDial, Parameter: "sip:alice@example.com"})

	second := Reduce(result.State, ActionEvent{Type: ActionDial, Target: "202"})
	if !errors.Is(second.Err, ErrCallInProgress) || len(second.Commands) != 0 {
		t.Fatalf("second dial result = %#v", second)
	}

	unregistered := NewState()
	if result := Reduce(unregistered, ActionEvent{Type: ActionDial, Target: "201"}); !errors.Is(result.Err, ErrNotRegistered) {
		t.Fatalf("unregistered dial error = %v", result.Err)
	}
	if result := Reduce(state, ActionEvent{Type: ActionDial, Target: "not a target"}); !errors.Is(result.Err, ErrEmptyTarget) {
		t.Fatalf("invalid dial error = %v", result.Err)
	}
}

func TestReducerDialResolvesContactName(t *testing.T) {
	at := time.Date(2026, 6, 7, 8, 9, 10, 0, time.UTC)
	state := NewState()
	state.Registered = true
	contacts := []Contact{testContact("sip:alice@example.com", "Alice")}

	dial := Reduce(state, ActionEvent{Type: ActionDial, Target: "sip:alice@example.com", Contacts: contacts, At: at})
	if dial.State.Peer != "Alice" {
		t.Fatalf("dial peer = %q, want Alice", dial.State.Peer)
	}
	closed := Reduce(dial.State, CallEvent{Type: CallEventClosed, At: at.Add(time.Second)})
	if len(closed.History) != 1 || closed.History[0].Peer != "Alice" || closed.History[0].Target != "sip:alice@example.com" {
		t.Fatalf("dial history = %#v", closed.History)
	}
}

func TestReducerRegistrationEventsAndSnapshot(t *testing.T) {
	registered := Reduce(NewState(), RegisterEvent{Type: RegisterEventOK, AccountAOR: "sips:201@example.com"})
	if !registered.State.Registered || registered.State.Registration != RegistrationRegistered || registered.State.RegistrationDetail != "201@example.com" {
		t.Fatalf("registered state = %#v", registered.State)
	}
	failed := Reduce(registered.State, RegisterEvent{Type: RegisterEventFail, Detail: "timeout\nretry"})
	if failed.State.Registered || failed.State.Registration != RegistrationFailed || failed.State.RegistrationDetail != "registration failed: timeout retry" {
		t.Fatalf("failed state = %#v", failed.State)
	}
	unregistered := Reduce(failed.State, RegisterEvent{Type: RegisterEventUnregistering})
	if unregistered.State.Registered || unregistered.State.Registration != RegistrationUnregistered {
		t.Fatalf("unregistered state = %#v", unregistered.State)
	}

	snapshot := Reduce(NewState(), RegistrationSnapshot{Info: RegistrationInfo{Known: true, Count: 1, Registered: true, AOR: "201@example.com"}})
	if !snapshot.State.Registered || snapshot.State.RegistrationDetail != "201@example.com" {
		t.Fatalf("snapshot state = %#v", snapshot.State)
	}
}

func assertCommands(t *testing.T, got []Command, want ...Command) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("commands = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("commands[%d] = %#v, want %#v", i, got[i], want[i])
		}
	}
}

func assertHistory(t *testing.T, got []CallHistoryEntry, want ...CallHistoryEntry) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("history = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("history[%d] = %#v, want %#v", i, got[i], want[i])
		}
	}
}

func assertNotifications(t *testing.T, got []Notification, want ...Notification) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("notifications = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("notifications[%d] = %#v, want %#v", i, got[i], want[i])
		}
	}
}
