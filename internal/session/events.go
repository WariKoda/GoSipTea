package session

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nibra/gosiptea/internal/app"
	"github.com/nibra/gosiptea/internal/baresip"
)

var (
	ErrUnsupportedEvent = errors.New("session: unsupported baresip event")
	ErrMalformedEvent   = errors.New("session: malformed baresip event")
)

// TranslateEvent converts a supported baresip JSON event to a domain event.
// Unsupported event types return ErrUnsupportedEvent so callers can ignore
// them without confusing them with malformed supported events.
func TranslateEvent(raw baresip.Event, contacts []app.Contact, countryCallingCode string, at time.Time) (app.Event, error) {
	eventType, err := requiredEventString(raw, "type")
	if err != nil {
		return nil, err
	}

	switch eventType {
	case string(app.CallEventIncoming), string(app.CallEventEstablished), string(app.CallEventClosed):
		id, err := optionalEventString(raw, "id")
		if err != nil {
			return nil, err
		}
		peerURI, err := optionalEventString(raw, "peeruri")
		if err != nil {
			return nil, err
		}
		peerDisplayName, err := optionalEventString(raw, "peerdisplayname")
		if err != nil {
			return nil, err
		}
		if peerDisplayName == "" {
			peerDisplayName, err = optionalEventString(raw, "peerdisplay")
			if err != nil {
				return nil, err
			}
		}
		return app.CallEvent{
			Type:               app.CallEventType(eventType),
			ID:                 id,
			PeerURI:            peerURI,
			PeerDisplayName:    peerDisplayName,
			Contacts:           append([]app.Contact(nil), contacts...),
			CountryCallingCode: countryCallingCode,
			At:                 at,
		}, nil
	case string(app.RegisterEventOK), string(app.RegisterEventFail), string(app.RegisterEventUnregistering):
		accountAOR, err := optionalEventString(raw, "accountaor")
		if err != nil {
			return nil, err
		}
		detail, err := optionalEventString(raw, "param")
		if err != nil {
			return nil, err
		}
		return app.RegisterEvent{
			Type:       app.RegisterEventType(eventType),
			AccountAOR: accountAOR,
			Detail:     detail,
			At:         at,
		}, nil
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedEvent, eventType)
	}
}

func requiredEventString(raw baresip.Event, key string) (string, error) {
	value, err := optionalEventString(raw, key)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%w: missing %q", ErrMalformedEvent, key)
	}
	return value, nil
}

func optionalEventString(raw baresip.Event, key string) (string, error) {
	value, exists := raw[key]
	if !exists || value == nil {
		return "", nil
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%w: %q is %T, want string", ErrMalformedEvent, key, value)
	}
	return text, nil
}
