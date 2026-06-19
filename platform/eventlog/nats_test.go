// SPDX-License-Identifier: AGPL-3.0-or-later

package eventlog_test

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"

	"github.com/e6qu/intraktible/platform/eventlog"
)

// runNATS starts an in-process NATS server with JetStream — the embedded
// stand-in for the broker, so the networked log is tested without external
// infrastructure.
func runNATS(t *testing.T) string {
	t.Helper()
	s, err := server.NewServer(&server.Options{Port: -1, JetStream: true, StoreDir: t.TempDir()})
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

func TestNATSLog(t *testing.T) {
	url := runNATS(t)
	ctx := context.Background()

	// Two logs over one server stand in for two HA nodes.
	node1, err := eventlog.OpenNATSLog(url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = node1.Close() }()
	node2, err := eventlog.OpenNATSLog(url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = node2.Close() }()

	// node1 subscribes before any append; the push consumer should deliver every
	// event, including those appended by node2 — the networked-delivery guarantee.
	ch, cancel := node1.Subscribe()
	defer cancel()

	env := func(seq int) eventlog.Envelope {
		return eventlog.Envelope{Org: "o", Workspace: "w", Actor: "a", Stream: "s", Type: "evt", Time: time.Unix(int64(seq), 0).UTC()}
	}
	// Appends from different nodes share one total order (JetStream sequence).
	first, err := node1.Append(ctx, env(1))
	if err != nil || first.Seq != 1 {
		t.Fatalf("node1 append -> seq=%d err=%v", first.Seq, err)
	}
	second, err := node2.Append(ctx, env(2))
	if err != nil || second.Seq != 2 {
		t.Fatalf("node2 append -> seq=%d err=%v", second.Seq, err)
	}

	// Read on either node is consistent and ordered.
	got, err := node2.Read(ctx, 0)
	if err != nil || len(got) != 2 || got[0].Seq != 1 || got[1].Seq != 2 {
		t.Fatalf("node2 Read = %+v err=%v", got, err)
	}
	if h := node1.Head(); h != 2 {
		t.Fatalf("node1 Head = %d, want 2", h)
	}

	// Both appends reach node1's subscriber over the push consumer.
	deadline := time.After(5 * time.Second)
	var seqs []uint64
	for len(seqs) < 2 {
		select {
		case e := <-ch:
			seqs = append(seqs, e.Seq)
		case <-deadline:
			t.Fatalf("node1 received %v over the bus, want 2 events", seqs)
		}
	}
	if seqs[0] != 1 || seqs[1] != 2 {
		t.Fatalf("delivered seqs = %v, want ordered 1,2", seqs)
	}

	_ = node1.Close()
	if _, err := node1.Append(ctx, env(3)); err == nil {
		t.Fatal("append after close should fail")
	}
}
