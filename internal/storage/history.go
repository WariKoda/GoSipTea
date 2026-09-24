package storage

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/nibra/gosiptea/internal/app"
)

const (
	callHistoryVersion    = 1
	MaxCallHistoryEntries = 200
)

type callHistoryDocument struct {
	Version int                    `json:"version"`
	Calls   []app.CallHistoryEntry `json:"calls"`
}

// ReadCallHistory reads the GoSipTea-owned call history. A missing file is an
// empty history, while malformed or unsupported data remains visible as an
// error instead of being overwritten.
func (s *Store) ReadCallHistory() ([]app.CallHistoryEntry, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	data, err := readRegularFile(s.paths.History, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read call history: %w", err)
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var document callHistoryDocument
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("%w: decode call history: %v", ErrInvalid, err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, fmt.Errorf("%w: decode call history: %v", ErrInvalid, err)
	}
	if document.Version != callHistoryVersion {
		return nil, fmt.Errorf("%w: unsupported call history version %d", ErrInvalid, document.Version)
	}
	if len(document.Calls) > MaxCallHistoryEntries {
		return nil, fmt.Errorf("%w: call history has more than %d entries", ErrInvalid, MaxCallHistoryEntries)
	}
	for index, entry := range document.Calls {
		if err := validateCallHistoryEntry(entry); err != nil {
			return nil, fmt.Errorf("%w: call history entry %d: %v", ErrInvalid, index, err)
		}
	}
	return append([]app.CallHistoryEntry(nil), document.Calls...), nil
}

// WriteCallHistory atomically replaces the history and retains only the newest
// entries. Callers pass entries in newest-first order.
func (s *Store) WriteCallHistory(entries []app.CallHistoryEntry) error {
	if err := s.validate(); err != nil {
		return err
	}
	if len(entries) > MaxCallHistoryEntries {
		entries = entries[:MaxCallHistoryEntries]
	}
	calls := append([]app.CallHistoryEntry(nil), entries...)
	for index, entry := range calls {
		if err := validateCallHistoryEntry(entry); err != nil {
			return fmt.Errorf("%w: call history entry %d: %v", ErrInvalid, index, err)
		}
	}

	data, err := json.MarshalIndent(callHistoryDocument{Version: callHistoryVersion, Calls: calls}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode call history: %w", err)
	}
	data = append(data, '\n')
	return s.withExclusiveLock(func() error {
		if err := refuseSymlinkOrSpecial(s.paths.History, true); err != nil {
			return fmt.Errorf("write call history: %w", err)
		}
		if err := atomicWrite(s.paths.History, data, 0o600); err != nil {
			return fmt.Errorf("write call history: %w", err)
		}
		return nil
	})
}

func validateCallHistoryEntry(entry app.CallHistoryEntry) error {
	switch entry.Direction {
	case app.CallDirectionIncoming, app.CallDirectionOutgoing:
	default:
		return errors.New("unknown direction")
	}
	switch entry.Outcome {
	case app.CallOutcomeConnected, app.CallOutcomeMissed, app.CallOutcomeRejected,
		app.CallOutcomeRejectedDND, app.CallOutcomeRejectedBusy,
		app.CallOutcomeNotConnected, app.CallOutcomeCanceled:
	default:
		return errors.New("unknown outcome")
	}
	if len(entry.Target) > MaxFieldLength {
		return errors.New("invalid target")
	}
	if len(entry.Peer) > MaxFieldLength {
		return errors.New("invalid peer")
	}
	if entry.StartedAt.IsZero() || entry.EndedAt.IsZero() || entry.EndedAt.Before(entry.StartedAt) {
		return errors.New("invalid timestamps")
	}
	if entry.Outcome == app.CallOutcomeConnected && entry.ConnectedAt.IsZero() {
		return errors.New("connected call has no connected timestamp")
	}
	if !entry.ConnectedAt.IsZero() && (entry.ConnectedAt.Before(entry.StartedAt) || entry.ConnectedAt.After(entry.EndedAt)) {
		return errors.New("invalid connected timestamp")
	}
	return nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return err
	}
	return errors.New("multiple JSON values")
}
