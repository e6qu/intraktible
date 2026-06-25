// SPDX-License-Identifier: AGPL-3.0-or-later

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"sort"

	"github.com/e6qu/intraktible/decision-engine/domain"
	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/decision-engine/flows"
	"github.com/e6qu/intraktible/decision-engine/history"
	"github.com/e6qu/intraktible/decision-engine/policy"
	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/identity"
)

// nodeStatsScan caps how many recent completed decisions a node-stats request
// aggregates — the read model has no field index, so the scan is bounded.
const nodeStatsScan = 500

// deriveDisposition assigns a disposition over a run's output by applying the
// flow's bound policy, reusing the exact lookup+Apply path the decide handler
// uses (policy.ActiveForFlow → ver.Spec.Apply). No policy bound → empty
// disposition; a policy that cannot evaluate refers (matching decide), so a
// re-run never fails on a policy problem. Only a store error is returned.
func (s *Service) deriveDisposition(ctx context.Context, id identity.Identity, slug string, output map[string]any) (policy.Disposition, error) {
	_, ver, ok, err := policy.ActiveForFlow(ctx, s.store, id, slug)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", nil
	}
	out, applyErr := ver.Spec.Apply(output)
	// A policy that can't evaluate this output falls back to a safe manual referral
	// rather than aborting the probe (structured so the nil return isn't masking err).
	disp := policy.Refer
	if applyErr == nil {
		disp = out.Disposition
	}
	return disp, nil
}

// publishedGraph returns the flow's published graph for the requested version (0
// = latest published). It is the same projection-sourced graph decide/backtest
// run, so a re-run matches the live decision path.
func publishedGraph(fv flows.FlowView, version int) (events.Graph, bool) {
	if len(fv.Versions) == 0 {
		return events.Graph{}, false
	}
	g, err := flows.GraphForVersion(fv, version)
	if err != nil {
		return events.Graph{}, false
	}
	return g, true
}

type nodeStat struct {
	NodeID string          `json:"node_id"`
	Type   events.NodeType `json:"type"`
	Count  int             `json:"count"`
	Pct    float64         `json:"pct"`
}

type nodeStatsResponse struct {
	Total        int            `json:"total"`
	Dispositions map[string]int `json:"dispositions"`
	Nodes        []nodeStat     `json:"nodes"`
}

// nodeStats is the traversal heatmap: across the flow's recent COMPLETED
// decisions, how many hit each node of its current published graph (a node is
// "hit" when it appears in a record's trace), plus the disposition tally.
//
//	GET /v1/flows/{flow_id}/node-stats?environment=production
func (s *Service) nodeStats(w http.ResponseWriter, r *http.Request) {
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
	graph, ok := publishedGraph(fv, 0)
	if !ok {
		// An unpublished flow has no graph to attribute hits to: zero stats, not an error.
		httpx.JSON(w, http.StatusOK, nodeStatsResponse{
			Dispositions: map[string]int{"approve": 0, "decline": 0, "refer": 0},
			Nodes:        []nodeStat{},
		})
		return
	}

	page, err := history.ListPage(r.Context(), s.store, id, history.Filter{
		Slug: fv.Slug, Environment: r.URL.Query().Get("environment"), Status: "completed", Limit: nodeStatsScan,
	})
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}

	httpx.JSON(w, http.StatusOK, aggregateNodeStats(graph, page.Records))
}

// aggregateNodeStats tallies node hits and the disposition distribution across a set
// of completed-decision records, attributing hits to the nodes of the current
// published graph. A record's node list may reference ids no longer in the graph
// (graph evolved since the decision ran) — those are counted internally but only the
// current graph's nodes are reported, so the per-node pct stays in [0,1]. Pure and
// bounded by the record/graph sizes the caller already capped.
func aggregateNodeStats(graph events.Graph, records []history.Record) nodeStatsResponse {
	counts := make(map[string]int, len(graph.Nodes))
	dispositions := map[string]int{"approve": 0, "decline": 0, "refer": 0}
	for _, rec := range records {
		seen := make(map[string]bool, len(rec.Nodes))
		for _, n := range rec.Nodes {
			if seen[n.NodeID] {
				continue
			}
			seen[n.NodeID] = true
			counts[n.NodeID]++
		}
		if _, tracked := dispositions[rec.Disposition]; tracked {
			dispositions[rec.Disposition]++
		}
	}

	total := len(records)
	nodes := make([]nodeStat, 0, len(graph.Nodes))
	for _, n := range graph.Nodes {
		c := counts[n.ID]
		// A node can be hit at most once per record (deduped above), so c <= total and
		// pct stays in [0,1]; total>0 guards the division.
		var pct float64
		if total > 0 {
			pct = float64(c) / float64(total)
		}
		nodes = append(nodes, nodeStat{NodeID: n.ID, Type: n.Type, Count: c, Pct: pct})
	}
	return nodeStatsResponse{Total: total, Dispositions: dispositions, Nodes: nodes}
}

