// Package audio discovers PipeWire audio nodes through wpctl.
package audio

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// Kind identifies whether a PipeWire node accepts playback or provides capture.
type Kind string

const (
	KindSink   Kind = "sink"
	KindSource Kind = "source"

	DefaultTimeout        = 5 * time.Second
	DefaultMaxOutputBytes = 256 * 1024
	DefaultMaxNodes       = 256
)

// Node is a PipeWire node that baresip can use.
//
// Name comes from PipeWire's node.name property, not from wpctl's display
// label. Baresip's pipewire backend accepts this value as its device name.
// Description comes from node.description, then node.nick if no description is
// present, and finally Name if PipeWire publishes no human-readable metadata.
type Node struct {
	ID          uint32
	Name        string
	Description string
	Default     bool
	Kind        Kind
}

type commandRunner func(context.Context, int, ...string) ([]byte, error)

// Lister discovers the current PipeWire sinks and sources.
//
// Discovery is not an atomic PipeWire transaction: wpctl list and status take
// snapshots and wpctl inspect reads each node afterward. List rejects a
// node if its ID has been reused or its name changed between those commands.
type Lister struct {
	timeout        time.Duration
	maxOutputBytes int
	maxNodes       int
	run            commandRunner
}

// NewLister returns a lister configured for the wpctl found on PATH.
func NewLister() *Lister {
	return &Lister{
		timeout:        DefaultTimeout,
		maxOutputBytes: DefaultMaxOutputBytes,
		maxNodes:       DefaultMaxNodes,
		run:            runCommand,
	}
}

// List returns the current PipeWire sinks and sources.
func List(ctx context.Context) ([]Node, error) {
	return NewLister().List(ctx)
}

// List returns the current PipeWire sinks and sources.
func (l *Lister) List(ctx context.Context) ([]Node, error) {
	if l == nil {
		return nil, errors.New("audio: nil lister")
	}

	ctx, cancel := context.WithTimeout(ctx, l.timeout)
	defer cancel()

	output, err := l.run(ctx, l.maxOutputBytes, "list", "audio")
	if err != nil {
		return nil, fmt.Errorf("audio: list PipeWire nodes: %w", err)
	}

	nodes, err := ParseList(bytes.NewReader(output))
	if err != nil {
		return nil, fmt.Errorf("audio: parse wpctl list: %w", err)
	}
	if len(nodes) > l.maxNodes {
		return nil, fmt.Errorf("audio: wpctl listed %d audio nodes, limit is %d", len(nodes), l.maxNodes)
	}

	output, err = l.run(ctx, l.maxOutputBytes, "status", "-n")
	if err != nil {
		return nil, fmt.Errorf("audio: list PipeWire filters: %w", err)
	}
	filters, err := ParseStatusFilters(bytes.NewReader(output))
	if err != nil {
		return nil, fmt.Errorf("audio: parse wpctl status: %w", err)
	}
	indices := make(map[uint32]int, len(nodes))
	for i, node := range nodes {
		indices[node.ID] = i
	}
	for _, node := range filters {
		if i, exists := indices[node.ID]; exists {
			if nodes[i].Name != node.Name || nodes[i].Kind != node.Kind {
				return nil, fmt.Errorf("audio: node %d changed between list and status", node.ID)
			}
			nodes[i].Default = node.Default
			continue
		}
		indices[node.ID] = len(nodes)
		nodes = append(nodes, node)
	}
	if len(nodes) > l.maxNodes {
		return nil, fmt.Errorf("audio: wpctl listed %d audio nodes, limit is %d", len(nodes), l.maxNodes)
	}

	available := make([]Node, 0, len(nodes))
	for i := range nodes {
		id := strconv.FormatUint(uint64(nodes[i].ID), 10)
		output, inspectErr := l.run(ctx, l.maxOutputBytes, "inspect", id)
		if inspectErr != nil {
			return nil, fmt.Errorf("audio: inspect node %s: %w", id, inspectErr)
		}

		properties, parseErr := ParseInspect(bytes.NewReader(output))
		if parseErr != nil {
			return nil, fmt.Errorf("audio: parse inspection for node %s: %w", id, parseErr)
		}
		// wpctl list can report internal split-device nodes as ordinary sources
		// or sinks. Only inspect exposes the class that excludes them here.
		if properties.Internal {
			continue
		}
		if properties.Name == "" {
			return nil, fmt.Errorf("audio: node %s has no node.name property", id)
		}
		if properties.Name != nodes[i].Name {
			return nil, fmt.Errorf("audio: node %s changed during discovery: list name %q, inspected name %q", id, nodes[i].Name, properties.Name)
		}
		if properties.Kind != nodes[i].Kind {
			return nil, fmt.Errorf("audio: node %s changed kind during discovery: list kind %q, inspected kind %q", id, nodes[i].Kind, properties.Kind)
		}

		nodes[i].Name = properties.Name
		nodes[i].Description = properties.Description
		if nodes[i].Description == "" {
			nodes[i].Description = properties.Nick
		}
		if nodes[i].Description == "" {
			nodes[i].Description = nodes[i].Name
		}
		available = append(available, nodes[i])
	}

	return available, nil
}
