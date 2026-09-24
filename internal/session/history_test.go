package session

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/nibra/gosiptea/internal/app"
	"github.com/nibra/gosiptea/internal/baresip"
	"github.com/nibra/gosiptea/internal/storage"
)

func TestOversizedIncomingHistoryDoesNotPoisonLaterCalls(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"missed", "connected", "rejected", "dnd", "busy", "shutdown"} {
		t.Run(mode, func(t *testing.T) {
			s, rt, store, client := newHistoryTestSession(t)
			seed := sessionHistoryEntry(s.deps.Now(), "sip:seed@example.com")
			if err := s.appendCallHistory([]app.CallHistoryEntry{seed}); err != nil {
				t.Fatal(err)
			}
			if mode == "dnd" {
				if err := s.applyDomainEvent(rt, rt.ctx, app.ActionEvent{Type: app.ActionToggleDND}); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "busy" {
				if err := s.handleBaresipEvent(rt, baresip.Event{"type": "CALL_INCOMING", "id": "first", "peeruri": "sip:first@example.com"}); err != nil {
					t.Fatal(err)
				}
			}
			target := "sip:" + strings.Repeat("a", 4096) + "@example.com"
			err := s.handleBaresipEvent(rt, baresip.Event{"type": "CALL_INCOMING", "id": "bad", "peeruri": target})
			switch mode {
			case "dnd", "busy":
				command := "hangup"
				if mode == "busy" {
					command += " bad"
				}
				if !client.hasCommand(command) {
					t.Fatalf("commands = %v, want %s", client.commandsCopy(), command)
				}
			default:
				if err != nil {
					t.Fatalf("incoming signaling rejected: %v", err)
				}
				state := s.Snapshot().State
				if state.CallState != app.CallIncoming || state.CallTarget != target || utf8.RuneCountInString(state.Peer) > app.MaxPeerDisplayLength {
					t.Fatal("incoming call lost or target silently changed")
				}
				if mode == "connected" {
					if err := s.handleBaresipEvent(rt, baresip.Event{"type": "CALL_ESTABLISHED", "id": "bad", "peeruri": target}); err != nil {
						t.Fatal(err)
					}
					if s.Snapshot().State.CallState != app.CallActive {
						t.Fatal("call did not become active")
					}
				}
				if mode == "rejected" {
					if err := s.applyDomainEvent(rt, rt.ctx, app.ActionEvent{Type: app.ActionHangup}); err != nil {
						t.Fatal(err)
					}
					if !client.hasCommand("hangup scode=603 reason=Decline") {
						t.Fatalf("commands = %v", client.commandsCopy())
					}
				}
				if mode == "shutdown" {
					err = s.finalizeActiveCall(s.deps.Now(), true)
				} else {
					err = s.handleBaresipEvent(rt, baresip.Event{"type": "CALL_CLOSED", "id": "bad", "peeruri": target})
				}
			}
			if !errors.Is(err, storage.ErrInvalid) {
				t.Fatalf("invalid history error = %v", err)
			}
			assertHistoryStored(t, s, store, []app.CallHistoryEntry{seed})
			if mode == "busy" {
				if state := s.Snapshot().State; state.CallState != app.CallIncoming || state.CallID != "first" {
					t.Fatalf("busy call replaced original state: %#v", state)
				}
				if err := s.handleBaresipEvent(rt, baresip.Event{"type": "CALL_CLOSED", "id": "first"}); err != nil {
					t.Fatal(err)
				}
			}
			if state := s.Snapshot().State; state.CallState != app.CallIdle || state.CallTarget != "" {
				t.Fatalf("closed call retained state: %#v", state)
			}
			if mode == "dnd" {
				if err := s.applyDomainEvent(rt, rt.ctx, app.ActionEvent{Type: app.ActionToggleDND}); err != nil {
					t.Fatal(err)
				}
			}
			want := s.Snapshot().History
			for i := range 3 {
				id := fmt.Sprintf("valid-%d", i)
				target := "sip:" + id + "@example.com"
				if err := s.handleBaresipEvent(rt, baresip.Event{"type": "CALL_INCOMING", "id": id, "peeruri": target}); err != nil {
					t.Fatal(err)
				}
				if err := s.handleBaresipEvent(rt, baresip.Event{"type": "CALL_CLOSED", "id": id}); err != nil {
					t.Fatalf("subsequent valid call %d: %v", i, err)
				}
				want = append([]app.CallHistoryEntry{sessionHistoryEntry(s.deps.Now(), target)}, want...)
				assertHistoryStored(t, s, store, want)
			}
		})
	}
}

