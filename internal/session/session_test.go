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

func TestLiveAudioDeviceUsesPipeWireDefault(t *testing.T) {
	t.Parallel()
	nodes := []audio.Node{
		{Name: "sink.other", Kind: audio.KindSink},
		{Name: "sink.default", Kind: audio.KindSink, Default: true},
		{Name: "source.default", Kind: audio.KindSource, Default: true},
	}

	if got, err := liveAudioDevice(nodes, audio.KindSink, ""); err != nil || got != "sink.default" {
		t.Fatalf("default sink = %q, %v", got, err)
	}
	if got, err := liveAudioDevice(nodes, audio.KindSource, "source.explicit"); err != nil || got != "source.explicit" {
		t.Fatalf("explicit source = %q, %v", got, err)
	}
	if _, err := liveAudioDevice(nodes, audio.Kind("video"), ""); !errors.Is(err, ErrAudioNodeMissing) {
		t.Fatalf("missing default error = %v", err)
	}
}

func TestTranslateEvent(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	contacts := []app.Contact{{Name: "Alice", URI: "sip:alice@example.com"}}

	event, err := TranslateEvent(baresip.Event{
		"class":           "call",
		"type":            "CALL_INCOMING",
		"id":              "call-7",
		"peeruri":         "sip:alice@example.com",
		"peerdisplayname": "Alice A.",
	}, contacts, "49", at)
	if err != nil {
		t.Fatalf("TranslateEvent() error = %v", err)
	}
	call, ok := event.(app.CallEvent)
	if !ok {
		t.Fatalf("TranslateEvent() type = %T, want app.CallEvent", event)
	}
	if call.Type != app.CallEventIncoming || call.ID != "call-7" || call.PeerURI != "sip:alice@example.com" || call.PeerDisplayName != "Alice A." || call.CountryCallingCode != "49" || !call.At.Equal(at) {
		t.Fatalf("translated call = %#v", call)
	}
	if !reflect.DeepEqual(call.Contacts, contacts) {
		t.Fatalf("translated contacts = %#v, want %#v", call.Contacts, contacts)
	}

	legacy, err := TranslateEvent(baresip.Event{
		"type":        "CALL_INCOMING",
		"peeruri":     "sip:legacy@example.com",
		"peerdisplay": "Legacy Caller",
	}, nil, "", at)
	if err != nil {
		t.Fatalf("TranslateEvent(legacy display) error = %v", err)
	}
	if got := legacy.(app.CallEvent).PeerDisplayName; got != "Legacy Caller" {
		t.Fatalf("legacy display = %q", got)
	}

	event, err = TranslateEvent(baresip.Event{
		"type":       "REGISTER_FAIL",
		"accountaor": "sip:100@example.com",
		"param":      "403 Forbidden",
	}, nil, "", at)
	if err != nil {
		t.Fatalf("TranslateEvent(register) error = %v", err)
	}
	registration := event.(app.RegisterEvent)
	if registration.Type != app.RegisterEventFail || registration.AccountAOR != "sip:100@example.com" || registration.Detail != "403 Forbidden" {
		t.Fatalf("translated registration = %#v", registration)
	}

	if _, err := TranslateEvent(baresip.Event{"type": "CALL_RINGING"}, nil, "", at); !errors.Is(err, ErrUnsupportedEvent) {
		t.Fatalf("unsupported error = %v", err)
	}
	if _, err := TranslateEvent(baresip.Event{"type": "CALL_CLOSED", "id": 7}, nil, "", at); !errors.Is(err, ErrMalformedEvent) {
		t.Fatalf("malformed error = %v", err)
	}
}

