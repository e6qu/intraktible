// SPDX-License-Identifier: AGPL-3.0-or-later

// Package fairlending computes a disparate-impact report over a flow's recorded
// decisions: the adverse-impact ratio (AIR) of favorable-outcome rates across the
// values of a protected-class attribute, evaluated against the four-fifths rule
// (ECOA / Reg B). It is a read-only aggregation over the decision-history read
// model — no new events, no I/O — so it reflects the decisions actually recorded.
//
// The analyst supplies which input field encodes the protected class and which
// disposition counts as favorable; the system does not infer a protected class.
// A decision is scored only when it completed with a disposition of the favorable
// value or "decline". Decisions that were referred to a human, never reached a
// disposition, or lack the attribute in their input are excluded and counted in
// Excluded — the report states what it left out rather than folding it in.
package fairlending

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/e6qu/intraktible/decision-engine/history"
	"github.com/e6qu/intraktible/decision-engine/policy"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// FourFifths is the adverse-impact-ratio threshold below which a group is flagged
// (the "four-fifths rule": a selection rate under 80% of the reference group's is
// the conventional trigger for a disparate-impact review).
const FourFifths = 0.8

// SmallSampleN is the group size below which the AIR is statistically weak; groups
// under it are marked so the reader does not over-read a ratio from a few decisions.
const SmallSampleN = 30

// Params configures a disparate-impact analysis. FlowID and Attribute are required.
type Params struct {
	FlowID      string             // the flow whose decisions are analyzed
	Attribute   string             // dot-path into the decision input naming the protected class (e.g. "applicant.gender")
	Favorable   policy.Disposition // the disposition counted as favorable; defaults to approve
	Environment string             // optional: restrict to one environment
}

// Group is one protected-class value's outcome tally and its AIR.
type Group struct {
	Value       string  `json:"value"`
	Total       int     `json:"total"` // scored decisions (favorable + adverse)
	Favorable   int     `json:"favorable"`
	Adverse     int     `json:"adverse"`
	Rate        float64 `json:"rate"` // favorable / total
	AIR         float64 `json:"air"`  // rate / reference rate
	Reference   bool    `json:"reference,omitempty"`
	Flagged     bool    `json:"flagged,omitempty"`      // AIR < FourFifths and not the reference
	SmallSample bool    `json:"small_sample,omitempty"` // Total < SmallSampleN
}

// Report is the disparate-impact analysis over one flow.
type Report struct {
	GeneratedAt time.Time          `json:"generated_at"`
	Org         string             `json:"org"`
	Workspace   string             `json:"workspace"`
	FlowID      string             `json:"flow_id"`
	Attribute   string             `json:"attribute"`
	Favorable   policy.Disposition `json:"favorable"`
	Environment string             `json:"environment,omitempty"`
	Groups      []Group            `json:"groups"`
	Reference   string             `json:"reference"`  // value of the highest-rate group (the AIR denominator)
	MinAIR      float64            `json:"min_air"`    // lowest AIR across the groups
	Passes      bool               `json:"passes"`     // four-fifths rule holds across all groups
	Decisions   int                `json:"decisions"`  // scored decisions folded into the groups
	Excluded    int                `json:"excluded"`   // completed decisions dropped (referred, no disposition, or attribute absent)
	Groups2Plus bool               `json:"two_groups"` // at least two protected-class values were present
}

// Build aggregates the flow's recorded decisions into a disparate-impact report.
// now is injected for a deterministic generated-at stamp.
func Build(ctx context.Context, s store.Store, id identity.Identity, p Params, now time.Time) (Report, error) {
	if p.FlowID == "" {
		return Report{}, fmt.Errorf("fairlending: flow is required")
	}
	if p.Attribute == "" {
		return Report{}, fmt.Errorf("fairlending: attribute is required")
	}
	fav := p.Favorable
	if fav == "" {
		fav = policy.Approve
	}

	recs, err := history.List(ctx, s, id)
	if err != nil {
		return Report{}, err
	}

	tally := map[string]*Group{}
	excluded := 0
	for _, r := range recs {
		if r.FlowID != p.FlowID || r.Status != "completed" {
			continue
		}
		if p.Environment != "" && r.Environment != p.Environment {
			continue
		}
		disp := policy.Disposition(r.Disposition)
		// Only a terminal favorable/decline decision is scored. Referred and
		// disposition-less runs are real but not a favorable/adverse outcome, so
		// they are excluded and reported, not silently treated as adverse.
		if disp != fav && disp != policy.Decline {
			excluded++
			continue
		}
		val, ok := valueAt(r.Data, p.Attribute)
		if !ok {
			excluded++
			continue
		}
		g := tally[val]
		if g == nil {
			g = &Group{Value: val}
			tally[val] = g
		}
		if disp == fav {
			g.Favorable++
		} else {
			g.Adverse++
		}
	}

	rep := Report{
		GeneratedAt: now, Org: id.Org, Workspace: id.Workspace,
		FlowID: p.FlowID, Attribute: p.Attribute, Favorable: fav, Environment: p.Environment,
		Excluded: excluded,
	}
	rep.Groups, rep.Reference, rep.MinAIR, rep.Passes = compute(tally)
	for _, g := range rep.Groups {
		rep.Decisions += g.Total
	}
	rep.Groups2Plus = len(rep.Groups) >= 2
	return rep, nil
}

