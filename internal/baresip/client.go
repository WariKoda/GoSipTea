package baresip

import (
	"context"
	"errors"
)

const (
	ServiceName   = "com.github.Baresip"
	ObjectPath    = "/baresip"
	InterfaceName = "com.github.Baresip"

	MaxCommandNameBytes = 32
	MaxCommandLineBytes = 8 * 1024
	MaxParamsBytes      = 2 * 1024
	MaxEventBytes       = 64 * 1024
	MaxResponseBytes    = 64 * 1024
)

var (
	ErrClosed             = errors.New("baresip client is closed")
	ErrServiceUnavailable = errors.New("baresip service is not available")
	ErrServiceGone        = errors.New("baresip service disappeared")
	ErrBusDisconnected    = errors.New("session bus disconnected")
	ErrInvalidCommand     = errors.New("invalid command name")
	ErrInvalidParams      = errors.New("invalid command params")
	ErrCommandTooLong     = errors.New("command line too long")
	ErrInvalidEvent       = errors.New("invalid baresip event")
	ErrEventTooLarge      = errors.New("baresip event is too large")
)

// Event contains the JSON object from the third argument of a baresip event signal.
type Event map[string]any

// Client is the control connection used by the rest of the application.
type Client interface {
	Invoke(ctx context.Context, commandLine string) (string, error)
	Command(ctx context.Context, command, params string) (string, error)
	Events() <-chan Event
	Done() <-chan struct{}
	Err() error
	Close() error
}
