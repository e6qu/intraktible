// SPDX-License-Identifier: AGPL-3.0-or-later

package httpx_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/e6qu/intraktible/platform/auth"
	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/testutil"
)

func TestManagedAPIKeysCreateUseRevoke(t *testing.T) {
	log, st := testutil.NewLogStore(t)
	keys := auth.NewStoreAPIKeys(st)
	keyring := auth.NewKeyring()
	keyring.UseResolver(keys)
	sessions := auth.NewSessions()
	adminID := identity.Identity{Org: "demo", Workspace: "main", Actor: "admin"}
	keyring.Add("admin-key", auth.APIKey{ID: "admin", Identity: adminID, Scope: auth.ScopeAll, Role: auth.RoleAdmin})

	api := http.NewServeMux()
	httpx.NewAPIKeysHandler(keys, log).Routes(api)
	api.HandleFunc("GET /v1/me", httpx.MeHandler())
	handler := httpx.Chain(api, httpx.Authenticate(keyring, sessions), httpx.Authorize)

	create := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/api-keys",
		strings.NewReader(`{"name":"etl","actor":"etl-worker","role":"operator","scope":"production"}`))
	req.Header.Set("X-Api-Key", "admin-key")
	handler.ServeHTTP(create, req)
	if create.Code != http.StatusCreated {
		t.Fatalf("create api key -> %d body=%s", create.Code, create.Body.String())
	}
	var created struct {
		APIKey auth.ManagedAPIKey `json:"api_key"`
		Secret string             `json:"secret"`
	}
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Secret == "" || created.APIKey.Hash != "" || created.APIKey.Identity.Actor != "etl-worker" {
		t.Fatalf("bad create response: %+v", created)
	}

	me := httptest.NewRecorder()
	meReq := httptest.NewRequest(http.MethodGet, "/v1/me", http.NoBody)
	meReq.Header.Set("X-Api-Key", created.Secret)
	handler.ServeHTTP(me, meReq)
	if me.Code != http.StatusOK || !strings.Contains(me.Body.String(), `"actor":"etl-worker"`) {
		t.Fatalf("managed key auth -> %d body=%s", me.Code, me.Body.String())
	}

	list := httptest.NewRecorder()
	listReq := httptest.NewRequest(http.MethodGet, "/v1/api-keys", http.NoBody)
	listReq.Header.Set("X-Api-Key", "admin-key")
	handler.ServeHTTP(list, listReq)
	if list.Code != http.StatusOK || strings.Contains(list.Body.String(), "hash") {
		t.Fatalf("list should redact hashes -> %d body=%s", list.Code, list.Body.String())
	}

	rotate := httptest.NewRecorder()
	rotReq := httptest.NewRequest(http.MethodPost, "/v1/api-keys/"+created.APIKey.ID+"/rotate",
		strings.NewReader(`{"grace_seconds":0}`))
	rotReq.Header.Set("X-Api-Key", "admin-key")
	handler.ServeHTTP(rotate, rotReq)
	if rotate.Code != http.StatusOK {
		t.Fatalf("rotate api key -> %d body=%s", rotate.Code, rotate.Body.String())
	}
	var rotated struct {
		APIKey auth.ManagedAPIKey `json:"api_key"`
		Secret string             `json:"secret"`
	}
	if err := json.Unmarshal(rotate.Body.Bytes(), &rotated); err != nil {
		t.Fatal(err)
	}
	if rotated.Secret == "" || rotated.Secret == created.Secret {
		t.Fatalf("rotate should mint a new secret, got %q", rotated.Secret)
	}

	// Grace 0: the original secret dies at once; the rotated one authenticates.
	oldAuth := httptest.NewRecorder()
	oldReq := httptest.NewRequest(http.MethodGet, "/v1/me", http.NoBody)
	oldReq.Header.Set("X-Api-Key", created.Secret)
	handler.ServeHTTP(oldAuth, oldReq)
	if oldAuth.Code != http.StatusUnauthorized {
		t.Fatalf("rotated-away secret should not authenticate -> %d", oldAuth.Code)
	}
	rotAuth := httptest.NewRecorder()
	rotMe := httptest.NewRequest(http.MethodGet, "/v1/me", http.NoBody)
	rotMe.Header.Set("X-Api-Key", rotated.Secret)
	handler.ServeHTTP(rotAuth, rotMe)
	if rotAuth.Code != http.StatusOK || !strings.Contains(rotAuth.Body.String(), `"actor":"etl-worker"`) {
		t.Fatalf("rotated secret should authenticate -> %d body=%s", rotAuth.Code, rotAuth.Body.String())
	}

	revoke := httptest.NewRecorder()
	revReq := httptest.NewRequest(http.MethodDelete, "/v1/api-keys/"+created.APIKey.ID, http.NoBody)
	revReq.Header.Set("X-Api-Key", "admin-key")
	handler.ServeHTTP(revoke, revReq)
	if revoke.Code != http.StatusOK {
		t.Fatalf("revoke api key -> %d body=%s", revoke.Code, revoke.Body.String())
	}

	after := httptest.NewRecorder()
	afterReq := httptest.NewRequest(http.MethodGet, "/v1/me", http.NoBody)
	afterReq.Header.Set("X-Api-Key", rotated.Secret)
	handler.ServeHTTP(after, afterReq)
	if after.Code != http.StatusUnauthorized {
		t.Fatalf("revoked key should not authenticate -> %d body=%s", after.Code, after.Body.String())
	}

	// Create, rotate, and revoke each leave an audit breadcrumb on the log,
	// attributed to the admin caller (not the token's bound actor) and
	// referencing the token id.
	evs, err := log.Read(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	var createdEv, rotatedEv, revokedEv bool
	for _, e := range evs {
		if e.Stream != auth.AuditStream || e.Actor != "admin" {
			continue
		}
		if !strings.Contains(string(e.Payload), created.APIKey.ID) {
			t.Fatalf("audit event %q missing token id: %s", e.Type, e.Payload)
		}
		switch e.Type {
		case auth.EventManagedKeyCreated:
			createdEv = true
		case auth.EventManagedKeyRotated:
			rotatedEv = true
		case auth.EventManagedKeyRevoked:
			revokedEv = true
		}
	}
	if !createdEv || !rotatedEv || !revokedEv {
		t.Fatalf("expected created+rotated+revoked audit events, got created=%v rotated=%v revoked=%v",
			createdEv, rotatedEv, revokedEv)
	}
}

