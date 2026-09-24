package baresip

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/godbus/dbus/v5"
)

var commandNamePattern = regexp.MustCompile(`^[a-z_]{1,32}$`)

// BuildCommand validates a baresip command and joins it to its optional params.
func BuildCommand(command, params string) (string, error) {
	if err := validateCommand(command, params); err != nil {
		return "", err
	}

	commandLine := command
	if params != "" {
		commandLine += " " + params
	}
	if len(commandLine) > MaxCommandLineBytes {
		return "", ErrCommandTooLong
	}
	return commandLine, nil
}

// ValidateCommandLine applies the same limits as BuildCommand to a complete line.
func ValidateCommandLine(commandLine string) error {
	if len(commandLine) > MaxCommandLineBytes {
		return ErrCommandTooLong
	}

	command, params, hasParams := strings.Cut(commandLine, " ")
	if !hasParams {
		params = ""
	}
	return validateCommand(command, params)
}

func validateCommand(command, params string) error {
	if len(command) > MaxCommandNameBytes || !commandNamePattern.MatchString(command) {
		return ErrInvalidCommand
	}
	if len(params) > MaxParamsBytes || !utf8.ValidString(params) {
		return ErrInvalidParams
	}
	for _, char := range params {
		if char < 32 {
			return ErrInvalidParams
		}
	}
	return nil
}

// ParseEventSignal parses the JSON payload in the third D-Bus signal argument.
func ParseEventSignal(body []any) (Event, error) {
	if len(body) < 3 {
		return nil, fmt.Errorf("%w: signal has %d arguments", ErrInvalidEvent, len(body))
	}

	var payload string
	switch value := body[2].(type) {
	case string:
		payload = value
	case dbus.Variant:
		var ok bool
		payload, ok = value.Value().(string)
		if !ok {
			return nil, fmt.Errorf("%w: third signal argument is %T", ErrInvalidEvent, value.Value())
		}
	default:
		return nil, fmt.Errorf("%w: third signal argument is %T", ErrInvalidEvent, body[2])
	}

	return ParseEventPayload(payload)
}

// ParseEventPayload decodes one bounded JSON object.
func ParseEventPayload(payload string) (Event, error) {
	if len(payload) > MaxEventBytes {
		return nil, ErrEventTooLarge
	}

	var event Event
	if err := json.Unmarshal([]byte(payload), &event); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidEvent, err)
	}
	if event == nil {
		return nil, fmt.Errorf("%w: payload is not an object", ErrInvalidEvent)
	}
	return event, nil
}

func limitResponse(response string) string {
	if len(response) <= MaxResponseBytes {
		return response
	}

	response = response[:MaxResponseBytes]
	for !utf8.ValidString(response) {
		_, size := utf8.DecodeLastRuneInString(response)
		if size == 0 {
			return ""
		}
		response = response[:len(response)-size]
	}
	return response
}

func serviceUnavailable(err error) error {
	if err == nil {
		return ErrServiceUnavailable
	}
	return errors.Join(ErrServiceUnavailable, err)
}
