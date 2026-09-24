package app

import (
	"errors"
	"strings"
	"time"
)

// CallState is the one-call state machine state.
type CallState string

const (
	CallIdle     CallState = "idle"
	CallIncoming CallState = "incoming"
	CallOutgoing CallState = "outgoing"
	CallActive   CallState = "active"
)

// CallDirection identifies which side initiated a call.
type CallDirection string

const (
	CallDirectionIncoming CallDirection = "incoming"
	CallDirectionOutgoing CallDirection = "outgoing"
)

// CallOutcome describes how a call attempt ended.
type CallOutcome string

const (
	CallOutcomeConnected    CallOutcome = "connected"
	CallOutcomeMissed       CallOutcome = "missed"
	CallOutcomeRejected     CallOutcome = "rejected"
	CallOutcomeRejectedDND  CallOutcome = "rejected_dnd"
	CallOutcomeRejectedBusy CallOutcome = "rejected_busy"
	CallOutcomeNotConnected CallOutcome = "not_connected"
	CallOutcomeCanceled     CallOutcome = "canceled"
)

// CallHistoryEntry is one completed call attempt. Target retains the value
// needed for redialing while Peer is the display value captured at call time.
type CallHistoryEntry struct {
	Direction   CallDirection `json:"direction"`
	Outcome     CallOutcome   `json:"outcome"`
	Peer        string        `json:"peer"`
	Target      string        `json:"target"`
	StartedAt   time.Time     `json:"started_at"`
	ConnectedAt time.Time     `json:"connected_at"`
	EndedAt     time.Time     `json:"ended_at"`
}

// RegistrationState describes the latest known account registration state.
type RegistrationState string

const (
	RegistrationUnknown      RegistrationState = "unknown"
	RegistrationRegistering  RegistrationState = "registering"
	RegistrationRegistered   RegistrationState = "registered"
	RegistrationFailed       RegistrationState = "failed"
	RegistrationUnregistered RegistrationState = "unregistered"
)

// State contains domain state only. The timestamps come from events, which
// keeps replay and tests deterministic.
type State struct {
	CallState     CallState
	CallID        string
	CallDirection CallDirection
	CallTarget    string
	Peer          string
	Muted         bool
	DND           bool
	EndRequested  bool

	Registered         bool
	Registration       RegistrationState
	RegistrationDetail string

	CallCreatedAt      time.Time
	CallStartedAt      time.Time
	CallEndedAt        time.Time
	CallStateChangedAt time.Time
	LastCallID         string
	IgnoredCallIDs     [8]string
	ignoredCallIndex   uint8
}

// NewState returns the canonical zero-activity state.
func NewState() State {
	return State{CallState: CallIdle, Registration: RegistrationUnknown}
}

// CallEventType uses the event names emitted by baresip.
type CallEventType string

const (
	CallEventIncoming    CallEventType = "CALL_INCOMING"
	CallEventEstablished CallEventType = "CALL_ESTABLISHED"
	CallEventClosed      CallEventType = "CALL_CLOSED"
)

// RegisterEventType uses the event names emitted by baresip.
type RegisterEventType string

const (
	RegisterEventOK            RegisterEventType = "REGISTER_OK"
	RegisterEventFail          RegisterEventType = "REGISTER_FAIL"
	RegisterEventUnregistering RegisterEventType = "UNREGISTERING"
)

// ActionType identifies a user request handled by the reducer.
type ActionType string

const (
	ActionDial       ActionType = "dial"
	ActionAnswer     ActionType = "answer"
	ActionHangup     ActionType = "hangup"
	ActionToggleMute ActionType = "toggle_mute"
	ActionToggleDND  ActionType = "toggle_dnd"
)

// Event is accepted by Reduce. All event implementations are immutable values.
type Event interface {
	domainEvent()
}

// ActionEvent represents a request from the UI or IPC layer. Contacts and
// country code are supplied on dial actions so peer resolution remains pure.
type ActionEvent struct {
	Type               ActionType
	Target             string
	Contacts           []Contact
	CountryCallingCode string
	At                 time.Time
}

func (ActionEvent) domainEvent() {}