func TestReducerDispatchAndNotifications(t *testing.T) {
	t.Parallel()
	harness := newHarness(t)
	ctx := context.Background()
	if err := harness.session.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer harness.session.Stop(ctx)

	if err := harness.session.Dial(ctx, "123 45"); err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	if state := harness.session.Snapshot().State; state.CallState != app.CallOutgoing || state.Peer != "12345" {
		t.Fatalf("state after dial = %#v", state)
	}
	if !harness.clients[0].hasCommand("dial 12345") {
		t.Fatalf("commands = %#v, want dial", harness.clients[0].commandsCopy())
	}

	harness.clients[0].events <- baresip.Event{
		"type":    "CALL_ESTABLISHED",
		"id":      "out-1",
		"peeruri": "sip:12345@example.com",
	}
	waitFor(t, func() bool { return harness.session.Snapshot().State.CallState == app.CallActive })
	if err := harness.session.ToggleMute(ctx); err != nil {
		t.Fatalf("ToggleMute() error = %v", err)
	}
	if !harness.session.Snapshot().State.Muted || !harness.clients[0].hasCommand("mute") {
		t.Fatalf("mute state = %#v, commands = %#v", harness.session.Snapshot().State, harness.clients[0].commandsCopy())
	}

	harness.clients[0].events <- baresip.Event{"type": "CALL_CLOSED", "id": "out-1"}
	waitFor(t, func() bool { return harness.session.Snapshot().State.CallState == app.CallIdle })
	if history := harness.session.Snapshot().History; len(history) != 1 || history[0].Outcome != app.CallOutcomeConnected || history[0].Target != "12345" {
		t.Fatalf("outgoing history = %#v", history)
	}
	if err := harness.session.ToggleDND(ctx); err != nil {
		t.Fatalf("ToggleDND() error = %v", err)
	}
	harness.clients[0].events <- baresip.Event{
		"type":            "CALL_INCOMING",
		"id":              "in-1",
		"peeruri":         "sip:alice@example.com",
		"peerdisplayname": "Alice",
	}
	waitFor(t, func() bool { return harness.clients[0].countCommand("hangup") == 1 })
	if state := harness.session.Snapshot().State; state.CallState != app.CallIdle || !state.DND {
		t.Fatalf("DND incoming state = %#v", state)
	}
	waitFor(t, func() bool { return harness.notifier.count() == 1 })
	waitFor(t, func() bool { return len(harness.session.Snapshot().History) == 2 })
	if history := harness.session.Snapshot().History; history[0].Outcome != app.CallOutcomeRejectedDND || history[0].Target != "sip:alice@example.com" {
		t.Fatalf("DND history = %#v", history)
	}
	if got := harness.notifier.last(); got.summary != "Call rejected by do not disturb" || got.body != "Alice" {
		t.Fatalf("notification = %#v", got)
	}
}

func TestDialResolvesContactNameForHistory(t *testing.T) {
	t.Parallel()
	harness := newHarness(t)
	harness.store.contacts = []storage.Contact{{Name: "Alice", URI: "sip:alice@example.com"}}
	ctx := context.Background()
	if err := harness.session.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer harness.session.Stop(ctx)

	if err := harness.session.Dial(ctx, "sip:alice@example.com"); err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	if peer := harness.session.Snapshot().State.Peer; peer != "Alice" {
		t.Fatalf("dial peer = %q, want Alice", peer)
	}
	harness.clients[0].events <- baresip.Event{"type": "CALL_CLOSED", "id": "out-alice"}
	waitFor(t, func() bool { return len(harness.session.Snapshot().History) == 1 })
	if history := harness.session.Snapshot().History; history[0].Peer != "Alice" {
		t.Fatalf("history = %#v", history)
	}
}

func TestIncomingCallPausesMediaWithoutResumingIt(t *testing.T) {
	t.Parallel()
	harness := newHarness(t)
	ctx := context.Background()
	if err := harness.session.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer harness.session.Stop(ctx)

	harness.clients[0].events <- baresip.Event{
		"type":    "CALL_INCOMING",
		"id":      "media-1",
		"peeruri": "sip:alice@example.com",
	}
	waitFor(t, func() bool { return harness.media.count() == 1 })

	harness.clients[0].events <- baresip.Event{"type": "CALL_CLOSED", "id": "media-1"}
	waitFor(t, func() bool { return harness.session.Snapshot().State.CallState == app.CallIdle })
	time.Sleep(5 * time.Millisecond)
	if got := harness.media.count(); got != 1 {
		t.Fatalf("media pause calls after close = %d, want 1", got)
	}
}

