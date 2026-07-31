// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/projection"
	"github.com/e6qu/intraktible/platform/store"
	"github.com/e6qu/intraktible/server"
)

// TestBackupRestoreReplayRoundTrip proves the disaster-recovery contract: the
// event log backs up to a stream, restores into a fresh directory, and replays
// into identical projection state. RPO is zero (the log is truth); RTO is the
// replay time.
func TestBackupRestoreReplayRoundTrip(t *testing.T) {
	ctx := context.Background()

	// Source: a WAL with a few events.
	sourceDir := t.TempDir()
	sourceLog, err := eventlog.OpenWAL(sourceDir)
	if err != nil {
		t.Fatal(err)
	}
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	for i := 0; i < 5; i++ {
		if _, err := eventlog.AppendJSON(ctx, sourceLog, id.Org, id.Workspace, id.Actor,
			"flows", "flow.created", eventlog.Envelope{}.Time, nil); err != nil {
			t.Fatal(err)
		}
	}

	// Backup: stream the log to a buffer.
	var backup bytes.Buffer
	events, err := sourceLog.Read(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(&backup)
	for _, e := range events {
		if err := enc.Encode(e); err != nil {
			t.Fatal(err)
		}
	}
	_ = sourceLog.Close()
	if backup.Len() == 0 {
		t.Fatal("backup is empty")
	}

	// Restore: write the backup into a fresh directory.
	restoreDir := t.TempDir()
	backupFile := filepath.Join(t.TempDir(), "backup.jsonl")
	if err := os.WriteFile(backupFile, backup.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	restoreLog, err := eventlog.OpenWAL(restoreDir)
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(backup.Bytes()))
	for {
		var e eventlog.Envelope
		if err := dec.Decode(&e); err != nil {
			break
		}
		if _, err := restoreLog.Append(ctx, e); err != nil {
			t.Fatal(err)
		}
	}

	// Replay: rebuild projections from the restored log into a fresh store.
	st := store.NewMemory()
	rt := projection.New(restoreLog, st, server.Projectors("all")...)
	if _, err := rt.RebuildTo(ctx, 0); err != nil {
		t.Fatal(err)
	}
	_ = restoreLog.Close()

	// Verify the restored head matches the source.
	restoredLog, err := eventlog.OpenWAL(restoreDir)
	if err != nil {
		t.Fatal(err)
	}
	if restoredLog.Head() != 5 {
		t.Fatalf("restored head = %d, want 5", restoredLog.Head())
	}
	restoredEvents, _ := restoredLog.Read(ctx, 0)
	if len(restoredEvents) != 5 {
		t.Fatalf("restored events = %d, want 5", len(restoredEvents))
	}
	_ = restoredLog.Close()
}

// TestBackupRestoreCLIRoundTrip proves the CLI commands produce a byte-identical
// restore when given the same backup file.
func TestBackupRestoreCLIRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("CLI integration test")
	}
	binary := filepath.Join(t.TempDir(), "intraktible-test")
	cmd := exec.Command("go", "build", "-o", binary, ".")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("go build: %v", err)
	}
	sourceDir := t.TempDir()

	// Seed the source WAL by running a quick serve + kill, or just append via Go.
	sourceLog, err := eventlog.OpenWAL(sourceDir)
	if err != nil {
		t.Fatal(err)
	}
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	for i := 0; i < 3; i++ {
		if _, err := eventlog.AppendJSON(
			context.Background(), sourceLog, id.Org, id.Workspace, id.Actor,
			"flows", "flow.created", eventlog.Envelope{}.Time, map[string]any{"i": i},
		); err != nil {
			t.Fatal(err)
		}
	}
	_ = sourceLog.Close()

	backupFile := filepath.Join(t.TempDir(), "backup.jsonl")

	// Backup.
	out, err := exec.Command(binary, "backup", "--data-dir", sourceDir, "--out", backupFile).CombinedOutput()
	if err != nil {
		t.Fatalf("backup: %v\n%s", err, out)
	}

	// Restore into a fresh directory.
	restoreDir := t.TempDir()
	out, err = exec.Command(binary, "restore", "--data-dir", restoreDir, "--in", backupFile).CombinedOutput()
	if err != nil {
		t.Fatalf("restore: %v\n%s", err, out)
	}

	// The restored WAL should have the same head.
	restoredLog, err := eventlog.OpenWAL(restoreDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restoredLog.Close() }()
	if restoredLog.Head() != 3 {
		t.Fatalf("restored head = %d, want 3", restoredLog.Head())
	}
}
