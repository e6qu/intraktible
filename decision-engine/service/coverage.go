// SPDX-License-Identifier: AGPL-3.0-or-later

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/http"
	"regexp"
	"sort"

	"github.com/e6qu/intraktible/decision-engine/domain"
	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/decision-engine/flows"
	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/identity"
)

const (
	defaultCoverageRuns = 200
	maxCoverageRuns     = 2000
)

type coverageNode struct {
	NodeID string          `json:"node_id"`
	Type   events.NodeType `json:"type"`
	Hits   int             `json:"hits"`
}

type coverageBranch struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Branch string `json:"branch"`
	Hits   int    `json:"hits"`
}

type coverageResponse struct {
	Runs         int              `json:"runs"`
	Fields       []string         `json:"fields"`
	Nodes        []coverageNode   `json:"nodes"`
	Branches     []coverageBranch `json:"branches"`
	Dispositions map[string]int   `json:"dispositions"`
	DeadNodes    []string         `json:"dead_nodes"`
	DeadBranches []coverageBranch `json:"dead_branches"`
}

// coverage is the red-team / reachability report: it generates a deterministic
// fan of synthetic inputs over the numeric fields the graph references, runs each
// through the pure engine, and tallies per-node hits, per-branch hits, and the
// disposition distribution. Nodes and branches with zero hits are reported as dead
// (unreachable under the sampled space). Pure and bounded (runs cap, deterministic seed).
//
//	POST /v1/flows/{flow_id}/coverage
//	{ "version": 2, "runs": 500 }
func (s *Service) coverage(w http.ResponseWriter, r *http.Request) {
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
		Version int `json:"version"`
		Runs    int `json:"runs"`
	}
	// An empty/optional body is fine; only a malformed one is a bad request.
	if r.ContentLength != 0 {
		if err := httpx.DecodeJSON(r, &req); err != nil {
			httpx.Error(w, http.StatusBadRequest, err)
			return
		}
	}
	graph, err := flows.GraphForVersion(fv, req.Version)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	if len(graph.Nodes) == 0 {
		httpx.Error(w, http.StatusBadRequest, fmt.Errorf("flow has no published graph to cover"))
		return
	}

	runs := req.Runs
	if runs <= 0 {
		runs = defaultCoverageRuns
	}
	if runs > maxCoverageRuns {
		runs = maxCoverageRuns
	}

	fields := discoverNumericFields(graph)
	rep := s.runCoverage(r.Context(), id, fv.Slug, graph, fields, runs)
	httpx.JSON(w, http.StatusOK, rep)
}

// runCoverage executes the synthetic fan and assembles the report. The disposition
// derivation reuses deriveDisposition (the same policy path decide uses).
func (s *Service) runCoverage(ctx context.Context, id identity.Identity, slug string, graph events.Graph, fields []string, runs int) coverageResponse {
	nodeHits := make(map[string]int, len(graph.Nodes))
	branchHits := make(map[string]int, len(graph.Edges))
	dispositions := map[string]int{"approve": 0, "decline": 0, "refer": 0}

	branchKey := func(e events.Edge) string { return e.From + "\x00" + e.To + "\x00" + e.Branch }

	for i := 0; i < runs; i++ {
		input := syntheticInput(fields, i)
		run := domain.Execute(graph, input)
		hit := make(map[string]bool, len(run.Results))
		// A branching node records its chosen branch in its output — that, not
		// "both endpoints ran", decides which edge was taken: in a converging
		// topology the untaken branch's target usually executes anyway via the
		// taken path, which would credit dead branches as covered.
		chosen := make(map[string]string)
		for _, res := range run.Results {
			if !hit[res.NodeID] {
				hit[res.NodeID] = true
				nodeHits[res.NodeID]++
			}
			var out struct {
				Branch string `json:"branch"`
			}
			if json.Unmarshal(res.Output, &out) == nil && out.Branch != "" {
				chosen[res.NodeID] = out.Branch
			}
		}
		// A branch edge is taken iff its source recorded choosing that branch —
		// an unhit or non-branching source yields "" and matches nothing, so a
		// mislabeled edge reads as dead instead of being credited by a heuristic.
		for _, e := range graph.Edges {
			if e.Branch != "" && chosen[e.From] == e.Branch {
				branchHits[branchKey(e)]++
			}
		}
		if run.Status != domain.StatusCompleted {
			continue
		}
		disp, err := s.deriveDisposition(ctx, id, slug, run.Output)
		if err != nil {
			continue
		}
		switch disp {
		case "approve", "decline", "refer":
			dispositions[string(disp)]++
		default:
			// No policy (or an unmapped disposition): bucket by the run's decision/approved
			// output field when present, else leave uncounted.
			if d := bucketByOutput(run.Output); d != "" {
				dispositions[d]++
			}
		}
	}

	nodes := make([]coverageNode, 0, len(graph.Nodes))
	var deadNodes []string
	for _, n := range graph.Nodes {
		h := nodeHits[n.ID]
		nodes = append(nodes, coverageNode{NodeID: n.ID, Type: n.Type, Hits: h})
		if h == 0 {
			deadNodes = append(deadNodes, n.ID)
		}
	}
	branches := make([]coverageBranch, 0)
	var deadBranches []coverageBranch
	for _, e := range graph.Edges {
		if e.Branch == "" {
			continue
		}
		h := branchHits[branchKey(e)]
		b := coverageBranch{From: e.From, To: e.To, Branch: e.Branch, Hits: h}
		branches = append(branches, b)
		if h == 0 {
			deadBranches = append(deadBranches, coverageBranch{From: e.From, To: e.To, Branch: e.Branch})
		}
	}
	if deadNodes == nil {
		deadNodes = []string{}
	}
	if deadBranches == nil {
		deadBranches = []coverageBranch{}
	}
	return coverageResponse{
		Runs: runs, Fields: fields, Nodes: nodes, Branches: branches,
		Dispositions: dispositions, DeadNodes: deadNodes, DeadBranches: deadBranches,
	}
}

