package session

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nibra/gosiptea/internal/app"
	"github.com/nibra/gosiptea/internal/audio"
	"github.com/nibra/gosiptea/internal/baresip"
	"github.com/nibra/gosiptea/internal/desktop"
	"github.com/nibra/gosiptea/internal/storage"
)

func (s *Session) run(rt *runtime) {
	var terminalErr error
	defer func() {
		rt.cancel()
		s.finishRuntime(rt, terminalErr)
		close(rt.done)
	}()

	for {
		select {
		case req := <-rt.requests:
			result, terminate := s.handleRequest(rt, req)
			req.result <- result
			if terminate {
				terminalErr = result.err
				return
			}
		case raw, ok := <-rt.client.Events():
			if !ok {
				terminalErr = s.clientTerminalError(rt.client)
				terminalErr = errors.Join(terminalErr, s.cleanupRuntime(rt, false, context.Background()))
				return
			}
			if err := s.handleBaresipEvent(rt, raw); err != nil && !errors.Is(err, ErrUnsupportedEvent) {
				s.recordError(err)
			}
		case <-rt.client.Done():
			terminalErr = s.clientTerminalError(rt.client)
			terminalErr = errors.Join(terminalErr, s.cleanupRuntime(rt, false, context.Background()))
			return
		case <-rt.process.Done():
			terminalErr = rt.process.Err()
			if terminalErr == nil {
				terminalErr = errors.New("session: baresip exited unexpectedly")
			} else {
				terminalErr = fmt.Errorf("session: baresip exited: %w", terminalErr)
			}
			if rt.client != nil {
				terminalErr = errors.Join(terminalErr, rt.client.Close())
				rt.client = nil
			}
			rt.process = nil
			return
		case <-rt.ctx.Done():
			terminalErr = s.cleanupRuntime(rt, true, context.Background())
			return
		}
	}
}

func (s *Session) domainContacts() []app.Contact {
	snapshot := s.Snapshot()
	contacts := make([]app.Contact, len(snapshot.Contacts))
	for i, contact := range snapshot.Contacts {
		contacts[i] = app.Contact{URI: contact.URI, Name: contact.Name}
	}
	return contacts
}

func (s *Session) withDialContacts(action app.ActionEvent) app.ActionEvent {
	if action.Type == app.ActionDial {
		action.Contacts = s.domainContacts()
		action.CountryCallingCode = s.config.CountryCallingCode
	}
	return action
}

func (s *Session) handleBaresipEvent(rt *runtime, raw baresip.Event) error {
	event, err := TranslateEvent(raw, s.domainContacts(), s.config.CountryCallingCode, s.deps.Now())
	if err != nil {
		return err
	}
	return s.applyDomainEvent(rt, rt.ctx, event)
}

func (s *Session) pauseMedia(ctx context.Context) {
	go func() {
		pauseCtx, cancel := context.WithTimeout(ctx, s.config.CommandTimeout)
		defer cancel()
		if err := s.deps.Media.PauseAll(pauseCtx); err != nil && ctx.Err() == nil {
			s.recordError(fmt.Errorf("session: pause media: %w", err))
		}
	}()
}

// focusWindow brings the application to the foreground so the ringing call is
// visible without switching workspace by hand.
func (s *Session) focusWindow(ctx context.Context) {
	go func() {
		focusCtx, cancel := context.WithTimeout(ctx, s.config.CommandTimeout)
		defer cancel()
		err := s.deps.Window.Focus(focusCtx)
		if err != nil && !errors.Is(err, desktop.ErrUnsupported) && ctx.Err() == nil {
			s.recordError(fmt.Errorf("session: focus window: %w", err))
		}
	}()
}

