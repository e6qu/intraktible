// SPDX-License-Identifier: AGPL-3.0-or-later

package policy

import (
	"fmt"
	"net/http"

	"github.com/e6qu/intraktible/decision-engine/flows"
	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/store"
)

// maxBacktest caps a disposition-backtest dataset (records nothing, so it can be
// larger than a batch decide).
const maxBacktest = 2000

// Service is the policy HTTP surface (imperative shell): author + read policies.
type Service struct {
	cmd   *Handler
	store store.Store
}

// New wires the policy command write side and the policies read model to HTTP.
func New(cmd *Handler, st store.Store) *Service {
	return &Service{cmd: cmd, store: st}
}

// Routes registers the policy endpoints on the API mux.
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/policies", s.create)
	mux.HandleFunc("GET /v1/policies", s.list)
	mux.HandleFunc("GET /v1/policies/{policy_id}", s.get)
	mux.HandleFunc("POST /v1/policies/{policy_id}/versions", s.publish)
	mux.HandleFunc("POST /v1/policies/{policy_id}/backtest", s.backtest)
}

type backtestRequest struct {
	FlowVersion    int              `json:"flow_version,omitempty"`
	CompareVersion int              `json:"compare_version,omitempty"`
	Spec           *Spec            `json:"spec,omitempty"` // inline draft; defaults to latest published
	Dataset        []map[string]any `json:"dataset"`
}

// backtest replays a dataset through the policy's bound flow and disposes each
// output — previewing the disposition distribution (and, with compare_version,
// how it shifts vs another policy version) without recording anything. The
// evaluated policy is the inline `spec` (the unpublished draft) or the latest
// published version.
func (s *Service) backtest(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var req backtestRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	if len(req.Dataset) == 0 {
		httpx.Error(w, http.StatusBadRequest, fmt.Errorf("policy backtest: dataset is empty"))
		return
	}
	if len(req.Dataset) > maxBacktest {
		httpx.Error(w, http.StatusBadRequest, fmt.Errorf("policy backtest: dataset too large (%d > %d)", len(req.Dataset), maxBacktest))
		return
	}

	pv, found, err := Read(r.Context(), s.store, id, r.PathValue("policy_id"))
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		httpx.Error(w, http.StatusNotFound, fmt.Errorf("policy not found"))
		return
	}

	// The evaluated spec: the inline draft, else the latest published version.
	var evaluated Spec
	switch {
	case req.Spec != nil:
		if err := req.Spec.Validate(); err != nil {
			httpx.Error(w, http.StatusBadRequest, err)
			return
		}
		evaluated = *req.Spec
	case len(pv.Versions) > 0:
		evaluated = latestVersion(pv).Spec
	default:
		httpx.Error(w, http.StatusBadRequest, fmt.Errorf("policy backtest: no spec to evaluate (policy has no published version; send a draft spec)"))
		return
	}

	// Optional compare baseline: another published version.
	var compare *Spec
	if req.CompareVersion > 0 {
		for i := range pv.Versions {
			if pv.Versions[i].Version == req.CompareVersion {
				compare = &pv.Versions[i].Spec
				break
			}
		}
		if compare == nil {
			httpx.Error(w, http.StatusBadRequest, fmt.Errorf("policy backtest: no version %d", req.CompareVersion))
			return
		}
	}

	// Resolve the bound flow's graph (at flow_version, else its latest).
	fv, ok2, err := flows.BySlug(r.Context(), s.store, id, pv.FlowSlug)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	if !ok2 || len(fv.Versions) == 0 {
		httpx.Error(w, http.StatusBadRequest, fmt.Errorf("policy backtest: bound flow %q has no published version", pv.FlowSlug))
		return
	}
	want := fv.Latest
	if req.FlowVersion > 0 {
		want = req.FlowVersion
	}
	graph := fv.Versions[len(fv.Versions)-1].Graph
	matched := false
	for i := range fv.Versions {
		if fv.Versions[i].Version == want {
			graph, matched = fv.Versions[i].Graph, true
			break
		}
	}
	if !matched {
		httpx.Error(w, http.StatusBadRequest, fmt.Errorf("policy backtest: flow %q has no version %d", pv.FlowSlug, want))
		return
	}

	httpx.JSON(w, http.StatusOK, Backtest(graph, req.Dataset, evaluated, compare))
}

type createRequest struct {
	Name     string `json:"name"`
	FlowSlug string `json:"flow_slug"`
}

func (s *Service) create(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var req createRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	policyID, e, err := s.cmd.CreatePolicy(r.Context(), id, req.Name, req.FlowSlug)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"policy_id": policyID, "event_id": e.ID, "seq": e.Seq})
}

type publishRequest struct {
	Spec Spec `json:"spec"`
}

func (s *Service) publish(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var req publishRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	version, etag, e, err := s.cmd.PublishVersion(r.Context(), id, r.PathValue("policy_id"), req.Spec)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{
		"version": version, "etag": etag, "event_id": e.ID, "seq": e.Seq,
	})
}

func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	pvs, err := List(r.Context(), s.store, id)
	httpx.WriteList(w, "policies", pvs, err)
}

func (s *Service) get(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	pv, found, err := Read(r.Context(), s.store, id, r.PathValue("policy_id"))
	httpx.WriteOne(w, pv, found, err, "policy not found")
}
