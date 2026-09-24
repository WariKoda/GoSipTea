package notify

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

func runCommand(ctx context.Context, maxOutputBytes int, args ...string) error {
	if maxOutputBytes < 1 {
		return errors.New("output limit must be positive")
	}

	stdout := newBoundedBuffer(maxOutputBytes)
	stderr := newBoundedBuffer(maxOutputBytes)
	cmd := exec.CommandContext(ctx, "notify-send", args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.WaitDelay = time.Second

	err := cmd.Run()
	if stdout.overflow || stderr.overflow {
		return fmt.Errorf("notify-send output exceeded %d bytes per stream", maxOutputBytes)
	}
	if err != nil {
		message := strings.TrimSpace(stderr.String())
		if message != "" {
			return fmt.Errorf("notify-send: %w: %s", err, message)
		}
		return fmt.Errorf("notify-send: %w", err)
	}
	return nil
}

type boundedBuffer struct {
	buffer   bytes.Buffer
	limit    int
	overflow bool
}

func newBoundedBuffer(limit int) *boundedBuffer {
	return &boundedBuffer{limit: limit}
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	originalLength := len(p)
	remaining := b.limit - b.buffer.Len()
	if remaining < len(p) {
		b.overflow = true
		if remaining < 0 {
			remaining = 0
		}
		p = p[:remaining]
	}
	_, _ = b.buffer.Write(p)
	return originalLength, nil
}

func (b *boundedBuffer) String() string {
	return b.buffer.String()
}