// A caller cannot mint or rotate a key broader than their own scope: a
// sandbox-scoped admin must be denied when issuing a production key, and denied
// when rotating an existing production key into a fresh live secret.
func TestManagedAPIKeysScopeCeiling(t *testing.T) {
	log, st := testutil.NewLogStore(t)
	keys := auth.NewStoreAPIKeys(st)
	keyring := auth.NewKeyring()
	keyring.UseResolver(keys)
	sessions := auth.NewSessions()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "admin"}
	// A full-scope admin to plant a production key, and a sandbox-scoped admin to
	// attempt the escalations.
	keyring.Add("super-key", auth.APIKey{ID: "super", Identity: id, Scope: auth.ScopeAll, Role: auth.RoleAdmin})
	keyring.Add("sandbox-admin", auth.APIKey{ID: "sbx", Identity: id, Scope: auth.Sandbox, Role: auth.RoleAdmin})

	api := http.NewServeMux()
	httpx.NewAPIKeysHandler(keys, log).Routes(api)
	handler := httpx.Chain(api, httpx.Authenticate(keyring, sessions), httpx.Authorize)

	do := func(key, method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("X-Api-Key", key)
		handler.ServeHTTP(rec, req)
		return rec
	}

	// The sandbox-scoped admin cannot create a production-scoped key.
	if rec := do("sandbox-admin", http.MethodPost, "/v1/api-keys",
		`{"name":"prod","actor":"p","role":"operator","scope":"production"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("sandbox admin creating production key -> %d, want 403 (body=%s)", rec.Code, rec.Body.String())
	}

	// It CAN create a sandbox key (within its ceiling).
	if rec := do("sandbox-admin", http.MethodPost, "/v1/api-keys",
		`{"name":"sbx","actor":"s","role":"operator","scope":"sandbox"}`); rec.Code != http.StatusCreated {
		t.Fatalf("sandbox admin creating sandbox key -> %d, want 201 (body=%s)", rec.Code, rec.Body.String())
	}

	// Plant a production key with the full-scope admin, then prove the sandbox admin
	// cannot rotate it into a fresh live secret.
	plant := do("super-key", http.MethodPost, "/v1/api-keys",
		`{"name":"etl","actor":"etl","role":"operator","scope":"production"}`)
	if plant.Code != http.StatusCreated {
		t.Fatalf("plant production key -> %d body=%s", plant.Code, plant.Body.String())
	}
	var planted struct {
		APIKey auth.ManagedAPIKey `json:"api_key"`
	}
	if err := json.Unmarshal(plant.Body.Bytes(), &planted); err != nil {
		t.Fatal(err)
	}
	if rec := do("sandbox-admin", http.MethodPost, "/v1/api-keys/"+planted.APIKey.ID+"/rotate",
		`{"grace_seconds":0}`); rec.Code != http.StatusForbidden {
		t.Fatalf("sandbox admin rotating production key -> %d, want 403 (body=%s)", rec.Code, rec.Body.String())
	}
}
