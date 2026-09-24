// Package media controls desktop media players through MPRIS.
package media

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/godbus/dbus/v5"
)

const (
	mprisPrefix     = "org.mpris.MediaPlayer2."
	mprisPath       = "/org/mpris/MediaPlayer2"
	playerInterface = "org.mpris.MediaPlayer2.Player"
	maxPlayers      = 64
)

// Pauser pauses every MPRIS player currently registered on the session bus.
type Pauser struct{}

// NewPauser creates an MPRIS pauser.
func NewPauser() *Pauser {
	return &Pauser{}
}

// PauseAll pauses all currently registered players. It does not resume them.
func (p *Pauser) PauseAll(ctx context.Context) error {
	if ctx == nil {
		return errors.New("media: nil context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	connection, err := dbus.ConnectSessionBus()
	if err != nil {
		return fmt.Errorf("media: connect to session bus: %w", err)
	}
	defer connection.Close()

	var names []string
	if err := connection.BusObject().CallWithContext(
		ctx,
		"org.freedesktop.DBus.ListNames",
		dbus.FlagNoAutoStart,
	).Store(&names); err != nil {
		return fmt.Errorf("media: list MPRIS players: %w", err)
	}
	players := PlayerNames(names)
	if len(players) > maxPlayers {
		players = players[:maxPlayers]
	}

	var pauseErrors []error
	for _, name := range players {
		if err := connection.Object(name, dbus.ObjectPath(mprisPath)).CallWithContext(
			ctx,
			playerInterface+".Pause",
			dbus.FlagNoAutoStart,
		).Err; err != nil {
			pauseErrors = append(pauseErrors, fmt.Errorf("pause %s: %w", name, err))
		}
	}
	return errors.Join(pauseErrors...)
}

// PlayerNames filters and sorts unique MPRIS bus names.
func PlayerNames(names []string) []string {
	seen := make(map[string]struct{})
	players := make([]string, 0, len(names))
	for _, name := range names {
		if !strings.HasPrefix(name, mprisPrefix) || len(name) > 255 {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		players = append(players, name)
	}
	sort.Strings(players)
	return players
}
