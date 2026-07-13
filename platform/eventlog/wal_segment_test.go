// SPDX-License-Identifier: AGPL-3.0-or-later

package eventlog

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// appendN appends n tenant events of the given type prefix and returns the WAL's head.
func appendN(t *testing.T, w *WAL, n int, typePrefix string) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < n; i++ {
		e := Envelope{
			Org: "o", Workspace: "w", Type: fmt.Sprintf("%s%d", typePrefix, i),
			Payload: json.RawMessage(fmt.Sprintf(`{"n":%d,"pad":"xxxxxxxxxxxxxxxxxxxx"}`, i)),
		}
		if _, err := w.Append(ctx, e); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
}

// assertContiguous checks a Read result is a gap-free run of seqs from wantFirst, with the
// expected count and correctly-typed payloads (so a mis-sliced segment read is caught).
func assertContiguous(t *testing.T, evs []Envelope, wantFirst uint64, wantCount int) {
	t.Helper()
	if len(evs) != wantCount {
		t.Fatalf("read %d events, want %d", len(evs), wantCount)
	}
	for i, e := range evs {
		if e.Seq != wantFirst+uint64(i) {
			t.Fatalf("event %d has seq %d, want %d", i, e.Seq, wantFirst+uint64(i))
		}
		var body struct {
			N int `json:"n"`
		}
		if err := json.Unmarshal(e.Payload, &body); err != nil {
			t.Fatalf("event seq %d payload undecodable: %v", e.Seq, err)
		}
	}
}

// TestWALRotatesAndSeals verifies a small segment cap seals the active segment and starts
// fresh ones, that seq stays contiguous across the boundaries, and that a full Read
// reassembles every event in order.
func TestWALRotatesAndSeals(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	// A tiny cap and a large keepHot so nothing compresses — this isolates rotation.
	w, err := OpenWALSized(dir, 200, 100)
	if err != nil {
		t.Fatal(err)
	}
	appendN(t, w, 40, "t")

	info := w.Info()
	if info.Segments < 3 {
		t.Fatalf("40 events at a 200-byte cap should span several segments, got %d", info.Segments)
	}
	if info.Compressed != 0 {
		t.Fatalf("keepHot=100 should compress nothing, got %d compressed", info.Compressed)
	}
	if info.Head != 40 {
		t.Fatalf("head = %d, want 40", info.Head)
	}

	all, err := w.Read(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	assertContiguous(t, all, 1, 40)

	// A read starting mid-log must begin exactly at fromSeq, crossing segment seams.
	mid, err := w.Read(ctx, 15)
	if err != nil {
		t.Fatal(err)
	}
	assertContiguous(t, mid, 15, 26)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopen: the sealed chain must be rediscovered and appends continue contiguously.
	w2, err := OpenWALSized(dir, 200, 100)
	if err != nil {
		t.Fatalf("reopen across sealed segments: %v", err)
	}
	defer func() { _ = w2.Close() }()
	if w2.Head() != 40 {
		t.Fatalf("head after reopen = %d, want 40", w2.Head())
	}
	e, err := w2.Append(ctx, Envelope{Org: "o", Workspace: "w", Type: "after"})
	if err != nil {
		t.Fatal(err)
	}
	if e.Seq != 41 {
		t.Fatalf("seq after reopen = %d, want 41", e.Seq)
	}
}

// TestWALArchivesAndReadsBack forces compression by keeping only a couple of hot segments,
// then checks that some segments really are gzip files on disk AND that a full Read still
// returns every event in order — the compaction must never lose or reorder history.
func TestWALArchivesAndReadsBack(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	w, err := OpenWALSized(dir, 200, 2)
	if err != nil {
		t.Fatal(err)
	}
	appendN(t, w, 60, "t")

	info := w.Info()
	if info.Compressed == 0 {
		t.Fatalf("keepHot=2 with many segments should compress the older ones, got 0")
	}

	// Prove the archive is really gzip on disk (not just a renamed plaintext).
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	gz := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".gz") {
			gz++
			b, err := os.ReadFile(filepath.Join(dir, e.Name())) // #nosec G304 -- test-owned temp dir
			if err != nil {
				t.Fatal(err)
			}
			if len(b) < 2 || b[0] != 0x1f || b[1] != 0x8b {
				t.Fatalf("%s is not a gzip stream (magic %x)", e.Name(), b[:min(2, len(b))])
			}
		}
	}
	if gz != info.Compressed {
		t.Fatalf("%d .gz files on disk but Info reports %d compressed", gz, info.Compressed)
	}

	all, err := w.Read(ctx, 0)
	if err != nil {
		t.Fatalf("read spanning compressed + hot + active segments: %v", err)
	}
	assertContiguous(t, all, 1, 60)

	// A read that starts inside a compressed segment must slice it correctly.
	from5, err := w.Read(ctx, 5)
	if err != nil {
		t.Fatal(err)
	}
	assertContiguous(t, from5, 5, 56)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopen: compressed segments must be rediscovered, the chain re-validated, and the
	// full history still readable without decompressing on open.
	w2, err := OpenWALSized(dir, 200, 2)
	if err != nil {
		t.Fatalf("reopen with compressed segments present: %v", err)
	}
	defer func() { _ = w2.Close() }()
	if w2.Head() != 60 {
		t.Fatalf("head after reopen = %d, want 60", w2.Head())
	}
	reread, err := w2.Read(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	assertContiguous(t, reread, 1, 60)
}