// CallEvent represents a baresip call event. Contacts and country code are
// supplied on incoming events so caller resolution remains pure.
type CallEvent struct {
	Type               CallEventType
	ID                 string
	PeerURI            string
	PeerDisplayName    string
	Contacts           []Contact
	CountryCallingCode string
	At                 time.Time
}

func (CallEvent) domainEvent() {}

// RegisterEvent represents a baresip register event.
type RegisterEvent struct {
	Type       RegisterEventType
	AccountAOR string
	Detail     string
	At         time.Time
}

func (RegisterEvent) domainEvent() {}

// RegistrationSnapshot applies a parsed reginfo response to state.
type RegistrationSnapshot struct {
	Info RegistrationInfo
	At   time.Time
}

func (RegistrationSnapshot) domainEvent() {}

// CommandKind names a side effect for the baresip adapter.
type CommandKind string

const (
	CommandDial   CommandKind = "dial"
	CommandAccept CommandKind = "accept"
	CommandHangup CommandKind = "hangup"
	CommandReject CommandKind = "reject"
	CommandMute   CommandKind = "mute"
)

// Command is returned by Reduce and never executed by this package.
type Command struct {
	Kind      CommandKind
	Parameter string
}

// NotificationKind lets the UI translate summaries without parsing text.
type NotificationKind string

const (
	NotificationIncoming     NotificationKind = "incoming_call"
	NotificationMissed       NotificationKind = "missed_call"
	NotificationRejectedDND  NotificationKind = "call_rejected_dnd"
	NotificationRejectedBusy NotificationKind = "call_rejected_busy"
)

// Notification is returned by Reduce and never sent by this package.
type Notification struct {
	Kind NotificationKind
	Body string
}

var (
	ErrEmptyTarget    = errors.New("empty target")
	ErrCallInProgress = errors.New("a call is already in progress")
	ErrNotRegistered  = errors.New("not registered to the server")
)

// Transition is the complete result of reducing one event.
type Transition struct {
	State         State
	Commands      []Command
	Notifications []Notification
	History       []CallHistoryEntry
	Err           error
}

// Reduce applies one action or baresip event. It does no I/O.
func Reduce(current State, event Event) Transition {
	if current.CallState == "" {
		current.CallState = CallIdle
	}
	if current.Registration == "" {
		current.Registration = RegistrationUnknown
	}

	result := Transition{State: current}
	switch typed := event.(type) {
	case ActionEvent:
		reduceAction(&result, typed)
	case *ActionEvent:
		if typed != nil {
			reduceAction(&result, *typed)
		}
	case CallEvent:
		reduceCall(&result, typed)
	case *CallEvent:
		if typed != nil {
			reduceCall(&result, *typed)
		}
	case RegisterEvent:
		reduceRegister(&result, typed)
	case *RegisterEvent:
		if typed != nil {
			reduceRegister(&result, *typed)
		}
	case RegistrationSnapshot:
		reduceRegistrationSnapshot(&result, typed)
	case *RegistrationSnapshot:
		if typed != nil {
			reduceRegistrationSnapshot(&result, *typed)
		}
	}
	return result
}

func reduceAction(result *Transition, event ActionEvent) {
	switch event.Type {
	case ActionDial:
		target := NormalizeDialTarget(event.Target)
		if target == "" {
			result.Err = ErrEmptyTarget
			return
		}
		if result.State.CallState != CallIdle {
			result.Err = ErrCallInProgress
			return
		}
		if !result.State.Registered {
			result.Err = ErrNotRegistered
			return
		}
		peer, ok := ContactName(event.Contacts, target, event.CountryCallingCode)
		if !ok {
			peer = PeerDisplay(target)
		}
		beginCall(&result.State, CallOutgoing, CallDirectionOutgoing, "", peer, target, event.At)
		result.Commands = append(result.Commands, Command{Kind: CommandDial, Parameter: target})
	case ActionAnswer:
		if result.State.CallState == CallIncoming {
			result.Commands = append(result.Commands, Command{Kind: CommandAccept})
		}
	case ActionHangup:
		if result.State.CallState == CallIncoming {
			result.State.EndRequested = true
			result.Commands = append(result.Commands, Command{Kind: CommandReject})
		} else if result.State.CallState != CallIdle {
			result.State.EndRequested = true
			result.Commands = append(result.Commands, Command{Kind: CommandHangup})
		}
	case ActionToggleMute:
		if result.State.CallState == CallActive {
			result.State.Muted = !result.State.Muted
			result.Commands = append(result.Commands, Command{Kind: CommandMute})
		}
	case ActionToggleDND:
		result.State.DND = !result.State.DND
	}
}

