// SPDX-License-Identifier: AGPL-3.0-or-later

package reconsideration_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/e6qu/intraktible/decision-engine/history"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/store"
	"github.com/e6qu/intraktible/platform/testutil"
	"github.com/e6qu/intraktible/reconsideration"
)

// The contest → human-review journey (docs/JOURNEYS.md, "Explain and challenge a
// decision") is the GDPR Art. 22 right to human intervention and the ECOA
// reconsideration path. It had package-level tests over the command and read model,
// but nothing exercising it through the assembled HTTP surface — so the routes, the
// eligibility gate that decides which decisions can be contested at all, and the
// projection round-trip were only covered in pieces.
//
// The eligibility gate is the part that most warrants an end-to-end test: it is the
// rule deciding whether a consumer may exercise a statutory right, and it is
// enforced in the service rather than the command, so a package test of the command
// cannot see it.

// seedDecision writes a decision straight into the read model. The reconsideration
// service reads decisions but never writes them, so seeding the projection directly
// keeps this test to its own surface instead of standing up the decision engine.
func seedDecision(t *testing.T, st store.Store, rec history.Record) {
	t.Helper()
	rec.Org, rec.Workspace = id.Org, id.Workspace
	if err := store.PutDoc(ctx, st, history.Collection,
		store.Key(id.Org, id.Workspace, rec.DecisionID), rec); err != nil {
		t.Fatalf("seed decision %s: %v", rec.DecisionID, err)
	}
}

func declinedDecision(decisionID string) history.Record {
	return history.Record{
		DecisionID: decisionID, FlowID: "f1", Slug: "credit", Version: 1,
		Environment: "production", Status: "completed", Disposition: "decline",
		EntityType: "applicant", EntityID: "APP-1",
	}
}

func startReconsiderationAPI(t *testing.T) (*testutil.API, store.Store) {
	t.Helper()
	log := eventlog.NewMemory()
	st := store.NewMemory()
	h := reconsideration.NewHandler(log).WithNow(func() time.Time { return now })
	svc := reconsideration.New(h, st)
	api := testutil.StartAPI(t, log, st, "k", id, svc.Routes,
		reconsideration.Projector{}, reconsideration.ContestProjector{})
	return api, st
}

func TestContestAndReviewOverHTTP(t *testing.T) {
	api, st := startReconsiderationAPI(t)
	seedDecision(t, st, declinedDecision("dec-http-1"))

	// Before any review, the endpoint reports the absence explicitly rather than
	// 404ing — the UI distinguishes "no review yet" from "no such decision".
	var before struct {
		Reviewed bool `json:"reviewed"`
	}
	api.Request(t, http.MethodGet, "/v1/decisions/dec-http-1/reconsideration", nil, http.StatusOK, &before)
	if before.Reviewed {
		t.Fatal("reviewed = true before any review was recorded")
	}

	// The subject contests the automated decline.
	api.Request(t, http.MethodPost, "/v1/decisions/dec-http-1/contest",
		map[string]any{"channel": "online_portal", "note": "Income was understated."},
		http.StatusOK, nil)

	// It surfaces as an open contest, keyed to the decision's subject.
	var open struct {
		Contests []struct {
			DecisionID string `json:"decision_id"`
			Subject    string `json:"subject"`
			Channel    string `json:"channel"`
			Status     string `json:"status"`
		} `json:"contests"`
	}
	api.Request(t, http.MethodGet, "/v1/contests?status=open", nil, http.StatusOK, &open)
	if len(open.Contests) != 1 {
		t.Fatalf("open contests = %d, want 1", len(open.Contests))
	}
	if got := open.Contests[0]; got.DecisionID != "dec-http-1" ||
		got.Subject != "applicant/APP-1" || got.Channel != "online_portal" {
		t.Fatalf("contest = %+v, want the seeded decision's subject and channel", got)
	}

	// A person reviews it and overturns the decline.
	api.Request(t, http.MethodPost, "/v1/decisions/dec-http-1/reconsideration",
		map[string]any{
			"basis":     "applicant_contest",
			"outcome":   "overturned",
			"rationale": "Paystubs show income the automated bureau pull missed; recomputed DTI is within the program limit.",
		}, http.StatusOK, nil)

	var reviewed struct {
		Reviewed bool `json:"reviewed"`
		Review   struct {
			Outcome   string `json:"outcome"`
			Basis     string `json:"basis"`
			Rationale string `json:"rationale"`
		} `json:"review"`
	}
	api.Request(t, http.MethodGet, "/v1/decisions/dec-http-1/reconsideration", nil, http.StatusOK, &reviewed)
	if !reviewed.Reviewed {
		t.Fatal("reviewed = false after recording a review")
	}
	if reviewed.Review.Outcome != "overturned" || reviewed.Review.Basis != "applicant_contest" {
		t.Fatalf("review = %+v, want an overturned applicant_contest", reviewed.Review)
	}
	if reviewed.Review.Rationale == "" {
		t.Fatal("the recorded rationale is empty — a review with no stated reasoning is the rubber-stamp Art. 22 forbids")
	}

	// And the contest is no longer awaiting review.
	var stillOpen struct {
		Contests []struct{} `json:"contests"`
	}
	api.Request(t, http.MethodGet, "/v1/contests?status=open", nil, http.StatusOK, &stillOpen)
	if len(stillOpen.Contests) != 0 {
		t.Fatalf("open contests after review = %d, want 0", len(stillOpen.Contests))
	}
}

// The eligibility gate decides whether a consumer may exercise a statutory right, so
// each way of failing it is asserted separately rather than as one "rejects bad
// input" case — a gate that accidentally allowed only one of these would still pass
// a combined test.
func TestContestEligibilityOverHTTP(t *testing.T) {
	cases := []struct {
		name    string
		rec     history.Record
		wantErr bool
	}{
		{"a completed automated decline is contestable", declinedDecision("dec-ok"), false},
		{"an approval is not an adverse outcome", func() history.Record {
			r := declinedDecision("dec-approved")
			r.Disposition = "approve"
			return r
		}(), true},
		{"an unfinished decision has no outcome to contest", func() history.Record {
			r := declinedDecision("dec-running")
			r.Status = "started"
			return r
		}(), true},
		{"a decision a person already reviewed is not solely automated", func() history.Record {
			r := declinedDecision("dec-reviewed")
			r.HumanReviewed = true
			return r
		}(), true},
		{"a decision that opened a case is not solely automated", func() history.Record {
			r := declinedDecision("dec-cased")
			r.CaseID = "case-1"
			return r
		}(), true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			api, st := startReconsiderationAPI(t)
			seedDecision(t, st, c.rec)

			want := http.StatusOK
			if c.wantErr {
				want = http.StatusBadRequest
			}
			api.Request(t, http.MethodPost, "/v1/decisions/"+c.rec.DecisionID+"/contest",
				map[string]any{"channel": "phone"}, want, nil)
		})
	}
}

// A decision that does not exist must 404 rather than open a contest against
// nothing — the same shape a mistyped decision id takes in the UI.
func TestContestUnknownDecisionOverHTTP(t *testing.T) {
	api, _ := startReconsiderationAPI(t)
	api.Request(t, http.MethodPost, "/v1/decisions/nope/contest",
		map[string]any{"channel": "phone"}, http.StatusNotFound, nil)
}
