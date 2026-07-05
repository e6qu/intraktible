// SPDX-License-Identifier: AGPL-3.0-or-later

// Discussion threads: multi-participant comments grounded in each flow's actual
// mechanics, the deployment-request approval exchanges, policy wording debates,
// and the reviewer notes on the suspended decisions. @-mentions notify the
// mentioned teammate's inbox through the real projector.
package main

import (
	"net/http"
	"time"
)

type commentSeed struct {
	author string
	body   string
	hrs    float64
	reply  bool
}

type threadSeed struct {
	subjectType string
	subjectID   func(s *seeder) string
	comments    []commentSeed
}

func staticID(tag string) func(*seeder) string { return func(s *seeder) string { return s.id(tag) } }

func flowSubject(slug string) func(*seeder) string {
	return func(s *seeder) string { return s.flowID(slug) }
}

func threadSeeds(bySeed map[string]*decideSlot) []threadSeed {
	decisionOf := func(tag string) func(*seeder) string {
		return func(*seeder) string { return bySeed[tag].decisionID }
	}
	return []threadSeed{
		{"decision", decisionOf("case:acme"), []commentSeed{
			{actorMarcus, "Counterparty KYC is stale — flagging for compliance review.", 18, false},
			{actorDiego, "Agreed, holding the wire pending refresh.", 16, true},
		}},
		{"decision", decisionOf("case:wonka"), []commentSeed{
			{actorDiego, "Paused at the underwriting hold — waiting on income verification (two recent pay stubs) before I resume; risk sits in the deep 60–70 refer band.", 1, false},
		}},
		{"decision", decisionOf("case:ollivanders"), []commentSeed{
			{actorDiego, "Holding at the EDD review — waiting on the certified translation of the registry extract before resuming (identity confidence is below the 40 hard stop without it).", 3, false},
		}},
		{"decision", decisionOf("case:umbrella-payout"), []commentSeed{
			{actorDiego, "Paused at payout ops review — waiting on shipping manifests to reconcile the volume spike before releasing the funds.", 2, false},
		}},
		{"case", staticID("case:acme"), []commentSeed{
			{actorDiego, "SAR draft started; will attach the narrative agent output.", 5, false},
			{actorMarcus, "Loop me in before filing.", 4, true},
		}},
		{"flow", flowSubject("credit-decision"), []commentSeed{
			{actorPriya, "v3 adds the live bureau pull + Reg B reason codes — please review before prod.", 14, false},
			{actorMarcus, "Reviewing. What happens to the run when the bureau pull fails mid-flow?", 11, true},
			{actorPriya, "The run fails loudly (no silent default score) and the failure-rate monitor catches it — that is the firing you see on the credit failure monitor.", 10, true},
			{actorMarcus, "@ava.chen confirming the adverse-action wording with compliance: DTI_TOO_HIGH must cite the ratio, not the band label — Reg B wants the specific reason.", 8, false},
		}},
		{"flow", flowSubject("aml-screening"), []commentSeed{
			{actorPriya, "v3 catches sub-threshold structuring (5+ deposits under $10k in 30 days) that v2 clears — the staging challenger exists to measure exactly that gap.", 19, false},
			{actorDiego, "Two of the referrals it added were payroll batches (Hooli again). Can the code node exempt whitelisted recurring corridors before we promote?", 16, true},
			{actorPriya, "That is what the TXN-9920 pre-approval is for — honored corridors skip the flow entirely, so the heuristic never sees them.", 15, true},
			{actorMarcus, "SAR narrative node pushes p50 latency over the 200ms monitor — expected, the AI call dominates. Alert threshold stays as a forcing function.", 12, false},
		}},
		{"flow", flowSubject("card-fraud"), []commentSeed{
			{actorPriya, "v4 runs as a 15% production challenger — same graph, plus the trusted-customer rules that shade the probability before banding.", 30, false},
			{actorDiego, "Block rate is holding under the 15% monitor so far; watching FRAUD_REVIEW referral volume before we widen the arm.", 22, true},
		}},
		{"flow", flowSubject("dispute-triage"), []commentSeed{
			{actorDiego, "v2 adds the reason-code liability table: 10.4 card-absent fraud auto-assigns issuer liability, quality goes to representment scoring.", 36, false},
			{actorPriya, "Chargeback season has the refer rate way above the captured baseline — the drift panel is genuinely red, not noise. Baseline recapture after the season?", 28, true},
			{actorMarcus, "Keep the old baseline until the season ends — recapturing now would normalize the anomaly away. @lena.hoff for the compliance record.", 24, true},
		}},
		{"flow", flowSubject("payout-risk"), []commentSeed{
			{actorPriya, "Matrix auto-releases medium-risk small payouts now — watch the chargeback cohort for two weeks.", 50, false},
			{actorDiego, "Cohort is clean so far (hold rate under the 20% monitor), and the shadow arm shows exactly what v1 would have queued instead.", 26, true},
			{actorMarcus, "MER-4515 fast-lane pre-approval expired last quarter — renewal review is on me before we re-grant the $25k auto-release cap.", 20, false},
		}},
		{"policy", staticID("policy:credit"), []commentSeed{
			{actorMarcus, "Reg B requires specific reasons — LOW_SCORE alone is too generic when DTI drove the decline.", 26, false},
			{actorPriya, "v2 orders DTI_TOO_HIGH ahead of the band label for exactly that. @ava.chen see the wording thread on the flow.", 24, true},
		}},
		{"policy", staticID("policy:claim"), []commentSeed{
			{actorMarcus, "Fast-track cap stays at $200 until the abuse model clears its drift review.", 100, false},
			{actorPriya, "Drift review scheduled — see the model-risk row for claim_fraud.", 96, true},
		}},
		{"deployment_request", staticID("req:c0"), []commentSeed{
			{actorPriya, "Backtest replayed 400 production decisions through v2 — dispositions match v1 except the intended band shift at risk 30→35.", 199, false},
			{actorMarcus, "Parity report looks right and the reason codes are Reg B-clean. Approving.", 196, true},
		}},
		{"deployment_request", staticID("req:c1"), []commentSeed{
			{actorMarcus, "@priya.nair before I approve: the bureau pull failed five times this week in staging. What is the blast radius in prod if the bureau has another bad day?", 10, false},
		}},
		{"deployment_request", staticID("req:f0"), []commentSeed{
			{actorPriya, "Q2 losses concentrated in the 35–40 score band v2 was approving — tightening the review band to 35 catches them.", 78, false},
			{actorMarcus, "Referral volume impact is within what the fraud queue can absorb. Approved.", 74, true},
		}},
		{"deployment_request", staticID("req:cl0"), []commentSeed{
			{actorMarcus, "Staging backtest: v2 refers 9% more claims than v1. That is the CLAIM_FRAUD_REVIEW band at 55 pulling in seasoned single-claim customers — adjusters cannot absorb it.", 158, false},
			{actorPriya, "The extra referrals are exactly the repeat-claimant cohort the abuse model flags — is the volume really the problem?", 157, true},
			{actorMarcus, "Half of them are first claims on mature policies — false positives. Rejecting; re-tune the fraud band and resubmit.", 156, true},
		}},
		{"deployment_request", staticID("req:cl1"), []commentSeed{
			{actorPriya, "Fraud band re-tuned to 60: the mature-policy first claims drop out, referral delta is +2% and it is the repeat-claimant cohort we want reviewed.", 125, false},
			{actorMarcus, "That matches the abuse-model intent. Approved — keep the $200 fast-track cap until the drift review clears.", 121, true},
		}},
	}
}

// commentActions posts every thread at its historical times, threading replies
// under the latest top-level comment (the single nesting level the UI offers).
func (s *seeder) commentActions(bySeed map[string]*decideSlot, anchor time.Time) []action {
	var acts []action
	for _, t := range threadSeeds(bySeed) {
		lastTop := new(string)
		for _, c := range t.comments {
			acts = append(acts, action{
				at:   anchor.Add(-time.Duration(c.hrs * float64(time.Hour))),
				name: "comment " + t.subjectType,
				run: func() {
					body := map[string]any{"body": c.body}
					if c.reply {
						if *lastTop == "" {
							fatalf("comment thread %s: reply before any top-level comment", t.subjectType)
						}
						body["parent_id"] = *lastTop
					}
					var res struct {
						CommentID string `json:"comment_id"`
					}
					s.call(c.author, http.MethodPost,
						"/v1/comments/"+t.subjectType+"/"+t.subjectID(s), body, &res)
					if !c.reply {
						*lastTop = res.CommentID
					}
				},
			})
		}
	}
	return acts
}