func reduceCall(result *Transition, event CallEvent) {
	if event.ID != "" && callIDWasIgnored(result.State, event.ID) {
		return
	}
	switch event.Type {
	case CallEventIncoming:
		reduceIncoming(result, event)
	case CallEventEstablished:
		reduceEstablished(result, event)
	case CallEventClosed:
		reduceClosed(result, event)
	}
}

func reduceIncoming(result *Transition, event CallEvent) {
	who := CallerName(event.Contacts, event.PeerURI, event.PeerDisplayName, event.CountryCallingCode)
	if event.ID != "" && event.ID == result.State.LastCallID && result.State.CallState == CallIdle {
		return
	}
	if result.State.DND {
		parameter := ""
		if result.State.CallState != CallIdle {
			parameter = event.ID
		}
		result.Commands = append(result.Commands, Command{Kind: CommandHangup, Parameter: parameter})
		result.Notifications = append(result.Notifications, Notification{Kind: NotificationRejectedDND, Body: who})
		result.History = append(result.History, immediateIncomingHistory(event, who, CallOutcomeRejectedDND))
		rememberIgnoredCall(&result.State, event.ID)
		return
	}
	if result.State.CallState != CallIdle {
		if event.ID != "" && event.ID == result.State.CallID {
			return
		}
		if event.ID != "" {
			result.Commands = append(result.Commands, Command{Kind: CommandHangup, Parameter: event.ID})
		}
		result.Notifications = append(result.Notifications, Notification{Kind: NotificationRejectedBusy, Body: who})
		result.History = append(result.History, immediateIncomingHistory(event, who, CallOutcomeRejectedBusy))
		rememberIgnoredCall(&result.State, event.ID)
		return
	}

	beginCall(&result.State, CallIncoming, CallDirectionIncoming, event.ID, who, event.PeerURI, event.At)
	result.Notifications = append(result.Notifications, Notification{Kind: NotificationIncoming, Body: who})
}

func reduceEstablished(result *Transition, event CallEvent) {
	if result.State.CallState != CallIncoming && result.State.CallState != CallOutgoing {
		return
	}
	if !eventMatchesCurrentCall(result.State, event.ID) {
		return
	}
	if event.ID != "" && result.State.CallID == "" {
		result.State.CallID = event.ID
	}
	if result.State.Peer == "" && event.PeerURI != "" {
		result.State.Peer = PeerDisplay(event.PeerURI)
	}
	result.State.CallState = CallActive
	result.State.Muted = false
	result.State.CallStartedAt = event.At
	result.State.CallStateChangedAt = event.At
}

func reduceClosed(result *Transition, event CallEvent) {
	if result.State.CallState == CallIdle || !eventMatchesCurrentCall(result.State, event.ID) {
		return
	}
	previous := result.State
	closedID := previous.CallID
	if closedID == "" {
		closedID = event.ID
	}

	outcome := CallOutcomeConnected
	switch previous.CallState {
	case CallIncoming:
		if previous.EndRequested {
			outcome = CallOutcomeRejected
		} else {
			outcome = CallOutcomeMissed
			result.Notifications = append(result.Notifications, Notification{Kind: NotificationMissed, Body: previous.Peer})
		}
	case CallOutgoing:
		if previous.EndRequested {
			outcome = CallOutcomeCanceled
		} else {
			outcome = CallOutcomeNotConnected
		}
	}
	result.History = append(result.History, CallHistoryEntry{
		Direction:   previous.CallDirection,
		Outcome:     outcome,
		Peer:        previous.Peer,
		Target:      previous.CallTarget,
		StartedAt:   previous.CallCreatedAt,
		ConnectedAt: previous.CallStartedAt,
		EndedAt:     event.At,
	})

	result.State.CallState = CallIdle
	result.State.CallID = ""
	result.State.CallDirection = ""
	result.State.CallTarget = ""
	result.State.Peer = ""
	result.State.Muted = false
	result.State.EndRequested = false
	result.State.CallCreatedAt = time.Time{}
	result.State.CallEndedAt = event.At
	result.State.CallStateChangedAt = event.At
	result.State.LastCallID = closedID
}