// bucketByOutput maps a flow's own output decision field to a disposition bucket
// when no policy assigns one — a best-effort fallback so a policy-less flow still
// reports a distribution.
func bucketByOutput(output map[string]any) string {
	if output == nil {
		return ""
	}
	if v, ok := output["approved"].(bool); ok {
		if v {
			return "approve"
		}
		return "decline"
	}
	if v, ok := output["decision"].(string); ok {
		switch normalizeDecision(v) {
		case "approve", "approved", "accept":
			return "approve"
		case "decline", "declined", "reject", "rejected", "deny":
			return "decline"
		case "refer", "review", "manual":
			return "refer"
		}
	}
	return ""
}

func normalizeDecision(v string) string {
	out := make([]rune, 0, len(v))
	for _, r := range v {
		if r >= 'A' && r <= 'Z' {
			r += 'a' - 'A'
		}
		out = append(out, r)
	}
	return string(out)
}

// identRe matches bare identifiers (and namespaced ones via the leading segment)
// in node configs and branch labels — the heuristic source of candidate field names.
var identRe = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)

// reservedIdents are keywords/operators expr-lang produces that are not input
// fields, plus the engine-owned namespaces and common config keys — filtered out
// of the discovered field set so a synthetic run perturbs real input levers only.
var reservedIdents = map[string]bool{
	"true": true, "false": true, "nil": true, "null": true,
	"and": true, "or": true, "not": true, "in": true, "matches": true,
	"len": true, "all": true, "any": true, "filter": true, "map": true,
	// Engine-owned namespaces (pre-resolved context, not caller input).
	"features": true, "connect": true, "ai": true, "predict": true,
	// Common config keys (the structural JSON around the expressions).
	"assignments": true, "target": true, "expr": true, "condition": true,
	"fields": true, "rules": true, "when": true, "disposition": true,
	"connector": true, "output": true, "agent": true, "model": true,
	"code": true, "company_name": true, "case_type": true, "sla_days": true,
}

// discoverNumericFields scans node configs and edge branch labels for identifiers
// and returns the plausible numeric input field names, sorted. Heuristic: a bare
// top-level identifier that is not a reserved keyword, an engine namespace, or a
// namespace-qualified reference (e.g. `connect.bureau.score` — its leading segment
// is a namespace) is treated as a caller-supplied field a synthetic run can vary.
func discoverNumericFields(graph events.Graph) []string {
	set := map[string]bool{}
	add := func(text string) {
		// Drop namespaced references: only the bare leading identifiers of a path are
		// candidates, and engine namespaces are already reserved.
		for _, m := range identRe.FindAllStringIndex(text, -1) {
			ident := text[m[0]:m[1]]
			// A token immediately preceded by '.' is a sub-field of a path, not a top-level field.
			if m[0] > 0 && text[m[0]-1] == '.' {
				continue
			}
			if reservedIdents[ident] {
				continue
			}
			set[ident] = true
		}
	}
	for _, n := range graph.Nodes {
		if len(n.Config) > 0 {
			add(string(n.Config))
		}
	}
	for _, e := range graph.Edges {
		if e.Branch != "" {
			add(e.Branch)
		}
	}
	fields := make([]string, 0, len(set))
	for f := range set {
		fields = append(fields, f)
	}
	sort.Strings(fields)
	return fields
}

// syntheticInput builds a reproducible input for run index i: each discovered field
// gets a value over a plausible range, varied across runs by a deterministic hash of
// (field, i). No global RNG and no time seed, so the same index always yields the
// same input — the endpoint is reproducible.
func syntheticInput(fields []string, i int) map[string]any {
	input := make(map[string]any, len(fields))
	for _, f := range fields {
		input[f] = sampleValue(f, i)
	}
	return input
}

// sampleValue maps (field, run index) to a deterministic value spread across a wide
// range, so the fan sweeps both small and large magnitudes (exercising thresholds on
// either side). The hash mixes the field name so different fields vary independently.
func sampleValue(field string, i int) float64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(field))
	_, _ = h.Write([]byte{byte(i & 0xff), byte((i >> 8) & 0xff), byte((i >> 16) & 0xff), byte((i >> 24) & 0xff)})
	// Map the hash to [0,1), then spread over [0, 1_000_000] so small probabilities
	// (0..1) and large dollar-scale values both occur across the fan.
	frac := float64(h.Sum64()%1_000_000) / 1_000_000.0
	return frac * 1_000_000
}