// dispositionRank orders dispositions by favorability (higher is more favorable),
// so the counterfactual search only reports a change that strictly improves the
// outcome. An unknown/empty disposition is least favorable.
func dispositionRank(d policy.Disposition) int {
	switch d {
	case policy.Approve:
		return 2
	case policy.Refer:
		return 1
	case policy.Decline:
		return 0
	default:
		return -1
	}
}

// counterfactual caps. cfEvalsPerField bounds the binary search per numeric field;
// cfMaxEvals bounds the whole request so a wide input can't run unbounded.
const (
	cfEvalsPerField = 24
	cfMaxEvals      = 400
)

type flip struct {
	Field       string  `json:"field"`
	From        float64 `json:"from"`
	To          float64 `json:"to"`
	Direction   string  `json:"direction"`
	Disposition string  `json:"disposition"`
}

type counterfactualResponse struct {
	Disposition string `json:"disposition"`
	Flips       []flip `json:"flips"`
	Searched    int    `json:"searched"`
}

// counterfactual answers "what would flip this?": for an unfavorable decision, it
// searches each numeric input field for the minimal single-field change that
// re-runs to a more favorable disposition. It re-runs with domain.Execute then
// applies the same policy derivation the decide path uses, so a reported flip is
// a real disposition change. Pure and bounded (no recording, capped evals).
//
//	POST /v1/decisions/{id}/counterfactual
func (s *Service) counterfactual(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	rec, found, err := history.Read(r.Context(), s.store, id, r.PathValue("id"))
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		httpx.Error(w, http.StatusNotFound, fmt.Errorf("decision not found"))
		return
	}
	original := policy.Disposition(rec.Disposition)
	if original == policy.Approve {
		// Already favorable: nothing to flip.
		httpx.JSON(w, http.StatusOK, counterfactualResponse{Disposition: "approve", Flips: []flip{}, Searched: 0})
		return
	}

	fv, found, err := flows.Read(r.Context(), s.store, id, rec.FlowID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		httpx.Error(w, http.StatusNotFound, fmt.Errorf("flow not found"))
		return
	}
	graph, err := flows.GraphForVersion(fv, rec.Version)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}

	var data map[string]any
	if len(rec.Data) > 0 {
		if err := json.Unmarshal(rec.Data, &data); err != nil {
			httpx.Error(w, http.StatusBadRequest, fmt.Errorf("decision input is not a JSON object: %w", err))
			return
		}
	}
	if data == nil {
		data = map[string]any{}
	}
	// The recorded input already carries any pre-resolved engine namespaces
	// (features/connect/ai/predict). Re-running over the same map keeps those nodes
	// from failing, since the pure re-run has no provider to re-resolve them.

	searched := 0
	var flips []flip
	// A field the graph recomputes (an assignment/scorecard/model target) is not a lever
	// the applicant controls, so it must not be offered as a counterfactual.
	derived := graphTargets(graph)
	for _, field := range numericFields(data) {
		if derived[field] {
			continue
		}
		if searched >= cfMaxEvals {
			break
		}
		budget := cfMaxEvals - searched
		if budget > cfEvalsPerField {
			budget = cfEvalsPerField
		}
		f, used := s.searchFlip(r.Context(), id, fv.Slug, graph, data, field, original, budget)
		searched += used
		if f != nil {
			flips = append(flips, *f)
		}
	}

	// Smallest relative change first, so the closest-to-the-threshold lever leads.
	sort.SliceStable(flips, func(i, j int) bool {
		return relChange(flips[i]) < relChange(flips[j])
	})
	if flips == nil {
		flips = []flip{}
	}
	httpx.JSON(w, http.StatusOK, counterfactualResponse{
		Disposition: string(original), Flips: flips, Searched: searched,
	})
}

// relChange is a flip's magnitude relative to the original value, used to order
// flips by which lever moves least.
func relChange(f flip) float64 {
	denom := math.Abs(f.From)
	if denom == 0 {
		denom = 1
	}
	return math.Abs(f.To-f.From) / denom
}

// roundCF rounds a counterfactual target for display + degenerate detection: whole
// numbers for large magnitudes, two decimals otherwise.
func roundCF(v float64) float64 {
	if math.Abs(v) >= 100 {
		return math.Round(v)
	}
	return math.Round(v*100) / 100
}

var cfTargetRe = regexp.MustCompile(`"(?:target|output)"\s*:\s*"([^"]*)"`)