func TestIncomingCallFocusesWindowOnlyWhileRinging(t *testing.T) {
	t.Parallel()
	harness := newHarness(t)
	ctx := context.Background()
	if err := harness.session.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer harness.session.Stop(ctx)

	harness.clients[0].events <- baresip.Event{
		"type":    "CALL_INCOMING",
		"id":      "focus-1",
		"peeruri": "sip:alice@example.com",
	}
	waitFor(t, func() bool { return harness.window.count() == 1 })

	harness.clients[0].events <- baresip.Event{"type": "CALL_CLOSED", "id": "focus-1"}
	waitFor(t, func() bool { return harness.session.Snapshot().State.CallState == app.CallIdle })
	time.Sleep(5 * time.Millisecond)
	if got := harness.window.count(); got != 1 {
		t.Fatalf("focus calls after close = %d, want 1", got)
	}
}

func TestRefusedHangupKeepsCallVisible(t *testing.T) {
	t.Parallel()
	harness := newHarness(t)
	ctx := context.Background()
	if err := harness.session.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer harness.session.Stop(ctx)

	harness.clients[0].responses["hangup"] = "no active call\n"
	harness.clients[0].events <- baresip.Event{
		"type":    "CALL_INCOMING",
		"id":      "reject-1",
		"peeruri": "sip:alice@example.com",
	}
	waitFor(t, func() bool { return harness.session.Snapshot().State.CallState == app.CallIncoming })

	err := harness.session.Hangup(ctx)
	if err == nil || !strings.Contains(err.Error(), "no active call") {
		t.Fatalf("Hangup() error = %v, want the baresip refusal", err)
	}
	if state := harness.session.Snapshot().State; state.CallState != app.CallIncoming {
		t.Fatalf("refused hangup left state = %#v, want the call still ringing", state)
	}
}

func TestCommandFailureDoesNotCommitReducerState(t *testing.T) {
	t.Parallel()
	harness := newHarness(t)
	ctx := context.Background()
	if err := harness.session.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer harness.session.Stop(ctx)

	commandErr := errors.New("dial rejected")
	harness.clients[0].commandErrors["dial"] = commandErr
	if err := harness.session.Dial(ctx, "123"); !errors.Is(err, commandErr) {
		t.Fatalf("Dial() error = %v, want command error", err)
	}
	if state := harness.session.Snapshot().State; state.CallState != app.CallIdle {
		t.Fatalf("failed dial committed state = %#v", state)
	}
	if history := harness.session.Snapshot().History; len(history) != 1 || history[0].Outcome != app.CallOutcomeNotConnected || history[0].Target != "123" {
		t.Fatalf("failed dial history = %#v", history)
	}
}

func TestLifecycleRetriesUsesConfigDirAndStopsInOrder(t *testing.T) {
	t.Parallel()
	harness := newHarness(t)
	harness.clientFailures = 2
	harness.session.config.ConnectAttempts = 3
	harness.session.config.ConnectInterval = time.Millisecond

	ctx := context.Background()
	if err := harness.session.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if harness.clientAttempts != 3 {
		t.Fatalf("client attempts = %d, want 3", harness.clientAttempts)
	}
	if len(harness.processOptions) != 1 {
		t.Fatalf("process options count = %d", len(harness.processOptions))
	}
	if got, want := harness.processOptions[0].Args, []string{"-f", harness.session.config.ConfigDir}; !reflect.DeepEqual(got, want) {
		t.Fatalf("process args = %#v, want %#v", got, want)
	}

	harness.clients[0].events <- baresip.Event{
		"type":    "CALL_INCOMING",
		"id":      "incoming-1",
		"peeruri": "sip:bob@example.com",
	}
	waitFor(t, func() bool { return harness.session.Snapshot().State.CallState == app.CallIncoming })
	if err := harness.session.Stop(ctx); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}

	order := harness.order.copy()
	hangup := indexOf(order, "client.command hangup")
	quit := indexOf(order, "client.command quit")
	closeClient := indexOf(order, "client.close")
	stopProcess := indexOf(order, "process.stop")
	if hangup < 0 || quit <= hangup || closeClient <= quit || stopProcess <= closeClient {
		t.Fatalf("shutdown order = %#v", order)
	}
	if harness.session.Snapshot().Running {
		t.Fatal("snapshot still reports running")
	}
	if history := harness.session.Snapshot().History; len(history) != 1 || history[0].Outcome != app.CallOutcomeRejected {
		t.Fatalf("shutdown history = %#v", history)
	}
}

