// SPDX-License-Identifier: AGPL-3.0-or-later

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/e6qu/intraktible/decision-engine/backtest"
	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/decision-engine/flows"
	"github.com/e6qu/intraktible/decision-engine/history"
	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

const (
	maxBacktestRecords = 2000 // largest dataset a single backtest accepts
	maxReturnedRecords = 200  // cap on per-record results in the response (summary is exact)
)

// backtestFlow replays a dataset of inputs through a flow version — and optionally
// compares it to another version — using the pure engine. It records no decision
// and performs no I/O: a safe pre-deploy confidence check. The dataset is either
// supplied inline, or (with from_recorded) sourced from this flow's previously
// RECORDED decisions in the event-sourced history, so a draft can be replayed against
// real production traffic.
//
//	POST /v1/flows/{flow_id}/backtest
//	{ "version": 2, "compare_version": 1, "dataset": [ {…}, {…} ] }
//	{ "version": 2, "compare_version": 1, "from_recorded": true, "environment": "production" }
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
		FromRecorded   bool             `json:"from_recorded"` // source the dataset from recorded decisions
		Environment    string           `json:"environment"`   // optional env filter when from_recorded
		Limit          int              `json:"limit"`         // optional cap when from_recorded
	}
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	dataset := req.Dataset
	if req.FromRecorded {
		dataset, err = recordedDataset(r.Context(), s.store, id, fv.Slug, req.Environment, req.Limit)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, err)
			return
		}
		if len(dataset) == 0 {
			httpx.Error(w, http.StatusBadRequest, fmt.Errorf("no recorded decisions to replay for this flow%s", envSuffix(req.Environment)))
			return
		}
	}
	if len(dataset) == 0 {
		httpx.Error(w, http.StatusBadRequest, fmt.Errorf("dataset is required (a non-empty array of input objects), or set from_recorded"))
		return
	}
	if len(dataset) > maxBacktestRecords {
		httpx.Error(w, http.StatusBadRequest, fmt.Errorf("dataset too large: %d (max %d)", len(dataset), maxBacktestRecords))
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

	rep := backtest.Run(baseline, candidate, dataset)
	rep.Records = sampleRecords(rep.Records, candidate != nil)
	httpx.JSON(w, http.StatusOK, rep)
}

// recordedDataset sources a backtest dataset from this flow's previously recorded
// decisions (the event-sourced history read model), newest-first, so a draft version
// can be replayed against the inputs that actually hit production. It reads each
// recorded decision's captured input; an input that won't parse is skipped rather than
// failing the whole backtest.
func recordedDataset(ctx context.Context, s store.Store, id identity.Identity, slug, env string, limit int) ([]map[string]any, error) {
	if limit <= 0 || limit > maxBacktestRecords {
		limit = maxBacktestRecords
	}
	page, err := history.ListPage(ctx, s, id, history.Filter{
		Slug: slug, Environment: env, Status: "completed", Limit: limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(page.Records))
	for _, rec := range page.Records {
		if len(rec.Data) == 0 {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(rec.Data, &m); err != nil {
			continue
		}
		out = append(out, m)
	}
	return out, nil
}

func envSuffix(env string) string {
	if env == "" {
		return ""
	}
	return fmt.Sprintf(" in %q", env)
}

// whatifFlow runs a sensitivity analysis: it sweeps one input field across a set
// of values and reports how the flow's outcome shifts. Like backtest it uses the
// pure engine, records no decision, and performs no I/O.
//
//	POST /v1/flows/{flow_id}/whatif
//	{ "version": 2, "base": {…}, "field": "score", "values": [600, 650, 700] }
func (s *Service) whatifFlow(w http.ResponseWriter, r *http.Request) {
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
		Version int            `json:"version"`
		Base    map[string]any `json:"base"`
		Field   string         `json:"field"`
		Values  []any          `json:"values"`
	}
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	if req.Field == "" {
		httpx.Error(w, http.StatusBadRequest, fmt.Errorf("field is required (the input field to sweep)"))
		return
	}
	if len(req.Values) == 0 {
		httpx.Error(w, http.StatusBadRequest, fmt.Errorf("values is required (a non-empty array to sweep the field over)"))
		return
	}
	if len(req.Values) > maxBacktestRecords {
		httpx.Error(w, http.StatusBadRequest, fmt.Errorf("too many values: %d (max %d)", len(req.Values), maxBacktestRecords))
		return
	}
	graph, err := flows.GraphForVersion(fv, req.Version)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusOK, backtest.Sweep(graph, req.Base, req.Field, req.Values))
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
