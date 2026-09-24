package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/nibra/gosiptea/internal/app"
)

var (
	colorAccent  = lipgloss.Color("63")
	colorGood    = lipgloss.Color("42")
	colorWarning = lipgloss.Color("214")
	colorError   = lipgloss.Color("196")
	colorMuted   = lipgloss.Color("244")

	appStyle = lipgloss.NewStyle().Padding(1, 2, 0, 2)

	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	labelStyle = lipgloss.NewStyle().Foreground(colorMuted).Width(15)
	valueStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	mutedStyle = lipgloss.NewStyle().Foreground(colorMuted)
	goodStyle  = lipgloss.NewStyle().Foreground(colorGood)
	warnStyle  = lipgloss.NewStyle().Foreground(colorWarning)
	errorStyle = lipgloss.NewStyle().Foreground(colorError)

	activeTabStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("230")).Background(colorAccent).Padding(0, 1)
	tabStyle       = lipgloss.NewStyle().Foreground(colorMuted).Padding(0, 1)

	selectedStyle = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	panelStyle    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("238")).Padding(0, 1)
	dialogStyle   = lipgloss.NewStyle().Border(lipgloss.DoubleBorder()).BorderForeground(colorWarning).Padding(1, 2)

	sidebarStyle = lipgloss.NewStyle().Width(sidebarWidth-1).Padding(0, 1).MarginRight(contentGap).
			Border(lipgloss.NormalBorder(), false, true, false, false).BorderForeground(lipgloss.Color("238"))

	statusBarStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Background(lipgloss.Color("236"))
	incomingStatusStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("232")).Background(colorWarning)
)

const (
	// Below this width the sidebar would squeeze the content, so the tabs
	// move back to a row above it.
	sidebarMinWidth = 80
	sidebarWidth    = 24
	// Short terminals drop the top padding and the blank footer line.
	compactHeight = 16
	contentGap    = 2
	appPaddingX   = 4
)

// frame is the text area left for the active view.
type frame struct {
	sidebar bool
	width   int
	height  int
}

func (m Model) frame() frame {
	f := frame{sidebar: m.width >= sidebarMinWidth}
	f.width = m.width - appPaddingX
	f.height = m.mainHeight()
	if f.sidebar {
		f.width -= sidebarWidth + contentGap
	} else {
		f.height -= 2
	}
	f.width = max(20, f.width)
	f.height = max(3, f.height)
	return f
}

func (m Model) mainHeight() int {
	return max(1, m.height-m.paddingTop()-lipgloss.Height(m.footerView()))
}

func (m Model) paddingTop() int {
	if m.height < compactHeight {
		return 0
	}
	return 1
}

// View renders the active screen from presentation state only.
func (m Model) View() string {
	if m.width < 44 || m.height < 12 {
		return smallTerminalView(m.width, m.height)
	}

	f := m.frame()
	var body string
	switch m.active {
	case ViewPhone:
		body = m.phoneView()
	case ViewContacts:
		body = m.contactsView()
	case ViewAudio:
		body = m.audioView()
	case ViewAccount:
		body = m.accountView()
	case ViewHistory:
		body = m.historyView()
	default:
		body = "Unknown view"
	}
	if m.quitPending {
		body = lipgloss.Place(f.width, f.height, lipgloss.Center, lipgloss.Center,
			dialogStyle.Render("A call is in progress. Quit anyway?\n\n[y] Quit   [n] Stay"))
	}

	mainHeight := m.mainHeight()
	contentHeight := mainHeight
	if !f.sidebar {
		contentHeight -= 2
	}
	// Fixed size keeps the footer on the last lines, whatever the view renders.
	content := lipgloss.NewStyle().Height(contentHeight).MaxHeight(contentHeight).MaxWidth(f.width).Render(body)

	var main string
	if f.sidebar {
		main = lipgloss.JoinHorizontal(lipgloss.Top, m.sidebarView(mainHeight), content)
	} else {
		main = m.headerView() + "\n\n" + content
	}
	return appStyle.PaddingTop(m.paddingTop()).Render(main + "\n" + m.footerView())
}

