package app

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	ansiEscapePattern = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
	userAgentsPattern = regexp.MustCompile(`(?i)User Agents \((\d+)\)`)
	aorPattern        = regexp.MustCompile(`(?i)sips?:([^;>\s]+)`)
	registrationOK    = regexp.MustCompile(`\bOK\b`)
	registrationFail  = regexp.MustCompile(`(?i)\b(?:ERR|FAIL)\b`)
	audioFailure      = regexp.MustCompile(`(?i)no such|Format should be|failed`)
	callFailure       = regexp.MustCompile(`(?i)command not found|no active call|not found|could not|cannot|can't|unable to|failed|error|invalid`)
)

// RegistrationInfo is the parsed result of a baresip reginfo response.
type RegistrationInfo struct {
	Known      bool
	Count      int
	Registered bool
	Failed     bool
	AOR        string
}

// StripANSI removes terminal CSI escape sequences from command output.
func StripANSI(text string) string {
	return ansiEscapePattern.ReplaceAllString(text, "")
}

// ParseRegistrationOutput parses baresip's textual reginfo output.
func ParseRegistrationOutput(data string) RegistrationInfo {
	clean := StripANSI(data)
	match := userAgentsPattern.FindStringSubmatch(clean)
	if match == nil {
		return RegistrationInfo{}
	}
	count, err := strconv.Atoi(match[1])
	if err != nil {
		return RegistrationInfo{}
	}

	failed := registrationFail.MatchString(clean)
	info := RegistrationInfo{
		Known:      true,
		Count:      count,
		Registered: count > 0 && registrationOK.MatchString(clean) && !failed,
		Failed:     failed,
	}
	if aor := aorPattern.FindStringSubmatch(clean); aor != nil {
		info.AOR = ClampText(aor[1], MaxRegistrationAOR)
	}
	return info
}

// ParseReginfo is an alias matching the corresponding Model.js operation.
func ParseReginfo(data string) RegistrationInfo {
	return ParseRegistrationOutput(data)
}

// ParseAudioCommandError returns the first-line baresip error. Baresip may
// report these failures in output while returning a successful process code.
func ParseAudioCommandError(data string) string {
	for _, line := range strings.Split(StripANSI(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if audioFailure.MatchString(line) {
			return ClampText(line, MaxCommandError)
		}
		return ""
	}
	return ""
}

// ParseCallCommandError returns the first-line baresip error of a call
// command. Baresip answers a refused dial, accept or hangup with explanatory
// output instead of a failed D-Bus call, so a caller that only checks the
// transport treats the refusal as success.
func ParseCallCommandError(data string) string {
	for _, line := range strings.Split(StripANSI(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if callFailure.MatchString(line) {
			return ClampText(line, MaxCommandError)
		}
		return ""
	}
	return ""
}

// AudioSwitchError is an alias matching the corresponding Model.js operation.
func AudioSwitchError(data string) string {
	return ParseAudioCommandError(data)
}