func reduceRegister(result *Transition, event RegisterEvent) {
	switch event.Type {
	case RegisterEventOK:
		result.State.Registered = true
		result.State.Registration = RegistrationRegistered
		detail := stripSIPScheme(event.AccountAOR)
		if detail == "" {
			detail = "registered"
		}
		result.State.RegistrationDetail = ClampText(detail, MaxRegistrationAOR)
	case RegisterEventFail:
		result.State.Registered = false
		result.State.Registration = RegistrationFailed
		detail := strings.TrimSpace(event.Detail)
		if detail == "" {
			result.State.RegistrationDetail = "registration failed"
		} else {
			result.State.RegistrationDetail = ClampText("registration failed: "+detail, MaxCommandError)
		}
	case RegisterEventUnregistering:
		result.State.Registered = false
		result.State.Registration = RegistrationUnregistered
		result.State.RegistrationDetail = "unregistered"
	}
}

func reduceRegistrationSnapshot(result *Transition, event RegistrationSnapshot) {
	info := event.Info
	if !info.Known {
		return
	}
	switch {
	case info.Count == 0:
		result.State.Registered = false
		result.State.Registration = RegistrationUnregistered
		result.State.RegistrationDetail = "unregistered"
	case info.Registered:
		result.State.Registered = true
		result.State.Registration = RegistrationRegistered
		result.State.RegistrationDetail = ClampText(info.AOR, MaxRegistrationAOR)
		if result.State.RegistrationDetail == "" {
			result.State.RegistrationDetail = "registered"
		}
	case info.Failed:
		result.State.Registered = false
		result.State.Registration = RegistrationFailed
		result.State.RegistrationDetail = "registration failed"
	default:
		result.State.Registered = false
		result.State.Registration = RegistrationRegistering
		result.State.RegistrationDetail = "registering"
	}
}

func beginCall(state *State, callState CallState, direction CallDirection, id, peer, target string, at time.Time) {
	state.CallState = callState
	state.CallID = id
	state.CallDirection = direction
	state.CallTarget = target
	state.Peer = peer
	state.Muted = false
	state.EndRequested = false
	state.CallCreatedAt = at
	state.CallStartedAt = time.Time{}
	state.CallEndedAt = time.Time{}
	state.CallStateChangedAt = at
}

func immediateIncomingHistory(event CallEvent, peer string, outcome CallOutcome) CallHistoryEntry {
	return CallHistoryEntry{
		Direction: CallDirectionIncoming,
		Outcome:   outcome,
		Peer:      peer,
		Target:    event.PeerURI,
		StartedAt: event.At,
		EndedAt:   event.At,
	}
}

func rememberIgnoredCall(state *State, callID string) {
	if callID == "" {
		return
	}
	state.IgnoredCallIDs[state.ignoredCallIndex%uint8(len(state.IgnoredCallIDs))] = callID
	state.ignoredCallIndex++
}

func callIDWasIgnored(state State, callID string) bool {
	for _, ignored := range state.IgnoredCallIDs {
		if ignored != "" && ignored == callID {
			return true
		}
	}
	return false
}

func eventMatchesCurrentCall(state State, eventID string) bool {
	if eventID != "" && eventID == state.LastCallID && state.CallID == "" {
		return false
	}
	return state.CallID == "" || eventID == "" || state.CallID == eventID
}

func stripSIPScheme(value string) string {
	value = strings.TrimSpace(value)
	lower := strings.ToLower(value)
	if strings.HasPrefix(lower, "sips:") {
		return value[len("sips:"):]
	}
	if strings.HasPrefix(lower, "sip:") {
		return value[len("sip:"):]
	}
	return value
}
