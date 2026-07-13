// SPDX-License-Identifier: AGPL-3.0-or-later

package eventlog

import (
	"bufio"
	"bytes"
	"compress/gzip"
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
	"sort"
	"strconv"
	"strings"
	"sync"
)

// WAL is a pure-Go, file-backed, append-only event log: one JSON object per line,
// durably fsync'd. Records are held across a series of SEGMENT files so the log can be
// compacted without ever discarding an event (the full history is a replay/audit
// guarantee). The newest segment — the file `events.log` — is the ACTIVE segment that
// Append writes to; when it exceeds maxSegBytes it is SEALED (renamed to
// `seg-<base>-<last>.log`, read-only) and a fresh active segment is started. Old sealed
// segments are ARCHIVED automatically: gzip-compressed in place once more than keepHot
// of them accumulate, so on-disk size stays bounded while every event remains readable.
//
// A single, un-segmented `events.log` from an earlier version opens transparently as the
// active segment at base seq 1 — no migration step.
//
// Only compact byte-offset indexes are kept in memory (one int64 per record of an
// uncompressed segment), NOT the events; records stream from disk on demand. Seq is
// authoritative from position (contiguous from 1 across all segments) and never moves
// when a segment is sealed or compressed. A corrupt complete record fails loudly at
// Read; a torn final write in the active segment (a crash mid-Append, before fsync) has
// no trailing newline and is dropped on open. For O(1)-open durability and indexed reads
// at large scale, the SQLite-backed Log is the alternative backend.
type WAL struct {
	mu  sync.Mutex
	dir string
	// Active segment (mutable): appended to, faulted in tests via f.
	f          walFile
	w          *bufio.Writer
	activeBase uint64  // seq of the active segment's first record (>= 1)
	offsets    []int64 // byte offset of each active record (record i has seq activeBase+i)
	size       int64   // bytes of complete active records (= the append position)
	// Sealed segments (read-only), ascending by baseSeq.
	sealed      []*segMeta
	maxSegBytes int64 // seal the active segment once it reaches this
	keepHot     int   // uncompressed sealed segments to keep before gzipping the oldest

	bus     *bus
	closed  bool
	failed  error
	claimed map[string]bool
}

// segMeta describes one sealed segment. offsets is set only for an uncompressed segment
// (a compressed one is read by decompressing it whole, so per-record offsets don't apply).
type segMeta struct {
	baseSeq    uint64
	count      uint64
	size       int64
	path       string
	compressed bool
	offsets    []int64
}

func (s *segMeta) lastSeq() uint64 { return s.baseSeq + s.count - 1 }

// walFile is the slice of *os.File the WAL uses, an interface so tests can inject
// write/fsync failures; OpenWAL always passes the real file.
type walFile interface {
	io.Reader
	io.Writer
	io.ReaderAt
	io.Closer
	Truncate(size int64) error
	Seek(offset int64, whence int) (int64, error)
	Sync() error
}

const (
	activeFileName     = "events.log"
	defaultMaxSegBytes = 64 << 20 // 64 MiB
	defaultKeepHot     = 4        // recent sealed segments kept uncompressed
)

// OpenWAL opens (or creates) the segmented log in dir with default segment sizing.
func OpenWAL(dir string) (*WAL, error) {
	return OpenWALSized(dir, defaultMaxSegBytes, defaultKeepHot)
}

// OpenWALSized opens the log with an explicit active-segment size cap and hot-segment
// retention — the demo/tests use small values to exercise rotation and archival.
func OpenWALSized(dir string, maxSegBytes int64, keepHot int) (*WAL, error) {
	if maxSegBytes <= 0 {
		maxSegBytes = defaultMaxSegBytes
	}
	if keepHot < 1 {
		keepHot = 1
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("eventlog: mkdir %q: %w", dir, err)
	}
	w := &WAL{dir: dir, bus: newBus(), claimed: map[string]bool{}, maxSegBytes: maxSegBytes, keepHot: keepHot}
	if err := w.load(); err != nil {
		if w.f != nil {
			_ = w.f.Close()
		}
		return nil, err
	}
	return w, nil
}

