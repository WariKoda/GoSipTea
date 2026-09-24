package session

import (
	"io"
	"sync"
)

// syncWriter serializes the two output streams of the baresip child process
// into one destination.
type syncWriter struct {
	mu     sync.Mutex
	writer io.Writer
}

func newSyncWriter(writer io.Writer) *syncWriter {
	return &syncWriter{writer: writer}
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writer.Write(p)
}
