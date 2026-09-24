package app

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func testContact(uri string, name ...string) Contact {
	contact := Contact{URI: uri, Name: "Anna"}
	if len(name) > 0 {
		contact.Name = name[0]
	}
	return contact
}

func TestCallerNameExactAddressWinsAndHostIsCaseInsensitive(t *testing.T) {
	got := CallerName([]Contact{testContact("sip:201@pbx")}, "sips:201@PBX;transport=tls", "Provider", "")
	if got != "Anna" {
		t.Fatalf("CallerName() = %q, want Anna", got)
	}
}

func TestCallerNameKeepsDisplayNameWithAt(t *testing.T) {
	got := CallerName(nil, "sip:+4930123456@trunk", "support@example.com", "49")
	if got != "support@example.com" {
		t.Fatalf("CallerName() = %q, want support@example.com", got)
	}
	if got := CallerName(nil, "sip:alice@example.com", "  ", "49"); got != "alice" {
		t.Fatalf("CallerName() with blank display name = %q, want alice", got)
	}
}

func TestNormalizeContactURILowercasesScheme(t *testing.T) {
	tests := map[string]string{
		"SIP:Alice@Example.com": "sip:Alice@Example.com",
		"Sips:bob@example.com":  "sips:bob@example.com",
		"sip:carol@example.com": "sip:carol@example.com",
	}
	for raw, want := range tests {
		if got := NormalizeContactURI(raw, ""); got != want {
			t.Errorf("NormalizeContactURI(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestContactNameResolvesDialTargets(t *testing.T) {
	contacts := []Contact{
		testContact("sip:alice@example.com", "Alice"),
		testContact("sip:+4930123456@pbx", "Bob"),
	}
	tests := []struct {
		target string
		want   string
		ok     bool
	}{
		{"sip:alice@example.com", "Alice", true},
		{"030123456", "Bob", true},
		{"+4930123456", "Bob", true},
		{"201", "", false},
		{"sip:carol@example.com", "", false},
	}
	for _, test := range tests {
		if got, ok := ContactName(contacts, test.target, "49"); got != test.want || ok != test.ok {
			t.Errorf("ContactName(%q) = %q, %v, want %q, %v", test.target, got, ok, test.want, test.ok)
		}
	}

	ambiguous := append(contacts, testContact("sip:030123456@other", "Carol"))
	if got, ok := ContactName(ambiguous, "030123456", "49"); ok {
		t.Fatalf("ambiguous number resolved to %q", got)
	}
}

func TestResolveHistoryPeersKeepsUnmatchedLabels(t *testing.T) {
	history := []CallHistoryEntry{
		{Peer: "alice", Target: "sip:alice@example.com"},
		{Peer: "Provider", Target: "sip:unknown@example.com"},
	}
	resolved := ResolveHistoryPeers(history, []Contact{testContact("sip:alice@example.com", "Alice")}, "49")
	if resolved[0].Peer != "Alice" || resolved[1].Peer != "Provider" {
		t.Fatalf("resolved history = %#v", resolved)
	}
	if history[0].Peer != "alice" {
		t.Fatal("ResolveHistoryPeers changed its input")
	}
}

func TestCallerNameMatchesInternationalNumbersAcrossHosts(t *testing.T) {
	tests := []struct {
		contact string
		peer    string
	}{
		{"sip:004930123456@pbx", "sip:+49-30-123456@trunk"},
		{"sip:+4930123456@pbx", `"Caller" <sip:%2B4930123456@trunk>`},
	}
	for _, test := range tests {
		if got := CallerName([]Contact{testContact(test.contact)}, test.peer, "", ""); got != "Anna" {
			t.Errorf("CallerName(%q, %q) = %q, want Anna", test.contact, test.peer, got)
		}
	}
}

func TestCallerNameNationalConversionRequiresCountryCode(t *testing.T) {
	contacts := []Contact{testContact("sip:030123456@pbx")}
	tests := []struct {
		contacts []Contact
		peer     string
		display  string
		country  string
		want     string
	}{
		{contacts, "sip:+4930123456@trunk", "Provider", "", "Provider"},
		{contacts, "sip:+4930123456@trunk", "Provider", "49", "Anna"},
		{[]Contact{testContact("sip:+4930123456@pbx")}, "sip:030123456@trunk", "", "+49", "Anna"},
		{contacts, "sip:+4430123456@trunk", "Provider", "49", "Provider"},
	}
	for _, test := range tests {
		if got := CallerName(test.contacts, test.peer, test.display, test.country); got != test.want {
			t.Errorf("CallerName(%q, country %q) = %q, want %q", test.peer, test.country, got, test.want)
		}
	}
}

func TestCallerNameDoesNotMatchExtensionsAcrossHostsOrUserCase(t *testing.T) {
	tests := []struct {
		contact string
		peer    string
		want    string
	}{
		{"sip:201@pbx", "sip:201@other", "201"},
		{"sip:Alice@pbx", "sip:alice@pbx", "alice"},
		{"sip:123456@pbx", "sip:+4930123456@pbx", "+4930123456"},
	}
	for _, test := range tests {
		if got := CallerName([]Contact{testContact(test.contact)}, test.peer, "", ""); got != test.want {
			t.Errorf("CallerName(%q, %q) = %q, want %q", test.contact, test.peer, got, test.want)
		}
	}
}

func TestCallerNameFallsBackOnAmbiguousNumber(t *testing.T) {
	contacts := []Contact{
		testContact("sip:+4930123456@one"),
		testContact("sip:004930123456@two", "Bob"),
	}
	if got := CallerName(contacts, "sip:+4930123456@trunk", "Provider", ""); got != "Provider" {
		t.Fatalf("ambiguous CallerName() = %q, want Provider", got)
	}
	if got := CallerName(contacts, "sip:+4930123456@one", "", ""); got != "Anna" {
		t.Fatalf("exact CallerName() = %q, want Anna", got)
	}
	contacts[0], contacts[1] = contacts[1], contacts[0]
	if got := CallerName(contacts, "sip:+4930123456@one", "", ""); got != "Anna" {
		t.Fatalf("reversed exact CallerName() = %q, want Anna", got)
	}
}

func TestCallerNameRetainsFallbacks(t *testing.T) {
	tests := []struct {
		contacts []Contact
		peer     string
		display  string
		want     string
	}{
		{nil, "sip:201@pbx", "Provider", "Provider"},
		{nil, "sip:201@pbx", "", "201"},
		{[]Contact{testContact("sip:201@pbx", "  ")}, "sip:201@pbx", "Provider", "Provider"},
		{[]Contact{testContact("sip:201@pbx")}, "", "Provider", "Provider"},
		{[]Contact{testContact("invalid")}, "sip:201@pbx", "", "201"},
		{nil, "", "", ""},
	}
	for _, test := range tests {
		if got := CallerName(test.contacts, test.peer, test.display, ""); got != test.want {
			t.Errorf("CallerName(%q, %q) = %q, want %q", test.peer, test.display, got, test.want)
		}
	}
}

func TestCallerNameAndNotificationTextAreBounded(t *testing.T) {
	name := CallerName([]Contact{testContact("sip:201@pbx", "<Anna>\n"+strings.Repeat("x", 100))}, "sip:201@pbx", "", "")
	if utf8.RuneCountInString(name) > MaxPeerDisplayLength {
		t.Fatalf("name has %d runes, limit is %d", utf8.RuneCountInString(name), MaxPeerDisplayLength)
	}
	if strings.ContainsAny(NotificationText(name), "\n<>&\"") {
		t.Fatalf("NotificationText(%q) retained markup or a newline", name)
	}
}

func TestCallerNameHandlesURIParametersAndEncodedUser(t *testing.T) {
	contacts := []Contact{testContact("sip:alice%2Bdesk@example.com;transport=tcp")}
	got := CallerName(contacts, `"Network" <SIPS:alice%2Bdesk@EXAMPLE.COM;transport=tls?subject=x>`, "Network", "")
	if got != "Anna" {
		t.Fatalf("CallerName() = %q, want Anna", got)
	}
}

func TestFilterContactsUsesFuzzyRelevanceAndStableOrder(t *testing.T) {
	contacts := []Contact{
		{URI: "sip:201@pbx", Name: "Support"},
		{URI: "sip:202@pbx", Name: "Sam Porter"},
		{URI: "sip:support@example.com", Name: "Desk"},
	}
	got := FilterContacts(contacts, "support")
	if len(got) != 2 || got[0] != contacts[0] || got[1] != contacts[2] {
		t.Fatalf("FilterContacts() = %#v", got)
	}
	if got := FilterContacts(contacts, "supt"); len(got) != 2 || got[0] != contacts[0] {
		t.Fatalf("subsequence FilterContacts() = %#v", got)
	}
	if got := FilterContacts(contacts, "zzz"); len(got) != 0 {
		t.Fatalf("non-match FilterContacts() = %#v, want empty", got)
	}
}
