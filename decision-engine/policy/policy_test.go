// SPDX-License-Identifier: AGPL-3.0-or-later

package policy_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/decision-engine/policy"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/projection"
	"github.com/e6qu/intraktible/platform/testutil"
)

func spec() policy.Spec {
	return policy.Spec{
		Rules: []policy.Rule{
			{When: "score >= 0.85", Disposition: policy.Approve, Code: "P-AUTO", Description: "high score"},
			{When: "score <= 0.30", Disposition: policy.Decline, Code: "P-LOW"},
		},
		Default: policy.Refer,
	}
}

func TestApplyFirstMatchWins(t *testing.T) {
	s := spec()
	cases := []struct {
		score float64
		want  policy.Disposition
		code  string
	}{
		{0.9, policy.Approve, "P-AUTO"},
		{0.2, policy.Decline, "P-LOW"},
		{0.5, policy.Refer, ""},
	}
	for _, c := range cases {
		out, err := s.Apply(map[string]any{"score": c.score})
		if err != nil {
			t.Fatalf("score %v: %v", c.score, err)
		}
		if out.Disposition != c.want || out.Code != c.code {
			t.Fatalf("score %v → %+v, want %s/%s", c.score, out, c.want, c.code)
		}
	}
}

func TestApplyDefaultsToReferWhenUnset(t *testing.T) {
	s := policy.Spec{Rules: []policy.Rule{{When: "x > 0", Disposition: policy.Approve}}}
	out, err := s.Apply(map[string]any{"x": -1})
	if err != nil || out.Disposition != policy.Refer {
		t.Fatalf("empty default should refer; got %+v err %v", out, err)
	}
}

func TestApplyFailsLoudlyOnBadCondition(t *testing.T) {
	// A condition referencing a missing field errors rather than silently passing.
	s := policy.Spec{Rules: []policy.Rule{{When: "missing >= 1", Disposition: policy.Approve}}}
	if _, err := s.Apply(map[string]any{"score": 1.0}); err == nil {
		t.Fatal("expected an evaluation error for a missing field")
	}
}

func TestValidate(t *testing.T) {
	if err := spec().Validate(); err != nil {
		t.Fatalf("valid spec rejected: %v", err)
	}
	bad := []policy.Spec{
		{Rules: []policy.Rule{{When: "score > 1", Disposition: "maybe"}}},      // bad disposition
		{Rules: []policy.Rule{{When: "", Disposition: policy.Approve}}},        // empty condition
		{Rules: []policy.Rule{{When: "score >", Disposition: policy.Approve}}}, // unparseable
		{Default: "nope"}, // bad default
	}
	for i, s := range bad {
		if err := s.Validate(); err == nil {
			t.Fatalf("bad spec %d passed validation", i)
		}
	}
}

func TestEtagStableAndContentSensitive(t *testing.T) {
	a, _ := policy.Etag(spec())
	b, _ := policy.Etag(spec())
	if a == "" || a != b {
		t.Fatalf("etag not stable: %q vs %q", a, b)
	}
	s2 := spec()
	s2.Default = policy.Decline
	c, _ := policy.Etag(s2)
	if c == a {
		t.Fatal("etag should change with the spec")
	}
}

// scoreGraph is a flow that passes its input `score` through to the output.
func scoreGraph() events.Graph {
	cfg := func(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
	return events.Graph{
		Nodes: []events.Node{
			{ID: "in", Type: events.NodeInput},
			{ID: "a", Type: events.NodeAssignment, Config: cfg(map[string]any{"assignments": []map[string]any{{"target": "score", "expr": "score"}}})},
			{ID: "out", Type: events.NodeOutput, Config: cfg(map[string]any{"fields": []string{"score"}})},
		},
		Edges: []events.Edge{{From: "in", To: "a"}, {From: "a", To: "out"}},
	}
}

func TestBacktestDistributionAndFlips(t *testing.T) {
	g := scoreGraph()
	ds := []map[string]any{{"score": 0.9}, {"score": 0.5}, {"score": 0.95}}
	strict := policy.Spec{Rules: []policy.Rule{{When: "score >= 0.85", Disposition: policy.Approve}}, Default: policy.Refer}

	// No-compare: 2 approve (0.9, 0.95), 1 refer (0.5).
	rep := policy.Backtest(g, ds, strict, nil)
	if rep.Summary.Total != 3 || rep.Summary.Evaluated.Approve != 2 || rep.Summary.Evaluated.Refer != 1 {
		t.Fatalf("unexpected distribution: %+v", rep.Summary)
	}
	if rep.Summary.Compare != nil {
		t.Fatal("compare should be nil without a compare spec")
	}

	// Compare a looser cutoff against the strict one: the 0.5 row flips to approve.
	loose := policy.Spec{Rules: []policy.Rule{{When: "score >= 0.4", Disposition: policy.Approve}}, Default: policy.Refer}
	cmp := policy.Backtest(g, ds, loose, &strict)
	if cmp.Summary.Evaluated.Approve != 3 || cmp.Summary.Compare.Approve != 2 || cmp.Summary.Flipped != 1 {
		t.Fatalf("unexpected compare summary: %+v", cmp.Summary)
	}
	if len(cmp.Flips) != 1 || cmp.Flips[0].Evaluated != policy.Approve || cmp.Flips[0].Compare != policy.Refer {
		t.Fatalf("unexpected flips: %+v", cmp.Flips)
	}
}

// TestPolicyLifecycle exercises command → log → projection → ActiveForFlow.
func TestPolicyLifecycle(t *testing.T) {
	log, st := testutil.NewLogStore(t)
	h := policy.NewHandler(log)
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "author"}
	ctx := context.Background()

	policyID, _, err := h.CreatePolicy(ctx, id, "credit-stp", "credit-risk")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := h.PublishVersion(ctx, id, policyID, spec()); err != nil {
		t.Fatal(err)
	}
	// Publishing to an unknown policy fails loudly.
	if _, _, _, err := h.PublishVersion(ctx, id, "nope", spec()); err == nil {
		t.Fatal("expected unknown-policy error")
	}

	if _, err := projection.New(log, st, policy.Projector{}).RebuildTo(ctx, 0); err != nil {
		t.Fatal(err)
	}
	pv, ver, ok, err := policy.ActiveForFlow(ctx, st, id, "credit-risk")
	if err != nil || !ok {
		t.Fatalf("active policy not found: ok=%v err=%v", ok, err)
	}
	if pv.PolicyID != policyID || ver.Version != 1 || len(ver.Spec.Rules) != 2 {
		t.Fatalf("unexpected active policy: %+v / %+v", pv, ver)
	}
	if _, _, ok, _ := policy.ActiveForFlow(ctx, st, id, "no-such-flow"); ok {
		t.Fatal("expected no active policy for an unbound flow")
	}
}
