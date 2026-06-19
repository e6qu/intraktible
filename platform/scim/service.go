// SPDX-License-Identifier: AGPL-3.0-or-later

package scim

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// Service is the SCIM 2.0 Users HTTP surface. It authenticates the IdP with a
// static bearer token and provisions into one configured tenant (org,
// workspace) — the tenant whose OIDC SSO it backs.
type Service struct {
	store     *Store
	token     string
	org       string
	workspace string
}

// NewService builds the SCIM service. token is the bearer credential the IdP
// presents; org/workspace is the tenant users are provisioned into.
func NewService(s *Store, token, org, workspace string) *Service {
	return &Service{store: s, token: token, org: org, workspace: workspace}
}

// Routes registers the SCIM Users endpoints (mounted public; bearer-authed here).
func (svc *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /scim/v2/Users", svc.auth(svc.create))
	mux.HandleFunc("GET /scim/v2/Users", svc.auth(svc.list))
	mux.HandleFunc("GET /scim/v2/Users/{id}", svc.auth(svc.get))
	mux.HandleFunc("PATCH /scim/v2/Users/{id}", svc.auth(svc.patch))
	mux.HandleFunc("DELETE /scim/v2/Users/{id}", svc.auth(svc.remove))
	mux.HandleFunc("POST /scim/v2/Groups", svc.auth(svc.createGroup))
	mux.HandleFunc("GET /scim/v2/Groups", svc.auth(svc.listGroups))
	mux.HandleFunc("GET /scim/v2/Groups/{id}", svc.auth(svc.getGroup))
	mux.HandleFunc("PUT /scim/v2/Groups/{id}", svc.auth(svc.replaceGroup))
	mux.HandleFunc("PATCH /scim/v2/Groups/{id}", svc.auth(svc.patchGroup))
	mux.HandleFunc("DELETE /scim/v2/Groups/{id}", svc.auth(svc.removeGroup))
}

func (svc *Service) auth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if svc.token == "" || subtle.ConstantTimeCompare([]byte(got), []byte(svc.token)) != 1 {
			writeError(w, http.StatusUnauthorized, "invalid bearer token")
			return
		}
		h(w, r)
	}
}

func (svc *Service) create(w http.ResponseWriter, r *http.Request) {
	var u User
	if err := json.NewDecoder(r.Body).Decode(&u); err != nil {
		writeError(w, http.StatusBadRequest, "invalid SCIM user body")
		return
	}
	u.Org, u.Workspace, u.ID = svc.org, svc.workspace, ""
	created, err := svc.store.Create(r.Context(), u)
	if err != nil {
		// A duplicate userName is the SCIM 409 case IdPs expect.
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "already exists") {
			status = http.StatusConflict
		}
		writeError(w, status, err.Error())
		return
	}
	write(w, http.StatusCreated, created)
}

func (svc *Service) list(w http.ResponseWriter, r *http.Request) {
	users, err := svc.store.List(r.Context(), svc.org, svc.workspace, userNameFilter(r.URL.Query().Get("filter")))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	write(w, http.StatusOK, map[string]any{
		"schemas":      []string{"urn:ietf:params:scim:api:messages:2.0:ListResponse"},
		"totalResults": len(users),
		"Resources":    users,
	})
}

