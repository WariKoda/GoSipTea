package tui

import (
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/nibra/gosiptea/internal/app"
)

const (
	defaultWidth  = 80
	defaultHeight = 24
	fieldLimit    = 255
)

type contactMode int

const (
	contactBrowse contactMode = iota
	contactAdd
)

// Model is a Bubble Tea model with no direct storage, audio, or telephony access.
type Model struct {
	dispatch Dispatch
	snapshot Snapshot
	active   View
	width    int
	height   int
	errText  string
	infoText string

	quitPending bool

	dial textinput.Model

	contactSearch textinput.Model
	contactName   textinput.Model
	contactURI    textinput.Model
	contactMode   contactMode
	contactField  int
	contactCursor int
	historyCursor int

	audioField   int
	inputCursor  int
	outputCursor int
	audioInput   string
	audioOutput  string

	accountFields      [5]textinput.Model
	accountFocus       int
	accountSecure      bool
	accountDirty       bool
	passwordRedactions []string
}

// New constructs a model from presentation state and an optional action dispatcher.
func New(snapshot Snapshot, dispatch Dispatch) Model {
	m := Model{
		dispatch: dispatch,
		width:    defaultWidth,
		height:   defaultHeight,
		active:   ViewPhone,
	}

	m.dial = newInput("Number or SIP address", app.MaxDialTargetLength)
	m.contactSearch = newInput("Search contacts", fieldLimit)
	m.contactName = newInput("Display name", 64)
	m.contactURI = newInput("sip:user@example.com", fieldLimit)

	placeholders := []string{"pbx.example.com", "Extension", "Defaults to server", "Defaults to user", "Leave blank to keep saved password"}
	for i := range m.accountFields {
		m.accountFields[i] = newInput(placeholders[i], fieldLimit)
	}
	m.accountFields[4].EchoMode = textinput.EchoPassword
	m.accountFields[4].EchoCharacter = '•'

	m.applySnapshot(snapshot, true)
	m.resizeInputs()
	return m
}

// NewModel is an alias for New.
func NewModel(snapshot Snapshot, dispatch Dispatch) Model {
	return New(snapshot, dispatch)
}

// Init starts the presentation clock used for active call duration.
func (m Model) Init() tea.Cmd {
	return tickCmd()
}

// ActiveView reports the current top-level view.
func (m Model) ActiveView() View {
	return m.active
}

// Snapshot returns a detached copy of the current presentation state.
func (m Model) Snapshot() Snapshot {
	return cloneSnapshot(m.snapshot)
}

// Update handles presentation messages and keyboard input.
func (m Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resizeInputs()
		return m, nil
	case Snapshot:
		m.applySnapshot(msg, false)
		return m, nil
	case *Snapshot:
		if msg != nil {
			m.applySnapshot(*msg, false)
		}
		return m, nil
	case SnapshotMsg:
		m.applySnapshot(msg.Snapshot, false)
		return m, nil
	case *SnapshotMsg:
		if msg != nil {
			m.applySnapshot(msg.Snapshot, false)
		}
		return m, nil
	case ActionResultMsg:
		m.applySnapshot(msg.Snapshot, false)
		m.setError(msg.Err)
		return m, nil
	case UpdateMsg:
		m.applyUpdate(msg)
		return m, nil
	case *UpdateMsg:
		if msg != nil {
			m.applyUpdate(*msg)
		}
		return m, nil
	case ErrorMsg:
		m.setError(msg.Err)
		return m, nil
	case *ErrorMsg:
		if msg != nil {
			m.setError(msg.Err)
		}
		return m, nil
	case error:
		m.setError(msg)
		return m, nil
	case TickMsg:
		m.snapshot.Now = msg.Now
		return m, tickCmd()
	case tea.KeyMsg:
		return m.updateKey(msg)
	default:
		return m, nil
	}
}

func (m Model) updateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if m.quitPending {
		switch key {
		case "y", "Y", "enter":
			m.quitPending = false
			return m, tea.Batch(m.emit(Action{Kind: ActionQuit}), tea.Quit)
		case "n", "N", "esc", "q":
			m.quitPending = false
			return m, nil
		}
		return m, nil
	}

	typing := m.isTyping()
	if !typing {
		switch key {
		case "q", "ctrl+c":
			if !callIsIdle(m.snapshot.Phone.CallState) {
				m.quitPending = true
				return m, nil
			}
			return m, tea.Batch(m.emit(Action{Kind: ActionQuit}), tea.Quit)
		case "1":
			m.changeView(ViewPhone)
			return m, nil
		case "2":
			m.changeView(ViewContacts)
			return m, nil
		case "3":
			m.changeView(ViewAudio)
			return m, nil
		case "4":
			m.changeView(ViewAccount)
			return m, nil
		case "5":
			m.changeView(ViewHistory)
			return m, nil
		}
	}

	switch m.active {
	case ViewPhone:
		return m.updatePhone(msg)
	case ViewContacts:
		return m.updateContacts(msg)
	case ViewAudio:
		return m.updateAudio(msg)
	case ViewAccount:
		return m.updateAccount(msg)
	case ViewHistory:
		return m.updateHistory(msg)
	default:
		return m, nil
	}
}

