package storage

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const audioDriver = "pipewire"

var audioLinePattern = regexp.MustCompile(`^\s*(audio_player|audio_alert|audio_source)\s+(\S*)`)

// AudioConfig stores PipeWire node names. Empty values select system defaults.
type AudioConfig struct {
	Output string
	Input  string
}

// ReadAudioConfig reads audio_player and audio_source. audio_alert does not override Output.
func (s *Store) ReadAudioConfig() (AudioConfig, error) {
	var result AudioConfig
	if err := s.validate(); err != nil {
		return result, err
	}
	data, err := readRegularFile(s.paths.Config, false)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, fmt.Errorf("read config: %w", err)
	}
	for _, line := range splitLines(data) {
		match := audioLinePattern.FindStringSubmatch(line)
		if match == nil || match[1] == "audio_alert" {
			continue
		}
		device := parseAudioDevice(match[2])
		if match[1] == "audio_player" {
			result.Output = device
		} else {
			result.Input = device
		}
	}
	return result, nil
}

// WriteAudioConfig updates every managed audio line and leaves all other lines unchanged.
func (s *Store) WriteAudioConfig(config AudioConfig) error {
	config.Output = strings.TrimSpace(config.Output)
	config.Input = strings.TrimSpace(config.Input)
	if err := validateDeviceName("output", config.Output); err != nil {
		return err
	}
	if err := validateDeviceName("input", config.Input); err != nil {
		return err
	}

	return s.withExclusiveLock(func() error {
		if err := refuseSymlinkOrSpecial(s.paths.Config, false); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("%w: baresip config does not exist", ErrNotFound)
			}
			return fmt.Errorf("write config: %w", err)
		}
		data, err := readRegularFile(s.paths.Config, false)
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: baresip config does not exist", ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("read config before write: %w", err)
		}

		lines := splitLines(data)
		seen := make(map[string]bool, 3)
		output := make([]string, 0, len(lines)+3)
		for _, line := range lines {
			match := audioLinePattern.FindStringSubmatch(line)
			if match == nil {
				output = append(output, line)
				continue
			}
			key := match[1]
			seen[key] = true
			device := config.Output
			if key == "audio_source" {
				device = config.Input
			}
			output = append(output, renderAudioLine(key, device))
		}
		for _, key := range []string{"audio_player", "audio_source", "audio_alert"} {
			if seen[key] {
				continue
			}
			device := config.Output
			if key == "audio_source" {
				device = config.Input
			}
			output = append(output, renderAudioLine(key, device))
		}
		if err := atomicWrite(s.paths.Config, joinLines(output), 0o644); err != nil {
			return fmt.Errorf("write config: %w", err)
		}
		return nil
	})
}

func parseAudioDevice(value string) string {
	parts := strings.SplitN(value, ",", 2)
	if len(parts) == 1 {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

func renderAudioLine(key, device string) string {
	value := audioDriver
	if device != "" {
		value += "," + device
	}
	return fmt.Sprintf("%-24s%s", key, value)
}

func validateDeviceName(name, value string) error {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) > MaxFieldLength {
		return fmt.Errorf("%w: invalid %s device name", ErrInvalid, name)
	}
	for _, char := range value {
		if char <= 0x1f || unicode.IsSpace(char) || char == ',' {
			return fmt.Errorf("%w: invalid %s device name", ErrInvalid, name)
		}
	}
	return nil
}