// graphTargets is the set of fields the graph assigns/derives (assignment/scorecard/model
// outputs); they must not be counterfactual levers since the graph recomputes them
// regardless of the supplied input.
func graphTargets(graph events.Graph) map[string]bool {
	out := map[string]bool{}
	for _, n := range graph.Nodes {
		for _, m := range cfTargetRe.FindAllStringSubmatch(string(n.Config), -1) {
			out[m[1]] = true
		}
	}
	return out
}

// searchFlip looks for the smallest single-field change to `field` that re-runs to
// a strictly more favorable disposition than `original`. It binary-searches both
// directions over [value/8 .. value*8] (with a small absolute floor so a near-zero
// value still has room to move) and returns the smaller |to-from| flip found, plus
// the number of evaluations used (bounded by budget).
func (s *Service) searchFlip(ctx context.Context, id identity.Identity, slug string, graph events.Graph, data map[string]any, field string, original policy.Disposition, budget int) (*flip, int) {
	val := data[field].(float64)
	// A non-finite field value has no meaningful threshold to search toward and would
	// poison the span/direction math (NaN comparisons, Inf±Inf), so it is not a lever.
	if math.IsNaN(val) || math.IsInf(val, 0) {
		return nil, 0
	}
	used := 0
	eval := func(x float64) (policy.Disposition, bool) {
		if used >= budget {
			return "", false
		}
		used++
		perturbed := make(map[string]any, len(data))
		for k, v := range data {
			perturbed[k] = v
		}
		perturbed[field] = x
		run := domain.Execute(graph, perturbed)
		if run.Status != domain.StatusCompleted {
			return "", true
		}
		disp, err := s.deriveDisposition(ctx, id, slug, run.Output)
		if err != nil {
			return "", false
		}
		return disp, true
	}

	originalRank := dispositionRank(original)
	better := func(d policy.Disposition) bool { return dispositionRank(d) > originalRank }

	// Bound the search to a sensible window — [val/4, val*4] for a positive field — so it
	// never suggests a negative income, a zero denominator, or a wild change. hi stays
	// finite for an extreme magnitude (val near MaxFloat64 would otherwise overflow).
	var lo, hi float64
	if val > 0 {
		lo = val * 0.125
		hi = val * 8
	} else {
		pad := math.Abs(val) * 8
		if pad < 25 {
			pad = 25
		}
		lo = math.Max(0, val-pad)
		hi = val + pad
	}
	if hi > math.MaxFloat64/2 {
		hi = math.MaxFloat64 / 2
	}

	up := boundFlip(eval, val, hi, better)
	down := boundFlip(eval, val, lo, better)

	best := closestFlip(val, up, down)
	if best == nil {
		return nil, used
	}
	to := roundCF(best.to)
	if to == roundCF(val) { // boundary sits on the current value — not a useful lever
		return nil, used
	}
	dir := "increase"
	if to < val {
		dir = "decrease"
	}
	return &flip{
		Field: field, From: val, To: to, Direction: dir, Disposition: string(best.disp),
	}, used
}

// flipPoint is a found favorable perturbation: the field value and the disposition it yields.
type flipPoint struct {
	to   float64
	disp policy.Disposition
}

// boundFlip binary-searches between `from` (assumed not better) and `to` for the
// value closest to `from` that evaluates to a strictly better disposition. It
// returns nil when `to` itself is not better (no flip in this direction) or the
// eval budget runs out.
func boundFlip(eval func(float64) (policy.Disposition, bool), from, to float64, better func(policy.Disposition) bool) *flipPoint {
	endDisp, ok := eval(to)
	if !ok || !better(endDisp) {
		return nil
	}
	lo, hi := from, to
	bestTo, bestDisp := to, endDisp
	// A fixed iteration count converges the threshold without unbounded evals; the
	// eval budget cuts it shorter if exhausted.
	for i := 0; i < 16; i++ {
		mid := (lo + hi) / 2
		disp, ok := eval(mid)
		if !ok {
			break
		}
		if better(disp) {
			bestTo, bestDisp = mid, disp
			hi = mid
		} else {
			lo = mid
		}
	}
	return &flipPoint{to: bestTo, disp: bestDisp}
}

// closestFlip returns whichever of two candidate flips moves the field least from val.
func closestFlip(val float64, a, b *flipPoint) *flipPoint {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	case math.Abs(a.to-val) <= math.Abs(b.to-val):
		return a
	default:
		return b
	}
}

// numericFields returns the sorted top-level numeric input field names (engine
// namespaces are skipped — they are pre-resolved context, not levers a caller controls).
func numericFields(data map[string]any) []string {
	var fields []string
	for k, v := range data {
		if isReservedNamespace(k) {
			continue
		}
		if _, ok := v.(float64); ok {
			fields = append(fields, k)
		}
	}
	sort.Strings(fields)
	return fields
}

func isReservedNamespace(k string) bool {
	switch k {
	case "features", "connect", "ai", "predict":
		return true
	default:
		return false
	}
}
