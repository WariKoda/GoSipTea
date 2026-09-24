package app

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	MaxDialTargetLength  = 255
	MaxPeerInputLength   = 255
	MaxPeerDisplayLength = 64
	MaxRegistrationAOR   = 96
	MaxCommandError      = 160
	MaxBridgeLineLength  = 65536
)

var (
	ErrInputTooLong     = errors.New("input exceeds length limit")
	ErrControlCharacter = errors.New("input contains a control character")
)

// ValidateInput checks untrusted input without changing it. Lengths are counted
// in Unicode code points so truncation never splits an encoded character.
func ValidateInput(text string, maxRunes int) error {
	if maxRunes < 0 || utf8.RuneCountInString(text) > maxRunes {
		return ErrInputTooLong
	}
	for _, r := range text {
		if isInputControl(r) {
			return ErrControlCharacter
		}
	}
	return nil
}

// ClampText replaces control characters and bounds text for display.
func ClampText(text string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}

	clean := strings.Map(func(r rune) rune {
		if isInputControl(r) {
			return ' '
		}
		return r
	}, text)
	if utf8.RuneCountInString(clean) <= maxRunes {
		return clean
	}

	runes := []rune(clean)
	if maxRunes == 1 {
		return "…"
	}
	return string(runes[:maxRunes-1]) + "…"
}

// NotificationText strips markup metacharacters after bounding the text.
func NotificationText(text string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '<', '>', '&', '"':
			return -1
		default:
			return r
		}
	}, ClampText(text, 80))
}

// NormalizeDialTarget converts a number, extension, or address to a baresip
// dial argument. An empty return value means the target is invalid.
func NormalizeDialTarget(raw string) string {
	target := strings.TrimSpace(raw)
	if target == "" || utf8.RuneCountInString(target) > MaxDialTargetLength {
		return ""
	}

	lower := strings.ToLower(target)
	isURI := strings.HasPrefix(lower, "sip:") || strings.HasPrefix(lower, "sips:")
	if isURI || (strings.Contains(target, "@") && !strings.HasPrefix(target, "@")) {
		for _, r := range target {
			if r < 0x21 || r > 0x7e {
				return ""
			}
		}
		if isURI {
			return target
		}
		return "sip:" + target
	}

	var normalized strings.Builder
	for _, r := range target {
		if r >= '0' && r <= '9' || r == '+' || r == '*' || r == '#' {
			normalized.WriteRune(r)
		}
	}
	return normalized.String()
}

// NormalizeTarget is kept as a short name for clients ported from Model.js.
func NormalizeTarget(raw string) string {
	return NormalizeDialTarget(raw)
}

func isInputControl(r rune) bool {
	return unicode.IsControl(r)
}