func (m Model) updatePhone(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if m.dial.Focused() {
		switch key {
		case "esc":
			m.dial.Blur()
			return m, nil
		case "enter":
			target := strings.TrimSpace(m.dial.Value())
			m.dial.Blur()
			if target == "" {
				return m, nil
			}
			m.errText = ""
			return m, m.emit(Action{Kind: ActionDial, Target: target})
		}
		var cmd tea.Cmd
		m.dial, cmd = m.dial.Update(msg)
		return m, cmd
	}

	switch key {
	case "i", "/", "enter":
		return m, m.dial.Focus()
	case "a":
		if m.snapshot.Phone.CallState == app.CallIncoming {
			return m, m.emit(Action{Kind: ActionAnswer})
		}
	case "r":
		if m.snapshot.Phone.CallState == app.CallIncoming {
			return m, m.emit(Action{Kind: ActionReject})
		}
	case "h":
		if !callIsIdle(m.snapshot.Phone.CallState) {
			return m, m.emit(Action{Kind: ActionHangup})
		}
	case "m":
		if m.snapshot.Phone.CallState == app.CallActive {
			return m, m.emit(Action{Kind: ActionToggleMute})
		}
	case "n":
		return m, m.emit(Action{Kind: ActionToggleDND})
	}
	return m, nil
}

func (m Model) updateHistory(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	calls := m.snapshot.History.Calls
	switch msg.String() {
	case "up", "k":
		if m.historyCursor > 0 {
			m.historyCursor--
		}
	case "down", "j":
		if m.historyCursor+1 < len(calls) {
			m.historyCursor++
		}
	case "enter", "d":
		if m.historyCursor >= 0 && m.historyCursor < len(calls) && calls[m.historyCursor].Target != "" {
			return m, m.emit(Action{Kind: ActionDial, Target: calls[m.historyCursor].Target})
		}
	}
	return m, nil
}

func (m Model) updateContacts(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if m.contactMode == contactAdd {
		return m.updateContactAdd(msg)
	}
	if m.contactSearch.Focused() {
		if key == "esc" || key == "enter" {
			m.contactSearch.Blur()
			return m, nil
		}
		oldQuery := m.contactSearch.Value()
		var cmd tea.Cmd
		m.contactSearch, cmd = m.contactSearch.Update(msg)
		if oldQuery != m.contactSearch.Value() {
			m.contactCursor = 0
		}
		return m, cmd
	}

	contacts := m.filteredContacts()
	switch key {
	case "/", "s":
		return m, m.contactSearch.Focus()
	case "up", "k":
		if m.contactCursor > 0 {
			m.contactCursor--
		}
	case "down", "j":
		if m.contactCursor+1 < len(contacts) {
			m.contactCursor++
		}
	case "enter", "d":
		if contact, ok := selectedContact(contacts, m.contactCursor); ok {
			return m, m.emit(Action{Kind: ActionDial, Target: contact.URI})
		}
	case "a":
		m.contactMode = contactAdd
		m.contactField = 0
		m.contactName.SetValue("")
		m.contactURI.SetValue("")
		return m, m.focusContactField()
	case "x", "delete":
		if contact, ok := selectedContact(contacts, m.contactCursor); ok {
			return m, m.emit(Action{Kind: ActionRemoveContact, Target: contact.URI})
		}
	}
	return m, nil
}

func (m Model) updateContactAdd(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if key == "esc" {
		m.cancelContactAdd()
		return m, nil
	}
	if key == "tab" || key == "shift+tab" {
		if key == "tab" {
			m.contactField = (m.contactField + 1) % 2
		} else {
			m.contactField = (m.contactField + 1) % 2
		}
		return m, m.focusContactField()
	}
	if key == "enter" {
		if m.contactField == 0 {
			m.contactField = 1
			return m, m.focusContactField()
		}
		uri := strings.TrimSpace(m.contactURI.Value())
		if uri == "" {
			return m, nil
		}
		action := Action{Kind: ActionAddContact, Contact: ContactInput{Name: strings.TrimSpace(m.contactName.Value()), URI: uri}}
		m.cancelContactAdd()
		return m, m.emit(action)
	}

	var cmd tea.Cmd
	if m.contactField == 0 {
		m.contactName, cmd = m.contactName.Update(msg)
	} else {
		m.contactURI, cmd = m.contactURI.Update(msg)
	}
	return m, cmd
}