func (s *Session) applyDomainEvent(rt *runtime, ctx context.Context, event app.Event) error {
	currentSnapshot := s.Snapshot()
	transition := app.Reduce(currentSnapshot.State, event)
	if transition.Err != nil {
		return transition.Err
	}
	if err := s.executeCommands(rt.client, ctx, transition.Commands); err != nil {
		var entries []app.CallHistoryEntry
		if failed, ok := failedDialHistory(event); ok {
			entries = append(entries, failed)
		}
		return errors.Join(err, s.appendCallHistory(entries))
	}

	history, historyErr := mergeCallHistory(currentSnapshot.History, transition.History)
	if len(transition.History) > 0 || historyErr != nil {
		if err := s.deps.Store.WriteCallHistory(history); err != nil {
			historyErr = errors.Join(historyErr, fmt.Errorf("session: persist call history: %w", err))
		}
	}
	s.updateSnapshot(func(snapshot *Snapshot) {
		snapshot.State = transition.State
		snapshot.History = history
	})

	var notificationErrors []error
	for _, notification := range transition.Notifications {
		// A failed desktop notification must not keep media playing while the
		// call rings, so both side effects start before Send.
		if notification.Kind == app.NotificationIncoming {
			s.focusWindow(ctx)
			s.pauseMedia(ctx)
		}
		summary := notificationSummary(notification.Kind)
		notifyCtx, cancel := context.WithTimeout(ctx, s.config.CommandTimeout)
		err := s.deps.Notifier.Send(notifyCtx, summary, app.NotificationText(notification.Body))
		cancel()
		if err != nil {
			notificationErrors = append(notificationErrors, fmt.Errorf("session: send %s notification: %w", notification.Kind, err))
		}
	}
	return errors.Join(historyErr, errors.Join(notificationErrors...))
}

func (s *Session) appendCallHistory(entries []app.CallHistoryEntry) error {
	if len(entries) == 0 {
		return nil
	}
	history, historyErr := mergeCallHistory(s.Snapshot().History, entries)
	if err := s.deps.Store.WriteCallHistory(history); err != nil {
		historyErr = errors.Join(historyErr, fmt.Errorf("session: persist call history: %w", err))
	}
	s.updateSnapshot(func(snapshot *Snapshot) {
		snapshot.History = history
	})
	return historyErr
}

func mergeCallHistory(current, added []app.CallHistoryEntry) ([]app.CallHistoryEntry, error) {
	limit := min(storage.MaxCallHistoryEntries, len(added)+len(current))
	result := make([]app.CallHistoryEntry, 0, limit)
	var historyErr error
	for _, entries := range [][]app.CallHistoryEntry{added, current} {
		for _, entry := range entries {
			entry.Peer = app.ClampText(entry.Peer, app.MaxPeerDisplayLength)
			entry.StartedAt = entry.StartedAt.UTC()
			entry.ConnectedAt = entry.ConnectedAt.UTC()
			entry.EndedAt = entry.EndedAt.UTC()
			// Invalid targets must not become truncated redial addresses. Drop
			// the entry, not the call event, so signaling can still complete.
			if err := storage.ValidateCallHistoryEntry(entry); err != nil {
				historyErr = errors.Join(historyErr, fmt.Errorf("session: discarded invalid call history entry: %w: %v", storage.ErrInvalid, err))
				continue
			}
			result = append(result, entry)
			if len(result) == limit {
				return result, historyErr
			}
		}
	}
	return result, historyErr
}

func failedDialHistory(event app.Event) (app.CallHistoryEntry, bool) {
	var action app.ActionEvent
	switch typed := event.(type) {
	case app.ActionEvent:
		action = typed
	case *app.ActionEvent:
		if typed == nil {
			return app.CallHistoryEntry{}, false
		}
		action = *typed
	default:
		return app.CallHistoryEntry{}, false
	}
	if action.Type != app.ActionDial {
		return app.CallHistoryEntry{}, false
	}
	target := app.NormalizeDialTarget(action.Target)
	if target == "" {
		return app.CallHistoryEntry{}, false
	}
	peer, ok := app.ContactName(action.Contacts, target, action.CountryCallingCode)
	if !ok {
		peer = app.PeerDisplay(target)
	}
	return app.CallHistoryEntry{
		Direction: app.CallDirectionOutgoing,
		Outcome:   app.CallOutcomeNotConnected,
		Peer:      peer,
		Target:    target,
		StartedAt: action.At,
		EndedAt:   action.At,
	}, true
}