// load discovers the sealed segments and opens the active one, rebuilding the offset
// indexes and the Unique-claim set. Sealed segments are sealed durably, so only the
// active segment's trailing record can be torn and is recovered there.
func (w *WAL) load() error {
	entries, err := os.ReadDir(w.dir)
	if err != nil {
		return fmt.Errorf("eventlog: read dir %q: %w", w.dir, err)
	}
	// A crash during archival can leave both seg-X.log and seg-X.log.gz (compressSegment
	// fsyncs the .gz before removing the plaintext). Keep them keyed by baseSeq so a
	// collision can be resolved rather than mistaken for a segment gap below.
	byBase := map[uint64]*segMeta{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m, ok := parseSegName(e.Name())
		if !ok {
			continue
		}
		m.path = filepath.Join(w.dir, e.Name())
		if prev, dup := byBase[m.baseSeq]; dup {
			keep, drop := reconcileDupSegment(prev, m)
			if err := os.Remove(drop.path); err != nil {
				return fmt.Errorf("eventlog: remove redundant segment %q: %w", drop.path, err)
			}
			slog.Warn("eventlog: reconciled a crash-during-archive duplicate", "kept", keep.path, "removed", drop.path)
			m = keep
		}
		byBase[m.baseSeq] = m
	}
	for _, m := range byBase {
		if err := w.indexSealed(m); err != nil {
			return err
		}
		w.sealed = append(w.sealed, m)
	}
	sort.Slice(w.sealed, func(i, j int) bool { return w.sealed[i].baseSeq < w.sealed[j].baseSeq })
	if err := w.validateSealedChain(); err != nil {
		return err
	}
	base := uint64(1)
	if n := len(w.sealed); n > 0 {
		base = w.sealed[n-1].lastSeq() + 1
	}
	w.activeBase = base
	return w.openActive()
}

// validateSealedChain checks the sealed segments form a gap-free 1..N prefix, so the
// authoritative-by-position seq contract holds across segment boundaries.
func (w *WAL) validateSealedChain() error {
	want := uint64(1)
	for _, s := range w.sealed {
		if s.baseSeq != want {
			return fmt.Errorf("eventlog: segment gap: %q starts at seq %d, expected %d", s.path, s.baseSeq, want)
		}
		want = s.lastSeq() + 1
	}
	return nil
}

// openActive opens (creating if absent) the active segment file and indexes it,
// recovering a torn final record. activeBase must be set.
func (w *WAL) openActive() error {
	path := filepath.Join(w.dir, activeFileName)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600) // #nosec G304 -- operator config dir, constant filename
	if err != nil {
		return fmt.Errorf("eventlog: open %q: %w", path, err)
	}
	w.f = f
	offsets, size, err := scanRecords(f, true, w.activeBase, w.claimed)
	if err != nil {
		return err
	}
	if err := f.Truncate(size); err != nil {
		return fmt.Errorf("eventlog: truncate active to last good record: %w", err)
	}
	if _, err := f.Seek(size, io.SeekStart); err != nil {
		return fmt.Errorf("eventlog: seek active to append position: %w", err)
	}
	w.offsets, w.size, w.w = offsets, size, bufio.NewWriter(f)
	return nil
}

// indexSealed builds a sealed segment's offset index (uncompressed) and rebuilds its
// Unique claims. A compressed segment keeps no offsets (it is read whole); its claims
// are NOT rescanned on open — an archived, long-past optimistic-concurrency/idempotency
// key guards state that no longer exists, and within-process double-claims are always
// caught, so skipping them keeps open O(hot) rather than decompressing the archive.
func (w *WAL) indexSealed(m *segMeta) error {
	if m.compressed {
		return nil
	}
	f, err := os.Open(m.path) // #nosec G304 -- path is a segment file in the operator data dir
	if err != nil {
		return fmt.Errorf("eventlog: open sealed %q: %w", m.path, err)
	}
	defer func() { _ = f.Close() }()
	offsets, size, err := scanRecords(f, false, m.baseSeq, w.claimed)
	if err != nil {
		return err
	}
	if uint64(len(offsets)) != m.count {
		return fmt.Errorf("eventlog: sealed %q holds %d records, name says %d", m.path, len(offsets), m.count)
	}
	m.offsets, m.size = offsets, size
	return nil
}