func (m Model) updateAudio(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if key == "tab" || key == "shift+tab" {
		m.audioField = (m.audioField + 1) % 2
		return m, nil
	}

	options := m.outputOptions()
	cursor := &m.outputCursor
	if m.audioField == 1 {
		options = m.inputOptions()
		cursor = &m.inputCursor
	}
	switch key {
	case "up", "k":
		if *cursor > 0 {
			*cursor--
		}
	case "down", "j":
		if *cursor+1 < len(options) {
			*cursor++
		}
	case "enter", " ":
		if len(options) == 0 {
			return m, nil
		}
		// The selection marker follows the session snapshot, not this key press.
		field := AudioOutput
		if m.audioField == 1 {
			field = AudioInput
		}
		return m, m.emit(Action{Kind: ActionSetAudio, Audio: AudioSelection{Field: field, Name: options[*cursor].Name}})
	}
	return m, nil
}

func (m Model) updateAccount(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if m.accountFocus < len(m.accountFields) && m.accountFields[m.accountFocus].Focused() {
		switch key {
		case "esc":
			m.accountFields[m.accountFocus].Blur()
			return m, nil
		case "tab", "enter":
			m.accountFocus = (m.accountFocus + 1) % 7
			return m, m.focusAccountField()
		case "shift+tab":
			m.accountFocus = (m.accountFocus + 6) % 7
			return m, m.focusAccountField()
		}
		oldValue := m.accountFields[m.accountFocus].Value()
		updated, cmd := m.accountFields[m.accountFocus].Update(msg)
		m.accountFields[m.accountFocus] = updated
		if oldValue != updated.Value() {
			m.accountDirty = true
		}
		return m, cmd
	}

	switch key {
	case "tab", "down", "j":
		m.accountFocus = (m.accountFocus + 1) % 7
		return m, nil
	case "shift+tab", "up", "k":
		m.accountFocus = (m.accountFocus + 6) % 7
		return m, nil
	case "enter", " ":
		switch m.accountFocus {
		case 5:
			m.accountSecure = !m.accountSecure
			m.accountDirty = true
			return m, nil
		case 6:
			action := m.accountAction()
			if action.Account.Password != "" {
				m.passwordRedactions = append(m.passwordRedactions, action.Account.Password)
			}
			m.accountFields[4].SetValue("")
			m.accountDirty = false
			return m, m.emit(action)
		default:
			if key == "enter" {
				return m, m.focusAccountField()
			}
		}
	}
	return m, nil
}

func (m *Model) applySnapshot(snapshot Snapshot, initial bool) {
	selected, hadSelection := selectedHistoryCall(m.snapshot.History.Calls, m.historyCursor)
	m.snapshot = cloneSnapshot(snapshot)
	if !initial && hadSelection {
		m.historyCursor = historyCallIndex(m.snapshot.History.Calls, selected)
	}
	if m.snapshot.Phone.CallState == "" {
		m.snapshot.Phone.CallState = app.CallIdle
	}
	if m.snapshot.Phone.Registration == "" {
		m.snapshot.Phone.Registration = app.RegistrationUnknown
	}
	if initial || !m.accountDirty {
		m.syncAccountForm()
	}
	m.syncAudioSelection()
	m.clampCursors()
}

func (m *Model) applyUpdate(update UpdateMsg) {
	if update.Phone != nil {
		m.snapshot.Phone = *update.Phone
		if m.snapshot.Phone.CallState == "" {
			m.snapshot.Phone.CallState = app.CallIdle
		}
	}
	if update.Contacts != nil {
		m.snapshot.Contacts = cloneContacts(*update.Contacts)
	}
	if update.Audio != nil {
		m.snapshot.Audio = cloneAudio(*update.Audio)
		m.syncAudioSelection()
	}
	if update.Account != nil {
		m.snapshot.Account = *update.Account
		if !m.accountDirty {
			m.syncAccountForm()
		}
	}
	if update.History != nil {
		selected, hadSelection := selectedHistoryCall(m.snapshot.History.Calls, m.historyCursor)
		m.snapshot.History = cloneHistory(*update.History)
		if hadSelection {
			m.historyCursor = historyCallIndex(m.snapshot.History.Calls, selected)
		}
	}
	if update.Now != nil {
		m.snapshot.Now = *update.Now
	}
	m.clampCursors()
}

