package audit

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// Entry captures the essential metadata of a single gateway request for post-hoc analysis.
type Entry struct {
	Time         time.Time `json:"time"`
	Caller       string    `json:"caller"`
	Node         string    `json:"node"`
	Model        string    `json:"model,omitempty"`
	InputTokens  int64     `json:"inputTokens"`
	OutputTokens int64     `json:"outputTokens"`
	Decision     string    `json:"decision"`
	Status       int       `json:"status"`
	Reason       string    `json:"reason,omitempty"`
}

// Log provides an append-only JSON Lines audit trail for gateway requests.
type Log struct {
	mu   sync.Mutex
	w    io.Writer
	file *os.File
}

// Open creates a new Log backed by the file at path, appending to existing content if present.
func Open(path string) (*Log, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening audit log %s: %w", path, err)
	}
	return &Log{
		w:    f,
		file: f,
	}, nil
}

// New creates a Log backed by the provided io.Writer.
func New(w io.Writer) *Log {
	return &Log{
		w: w,
	}
}

// Write records an audit Entry as a single JSON line.
func (l *Log) Write(e Entry) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	} else {
		e.Time = e.Time.UTC()
	}

	b, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("writing audit line: %w", err)
	}

	n, err := l.w.Write(append(b, '\n'))
	if err != nil {
		return fmt.Errorf("writing audit line: %w", err)
	}
	if n < len(b)+1 {
		return fmt.Errorf("writing audit line: %w", io.ErrShortWrite)
	}

	return nil
}

// Close releases resources held by the Log.
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.file != nil {
		err := l.file.Close()
		l.file = nil
		return err
	}
	return nil
}
