// SPDX-License-Identifier: AGPL-3.0-or-later

// Package service exposes the provider lifecycle over HTTP: versioned provider
// manifests with install → test → approve → deploy → pause/resume → upgrade →
// retire per environment, and health reads.
package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
	"github.com/e6qu/intraktible/providers/command"
	"github.com/e6qu/intraktible/providers/domain"
	"github.com/e6qu/intraktible/providers/projection"
)

// Service is the providers HTTP shell.
type Service struct {
	cmd   *command.Handler
	store store.Store
}

// New constructs a providers Service.
func New(cmd *command.Handler, st store.Store) *Service {
	return &Service{cmd: cmd, store: st}
}

// Routes registers the provider lifecycle endpoints.
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/providers", s.install)
	mux.HandleFunc("GET /v1/providers", s.list)
	mux.HandleFunc("GET /v1/providers/{name}/{version}", s.get)
	mux.HandleFunc("POST /v1/providers/{name}/{version}/configure", s.configure)
	mux.HandleFunc("POST /v1/providers/{name}/{version}/test", s.test)
	mux.HandleFunc("POST /v1/providers/{name}/{version}/approve", s.approve)
	mux.HandleFunc("POST /v1/providers/{name}/{version}/deploy", s.deploy)
	mux.HandleFunc("POST /v1/providers/{name}/{version}/pause", s.pause)
	mux.HandleFunc("POST /v1/providers/{name}/{version}/resume", s.resume)
	mux.HandleFunc("POST /v1/providers/{name}/upgrade", s.upgrade)
	mux.HandleFunc("POST /v1/providers/{name}/{version}/retire", s.retire)
	mux.HandleFunc("GET /v1/providers/health", s.health)
}

type installRequest struct {
	Name        string             `json:"name"`
	Connector   string             `json:"connector"`
	Description string             `json:"description"`
	Conformance domain.Conformance `json:"conformance"`
}

func (s *Service) install(w http.ResponseWriter, r *http.Request) {
	var request installRequest
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	if err := httpx.DecodeJSON(r, &request); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	version, envelope, err := s.cmd.Install(r.Context(), id, request.Name, domain.Manifest{
		Connector: request.Connector, Description: request.Description,
		Conformance: request.Conformance,
	})
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
	items, err := listProviders(r.Context(), s.store, id)
	httpx.WriteList(w, "providers", items, err)
}

func (s *Service) get(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	version, err := pathVersion(r)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	view, found, err := readProvider(r.Context(), s.store, id, r.PathValue("name"), version)
	httpx.WriteOne(w, view, found, err, "providers: provider version not found")
}

func (s *Service) configure(w http.ResponseWriter, r *http.Request) {
	var request domain.Configuration
	s.versionedAction(w, r, &request, func(id identity.Identity, version int) (eventlog.Envelope, error) {
		return s.cmd.Configure(r.Context(), id, r.PathValue("name"), version, request)
	})
}

func (s *Service) test(w http.ResponseWriter, r *http.Request) {
	var request domain.TestEvidence
	s.versionedAction(w, r, &request, func(id identity.Identity, version int) (eventlog.Envelope, error) {
		return s.cmd.Test(r.Context(), id, r.PathValue("name"), version, request)
	})
}

type approveRequest struct {
	RequestID string `json:"request_id"`
	Reason    string `json:"reason"`
}

func (s *Service) approve(w http.ResponseWriter, r *http.Request) {
	var request approveRequest
	s.versionedAction(w, r, &request, func(id identity.Identity, version int) (eventlog.Envelope, error) {
		return s.cmd.Approve(r.Context(), id, r.PathValue("name"), version, request.RequestID, request.Reason)
	})
}

type envRequest struct {
	Environment domain.Environment `json:"environment"`
}

func (s *Service) deploy(w http.ResponseWriter, r *http.Request) {
	var request envRequest
	s.versionedAction(w, r, &request, func(id identity.Identity, version int) (eventlog.Envelope, error) {
		return s.cmd.Deploy(r.Context(), id, r.PathValue("name"), version, request.Environment)
	})
}

