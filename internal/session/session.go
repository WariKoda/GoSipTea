// Package session coordinates one owned baresip process with application state
// and persisted configuration.
package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sync"
	"time"

	"github.com/nibra/gosiptea/internal/app"
	"github.com/nibra/gosiptea/internal/audio"
	"github.com/nibra/gosiptea/internal/baresip"
	"github.com/nibra/gosiptea/internal/desktop"
	"github.com/nibra/gosiptea/internal/media"
	"github.com/nibra/gosiptea/internal/notify"
	"github.com/nibra/gosiptea/internal/storage"
)

const (
	defaultConnectAttempts = 30
	defaultConnectInterval = 100 * time.Millisecond
	defaultCommandTimeout  = 10 * time.Second
	defaultStopTimeout     = 5 * time.Second
)

var (
	ErrAlreadyRunning   = errors.New("session: already running")
	ErrNotRunning       = errors.New("session: not running")
	ErrCallActive       = errors.New("session: account changes require an idle call")
	ErrContactNotFound  = errors.New("session: contact not found")
	ErrAudioNodeMissing = errors.New("session: selected audio node is not available")
)

// Config contains runtime settings. ConfigDir is required; session never falls
// back to ~/.baresip.
type Config struct {
	ConfigDir          string
	BaresipPath        string
	CountryCallingCode string
	// Log receives the output of the baresip child process. A nil writer
	// discards it, which is the default because the output contains call
	// metadata and, with siptrace enabled, complete SIP messages.
	Log             io.Writer
	SIPTrace        bool
	ConnectAttempts int
	ConnectInterval time.Duration
	CommandTimeout  time.Duration
	StopTimeout     time.Duration
}

// Process is the child-process subset used by Session.
type Process interface {
	Start(context.Context) error
	Stop(context.Context) error
	Done() <-chan struct{}
	Err() error
}

// Store is the persisted configuration used by Session.
type Store interface {
	ListContacts() (storage.ContactList, error)
	AddContact(storage.Contact) error
	RemoveContact(string) error
	ReadAudioConfig() (storage.AudioConfig, error)
	WriteAudioConfig(storage.AudioConfig) error
	ReadAccount() (storage.Account, error)
	WriteAccount(storage.AccountCredentials) error
	ReadCallHistory() ([]app.CallHistoryEntry, error)
	WriteCallHistory([]app.CallHistoryEntry) error
}

// AudioLister discovers PipeWire nodes.
type AudioLister interface {
	List(context.Context) ([]audio.Node, error)
}

// Notifier sends a desktop notification.
type Notifier interface {
	Send(context.Context, string, string) error
}

// MediaPauser pauses desktop media players when a call arrives.
type MediaPauser interface {
	PauseAll(context.Context) error
}

// WindowFocuser raises the window hosting the application when a call arrives.
type WindowFocuser interface {
	Focus(context.Context) error
}

// Dependencies replace process, D-Bus, storage, audio, media, notification,
// window, and time adapters in tests.
type Dependencies struct {
	NewProcess func(baresip.ProcessOptions) Process
	NewClient  func(context.Context) (baresip.Client, error)
	Store      Store
	Audio      AudioLister
	Media      MediaPauser
	Notifier   Notifier
	Window     WindowFocuser
	Now        func() time.Time
}

// Snapshot is an immutable view returned to callers. Slice fields are copied
// by Snapshot and before publication to subscribers.
type Snapshot struct {
	State       app.State
	Contacts    []storage.Contact
	AudioNodes  []audio.Node
	AudioConfig storage.AudioConfig
	Account     storage.Account
	History     []app.CallHistoryEntry
	Running     bool
	LastError   string
	Revision    uint64
}

type requestKind uint8

const (
	requestAction requestKind = iota
	requestReloadContacts
	requestAddContact
	requestRemoveContact
	requestDialContact
	requestListAudio
	requestPersistAudio
	requestApplyAudio
	requestSelectAudio
	requestReadAccount
	requestWriteAccount
	requestStop
)

