package session

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nibra/gosiptea/internal/app"
	"github.com/nibra/gosiptea/internal/audio"
	"github.com/nibra/gosiptea/internal/baresip"
	"github.com/nibra/gosiptea/internal/storage"
)

func newAudioHarness(t *testing.T) (*harness, context.Context) {
	t.Helper()
	h := newHarness(t)
	h.session.deps.Audio = fakeAudioLister{nodes: []audio.Node{
		{ID: 1, Name: "sink.old", Kind: audio.KindSink, Default: true},
		{ID: 2, Name: "sink.new", Kind: audio.KindSink},
		{ID: 3, Name: "source.old", Kind: audio.KindSource, Default: true},
		{ID: 4, Name: "source.new", Kind: audio.KindSource},
	}}
	ctx := context.Background()
	if err := h.session.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() { _ = h.session.Stop(ctx) })
	return h, ctx
}

func audioCommands(client *fakeClient) []string {
	var commands []string
	for _, command := range client.commandsCopy() {
		if strings.HasPrefix(command, "auplay ") || strings.HasPrefix(command, "ausrc ") {
			commands = append(commands, command)
		}
	}
	return commands
}

func TestSelectAudioValidatesEveryChangeBeforeApplying(t *testing.T) {
	t.Parallel()
	h, ctx := newAudioHarness(t)

	err := h.session.SelectAudio(ctx, storage.AudioConfig{Output: "sink.new", Input: "source.missing"})
	if !errors.Is(err, ErrAudioNodeMissing) {
		t.Fatalf("SelectAudio() error = %v, want ErrAudioNodeMissing", err)
	}
	if got := audioCommands(h.clients[0]); len(got) != 0 {
		t.Fatalf("audio commands after failed validation = %v", got)
	}
	if got := h.session.Snapshot().AudioConfig; got != (storage.AudioConfig{}) {
		t.Fatalf("audio config after failed validation = %#v", got)
	}
	if h.store.audioConfig != (storage.AudioConfig{}) {
		t.Fatalf("persisted audio config after failed validation = %#v", h.store.audioConfig)
	}
}

func TestSelectAudioRestoresOutputWhenInputCommandFails(t *testing.T) {
	t.Parallel()
	h, ctx := newAudioHarness(t)
	h.clients[0].commandErrors["ausrc"] = errors.New("ausrc failed")

	err := h.session.SelectAudio(ctx, storage.AudioConfig{Output: "sink.new", Input: "source.new"})
	if err == nil || !strings.Contains(err.Error(), "ausrc failed") {
		t.Fatalf("SelectAudio() error = %v", err)
	}
	want := []string{
		"auplay pipewire,sink.new",
		"ausrc pipewire,source.new",
		"auplay pipewire,sink.old",
	}
	if got := audioCommands(h.clients[0]); !reflect.DeepEqual(got, want) {
		t.Fatalf("audio commands = %v, want %v", got, want)
	}
	if got := h.session.Snapshot().AudioConfig; got != (storage.AudioConfig{}) {
		t.Fatalf("audio config after failed input = %#v", got)
	}
	if h.store.audioConfig != (storage.AudioConfig{}) {
		t.Fatalf("persisted audio config after failed input = %#v", h.store.audioConfig)
	}
}

func TestSelectAudioDeviceKeepsOtherSelection(t *testing.T) {
	t.Parallel()
	h, ctx := newAudioHarness(t)

	if err := h.session.SelectAudioDevice(ctx, audio.KindSink, "sink.new"); err != nil {
		t.Fatalf("select output: %v", err)
	}
	if err := h.session.SelectAudioDevice(ctx, audio.KindSource, "source.new"); err != nil {
		t.Fatalf("select input: %v", err)
	}
	want := storage.AudioConfig{Output: "sink.new", Input: "source.new"}
	if got := h.session.Snapshot().AudioConfig; got != want {
		t.Fatalf("audio config = %#v, want %#v", got, want)
	}
	if h.store.audioConfig != want {
		t.Fatalf("persisted audio config = %#v, want %#v", h.store.audioConfig, want)
	}
	want = storage.AudioConfig{Input: "source.new"}
	if err := h.session.SelectAudioDevice(ctx, audio.KindSink, ""); err != nil {
		t.Fatalf("select default output: %v", err)
	}
	if got := h.session.Snapshot().AudioConfig; got != want {
		t.Fatalf("audio config after default output = %#v, want %#v", got, want)
	}
}

func TestConcurrentAudioDeviceSelectionsDoNotRevertEachOther(t *testing.T) {
	t.Parallel()
	h, ctx := newAudioHarness(t)

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, selection := range []struct {
		kind audio.Kind
		name string
	}{{audio.KindSink, "sink.new"}, {audio.KindSource, "source.new"}} {
		wg.Go(func() { errs <- h.session.SelectAudioDevice(ctx, selection.kind, selection.name) })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("SelectAudioDevice() error = %v", err)
		}
	}
	want := storage.AudioConfig{Output: "sink.new", Input: "source.new"}
	if got := h.session.Snapshot().AudioConfig; got != want {
		t.Fatalf("audio config = %#v, want %#v", got, want)
	}
}

func TestSelectAudioDeviceRejectsUnknownKind(t *testing.T) {
	t.Parallel()
	h, ctx := newAudioHarness(t)
	if err := h.session.SelectAudioDevice(ctx, audio.Kind("video"), "camera"); err == nil {
		t.Fatal("SelectAudioDevice() accepted an unknown kind")
	}
	if got := audioCommands(h.clients[0]); len(got) != 0 {
		t.Fatalf("audio commands = %v", got)
	}
}