func (s *Session) finalizeActiveCall(at time.Time, endRequested bool) error {
	current := s.Snapshot()
	if current.State.CallState == app.CallIdle {
		return nil
	}
	if endRequested {
		current.State.EndRequested = true
	}
	transition := app.Reduce(current.State, app.CallEvent{Type: app.CallEventClosed, ID: current.State.CallID, At: at})
	history, historyErr := mergeCallHistory(current.History, transition.History)
	if err := s.deps.Store.WriteCallHistory(history); err != nil {
		historyErr = errors.Join(historyErr, fmt.Errorf("session: persist call history: %w", err))
	}
	s.updateSnapshot(func(snapshot *Snapshot) {
		snapshot.State = transition.State
		snapshot.History = history
	})
	return historyErr
}

func (s *Session) executeCommands(client baresip.Client, ctx context.Context, commands []app.Command) error {
	for _, command := range commands {
		var name string
		params := command.Parameter
		switch command.Kind {
		case app.CommandDial:
			name = "dial"
		case app.CommandAccept:
			name = "accept"
		case app.CommandHangup:
			name = "hangup"
		case app.CommandReject:
			name = "hangup"
			// 486 rejects only this endpoint; 603 declines the call globally.
			params = "scode=603 reason=Decline"
		case app.CommandMute:
			name = "mute"
		default:
			return fmt.Errorf("session: unsupported app command %q", command.Kind)
		}
		commandCtx, cancel := context.WithTimeout(ctx, s.config.CommandTimeout)
		response, err := client.Command(commandCtx, name, params)
		cancel()
		if err != nil {
			return fmt.Errorf("session: execute %s: %w", command.Kind, err)
		}
		if outputErr := app.ParseCallCommandError(response); outputErr != "" {
			return fmt.Errorf("session: execute %s: %s", command.Kind, outputErr)
		}
	}
	return nil
}

func notificationSummary(kind app.NotificationKind) string {
	switch kind {
	case app.NotificationIncoming:
		return "Incoming call"
	case app.NotificationMissed:
		return "Missed call"
	case app.NotificationRejectedDND:
		return "Call rejected by do not disturb"
	case app.NotificationRejectedBusy:
		return "Call rejected while busy"
	default:
		return "SIP call"
	}
}

func (s *Session) handleRequest(rt *runtime, req request) (requestResult, bool) {
	if err := req.ctx.Err(); err != nil {
		return requestResult{err: err}, false
	}

	var result requestResult
	var terminate bool
	switch req.kind {
	case requestAction:
		result.err = s.applyDomainEvent(rt, req.ctx, s.withDialContacts(req.action))
	case requestReloadContacts:
		result.value, result.err = s.reloadContacts()
	case requestAddContact:
		result.value, result.err = s.addContact(req.contact)
	case requestRemoveContact:
		result.value, result.err = s.removeContact(req.text)
	case requestDialContact:
		result.err = s.dialContact(rt, req.ctx, req.text)
	case requestListAudio:
		result.value, result.err = s.listAudio(req.ctx)
	case requestPersistAudio:
		result.err = s.persistAudio(req.audio)
	case requestApplyAudio:
		result.err = s.applySelectedAudio(rt, req.ctx, req.audio, true)
	case requestSelectAudio:
		result.err = s.selectAudio(rt, req.ctx, req.audio)
	case requestSelectAudioDevice:
		result.err = s.selectAudioDevice(rt, req.ctx, req.audioKind, req.text)
	case requestReadAccount:
		result.value, result.err = s.readAccount()
	case requestWriteAccount:
		result.err, terminate = s.writeAccount(rt, req.ctx, req.account)
	case requestStop:
		result.err = s.cleanupRuntime(rt, true, req.ctx)
		terminate = true
	default:
		result.err = errors.New("session: unknown request")
	}
	if result.err != nil && !terminate {
		s.recordError(result.err)
	} else if result.err == nil && !terminate {
		s.clearError()
	}
	return result, terminate
}

func (s *Session) reloadContacts() ([]storage.Contact, error) {
	list, err := s.deps.Store.ListContacts()
	if err != nil {
		return nil, fmt.Errorf("session: read contacts: %w", err)
	}
	contacts := append([]storage.Contact(nil), list.Contacts...)
	s.updateSnapshot(func(snapshot *Snapshot) {
		snapshot.Contacts = contacts
	})
	return append([]storage.Contact(nil), contacts...), nil
}

func (s *Session) addContact(contact storage.Contact) ([]storage.Contact, error) {
	if err := s.deps.Store.AddContact(contact); err != nil {
		return nil, fmt.Errorf("session: add contact: %w", err)
	}
	return s.reloadContacts()
}