// scanRecords walks a segment file building the per-record byte-offset index and, when
// claims is non-nil, the Unique-claim set. recoverTorn drops a trailing record that has
// no newline (a crash mid-Append) — valid only for the active segment. It returns the
// offsets and the byte length of the complete records.
func scanRecords(f io.Reader, recoverTorn bool, baseSeq uint64, claims map[string]bool) ([]int64, int64, error) {
	r := bufio.NewReaderSize(f, 64*1024)
	var offsets []int64
	var pos int64
	for {
		line, err := r.ReadBytes('\n')
		complete := len(line) > 0 && line[len(line)-1] == '\n'
		if complete {
			if len(line) > 1 {
				offsets = append(offsets, pos)
				claimUnique(line, claims)
			}
			pos += int64(len(line))
			continue
		}
		if err == io.EOF {
			if len(line) > 0 {
				if !recoverTorn {
					return nil, 0, fmt.Errorf("eventlog: sealed segment has a torn final record (%d bytes)", len(line))
				}
				slog.Warn("eventlog: discarding torn final record (crash during append)",
					"bytes", len(line), "after_seq", baseSeq+uint64(len(offsets))-1)
			}
			return offsets, pos, nil
		}
		if err != nil {
			return nil, 0, fmt.Errorf("eventlog: read segment: %w", err)
		}
	}
}

func claimUnique(line []byte, claims map[string]bool) {
	if claims == nil {
		return
	}
	var rec struct {
		Unique string `json:"unique"`
	}
	if json.Unmarshal(line, &rec) == nil && rec.Unique != "" {
		claims[rec.Unique] = true
	}
}

// Append assigns the next Seq, persists durably to the active segment, then publishes.
// A full active segment is sealed (and old ones archived) afterward, off the record's
// durability path.
func (w *WAL) Append(_ context.Context, e Envelope) (Envelope, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return Envelope{}, ErrClosed
	}
	if w.failed != nil {
		return Envelope{}, w.failed
	}
	e, err := stampForAppend(e, w.claimed, w.activeBase+uint64(len(w.offsets)))
	if err != nil {
		return Envelope{}, err
	}
	b, err := json.Marshal(e)
	if err != nil {
		return Envelope{}, fmt.Errorf("eventlog: marshal: %w", err)
	}
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
	if w.size >= w.maxSegBytes {
		if err := w.rotate(); err != nil {
			// The event IS durable; sealing failed. Poison further appends so the
			// operator reopens rather than the log growing an oversized active segment
			// or a half-rotated state silently.
			w.failed = fmt.Errorf("eventlog: seal segment (reopen the log): %w", err)
		}
	}
	return e, nil
}

// rotate seals the active segment (rename to seg-<base>-<last>.log) and starts a fresh
// active segment, then archives old sealed segments. Called only after a durable append,
// with the lock held.
func (w *WAL) rotate() error {
	if len(w.offsets) == 0 {
		return nil
	}
	if err := w.f.Sync(); err != nil {
		return fmt.Errorf("rotate fsync: %w", err)
	}
	if err := w.f.Close(); err != nil {
		return fmt.Errorf("rotate close active: %w", err)
	}
	last := w.activeBase + uint64(len(w.offsets)) - 1
	sealedPath := filepath.Join(w.dir, sealName(w.activeBase, last, false))
	if err := os.Rename(filepath.Join(w.dir, activeFileName), sealedPath); err != nil {
		return fmt.Errorf("rotate rename: %w", err)
	}
	w.sealed = append(w.sealed, &segMeta{
		baseSeq: w.activeBase, count: uint64(len(w.offsets)), size: w.size, path: sealedPath, offsets: w.offsets,
	})
	slog.Info("eventlog: sealed segment", "base_seq", w.activeBase, "last_seq", last, "bytes", w.size)
	w.activeBase = last + 1
	w.offsets, w.size = nil, 0
	if err := w.openActive(); err != nil {
		return err
	}
	return w.archive()
}

// archive gzip-compresses the oldest uncompressed sealed segments while more than keepHot
// remain uncompressed, bounding on-disk size. Every event stays readable (a compressed
// segment is decompressed on Read).
func (w *WAL) archive() error {
	uncompressed := 0
	for _, s := range w.sealed {
		if !s.compressed {
			uncompressed++
		}
	}
	for _, s := range w.sealed {
		if uncompressed <= w.keepHot {
			break
		}
		if s.compressed {
			continue
		}
		if err := compressSegment(s); err != nil {
			return err
		}
		uncompressed--
		slog.Info("eventlog: archived segment", "base_seq", s.baseSeq, "last_seq", s.lastSeq())
	}
	return nil
}

