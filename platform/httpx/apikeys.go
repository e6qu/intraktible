// SPDX-License-Identifier: AGPL-3.0-or-later

package httpx

import (
	"fmt"
	"net/http"
	"time"

	"github.com/e6qu/intraktible/platform/auth"
	"github.com/e6qu/intraktible/platform/identity"
)

// APIKeysHandler exposes admin-only management for durable API tokens.
type APIKeysHandler struct {
	keys *auth.StoreAPIKeys
}

// NewAPIKeysHandler returns the HTTP shell for managed API tokens.
func NewAPIKeysHandler(keys *auth.StoreAPIKeys) *APIKeysHandler {
	return &APIKeysHandler{keys: keys}
}

// Routes registers managed API-token endpoints.
func (h *APIKeysHandler) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/api-keys", h.list)
	mux.HandleFunc("POST /v1/api-keys", h.create)
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
	JSON(w, http.StatusCreated, map[string]any{"api_key": key, "secret": secret})
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
	key, err = h.keys.Revoke(r.Context(), keyID)
	if err != nil {
		Error(w, http.StatusBadRequest, err)
		return
	}
	JSON(w, http.StatusOK, map[string]any{"api_key": key})
}
