// SPDX-License-Identifier: AGPL-3.0-or-later

package eventlog

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

// WAL is a pure-Go, file-backed, append-only event log: one JSON object per line,
// durably fsync'd. It keeps only a compact in-memory byte-offset index (one int64
// per record) — NOT the events themselves — and streams records from disk on
// demand via ReadAt. Retained memory is therefore O(events) small offsets rather
// than O(events × envelope size), and opening the log scans for record boundaries
// without decoding every record.
//
// Records are validated lazily, when Read decodes them: a corrupt complete record
// fails loudly there. The projection runtime Reads the whole log at boot (to
// rebuild), so corruption still stops startup — it just surfaces at Read, not at
// open. A torn final write (a crash mid-Append, before fsync) has no trailing
// newline and is dropped on open. For O(1)-open durability and indexed reads at
// large scale, the SQLite-backed Log (SQLiteLog) is the alternative backend.
type WAL struct {
	mu      sync.Mutex
	f       walFile
	w       *bufio.Writer
	offsets []int64 // byte offset where each record (seq = index+1) begins
	size    int64   // total bytes of complete records (= the append position)
	bus     *bus
	closed  bool
	// failed poisons Append after a failed write could not be rolled back: the
	// file then holds bytes past w.size, so appending more would corrupt the log.
	// Reads stay available (they never look past w.size); reopening recovers.
	failed error
	// claimed holds the Unique keys appended in THIS process, for ErrConflict
	// detection. The WAL is single-process (the embedded binary), and the writer
	// folds the log before computing a claim, so a cross-restart duplicate can't
	// arise; this only needs to catch a within-process double-claim, honoring the
	// Append contract uniformly with the shared SQL backends.
	claimed map[string]bool
}

// walFile is the slice of *os.File the WAL uses, an interface so tests can
// inject write/fsync failures; OpenWAL always passes the real file.
type walFile interface {
	io.Reader
	io.Writer
	io.ReaderAt
	io.Closer
	Truncate(size int64) error
	Seek(offset int64, whence int) (int64, error)
	Sync() error
}

// OpenWAL opens (or creates) the log file at dir/events.log and indexes it.
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
	w := &WAL{f: f, w: bufio.NewWriter(f), bus: newBus(), claimed: map[string]bool{}}
	if err := w.load(); err != nil {
		_ = f.Close()
		return nil, err
	}
	return w, nil
}

