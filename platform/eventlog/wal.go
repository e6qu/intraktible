// SPDX-License-Identifier: AGPL-3.0-or-later

package eventlog

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

// WAL is a pure-Go, file-backed, append-only event log: one JSON object per
// line, durably fsync'd. Events are also held in memory for fast Read/replay
// (MVP assumption: the log fits in memory). The Log interface lets a segmented
// backend or BadgerDB replace this later.
type WAL struct {
	mu     sync.Mutex
	f      *os.File
	w      *bufio.Writer
	events []Envelope
	bus    *bus
	closed bool
}

// OpenWAL opens (or creates) the log file at dir/events.log and loads it.
func OpenWAL(dir string) (*WAL, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("eventlog: mkdir %q: %w", dir, err)
	}
	// dir is operator-provided config (the --data-dir flag), not request input,
	// and the filename is constant, so the joined path is not attacker-controlled.
	path := filepath.Join(dir, "events.log")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600) // #nosec G304 -- operator config path, not request input
	if err != nil {
		return nil, fmt.Errorf("eventlog: open %q: %w", path, err)
	}
	w := &WAL{f: f, w: bufio.NewWriter(f), bus: newBus()}
	if err := w.load(); err != nil {
		_ = f.Close()
		return nil, err
	}
	return w, nil
}

// load replays the file into memory. Each record is a newline-terminated JSON
// line. A complete line that fails to parse is real corruption and fails loudly.
// A trailing line WITHOUT a newline is a torn write — a crash interrupted an
// Append before it fsync'd and acknowledged — so it is truncated and recovered
// (no acknowledged event is dropped). It also leaves the write offset at the end
// of the last good record so subsequent appends are clean.
func (w *WAL) load() error {
	r := bufio.NewReaderSize(w.f, 64*1024)
	var offset int64 // bytes of complete, valid records
	for {
		line, err := r.ReadBytes('\n')
		complete := len(line) > 0 && line[len(line)-1] == '\n'
		if complete {
			if rec := line[:len(line)-1]; len(rec) > 0 {
				var e Envelope
				if uerr := json.Unmarshal(rec, &e); uerr != nil {
					return fmt.Errorf("eventlog: corrupt record at seq %d: %w", len(w.events)+1, uerr)
				}
				w.events = append(w.events, e)
			}
			offset += int64(len(line))
			continue
		}
		// No trailing newline: either clean EOF (offset==size, no-op) or a torn
		// final write to drop.
		if err == io.EOF {
			if len(line) > 0 {
				slog.Warn("eventlog: discarding torn final record (crash during append)",
					"bytes", len(line), "after_seq", len(w.events))
			}
			break
		}
		if err != nil {
			return fmt.Errorf("eventlog: read log: %w", err)
		}
	}
	if err := w.f.Truncate(offset); err != nil {
		return fmt.Errorf("eventlog: truncate to last good record: %w", err)
	}
	if _, err := w.f.Seek(offset, io.SeekStart); err != nil {
		return fmt.Errorf("eventlog: seek to append position: %w", err)
	}
	return nil
}

// Append assigns the next Seq, persists durably, then publishes. Caller-supplied
// Seq is ignored; tenancy and Time must be set by the (imperative-shell) caller.
func (w *WAL) Append(_ context.Context, e Envelope) (Envelope, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return Envelope{}, ErrClosed
	}
	if e.Org == "" || e.Workspace == "" {
		return Envelope{}, fmt.Errorf("eventlog: event %q missing org/workspace", e.Type)
	}
	if e.ID == "" {
		e.ID = newID()
	}
	e.Seq = uint64(len(w.events)) + 1
	b, err := json.Marshal(e)
	if err != nil {
		return Envelope{}, fmt.Errorf("eventlog: marshal: %w", err)
	}
	if _, err := w.w.Write(append(b, '\n')); err != nil {
		return Envelope{}, fmt.Errorf("eventlog: write: %w", err)
	}
	if err := w.w.Flush(); err != nil {
		return Envelope{}, fmt.Errorf("eventlog: flush: %w", err)
	}
	if err := w.f.Sync(); err != nil {
		return Envelope{}, fmt.Errorf("eventlog: fsync: %w", err)
	}
	w.events = append(w.events, e)
	w.bus.publish(e)
	return e, nil
}

// Read returns all events with Seq >= fromSeq, in order.
func (w *WAL) Read(_ context.Context, fromSeq uint64) ([]Envelope, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil, ErrClosed
	}
	if fromSeq == 0 {
		fromSeq = 1
	}
	if fromSeq > uint64(len(w.events)) {
		return nil, nil
	}
	out := make([]Envelope, len(w.events[fromSeq-1:]))
	copy(out, w.events[fromSeq-1:])
	return out, nil
}

// Subscribe returns events appended after the call (in-process bus).
func (w *WAL) Subscribe() (<-chan Envelope, func()) { return w.bus.subscribe() }

// Head returns the highest assigned Seq (0 when empty).
func (w *WAL) Head() uint64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return uint64(len(w.events))
}

// Close flushes and closes the log and all subscriptions.
func (w *WAL) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	w.bus.closeAll()
	if err := w.w.Flush(); err != nil {
		_ = w.f.Close()
		return err
	}
	return w.f.Close()
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