type pauseRequest struct {
	Environment domain.Environment `json:"environment"`
	Reason      string             `json:"reason"`
}

func (s *Service) pause(w http.ResponseWriter, r *http.Request) {
	var request pauseRequest
	s.versionedAction(w, r, &request, func(id identity.Identity, version int) (eventlog.Envelope, error) {
		return s.cmd.Pause(r.Context(), id, r.PathValue("name"), version, request.Environment, request.Reason)
	})
}

func (s *Service) resume(w http.ResponseWriter, r *http.Request) {
	var request envRequest
	s.versionedAction(w, r, &request, func(id identity.Identity, version int) (eventlog.Envelope, error) {
		return s.cmd.Resume(r.Context(), id, r.PathValue("name"), version, request.Environment)
	})
}

type upgradeRequest struct {
	ToVersion   int                `json:"to_version"`
	Environment domain.Environment `json:"environment"`
}

func (s *Service) upgrade(w http.ResponseWriter, r *http.Request) {
	var request upgradeRequest
	httpx.Emit(w, r, &request, func(id identity.Identity) (eventlog.Envelope, error) {
		return s.cmd.Upgrade(r.Context(), id, r.PathValue("name"), request.ToVersion, request.Environment)
	})
}

type retireRequest struct {
	Environment domain.Environment `json:"environment"`
	Reason      string             `json:"reason"`
}

func (s *Service) retire(w http.ResponseWriter, r *http.Request) {
	var request retireRequest
	s.versionedAction(w, r, &request, func(id identity.Identity, version int) (eventlog.Envelope, error) {
		return s.cmd.Retire(r.Context(), id, r.PathValue("name"), version, request.Environment, request.Reason)
	})
}

// versionedAction is the shared shape for provider-version lifecycle mutations:
// decode the body, resolve the version path parameter, and run the command.
func (s *Service) versionedAction(
	w http.ResponseWriter,
	r *http.Request,
	request any,
	run func(id identity.Identity, version int) (eventlog.Envelope, error),
) {
	httpx.Emit(w, r, request, func(id identity.Identity) (eventlog.Envelope, error) {
		version, err := pathVersion(r)
		if err != nil {
			return eventlog.Envelope{}, err
		}
		return run(id, version)
	})
}

func (s *Service) health(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	items, err := listHealth(r.Context(), s.store, id)
	httpx.WriteList(w, "health", items, err)
}

// pathVersion parses the version path parameter as a positive integer.
func pathVersion(r *http.Request) (int, error) {
	version, err := strconv.Atoi(r.PathValue("version"))
	if err != nil || version < 1 {
		return 0, errors.New("providers: version must be a positive integer")
	}
	return version, nil
}

func listProviders(
	ctx context.Context,
	st store.Store,
	id identity.Identity,
) ([]projection.ProviderView, error) {
	return store.QueryDocs(ctx, st, projection.CollectionProviders, store.Key(id.Org, id.Workspace, ""),
		nil,
		func(a, b projection.ProviderView) bool {
			if a.Name != b.Name {
				return a.Name < b.Name
			}
			return a.Version < b.Version
		})
}

func readProvider(
	ctx context.Context,
	st store.Store,
	id identity.Identity,
	name string,
	version int,
) (projection.ProviderView, bool, error) {
	return store.GetDoc[projection.ProviderView](
		ctx, st, projection.CollectionProviders, store.Key(id.Org, id.Workspace, name+"/"+fmt.Sprint(version)),
	)
}

func listHealth(
	ctx context.Context,
	st store.Store,
	id identity.Identity,
) ([]projection.HealthView, error) {
	return store.QueryDocs(ctx, st, projection.CollectionHealth, store.Key(id.Org, id.Workspace, ""),
		nil,
		func(a, b projection.HealthView) bool { return a.Name < b.Name })
}
