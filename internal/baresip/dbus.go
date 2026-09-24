package baresip

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	InvokeTimeout   = 10 * time.Second
	EventBufferSize = 64

	dbusInterface          = "org.freedesktop.DBus"
	dbusPath               = "/org/freedesktop/DBus"
	nameOwnerChangedSignal = dbusInterface + ".NameOwnerChanged"
	baresipEventSignal     = InterfaceName + ".event"
)

// DBusClient controls one owner of com.github.Baresip on the session bus.
type DBusClient struct {
	conn       *dbus.Conn
	object     dbus.BusObject
	owner      string
	signals    chan *dbus.Signal
	events     chan Event
	done       chan struct{}
	cancel     context.CancelFunc
	eventMatch []dbus.MatchOption
	ownerMatch []dbus.MatchOption

	mu      sync.RWMutex
	err     error
	closing bool
}

// NewClient connects to the current baresip D-Bus owner and starts watching it.
func NewClient(ctx context.Context) (*DBusClient, error) {
	if ctx == nil {
		return nil, errors.New("nil context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, fmt.Errorf("connect to session bus: %w", err)
	}

	client := &DBusClient{
		conn:    conn,
		object:  conn.Object(ServiceName, dbus.ObjectPath(ObjectPath)),
		signals: make(chan *dbus.Signal, EventBufferSize),
		events:  make(chan Event, EventBufferSize),
		done:    make(chan struct{}),
		eventMatch: []dbus.MatchOption{
			dbus.WithMatchSender(ServiceName),
			dbus.WithMatchInterface(InterfaceName),
			dbus.WithMatchMember("event"),
			dbus.WithMatchObjectPath(dbus.ObjectPath(ObjectPath)),
		},
		ownerMatch: []dbus.MatchOption{
			dbus.WithMatchSender(dbusInterface),
			dbus.WithMatchInterface(dbusInterface),
			dbus.WithMatchMember("NameOwnerChanged"),
			dbus.WithMatchObjectPath(dbus.ObjectPath(dbusPath)),
			dbus.WithMatchArg(0, ServiceName),
		},
	}
	conn.Signal(client.signals)

	cleanup := func() {
		conn.RemoveSignal(client.signals)
		_ = conn.Close()
	}
	if err := conn.AddMatchSignalContext(ctx, client.eventMatch...); err != nil {
		cleanup()
		return nil, fmt.Errorf("subscribe to baresip events: %w", err)
	}
	if err := conn.AddMatchSignalContext(ctx, client.ownerMatch...); err != nil {
		cleanup()
		return nil, fmt.Errorf("watch baresip service owner: %w", err)
	}

	owner, err := getNameOwner(ctx, conn)
	if err != nil {
		cleanup()
		return nil, err
	}
	client.owner = owner

	runCtx, cancel := context.WithCancel(ctx)
	client.cancel = cancel
	go client.run(runCtx)
	return client, nil
}

// Invoke calls com.github.Baresip.invoke with one validated command line.
func (client *DBusClient) Invoke(ctx context.Context, commandLine string) (string, error) {
	if ctx == nil {
		return "", errors.New("nil context")
	}
	if err := ValidateCommandLine(commandLine); err != nil {
		return "", err
	}
	if err := client.connectionError(); err != nil {
		return "", err
	}

	callCtx, cancel := context.WithTimeout(ctx, InvokeTimeout)
	defer cancel()

	var response string
	call := client.object.CallWithContext(
		callCtx,
		InterfaceName+".invoke",
		dbus.FlagNoAutoStart,
		commandLine,
	)
	if err := call.Store(&response); err != nil {
		if connectionErr := client.connectionError(); connectionErr != nil {
			return "", connectionErr
		}
		return "", fmt.Errorf("invoke baresip command: %w", err)
	}
	return limitResponse(response), nil
}

// Command validates command and params separately before invoking baresip.
func (client *DBusClient) Command(ctx context.Context, command, params string) (string, error) {
	commandLine, err := BuildCommand(command, params)
	if err != nil {
		return "", err
	}
	return client.Invoke(ctx, commandLine)
}

func (client *DBusClient) Events() <-chan Event {
	return client.events
}

func (client *DBusClient) Done() <-chan struct{} {
	return client.done
}

// Err reports why the connection ended. It returns nil after an explicit Close.
func (client *DBusClient) Err() error {
	client.mu.RLock()
	defer client.mu.RUnlock()
	return client.err
}

func (client *DBusClient) Close() error {
	client.mu.Lock()
	if !client.closing {
		client.closing = true
		client.cancel()
	}
	client.mu.Unlock()

	<-client.done
	return nil
}

func (client *DBusClient) run(ctx context.Context) {
	var terminalErr error
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if client.conn.Connected() {
			_ = client.conn.RemoveMatchSignalContext(cleanupCtx, client.ownerMatch...)
			_ = client.conn.RemoveMatchSignalContext(cleanupCtx, client.eventMatch...)
		}
		client.conn.RemoveSignal(client.signals)
		_ = client.conn.Close()

		client.mu.Lock()
		if !client.closing {
			client.err = terminalErr
		}
		client.mu.Unlock()
		close(client.events)
		close(client.done)
	}()

	for {
		select {
		case <-ctx.Done():
			terminalErr = ctx.Err()
			return
		case <-client.conn.Context().Done():
			terminalErr = ErrBusDisconnected
			return
		case signal, ok := <-client.signals:
			if !ok {
				terminalErr = ErrBusDisconnected
				return
			}
			if signal == nil {
				continue
			}

			switch signal.Name {
			case baresipEventSignal:
				event, err := ParseEventSignal(signal.Body)
				if err != nil {
					continue
				}
				select {
				case client.events <- event:
				case <-ctx.Done():
					terminalErr = ctx.Err()
					return
				case <-client.conn.Context().Done():
					terminalErr = ErrBusDisconnected
					return
				}
			case nameOwnerChangedSignal:
				if client.ownerChanged(signal.Body) {
					terminalErr = ErrServiceGone
					return
				}
			}
		}
	}
}

