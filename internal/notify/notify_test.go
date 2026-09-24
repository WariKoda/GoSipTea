package notify

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestBuildArgs(t *testing.T) {
	args, err := BuildArgs("GoSipTea", "Incoming <call>", "Alice & Bob\r\n--urgency=critical")
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"--app-name", "GoSipTea",
		"--",
		"Incoming &lt;call&gt;",
		"Alice &amp; Bob\n--urgency=critical",
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("BuildArgs() = %#v, want %#v", args, want)
	}
}

func TestBuildArgsBoundsPlainText(t *testing.T) {
	summary := strings.Repeat("<&é", MaxSummaryBytes)
	body := strings.Repeat("x", MaxBodyBytes+100)

	args, err := BuildArgs(DefaultAppName, summary, body)
	if err != nil {
		t.Fatal(err)
	}
	if len(args[3]) > MaxSummaryBytes {
		t.Fatalf("summary has %d bytes, limit is %d", len(args[3]), MaxSummaryBytes)
	}
	if len(args[4]) > MaxBodyBytes {
		t.Fatalf("body has %d bytes, limit is %d", len(args[4]), MaxBodyBytes)
	}
	if !utf8.ValidString(args[3]) || !utf8.ValidString(args[4]) {
		t.Fatal("bounded arguments are not valid UTF-8")
	}
	if strings.Contains(args[3], "<") || strings.Contains(args[3], ">") {
		t.Fatalf("summary contains unescaped markup: %q", args[3])
	}
}

func TestBuildArgsRejectsInvalidAppName(t *testing.T) {
	tests := []string{
		"",
		"oma\nsip",
		strings.Repeat("a", MaxAppNameBytes+1),
		string([]byte{0xff}),
	}
	for _, appName := range tests {
		if _, err := BuildArgs(appName, "summary", "body"); err == nil {
			t.Fatalf("BuildArgs(%q) accepted invalid app name", appName)
		}
	}
}

func TestNotifierUsesArgvWithoutDisplayingNotification(t *testing.T) {
	var gotArgs []string
	var gotLimit int
	notifier := New("test-app")
	notifier.run = func(_ context.Context, limit int, args ...string) error {
		gotLimit = limit
		gotArgs = append([]string(nil), args...)
		return nil
	}

	if err := notifier.Send(context.Background(), "--help", "$(touch /tmp/nope)"); err != nil {
		t.Fatal(err)
	}

	if gotLimit != MaxOutputBytes {
		t.Fatalf("output limit = %d, want %d", gotLimit, MaxOutputBytes)
	}
	wantArgs := []string{
		"--app-name", "test-app",
		"--",
		"--help",
		"$(touch /tmp/nope)",
	}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("argv = %#v, want %#v", gotArgs, wantArgs)
	}
}

func TestBoundedBuffer(t *testing.T) {
	buffer := newBoundedBuffer(3)
	n, err := buffer.Write([]byte("12345"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 5 || buffer.String() != "123" || !buffer.overflow {
		t.Fatalf("Write() = (%d, %q, overflow=%t)", n, buffer.String(), buffer.overflow)
	}
}