func (m Model) footerView() string {
	width := max(1, m.width-appPaddingX)
	wrap := lipgloss.NewStyle().Width(width)
	footer := m.statusBarView(width) + "\n" + wrap.Render(mutedStyle.Render(m.helpView()))
	if status := m.statusView(); status != "" || m.height >= compactHeight {
		footer = wrap.Render(status) + "\n" + footer
	}
	if m.height >= compactHeight {
		footer = "\n" + footer
	}
	return footer
}

// statusBarView keeps the phone state visible in every view and layout. When
// space runs out it drops the user name, then the peer, then the registration
// word next to its colored dot, so call state and duration stay readable.
func (m Model) statusBarView(width int) string {
	phone := m.snapshot.Phone
	incoming := phone.CallState == app.CallIncoming
	base := statusBarStyle
	if incoming {
		base = incomingStatusStyle
	}
	segment := func(text string, color lipgloss.TerminalColor) string {
		if incoming || color == nil {
			return base.Render(text)
		}
		return base.Foreground(color).Render(text)
	}

	peer := strings.TrimSpace(phone.Peer)
	if peer == "" {
		peer = "Unknown peer"
	}
	user := strings.TrimSpace(m.snapshot.Account.Username)
	var last string
	for _, detail := range []struct{ user, peer, registration bool }{
		{true, true, true}, {false, true, true}, {false, false, true}, {false, false, false},
	} {
		registration := "●"
		if detail.registration {
			registration += " " + shortRegistrationText(phone)
		}
		if detail.user && user != "" {
			registration += " " + user
		}
		var call string
		var callColor lipgloss.TerminalColor = colorMuted
		switch phone.CallState {
		case app.CallIncoming:
			call = "Incoming"
			if detail.peer {
				call += ": " + peer
			}
			if m.active != ViewPhone {
				call += "  [1] answer"
			}
		case app.CallOutgoing:
			call, callColor = "Calling", colorWarning
			if detail.peer {
				call += " " + peer
			}
		case app.CallActive:
			call, callColor = "Active "+formatDuration(phone.CallStartedAt, m.snapshot.Now), colorGood
			if detail.peer {
				call += " " + peer
			}
		default:
			call = "Idle"
		}

		segments := []string{segment(registration, registrationStyle(phone).GetForeground()), segment(call, callColor)}
		if phone.CallState == app.CallActive && phone.Muted {
			segments = append(segments, segment("Muted", colorWarning))
		}
		if phone.DND {
			segments = append(segments, segment("DND on", colorWarning))
		} else {
			segments = append(segments, segment("DND off", nil))
		}
		bar := base.Render(" ") + strings.Join(segments, base.Render(" │ ")) + base.Render(" ")
		if gap := width - lipgloss.Width(bar); gap >= 0 {
			return bar + base.Render(strings.Repeat(" ", gap))
		}
		last = bar
	}
	return lipgloss.NewStyle().MaxWidth(width).Render(last)
}

func (m Model) sidebarView(height int) string {
	names := []string{"Phone", "Contacts", "Audio", "Account", "History"}
	lines := []string{titleStyle.Render("GoSipTea"), ""}
	for i, name := range names {
		row := fmt.Sprintf("  %d %s", i+1, name)
		if View(i) == m.active {
			row = selectedStyle.Render(fmt.Sprintf("› %d %s", i+1, name))
		}
		lines = append(lines, row)
	}
	if len(lines) > height {
		lines = lines[2:]
	}
	return sidebarStyle.Height(height).MaxHeight(height).Render(strings.Join(lines, "\n"))
}

func (m Model) headerView() string {
	names := []string{"1 Phone", "2 Contacts", "3 Audio", "4 Account", "5 History"}
	if m.width < 64 {
		names = []string{"1 Tel", "2 Con", "3 Aud", "4 Acc", "5 Hist"}
	}
	tabs := make([]string, len(names))
	for i, name := range names {
		style := tabStyle
		if View(i) == m.active {
			style = activeTabStyle
		}
		tabs[i] = style.Render(name)
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, tabs...)
}

