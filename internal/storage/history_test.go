package storage

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nibra/gosiptea/internal/app"
)

func TestCallHistoryUnicodePeerRoundTrip(t *testing.T) {
	t.Parallel()
	for _, peer := range []string{
		strings.Repeat("😀", MaxContactName),
		strings.Repeat("a", MaxFieldLength),
	} {
		store := New(t.TempDir())
		entry := historyTestEntry()
		entry.Peer = peer
		entry.Target = "sip:" + strings.Repeat("a", MaxFieldLength-len("sip:@example.com")) + "@example.com"
		if err := store.WriteCallHistory([]app.CallHistoryEntry{entry}); err != nil {
			t.Fatalf("WriteCallHistory() = %v", err)
		}
		got, err := store.ReadCallHistory()
		if err != nil || !reflect.DeepEqual(got, []app.CallHistoryEntry{entry}) {
			t.Fatalf("round trip = %#v, %v", got, err)
		}
	}
}

func TestCallHistoryRejectsInvalidFieldsWithoutReplacingFile(t *testing.T) {
	t.Parallel()
	for name, change := range map[string]func(*app.CallHistoryEntry){
		"long target":          func(e *app.CallHistoryEntry) { e.Target = strings.Repeat("a", MaxFieldLength+1) },
		"target byte limit":    func(e *app.CallHistoryEntry) { e.Target = "sip:" + strings.Repeat("😀", 64) + "@example.com" },
		"target control":       func(e *app.CallHistoryEntry) { e.Target = "sip:a\n@example.com" },
		"target invalid UTF-8": func(e *app.CallHistoryEntry) { e.Target = "sip:\xff@example.com" },
		"long peer":            func(e *app.CallHistoryEntry) { e.Peer = strings.Repeat("😀", MaxFieldLength+1) },
		"peer invalid UTF-8":   func(e *app.CallHistoryEntry) { e.Peer = "\xff" },
	} {
		t.Run(name, func(t *testing.T) {
			store := New(t.TempDir())
			valid := historyTestEntry()
			if err := store.WriteCallHistory([]app.CallHistoryEntry{valid}); err != nil {
				t.Fatal(err)
			}
			invalid := valid
			change(&invalid)
			if err := store.WriteCallHistory([]app.CallHistoryEntry{invalid, valid}); !errors.Is(err, ErrInvalid) {
				t.Fatalf("WriteCallHistory() = %v, want ErrInvalid", err)
			}
			got, err := store.ReadCallHistory()
			if err != nil || !reflect.DeepEqual(got, []app.CallHistoryEntry{valid}) {
				t.Fatalf("history after rejected write = %#v, %v", got, err)
			}
		})
	}
}

func historyTestEntry() app.CallHistoryEntry {
	at := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	return app.CallHistoryEntry{
		Direction: app.CallDirectionIncoming,
		Outcome:   app.CallOutcomeMissed,
		Peer:      "Alice",
		Target:    "sip:alice@example.com",
		StartedAt: at,
		EndedAt:   at,
	}
}
