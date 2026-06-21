// SPDX-License-Identifier: AGPL-3.0-or-later

package eventlog_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/secretbox"
)

func encTestKeyring(t *testing.T) *secretbox.Keyring {
	t.Helper()
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i + 1)
	}
	kr, err := secretbox.NewKeyring(k)
	if err != nil {
		t.Fatal(err)
	}
	return kr
}

// Event payloads are sealed on disk while metadata (org/workspace/stream/type) stays
// plaintext for filtering/ordering; reads return the plaintext payload.
func TestEncryptedLogSealsPayload(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	inner, err := eventlog.OpenWAL(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = inner.Close() }()
	log := eventlog.Encrypted(inner, encTestKeyring(t))

	stored, err := log.Append(ctx, eventlog.Envelope{
		Org: "demo", Workspace: "main", Stream: "decision.runs", Type: "decided",
		Payload: json.RawMessage(`{"ssn":"123-45-6789"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	// Append returns the plaintext payload (with the assigned Seq) to the caller.
	if string(stored.Payload) != `{"ssn":"123-45-6789"}` || stored.Seq == 0 {
		t.Fatalf("append returned %+v", stored)
	}

	// On disk: the secret must not appear in the clear; metadata must.
	raw, err := os.ReadFile(filepath.Join(dir, "events.log"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "123-45-6789") {
		t.Fatal("payload secret stored in the clear on disk")
	}
	if !strings.Contains(string(raw), "decision.runs") || !strings.Contains(string(raw), `"type":"decided"`) {
		t.Fatal("metadata should stay plaintext for filtering/ordering")
	}

	// Read returns the plaintext payload and intact metadata.
	evs, err := log.Read(ctx, 0)
	if err != nil || len(evs) != 1 {
		t.Fatalf("read = %d, %v", len(evs), err)
	}
	if string(evs[0].Payload) != `{"ssn":"123-45-6789"}` {
		t.Fatalf("read payload = %s", evs[0].Payload)
	}
	if evs[0].Stream != "decision.runs" || evs[0].Type != "decided" {
		t.Fatalf("metadata lost: %+v", evs[0])
	}
}

// Subscribe delivers decrypted payloads to the in-process bus.
func TestEncryptedLogSubscribe(t *testing.T) {
	ctx := context.Background()
	inner, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = inner.Close() }()
	log := eventlog.Encrypted(inner, encTestKeyring(t))

	ch, cancel := log.Subscribe()
	defer cancel()
	if _, err := log.Append(ctx, eventlog.Envelope{Org: "o", Workspace: "w", Type: "t", Payload: json.RawMessage(`{"x":1}`)}); err != nil {
		t.Fatal(err)
	}
	e := <-ch
	if string(e.Payload) != `{"x":1}` {
		t.Fatalf("subscribed payload = %s (want decrypted)", e.Payload)
	}
}

// A plaintext-payload event written before encryption was enabled still reads
// through the wrapper (transparent migration).
func TestEncryptedLogPlaintextPassthrough(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	inner, err := eventlog.OpenWAL(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Append plaintext via the bare inner log (pre-encryption history).
	if _, err := inner.Append(ctx, eventlog.Envelope{Org: "o", Workspace: "w", Type: "legacy", Payload: json.RawMessage(`{"old":true}`)}); err != nil {
		t.Fatal(err)
	}
	log := eventlog.Encrypted(inner, encTestKeyring(t))
	// And a sealed one through the wrapper.
	if _, err := log.Append(ctx, eventlog.Envelope{Org: "o", Workspace: "w", Type: "new", Payload: json.RawMessage(`{"new":true}`)}); err != nil {
		t.Fatal(err)
	}
	evs, err := log.Read(ctx, 0)
	if err != nil || len(evs) != 2 {
		t.Fatalf("read = %d, %v", len(evs), err)
	}
	if string(evs[0].Payload) != `{"old":true}` || string(evs[1].Payload) != `{"new":true}` {
		t.Fatalf("mixed read = %s | %s", evs[0].Payload, evs[1].Payload)
	}
}

func TestEncryptedLogNilKeyringPassthrough(t *testing.T) {
	inner, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = inner.Close() }()
	if eventlog.Encrypted(inner, nil) != eventlog.Log(inner) {
		t.Fatal("nil keyring should return the inner log unchanged")
	}
}