func (m Model) phoneView() string {
	phone := m.snapshot.Phone
	lines := []string{
		titleStyle.Render("Phone"),
		fieldLine("Registration", registrationStyle(phone).Render(registrationText(phone))),
		fieldLine("Call", callStateText(phone.CallState)),
	}
	if !callIsIdle(phone.CallState) {
		peer := phone.Peer
		if peer == "" {
			peer = "Unknown peer"
		}
		lines = append(lines, fieldLine("Peer", valueStyle.Render(peer)))
	}
	if phone.CallState == app.CallActive {
		lines = append(lines,
			fieldLine("Duration", formatDuration(phone.CallStartedAt, m.snapshot.Now)),
			fieldLine("Microphone", toggleText(phone.Muted, "Muted", "Live")),
		)
	}
	lines = append(lines,
		fieldLine("Do not disturb", toggleText(phone.DND, "On", "Off")),
		"",
		labelStyle.Render("Dial")+m.dial.View(),
		"",
		m.phoneControls(),
	)
	return strings.Join(lines, "\n")
}

func (m Model) phoneControls() string {
	switch m.snapshot.Phone.CallState {
	case app.CallIncoming:
		return "[a] Answer   [r] Reject   [n] DND"
	case app.CallOutgoing:
		return "[h] Hang up   [n] DND"
	case app.CallActive:
		mute := "Mute"
		if m.snapshot.Phone.Muted {
			mute = "Unmute"
		}
		return fmt.Sprintf("[h] Hang up   [m] %s   [n] DND", mute)
	default:
		return "[i] Enter number   [n] DND"
	}
}

func (m Model) historyView() string {
	calls := m.snapshot.History.Calls
	lines := []string{titleStyle.Render("Call history"), ""}
	if len(calls) == 0 {
		return strings.Join(append(lines, mutedStyle.Render("No calls yet.")), "\n")
	}

	f := m.frame()
	limit := max(1, f.height-2)
	start := listStart(m.historyCursor, len(calls), limit)
	end := min(len(calls), start+limit)
	peerWidth := max(10, f.width-38)
	for i := start; i < end; i++ {
		call := calls[i]
		peer := strings.TrimSpace(call.Peer)
		if peer == "" {
			peer = call.Target
		}
		row := fmt.Sprintf("  %s %-*s %-14s %s", directionText(call.Direction), peerWidth, truncate(peer, peerWidth), historyResultText(call), historyTimeText(call.EndedAt, m.snapshot.Now))
		if i == m.historyCursor {
			row = selectedStyle.Render("› " + strings.TrimPrefix(row, "  "))
		}
		lines = append(lines, row)
	}
	return strings.Join(lines, "\n")
}

func (m Model) contactsView() string {
	if m.contactMode == contactAdd {
		return strings.Join([]string{
			titleStyle.Render("Add contact"),
			fieldLine("Name", m.contactName.View()),
			fieldLine("SIP address", m.contactURI.View()),
			"",
			"Enter saves from the address field. Esc cancels.",
		}, "\n")
	}

	contacts := m.filteredContacts()
	lines := []string{
		titleStyle.Render("Contacts"),
		labelStyle.Render("Search") + m.contactSearch.View(),
		"",
	}
	if len(contacts) == 0 {
		lines = append(lines, mutedStyle.Render("No matching contacts."))
	} else {
		f := m.frame()
		limit := max(1, f.height-3)
		start := listStart(m.contactCursor, len(contacts), limit)
		end := min(len(contacts), start+limit)
		for i := start; i < end; i++ {
			contact := contacts[i]
			name := strings.TrimSpace(app.ClampText(contact.Name, 64))
			displayURI := app.ClampText(contact.URI, 255)
			if name == "" {
				name = displayURI
			}
			row := fmt.Sprintf("  %-24s %s", truncate(name, 24), truncate(displayURI, max(12, f.width-27)))
			if i == m.contactCursor {
				row = selectedStyle.Render("› " + strings.TrimPrefix(row, "  "))
			}
			lines = append(lines, row)
		}
	}
	return strings.Join(lines, "\n")
}