func (svc *Service) get(w http.ResponseWriter, r *http.Request) {
	u, ok, err := svc.store.Get(r.Context(), svc.org, svc.workspace, r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	write(w, http.StatusOK, u)
}

// patch handles the deprovision/reactivate operation: a SCIM PatchOp that sets
// active. It tolerates the Okta ({value:{active:false}}) and Azure
// ({path:"active", value:false|"False"}) shapes.
func (svc *Service) patch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Operations []struct {
			Op    string          `json:"op"`
			Path  string          `json:"path"`
			Value json.RawMessage `json:"value"`
		} `json:"Operations"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid SCIM patch body")
		return
	}
	active, found := false, false
	for _, op := range req.Operations {
		if !strings.EqualFold(op.Op, "replace") && !strings.EqualFold(op.Op, "add") {
			continue
		}
		if strings.EqualFold(op.Path, "active") {
			if v, ok := parseBool(op.Value); ok {
				active, found = v, true
			}
		} else if op.Path == "" {
			var obj struct {
				Active *bool `json:"active"`
			}
			if json.Unmarshal(op.Value, &obj) == nil && obj.Active != nil {
				active, found = *obj.Active, true
			}
		}
	}
	if !found {
		writeError(w, http.StatusBadRequest, "patch must set active")
		return
	}
	u, err := svc.store.SetActive(r.Context(), svc.org, svc.workspace, r.PathValue("id"), active)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	write(w, http.StatusOK, u)
}

func (svc *Service) remove(w http.ResponseWriter, r *http.Request) {
	if err := svc.store.Delete(r.Context(), svc.org, svc.workspace, r.PathValue("id")); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (svc *Service) createGroup(w http.ResponseWriter, r *http.Request) {
	var g Group
	if err := json.NewDecoder(r.Body).Decode(&g); err != nil {
		writeError(w, http.StatusBadRequest, "invalid SCIM group body")
		return
	}
	g.Org, g.Workspace, g.ID = svc.org, svc.workspace, ""
	created, err := svc.store.CreateGroup(r.Context(), g)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	write(w, http.StatusCreated, created)
}

func (svc *Service) listGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := svc.store.ListGroups(r.Context(), svc.org, svc.workspace)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	write(w, http.StatusOK, map[string]any{
		"schemas":      []string{"urn:ietf:params:scim:api:messages:2.0:ListResponse"},
		"totalResults": len(groups),
		"Resources":    groups,
	})
}

func (svc *Service) getGroup(w http.ResponseWriter, r *http.Request) {
	g, ok, err := svc.store.GetGroup(r.Context(), svc.org, svc.workspace, r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "group not found")
		return
	}
	write(w, http.StatusOK, g)
}

// replaceGroup (PUT) sets the membership to exactly the body's members — the
// shape Okta uses to sync a group.
func (svc *Service) replaceGroup(w http.ResponseWriter, r *http.Request) {
	var g Group
	if err := json.NewDecoder(r.Body).Decode(&g); err != nil {
		writeError(w, http.StatusBadRequest, "invalid SCIM group body")
		return
	}
	out, err := svc.store.SetMembers(r.Context(), svc.org, svc.workspace, r.PathValue("id"), memberIDs(g.Members), MembersReplace)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	write(w, http.StatusOK, out)
}

// patchGroup applies member add/remove operations — the shape Azure AD uses.
func (svc *Service) patchGroup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Operations []struct {
			Op    string          `json:"op"`
			Path  string          `json:"path"`
			Value json.RawMessage `json:"value"`
		} `json:"Operations"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid SCIM patch body")
		return
	}
	id := r.PathValue("id")
	// Collect the member ops and apply them in one atomic read-modify-write, so a
	// multi-op PATCH is all-or-nothing (a partial apply + IdP retry would otherwise
	// double-apply adds/removes).
	var ops []MemberOp
	for _, op := range req.Operations {
		if !strings.Contains(strings.ToLower(op.Path), "members") {
			continue
		}
		mode := MembersAdd
		ids := memberValuesFromPatch(op.Value)
		switch {
		case strings.EqualFold(op.Op, "remove"):
			mode = MembersRemove
			if filtered := memberIDFromPath(op.Path); filtered != "" {
				ids = []string{filtered}
			}
		case strings.EqualFold(op.Op, "replace"):
			mode = MembersReplace
		}
		ops = append(ops, MemberOp{Mode: mode, IDs: ids})
	}
	if len(ops) > 0 {
		if _, err := svc.store.PatchMembers(r.Context(), svc.org, svc.workspace, id, ops); err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
	}
	g, ok, err := svc.store.GetGroup(r.Context(), svc.org, svc.workspace, id)
	if err != nil || !ok {
		writeError(w, http.StatusNotFound, "group not found")
		return
	}
	write(w, http.StatusOK, g)
}

func (svc *Service) removeGroup(w http.ResponseWriter, r *http.Request) {
	if err := svc.store.DeleteGroup(r.Context(), svc.org, svc.workspace, r.PathValue("id")); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func memberIDs(ms []Member) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.Value)
	}
	return out
}

// memberValuesFromPatch reads a PATCH op value that is an array of {value} member
// references (the add/replace form).
func memberValuesFromPatch(raw json.RawMessage) []string {
	var ms []Member
	if json.Unmarshal(raw, &ms) == nil {
		return memberIDs(ms)
	}
	return nil
}

// memberIDFromPath extracts ID from a `members[value eq "ID"]` remove path (the
// Azure single-member-remove form).
func memberIDFromPath(path string) string {
	i, j := strings.Index(path, `"`), strings.LastIndex(path, `"`)
	if i >= 0 && j > i {
		return path[i+1 : j]
	}
	return ""
}

// userNameFilter extracts X from a `userName eq "X"` SCIM filter (the only
// filter IdPs use to find an existing user); anything else yields no filter.
func userNameFilter(filter string) string {
	parts := strings.SplitN(strings.TrimSpace(filter), " ", 3)
	if len(parts) == 3 && strings.EqualFold(parts[0], "userName") && strings.EqualFold(parts[1], "eq") {
		return strings.Trim(parts[2], `"`)
	}
	return ""
}

func parseBool(raw json.RawMessage) (bool, bool) {
	var b bool
	if json.Unmarshal(raw, &b) == nil {
		return b, true
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if v, err := strconv.ParseBool(s); err == nil {
			return v, true
		}
	}
	return false, false
}

func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/scim+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, detail string) {
	write(w, status, map[string]any{
		"schemas": []string{"urn:ietf:params:scim:api:messages:2.0:Error"},
		"detail":  detail,
		"status":  strconv.Itoa(status),
	})
}