func (client *DBusClient) ownerChanged(body []any) bool {
	var name, oldOwner, newOwner string
	if err := dbus.Store(body, &name, &oldOwner, &newOwner); err != nil {
		return false
	}
	return name == ServiceName && oldOwner == client.owner && newOwner != client.owner
}

func (client *DBusClient) connectionError() error {
	select {
	case <-client.done:
		if err := client.Err(); err != nil {
			return err
		}
		return ErrClosed
	default:
		return nil
	}
}

func getNameOwner(ctx context.Context, conn *dbus.Conn) (string, error) {
	var hasOwner bool
	if err := conn.BusObject().CallWithContext(
		ctx,
		dbusInterface+".NameHasOwner",
		dbus.FlagNoAutoStart,
		ServiceName,
	).Store(&hasOwner); err != nil {
		return "", serviceUnavailable(err)
	}
	if !hasOwner {
		return "", ErrServiceUnavailable
	}

	var owner string
	if err := conn.BusObject().CallWithContext(
		ctx,
		dbusInterface+".GetNameOwner",
		dbus.FlagNoAutoStart,
		ServiceName,
	).Store(&owner); err != nil {
		return "", serviceUnavailable(err)
	}
	if owner == "" {
		return "", ErrServiceUnavailable
	}
	return owner, nil
}

// ServiceHasOwner checks the session bus without activating baresip.
func ServiceHasOwner(ctx context.Context) (bool, error) {
	if ctx == nil {
		return false, errors.New("nil context")
	}
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return false, fmt.Errorf("connect to session bus: %w", err)
	}
	defer conn.Close()

	var hasOwner bool
	if err := conn.BusObject().CallWithContext(
		ctx,
		dbusInterface+".NameHasOwner",
		dbus.FlagNoAutoStart,
		ServiceName,
	).Store(&hasOwner); err != nil {
		return false, fmt.Errorf("check baresip service owner: %w", err)
	}
	return hasOwner, nil
}