func TestUnicodeHistoryLabelsPersist(t *testing.T) {
	t.Parallel()
	for _, source := range []string{"contact", "display", "oversized display"} {
		t.Run(source, func(t *testing.T) {
			s, rt, store, _ := newHistoryTestSession(t)
			target := "sip:unicode@example.com"
			name := strings.Repeat("😀", storage.MaxContactName)
			display := name
			wantName := name
			if source == "contact" {
				if err := store.AddContact(storage.Contact{Name: name, URI: target}); err != nil {
					t.Fatal(err)
				}
				if _, err := s.reloadContacts(); err != nil {
					t.Fatal(err)
				}
				display = "provider label"
			} else if source == "oversized display" {
				display = name + "\n" + strings.Repeat("😀", 100)
				wantName = app.ClampText(display, app.MaxPeerDisplayLength)
			}
			if err := s.handleBaresipEvent(rt, baresip.Event{"type": "CALL_INCOMING", "id": "unicode", "peeruri": target, "peerdisplayname": display}); err != nil {
				t.Fatal(err)
			}
			if s.Snapshot().State.Peer != wantName {
				t.Fatalf("display = %q", s.Snapshot().State.Peer)
			}
			if err := s.handleBaresipEvent(rt, baresip.Event{"type": "CALL_CLOSED", "id": "unicode"}); err != nil {
				t.Fatal(err)
			}
			want := sessionHistoryEntry(s.deps.Now(), target)
			want.Peer = wantName
			assertHistoryStored(t, s, store, []app.CallHistoryEntry{want})
		})
	}
}

func TestHistoryMergeDropsInvalidRetainedAndNewEntries(t *testing.T) {
	t.Parallel()
	for _, invalidField := range []string{"target", "timestamps", "direction", "outcome", "connected timestamp"} {
		t.Run(invalidField, func(t *testing.T) {
			s, _, store, _ := newHistoryTestSession(t)
			valid := sessionHistoryEntry(s.deps.Now(), "sip:valid@example.com")
			invalid := valid
			switch invalidField {
			case "target":
				invalid.Target = strings.Repeat("a", storage.MaxFieldLength+1)
			case "timestamps":
				invalid.StartedAt = time.Time{}
			case "direction":
				invalid.Direction = "unknown"
			case "outcome":
				invalid.Outcome = "unknown"
			case "connected timestamp":
				invalid.Outcome = app.CallOutcomeConnected
			}
			s.updateSnapshot(func(snapshot *Snapshot) { snapshot.History = []app.CallHistoryEntry{invalid} })
			if err := s.appendCallHistory([]app.CallHistoryEntry{invalid, valid}); !errors.Is(err, storage.ErrInvalid) {
				t.Fatalf("append invalid history = %v", err)
			}
			assertHistoryStored(t, s, store, []app.CallHistoryEntry{valid})
			if err := s.appendCallHistory([]app.CallHistoryEntry{valid}); err != nil {
				t.Fatalf("subsequent append = %v", err)
			}
			assertHistoryStored(t, s, store, []app.CallHistoryEntry{valid, valid})
		})
	}
}

func TestFailedDialHistoryDropsOversizedNormalizedTarget(t *testing.T) {
	t.Parallel()
	s, rt, store, client := newHistoryTestSession(t)
	s.updateSnapshot(func(snapshot *Snapshot) { snapshot.State.Registered = true })
	client.commandErrors["dial"] = errors.New("dial failed")
	// Adding the SIP scheme can push a bare address past the storage limit.
	target := strings.Repeat("a", storage.MaxFieldLength-len("@example.com")) + "@example.com"
	err := s.applyDomainEvent(rt, context.Background(), app.ActionEvent{Type: app.ActionDial, Target: target, At: s.deps.Now()})
	if !errors.Is(err, storage.ErrInvalid) || !client.hasCommand("dial sip:"+target) {
		t.Fatalf("failed dial = %v, commands = %v", err, client.commandsCopy())
	}
	if len(s.Snapshot().History) != 0 || s.Snapshot().State.CallState != app.CallIdle {
		t.Fatal("invalid failed dial retained")
	}
	valid := sessionHistoryEntry(s.deps.Now(), "sip:valid@example.com")
	if err := s.appendCallHistory([]app.CallHistoryEntry{valid}); err != nil {
		t.Fatal(err)
	}
	assertHistoryStored(t, s, store, []app.CallHistoryEntry{valid})
}

func newHistoryTestSession(t *testing.T) (*Session, *runtime, *storage.Store, *fakeClient) {
	t.Helper()
	h := newHarness(t)
	store := storage.New(t.TempDir())
	h.session.deps.Store = store
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	client := newFakeClient(h.order)
	return h.session, &runtime{ctx: ctx, client: client}, store, client
}

func sessionHistoryEntry(at time.Time, target string) app.CallHistoryEntry {
	return app.CallHistoryEntry{
		Direction: app.CallDirectionIncoming,
		Outcome:   app.CallOutcomeMissed,
		Peer:      app.PeerDisplay(target),
		Target:    target,
		StartedAt: at,
		EndedAt:   at,
	}
}

func assertHistoryStored(t *testing.T, s *Session, store *storage.Store, want []app.CallHistoryEntry) {
	t.Helper()
	if got := s.Snapshot().History; !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot history = %#v, want %#v", got, want)
	}
	got, err := store.ReadCallHistory()
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("persisted history = %#v, %v; want %#v", got, err, want)
	}
}
