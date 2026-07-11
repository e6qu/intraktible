// SPDX-License-Identifier: AGPL-3.0-or-later

package audit_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/intraktible/platform/audit"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/testutil"
)

func TestAuditEndpointReturnsTenantTrail(t *testing.T) {
	log, st := testutil.NewLogStore(t)
	id := identity.Identity{Org: "o", Workspace: "w", Actor: "alice"}

	// Seed before StartAPI so the boot projection indexes these events.
	at := time.Date(2026, 6, 16, 9, 0, 0, 0, time.UTC)
	if _, err := log.Append(context.Background(), eventlog.Envelope{
		Org: "o", Workspace: "w", Actor: "alice", Stream: "flows",
		Type: "flow.created", Time: at, Payload: []byte(`{"flow_id":"f1"}`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := log.Append(context.Background(), eventlog.Envelope{
		Org: "other", Workspace: "w", Actor: "mallory", Stream: "flows",
		Type: "flow.created", Time: at, Payload: []byte(`{"flow_id":"x"}`),
	}); err != nil {
		t.Fatal(err)
	}

	api := testutil.StartAPI(t, log, st, "admin-key", id, audit.New(st).Routes, audit.Projector{})

	var out struct {
		Entries []audit.Entry `json:"entries"`
	}
	if !testutil.Eventually(t, func() bool {
		out.Entries = nil
		api.Request(t, http.MethodGet, "/v1/audit", nil, http.StatusOK, &out)
		return len(out.Entries) == 1
	}) {
		t.Fatalf("want 1 tenant entry, got %d: %+v", len(out.Entries), out.Entries)
	}
	if out.Entries[0].Type != "flow.created" || out.Entries[0].Actor != "alice" {
		t.Fatalf("unexpected entry: %+v", out.Entries[0])
	}

	// A bad time bound is a loud 400.
	api.Request(t, http.MethodGet, "/v1/audit?since=not-a-time", nil, http.StatusBadRequest, nil)
}

func TestAuditEndpointCSVExport(t *testing.T) {
	log, st := testutil.NewLogStore(t)
	id := identity.Identity{Org: "o", Workspace: "w", Actor: "alice"}

	if _, err := log.Append(context.Background(), eventlog.Envelope{
		Org: "o", Workspace: "w", Actor: "alice", Stream: "cases",
		Type: "case.opened", Time: time.Now().UTC(), Payload: []byte(`{"case_id":"c1"}`),
	}); err != nil {
		t.Fatal(err)
	}

	api := testutil.StartAPI(t, log, st, "admin-key", id, audit.New(st).Routes, audit.Projector{})

	// Wait for the boot projection to index the seeded event before exporting.
	if !testutil.Eventually(t, func() bool {
		var out struct {
			Entries []audit.Entry `json:"entries"`
		}
		api.Request(t, http.MethodGet, "/v1/audit", nil, http.StatusOK, &out)
		return len(out.Entries) == 1
	}) {
		t.Fatal("audit entry never indexed")
	}

	req, _ := http.NewRequest(http.MethodGet, api.Server.URL+"/v1/audit?format=csv", http.NoBody)
	req.Header.Set("X-Api-Key", api.Key)
	resp, err := api.Server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("csv export -> %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Fatalf("content-type = %q, want text/csv", ct)
	}
	raw, _ := io.ReadAll(resp.Body)
	body := string(raw)
	if !strings.HasPrefix(body, "seq,time,actor,stream,type,payload") || !strings.Contains(body, "case.opened") {
		t.Fatalf("unexpected csv export:\n%s", body)
	}
}