func TestStartLoadsPersistedCallHistory(t *testing.T) {
	t.Parallel()
	harness := newHarness(t)
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	harness.store.history = []app.CallHistoryEntry{{
		Direction: app.CallDirectionIncoming,
		Outcome:   app.CallOutcomeMissed,
		Peer:      "Alice",
		Target:    "sip:alice@example.com",
		StartedAt: at,
		EndedAt:   at.Add(time.Second),
	}}
	if err := harness.session.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer harness.session.Stop(context.Background())

	snapshot := harness.session.Snapshot()
	if !reflect.DeepEqual(snapshot.History, harness.store.history) {
		t.Fatalf("loaded history = %#v, want %#v", snapshot.History, harness.store.history)
	}
	snapshot.History[0].Peer = "changed"
	if harness.session.Snapshot().History[0].Peer != "Alice" {
		t.Fatal("Snapshot returned a mutable history slice")
	}
}

func TestExistingOwnerRemainsHardStartError(t *testing.T) {
	t.Parallel()
	harness := newHarness(t)
	harness.processStartErr = baresip.ErrServiceOwned

	err := harness.session.Start(context.Background())
	if !errors.Is(err, baresip.ErrServiceOwned) {
		t.Fatalf("Start() error = %v, want ErrServiceOwned", err)
	}
	if harness.clientAttempts != 0 {
		t.Fatalf("client attempts = %d, want 0", harness.clientAttempts)
	}
	if harness.session.Snapshot().Running {
		t.Fatal("failed start reports running")
	}
}

func TestAccountWriteRestartsOnlyWhenIdle(t *testing.T) {
	t.Parallel()
	harness := newHarness(t)
	ctx := context.Background()
	if err := harness.session.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer harness.session.Stop(ctx)

	harness.clients[0].events <- baresip.Event{
		"type":    "CALL_INCOMING",
		"id":      "incoming-2",
		"peeruri": "sip:bob@example.com",
	}
	waitFor(t, func() bool { return harness.session.Snapshot().State.CallState == app.CallIncoming })
	credentials := storage.AccountCredentials{Server: "new.example.com", Username: "200", Password: "secret"}
	if err := harness.session.WriteAccount(ctx, credentials); !errors.Is(err, ErrCallActive) {
		t.Fatalf("WriteAccount(active) error = %v, want ErrCallActive", err)
	}
	if harness.store.accountWrites != 0 || len(harness.processes) != 1 {
		t.Fatalf("active write changed account or process: writes=%d processes=%d", harness.store.accountWrites, len(harness.processes))
	}

	harness.clients[0].events <- baresip.Event{"type": "CALL_CLOSED", "id": "incoming-2"}
	waitFor(t, func() bool { return harness.session.Snapshot().State.CallState == app.CallIdle })
	if err := harness.session.WriteAccount(ctx, credentials); err != nil {
		t.Fatalf("WriteAccount(idle) error = %v", err)
	}
	if harness.store.accountWrites != 1 || len(harness.processes) != 2 || len(harness.clients) != 2 {
		t.Fatalf("restart counts: writes=%d processes=%d clients=%d", harness.store.accountWrites, len(harness.processes), len(harness.clients))
	}
	if got := harness.session.Snapshot().Account.Username; got != "200" {
		t.Fatalf("snapshot account username = %q", got)
	}
}

