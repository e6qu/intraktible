// SPDX-License-Identifier: AGPL-3.0-or-later

package httpx_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/e6qu/intraktible/platform/auth"
	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

func TestManagedAPIKeysCreateUseRevoke(t *testing.T) {
	st := store.NewMemory()
	keys := auth.NewStoreAPIKeys(st)
	keyring := auth.NewKeyring()
	keyring.UseResolver(keys)
	sessions := auth.NewSessions()
	adminID := identity.Identity{Org: "demo", Workspace: "main", Actor: "admin"}
	keyring.Add("admin-key", auth.APIKey{ID: "admin", Identity: adminID, Scope: auth.Sandbox, Role: auth.RoleAdmin})

	api := http.NewServeMux()
	httpx.NewAPIKeysHandler(keys).Routes(api)
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

	revoke := httptest.NewRecorder()
	revReq := httptest.NewRequest(http.MethodDelete, "/v1/api-keys/"+created.APIKey.ID, http.NoBody)
	revReq.Header.Set("X-Api-Key", "admin-key")
	handler.ServeHTTP(revoke, revReq)
	if revoke.Code != http.StatusOK {
		t.Fatalf("revoke api key -> %d body=%s", revoke.Code, revoke.Body.String())
	}

	after := httptest.NewRecorder()
	afterReq := httptest.NewRequest(http.MethodGet, "/v1/me", http.NoBody)
	afterReq.Header.Set("X-Api-Key", created.Secret)
	handler.ServeHTTP(after, afterReq)
	if after.Code != http.StatusUnauthorized {
		t.Fatalf("revoked key should not authenticate -> %d body=%s", after.Code, after.Body.String())
	}
}