// load scans the file to build the offset index. Records are NOT decoded here
// (that happens lazily in Read), so a malformed-but-complete record is caught
// when it is read, not at open. A trailing line WITHOUT a newline is a torn write
// — a crash interrupted an Append before it fsync'd and acknowledged — so it is
// truncated and recovered (no acknowledged event is dropped). The file is always
// truncated and the write head seeked to the end of the last complete record, so
// subsequent appends are clean.
func (w *WAL) load() error {
	r := bufio.NewReaderSize(w.f, 64*1024)
	var pos int64 // bytes of complete records
	for {
		line, err := r.ReadBytes('\n')
		complete := len(line) > 0 && line[len(line)-1] == '\n'
		if complete {
			if len(line) > 1 { // a non-empty record (skip stray blank lines)
				w.offsets = append(w.offsets, pos)
				// Rebuild the Unique-claim set from disk so a key claimed BEFORE this
				// restart is still enforced — otherwise Append's "unique across the whole
				// log" contract would silently lapse across a reopen and accept a duplicate
				// (a stale optimistic-concurrency claim). Best-effort like the rest of load:
				// a record's full validity is checked lazily on Read.
				var rec struct {
					Unique string `json:"unique"`
				}
				if json.Unmarshal(line, &rec) == nil && rec.Unique != "" {
					w.claimed[rec.Unique] = true
				}
			}
			pos += int64(len(line))
			continue
		}
		// No trailing newline: clean EOF (no-op) or a torn final write to drop.
		if err == io.EOF {
			if len(line) > 0 {
				slog.Warn("eventlog: discarding torn final record (crash during append)",
					"bytes", len(line), "after_seq", len(w.offsets))
			}
			break
		}
		if err != nil {
			return fmt.Errorf("eventlog: read log: %w", err)
		}
	}
	w.size = pos
	if err := w.f.Truncate(pos); err != nil {
		return fmt.Errorf("eventlog: truncate to last good record: %w", err)
	}
	if _, err := w.f.Seek(pos, io.SeekStart); err != nil {
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
	if w.failed != nil {
		return Envelope{}, w.failed
	}
	e, err := stampForAppend(e, w.claimed, uint64(len(w.offsets))+1)
	if err != nil {
		return Envelope{}, err
	}
	b, err := json.Marshal(e)
	if err != nil {
		return Envelope{}, fmt.Errorf("eventlog: marshal: %w", err)
	}
	// One record = the JSON line + a newline terminator. Any failure past this
	// point may have left partial (or complete-but-not-durable) record bytes in
	// the file while the in-memory index has NOT advanced — roll the file back to
	// w.size so the ghost can't mis-index the next append or, worst for the fsync
	// case, replay after a reopen an event whose Append the caller saw fail.
	if _, err := w.w.Write(b); err != nil {
		w.rollback()
		return Envelope{}, fmt.Errorf("eventlog: write: %w", err)
	}
	if err := w.w.WriteByte('\n'); err != nil {
		w.rollback()
		return Envelope{}, fmt.Errorf("eventlog: write: %w", err)
	}
	if err := w.w.Flush(); err != nil {
		w.rollback()
		return Envelope{}, fmt.Errorf("eventlog: flush: %w", err)
	}
	if err := w.f.Sync(); err != nil {
		w.rollback()
		return Envelope{}, fmt.Errorf("eventlog: fsync: %w", err)
	}
	w.offsets = append(w.offsets, w.size)
	w.size += int64(len(b)) + 1
	if e.Unique != "" {
		w.claimed[e.Unique] = true
	}
	w.bus.publish(e)
	return e, nil
}

// rollback restores the append invariant after a mid-Append failure: the file is
// truncated back to the last acknowledged record (w.size), the write head is
// re-seeked there, and the bufio writer's buffered bytes and sticky error are
// discarded. If the rollback itself fails, the WAL is poisoned for appends —
// failing loudly beats writing after unacknowledged bytes.
func (w *WAL) rollback() {
	if err := w.f.Truncate(w.size); err != nil {
		w.failed = fmt.Errorf("eventlog: rollback truncate after failed append (reopen the log): %w", err)
		return
	}
	if _, err := w.f.Seek(w.size, io.SeekStart); err != nil {
		w.failed = fmt.Errorf("eventlog: rollback seek after failed append (reopen the log): %w", err)
		return
	}
	w.w.Reset(w.f)
}

// Read returns all events with Seq >= fromSeq, in order, decoding them from disk
// (one ReadAt for the contiguous tail). A corrupt record fails loudly here.
// ReadTenantStream reads then filters (the WAL is a sequential file with no index).
func (w *WAL) ReadTenantStream(ctx context.Context, org, workspace, stream string, fromSeq uint64) ([]Envelope, error) {
	evs, err := w.Read(ctx, fromSeq)
	if err != nil {
		return nil, err
	}
	return filterTenantStream(evs, org, workspace, stream), nil
}

func (w *WAL) Read(_ context.Context, fromSeq uint64) ([]Envelope, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil, ErrClosed
	}
	if fromSeq == 0 {
		fromSeq = 1
	}
	n := uint64(len(w.offsets))
	if fromSeq > n {
		return nil, nil
	}
	start := w.offsets[fromSeq-1]
	buf := make([]byte, w.size-start)
	// ReadAt fills exactly len(buf) on success; a short read (io.EOF with n<len)
	// means the file was truncated under us since the offset index was built — fail
	// loudly rather than json.Unmarshal a partly-zero buffer or silently return fewer
	// events than Head() reports.
	if nRead, err := w.f.ReadAt(buf, start); err != nil && (!errors.Is(err, io.EOF) || nRead != len(buf)) {
		return nil, fmt.Errorf("eventlog: read from seq %d (%d/%d bytes): %w", fromSeq, nRead, len(buf), err)
	}
	out := make([]Envelope, 0, n-fromSeq+1)
	seq := fromSeq
	for _, rec := range bytes.Split(buf, []byte{'\n'}) {
		if len(rec) == 0 {
			continue // trailing newline / blank line
		}
		var e Envelope
		if err := json.Unmarshal(rec, &e); err != nil {
			return nil, fmt.Errorf("eventlog: corrupt record at seq %d: %w", seq, err)
		}
		// The byte-offset position is the authoritative seq (the offset index is built
		// on it, and the projection runtime keys contiguity off e.Seq) — make it win
		// over a possibly-stale stored Seq, matching how SQLite/Postgres derive Seq
		// from the row id rather than trusting the payload.
		e.Seq = seq
		out = append(out, e)
		seq++
	}
	return out, nil
}

// Subscribe returns events appended after the call (in-process bus).
func (w *WAL) Subscribe() (<-chan Envelope, func()) { return w.bus.subscribe() }

// Head returns the highest assigned Seq (0 when empty).
func (w *WAL) Head() uint64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return uint64(len(w.offsets))
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
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		panic("eventlog: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}