func (m Model) audioView() string {
	// Title, blank line and two panels with border and heading take 8 lines.
	available := max(2, (m.frame().height-8)/2)
	outputs := m.audioListView("Output", m.outputOptions(), m.outputCursor, m.audioOutput, m.audioField == 0, available)
	inputs := m.audioListView("Input", m.inputOptions(), m.inputCursor, m.audioInput, m.audioField == 1, available)
	return strings.Join([]string{titleStyle.Render("Audio"), outputs, "", inputs}, "\n")
}

func (m Model) audioListView(title string, options []audioOption, cursor int, selected string, focused bool, limit int) string {
	heading := title
	if focused {
		heading = selectedStyle.Render(title)
	} else {
		heading = valueStyle.Render(title)
	}
	lines := []string{heading}
	start := listStart(cursor, len(options), limit)
	end := min(len(options), start+limit)
	for i := start; i < end; i++ {
		option := options[i]
		marker := "  "
		if option.Name == selected {
			marker = "✓ "
		}
		defaultLabel := ""
		if option.Default && option.Name != "" {
			defaultLabel = " (current system default)"
		}
		description := app.ClampText(option.Description, 255)
		row := marker + truncate(description+defaultLabel, max(16, m.frame().width-6))
		if focused && i == cursor {
			row = selectedStyle.Render("› " + strings.TrimPrefix(row, "  "))
		}
		lines = append(lines, row)
	}
	return panelStyle.Width(max(20, m.frame().width-2)).Render(strings.Join(lines, "\n"))
}

func (m Model) accountView() string {
	labels := []string{"Server", "User", "Domain", "Login", "Password"}
	lines := []string{titleStyle.Render("Account")}
	for i, label := range labels {
		input := m.accountFields[i].View()
		if i == 4 && m.snapshot.Account.HasPassword && m.accountFields[i].Value() == "" {
			input += mutedStyle.Render("  Saved password will be kept")
		}
		prefix := "  "
		if m.accountFocus == i {
			prefix = selectedStyle.Render("› ")
		}
		lines = append(lines, prefix+labelStyle.Render(label)+input)
	}

	secure := "[ ] TLS and SRTP"
	if m.accountSecure {
		secure = "[x] TLS and SRTP"
	}
	if m.accountFocus == 5 {
		secure = selectedStyle.Render("› " + secure)
	} else {
		secure = "  " + secure
	}
	lines = append(lines, secure)

	save := "  Save account"
	if m.accountFocus == 6 {
		save = selectedStyle.Render("› Save account")
	}
	lines = append(lines, "", save)
	return strings.Join(lines, "\n")
}

func (m Model) statusView() string {
	if m.errText != "" {
		return errorStyle.Render("Error: " + m.errText)
	}
	if m.infoText != "" {
		return goodStyle.Render(m.infoText)
	}
	return ""
}

func (m Model) helpView() string {
	if m.isTyping() {
		return "Enter/Tab continue   Esc stop typing"
	}
	switch m.active {
	case ViewContacts:
		return "/ search   ↑/↓ select   d dial   a add   x remove   1-5 views   q quit"
	case ViewAudio:
		return "Tab input/output   ↑/↓ select   Enter apply   1-5 views   q quit"
	case ViewAccount:
		return "↑/↓ select   Enter edit/toggle/save   1-5 views   q quit"
	case ViewHistory:
		return "↑/↓ select   Enter/d dial   1-5 views   q quit"
	default:
		return "1-5 views   q quit"
	}
}

func smallTerminalView(width, height int) string {
	return fmt.Sprintf("Terminal too small\n\nCurrent: %dx%d\nRequired: at least 44x12\n\nResize the terminal to continue.", width, height)
}

func registrationStyle(phone PhoneSnapshot) lipgloss.Style {
	switch {
	case phone.Registered || phone.Registration == app.RegistrationRegistered:
		return goodStyle
	case phone.Registration == app.RegistrationFailed:
		return errorStyle
	default:
		return warnStyle
	}
}

