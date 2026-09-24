package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/nibra/gosiptea/internal/app"
	"github.com/nibra/gosiptea/internal/audio"
	"github.com/nibra/gosiptea/internal/baresip"
	"github.com/nibra/gosiptea/internal/bootstrap"
	"github.com/nibra/gosiptea/internal/session"
	"github.com/nibra/gosiptea/internal/storage"
	"github.com/nibra/gosiptea/internal/tui"
)

const operationTimeout = 15 * time.Second

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "gosiptea:", err)
		os.Exit(1)
	}
}

// logWriter avoids handing a typed nil pointer to the session, which would
// look like a configured writer.
func logWriter(file *os.File) io.Writer {
	if file == nil {
		return nil
	}
	return file
}

func run() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("find home directory: %w", err)
	}

	configDir := flag.String("config-dir", filepath.Join(home, ".baresip"), "baresip configuration directory")
	baresipPath := flag.String("baresip", "baresip", "baresip executable")
	countryCode := flag.String("country-code", "49", "country calling code used for contact matching")
	baresipLog := flag.String("baresip-log", "", "append baresip output to this file for debugging")
	sipTrace := flag.Bool("sip-trace", false, "trace SIP from startup, requires -baresip-log; includes sensitive call data")
	flag.Parse()

	if *sipTrace && *baresipLog == "" {
		return errors.New("-sip-trace requires -baresip-log")
	}

	if err := bootstrap.EnsureConfig(*configDir); err != nil {
		return err
	}

	var logFile *os.File
	if *baresipLog != "" {
		logFile, err = os.OpenFile(*baresipLog, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
		if err != nil {
			return fmt.Errorf("open baresip log: %w", err)
		}
		defer logFile.Close()
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer cancel()

	phone, err := session.New(session.Config{
		ConfigDir:          *configDir,
		BaresipPath:        *baresipPath,
		CountryCallingCode: *countryCode,
		Log:                logWriter(logFile),
		SIPTrace:           *sipTrace,
	})
	if err != nil {
		return err
	}

	startCtx, startCancel := context.WithTimeout(ctx, operationTimeout)
	err = phone.Start(startCtx)
	startCancel()
	if err != nil {
		if errors.Is(err, baresip.ErrServiceOwned) {
			return fmt.Errorf("another baresip instance is active; GoSipTea requires exclusive use of com.github.Baresip: %w", err)
		}
		return fmt.Errorf("start phone session: %w", err)
	}
	stopped := false
	defer func() {
		if stopped {
			return
		}
		stopCtx, stopCancel := context.WithTimeout(context.Background(), operationTimeout)
		defer stopCancel()
		if stopErr := phone.Stop(stopCtx); stopErr != nil {
			fmt.Fprintln(os.Stderr, "gosiptea: stop phone session:", stopErr)
		}
	}()

	listCtx, listCancel := context.WithTimeout(ctx, operationTimeout)
	_, _ = phone.ListAudio(listCtx)
	listCancel()

	updates, unsubscribe := phone.Subscribe(8)
	defer unsubscribe()

	dispatch := makeDispatch(ctx, phone, *countryCode)
	model := tui.New(toTUISnapshot(phone.Snapshot(), *countryCode), dispatch)
	program := tea.NewProgram(model, tea.WithAltScreen(), tea.WithContext(ctx))

	updatesDone := make(chan struct{})
	go func() {
		defer close(updatesDone)
		lastError := ""
		for snapshot := range updates {
			program.Send(tui.SnapshotMsg{Snapshot: toTUISnapshot(snapshot, *countryCode)})
			if snapshot.LastError != lastError {
				if snapshot.LastError == "" {
					program.Send(tui.ErrorMsg{})
				} else {
					program.Send(tui.ErrorMsg{Err: errors.New(snapshot.LastError)})
				}
			}
			lastError = snapshot.LastError
			if !snapshot.Running {
				program.Quit()
			}
		}
	}()

	_, runErr := program.Run()
	unsubscribe()
	<-updatesDone

	terminalSnapshot := phone.Snapshot()
	stopCtx, stopCancel := context.WithTimeout(context.Background(), operationTimeout)
	stopErr := phone.Stop(stopCtx)
	stopCancel()
	stopped = true

	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		return errors.Join(fmt.Errorf("run terminal interface: %w", runErr), stopErr)
	}
	if stopErr != nil {
		return fmt.Errorf("stop phone session: %w", stopErr)
	}
	if !terminalSnapshot.Running && terminalSnapshot.LastError != "" {
		return errors.New(terminalSnapshot.LastError)
	}
	return nil
}

