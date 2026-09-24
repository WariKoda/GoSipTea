package baresip

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/godbus/dbus/v5"
)

func TestBuildCommand(t *testing.T) {
	tests := []struct {
		name    string
		command string
		params  string
		want    string
		wantErr error
	}{
		{name: "without params", command: "accept", want: "accept"},
		{name: "with params", command: "dial", params: "sip:alice@example.com", want: "dial sip:alice@example.com"},
		{name: "underscore", command: "audio_debug", want: "audio_debug"},
		{name: "uppercase", command: "Dial", wantErr: ErrInvalidCommand},
		{name: "hyphen", command: "reg-info", wantErr: ErrInvalidCommand},
		{name: "empty", wantErr: ErrInvalidCommand},
		{name: "long name", command: strings.Repeat("a", MaxCommandNameBytes+1), wantErr: ErrInvalidCommand},
		{name: "long params", command: "dial", params: strings.Repeat("a", MaxParamsBytes+1), wantErr: ErrInvalidParams},
		{name: "newline", command: "dial", params: "alice\nbob", wantErr: ErrInvalidParams},
		{name: "nul", command: "dial", params: "alice\x00bob", wantErr: ErrInvalidParams},
		{name: "invalid UTF-8", command: "dial", params: string([]byte{0xff}), wantErr: ErrInvalidParams},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := BuildCommand(test.command, test.params)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("BuildCommand() error = %v, want %v", err, test.wantErr)
			}
			if got != test.want {
				t.Fatalf("BuildCommand() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestValidateCommandLine(t *testing.T) {
	if err := ValidateCommandLine("dial sip:alice@example.com"); err != nil {
		t.Fatalf("valid line rejected: %v", err)
	}
	if err := ValidateCommandLine(strings.Repeat("a", MaxCommandLineBytes+1)); !errors.Is(err, ErrCommandTooLong) {
		t.Fatalf("oversized line error = %v, want %v", err, ErrCommandTooLong)
	}
	if err := ValidateCommandLine("dial bad\tparam"); !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("control character error = %v, want %v", err, ErrInvalidParams)
	}
}

func TestParseEventSignal(t *testing.T) {
	body := []any{"call", "CALL_INCOMING", `{"class":"call","type":"CALL_INCOMING","id":"7"}`}
	event, err := ParseEventSignal(body)
	if err != nil {
		t.Fatalf("ParseEventSignal() error: %v", err)
	}
	if event["class"] != "call" || event["type"] != "CALL_INCOMING" || event["id"] != "7" {
		t.Fatalf("unexpected event: %#v", event)
	}

	variantBody := []any{"call", "CALL_CLOSED", dbus.MakeVariant(`{"class":"call","type":"CALL_CLOSED"}`)}
	if _, err := ParseEventSignal(variantBody); err != nil {
		t.Fatalf("variant payload rejected: %v", err)
	}
}

func TestOwnerChanged(t *testing.T) {
	client := DBusClient{owner: ":1.42"}
	if !client.ownerChanged([]any{ServiceName, ":1.42", ""}) {
		t.Fatal("service disappearance was not detected")
	}
	if !client.ownerChanged([]any{ServiceName, ":1.42", ":1.43"}) {
		t.Fatal("service replacement was not detected")
	}
	if client.ownerChanged([]any{ServiceName, "", ":1.42"}) {
		t.Fatal("initial owner acquisition was treated as disappearance")
	}
	if client.ownerChanged([]any{"com.example.Other", ":1.42", ""}) {
		t.Fatal("another service was treated as baresip disappearance")
	}
	if client.ownerChanged([]any{"malformed"}) {
		t.Fatal("malformed signal was treated as disappearance")
	}
}

func TestParseEventSignalRejectsMalformedInput(t *testing.T) {
	tests := []struct {
		name    string
		body    []any
		wantErr error
	}{
		{name: "too few arguments", body: []any{"call", "CALL_CLOSED"}, wantErr: ErrInvalidEvent},
		{name: "wrong payload type", body: []any{"call", "CALL_CLOSED", 42}, wantErr: ErrInvalidEvent},
		{name: "invalid json", body: []any{"call", "CALL_CLOSED", "{"}, wantErr: ErrInvalidEvent},
		{name: "array", body: []any{"call", "CALL_CLOSED", "[]"}, wantErr: ErrInvalidEvent},
		{name: "null", body: []any{"call", "CALL_CLOSED", "null"}, wantErr: ErrInvalidEvent},
		{name: "oversized", body: []any{"call", "CALL_CLOSED", strings.Repeat("x", MaxEventBytes+1)}, wantErr: ErrEventTooLarge},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseEventSignal(test.body)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("ParseEventSignal() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestLimitResponse(t *testing.T) {
	short := "ok"
	if got := limitResponse(short); got != short {
		t.Fatalf("short response changed to %q", got)
	}

	response := strings.Repeat("a", MaxResponseBytes-1) + "€"
	got := limitResponse(response)
	if len(got) > MaxResponseBytes {
		t.Fatalf("response has %d bytes, maximum is %d", len(got), MaxResponseBytes)
	}
	if !utf8.ValidString(got) {
		t.Fatal("truncated response is not valid UTF-8")
	}
}
