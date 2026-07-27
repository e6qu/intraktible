// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !js

package eventlog

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

func runNATSClaimTest(t *testing.T) string {
	t.Helper()
	s, err := server.NewServer(&server.Options{
		Port:      -1,
		JetStream: true,
		StoreDir:  t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	go s.Start()
	if !s.ReadyForConnections(5 * time.Second) {
		t.Fatal("nats: server not ready")
	}
	t.Cleanup(s.Shutdown)
	return s.ClientURL()
}

// Envelope.Unique promises a claim across the whole retained log, not merely
// within JetStream's time-bounded Msg-Id cache. A deliberately short cache makes
// the distinction testable without turning this into a five-minute test.
func TestNATSUniqueClaimSurvivesDedupWindow(t *testing.T) {
	const window = 100 * time.Millisecond
	url := runNATSClaimTest(t)
	writer, err := openNATSLog(url, window)
	if err != nil {
		t.Fatal(err)
	}
	env := Envelope{
		Org: "demo", Workspace: "main", Actor: "author", Stream: "flows",
		Type: "flow.created", Time: time.Unix(1, 0).UTC(), Unique: "flow.slug\x00demo\x00main\x00alpha",
	}
	if _, err := writer.Append(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.js.GetLastMsg(natsStream, natsClaimSubject(env.Unique)); err != nil {
		t.Fatalf("claimed event was not stored on its permanent subject: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * window)

	reader, err := openNATSLog(url, window)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	if _, err := reader.Append(context.Background(), env); !errors.Is(err, ErrConflict) {
		t.Fatalf("re-claim after the dedup window returned %v, want ErrConflict", err)
	}
}

// A rolling upgrade starts from a stream where older binaries wrote every event
// on natsSubject. Opening the new binary must add the permanent-claim subject
// without losing history, and its compatibility scan must preserve an old claim
// even after the old Msg-Id cache has forgotten it.
func TestNATSUniqueClaimMigratesLegacyStream(t *testing.T) {
	const window = 100 * time.Millisecond
	url := runNATSClaimTest(t)
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	js, err := nc.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := js.AddStream(&nats.StreamConfig{
		Name:       natsStream,
		Subjects:   []string{natsSubject},
		Storage:    nats.FileStorage,
		Duplicates: window,
	}); err != nil {
		t.Fatal(err)
	}

	legacy := Envelope{
		Org: "demo", Workspace: "main", Actor: "old-node", Stream: "flows",
		Type: "flow.created", Time: time.Unix(1, 0).UTC(), Unique: "flow.slug\x00demo\x00main\x00alpha",
	}
	body, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := js.Publish(natsSubject, body, nats.MsgId(legacy.Unique)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * window)

	log, err := openNATSLog(url, window)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	info, err := js.StreamInfo(natsStream)
	if err != nil {
		t.Fatal(err)
	}
	if !containsNATSSubject(info.Config.Subjects, natsClaimSubjectFilter) {
		t.Fatalf("migrated subjects = %v, missing %q", info.Config.Subjects, natsClaimSubjectFilter)
	}
	if _, err := log.Append(context.Background(), legacy); !errors.Is(err, ErrConflict) {
		t.Fatalf("re-claiming a legacy key returned %v, want ErrConflict", err)
	}

	distinct := legacy
	distinct.Unique = "flow.slug\x00demo\x00main\x00beta"
	if _, err := log.Append(context.Background(), distinct); err != nil {
		t.Fatalf("a new claim after migration failed: %v", err)
	}
	events, err := log.Read(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Unique != legacy.Unique || events[1].Unique != distinct.Unique {
		t.Fatalf("history after migration = %+v, want legacy and new claimed events", events)
	}
}

func TestNATSLogUnboundsIntactExistingStream(t *testing.T) {
	url := runNATSClaimTest(t)
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	js, err := nc.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := js.AddStream(&nats.StreamConfig{
		Name:              natsStream,
		Subjects:          []string{natsSubject},
		Storage:           nats.FileStorage,
		Retention:         nats.LimitsPolicy,
		MaxConsumers:      1,
		MaxAge:            time.Hour,
		MaxMsgs:           10,
		MaxBytes:          1024,
		MaxMsgsPerSubject: 5,
		Discard:           nats.DiscardOld,
		Duplicates:        100 * time.Millisecond,
	}); err != nil {
		t.Fatal(err)
	}

	log, err := openNATSLog(url, 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	info, err := js.StreamInfo(natsStream)
	if err != nil {
		t.Fatal(err)
	}
	cfg := info.Config
	if cfg.MaxConsumers != -1 || cfg.MaxAge != 0 || cfg.MaxMsgs != -1 ||
		cfg.MaxBytes != -1 || cfg.MaxMsgsPerSubject != -1 ||
		cfg.Discard != nats.DiscardNew || !cfg.DenyDelete || !cfg.DenyPurge {
		t.Fatalf("reconciled stream remains bounded or mutable: %+v", cfg)
	}
	if !containsNATSSubject(cfg.Subjects, natsSubject) ||
		!containsNATSSubject(cfg.Subjects, natsClaimSubjectFilter) {
		t.Fatalf("reconciled subjects = %v, want base and permanent-claim subjects", cfg.Subjects)
	}
}

func TestNATSLogRefusesDiscardedHistory(t *testing.T) {
	url := runNATSClaimTest(t)
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	js, err := nc.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := js.AddStream(&nats.StreamConfig{
		Name:       natsStream,
		Subjects:   []string{natsSubject},
		Storage:    nats.FileStorage,
		MaxMsgs:    1,
		Duplicates: 100 * time.Millisecond,
	}); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 2; i++ {
		body, err := json.Marshal(Envelope{
			Org: "demo", Workspace: "main", Stream: "flows",
			Type: "event", Time: time.Unix(int64(i), 0).UTC(),
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := js.Publish(natsSubject, body); err != nil {
			t.Fatal(err)
		}
	}

	log, err := openNATSLog(url, 100*time.Millisecond)
	if log != nil {
		_ = log.Close()
		t.Fatal("opened a stream whose retained history has already lost its prefix")
	}
	if err == nil || !strings.Contains(err.Error(), "prefix has been discarded") {
		t.Fatalf("OpenNATSLog error = %v, want an explicit discarded-history refusal", err)
	}
}

func TestNATSLogRefusesMemoryStream(t *testing.T) {
	url := runNATSClaimTest(t)
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	js, err := nc.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := js.AddStream(&nats.StreamConfig{
		Name:       natsStream,
		Subjects:   []string{natsSubject},
		Storage:    nats.MemoryStorage,
		Duplicates: 100 * time.Millisecond,
	}); err != nil {
		t.Fatal(err)
	}

	log, err := openNATSLog(url, 100*time.Millisecond)
	if log != nil {
		_ = log.Close()
		t.Fatal("opened a memory-backed stream as a durable event log")
	}
	if err == nil || !strings.Contains(err.Error(), "want file storage") {
		t.Fatalf("OpenNATSLog error = %v, want an explicit non-durable-storage refusal", err)
	}
}