func (s *Session) removeContact(uri string) ([]storage.Contact, error) {
	if err := s.deps.Store.RemoveContact(uri); err != nil {
		return nil, fmt.Errorf("session: remove contact: %w", err)
	}
	return s.reloadContacts()
}

func (s *Session) dialContact(rt *runtime, ctx context.Context, uri string) error {
	for _, contact := range s.Snapshot().Contacts {
		if contact.URI == uri {
			return s.applyDomainEvent(rt, ctx, s.withDialContacts(app.ActionEvent{Type: app.ActionDial, Target: contact.URI, At: s.deps.Now()}))
		}
	}
	return fmt.Errorf("%w: %s", ErrContactNotFound, uri)
}

func (s *Session) listAudio(ctx context.Context) ([]audio.Node, error) {
	listCtx, cancel := context.WithTimeout(ctx, s.config.CommandTimeout)
	nodes, err := s.deps.Audio.List(listCtx)
	cancel()
	if err != nil {
		return nil, err
	}
	nodes = append([]audio.Node(nil), nodes...)
	s.updateSnapshot(func(snapshot *Snapshot) {
		snapshot.AudioNodes = nodes
	})
	return append([]audio.Node(nil), nodes...), nil
}

func (s *Session) persistAudio(config storage.AudioConfig) error {
	config = normalizeAudioConfig(config)
	if err := s.deps.Store.WriteAudioConfig(config); err != nil {
		return fmt.Errorf("session: persist audio selection: %w", err)
	}
	s.updateSnapshot(func(snapshot *Snapshot) {
		snapshot.AudioConfig = config
	})
	return nil
}

func (s *Session) applySelectedAudio(rt *runtime, ctx context.Context, config storage.AudioConfig, refresh bool) error {
	config = normalizeAudioConfig(config)
	var nodes []audio.Node
	var err error
	if refresh {
		nodes, err = s.listAudio(ctx)
		if err != nil {
			return err
		}
	} else {
		nodes = s.Snapshot().AudioNodes
	}
	if err := validateAudioSelection(nodes, config); err != nil {
		return err
	}
	output, err := liveAudioDevice(nodes, audio.KindSink, config.Output)
	if err != nil {
		return err
	}
	input, err := liveAudioDevice(nodes, audio.KindSource, config.Input)
	if err != nil {
		return err
	}
	if err := s.applyAudioCommand(rt, ctx, "auplay", output); err != nil {
		return err
	}
	return s.applyAudioCommand(rt, ctx, "ausrc", input)
}

func (s *Session) applyAudioCommand(rt *runtime, ctx context.Context, command, device string) error {
	commandCtx, cancel := context.WithTimeout(ctx, s.config.CommandTimeout)
	response, err := rt.client.Command(commandCtx, command, "pipewire,"+device)
	cancel()
	if err != nil {
		return fmt.Errorf("session: apply %s: %w", command, err)
	}
	if outputErr := app.ParseAudioCommandError(response); outputErr != "" {
		return fmt.Errorf("session: apply %s: %s", command, outputErr)
	}
	return nil
}

func (s *Session) selectAudio(rt *runtime, ctx context.Context, config storage.AudioConfig) error {
	config = normalizeAudioConfig(config)
	current := s.Snapshot().AudioConfig
	changeOutput := config.Output != current.Output
	changeInput := config.Input != current.Input
	if !changeOutput && !changeInput {
		return nil
	}

	nodes, err := s.listAudio(ctx)
	if err != nil {
		return err
	}

	// Resolve every change before the first command, so a missing input cannot
	// leave only the output switched.
	var changes []audioChange
	if changeOutput {
		change, err := resolveAudioChange(nodes, "auplay", audio.KindSink, config.Output, current.Output)
		if err != nil {
			return err
		}
		changes = append(changes, change)
	}
	if changeInput {
		change, err := resolveAudioChange(nodes, "ausrc", audio.KindSource, config.Input, current.Input)
		if err != nil {
			return err
		}
		changes = append(changes, change)
	}
	for i, change := range changes {
		if err := s.applyAudioCommand(rt, ctx, change.command, change.device); err != nil {
			return errors.Join(err, s.restoreAudio(rt, ctx, changes[:i]))
		}
	}
	if err := s.persistAudio(config); err != nil {
		return fmt.Errorf("session: audio changed for this process but was not saved: %w", err)
	}
	return nil
}

