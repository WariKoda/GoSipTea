package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/nibra/gosiptea/internal/app"
)

// View identifies one of the application's top-level screens.
type View int

const (
	ViewPhone View = iota
	ViewContacts
	ViewAudio
	ViewAccount
	ViewHistory
)

// PhoneSnapshot contains the call and registration state needed for display.
type PhoneSnapshot struct {
	Registered         bool
	Registration       app.RegistrationState
	RegistrationDetail string
	CallState          app.CallState
	Peer               string
	Muted              bool
	DND                bool
	CallStartedAt      time.Time
}

// PhoneSnapshotFromState maps domain state without adding adapter behavior to the model.
func PhoneSnapshotFromState(state app.State) PhoneSnapshot {
	return PhoneSnapshot{
		Registered:         state.Registered,
		Registration:       state.Registration,
		RegistrationDetail: state.RegistrationDetail,
		CallState:          state.CallState,
		Peer:               state.Peer,
		Muted:              state.Muted,
		DND:                state.DND,
		CallStartedAt:      state.CallStartedAt,
	}
}

// ContactSnapshot is a contact row shown by the contacts view.
type ContactSnapshot struct {
	Name string
	URI  string
}

// ContactsSnapshot is the latest contact list supplied by the parent.
type ContactsSnapshot struct {
	Contacts []ContactSnapshot
}

// AudioNodeSnapshot describes one discovered input or output node.
type AudioNodeSnapshot struct {
	Name        string
	Description string
	Default     bool
}

// AudioSnapshot contains discovered nodes and the saved selection. Empty selections mean system default.
type AudioSnapshot struct {
	Inputs         []AudioNodeSnapshot
	Outputs        []AudioNodeSnapshot
	SelectedInput  string
	SelectedOutput string
}

// AccountSnapshot intentionally contains no password value.
type AccountSnapshot struct {
	Configured  bool
	Server      string
	Username    string
	Domain      string
	Login       string
	HasPassword bool
	Secure      bool
}

// CallHistorySnapshot contains completed call attempts in newest-first order.
type CallHistorySnapshot struct {
	Calls []app.CallHistoryEntry
}

// Snapshot is the complete presentation state. Now makes call duration rendering deterministic.
type Snapshot struct {
	Phone    PhoneSnapshot
	Contacts ContactsSnapshot
	Audio    AudioSnapshot
	Account  AccountSnapshot
	History  CallHistorySnapshot
	Now      time.Time
}

// SnapshotMsg replaces all presentation state.
type SnapshotMsg struct {
	Snapshot Snapshot
}

// UpdateMsg patches selected presentation state. Nil fields remain unchanged.
type UpdateMsg struct {
	Phone    *PhoneSnapshot
	Contacts *ContactsSnapshot
	Audio    *AudioSnapshot
	Account  *AccountSnapshot
	History  *CallHistorySnapshot
	Now      *time.Time
}

// ErrorMsg displays an adapter or domain error without coupling the model to its source.
type ErrorMsg struct {
	Err error
}

// TickMsg advances presentation time. A parent may send it instead of running Model.Init.
type TickMsg struct {
	Now time.Time
}

// ActionKind identifies a request for the parent adapter.
type ActionKind string

const (
	ActionDial          ActionKind = "dial"
	ActionAnswer        ActionKind = "answer"
	ActionReject        ActionKind = "reject"
	ActionHangup        ActionKind = "hangup"
	ActionToggleMute    ActionKind = "toggle_mute"
	ActionToggleDND     ActionKind = "toggle_dnd"
	ActionAddContact    ActionKind = "add_contact"
	ActionRemoveContact ActionKind = "remove_contact"
	ActionSetAudio      ActionKind = "set_audio"
	ActionSaveAccount   ActionKind = "save_account"
	ActionQuit          ActionKind = "quit"
)

// ContactInput is the payload for adding a contact.
type ContactInput struct {
	Name string
	URI  string
}

// AudioSelection uses an empty node name for the system default.
type AudioSelection struct {
	Input  string
	Output string
}

// AccountInput contains account form values. Password is write-only and must not be echoed by adapters.
type AccountInput struct {
	Server   string
	Username string
	Domain   string
	Login    string
	Password string
	Secure   bool
}

// Action is emitted by the model. Only fields relevant to Kind are populated.
type Action struct {
	Kind    ActionKind
	Target  string
	Contact ContactInput
	Audio   AudioSelection
	Account AccountInput
}

// ActionMsg names Action for integrations that use Bubble Tea message naming.
type ActionMsg = Action

// Dispatch translates a presentation action into work owned by the parent.
type Dispatch func(Action) tea.Cmd
