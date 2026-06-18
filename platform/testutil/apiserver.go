// SPDX-License-Identifier: AGPL-3.0-or-later

package testutil

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/e6qu/intraktible/platform/auth"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/projection"
	"github.com/e6qu/intraktible/platform/store"
)

// API is a running, in-process copy of the full HTTP stack (auth middleware +
// module routes + live projections), backing the API's HTTP-based e2e tests.
type API struct {
	Server   *httptest.Server
	Key      string
	Identity identity.Identity

	// log + rt let a request wait for the read model to reflect every event
	// appended so far, so HTTP-level read-after-write is deterministic in tests
	// (production reads are eventually consistent — see settle).
	log eventlog.Log
	rt  *projection.Runtime
}

// NewLogStore opens a per-test WAL (closed on cleanup) and a fresh in-memory
// store — the common backing pair for a module's e2e harness.
func NewLogStore(t *testing.T) (eventlog.Log, store.Store) {
	t.Helper()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatalf("testutil: open WAL: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	return log, store.NewMemory()
}

// StartAPI assembles the same handler chain as cmd/intraktible (auth-gated /v1,
// recover/request-id/logger middleware) over a real httptest server, with the
// given module routes and projections wired to log/st. It seeds an API key
// resolving to id and tears everything down on test cleanup.
func StartAPI(t *testing.T, log eventlog.Log, st store.Store, key string, id identity.Identity, routes func(*http.ServeMux), projectors ...projection.Projector) *API {
	t.Helper()

	keyring := auth.NewKeyring()
	keyring.Add(key, auth.APIKey{ID: "test", Identity: id, Scope: auth.Sandbox, Role: auth.RoleAdmin})
	sessions := auth.NewSessions()

	api := http.NewServeMux()
	routes(api)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	rt := projection.New(log, st, projectors...)
	if err := rt.Start(ctx); err != nil {
		t.Fatalf("testutil: projection start: %v", err)
	}

	root := http.NewServeMux()
	root.Handle("/v1/", httpx.Chain(api, httpx.Authenticate(keyring, sessions), httpx.Authorize))
	handler := httpx.Chain(root, httpx.Recover, httpx.RequestID, httpx.Logger)

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &API{Server: srv, Key: key, Identity: id, log: log, rt: rt}
}

// settle blocks until the projection has applied every event appended so far, so
// a subsequent read sees prior writes. The live projection consumes the bus in a
// goroutine, so without this a read issued right after a write can race it (flaky
// under -race on a loaded runner). Bounded so a genuinely stuck projection still
// fails the test rather than hanging.
func (a *API) settle() {
	if a.rt == nil || a.log == nil {
		return
	}
	deadline := time.Now().Add(5 * time.Second)
	for a.rt.Applied() < a.log.Head() {
		if time.Now().After(deadline) {
			return
		}
		time.Sleep(time.Millisecond)
	}
}

// Request issues an authenticated request to the test server, asserts the
// response status, and decodes the JSON body into out (pass nil to skip
// decoding). A non-nil body is JSON encoded. The response body is always closed.
func (a *API) Request(t *testing.T, method, path string, body any, wantStatus int, out any) {
	t.Helper()
	a.settle() // read-after-write: the read model reflects every prior write
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("testutil: marshal request body: %v", err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, a.Server.URL+path, rdr)
	if err != nil {
		t.Fatalf("testutil: build request: %v", err)
	}
	req.Header.Set("X-Api-Key", a.Key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("testutil: %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != wantStatus {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("%s %s -> %d, want %d (body: %s)", method, path, resp.StatusCode, wantStatus, b)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("testutil: decode response: %v", err)
		}
	}
}

// RequestStatus issues an authenticated request and returns the status code
// without asserting it — for polling an eventually-consistent read (e.g. a
// decision record that the projection has not applied yet, which 404s until it
// catches up). On a 2xx with a non-nil out it best-effort decodes the body.
// Transport/build errors still fail the test.
func (a *API) RequestStatus(t *testing.T, method, path string, body, out any) int {
	t.Helper()
	a.settle()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("testutil: marshal request body: %v", err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, a.Server.URL+path, rdr)
	if err != nil {
		t.Fatalf("testutil: build request: %v", err)
	}
	req.Header.Set("X-Api-Key", a.Key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("testutil: %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if out != nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("testutil: decode response: %v", err)
		}
	}
	return resp.StatusCode
}