// TestWALMigratesLegacySingleFile confirms an existing un-segmented events.log from an
// earlier version opens as the active segment at base 1 (no migration), and that once it
// exceeds the cap it rotates normally while keeping seq 1 stable.
func TestWALMigratesLegacySingleFile(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	// Write a legacy single-file log directly (as the old WAL would have left it).
	var legacy strings.Builder
	for i := 1; i <= 5; i++ {
		legacy.WriteString(fmt.Sprintf(`{"org":"o","workspace":"w","type":"legacy%d"}`+"\n", i))
	}
	if err := os.WriteFile(filepath.Join(dir, activeFileName), []byte(legacy.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	w, err := OpenWALSized(dir, 200, 2)
	if err != nil {
		t.Fatalf("opening a legacy single-file log should just work: %v", err)
	}
	if w.Head() != 5 {
		t.Fatalf("head over legacy file = %d, want 5", w.Head())
	}
	first, err := w.Read(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 5 || first[0].Seq != 1 || first[0].Type != "legacy1" {
		t.Fatalf("legacy read: %+v", first)
	}

	// Append past the cap: it must rotate, sealing the legacy content as seg base 1.
	appendN(t, w, 30, "new")
	if w.Info().Segments < 2 {
		t.Fatalf("appending past the cap should rotate, still 1 segment")
	}
	all, err := w.Read(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if all[0].Seq != 1 || all[0].Type != "legacy1" {
		t.Fatalf("seq 1 must stay the legacy record after rotation, got %+v", all[0])
	}
	if uint64(len(all)) != w.Head() {
		t.Fatalf("read %d events, head %d", len(all), w.Head())
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestWALUniqueClaimAcrossSealedSegments guards optimistic concurrency across the segment
// boundary: a Unique key written into a now-sealed (uncompressed) segment must still be
// rejected after a reopen that re-indexes the sealed segments.
func TestWALUniqueClaimAcrossSealedSegments(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	w, err := OpenWALSized(dir, 200, 100)
	if err != nil {
		t.Fatal(err)
	}
	// Claim a key, then push enough events to seal the segment holding it.
	if _, err := w.Append(ctx, Envelope{Org: "o", Workspace: "w", Type: "claim", Unique: "k-sealed"}); err != nil {
		t.Fatal(err)
	}
	appendN(t, w, 30, "t")
	if w.Info().Segments < 2 {
		t.Fatal("expected the claim's segment to have been sealed")
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	w2, err := OpenWALSized(dir, 200, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w2.Close() }()
	if _, err := w2.Append(ctx, Envelope{Org: "o", Workspace: "w", Type: "claim", Unique: "k-sealed"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("a key in a sealed segment must still conflict after reopen, got %v", err)
	}
}

// TestWALRejectsSegmentGap confirms the load-time chain check fails loudly rather than
// silently serving a log with a hole in it (a sealed segment removed out from under it).
func TestWALRejectsSegmentGap(t *testing.T) {
	dir := t.TempDir()
	w, err := OpenWALSized(dir, 200, 100)
	if err != nil {
		t.Fatal(err)
	}
	appendN(t, w, 40, "t")
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	// Delete the earliest sealed segment, punching a hole at the front of the chain.
	entries, _ := os.ReadDir(dir)
	var sealedNames []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "seg-") {
			sealedNames = append(sealedNames, e.Name())
		}
	}
	if len(sealedNames) < 2 {
		t.Fatalf("need multiple sealed segments to test a gap, got %d", len(sealedNames))
	}
	if err := os.Remove(filepath.Join(dir, sealedNames[0])); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenWALSized(dir, 200, 100); err == nil {
		t.Fatal("opening a log with a missing sealed segment must fail loudly")
	}
}

// TestWALReconcilesCrashDuringArchive simulates a crash between writing a segment's .gz
// and removing its plaintext (compressSegment fsyncs the .gz first): both files exist for
// the same seq range. Reopening must NOT mistake this for a segment gap — it keeps the
// durable plaintext, removes the redundant .gz, and still reads the full history.
func TestWALReconcilesCrashDuringArchive(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	w, err := OpenWALSized(dir, 200, 2)
	if err != nil {
		t.Fatal(err)
	}
	appendN(t, w, 60, "t")
	if w.Info().Compressed == 0 {
		t.Fatal("need a compressed segment for this test")
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	// Find a .gz segment and recreate its plaintext beside it, as a crash-before-remove
	// would leave: decompress the archive back to seg-<base>-<last>.log.
	entries, _ := os.ReadDir(dir)
	var gzName string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".gz") {
			gzName = e.Name()
			break
		}
	}
	if gzName == "" {
		t.Fatal("expected a .gz segment on disk")
	}
	m, ok := parseSegName(gzName)
	if !ok {
		t.Fatalf("could not parse %q", gzName)
	}
	gzf, err := os.Open(filepath.Join(dir, gzName)) // #nosec G304 -- test temp dir
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(gzf)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	_ = zr.Close()
	_ = gzf.Close()
	plainPath := filepath.Join(dir, sealName(m.baseSeq, m.lastSeq(), false))
	if err := os.WriteFile(plainPath, plain, 0o600); err != nil {
		t.Fatal(err)
	}

	// Reopen: both files exist for the same range; it must reconcile, not report a gap.
	w2, err := OpenWALSized(dir, 200, 2)
	if err != nil {
		t.Fatalf("reopen with a duplicate plaintext+gz segment should reconcile, got: %v", err)
	}
	defer func() { _ = w2.Close() }()
	if _, err := os.Stat(filepath.Join(dir, gzName)); !os.IsNotExist(err) {
		t.Fatalf("the redundant .gz should have been removed, stat err = %v", err)
	}
	evs, err := w2.Read(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	assertContiguous(t, evs, 1, 60)
}

// TestWALTenantStreamAcrossSegments confirms tenant filtering spans sealed (incl.
// compressed) and active segments — the filter runs over the reassembled Read.
func TestWALTenantStreamAcrossSegments(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	w, err := OpenWALSized(dir, 200, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	// Interleave two orgs so a correct filter must reach into every segment.
	for i := 0; i < 30; i++ {
		org := "a"
		if i%2 == 1 {
			org = "b"
		}
		if _, err := w.Append(ctx, Envelope{Org: org, Workspace: "w", Type: "t", Stream: "s"}); err != nil {
			t.Fatal(err)
		}
	}
	if w.Info().Compressed == 0 {
		t.Fatal("expected some segments compressed for this test to be meaningful")
	}
	got, err := w.ReadTenantStream(ctx, "a", "w", "s", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 15 {
		t.Fatalf("tenant a should have 15 of 30 interleaved events, got %d", len(got))
	}
	for _, e := range got {
		if e.Org != "a" {
			t.Fatalf("tenant filter leaked org %q", e.Org)
		}
	}
}
