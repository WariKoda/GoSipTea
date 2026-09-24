// Package notify sends bounded desktop notifications through notify-send.
package notify

import (
	"context"
	"errors"
	"fmt"
	"html"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	DefaultAppName = "GoSipTea"
	DefaultTimeout = 3 * time.Second

	MaxAppNameBytes = 128
	MaxSummaryBytes = 256
	MaxBodyBytes    = 4096
	MaxOutputBytes  = 16 * 1024
)

type commandRunner func(context.Context, int, ...string) error

// Notifier invokes notify-send directly. It never passes notification text to
// a shell.
type Notifier struct {
	appName        string
	timeout        time.Duration
	maxOutputBytes int
	run            commandRunner
}

// New returns a notifier with the given desktop application name.
// Send reports an error if appName is empty, invalid UTF-8, contains control
// characters, or exceeds MaxAppNameBytes.
func New(appName string) *Notifier {
	return &Notifier{
		appName:        appName,
		timeout:        DefaultTimeout,
		maxOutputBytes: MaxOutputBytes,
		run:            runCommand,
	}
}

// Send displays a notification using DefaultAppName.
func Send(ctx context.Context, summary, body string) error {
	return New(DefaultAppName).Send(ctx, summary, body)
}

// Send displays a notification.
//
// Summary and body are escaped so notification servers render markup as text.
// Unsupported control characters are removed, invalid UTF-8 is replaced, and
// each argument is truncated on a rune boundary to its exported byte limit.
func (n *Notifier) Send(ctx context.Context, summary, body string) error {
	if n == nil {
		return errors.New("notify: nil notifier")
	}

	args, err := BuildArgs(n.appName, summary, body)
	if err != nil {
		return fmt.Errorf("notify: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, n.timeout)
	defer cancel()
	if err := n.run(ctx, n.maxOutputBytes, args...); err != nil {
		return fmt.Errorf("notify: send notification: %w", err)
	}
	return nil
}

// BuildArgs constructs safe notify-send arguments without invoking a command.
func BuildArgs(appName, summary, body string) ([]string, error) {
	if err := validateAppName(appName); err != nil {
		return nil, err
	}

	return []string{
		"--app-name", appName,
		"--",
		boundedPlainText(summary, MaxSummaryBytes),
		boundedPlainText(body, MaxBodyBytes),
	}, nil
}

func validateAppName(appName string) error {
	if appName == "" {
		return errors.New("app name is empty")
	}
	if len(appName) > MaxAppNameBytes {
		return fmt.Errorf("app name exceeds %d bytes", MaxAppNameBytes)
	}
	if !utf8.ValidString(appName) {
		return errors.New("app name is not valid UTF-8")
	}
	for _, r := range appName {
		if unicode.IsControl(r) {
			return errors.New("app name contains a control character")
		}
	}
	return nil
}

func boundedPlainText(value string, limit int) string {
	value = strings.ToValidUTF8(value, "�")
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")

	var result strings.Builder
	result.Grow(min(len(value), limit))
	for _, r := range value {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			continue
		}

		escaped := html.EscapeString(string(r))
		if result.Len()+len(escaped) > limit {
			break
		}
		result.WriteString(escaped)
	}
	return result.String()
}