type request struct {
	ctx     context.Context
	kind    requestKind
	action  app.ActionEvent
	contact storage.Contact
	text    string
	audio   storage.AudioConfig
	account storage.AccountCredentials
	result  chan requestResult
}

type requestResult struct {
	value any
	err   error
}

type runtime struct {
	ctx      context.Context
	cancel   context.CancelFunc
	requests chan request
	done     chan struct{}
	process  Process
	client   baresip.Client
}

// Session owns at most one child process and serializes all reducer activity.
type Session struct {
	config Config
	deps   Dependencies

	lifecycleMu sync.Mutex
	mu          sync.RWMutex
	snapshot    Snapshot
	active      *runtime

	subMu       sync.Mutex
	subscribers map[uint64]chan Snapshot
	nextSubID   uint64
}

// New constructs a production session rooted at config.ConfigDir.
func New(config Config) (*Session, error) {
	if config.ConfigDir == "" {
		return nil, errors.New("session: config directory is required")
	}
	store := storage.New(filepath.Clean(config.ConfigDir))
	return NewWithDependencies(config, Dependencies{
		NewProcess: func(options baresip.ProcessOptions) Process {
			return baresip.NewProcessManager(options)
		},
		NewClient: func(ctx context.Context) (baresip.Client, error) {
			return baresip.NewClient(ctx)
		},
		Store:    store,
		Audio:    audio.NewLister(),
		Media:    media.NewPauser(),
		Notifier: notify.New(notify.DefaultAppName),
		Window:   desktop.NewFocuser(),
		Now:      time.Now,
	})
}

// NewWithDependencies constructs a session without touching the filesystem,
// D-Bus, PipeWire, or the process table.
func NewWithDependencies(config Config, dependencies Dependencies) (*Session, error) {
	if config.ConfigDir == "" {
		return nil, errors.New("session: config directory is required")
	}
	if dependencies.NewProcess == nil || dependencies.NewClient == nil || dependencies.Store == nil || dependencies.Audio == nil || dependencies.Media == nil || dependencies.Notifier == nil || dependencies.Window == nil {
		return nil, errors.New("session: incomplete dependencies")
	}
	if dependencies.Now == nil {
		dependencies.Now = time.Now
	}
	config.ConfigDir = filepath.Clean(config.ConfigDir)
	if config.ConnectAttempts <= 0 {
		config.ConnectAttempts = defaultConnectAttempts
	}
	if config.ConnectInterval <= 0 {
		config.ConnectInterval = defaultConnectInterval
	}
	if config.CommandTimeout <= 0 {
		config.CommandTimeout = defaultCommandTimeout
	}
	if config.StopTimeout <= 0 {
		config.StopTimeout = defaultStopTimeout
	}

	return &Session{
		config:      config,
		deps:        dependencies,
		snapshot:    Snapshot{State: app.NewState()},
		subscribers: make(map[uint64]chan Snapshot),
	}, nil
}

