package baresip

import (
	"context"
	"errors"
	"fmt"

	"github.com/godbus/dbus/v5"
)

const startupLockName = "com.github.GoSipTea.OwnedProcess"

type busStartupLock struct {
	*dbus.Conn
}

func (lock *busStartupLock) Done() <-chan struct{} {
	return lock.Context().Done()
}

// AcquireStartupLock takes a bus-wide, non-queued lock on a private connection.
// The bus releases it if the app dies. It does not replace the baresip owner check.
func AcquireStartupLock(ctx context.Context) (StartupLock, error) {
	if ctx == nil {
		return nil, errors.New("baresip: nil context")
	}
	ctx, cancel := context.WithTimeout(ctx, InvokeTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, fmt.Errorf("baresip: connect for startup lock: %w", err)
	}
	var reply uint32
	err = conn.BusObject().CallWithContext(ctx, dbusInterface+".RequestName", dbus.FlagNoAutoStart,
		startupLockName, uint32(dbus.NameFlagDoNotQueue)).Store(&reply)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("baresip: request startup lock: %w", err)
	}
	if dbus.RequestNameReply(reply) != dbus.RequestNameReplyPrimaryOwner {
		_ = conn.Close()
		return nil, ErrStartupLocked
	}
	return &busStartupLock{Conn: conn}, nil
}