type harness struct {
	t               *testing.T
	session         *Session
	store           *fakeStore
	notifier        *fakeNotifier
	media           *fakeMediaPauser
	window          *fakeWindowFocuser
	order           *recorder
	processes       []*fakeProcess
	clients         []*fakeClient
	processOptions  []baresip.ProcessOptions
	clientAttempts  int
	clientFailures  int
	processStartErr error
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{
		t:        t,
		store:    &fakeStore{account: storage.Account{Configured: true, Username: "100", Domain: "example.com", Server: "example.com", HasPassword: true, Secure: true}},
		notifier: &fakeNotifier{},
		media:    &fakeMediaPauser{},
		window:   &fakeWindowFocuser{},
		order:    &recorder{},
	}
	config := Config{
		ConfigDir:       t.TempDir(),
		ConnectAttempts: 2,
		ConnectInterval: time.Millisecond,
		CommandTimeout:  200 * time.Millisecond,
		StopTimeout:     200 * time.Millisecond,
	}
	dependencies := Dependencies{
		NewProcess: func(options baresip.ProcessOptions) Process {
			process := newFakeProcess(h.order)
			process.startErr = h.processStartErr
			h.processes = append(h.processes, process)
			h.processOptions = append(h.processOptions, options)
			return process
		},
		NewClient: func(context.Context) (baresip.Client, error) {
			h.clientAttempts++
			if h.clientAttempts <= h.clientFailures {
				return nil, baresip.ErrServiceUnavailable
			}
			client := newFakeClient(h.order)
			h.clients = append(h.clients, client)
			return client, nil
		},
		Store:    h.store,
		Audio:    fakeAudioLister{nodes: []audio.Node{{ID: 1, Name: "sink", Kind: audio.KindSink}, {ID: 2, Name: "source", Kind: audio.KindSource}}},
		Media:    h.media,
		Notifier: h.notifier,
		Window:   h.window,
		Now:      func() time.Time { return time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC) },
	}
	var err error
	h.session, err = NewWithDependencies(config, dependencies)
	if err != nil {
		t.Fatalf("NewWithDependencies() error = %v", err)
	}
	return h
}

type recorder struct {
	mu      sync.Mutex
	entries []string
}

func (r *recorder) add(entry string) {
	r.mu.Lock()
	r.entries = append(r.entries, entry)
	r.mu.Unlock()
}

func (r *recorder) copy() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.entries...)
}

type fakeProcess struct {
	order    *recorder
	done     chan struct{}
	startErr error
	err      error
	stopOnce sync.Once
}

func newFakeProcess(order *recorder) *fakeProcess {
	return &fakeProcess{order: order, done: make(chan struct{})}
}

func (p *fakeProcess) Start(context.Context) error {
	p.order.add("process.start")
	return p.startErr
}

func (p *fakeProcess) Stop(context.Context) error {
	p.order.add("process.stop")
	p.stopOnce.Do(func() { close(p.done) })
	return nil
}

func (p *fakeProcess) Done() <-chan struct{} { return p.done }
func (p *fakeProcess) Err() error            { return p.err }

type fakeClient struct {
	order         *recorder
	events        chan baresip.Event
	done          chan struct{}
	closeOnce     sync.Once
	mu            sync.Mutex
	commands      []string
	commandErrors map[string]error
	responses     map[string]string
	err           error
}

func newFakeClient(order *recorder) *fakeClient {
	return &fakeClient{
		order:         order,
		events:        make(chan baresip.Event, 16),
		done:          make(chan struct{}),
		commandErrors: make(map[string]error),
		responses:     make(map[string]string),
	}
}

func (c *fakeClient) Invoke(ctx context.Context, commandLine string) (string, error) {
	command, params, _ := strings.Cut(commandLine, " ")
	return c.Command(ctx, command, params)
}

func (c *fakeClient) Command(_ context.Context, command, params string) (string, error) {
	line := command
	if params != "" {
		line += " " + params
	}
	c.mu.Lock()
	c.commands = append(c.commands, line)
	c.mu.Unlock()
	c.order.add("client.command " + line)
	if err := c.commandErrors[command]; err != nil {
		return "", err
	}
	if response, ok := c.responses[command]; ok {
		return response, nil
	}
	if command == "reginfo" {
		return "User Agents (1)\n<sip:100@example.com> OK", nil
	}
	return "", nil
}