// Start loads persisted state, starts an owned baresip child with
// "-f <config dir>", and connects to that child's D-Bus service.
func (s *Session) Start(ctx context.Context) error {
	if ctx == nil {
		return errors.New("session: nil context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()

	s.mu.RLock()
	alreadyRunning := s.active != nil
	s.mu.RUnlock()
	if alreadyRunning {
		return ErrAlreadyRunning
	}

	contacts, audioConfig, account, history, err := s.loadPersistedState()
	if err != nil {
		return err
	}

	runCtx, cancel := context.WithCancel(context.Background())
	rt := &runtime{
		ctx:      runCtx,
		cancel:   cancel,
		requests: make(chan request),
		done:     make(chan struct{}),
	}
	process, client, err := s.launch(rt.ctx, ctx)
	if err != nil {
		cancel()
		return err
	}
	rt.process = process
	rt.client = client

	state := app.NewState()
	s.mu.RLock()
	state.DND = s.snapshot.State.DND
	s.mu.RUnlock()
	startupErr := s.registrationState(rt, &state)

	s.mu.Lock()
	s.active = rt
	s.mu.Unlock()
	s.updateSnapshot(func(snapshot *Snapshot) {
		snapshot.State = state
		snapshot.Contacts = contacts
		snapshot.AudioConfig = audioConfig
		snapshot.Account = account
		snapshot.History = history
		snapshot.AudioNodes = nil
		snapshot.Running = true
		snapshot.LastError = errorText(startupErr)
	})

	go s.run(rt)
	return nil
}

// Stop hangs up an active call, closes D-Bus, and then stops the owned child.
func (s *Session) Stop(ctx context.Context) error {
	if ctx == nil {
		return errors.New("session: nil context")
	}

	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()

	s.mu.RLock()
	rt := s.active
	s.mu.RUnlock()
	if rt == nil {
		return nil
	}

	result, requestErr := s.sendRequest(rt, request{ctx: ctx, kind: requestStop})
	select {
	case <-rt.done:
		if requestErr != nil {
			return requestErr
		}
		return result.err
	case <-ctx.Done():
		rt.cancel()
		return ctx.Err()
	}
}

// Snapshot returns a race-safe copy of current runtime state.
func (s *Session) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneSnapshot(s.snapshot)
}

// Subscribe returns snapshots after each state change and an unsubscribe
// function. The channel receives the current snapshot immediately. A slow
// subscriber keeps its bounded backlog and then receives the newest update.
func (s *Session) Subscribe(buffer int) (<-chan Snapshot, func()) {
	if buffer < 1 {
		buffer = 1
	}
	updates := make(chan Snapshot, buffer)

	s.subMu.Lock()
	id := s.nextSubID
	s.nextSubID++
	s.subscribers[id] = updates
	updates <- s.Snapshot()
	s.subMu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			s.subMu.Lock()
			if channel, ok := s.subscribers[id]; ok {
				delete(s.subscribers, id)
				close(channel)
			}
			s.subMu.Unlock()
		})
	}
	return updates, cancel
}

func (s *Session) loadPersistedState() ([]storage.Contact, storage.AudioConfig, storage.Account, []app.CallHistoryEntry, error) {
	contactList, err := s.deps.Store.ListContacts()
	if err != nil {
		return nil, storage.AudioConfig{}, storage.Account{}, nil, fmt.Errorf("session: load contacts: %w", err)
	}
	audioConfig, err := s.deps.Store.ReadAudioConfig()
	if err != nil {
		return nil, storage.AudioConfig{}, storage.Account{}, nil, fmt.Errorf("session: load audio config: %w", err)
	}
	account, err := s.deps.Store.ReadAccount()
	if err != nil {
		return nil, storage.AudioConfig{}, storage.Account{}, nil, fmt.Errorf("session: load account: %w", err)
	}
	history, err := s.deps.Store.ReadCallHistory()
	if err != nil {
		return nil, storage.AudioConfig{}, storage.Account{}, nil, fmt.Errorf("session: load call history: %w", err)
	}
	return append([]storage.Contact(nil), contactList.Contacts...), audioConfig, account, append([]app.CallHistoryEntry(nil), history...), nil
}