// selectAudioDevice changes one direction and keeps the other selection. The
// merge runs on the runtime goroutine, so selections cannot revert each other.
func (s *Session) selectAudioDevice(rt *runtime, ctx context.Context, kind audio.Kind, name string) error {
	config := s.Snapshot().AudioConfig
	switch kind {
	case audio.KindSink:
		config.Output = name
	case audio.KindSource:
		config.Input = name
	default:
		return fmt.Errorf("session: unknown audio kind %q", kind)
	}
	return s.selectAudio(rt, ctx, config)
}

type audioChange struct {
	command  string
	device   string
	previous string
}

func resolveAudioChange(nodes []audio.Node, command string, kind audio.Kind, selected, current string) (audioChange, error) {
	config := storage.AudioConfig{Output: selected}
	if kind == audio.KindSource {
		config = storage.AudioConfig{Input: selected}
	}
	if err := validateAudioSelection(nodes, config); err != nil {
		return audioChange{}, err
	}
	device, err := liveAudioDevice(nodes, kind, selected)
	if err != nil {
		return audioChange{}, err
	}
	// Without a previous device there is nothing to restore after a failure.
	previous, _ := liveAudioDevice(nodes, kind, current)
	return audioChange{command: command, device: device, previous: previous}, nil
}

// restoreAudio switches already applied changes back after a later command
// failed. It ignores the caller's cancellation because a timeout is a common
// reason for that failure; applyAudioCommand still bounds each call.
func (s *Session) restoreAudio(rt *runtime, ctx context.Context, applied []audioChange) error {
	ctx = context.WithoutCancel(ctx)
	var errs []error
	for _, change := range applied {
		if change.previous == "" {
			errs = append(errs, fmt.Errorf("session: restore %s: no previous device", change.command))
			continue
		}
		if err := s.applyAudioCommand(rt, ctx, change.command, change.previous); err != nil {
			errs = append(errs, fmt.Errorf("session: restore previous device: %w", err))
		}
	}
	return errors.Join(errs...)
}

func normalizeAudioConfig(config storage.AudioConfig) storage.AudioConfig {
	config.Output = strings.TrimSpace(config.Output)
	config.Input = strings.TrimSpace(config.Input)
	return config
}

func liveAudioDevice(nodes []audio.Node, kind audio.Kind, selected string) (string, error) {
	if selected != "" {
		return selected, nil
	}
	for _, node := range nodes {
		if node.Kind == kind && node.Default {
			return node.Name, nil
		}
	}
	return "", fmt.Errorf("%w: no system default for %s", ErrAudioNodeMissing, kind)
}