func (m *Model) syncAccountForm() {
	values := []string{m.snapshot.Account.Server, m.snapshot.Account.Username, m.snapshot.Account.Domain, m.snapshot.Account.Login, ""}
	for i, value := range values {
		m.accountFields[i].SetValue(value)
	}
	m.accountSecure = m.snapshot.Account.Secure
	m.accountDirty = false
}

// syncAudioSelection moves a cursor to the selected node only when the
// selection changed. Otherwise the cursor stays on the same node, so unrelated
// snapshots do not interrupt navigation.
func (m *Model) syncAudioSelection() {
	inputName := optionName(m.inputOptions(), m.inputCursor)
	outputName := optionName(m.outputOptions(), m.outputCursor)
	inputChanged := m.audioInput != m.snapshot.Audio.SelectedInput
	outputChanged := m.audioOutput != m.snapshot.Audio.SelectedOutput
	m.audioInput = m.snapshot.Audio.SelectedInput
	m.audioOutput = m.snapshot.Audio.SelectedOutput
	if inputChanged {
		inputName = m.audioInput
	}
	if outputChanged {
		outputName = m.audioOutput
	}
	m.inputCursor = optionIndex(m.inputOptions(), inputName)
	m.outputCursor = optionIndex(m.outputOptions(), outputName)
}

func (m *Model) clampCursors() {
	contacts := m.filteredContacts()
	if len(contacts) == 0 {
		m.contactCursor = 0
	} else if m.contactCursor >= len(contacts) {
		m.contactCursor = len(contacts) - 1
	}
	m.historyCursor = clampCursor(m.historyCursor, len(m.snapshot.History.Calls))
	m.inputCursor = clampCursor(m.inputCursor, len(m.inputOptions()))
	m.outputCursor = clampCursor(m.outputCursor, len(m.outputOptions()))
}

func (m *Model) changeView(view View) {
	if view < ViewPhone || view > ViewHistory {
		return
	}
	m.blurAll()
	m.active = view
	m.infoText = ""
	if view == ViewAccount {
		m.accountFocus = 0
	}
}

func (m *Model) blurAll() {
	m.dial.Blur()
	m.contactSearch.Blur()
	m.contactName.Blur()
	m.contactURI.Blur()
	for i := range m.accountFields {
		m.accountFields[i].Blur()
	}
}

func (m Model) isTyping() bool {
	switch m.active {
	case ViewPhone:
		return m.dial.Focused()
	case ViewContacts:
		return m.contactSearch.Focused() || m.contactMode == contactAdd
	case ViewAccount:
		return m.accountFocus < len(m.accountFields) && m.accountFields[m.accountFocus].Focused()
	default:
		return false
	}
}

func (m *Model) resizeInputs() {
	width := m.width - 24
	if width < 12 {
		width = 12
	}
	if width > 64 {
		width = 64
	}
	m.dial.Width = width
	m.contactSearch.Width = width
	m.contactName.Width = width
	m.contactURI.Width = width
	for i := range m.accountFields {
		m.accountFields[i].Width = width
	}
}

func (m *Model) focusContactField() tea.Cmd {
	m.contactName.Blur()
	m.contactURI.Blur()
	if m.contactField == 0 {
		return m.contactName.Focus()
	}
	return m.contactURI.Focus()
}

func (m *Model) cancelContactAdd() {
	m.contactMode = contactBrowse
	m.contactName.Blur()
	m.contactURI.Blur()
}

func (m *Model) focusAccountField() tea.Cmd {
	for i := range m.accountFields {
		m.accountFields[i].Blur()
	}
	if m.accountFocus < len(m.accountFields) {
		return m.accountFields[m.accountFocus].Focus()
	}
	return nil
}

func (m Model) accountAction() Action {
	return Action{
		Kind: ActionSaveAccount,
		Account: AccountInput{
			Server:   strings.TrimSpace(m.accountFields[0].Value()),
			Username: strings.TrimSpace(m.accountFields[1].Value()),
			Domain:   strings.TrimSpace(m.accountFields[2].Value()),
			Login:    strings.TrimSpace(m.accountFields[3].Value()),
			Password: m.accountFields[4].Value(),
			Secure:   m.accountSecure,
		},
	}
}

func (m Model) emit(action Action) tea.Cmd {
	if m.dispatch == nil {
		return nil
	}
	return m.dispatch(action)
}