// compute is the pure AIR calculation over per-value tallies: it fills each group's
// rate/AIR/flags, picks the reference (highest-rate) group as the AIR denominator,
// and applies the four-fifths rule. Returned groups are ordered most-impacted first
// (lowest AIR), so the reader sees the worst disparity at the top.
func compute(tally map[string]*Group) (groups []Group, reference string, minAIR float64, passes bool) {
	for _, g := range tally {
		g.Total = g.Favorable + g.Adverse
		if g.Total > 0 {
			g.Rate = float64(g.Favorable) / float64(g.Total)
		}
		g.SmallSample = g.Total < SmallSampleN
	}

	// Reference = the highest selection rate (ties broken by the larger sample, then
	// the value) so the denominator is deterministic across calls.
	var ref *Group
	for _, g := range tally {
		if ref == nil || g.Rate > ref.Rate ||
			(g.Rate == ref.Rate && g.Total > ref.Total) ||
			(g.Rate == ref.Rate && g.Total == ref.Total && g.Value < ref.Value) {
			ref = g
		}
	}

	minAIR = 1
	passes = true
	for _, g := range tally {
		switch {
		case ref == nil || ref.Rate == 0:
			// No group has any favorable outcome (or no groups at all): there is no
			// selection-rate disparity to measure. AIR is 1 by convention.
			g.AIR = 1
		default:
			g.AIR = g.Rate / ref.Rate
		}
		if ref != nil && g.Value == ref.Value {
			g.Reference = true
		}
		if !g.Reference && g.AIR < FourFifths {
			g.Flagged = true
			passes = false
		}
		if g.AIR < minAIR {
			minAIR = g.AIR
		}
		groups = append(groups, *g)
	}
	if ref != nil {
		reference = ref.Value
	}
	// A four-fifths test needs at least two groups to compare; with fewer, there is
	// nothing to flag but the result is not evidence of no disparate impact.
	if len(groups) < 2 {
		passes = true
	}

	sort.Slice(groups, func(i, j int) bool {
		if groups[i].AIR != groups[j].AIR {
			return groups[i].AIR < groups[j].AIR
		}
		return groups[i].Value < groups[j].Value
	})
	return groups, reference, minAIR, passes
}

// valueAt reads the scalar at a dot-path in a decision's recorded input and returns
// its string form. It returns ok=false when the path is absent, traverses a
// non-object, or lands on a non-scalar (array/object) — those decisions cannot be
// grouped by the attribute and are excluded rather than coerced.
func valueAt(data json.RawMessage, path string) (string, bool) {
	if len(data) == 0 || path == "" {
		return "", false
	}
	var cur any
	if err := json.Unmarshal(data, &cur); err != nil {
		return "", false
	}
	seg := ""
	for _, part := range splitPath(path) {
		seg = part
		m, ok := cur.(map[string]any)
		if !ok {
			return "", false
		}
		cur, ok = m[part]
		if !ok {
			return "", false
		}
	}
	_ = seg
	switch v := cur.(type) {
	case string:
		return v, true
	case bool:
		return strconv.FormatBool(v), true
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), true
	case json.Number:
		return v.String(), true
	default:
		return "", false
	}
}

// splitPath splits a dot-path into its segments, skipping empty segments so a
// stray leading/trailing/double dot does not produce an unmatchable "" key.
func splitPath(path string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(path); i++ {
		if i == len(path) || path[i] == '.' {
			if i > start {
				out = append(out, path[start:i])
			}
			start = i + 1
		}
	}
	return out
}
