// SPDX-License-Identifier: AGPL-3.0-or-later

package fairlending_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/intraktible/decision-engine/history"
	"github.com/e6qu/intraktible/decision-engine/policy"
	"github.com/e6qu/intraktible/fairlending"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

var (
	ctx = context.Background()
	id  = identity.Identity{Org: "demo", Workspace: "main", Actor: "tester"}
	now = time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
)

// seed appends n completed decisions for a flow, each with the given nested
// attribute value and disposition, so a test can build a known population.
func seed(t *testing.T, st store.Store, flowID, env, attrVal string, disp policy.Disposition, n int, tag string) {
	t.Helper()
	for i := 0; i < n; i++ {
		did := fmt.Sprintf("%s-%s-%d", flowID, tag, i)
		data, _ := json.Marshal(map[string]any{"applicant": map[string]any{"gender": attrVal}})
		rec := history.Record{
			Org: id.Org, Workspace: id.Workspace, DecisionID: did,
			FlowID: flowID, Environment: env, Status: "completed",
			Data: data, Disposition: string(disp), StartedAt: now,
		}
		if err := store.PutDoc(ctx, st, history.Collection, store.Key(id.Org, id.Workspace, did), rec); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBuildFlagsDisparateImpact(t *testing.T) {
	st := store.NewMemory()
	// male: 80/100 approved (rate .80); female: 50/100 approved (rate .50).
	seed(t, st, "f1", "production", "male", policy.Approve, 80, "ma")
	seed(t, st, "f1", "production", "male", policy.Decline, 20, "md")
	seed(t, st, "f1", "production", "female", policy.Approve, 50, "fa")
	seed(t, st, "f1", "production", "female", policy.Decline, 50, "fd")

	rep, err := fairlending.Build(ctx, st, id, fairlending.Params{FlowID: "f1", Attribute: "applicant.gender"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Decisions != 200 || rep.Excluded != 0 {
		t.Fatalf("decisions=%d excluded=%d", rep.Decisions, rep.Excluded)
	}
	if rep.Reference != "male" {
		t.Fatalf("reference = %q, want male", rep.Reference)
	}
	if rep.Passes {
		t.Fatal("expected four-fifths rule to fail")
	}
	// Groups are ordered most-impacted first, so female (AIR 0.625) leads.
	if len(rep.Groups) != 2 || rep.Groups[0].Value != "female" {
		t.Fatalf("groups = %+v", rep.Groups)
	}
	f := rep.Groups[0]
	if !f.Flagged || f.Reference {
		t.Fatalf("female group flags = %+v", f)
	}
	if air := f.AIR; air < 0.62 || air > 0.63 {
		t.Fatalf("female AIR = %v, want ~0.625", air)
	}
	if m := rep.Groups[1]; !m.Reference || m.Flagged || m.AIR != 1 {
		t.Fatalf("male group = %+v", m)
	}
	if rep.MinAIR < 0.62 || rep.MinAIR > 0.63 {
		t.Fatalf("min AIR = %v", rep.MinAIR)
	}
}

func TestBuildPassesWhenWithinFourFifths(t *testing.T) {
	st := store.NewMemory()
	seed(t, st, "f1", "", "male", policy.Approve, 80, "ma")
	seed(t, st, "f1", "", "male", policy.Decline, 20, "md")
	seed(t, st, "f1", "", "female", policy.Approve, 70, "fa") // rate .70, AIR .875
	seed(t, st, "f1", "", "female", policy.Decline, 30, "fd")

	rep, err := fairlending.Build(ctx, st, id, fairlending.Params{FlowID: "f1", Attribute: "applicant.gender"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Passes || !rep.Groups2Plus {
		t.Fatalf("expected pass with two groups: passes=%v two=%v", rep.Passes, rep.Groups2Plus)
	}
	for _, g := range rep.Groups {
		if g.Flagged {
			t.Fatalf("no group should be flagged: %+v", g)
		}
	}
}

func TestBuildExcludesReferredMissingAndOtherFlows(t *testing.T) {
	st := store.NewMemory()
	seed(t, st, "f1", "", "male", policy.Approve, 5, "ma")
	seed(t, st, "f1", "", "male", policy.Refer, 4, "mr")  // referred → excluded
	seed(t, st, "f2", "", "male", policy.Approve, 9, "x") // other flow → ignored entirely

	// A completed decision whose input lacks the attribute → excluded, not grouped.
	noAttr, _ := json.Marshal(map[string]any{"applicant": map[string]any{"age": 40}})
	rec := history.Record{
		Org: id.Org, Workspace: id.Workspace, DecisionID: "f1-noattr",
		FlowID: "f1", Status: "completed", Data: noAttr, Disposition: string(policy.Approve), StartedAt: now,
	}
	if err := store.PutDoc(ctx, st, history.Collection, store.Key(id.Org, id.Workspace, "f1-noattr"), rec); err != nil {
		t.Fatal(err)
	}

	rep, err := fairlending.Build(ctx, st, id, fairlending.Params{FlowID: "f1", Attribute: "applicant.gender"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Decisions != 5 {
		t.Fatalf("scored = %d, want 5", rep.Decisions)
	}
	if rep.Excluded != 5 { // 4 referred + 1 missing attribute
		t.Fatalf("excluded = %d, want 5", rep.Excluded)
	}
	if rep.Groups2Plus {
		t.Fatal("only one group present; two_groups should be false")
	}
	if !rep.Passes {
		t.Fatal("single group cannot fail four-fifths (nothing to compare)")
	}
}

func TestBuildEnvironmentFilter(t *testing.T) {
	st := store.NewMemory()
	seed(t, st, "f1", "production", "male", policy.Approve, 10, "p")
	seed(t, st, "f1", "sandbox", "male", policy.Approve, 3, "s")

	rep, err := fairlending.Build(ctx, st, id,
		fairlending.Params{FlowID: "f1", Attribute: "applicant.gender", Environment: "production"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Decisions != 10 {
		t.Fatalf("scored = %d, want 10 (sandbox excluded)", rep.Decisions)
	}
}

func TestBuildCustomFavorableOutcome(t *testing.T) {
	st := store.NewMemory()
	// Favorable = "refer" (e.g. a flow where referral is the sought outcome).
	seed(t, st, "f1", "", "male", policy.Refer, 8, "mr")
	seed(t, st, "f1", "", "male", policy.Decline, 2, "md")
	rep, err := fairlending.Build(ctx, st, id,
		fairlending.Params{FlowID: "f1", Attribute: "applicant.gender", Favorable: policy.Refer}, now)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Favorable != policy.Refer || rep.Decisions != 10 || rep.Groups[0].Favorable != 8 {
		t.Fatalf("custom favorable report = %+v", rep)
	}
}

func TestBuildValidatesParams(t *testing.T) {
	st := store.NewMemory()
	if _, err := fairlending.Build(ctx, st, id, fairlending.Params{Attribute: "x"}, now); err == nil {
		t.Fatal("expected error for missing flow")
	}
	if _, err := fairlending.Build(ctx, st, id, fairlending.Params{FlowID: "f1"}, now); err == nil {
		t.Fatal("expected error for missing attribute")
	}
}

func TestBuildSmallSampleMarked(t *testing.T) {
	st := store.NewMemory()
	seed(t, st, "f1", "", "male", policy.Approve, 5, "ma") // total 5 < 30
	rep, err := fairlending.Build(ctx, st, id, fairlending.Params{FlowID: "f1", Attribute: "applicant.gender"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Groups) != 1 || !rep.Groups[0].SmallSample {
		t.Fatalf("expected small-sample flag: %+v", rep.Groups)
	}
}

func TestExportsRenderRows(t *testing.T) {
	st := store.NewMemory()
	seed(t, st, "f1", "", "male", policy.Approve, 40, "ma")
	seed(t, st, "f1", "", "female", policy.Decline, 40, "fd")
	rep, err := fairlending.Build(ctx, st, id, fairlending.Params{FlowID: "f1", Attribute: "applicant.gender"}, now)
	if err != nil {
		t.Fatal(err)
	}
	csv, err := fairlending.CSV(rep)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(csv, "value,total,favorable") || !strings.Contains(csv, "female") {
		t.Fatalf("csv missing header/rows:\n%s", csv)
	}
	md := fairlending.Markdown(rep)
	if !strings.Contains(md, "Disparate-impact report") || !strings.Contains(md, "applicant.gender") {
		t.Fatalf("markdown missing sections:\n%s", md)
	}
}
