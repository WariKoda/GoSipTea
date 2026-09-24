package session

import (
	"context"
	"errors"

	"github.com/nibra/gosiptea/internal/app"
	"github.com/nibra/gosiptea/internal/audio"
	"github.com/nibra/gosiptea/internal/storage"
)

// Dial starts an outgoing call through the reducer.
func (s *Session) Dial(ctx context.Context, target string) error {
	return s.action(ctx, app.ActionDial, target)
}

// Answer accepts the current incoming call.
func (s *Session) Answer(ctx context.Context) error {
	return s.action(ctx, app.ActionAnswer, "")
}

// Hangup ends the current call.
func (s *Session) Hangup(ctx context.Context) error {
	return s.action(ctx, app.ActionHangup, "")
}

// ToggleMute toggles mute for an active call.
func (s *Session) ToggleMute(ctx context.Context) error {
	return s.action(ctx, app.ActionToggleMute, "")
}

// ToggleDND toggles do not disturb.
func (s *Session) ToggleDND(ctx context.Context) error {
	return s.action(ctx, app.ActionToggleDND, "")
}

// ReloadContacts refreshes the session snapshot from storage. Dialing uses the
// stored URI directly, so baresip's in-memory contact list need not be changed.
func (s *Session) ReloadContacts(ctx context.Context) ([]storage.Contact, error) {
	result, err := s.request(ctx, request{kind: requestReloadContacts})
	if err != nil {
		return nil, err
	}
	return result.value.([]storage.Contact), nil
}

// AddContact persists a contact and refreshes the snapshot.
func (s *Session) AddContact(ctx context.Context, contact storage.Contact) ([]storage.Contact, error) {
	result, err := s.request(ctx, request{kind: requestAddContact, contact: contact})
	if err != nil {
		return nil, err
	}
	return result.value.([]storage.Contact), nil
}

// RemoveContact removes a contact by exact URI and refreshes the snapshot.
func (s *Session) RemoveContact(ctx context.Context, uri string) ([]storage.Contact, error) {
	result, err := s.request(ctx, request{kind: requestRemoveContact, text: uri})
	if err != nil {
		return nil, err
	}
	return result.value.([]storage.Contact), nil
}

// DialContact dials an exact URI from the current contact snapshot.
func (s *Session) DialContact(ctx context.Context, uri string) error {
	_, err := s.request(ctx, request{kind: requestDialContact, text: uri})
	return err
}

// ListAudio refreshes the available PipeWire nodes.
func (s *Session) ListAudio(ctx context.Context) ([]audio.Node, error) {
	result, err := s.request(ctx, request{kind: requestListAudio})
	if err != nil {
		return nil, err
	}
	return result.value.([]audio.Node), nil
}

// PersistAudio writes a selection without changing the running baresip
// process.
func (s *Session) PersistAudio(ctx context.Context, config storage.AudioConfig) error {
	_, err := s.request(ctx, request{kind: requestPersistAudio, audio: config})
	return err
}

// ApplyAudio validates a selection against current PipeWire nodes and applies
// it to the running process without persisting it.
func (s *Session) ApplyAudio(ctx context.Context, config storage.AudioConfig) error {
	_, err := s.request(ctx, request{kind: requestApplyAudio, audio: config})
	return err
}

// SelectAudio validates, applies, and then persists an audio selection.
func (s *Session) SelectAudio(ctx context.Context, config storage.AudioConfig) error {
	_, err := s.request(ctx, request{kind: requestSelectAudio, audio: config})
	return err
}

// SelectAudioDevice validates, applies, and persists one direction. An empty
// name selects the system default. The other direction keeps its selection.
func (s *Session) SelectAudioDevice(ctx context.Context, kind audio.Kind, name string) error {
	_, err := s.request(ctx, request{kind: requestSelectAudioDevice, audioKind: kind, text: name})
	return err
}

// ReadAccount refreshes and returns the non-secret account view.
func (s *Session) ReadAccount(ctx context.Context) (storage.Account, error) {
	result, err := s.request(ctx, request{kind: requestReadAccount})
	if err != nil {
		return storage.Account{}, err
	}
	return result.value.(storage.Account), nil
}

// WriteAccount persists account credentials and restarts the owned child. It
// refuses to change the account while a call is not idle.
func (s *Session) WriteAccount(ctx context.Context, credentials storage.AccountCredentials) error {
	_, err := s.request(ctx, request{kind: requestWriteAccount, account: credentials})
	return err
}

func (s *Session) action(ctx context.Context, actionType app.ActionType, target string) error {
	_, err := s.request(ctx, request{
		kind: requestAction,
		action: app.ActionEvent{
			Type:   actionType,
			Target: target,
			At:     s.deps.Now(),
		},
	})
	return err
}

func (s *Session) request(ctx context.Context, req request) (requestResult, error) {
	if ctx == nil {
		return requestResult{}, errors.New("session: nil context")
	}
	s.mu.RLock()
	rt := s.active
	s.mu.RUnlock()
	if rt == nil {
		return requestResult{}, ErrNotRunning
	}
	return s.sendRequest(rt, requestWithContext(req, ctx))
}

func (s *Session) sendRequest(rt *runtime, req request) (requestResult, error) {
	req.result = make(chan requestResult, 1)
	select {
	case rt.requests <- req:
	case <-rt.done:
		return requestResult{}, ErrNotRunning
	case <-req.ctx.Done():
		return requestResult{}, req.ctx.Err()
	}

	select {
	case result := <-req.result:
		return result, result.err
	case <-rt.done:
		select {
		case result := <-req.result:
			return result, result.err
		default:
			return requestResult{}, ErrNotRunning
		}
	case <-req.ctx.Done():
		return requestResult{}, req.ctx.Err()
	}
}

func requestWithContext(req request, ctx context.Context) request {
	req.ctx = ctx
	return req
}