func makeDispatch(parent context.Context, phone *session.Session, countryCode string) tui.Dispatch {
	return func(action tui.Action) tea.Cmd {
		if action.Kind == tui.ActionQuit {
			return nil
		}
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(parent, operationTimeout)
			defer cancel()

			var err error
			switch action.Kind {
			case tui.ActionDial:
				err = phone.Dial(ctx, action.Target)
			case tui.ActionAnswer:
				err = phone.Answer(ctx)
			case tui.ActionReject, tui.ActionHangup:
				err = phone.Hangup(ctx)
			case tui.ActionToggleMute:
				err = phone.ToggleMute(ctx)
			case tui.ActionToggleDND:
				err = phone.ToggleDND(ctx)
			case tui.ActionAddContact:
				snapshot := phone.Snapshot()
				domain := snapshot.Account.Domain
				if domain == "" {
					domain = snapshot.Account.Server
				}
				uri := app.NormalizeContactURI(action.Contact.URI, domain)
				if uri == "" {
					err = errors.New("enter a full SIP address or configure an account domain")
					break
				}
				_, err = phone.AddContact(ctx, storage.Contact{Name: action.Contact.Name, URI: uri})
			case tui.ActionRemoveContact:
				_, err = phone.RemoveContact(ctx, action.Target)
			case tui.ActionSetAudio:
				switch action.Audio.Field {
				case tui.AudioOutput:
					err = phone.SelectAudioDevice(ctx, audio.KindSink, action.Audio.Name)
				case tui.AudioInput:
					err = phone.SelectAudioDevice(ctx, audio.KindSource, action.Audio.Name)
				default:
					err = fmt.Errorf("unsupported audio field %q", action.Audio.Field)
				}
			case tui.ActionSaveAccount:
				secure := action.Account.Secure
				err = phone.WriteAccount(ctx, storage.AccountCredentials{
					Server:   action.Account.Server,
					Username: action.Account.Username,
					Domain:   action.Account.Domain,
					Login:    action.Account.Login,
					Password: action.Account.Password,
					Secure:   &secure,
				})
			default:
				err = fmt.Errorf("unsupported UI action %q", action.Kind)
			}
			if err != nil {
				return tui.ErrorMsg{Err: err}
			}
			// A session error from a concurrent event must stay visible.
			snapshot := phone.Snapshot()
			result := tui.ActionResultMsg{Snapshot: toTUISnapshot(snapshot, countryCode)}
			if snapshot.LastError != "" {
				result.Err = errors.New(snapshot.LastError)
			}
			return result
		}
	}
}

func toTUISnapshot(snapshot session.Snapshot, countryCode string) tui.Snapshot {
	contacts := make([]tui.ContactSnapshot, len(snapshot.Contacts))
	domainContacts := make([]app.Contact, len(snapshot.Contacts))
	for index, contact := range snapshot.Contacts {
		contacts[index] = tui.ContactSnapshot{Name: contact.Name, URI: contact.URI}
		domainContacts[index] = app.Contact{Name: contact.Name, URI: contact.URI}
	}

	var inputs []tui.AudioNodeSnapshot
	var outputs []tui.AudioNodeSnapshot
	for _, node := range snapshot.AudioNodes {
		converted := tui.AudioNodeSnapshot{Name: node.Name, Description: node.Description, Default: node.Default}
		switch node.Kind {
		case audio.KindSink:
			outputs = append(outputs, converted)
		case audio.KindSource:
			inputs = append(inputs, converted)
		}
	}

	return tui.Snapshot{
		Phone: tui.PhoneSnapshotFromState(snapshot.State),
		Contacts: tui.ContactsSnapshot{
			Contacts: contacts,
		},
		Audio: tui.AudioSnapshot{
			Inputs:         inputs,
			Outputs:        outputs,
			SelectedInput:  snapshot.AudioConfig.Input,
			SelectedOutput: snapshot.AudioConfig.Output,
		},
		Account: tui.AccountSnapshot{
			Configured:  snapshot.Account.Configured,
			Server:      app.ClampText(snapshot.Account.Server, storage.MaxFieldLength),
			Username:    app.ClampText(snapshot.Account.Username, storage.MaxFieldLength),
			Domain:      app.ClampText(snapshot.Account.Domain, storage.MaxFieldLength),
			Login:       app.ClampText(snapshot.Account.Login, storage.MaxFieldLength),
			HasPassword: snapshot.Account.HasPassword,
			Secure:      snapshot.Account.Secure,
		},
		History: tui.CallHistorySnapshot{
			Calls: app.ResolveHistoryPeers(snapshot.History, domainContacts, countryCode),
		},
		Now: time.Now(),
	}
}
