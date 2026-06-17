// SPDX-License-Identifier: AGPL-3.0-or-later

package service

import (
	"fmt"
	"net/http"

	"github.com/e6qu/intraktible/decision-engine/backtest"
	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/decision-engine/flows"
	"github.com/e6qu/intraktible/platform/httpx"
)

const (
	maxBacktestRecords = 2000 // largest dataset a single backtest accepts
	maxReturnedRecords = 200  // cap on per-record results in the response (summary is exact)
)

// backtestFlow replays a dataset of inputs through a flow version — and optionally
// compares it to another version — using the pure engine. It records no decision
// and performs no I/O: a safe pre-deploy confidence check.
//
//	POST /v1/flows/{flow_id}/backtest
//	{ "version": 2, "compare_version": 1, "dataset": [ {…}, {…} ] }
func (s *Service) backtestFlow(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	fv, found, err := flows.Read(r.Context(), s.store, id, r.PathValue("flow_id"))
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		httpx.Error(w, http.StatusNotFound, fmt.Errorf("flow not found"))
		return
	}
	var req struct {
		Version        int              `json:"version"`
		CompareVersion int              `json:"compare_version"`
		Dataset        []map[string]any `json:"dataset"`
	}
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	if len(req.Dataset) == 0 {
		httpx.Error(w, http.StatusBadRequest, fmt.Errorf("dataset is required (a non-empty array of input objects)"))
		return
	}
	if len(req.Dataset) > maxBacktestRecords {
		httpx.Error(w, http.StatusBadRequest, fmt.Errorf("dataset too large: %d (max %d)", len(req.Dataset), maxBacktestRecords))
		return
	}

	baseline, err := flows.GraphForVersion(fv, req.Version)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	var candidate *events.Graph
	if req.CompareVersion != 0 {
		c, err := flows.GraphForVersion(fv, req.CompareVersion)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, err)
			return
		}
		candidate = &c
	}

	rep := backtest.Run(baseline, candidate, req.Dataset)
	rep.Records = sampleRecords(rep.Records, candidate != nil)
	httpx.JSON(w, http.StatusOK, rep)
}

// sampleRecords caps the per-record results: in compare mode it returns the
// changed records first (the ones worth inspecting); the summary is always exact.
func sampleRecords(recs []backtest.RecordResult, compare bool) []backtest.RecordResult {
	if compare {
		changed := make([]backtest.RecordResult, 0)
		for _, rec := range recs {
			if rec.Changed {
				changed = append(changed, rec)
			}
		}
		recs = changed
	}
	if len(recs) > maxReturnedRecords {
		recs = recs[:maxReturnedRecords]
	}
	return recs
}
