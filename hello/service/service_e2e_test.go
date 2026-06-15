// SPDX-License-Identifier: AGPL-3.0-or-later

package service_test

import (
	"net/http"
	"testing"

	"github.com/e6qu/intraktible/hello/command"
	"github.com/e6qu/intraktible/hello/service"
	"github.com/e6qu/intraktible/hello/stats"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
	"github.com/e6qu/intraktible/platform/testutil"
)

func TestHelloAPIEndToEnd(t *testing.T) {
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	st := store.NewMemory()
	svc := service.New(command.NewHandler(log), st)
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}

	api := testutil.StartAPI(t, log, st, "test-key", id, svc.Routes, stats.Projector{})

	// POST a greeting over HTTP.
	var posted struct {
		Seq uint64 `json:"seq"`
	}
	api.Request(t, http.MethodPost, "/v1/hello", map[string]string{"name": "ada"}, http.StatusAccepted, &posted)
	if posted.Seq == 0 {
		t.Fatal("expected a sequence number for the appended event")
	}

	// The read model updates asynchronously via the bus; poll the HTTP stats.
	if !testutil.Eventually(t, func() bool {
		var s struct {
			Count    int    `json:"count"`
			LastName string `json:"last_name"`
		}
		api.Request(t, http.MethodGet, "/v1/hello/stats", nil, http.StatusOK, &s)
		return s.Count == 1 && s.LastName == "ada"
	}) {
		t.Fatal("stats endpoint never reflected the greeting")
	}
}

func TestHelloAPIRequiresAuth(t *testing.T) {
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	st := store.NewMemory()
	svc := service.New(command.NewHandler(log), st)
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	api := testutil.StartAPI(t, log, st, "test-key", id, svc.Routes, stats.Projector{})

	// No X-Api-Key header -> 401.
	resp, err := http.Get(api.Server.URL + "/v1/hello/stats")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated request -> %d, want 401", resp.StatusCode)
	}
}
