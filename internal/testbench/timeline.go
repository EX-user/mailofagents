package testbench

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Event is one timeline record: everything the bench did or saw, in
// order, with timestamps. The timeline is the evidence backbone for the
// rubric (M3) and the replay pack — assertions cite it, they do not
// stand alone.
type Event struct {
	At   time.Time `json:"at"`
	Kind string    `json:"kind"` // call | mail | note | assert
	What string    `json:"what"`
	// Detail carries kind-specific payload (request/response summaries).
	Detail any `json:"detail,omitempty"`
}

// Timeline is an append-only, crash-safe (flush-per-event) JSONL log.
type Timeline struct {
	mu   sync.Mutex
	f    *os.File
	path string
}

// OpenTimeline creates (or appends) the JSONL timeline at path.
func OpenTimeline(path string) (*Timeline, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("timeline dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("timeline open: %w", err)
	}
	return &Timeline{f: f, path: path}, nil
}

// Add records one event; encoding errors are returned (a bench that
// silently loses evidence is worse than one that stops).
func (t *Timeline) Add(kind, what string, detail any) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	line, err := json.Marshal(Event{At: time.Now(), Kind: kind, What: what, Detail: detail})
	if err != nil {
		return fmt.Errorf("timeline encode: %w", err)
	}
	if _, err := t.f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("timeline write: %w", err)
	}
	return t.f.Sync()
}

// Close flushes and closes the underlying file.
func (t *Timeline) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.f.Close()
}

// Path returns where this timeline lives (for the report).
func (t *Timeline) Path() string { return t.path }