func (c *fakeClient) Events() <-chan baresip.Event { return c.events }
func (c *fakeClient) Done() <-chan struct{}        { return c.done }
func (c *fakeClient) Err() error                   { return c.err }

func (c *fakeClient) Close() error {
	c.order.add("client.close")
	c.closeOnce.Do(func() {
		close(c.done)
		close(c.events)
	})
	return nil
}

func (c *fakeClient) commandsCopy() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.commands...)
}

func (c *fakeClient) hasCommand(command string) bool {
	return c.countCommand(command) > 0
}

func (c *fakeClient) countCommand(command string) int {
	count := 0
	for _, got := range c.commandsCopy() {
		if got == command {
			count++
		}
	}
	return count
}

type fakeStore struct {
	contacts      []storage.Contact
	audioConfig   storage.AudioConfig
	account       storage.Account
	history       []app.CallHistoryEntry
	accountWrites int
	historyWrites int
}

func (s *fakeStore) ListContacts() (storage.ContactList, error) {
	return storage.ContactList{Configured: true, Contacts: append([]storage.Contact(nil), s.contacts...)}, nil
}

func (s *fakeStore) AddContact(contact storage.Contact) error {
	s.contacts = append(s.contacts, contact)
	return nil
}

func (s *fakeStore) RemoveContact(uri string) error {
	for i, contact := range s.contacts {
		if contact.URI == uri {
			s.contacts = append(s.contacts[:i], s.contacts[i+1:]...)
			return nil
		}
	}
	return storage.ErrNotFound
}

func (s *fakeStore) ReadAudioConfig() (storage.AudioConfig, error) { return s.audioConfig, nil }

func (s *fakeStore) WriteAudioConfig(config storage.AudioConfig) error {
	s.audioConfig = config
	return nil
}

func (s *fakeStore) ReadAccount() (storage.Account, error) { return s.account, nil }

func (s *fakeStore) ReadCallHistory() ([]app.CallHistoryEntry, error) {
	return append([]app.CallHistoryEntry(nil), s.history...), nil
}

func (s *fakeStore) WriteCallHistory(history []app.CallHistoryEntry) error {
	s.historyWrites++
	s.history = append([]app.CallHistoryEntry(nil), history...)
	return nil
}

func (s *fakeStore) WriteAccount(credentials storage.AccountCredentials) error {
	s.accountWrites++
	s.account = storage.Account{
		Configured:  true,
		Username:    credentials.Username,
		Domain:      credentials.Domain,
		Server:      credentials.Server,
		Login:       credentials.Login,
		HasPassword: credentials.Password != "",
		Secure:      true,
	}
	return nil
}

type fakeAudioLister struct {
	nodes []audio.Node
	err   error
}

func (l fakeAudioLister) List(context.Context) ([]audio.Node, error) {
	return append([]audio.Node(nil), l.nodes...), l.err
}

type fakeMediaPauser struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (p *fakeMediaPauser) PauseAll(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	return p.err
}

func (p *fakeMediaPauser) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

type fakeWindowFocuser struct {
	mu    sync.Mutex
	calls int
}

func (f *fakeWindowFocuser) Focus(context.Context) error {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return nil
}

func (f *fakeWindowFocuser) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type sentNotification struct {
	summary string
	body    string
}

type fakeNotifier struct {
	mu   sync.Mutex
	sent []sentNotification
}

func (n *fakeNotifier) Send(_ context.Context, summary, body string) error {
	n.mu.Lock()
	n.sent = append(n.sent, sentNotification{summary: summary, body: body})
	n.mu.Unlock()
	return nil
}

func (n *fakeNotifier) count() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.sent)
}

func (n *fakeNotifier) last() sentNotification {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.sent[len(n.sent)-1]
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition was not met before timeout")
}

func indexOf(values []string, value string) int {
	for i, candidate := range values {
		if candidate == value {
			return i
		}
	}
	return -1
}