func TestIncomingCallPausesMediaWhenNotificationFails(t *testing.T) {
	t.Parallel()
	harness := newHarness(t)
	harness.notifier.err = errors.New("notify-send failed")
	ctx := context.Background()
	if err := harness.session.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer harness.session.Stop(ctx)

	harness.clients[0].events <- baresip.Event{
		"type":    "CALL_INCOMING",
		"id":      "media-2",
		"peeruri": "sip:alice@example.com",
	}
	waitFor(t, func() bool { return harness.media.count() == 1 })
	waitFor(t, func() bool { return strings.Contains(harness.session.Snapshot().LastError, "notify-send failed") })
	if state := harness.session.Snapshot().State; state.CallState != app.CallIncoming {
		t.Fatalf("call state = %v, want incoming", state.CallState)
	}
}

func TestDNDRejectedCallDoesNotPauseMedia(t *testing.T) {
	t.Parallel()
	harness := newHarness(t)
	ctx := context.Background()
	if err := harness.session.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer harness.session.Stop(ctx)
	if err := harness.session.ToggleDND(ctx); err != nil {
		t.Fatalf("ToggleDND() error = %v", err)
	}

	harness.clients[0].events <- baresip.Event{
		"type":    "CALL_INCOMING",
		"id":      "dnd-1",
		"peeruri": "sip:alice@example.com",
	}
	waitFor(t, func() bool { return harness.notifier.count() == 1 })
	time.Sleep(5 * time.Millisecond)
	if got := harness.media.count(); got != 0 {
		t.Fatalf("media pause calls for a DND-rejected call = %d, want 0", got)
	}
}

func TestSelectRingtoneSavesWithoutBaresipCommand(t *testing.T) {
	t.Parallel()
	h, ctx := newAudioHarness(t)

	if err := h.session.SelectRingtone(ctx, "sink.new"); err != nil {
		t.Fatalf("SelectRingtone() error = %v", err)
	}
	if got := audioCommands(h.clients[0]); len(got) != 0 {
		t.Fatalf("audio commands = %v, want none", got)
	}
	want := storage.AudioConfig{Alert: "sink.new"}
	if h.store.audioConfig != want {
		t.Fatalf("persisted audio config = %#v, want %#v", h.store.audioConfig, want)
	}
	snapshot := h.session.Snapshot()
	if snapshot.AudioConfig != want || !snapshot.RingtoneRestartRequired {
		t.Fatalf("snapshot audio = %#v, restart required = %v", snapshot.AudioConfig, snapshot.RingtoneRestartRequired)
	}

	// Choosing the device baresip already rings on clears the hint again.
	if err := h.session.SelectRingtone(ctx, ""); err != nil {
		t.Fatalf("SelectRingtone(\"\") error = %v", err)
	}
	if h.session.Snapshot().RingtoneRestartRequired {
		t.Fatal("restart still required after returning to the running ringtone device")
	}
}

func TestSelectRingtoneRejectsMissingOutput(t *testing.T) {
	t.Parallel()
	h, ctx := newAudioHarness(t)
	if err := h.session.SelectRingtone(ctx, "source.new"); !errors.Is(err, ErrAudioNodeMissing) {
		t.Fatalf("SelectRingtone(source) error = %v, want ErrAudioNodeMissing", err)
	}
	if h.store.audioConfig != (storage.AudioConfig{}) {
		t.Fatalf("persisted audio config = %#v", h.store.audioConfig)
	}
}

// baresip's auplay also moves the ringtone, so changing the call output while
// a separate ringtone is configured needs a restart.
func TestOutputChangeMovesRunningRingtone(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.store.audioConfig = storage.AudioConfig{Output: "sink.old", Alert: "sink.new"}
	h.session.deps.Audio = fakeAudioLister{nodes: []audio.Node{
		{ID: 1, Name: "sink.old", Kind: audio.KindSink, Default: true},
		{ID: 2, Name: "sink.new", Kind: audio.KindSink},
		{ID: 3, Name: "sink.third", Kind: audio.KindSink},
	}}
	ctx := context.Background()
	if err := h.session.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() { _ = h.session.Stop(ctx) })
	if h.session.Snapshot().RingtoneRestartRequired {
		t.Fatal("restart required right after start")
	}

	if err := h.session.SelectAudioDevice(ctx, audio.KindSink, "sink.third"); err != nil {
		t.Fatalf("SelectAudioDevice() error = %v", err)
	}
	snapshot := h.session.Snapshot()
	if !snapshot.RingtoneRestartRequired {
		t.Fatal("restart not required after auplay moved the ringtone")
	}
	if want := (storage.AudioConfig{Output: "sink.third", Alert: "sink.new"}); snapshot.AudioConfig != want {
		t.Fatalf("audio config = %#v, want %#v", snapshot.AudioConfig, want)
	}
}

func TestOutputChangeKeepsFollowingRingtone(t *testing.T) {
	t.Parallel()
	h, ctx := newAudioHarness(t)
	if err := h.session.SelectAudioDevice(ctx, audio.KindSink, "sink.new"); err != nil {
		t.Fatalf("SelectAudioDevice() error = %v", err)
	}
	if h.session.Snapshot().RingtoneRestartRequired {
		t.Fatal("restart required although the ringtone follows the output")
	}
}
