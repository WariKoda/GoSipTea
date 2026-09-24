package app

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestNormalizeDialTarget(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{" 201 ", "201"},
		{"+49 (30) 12-34", "+49301234"},
		{"*123#", "*123#"},
		{"alice@example.com", "sip:alice@example.com"},
		{"sip:alice@example.com;transport=tcp", "sip:alice@example.com;transport=tcp"},
		{"SIPS:alice@example.com", "SIPS:alice@example.com"},
		{"alice @example.com", ""},
		{"not a target", ""},
		{"@example.com", ""},
		{"sip:alice@example.com\ncommand", ""},
		{strings.Repeat("1", MaxDialTargetLength+1), ""},
	}
	for _, test := range tests {
		if got := NormalizeDialTarget(test.raw); got != test.want {
			t.Errorf("NormalizeDialTarget(%q) = %q, want %q", test.raw, got, test.want)
		}
	}
}

func TestNormalizeContactURI(t *testing.T) {
	tests := []struct {
		raw    string
		domain string
		want   string
	}{
		{"201", "pbx.example.com", "sip:201@pbx.example.com"},
		{"+49 (30) 123", "pbx.example.com", "sip:+4930123@pbx.example.com"},
		{"alice@example.com", "", "sip:alice@example.com"},
		{"sips:alice@example.com", "", "sips:alice@example.com"},
		{"201", "", ""},
		{"sip:alice", "pbx.example.com", ""},
		{"alice@@example.com", "", ""},
	}
	for _, test := range tests {
		if got := NormalizeContactURI(test.raw, test.domain); got != test.want {
			t.Errorf("NormalizeContactURI(%q, %q) = %q, want %q", test.raw, test.domain, got, test.want)
		}
	}
}

func TestPeerDisplay(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{"sip:203@sip.example.com", "203"},
		{`"Alice" <sips:203@example.com;transport=tls>`, "Alice (203)"},
		{"sip:%2B4930123@example.com", "+4930123"},
		{"Provider", "Provider"},
	}
	for _, test := range tests {
		if got := PeerDisplay(test.raw); got != test.want {
			t.Errorf("PeerDisplay(%q) = %q, want %q", test.raw, got, test.want)
		}
	}
}

func TestParseRegistrationOutput(t *testing.T) {
	registered := ParseRegistrationOutput("--- User Agents (1) ---\n\x1b[32msip:201@example.com OK\x1b[0m")
	if !registered.Known || registered.Count != 1 || !registered.Registered || registered.Failed || registered.AOR != "201@example.com" {
		t.Fatalf("registered output = %#v", registered)
	}
	failed := ParseRegistrationOutput("--- User Agents (1) ---\nsips:201@example.com FAIL timeout")
	if !failed.Known || !failed.Failed || failed.Registered {
		t.Fatalf("failed output = %#v", failed)
	}
	empty := ParseRegistrationOutput("--- User Agents (0) ---")
	if !empty.Known || empty.Count != 0 || empty.Registered {
		t.Fatalf("empty output = %#v", empty)
	}
	if unknown := ParseRegistrationOutput("not reginfo"); unknown.Known {
		t.Fatalf("unknown output = %#v", unknown)
	}
}

func TestParseAudioCommandErrorUsesFirstNonemptyLine(t *testing.T) {
	if got := ParseAudioCommandError("\n\x1b[31mno such device for pipewire audio-player: missing\x1b[0m\nnode list"); got != "no such device for pipewire audio-player: missing" {
		t.Fatalf("audio error = %q", got)
	}
	if got := ParseAudioCommandError("pipewire,headset\nfailed later"); got != "" {
		t.Fatalf("successful first line returned error %q", got)
	}
	longError := "failed: " + strings.Repeat("x", 300)
	if got := ParseAudioCommandError(longError); utf8.RuneCountInString(got) > MaxCommandError {
		t.Fatalf("audio error has %d runes", utf8.RuneCountInString(got))
	}
}

func TestParseCallCommandErrorDetectsRefusals(t *testing.T) {
	refusals := []string{
		"no active call\n",
		"command not found (mute)\n",
		"could not answer call (Invalid argument [22])\n",
		"can't find a URI to dial to\n",
	}
	for _, response := range refusals {
		if got := ParseCallCommandError(response); got == "" {
			t.Fatalf("refusal %q reported no error", response)
		}
	}
	if got := ParseCallCommandError(""); got != "" {
		t.Fatalf("empty response returned error %q", got)
	}
	if got := ParseCallCommandError("\n\n"); got != "" {
		t.Fatalf("blank response returned error %q", got)
	}
	longError := "failed: " + strings.Repeat("x", 300)
	if got := ParseCallCommandError(longError); utf8.RuneCountInString(got) > MaxCommandError {
		t.Fatalf("call error has %d runes", utf8.RuneCountInString(got))
	}
}

func TestInputValidationAndClamping(t *testing.T) {
	if err := ValidateInput("hello", 5); err != nil {
		t.Fatalf("ValidateInput() error = %v", err)
	}
	if err := ValidateInput("hello\n", 10); !errors.Is(err, ErrControlCharacter) {
		t.Fatalf("control error = %v", err)
	}
	if err := ValidateInput("hello", 4); !errors.Is(err, ErrInputTooLong) {
		t.Fatalf("length error = %v", err)
	}
	if got := ClampText("a\nb", 10); got != "a b" {
		t.Fatalf("ClampText control result = %q", got)
	}
	if got := ClampText("abcdef", 4); got != "abc…" {
		t.Fatalf("ClampText length result = %q", got)
	}
	if got := ClampText("åäö", 3); got != "åäö" {
		t.Fatalf("ClampText Unicode result = %q", got)
	}
}
