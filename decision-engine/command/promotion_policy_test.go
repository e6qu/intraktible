// SPDX-License-Identifier: AGPL-3.0-or-later

package command_test

import (
	"context"
	"testing"

	"github.com/e6qu/intraktible/decision-engine/command"
	"github.com/e6qu/intraktible/decision-engine/domain"
	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/platform/eventlog"
)

func TestPromotionPolicyReadsAuthoritativeEventStream(t *testing.T) {
	ctx := context.Background()
	h := command.NewHandler(eventlog.NewMemory())
	id := idFor("builder")

	flowID, _, err := h.CreateFlow(ctx, id, domain.CreateFlow{Slug: "policy-source", Name: "Policy source"})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := h.PromotionPolicy(ctx, id, flowID)
	if err != nil {
		t.Fatal(err)
	}
	if policy["staging"].RequireReview {
		t.Fatal("default staging policy unexpectedly requires review")
	}

	_, err = h.SetPromotionPolicy(ctx, id, domain.SetPromotionPolicy{
		FlowID: flowID,
		Policy: map[string]events.PromotionStagePolicy{
			"staging": {RequireReview: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// No projector is running in this test. The command-side read must still see
	// the just-appended policy event because it is the enforcement boundary.
	policy, err = h.PromotionPolicy(ctx, id, flowID)
	if err != nil {
		t.Fatal(err)
	}
	if !policy["staging"].RequireReview {
		t.Fatalf("authoritative staging policy = %+v, want review required", policy["staging"])
	}
	if !policy["production"].RequireReview || policy["production"].AllowForce {
		t.Fatalf("production invariants were not preserved: %+v", policy["production"])
	}
}