func (s *Session) launch(lifetimeCtx, startupCtx context.Context) (Process, baresip.Client, error) {
	var log io.Writer
	if s.config.Log != nil {
		// Stdout and Stderr are written by separate goroutines inside os/exec.
		log = newSyncWriter(s.config.Log)
	}
	args := []string{"-f", s.config.ConfigDir}
	if s.config.SIPTrace {
		args = append(args, "-s")
	}
	options := baresip.ProcessOptions{
		Path:        s.config.BaresipPath,
		Args:        args,
		Stdout:      log,
		Stderr:      log,
		StopTimeout: s.config.StopTimeout,
		OwnerChecker: baresip.OwnerCheckFunc(func(context.Context) (bool, error) {
			return baresip.ServiceHasOwner(startupCtx)
		}),
	}
	process := s.deps.NewProcess(options)
	if process == nil {
		return nil, nil, errors.New("session: process factory returned nil")
	}
	if err := process.Start(lifetimeCtx); err != nil {
		return nil, nil, fmt.Errorf("session: start baresip: %w", err)
	}

	client, err := s.connect(lifetimeCtx, startupCtx, process)
	if err == nil {
		return process, client, nil
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), s.config.StopTimeout)
	defer cancel()
	stopErr := process.Stop(stopCtx)
	return nil, nil, errors.Join(err, stopErr)
}

func (s *Session) connect(lifetimeCtx, startupCtx context.Context, process Process) (baresip.Client, error) {
	var lastErr error
	for attempt := 1; attempt <= s.config.ConnectAttempts; attempt++ {
		if err := startupCtx.Err(); err != nil {
			return nil, err
		}
		client, err := s.deps.NewClient(lifetimeCtx)
		if err == nil {
			if client == nil {
				return nil, errors.New("session: client factory returned nil")
			}
			return client, nil
		}
		lastErr = err
		if attempt == s.config.ConnectAttempts {
			break
		}

		timer := time.NewTimer(s.config.ConnectInterval)
		select {
		case <-timer.C:
		case <-startupCtx.Done():
			stopAndDrainTimer(timer)
			return nil, startupCtx.Err()
		case <-lifetimeCtx.Done():
			stopAndDrainTimer(timer)
			return nil, lifetimeCtx.Err()
		case <-process.Done():
			stopAndDrainTimer(timer)
			processErr := process.Err()
			if processErr == nil {
				processErr = errors.New("baresip exited before D-Bus became available")
			}
			return nil, fmt.Errorf("session: connect to baresip: %w", processErr)
		}
	}
	return nil, fmt.Errorf("session: connect to baresip after %d attempts: %w", s.config.ConnectAttempts, lastErr)
}

func (s *Session) registrationState(rt *runtime, state *app.State) error {
	ctx, cancel := context.WithTimeout(rt.ctx, s.config.CommandTimeout)
	defer cancel()
	response, err := rt.client.Command(ctx, "reginfo", "")
	if err != nil {
		return fmt.Errorf("session: read registration state: %w", err)
	}
	info := app.ParseRegistrationOutput(response)
	if !info.Known {
		return errors.New("session: baresip returned an unrecognized reginfo response")
	}
	transition := app.Reduce(*state, app.RegistrationSnapshot{Info: info, At: s.deps.Now()})
	*state = transition.State
	return transition.Err
}

func (s *Session) updateSnapshot(update func(*Snapshot)) Snapshot {
	s.mu.Lock()
	update(&s.snapshot)
	s.snapshot.Revision++
	current := cloneSnapshot(s.snapshot)
	s.mu.Unlock()
	s.publish(current)
	return current
}

func (s *Session) publish(snapshot Snapshot) {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	for _, updates := range s.subscribers {
		copy := cloneSnapshot(snapshot)
		select {
		case updates <- copy:
		default:
			<-updates
			updates <- copy
		}
	}
}

func cloneSnapshot(snapshot Snapshot) Snapshot {
	snapshot.Contacts = append([]storage.Contact(nil), snapshot.Contacts...)
	snapshot.AudioNodes = append([]audio.Node(nil), snapshot.AudioNodes...)
	snapshot.History = append([]app.CallHistoryEntry(nil), snapshot.History...)
	return snapshot
}

func stopAndDrainTimer(timer *time.Timer) {
	if timer.Stop() {
		return
	}
	select {
	case <-timer.C:
	default:
	}
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