// compressSegment gzips a sealed segment's file to <name>.gz, fsyncs it, then removes the
// plaintext and updates the meta. The .gz is written beside the original first, so a
// crash before the remove leaves the original intact (recovered on next open).
func compressSegment(s *segMeta) error {
	src, err := os.Open(s.path) // #nosec G304 -- segment file in the operator data dir
	if err != nil {
		return fmt.Errorf("eventlog: open segment to archive: %w", err)
	}
	defer func() { _ = src.Close() }()
	gzPath := s.path + ".gz"
	dst, err := os.OpenFile(gzPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600) // #nosec G304 -- derived from a segment path
	if err != nil {
		return fmt.Errorf("eventlog: create archive: %w", err)
	}
	zw := gzip.NewWriter(dst)
	if _, err := io.Copy(zw, src); err != nil {
		_ = zw.Close()
		_ = dst.Close()
		return fmt.Errorf("eventlog: gzip segment: %w", err)
	}
	if err := zw.Close(); err != nil {
		_ = dst.Close()
		return fmt.Errorf("eventlog: finish gzip: %w", err)
	}
	if err := dst.Sync(); err != nil {
		_ = dst.Close()
		return fmt.Errorf("eventlog: fsync archive: %w", err)
	}
	if err := dst.Close(); err != nil {
		return fmt.Errorf("eventlog: close archive: %w", err)
	}
	if err := os.Remove(s.path); err != nil {
		return fmt.Errorf("eventlog: remove plaintext segment: %w", err)
	}
	s.path, s.compressed, s.offsets = gzPath, true, nil
	return nil
}

// rollback restores the append invariant after a mid-Append failure to the active
// segment: truncate to the last acknowledged record, re-seek, discard buffered bytes.
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

// Read returns all events with Seq >= fromSeq, in order, spanning sealed (including
// compressed) and active segments. A corrupt record fails loudly.
func (w *WAL) Read(_ context.Context, fromSeq uint64) ([]Envelope, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil, ErrClosed
	}
	if fromSeq == 0 {
		fromSeq = 1
	}
	head := w.activeBase + uint64(len(w.offsets)) - 1
	if fromSeq > head {
		return nil, nil
	}
	out := make([]Envelope, 0, head-fromSeq+1)
	for _, s := range w.sealed {
		if s.lastSeq() < fromSeq {
			continue
		}
		evs, err := readSealed(s, maxU64(fromSeq, s.baseSeq))
		if err != nil {
			return nil, err
		}
		out = append(out, evs...)
	}
	if len(w.offsets) > 0 && fromSeq <= head {
		start := maxU64(fromSeq, w.activeBase)
		off := w.offsets[start-w.activeBase]
		buf := make([]byte, w.size-off)
		if nRead, err := w.f.ReadAt(buf, off); err != nil && (!errors.Is(err, io.EOF) || nRead != len(buf)) {
			return nil, fmt.Errorf("eventlog: read active from seq %d (%d/%d bytes): %w", start, nRead, len(buf), err)
		}
		evs, err := decodeRecords(buf, start)
		if err != nil {
			return nil, err
		}
		out = append(out, evs...)
	}
	return out, nil
}

// readSealed returns a sealed segment's events with seq >= fromSeq (fromSeq >= baseSeq).
func readSealed(s *segMeta, fromSeq uint64) ([]Envelope, error) {
	if s.compressed {
		f, err := os.Open(s.path) // #nosec G304 -- segment file in the operator data dir
		if err != nil {
			return nil, fmt.Errorf("eventlog: open archive %q: %w", s.path, err)
		}
		defer func() { _ = f.Close() }()
		zr, err := gzip.NewReader(f)
		if err != nil {
			return nil, fmt.Errorf("eventlog: open gzip %q: %w", s.path, err)
		}
		defer func() { _ = zr.Close() }()
		buf, err := io.ReadAll(zr)
		if err != nil {
			return nil, fmt.Errorf("eventlog: read archive %q: %w", s.path, err)
		}
		all, err := decodeRecords(buf, s.baseSeq)
		if err != nil {
			return nil, err
		}
		if fromSeq <= s.baseSeq {
			return all, nil
		}
		return all[fromSeq-s.baseSeq:], nil
	}
	f, err := os.Open(s.path) // #nosec G304 -- segment file in the operator data dir
	if err != nil {
		return nil, fmt.Errorf("eventlog: open sealed %q: %w", s.path, err)
	}
	defer func() { _ = f.Close() }()
	off := s.offsets[fromSeq-s.baseSeq]
	buf := make([]byte, s.size-off)
	if nRead, err := f.ReadAt(buf, off); err != nil && (!errors.Is(err, io.EOF) || nRead != len(buf)) {
		return nil, fmt.Errorf("eventlog: read sealed %q from seq %d (%d/%d): %w", s.path, fromSeq, nRead, len(buf), err)
	}
	return decodeRecords(buf, fromSeq)
}

