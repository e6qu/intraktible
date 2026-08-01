// SPDX-License-Identifier: AGPL-3.0-or-later

// Package service exposes the solution-pack lifecycle over HTTP: define,
// install, upgrade, roll back, and retire signed, versioned, dependency-pinned
// pack manifests.
package service

import (
	"context"
	"net/http"

	"github.com/e6qu/intraktible/packs/command"
	"github.com/e6qu/intraktible/packs/domain"
	"github.com/e6qu/intraktible/packs/projection"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// Service is the solution-packs HTTP shell.
type Service struct {
	cmd   *command.Handler
	store store.Store
}

// New constructs a packs Service.
func New(cmd *command.Handler, st store.Store) *Service {
	return &Service{cmd: cmd, store: st}
}

// Routes registers the solution-pack lifecycle endpoints.
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/packs", s.define)
	mux.HandleFunc("GET /v1/packs", s.list)
	mux.HandleFunc("GET /v1/packs/{name}", s.get)
	mux.HandleFunc("POST /v1/packs/{name}/install", s.install)
	mux.HandleFunc("POST /v1/packs/{name}/upgrade", s.upgrade)
	mux.HandleFunc("POST /v1/packs/{name}/rollback", s.rollback)
	mux.HandleFunc("POST /v1/packs/{name}/retire", s.retire)
}

func (s *Service) define(w http.ResponseWriter, r *http.Request) {
	var request domain.Manifest
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	if err := httpx.DecodeJSON(r, &request); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	version, envelope, err := s.cmd.Define(r.Context(), id, request.Name, request)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, map[string]any{
		"event_id": envelope.ID, "seq": envelope.Seq, "version": version,
	})
}

func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	items, err := listPacks(r.Context(), s.store, id)
	httpx.WriteList(w, "packs", items, err)
}

func (s *Service) get(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	view, found, err := readPack(r.Context(), s.store, id, r.PathValue("name"))
	httpx.WriteOne(w, view, found, err, "packs: pack not found")
}

type versionRequest struct {
	Version int `json:"version"`
}

func (s *Service) install(w http.ResponseWriter, r *http.Request) {
	var request versionRequest
	httpx.Emit(w, r, &request, func(id identity.Identity) (eventlog.Envelope, error) {
		return s.cmd.Install(r.Context(), id, r.PathValue("name"), request.Version)
	})
}

func (s *Service) upgrade(w http.ResponseWriter, r *http.Request) {
	var request versionRequest
	httpx.Emit(w, r, &request, func(id identity.Identity) (eventlog.Envelope, error) {
		return s.cmd.Upgrade(r.Context(), id, r.PathValue("name"), request.Version)
	})
}

func (s *Service) rollback(w http.ResponseWriter, r *http.Request) {
	var request versionRequest
	httpx.Emit(w, r, &request, func(id identity.Identity) (eventlog.Envelope, error) {
		return s.cmd.Rollback(r.Context(), id, r.PathValue("name"), request.Version)
	})
}

type retireRequest struct {
	Reason string `json:"reason"`
}

func (s *Service) retire(w http.ResponseWriter, r *http.Request) {
	var request retireRequest
	httpx.Emit(w, r, &request, func(id identity.Identity) (eventlog.Envelope, error) {
		return s.cmd.Retire(r.Context(), id, r.PathValue("name"), request.Reason)
	})
}

func listPacks(
	ctx context.Context,
	st store.Store,
	id identity.Identity,
) ([]projection.PackView, error) {
	return store.QueryDocs(ctx, st, projection.CollectionPacks, store.Key(id.Org, id.Workspace, ""),
		nil,
		func(a, b projection.PackView) bool { return a.Name < b.Name })
}

func readPack(
	ctx context.Context,
	st store.Store,
	id identity.Identity,
	name string,
) (projection.PackView, bool, error) {
	return store.GetDoc[projection.PackView](
		ctx, st, projection.CollectionPacks, store.Key(id.Org, id.Workspace, name),
	)
}