func validateAudioSelection(nodes []audio.Node, config storage.AudioConfig) error {
	selected := []struct {
		name string
		kind audio.Kind
	}{
		{name: strings.TrimSpace(config.Output), kind: audio.KindSink},
		{name: strings.TrimSpace(config.Input), kind: audio.KindSource},
	}
	for _, selection := range selected {
		if selection.name == "" {
			continue
		}
		found := false
		for _, node := range nodes {
			if node.Name == selection.name && node.Kind == selection.kind {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%w: %s %q", ErrAudioNodeMissing, selection.kind, selection.name)
		}
	}
	return nil
}

func (s *Session) readAccount() (storage.Account, error) {
	account, err := s.deps.Store.ReadAccount()
	if err != nil {
		return storage.Account{}, fmt.Errorf("session: read account: %w", err)
	}
	s.updateSnapshot(func(snapshot *Snapshot) {
		snapshot.Account = account
	})
	return account, nil
}

func (s *Session) writeAccount(rt *runtime, ctx context.Context, credentials storage.AccountCredentials) (error, bool) {
	if s.Snapshot().State.CallState != app.CallIdle {
		return ErrCallActive, false
	}
	if err := s.deps.Store.WriteAccount(credentials); err != nil {
		return fmt.Errorf("session: write account: %w", err), false
	}
	account, err := s.readAccount()
	if err != nil {
		return fmt.Errorf("session: account was written but cannot be read: %w", err), false
	}

	if err := s.cleanupRuntime(rt, false, ctx); err != nil {
		return fmt.Errorf("session: account was written but stopping baresip failed: %w", err), true
	}
	process, client, err := s.launch(rt.ctx, ctx)
	if err != nil {
		return fmt.Errorf("session: account was written but baresip restart failed: %w", err), true
	}
	rt.process = process
	rt.client = client

	state := app.NewState()
	state.DND = s.Snapshot().State.DND
	registrationErr := s.registrationState(rt, &state)
	s.updateSnapshot(func(snapshot *Snapshot) {
		snapshot.State = state
		snapshot.Account = account
		snapshot.Running = true
		snapshot.LastError = errorText(registrationErr)
	})
	return nil, false
}

// quitBaresip asks baresip to shut down by itself. Only that path deregisters
// the account. A terminating signal leaves a stale contact at the registrar,
// which then keeps routing incoming calls to a socket nobody listens on.
func (s *Session) quitBaresip(rt *runtime, ctx context.Context) {
	if rt.client == nil || rt.process == nil {
		return
	}

	commandCtx, cancel := context.WithTimeout(ctx, s.config.CommandTimeout)
	// baresip may exit before it answers, so the error says nothing useful.
	_, _ = rt.client.Command(commandCtx, "quit", "")
	cancel()

	timer := time.NewTimer(s.config.StopTimeout)
	defer timer.Stop()
	select {
	case <-rt.process.Done():
	case <-timer.C:
	case <-ctx.Done():
	}
}

func (s *Session) cleanupRuntime(rt *runtime, hangup bool, ctx context.Context) error {
	var cleanupErrors []error
	if rt.client != nil {
		if hangup && s.Snapshot().State.CallState != app.CallIdle {
			commandCtx, cancel := context.WithTimeout(ctx, s.config.CommandTimeout)
			response, err := rt.client.Command(commandCtx, "hangup", "")
			cancel()
			if err != nil {
				cleanupErrors = append(cleanupErrors, fmt.Errorf("session: hang up before stop: %w", err))
			} else if outputErr := app.ParseCallCommandError(response); outputErr != "" {
				cleanupErrors = append(cleanupErrors, fmt.Errorf("session: hang up before stop: %s", outputErr))
			}
		}
		if err := s.finalizeActiveCall(s.deps.Now(), hangup); err != nil {
			cleanupErrors = append(cleanupErrors, err)
		}
		s.quitBaresip(rt, ctx)
		if err := rt.client.Close(); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("session: close baresip client: %w", err))
		}
		rt.client = nil
	}
	if rt.process != nil {
		stopCtx, cancel := context.WithTimeout(ctx, s.config.StopTimeout)
		if err := rt.process.Stop(stopCtx); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("session: stop baresip: %w", err))
		}
		cancel()
		rt.process = nil
	}
	return errors.Join(cleanupErrors...)
}

func (s *Session) clientTerminalError(client baresip.Client) error {
	if err := client.Err(); err != nil {
		return fmt.Errorf("session: baresip client stopped: %w", err)
	}
	return errors.New("session: baresip client stopped")
}

func (s *Session) finishRuntime(rt *runtime, terminalErr error) {
	terminalErr = errors.Join(terminalErr, s.finalizeActiveCall(s.deps.Now(), false))
	s.mu.Lock()
	if s.active != rt {
		s.mu.Unlock()
		return
	}
	s.active = nil
	dnd := s.snapshot.State.DND
	s.snapshot.State = app.NewState()
	s.snapshot.State.DND = dnd
	s.snapshot.Running = false
	if terminalErr != nil {
		s.snapshot.LastError = terminalErr.Error()
	}
	s.snapshot.Revision++
	current := cloneSnapshot(s.snapshot)
	s.mu.Unlock()
	s.publish(current)
}

func (s *Session) clearError() {
	if s.Snapshot().LastError == "" {
		return
	}
	s.updateSnapshot(func(snapshot *Snapshot) {
		snapshot.LastError = ""
	})
}

func (s *Session) recordError(err error) {
	if err == nil {
		return
	}
	s.updateSnapshot(func(snapshot *Snapshot) {
		snapshot.LastError = err.Error()
	})
}