// decodeRecords splits a newline-delimited buffer into envelopes, assigning seq from
// firstSeq by position (the position, not any stored Seq, is authoritative).
func decodeRecords(buf []byte, firstSeq uint64) ([]Envelope, error) {
	out := make([]Envelope, 0)
	seq := firstSeq
	for _, rec := range bytes.Split(buf, []byte{'\n'}) {
		if len(rec) == 0 {
			continue
		}
		var e Envelope
		if err := json.Unmarshal(rec, &e); err != nil {
			return nil, fmt.Errorf("eventlog: corrupt record at seq %d: %w", seq, err)
		}
		e.Seq = seq
		out = append(out, e)
		seq++
	}
	return out, nil
}

// ReadTenantStream reads then filters (the WAL has no per-tenant index).
func (w *WAL) ReadTenantStream(ctx context.Context, org, workspace, stream string, fromSeq uint64) ([]Envelope, error) {
	evs, err := w.Read(ctx, fromSeq)
	if err != nil {
		return nil, err
	}
	return filterTenantStream(evs, org, workspace, stream), nil
}

// Subscribe returns events appended after the call (in-process bus).
func (w *WAL) Subscribe() (<-chan Envelope, func()) { return w.bus.subscribe() }

// Head returns the highest assigned Seq (0 when empty).
func (w *WAL) Head() uint64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.activeBase + uint64(len(w.offsets)) - 1
}

// SegmentInfo reports the log's segment layout: total segments, how many are compressed,
// total on-disk bytes, and the head seq — for ops observability of compaction.
type SegmentInfo struct {
	Segments   int    `json:"segments"`
	Compressed int    `json:"compressed"`
	Bytes      int64  `json:"bytes"`
	Head       uint64 `json:"head"`
}

// Info returns the current segment layout.
func (w *WAL) Info() SegmentInfo {
	w.mu.Lock()
	defer w.mu.Unlock()
	info := SegmentInfo{Segments: len(w.sealed) + 1, Bytes: w.size, Head: w.activeBase + uint64(len(w.offsets)) - 1}
	for _, s := range w.sealed {
		info.Bytes += s.size
		if s.compressed {
			info.Compressed++
		}
	}
	return info
}

// Close flushes and closes the active segment and all subscriptions.
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

// sealName is a sealed segment's filename encoding its inclusive seq range (so both
// bounds are known without reading the file), with a .gz suffix when compressed.
func sealName(base, last uint64, compressed bool) string {
	name := fmt.Sprintf("seg-%020d-%020d.log", base, last)
	if compressed {
		name += ".gz"
	}
	return name
}

// reconcileDupSegment picks which of two segments covering the same seq range to keep
// when a crash mid-archive left both a plaintext and a .gz file. The plaintext is kept:
// it was sealed durably (fsync + rename) and is known-good, whereas the .gz may be a
// partial write from the interrupted compression. archive() will re-compress it later.
func reconcileDupSegment(a, b *segMeta) (keep, drop *segMeta) {
	if a.compressed {
		return b, a
	}
	return a, b
}

// parseSegName parses a sealed segment's base/last/compressed from its filename.
func parseSegName(name string) (*segMeta, bool) {
	rest, gz := strings.CutSuffix(name, ".gz")
	rest, ok := strings.CutSuffix(rest, ".log")
	if !ok {
		return nil, false
	}
	rest, ok = strings.CutPrefix(rest, "seg-")
	if !ok {
		return nil, false
	}
	base, last, ok := strings.Cut(rest, "-")
	if !ok {
		return nil, false
	}
	b, err1 := strconv.ParseUint(base, 10, 64)
	l, err2 := strconv.ParseUint(last, 10, 64)
	if err1 != nil || err2 != nil || b == 0 || l < b {
		return nil, false
	}
	return &segMeta{baseSeq: b, count: l - b + 1, compressed: gz}, true
}

func maxU64(a, b uint64) uint64 {
	if a > b {
		return a
	}
	return b
}

func newID() string {
	var b [16]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		panic("eventlog: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}
