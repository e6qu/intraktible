// SPDX-License-Identifier: AGPL-3.0-or-later

package assertions

import (
	"context"
	"fmt"
	"net/http"

	"github.com/e6qu/intraktible/decision-engine/flows"
	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// Service is the assertions HTTP surface (imperative shell): manage and run a
// flow's test cases.
type Service struct {
	cmd   *Handler
	store store.Store
}

// New wires the assertions write side and read model to HTTP.
func New(cmd *Handler, st store.Store) *Service {
	return &Service{cmd: cmd, store: st}
}

// Routes registers the assertion endpoints (under a flow).
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/flows/{flow_id}/assertions", s.get)
	mux.HandleFunc("PUT /v1/flows/{flow_id}/assertions", s.set)
	mux.HandleFunc("POST /v1/flows/{flow_id}/assertions/run", s.run)
}

func (s *Service) get(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	v, found, err := Read(r.Context(), s.store, id, r.PathValue("flow_id"))
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		v = View{FlowID: r.PathValue("flow_id"), Cases: []Case{}}
	}
	if v.Cases == nil {
		v.Cases = []Case{}
	}
	httpx.JSON(w, http.StatusOK, v)
}

type setRequest struct {
	Cases []Case `json:"cases"`
}

func (s *Service) set(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var req setRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	e, err := s.cmd.SetCases(r.Context(), id, r.PathValue("flow_id"), req.Cases)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"event_id": e.ID, "seq": e.Seq})
}

type runRequest struct {
	Version int `json:"version,omitempty"`
}

func (s *Service) run(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var req runRequest
	_ = httpx.DecodeJSON(r, &req)
	flowID := r.PathValue("flow_id")
	rep, err := RunForFlow(r.Context(), s.store, id, flowID, req.Version)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusOK, rep)
}

// RunForFlow loads a flow's graph (version 0 = latest) and stored cases and runs
// them — shared by the run endpoint and the promotion gate.
func RunForFlow(ctx context.Context, s store.Store, id identity.Identity, flowID string, version int) (Report, error) {
	fv, found, err := flows.Read(ctx, s, id, flowID)
	if err != nil {
		return Report{}, err
	}
	if !found {
		return Report{}, fmt.Errorf("assertions: unknown flow %q", flowID)
	}
	graph, err := flows.GraphForVersion(fv, version)
	if err != nil {
		return Report{}, err
	}
	v, _, err := Read(ctx, s, id, flowID)
	if err != nil {
		return Report{}, err
	}
	return Run(graph, v.Cases), nil
}