func (m *Model) setError(err error) {
	if err == nil {
		m.errText = ""
		return
	}
	text := app.ClampText(err.Error(), app.MaxCommandError)
	passwords := append([]string(nil), m.passwordRedactions...)
	passwords = append(passwords, m.accountFields[4].Value())
	for _, password := range passwords {
		if password != "" {
			text = strings.ReplaceAll(text, password, "••••")
		}
	}
	m.errText = text
}

func (m Model) filteredContacts() []ContactSnapshot {
	contacts := make([]app.Contact, len(m.snapshot.Contacts.Contacts))
	for i, contact := range m.snapshot.Contacts.Contacts {
		contacts[i] = app.Contact{Name: contact.Name, URI: contact.URI}
	}
	filtered := app.FilterContacts(contacts, m.contactSearch.Value())
	result := make([]ContactSnapshot, len(filtered))
	for i, contact := range filtered {
		result[i] = ContactSnapshot{Name: contact.Name, URI: contact.URI}
	}
	return result
}

type audioOption struct {
	Name        string
	Description string
	Default     bool
}

func (m Model) inputOptions() []audioOption {
	return audioOptions(m.snapshot.Audio.Inputs, m.audioInput)
}

func (m Model) outputOptions() []audioOption {
	return audioOptions(m.snapshot.Audio.Outputs, m.audioOutput)
}

func audioOptions(nodes []AudioNodeSnapshot, selected string) []audioOption {
	options := make([]audioOption, 1, len(nodes)+2)
	options[0] = audioOption{Description: "System default", Default: true}
	found := selected == ""
	for _, node := range nodes {
		description := strings.TrimSpace(node.Description)
		if description == "" {
			description = node.Name
		}
		if node.Name == selected {
			found = true
		}
		options = append(options, audioOption{Name: node.Name, Description: description, Default: node.Default})
	}
	if !found {
		options = append(options, audioOption{Name: selected, Description: selected + " (unavailable)"})
	}
	return options
}

func optionName(options []audioOption, index int) string {
	if index < 0 || index >= len(options) {
		return ""
	}
	return options[index].Name
}

func optionIndex(options []audioOption, name string) int {
	for i, option := range options {
		if option.Name == name {
			return i
		}
	}
	return 0
}

func selectedHistoryCall(calls []app.CallHistoryEntry, cursor int) (app.CallHistoryEntry, bool) {
	if cursor < 0 || cursor >= len(calls) {
		return app.CallHistoryEntry{}, false
	}
	return calls[cursor], true
}

// historyCallIndex ignores Peer because contact edits may relabel an entry.
func historyCallIndex(calls []app.CallHistoryEntry, selected app.CallHistoryEntry) int {
	for i, call := range calls {
		call.Peer = selected.Peer
		if call == selected {
			return i
		}
	}
	return 0
}

func selectedContact(contacts []ContactSnapshot, cursor int) (ContactSnapshot, bool) {
	if cursor < 0 || cursor >= len(contacts) {
		return ContactSnapshot{}, false
	}
	return contacts[cursor], true
}

func clampCursor(cursor, length int) int {
	if length <= 0 || cursor < 0 {
		return 0
	}
	if cursor >= length {
		return length - 1
	}
	return cursor
}

func newInput(placeholder string, limit int) textinput.Model {
	input := textinput.New()
	input.Prompt = ""
	input.Placeholder = placeholder
	input.CharLimit = limit
	return input
}

func callIsIdle(state app.CallState) bool {
	return state == "" || state == app.CallIdle
}

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(now time.Time) tea.Msg {
		return TickMsg{Now: now}
	})
}

func cloneSnapshot(snapshot Snapshot) Snapshot {
	snapshot.Contacts = cloneContacts(snapshot.Contacts)
	snapshot.Audio = cloneAudio(snapshot.Audio)
	snapshot.History = cloneHistory(snapshot.History)
	return snapshot
}

func cloneContacts(snapshot ContactsSnapshot) ContactsSnapshot {
	snapshot.Contacts = append([]ContactSnapshot(nil), snapshot.Contacts...)
	return snapshot
}

func cloneAudio(snapshot AudioSnapshot) AudioSnapshot {
	snapshot.Inputs = append([]AudioNodeSnapshot(nil), snapshot.Inputs...)
	snapshot.Outputs = append([]AudioNodeSnapshot(nil), snapshot.Outputs...)
	return snapshot
}

func cloneHistory(snapshot CallHistorySnapshot) CallHistorySnapshot {
	snapshot.Calls = append([]app.CallHistoryEntry(nil), snapshot.Calls...)
	return snapshot
}