// shortRegistrationText ignores the detail text, which rarely fits the sidebar.
func shortRegistrationText(phone PhoneSnapshot) string {
	switch {
	case phone.Registered || phone.Registration == app.RegistrationRegistered:
		return "Registered"
	case phone.Registration == app.RegistrationRegistering:
		return "Registering"
	case phone.Registration == app.RegistrationFailed:
		return "Failed"
	case phone.Registration == app.RegistrationUnregistered:
		return "Offline"
	default:
		return "Unknown"
	}
}

func registrationText(phone PhoneSnapshot) string {
	detail := strings.TrimSpace(phone.RegistrationDetail)
	if detail != "" {
		return detail
	}
	switch phone.Registration {
	case app.RegistrationRegistered:
		return "Registered"
	case app.RegistrationRegistering:
		return "Registering"
	case app.RegistrationFailed:
		return "Registration failed"
	case app.RegistrationUnregistered:
		return "Unregistered"
	default:
		return "Unknown"
	}
}

func callStateText(state app.CallState) string {
	switch state {
	case app.CallIncoming:
		return warnStyle.Render("Incoming")
	case app.CallOutgoing:
		return warnStyle.Render("Calling")
	case app.CallActive:
		return goodStyle.Render("Active")
	default:
		return mutedStyle.Render("Idle")
	}
}

func directionText(direction app.CallDirection) string {
	if direction == app.CallDirectionIncoming {
		return "↓"
	}
	return "↑"
}

func historyResultText(call app.CallHistoryEntry) string {
	switch call.Outcome {
	case app.CallOutcomeConnected:
		return formatDuration(call.ConnectedAt, call.EndedAt)
	case app.CallOutcomeMissed:
		return "Missed"
	case app.CallOutcomeRejected:
		return "Rejected"
	case app.CallOutcomeRejectedDND:
		return "DND"
	case app.CallOutcomeRejectedBusy:
		return "Busy"
	case app.CallOutcomeCanceled:
		return "Canceled"
	default:
		return "Not connected"
	}
}

func historyTimeText(endedAt, now time.Time) string {
	if endedAt.IsZero() {
		return "Unknown"
	}
	local := endedAt.Local()
	if now.IsZero() {
		return local.Format("02 Jan 15:04")
	}
	today := now.Local()
	date := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, today.Location())
	callDate := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, local.Location())
	switch {
	case callDate.Equal(date):
		return "Today " + local.Format("15:04")
	case callDate.Equal(date.AddDate(0, 0, -1)):
		return "Yesterday " + local.Format("15:04")
	default:
		if local.Year() != today.Year() {
			return local.Format("02 Jan 2006 15:04")
		}
		return local.Format("02 Jan 15:04")
	}
}

func toggleText(on bool, enabled, disabled string) string {
	if on {
		return warnStyle.Render(enabled)
	}
	return valueStyle.Render(disabled)
}

func fieldLine(label, value string) string {
	return labelStyle.Render(label) + value
}

func formatDuration(start, now time.Time) string {
	if start.IsZero() || now.Before(start) {
		return "00:00"
	}
	duration := now.Sub(start).Truncate(time.Second)
	hours := int(duration / time.Hour)
	minutes := int(duration/time.Minute) % 60
	seconds := int(duration/time.Second) % 60
	if hours > 0 {
		return fmt.Sprintf("%02d:%02d:%02d", hours, minutes, seconds)
	}
	return fmt.Sprintf("%02d:%02d", minutes, seconds)
}

func listStart(cursor, length, limit int) int {
	if length <= limit || limit <= 0 {
		return 0
	}
	start := cursor - limit/2
	if start < 0 {
		return 0
	}
	if start+limit > length {
		return length - limit
	}
	return start
}

func truncate(value string, width int) string {
	if width <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= width {
		return value
	}
	if width == 1 {
		return "…"
	}
	return string(runes[:width-1]) + "…"
}
