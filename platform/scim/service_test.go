// SPDX-License-Identifier: AGPL-3.0-or-later

package scim_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/scim"
	"github.com/e6qu/intraktible/platform/store"
)

// A SCIM create that omits "active" must provision an ACTIVE user (RFC 7643:
// omitted active = true), not a locked-out one.
func TestSCIMCreateDefaultsActiveWhenOmitted(t *testing.T) {
	mux := http.NewServeMux()
	scim.NewService(scim.NewStore(store.NewMemory()), "scim-token", "demo", "main").Routes(mux)
	rec := bearerDo(mux, http.MethodPost, "/scim/v2/Users", `{"userName":"omit@acme.com"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create -> %d", rec.Code)
	}
	var u scim.User
	_ = json.Unmarshal(rec.Body.Bytes(), &u)
	if !u.Active {
		t.Fatal("a user created without an explicit active flag must be active (not locked out)")
	}
}

// SCIM mutation endpoints sit behind only the static bearer token (outside the
// authenticated chain's body limit), so each must bound its own body. A body past
// the cap is rejected (400) rather than read whole into memory.
func TestSCIMRejectsOversizedBody(t *testing.T) {
	mux := http.NewServeMux()
	scim.NewService(scim.NewStore(store.NewMemory()), "scim-token", "demo", "main").Routes(mux)
	// A >1 MiB body: a valid JSON prefix the LimitReader truncates mid-string, so it
	// fails to parse instead of being fully buffered.
	huge := `{"userName":"` + strings.Repeat("a", 2<<20) + `"}`
	for _, path := range []string{"/scim/v2/Users", "/scim/v2/Groups"} {
		rec := bearerDo(mux, http.MethodPost, path, huge)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("POST %s oversized body -> %d, want 400", path, rec.Code)
		}
	}
}

func TestSCIMProvisionAndDeprovision(t *testing.T) {
	st := store.NewMemory()
	users := scim.NewStore(st)
	mux := http.NewServeMux()
	scim.NewService(users, "scim-token", "demo", "main").Routes(mux)

	do := func(method, path, body string) *httptest.ResponseRecorder {
		return bearerDo(mux, method, path, body)
	}

	// Provision a user.
	create := do(http.MethodPost, "/scim/v2/Users", `{"userName":"ada@acme.com","active":true}`)
	if create.Code != http.StatusCreated {
		t.Fatalf("create -> %d body=%s", create.Code, create.Body.String())
	}
	var created scim.User
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.UserName != "ada@acme.com" || !created.Active {
		t.Fatalf("created = %+v", created)
	}

	// A duplicate userName is a 409.
	if dup := do(http.MethodPost, "/scim/v2/Users", `{"userName":"ada@acme.com"}`); dup.Code != http.StatusConflict {
		t.Fatalf("duplicate create -> %d, want 409", dup.Code)
	}

	// Filter by userName finds it (the IdP's pre-create lookup).
	list := do(http.MethodGet, "/scim/v2/Users?filter="+url.QueryEscape(`userName eq "ada@acme.com"`), "")
	var lr struct {
		TotalResults int         `json:"totalResults"`
		Resources    []scim.User `json:"Resources"`
	}
	_ = json.Unmarshal(list.Body.Bytes(), &lr)
	if lr.TotalResults != 1 || lr.Resources[0].ID != created.ID {
		t.Fatalf("list/filter = %s", list.Body.String())
	}

	// A present-but-empty filter (`userName eq ""`) is a precise filter that matches
	// no user — it must NOT degrade into enumerating every user in the tenant.
	empty := do(http.MethodGet, "/scim/v2/Users?filter="+url.QueryEscape(`userName eq ""`), "")
	var er struct {
		TotalResults int `json:"totalResults"`
	}
	_ = json.Unmarshal(empty.Body.Bytes(), &er)
	if er.TotalResults != 0 {
		t.Fatalf("empty-value filter must match no users, got %d: %s", er.TotalResults, empty.Body.String())
	}

	ctx := context.Background()
	if !users.Allowed(ctx, identity.Identity{Org: "demo", Workspace: "main"}, "ada@acme.com") {
		t.Fatal("an active user should be allowed to log in")
	}

	// Deprovision via PATCH (the Azure shape: path=active, value=false).
	patch := do(http.MethodPatch, "/scim/v2/Users/"+created.ID,
		`{"Operations":[{"op":"replace","path":"active","value":false}]}`)
	if patch.Code != http.StatusOK {
		t.Fatalf("patch -> %d body=%s", patch.Code, patch.Body.String())
	}
	if users.Allowed(ctx, identity.Identity{Org: "demo", Workspace: "main"}, "ada@acme.com") {
		t.Fatal("a deactivated user must not be allowed to log in")
	}
	// An unprovisioned user is allowed (SCIM gates deprovisioning, not first login).
	if !users.Allowed(ctx, identity.Identity{Org: "demo", Workspace: "main"}, "newcomer@acme.com") {
		t.Fatal("an unprovisioned user should be allowed")
	}

	// Reactivate via the Okta shape (value object), then delete.
	if r := do(http.MethodPatch, "/scim/v2/Users/"+created.ID,
		`{"Operations":[{"op":"replace","value":{"active":true}}]}`); r.Code != http.StatusOK {
		t.Fatalf("reactivate -> %d", r.Code)
	}
	if !users.Allowed(ctx, identity.Identity{Org: "demo", Workspace: "main"}, "ada@acme.com") {
		t.Fatal("reactivated user should be allowed")
	}
	if d := do(http.MethodDelete, "/scim/v2/Users/"+created.ID, ""); d.Code != http.StatusNoContent {
		t.Fatalf("delete -> %d", d.Code)
	}
}

func bearerDo(mux *http.ServeMux, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer scim-token")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestSCIMGroupsDriveMembership(t *testing.T) {
	st := store.NewMemory()
	users := scim.NewStore(st)
	mux := http.NewServeMux()
	scim.NewService(users, "scim-token", "demo", "main").Routes(mux)
	ctx := context.Background()

	// Provision a user, then a group that includes them.
	var u scim.User
	_ = json.Unmarshal(bearerDo(mux, http.MethodPost, "/scim/v2/Users", `{"userName":"ada@acme.com","active":true}`).Body.Bytes(), &u)
	g := bearerDo(mux, http.MethodPost, "/scim/v2/Groups",
		`{"displayName":"engineers","members":[{"value":"`+u.ID+`"}]}`)
	if g.Code != http.StatusCreated {
		t.Fatalf("create group -> %d body=%s", g.Code, g.Body.String())
	}
	var group scim.Group
	_ = json.Unmarshal(g.Body.Bytes(), &group)

	names, err := users.GroupsForUser(ctx, identity.Identity{Org: "demo", Workspace: "main"}, "ada@acme.com")
	if err != nil || len(names) != 1 || names[0] != "engineers" {
		t.Fatalf("GroupsForUser = %v err=%v", names, err)
	}

	// Azure-style PATCH remove drops the member; GroupsForUser reflects it.
	rm := bearerDo(mux, http.MethodPatch, "/scim/v2/Groups/"+group.ID,
		`{"Operations":[{"op":"remove","path":"members[value eq \"`+u.ID+`\"]"}]}`)
	if rm.Code != http.StatusOK {
		t.Fatalf("patch remove -> %d body=%s", rm.Code, rm.Body.String())
	}
	if names, _ := users.GroupsForUser(ctx, identity.Identity{Org: "demo", Workspace: "main"}, "ada@acme.com"); len(names) != 0 {
		t.Fatalf("member should have been removed, groups=%v", names)
	}

	// PATCH add puts them back.
	add := bearerDo(mux, http.MethodPatch, "/scim/v2/Groups/"+group.ID,
		`{"Operations":[{"op":"add","path":"members","value":[{"value":"`+u.ID+`"}]}]}`)
	if add.Code != http.StatusOK {
		t.Fatalf("patch add -> %d body=%s", add.Code, add.Body.String())
	}
	if names, _ := users.GroupsForUser(ctx, identity.Identity{Org: "demo", Workspace: "main"}, "ada@acme.com"); len(names) != 1 {
		t.Fatalf("member should be back, groups=%v", names)
	}

	// PUT replaces the membership wholesale (here: empty).
	if r := bearerDo(mux, http.MethodPut, "/scim/v2/Groups/"+group.ID,
		`{"displayName":"engineers","members":[]}`); r.Code != http.StatusOK {
		t.Fatalf("put replace -> %d", r.Code)
	}
	if names, _ := users.GroupsForUser(ctx, identity.Identity{Org: "demo", Workspace: "main"}, "ada@acme.com"); len(names) != 0 {
		t.Fatalf("PUT should have cleared membership, groups=%v", names)
	}
}

func TestSCIMRequiresBearerToken(t *testing.T) {
	mux := http.NewServeMux()
	scim.NewService(scim.NewStore(store.NewMemory()), "scim-token", "demo", "main").Routes(mux)

	req := httptest.NewRequest(http.MethodGet, "/scim/v2/Users", http.NoBody)
	req.Header.Set("Authorization", "Bearer wrong")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad token -> %d, want 401", rec.Code)
	}
}

// TestSCIMExternalIDIdempotentCreate guards the rename/re-create class: a second
// Create carrying the same immutable externalId updates the existing record in
// place (same id, new userName) rather than leaving a stale duplicate.
func TestSCIMExternalIDIdempotentCreate(t *testing.T) {
	ctx := context.Background()
	users := scim.NewStore(store.NewMemory())

	first, err := users.Create(ctx, scim.User{Org: "demo", Workspace: "main", UserName: "ada@acme.com", ExternalID: "ext-1", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	// Re-create with the SAME externalId but a renamed userName.
	second, err := users.Create(ctx, scim.User{Org: "demo", Workspace: "main", UserName: "ada.lovelace@acme.com", ExternalID: "ext-1", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID {
		t.Fatalf("re-create with the same externalId must reuse the id: %s != %s", second.ID, first.ID)
	}
	if second.UserName != "ada.lovelace@acme.com" {
		t.Fatalf("re-create must update the userName, got %q", second.UserName)
	}
	all, err := users.List(ctx, identity.Identity{Org: "demo", Workspace: "main"}, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("expected exactly one user after rename re-create, got %d", len(all))
	}
}
