// SPDX-License-Identifier: AGPL-3.0-or-later

package httpx

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/e6qu/intraktible/platform/auth"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
)

// APIKeysHandler exposes admin-only management for durable API tokens.
type APIKeysHandler struct {
	keys *auth.StoreAPIKeys
	log  eventlog.Log
}

// NewAPIKeysHandler returns the HTTP shell for managed API tokens. The log
// receives a token-lifecycle audit event on each create/revoke.
func NewAPIKeysHandler(keys *auth.StoreAPIKeys, log eventlog.Log) *APIKeysHandler {
	return &APIKeysHandler{keys: keys, log: log}
}

// Routes registers managed API-token endpoints.
func (h *APIKeysHandler) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/api-keys", h.list)
	mux.HandleFunc("POST /v1/api-keys", h.create)
	mux.HandleFunc("POST /v1/api-keys/{key_id}/rotate", h.rotate)
	mux.HandleFunc("DELETE /v1/api-keys/{key_id}", h.revoke)
}

type createAPIKeyRequest struct {
	Name      string     `json:"name"`
	Actor     string     `json:"actor"`
	Scope     auth.Scope `json:"scope,omitempty"`
	Role      auth.Role  `json:"role"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

func (h *APIKeysHandler) list(w http.ResponseWriter, r *http.Request) {
	id, ok := Caller(w, r)
	if !ok {
		return
	}
	keys, err := h.keys.List(r.Context())
	if err != nil {
		Error(w, http.StatusInternalServerError, err)
		return
	}
	filtered := make([]auth.ManagedAPIKey, 0, len(keys))
	for _, key := range keys {
		if key.Identity.Org == id.Org && key.Identity.Workspace == id.Workspace {
			filtered = append(filtered, key)
		}
	}
	JSON(w, http.StatusOK, map[string]any{"api_keys": filtered})
}

func (h *APIKeysHandler) create(w http.ResponseWriter, r *http.Request) {
	id, ok := Caller(w, r)
	if !ok {
		return
	}
	var req createAPIKeyRequest
	if err := DecodeJSON(r, &req); err != nil {
		Error(w, http.StatusBadRequest, err)
		return
	}
	// Ceiling: a caller cannot mint a key broader than their own credential — neither
	// a wider environment scope nor a higher role. Without this, a sandbox-scoped (or
	// lower-privileged) admin could escalate by issuing a production/admin key.
	reqScope := req.Scope
	if reqScope == "" {
		reqScope = auth.Sandbox // mirror StoreAPIKeys.Create's default before the ceiling check
	}
	if callerScope, ok := Scope(r.Context()); !ok || !callerScope.Covers(reqScope) {
		Error(w, http.StatusForbidden, fmt.Errorf("cannot create a key with scope %q exceeding your own", reqScope))
		return
	}
	if !RoleOf(r.Context()).AtLeast(req.Role) {
		Error(w, http.StatusForbidden, fmt.Errorf("cannot create a key with role %q exceeding your own", req.Role))
		return
	}
	key, secret, err := h.keys.Create(r.Context(), auth.ManagedAPIKey{
		Name: req.Name,
		Identity: identity.Identity{
			Org:       id.Org,
			Workspace: id.Workspace,
			Actor:     req.Actor,
		},
		Scope:     req.Scope,
		Role:      req.Role,
		ExpiresAt: req.ExpiresAt,
	})
	if err != nil {
		Error(w, http.StatusBadRequest, err)
		return
	}
	if err := h.audit(r.Context(), id, auth.EventManagedKeyCreated, key.CreatedAt, auth.APIKeyAudit{
		KeyID:      key.ID,
		Name:       key.Name,
		Role:       key.Role,
		Scope:      key.Scope,
		TokenActor: key.Identity.Actor,
		ExpiresAt:  key.ExpiresAt,
	}); err != nil {
		Error(w, http.StatusInternalServerError, err)
		return
	}
	JSON(w, http.StatusCreated, map[string]any{"api_key": key, "secret": secret})
}

// audit records a token-lifecycle event on the log under the caller's identity,
// so the action is attributed to the admin who performed it (not the token's
// own bound actor). The recorded time is the lifecycle timestamp for replay
// stability.
func (h *APIKeysHandler) audit(ctx context.Context, id identity.Identity, typ string, at time.Time, payload auth.APIKeyAudit) error {
	_, err := eventlog.AppendJSON(ctx, h.log, id.Org, id.Workspace, id.Actor, auth.AuditStream, typ, at, payload)
	return err
}

type rotateAPIKeyRequest struct {
	GraceSeconds int `json:"grace_seconds,omitempty"`
}

func (h *APIKeysHandler) rotate(w http.ResponseWriter, r *http.Request) {
	id, ok := Caller(w, r)
	if !ok {
		return
	}
	keyID := r.PathValue("key_id")
	key, found, err := h.keys.Get(r.Context(), keyID)
	if err != nil {
		Error(w, http.StatusInternalServerError, err)
		return
	}
	if !found || key.Identity.Org != id.Org || key.Identity.Workspace != id.Workspace {
		Error(w, http.StatusNotFound, fmt.Errorf("api key not found"))
		return
	}
	// Ceiling: rotation mints a fresh working secret, so a caller must not rotate a
	// key broader than their own scope (else a sandbox-scoped admin could obtain a
	// live production secret for an existing key).
	if callerScope, ok := Scope(r.Context()); !ok || !callerScope.Covers(key.Scope) {
		Error(w, http.StatusForbidden, fmt.Errorf("cannot rotate a key with scope %q exceeding your own", key.Scope))
		return
	}
	var req rotateAPIKeyRequest
	if r.ContentLength != 0 {
		if err := DecodeJSON(r, &req); err != nil {
			Error(w, http.StatusBadRequest, err)
			return
		}
	}
	if req.GraceSeconds < 0 {
		Error(w, http.StatusBadRequest, fmt.Errorf("grace_seconds must be >= 0"))
		return
	}
	key, secret, err := h.keys.Rotate(r.Context(), keyID, time.Duration(req.GraceSeconds)*time.Second)
	if err != nil {
		Error(w, http.StatusBadRequest, err)
		return
	}
	at := key.CreatedAt
	if key.RotatedAt != nil {
		at = *key.RotatedAt
	}
	if err := h.audit(r.Context(), id, auth.EventManagedKeyRotated, at, auth.APIKeyAudit{
		KeyID: key.ID,
		Name:  key.Name,
		Role:  key.Role,
		Scope: key.Scope,
	}); err != nil {
		Error(w, http.StatusInternalServerError, err)
		return
	}
	JSON(w, http.StatusOK, map[string]any{"api_key": key, "secret": secret})
}

func (h *APIKeysHandler) revoke(w http.ResponseWriter, r *http.Request) {
	id, ok := Caller(w, r)
	if !ok {
		return
	}
	keyID := r.PathValue("key_id")
	key, found, err := h.keys.Get(r.Context(), keyID)
	if err != nil {
		Error(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		Error(w, http.StatusNotFound, fmt.Errorf("api key not found"))
		return
	}
	if key.Identity.Org != id.Org || key.Identity.Workspace != id.Workspace {
		Error(w, http.StatusNotFound, fmt.Errorf("api key not found"))
		return
	}
	// Ceiling (mirrors rotate): a lower-scoped admin must not disable a key broader
	// than their own scope — revoking a production credential from a sandbox-scoped
	// admin is a denial-of-service against an environment they can't reach.
	if callerScope, ok := Scope(r.Context()); !ok || !callerScope.Covers(key.Scope) {
		Error(w, http.StatusForbidden, fmt.Errorf("cannot revoke a key with scope %q exceeding your own", key.Scope))
		return
	}
	key, err = h.keys.Revoke(r.Context(), keyID)
	if err != nil {
		Error(w, http.StatusBadRequest, err)
		return
	}
	revokedAt := key.CreatedAt
	if key.RevokedAt != nil {
		revokedAt = *key.RevokedAt
	}
	if err := h.audit(r.Context(), id, auth.EventManagedKeyRevoked, revokedAt, auth.APIKeyAudit{
		KeyID: key.ID,
		Name:  key.Name,
		Role:  key.Role,
		Scope: key.Scope,
	}); err != nil {
		Error(w, http.StatusInternalServerError, err)
		return
	}
	JSON(w, http.StatusOK, map[string]any{"api_key": key})
}
