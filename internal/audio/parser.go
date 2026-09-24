package audio

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

const maxParserLineBytes = 64 * 1024

// InspectProperties contains the PipeWire properties needed to construct a
// Node. ParseInspect does not substitute display labels for Name.
type InspectProperties struct {
	Name        string
	Description string
	Nick        string
	Kind        Kind
	Internal    bool
}

// ParseList parses the tab-separated output of "wpctl list audio".
//
// The second column is retained only so List can detect an ID reuse while it
// runs wpctl inspect. List publishes node.name from inspect as the final Name.
func ParseList(r io.Reader) ([]Node, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), maxParserLineBytes)

	var nodes []Node
	ids := make(map[uint32]struct{})
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}

		fields := strings.Split(line, "\t")
		if len(fields) < 3 {
			return nil, fmt.Errorf("line %d: expected tab-separated wpctl fields", lineNumber)
		}

		kind, ok := parseMediaClass(strings.TrimSpace(fields[2]))
		if !ok {
			continue
		}
		if len(fields) != 4 {
			return nil, fmt.Errorf("line %d: expected 4 fields for an audio node, got %d", lineNumber, len(fields))
		}

		idValue, err := strconv.ParseUint(strings.TrimSpace(fields[0]), 10, 32)
		if err != nil {
			return nil, fmt.Errorf("line %d: invalid node ID: %w", lineNumber, err)
		}
		id := uint32(idValue)
		if _, duplicate := ids[id]; duplicate {
			return nil, fmt.Errorf("line %d: duplicate node ID %d", lineNumber, id)
		}
		ids[id] = struct{}{}

		name := strings.TrimSpace(fields[1])
		if name == "" {
			return nil, fmt.Errorf("line %d: empty node name", lineNumber)
		}

		defaultMarker := strings.TrimSpace(fields[3])
		if defaultMarker != "" && defaultMarker != "*" {
			return nil, fmt.Errorf("line %d: unknown default marker %q", lineNumber, defaultMarker)
		}

		nodes = append(nodes, Node{
			ID:      id,
			Name:    name,
			Default: defaultMarker == "*",
			Kind:    kind,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read wpctl list output: %w", err)
	}

	return nodes, nil
}

// ParseStatusFilters reads audio endpoints from the Filters section of
// "wpctl status -n". Split-device endpoints are absent from "wpctl list audio".
func ParseStatusFilters(r io.Reader) ([]Node, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), maxParserLineBytes)

	var records strings.Builder
	inAudio, inFilters, foundAudio := false, false, false
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		switch line {
		case "Audio":
			inAudio, foundAudio = true, true
			inFilters = false
			continue
		case "Video", "Settings":
			inAudio, inFilters = false, false
			continue
		}
		if !inAudio {
			continue
		}
		line = strings.TrimLeft(line, " \t│├└─")
		if strings.HasSuffix(line, ":") {
			inFilters = line == "Filters:"
			continue
		}
		if !inFilters || !strings.HasSuffix(line, "]") {
			continue
		}
		classStart := strings.LastIndex(line, "[")
		if classStart < 0 {
			continue
		}
		mediaClass := line[classStart+1 : len(line)-1]
		if _, ok := parseMediaClass(mediaClass); !ok {
			continue
		}
		entry := strings.TrimSpace(line[:classStart])
		defaultMarker := ""
		if strings.HasPrefix(entry, "*") {
			defaultMarker = "*"
			entry = strings.TrimSpace(strings.TrimPrefix(entry, "*"))
		}
		id, name, ok := strings.Cut(entry, ".")
		if !ok {
			return nil, fmt.Errorf("line %d: missing filter node ID separator", lineNumber)
		}
		fmt.Fprintf(&records, "%s\t%s\t%s\t%s\n", strings.TrimSpace(id), strings.TrimSpace(name), mediaClass, defaultMarker)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read wpctl status output: %w", err)
	}
	if !foundAudio {
		return nil, fmt.Errorf("missing Audio section in wpctl status output")
	}
	return ParseList(strings.NewReader(records.String()))
}

// ParseInspect parses the properties printed by "wpctl inspect ID".
func ParseInspect(r io.Reader) (InspectProperties, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), maxParserLineBytes)

	properties := InspectProperties{}
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		line = strings.TrimSpace(strings.TrimPrefix(line, "*"))
		key, value, ok := strings.Cut(line, " = ")
		if !ok {
			continue
		}

		key = strings.TrimSpace(key)
		if key != "node.name" && key != "node.description" && key != "node.nick" && key != "media.class" {
			continue
		}

		decoded, err := decodeProperty(strings.TrimSpace(value))
		if err != nil {
			return InspectProperties{}, fmt.Errorf("line %d: parse %s: %w", lineNumber, key, err)
		}

		switch key {
		case "node.name":
			properties.Name = decoded
		case "node.description":
			properties.Description = decoded
		case "node.nick":
			properties.Nick = decoded
		case "media.class":
			mediaClass := strings.ToLower(decoded)
			properties.Internal = mediaClass == "audio/source/internal" || mediaClass == "audio/sink/internal"
			if properties.Internal {
				mediaClass = strings.TrimSuffix(mediaClass, "/internal")
			}
			kind, valid := parseMediaClass(mediaClass)
			if !valid {
				return InspectProperties{}, fmt.Errorf("line %d: unsupported media.class %q", lineNumber, decoded)
			}
			properties.Kind = kind
		}
	}
	if err := scanner.Err(); err != nil {
		return InspectProperties{}, fmt.Errorf("read wpctl inspect output: %w", err)
	}

	if properties.Kind == "" {
		return InspectProperties{}, fmt.Errorf("missing media.class property")
	}
	return properties, nil
}

func parseMediaClass(value string) (Kind, bool) {
	switch strings.ToLower(value) {
	case "audio/sink":
		return KindSink, true
	case "audio/source":
		return KindSource, true
	default:
		return "", false
	}
}

func decodeProperty(value string) (string, error) {
	if !strings.HasPrefix(value, "\"") {
		return value, nil
	}

	decoded, err := strconv.Unquote(value)
	if err != nil {
		return "", err
	}
	return decoded, nil
}
