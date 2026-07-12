// SPDX-License-Identifier: AGPL-3.0-or-later

// The workspace's change-control history: API keys for the demo cast and the
// service identities, the ten-flow fleet (every version published through the
// real registry, environments deployed through maker-checker where production
// requires it), disposition policies, promotion policies, SLOs, monitors,
// webhooks, assertions, and the shadow arm.
package main

import (
	"net/http"
	"net/url"
	"time"
)

// adverseActionActions serves the ECOA / FCRA adverse-action notice for most of the
// credit flow's declined decisions — recording who served it, when, how, and a hash
// of the exact document — and leaves the three most recent pending so the Fair-lending
// work queue shows real outstanding work. Only credit-decision declines are served:
// they carry the reason codes (and drew on the bureau) an FCRA notice needs.
func (s *seeder) adverseActionActions(anchor time.Time) []action {
	return []action{{at: anchor.Add(2 * time.Hour), name: "issue adverse-action notices", run: func() {
		var q struct {
			AdverseActions []struct {
				DecisionID string `json:"decision_id"`
				Slug       string `json:"slug"`
			} `json:"adverse_actions"`
		}
		s.call(actorDiego, http.MethodGet, "/v1/adverse-actions?status=pending", nil, &q)
		var credit []string
		for _, aa := range q.AdverseActions {
			if aa.Slug == "credit-decision" {
				credit = append(credit, aa.DecisionID)
			}
		}
		// Serve all but the last three, so the queue carries both issued and pending.
		methods := []string{"mail", "email", "in_app"}
		for i := 0; i < len(credit)-3; i++ {
			s.call(actorDiego, http.MethodPost,
				"/v1/decisions/"+url.PathEscape(credit[i])+"/adverse-action/issue",
				map[string]any{"method": methods[i%len(methods)], "based_on_consumer_report": true}, nil)
		}
	}}}
}

// reconsiderationActions records a human review of a couple of solely-automated credit
// declines — one overturned, one upheld — so the Art. 22 / reconsideration surface on
// the decision page shows real records. It only picks declines that ran end to end with
// no person in the loop (the eligibility the endpoint enforces).
func (s *seeder) reconsiderationActions(anchor time.Time) []action {
	return []action{{at: anchor.Add(3 * time.Hour), name: "record human reviews", run: func() {
		var q struct {
			AdverseActions []struct {
				DecisionID string `json:"decision_id"`
				Slug       string `json:"slug"`
			} `json:"adverse_actions"`
		}
		s.call(actorDiego, http.MethodGet, "/v1/adverse-actions", nil, &q)
		reviews := []map[string]any{
			{"basis": "applicant_contest", "outcome": "overturned",
				"rationale": "Applicant supplied a pay stub the automated bureau pull missed; recomputed DTI is within the program limit, so the decline is reversed."},
			{"basis": "proactive", "outcome": "upheld",
				"rationale": "Reviewed against the file: the delinquency and utilization drivers are confirmed and material; the automated decline stands."},
		}
		i := 0
		for _, aa := range q.AdverseActions {
			if i >= len(reviews) || aa.Slug != "credit-decision" {
				continue
			}
			var dec struct {
				Disposition   string `json:"disposition"`
				Status        string `json:"status"`
				CaseID        string `json:"case_id"`
				HumanReviewed bool   `json:"human_reviewed"`
			}
			s.call(actorDiego, http.MethodGet, "/v1/decisions/"+url.PathEscape(aa.DecisionID), nil, &dec)
			if dec.Status != "completed" || dec.Disposition != "decline" || dec.CaseID != "" || dec.HumanReviewed {
				continue
			}
			s.call(actorDiego, http.MethodPost, "/v1/decisions/"+url.PathEscape(aa.DecisionID)+"/reconsideration", reviews[i], nil)
			i++
		}
	}}}
}

// timeCursor hands out strictly increasing config-phase timestamps.
type timeCursor struct{ t time.Time }

func (c *timeCursor) step(d time.Duration) time.Time {
	c.t = c.t.Add(d)
	return c.t
}

type keySpec struct {
	tag    string
	name   string
	actor  string
	scope  string
	role   string
	by     string
	expiry time.Duration // 0 = none
}

func keySpecs() []keySpec {
	return []keySpec{
		{"key:ava", "Ava Chen — Head of Decisioning", actorAva, "*", "admin", actorDev, 0},
		{"key:marcus", "Marcus Reed — Risk Approver", actorMarcus, "*", "approver", actorAva, 0},
		{"key:priya", "Priya Nair — Flow Author", actorPriya, "*", "editor", actorAva, 0},
		{"key:diego", "Diego Santos — Case Analyst", actorDiego, "*", "operator", actorAva, 0},
		{"key:lena", "Lena Hoff — Audit & Compliance", actorLena, "*", "viewer", actorAva, 0},
		{"key:svc-prod", "Production server", actorSvcProd, "production", "editor", actorAva, 0},
		{"key:svc-ci", "CI sandbox", actorSvcCI, "sandbox", "operator", actorAva, 120 * 24 * time.Hour},
		{"key:svc-scheduler", "Ops scheduler", actorSvcSched, "*", "operator", actorAva, 0},
		{"key:svc-bi", "Analytics read-only", actorSvcBI, "*", "viewer", actorAva, 0},
		{"key:svc-partner", "Decommissioned partner", actorSvcOldPtr, "production", "operator", actorAva, 0},
	}
}

// keyActions mints every managed key; the returned secrets let the seeder act as
// each identity for the rest of the timeline.
func (s *seeder) keyActions(cfg *timeCursor) []action {
	var acts []action
	for _, k := range keySpecs() {
		acts = append(acts, action{at: cfg.step(3 * time.Minute), name: "api key " + k.actor, run: func() {
			body := map[string]any{"name": k.name, "actor": k.actor, "scope": k.scope, "role": k.role}
			if k.expiry > 0 {
				body["expires_at"] = s.clk.Now().Add(k.expiry).Format(time.RFC3339)
			}
			var res struct {
				APIKey struct {
					ID string `json:"id"`
				} `json:"api_key"`
				Secret string `json:"secret"`
			}
			s.call(k.by, http.MethodPost, "/v1/api-keys", body, &res)
			s.keys[k.actor] = res.Secret
			s.setID(k.tag, res.APIKey.ID)
		}})
	}
	return acts
}

type versionSpec struct {
	graph  map[string]any
	schema map[string]any // nil = none published for this version
	by     string
}

type flowSpec struct {
	tag         string // "flow:<slug>"
	slug        string
	name        string
	description string
	by          string // flow creator
	versions    []versionSpec
}

func flowSpecs() []flowSpec {
	return []flowSpec{
		{"flow:credit-decision", "credit-decision", "Consumer Credit Decision",
			"Scores loan applications with bureau enrichment and the PD/propensity models, banding risk into auto-approve, underwriter referral, or decline. v3 adds the live Experian pull and Reg B adverse-action reason codes.",
			actorAva, []versionSpec{
				{creditGraphV1(), nil, actorAva},
				{creditGraphV2(), creditSchema(), actorPriya},
				{creditGraphV3(), creditSchema(), actorPriya},
			}},
		{"flow:aml-screening", "aml-screening", "AML Transaction Screening",
			"Screens wires and transfers against the sanctions hard-stop, sub-threshold structuring heuristics, and a composite AML risk score. Referrals reach the analyst queue with an AI-drafted SAR narrative attached.",
			actorAva, []versionSpec{
				{amlGraphV1(), nil, actorAva},
				{amlGraphV2(), amlSchema(), actorPriya},
				{amlGraphV3(), amlSchema(), actorPriya},
			}},
		{"flow:kyc-onboarding", "kyc-onboarding", "KYC Onboarding",
			"Verifies onboarding packets via document extraction, PEP/adverse-media checks, and the vendor score, gating on identity confidence. Low confidence routes to EDD review; hard stops suspend into a durable EDD hold.",
			actorPriya, []versionSpec{
				{kycGraphV1(), nil, actorPriya},
				{kycGraphV2(), kycSchema(), actorPriya},
			}},
		{"flow:card-fraud", "card-fraud", "Card Fraud Scoring",
			"Scores card authorizations in real time from velocity, device, and amount-ratio features. High fraud probability blocks outright, the mid band refers to a fraud analyst, and the v4 challenger shades scores with trusted-customer rules.",
			actorAva, []versionSpec{
				{fraudGraphV1(), nil, actorAva},
				{fraudGraphV2(), nil, actorPriya},
				{fraudGraphV3(), fraudSchema(), actorPriya},
				{fraudGraphV4(), fraudSchema(), actorPriya},
			}},
		{"flow:dispute-triage", "dispute-triage", "Dispute / Chargeback Triage",
			"Triages chargebacks by reason-code liability and value tier: low-scoring disputes auto-refund, the rest route to disputes ops with an AI summary and the evidence checklist for representment.",
			actorPriya, []versionSpec{
				{disputeGraphV1(), nil, actorPriya},
				{disputeGraphV2(), disputeSchema(), actorPriya},
			}},
		{"flow:merchant-onboarding", "merchant-onboarding", "Merchant Onboarding",
			"Underwrites merchant applications from the MCC tier adder, volume features, and the merchant risk score. Scores above the gate go to underwriting review with an AI-drafted memo; the rest board automatically.",
			actorPriya, []versionSpec{
				{merchantGraphV1(), nil, actorPriya},
				{merchantGraphV2(), merchantSchema(), actorPriya},
			}},
		{"flow:collections-hardship", "collections-hardship", "Collections Hardship Program",
			"Assesses hardship applications on a weighted scorecard (income drop, missed payments, medical events, tenure). Qualifying cases get tiered plan terms; concessions above authority escalate to supervisor review.",
			actorPriya, []versionSpec{
				{collectionsGraphV1(), nil, actorPriya},
				{collectionsGraphV2(), collectionsSchema(), actorPriya},
			}},
		{"flow:claim-triage", "claim-triage", "Purchase Protection Claim Triage",
			"Triages purchase-protection claims: lapsed policies deny with reason codes, low-value first claims fast-track to payment, and abuse-model flags or high severity refer to an adjuster with an AI brief.",
			actorPriya, []versionSpec{
				{claimGraphV1(), nil, actorPriya},
				{claimGraphV2(), claimSchema(), actorPriya},
			}},
		{"flow:payout-risk", "payout-risk", "Marketplace Payout Risk",
			"Gates marketplace seller payouts through a risk × amount matrix over core-banking ledger history and the payout risk score — auto-release, ops review, or funds hold. Large flagged payouts suspend into a durable ops hold.",
			actorPriya, []versionSpec{
				{payoutGraphV1(), nil, actorPriya},
				{payoutGraphV2(), payoutSchema(), actorPriya},
			}},
		{"flow:limit-increase", "limit-increase", "Card Limit Increase",
			"Decides card limit-increase requests from utilization, DTI, and PD risk: low-risk requests auto-grant up to 1.5x the current limit, the mid band refers to credit ops, and the rest keep their limit.",
			actorPriya, []versionSpec{
				{limitGraphV1(), limitSchema(), actorPriya},
			}},
	}
}

func (s *seeder) flowID(slug string) string { return s.id("flow:" + slug) }

// flowActions creates every flow and publishes its version history in order.
func (s *seeder) flowActions(cfg *timeCursor) []action {
	var acts []action
	for _, f := range flowSpecs() {
		acts = append(acts, action{at: cfg.step(5 * time.Minute), name: "flow " + f.slug, run: func() {
			var res struct {
				FlowID string `json:"flow_id"`
			}
			s.call(f.by, http.MethodPost, "/v1/flows",
				map[string]any{"slug": f.slug, "name": f.name, "description": f.description}, &res)
			s.setID(f.tag, res.FlowID)
		}})
		for vi, v := range f.versions {
			want := vi + 1
			acts = append(acts, action{at: cfg.step(25 * time.Minute), name: fmtName("publish %s v%d", f.slug, want), run: func() {
				body := map[string]any{"graph": v.graph}
				if v.schema != nil {
					body["input_schema"] = v.schema
				}
				var res struct {
					Version int `json:"version"`
				}
				s.call(v.by, http.MethodPost, "/v1/flows/"+s.flowID(f.slug)+"/versions", body, &res)
				if res.Version != want {
					fatalf("%s: published version %d, want %d", f.slug, res.Version, want)
				}
			}})
		}
	}
	return acts
}

// deployStep is one change-control step: a direct deploy (non-production) or a
// maker-checker deployment request with its verdict.
type deployStep struct {
	slug              string
	env               string
	version           int
	challengerVersion int
	challengerPct     int
	requestTag        string // non-empty = go through a deployment request
	requestedBy       string
	requestReason     string
	verdict           string // "approved" | "rejected"
	verdictBy         string
	verdictReason     string
	by                string // direct-deploy actor (non-production)
}

func deploySteps() []deployStep {
	direct := func(slug, env string, version int) deployStep {
		return deployStep{slug: slug, env: env, version: version, by: actorMarcus}
	}
	directCh := func(slug, env string, version, chVersion, chPct int) deployStep {
		return deployStep{slug: slug, env: env, version: version, challengerVersion: chVersion, challengerPct: chPct, by: actorMarcus}
	}
	approved := func(tag, slug string, version int, reqBy, reason, verdictReason string) deployStep {
		return deployStep{slug: slug, env: "production", version: version, requestTag: tag,
			requestedBy: reqBy, requestReason: reason, verdict: "approved", verdictBy: actorMarcus, verdictReason: verdictReason}
	}
	return []deployStep{
		// Consumer credit: v2 to production through maker-checker, staging walks
		// v2 -> v3, sandbox runs the v3 champion with a v2 challenger arm.
		approved("req:c0", "credit-decision", 2, actorPriya,
			"Backtest parity confirmed; dual-model rollout",
			"Parity report looks right and the reason codes are Reg B-clean."),
		direct("credit-decision", "staging", 2),
		direct("credit-decision", "staging", 3),
		directCh("credit-decision", "sandbox", 3, 2, 20),
		// AML: v2 to production, staging runs the v3 champion with a v2 challenger
		// (the structuring experiment), sandbox v3.
		approved("req:a0", "aml-screening", 2, actorPriya,
			"Composite screening + SAR narrative rollout",
			"Sanctions hard-stop verified against the watchlist suite."),
		directCh("aml-screening", "staging", 3, 2, 30),
		direct("aml-screening", "sandbox", 3),
		// KYC: v1 then v2 to production (previous version preserved), v2 elsewhere.
		approved("req:k0", "kyc-onboarding", 1, actorPriya,
			"Initial KYC onboarding go-live",
			"Vendor score thresholds match the compliance memo."),
		approved("req:k1", "kyc-onboarding", 2, actorPriya,
			"Add Jumio doc verification + the EDD hard-stop hold",
			"Hold band verified against last quarter's EDD queue."),
		direct("kyc-onboarding", "staging", 2),
		direct("kyc-onboarding", "sandbox", 2),
		// Card fraud: v2 then v3 through maker-checker, then the v4 challenger arm;
		// staging/sandbox run v4.
		approved("req:f-1", "card-fraud", 2, actorPriya,
			"Add the analyst explanation to production scoring",
			"Explanation adds reviewer context without changing the bands."),
		approved("req:f0", "card-fraud", 3, actorPriya,
			"Tighten the review band to 35 after the Q2 loss review",
			"Referral volume impact is within what the fraud queue can absorb. Approved."),
		{slug: "card-fraud", env: "production", version: 3, challengerVersion: 4, challengerPct: 15,
			requestTag: "req:f1", requestedBy: actorPriya,
			requestReason: "Run the trusted-customer rules as a 15% production challenger",
			verdict:       "approved", verdictBy: actorMarcus,
			verdictReason: "Arm is small enough to unwind fast if the block rate moves."},
		direct("card-fraud", "staging", 4),
		direct("card-fraud", "sandbox", 4),
		// Dispute: v1 to production; staging walks v1 -> v2; sandbox v2.
		approved("req:d0", "dispute-triage", 1, actorPriya,
			"Dispute triage go-live",
			"Auto-refund band matches the ops runbook."),
		direct("dispute-triage", "staging", 1),
		direct("dispute-triage", "staging", 2),
		direct("dispute-triage", "sandbox", 2),
		// Merchant: staging walks v1 -> v2; sandbox v2 (no production yet).
		direct("merchant-onboarding", "staging", 1),
		direct("merchant-onboarding", "staging", 2),
		direct("merchant-onboarding", "sandbox", 2),
		// Collections: v1 then v2 to production.
		approved("req:h0", "collections-hardship", 1, actorPriya,
			"Hardship program go-live",
			"Scorecard weights countersigned by the program office."),
		approved("req:h1", "collections-hardship", 2, actorPriya,
			"Add plan terms + reviewer summary",
			"Plan table matches the relief matrix in the program guide."),
		direct("collections-hardship", "staging", 2),
		direct("collections-hardship", "sandbox", 2),
		// Claims: v1 live, v2 rejected once (referral overload), re-tuned and approved.
		approved("req:cl-1", "claim-triage", 1, actorPriya,
			"Claim triage go-live",
			"Severity bands match the adjuster manual."),
		{slug: "claim-triage", env: "production", version: 2, requestTag: "req:cl0",
			requestedBy:   actorPriya,
			requestReason: "Fast-track low-value first claims + denial reason codes",
			verdict:       "rejected", verdictBy: actorMarcus,
			verdictReason: "Staging backtest shows +9% referral rate — tune the fraud band first"},
		approved("req:cl1", "claim-triage", 2, actorPriya,
			"Fraud band re-tuned to 60; referral delta now +2%",
			"That matches the abuse-model intent. Approved — keep the $200 fast-track cap until the drift review clears."),
		direct("claim-triage", "staging", 2),
		direct("claim-triage", "sandbox", 2),
		// Payout: v1 then v2 to production; staging/sandbox v2.
		approved("req:p0", "payout-risk", 1, actorPriya,
			"Payout risk gating go-live",
			"Hold thresholds match treasury's risk appetite."),
		approved("req:p1", "payout-risk", 2, actorPriya,
			"Route through the risk × amount matrix (auto-release small payouts)",
			"Matrix cuts the ops queue without widening the hold band."),
		direct("payout-risk", "staging", 2),
		direct("payout-risk", "sandbox", 2),
		// Limit increase: pre-production only.
		direct("limit-increase", "staging", 1),
		direct("limit-increase", "sandbox", 1),
	}
}

func (s *seeder) deployActions(cfg *timeCursor) []action {
	var acts []action
	for _, d := range deploySteps() {
		acts = append(acts, action{at: cfg.step(9 * time.Minute), name: "deploy " + d.slug + " " + d.env, run: func() {
			body := map[string]any{"environment": d.env, "version": d.version}
			if d.challengerVersion > 0 {
				body["challenger_version"] = d.challengerVersion
				body["challenger_pct"] = d.challengerPct
			}
			flowID := s.flowID(d.slug)
			if d.requestTag == "" {
				s.call(d.by, http.MethodPost, "/v1/flows/"+flowID+"/deployments", body, nil)
				return
			}
			var res struct {
				RequestID string `json:"request_id"`
			}
			s.call(d.requestedBy, http.MethodPost, "/v1/flows/"+flowID+"/deployment-requests", body, &res)
			s.setID(d.requestTag, res.RequestID)
			s.clk.Advance(45 * time.Minute) // the checker reviews before deciding
			s.call(d.verdictBy, http.MethodPost,
				"/v1/flows/"+flowID+"/deployment-requests/"+res.RequestID+"/"+verdictPath(d.verdict),
				map[string]any{"reason": d.verdictReason}, nil)
		}})
	}
	return acts
}

func verdictPath(verdict string) string {
	switch verdict {
	case "approved":
		return "approve"
	case "rejected":
		return "reject"
	}
	fatalf("unknown verdict %q", verdict)
	return ""
}

// pendingRequestActions raises the still-open deployment requests near the end
// of the window (they anchor the maker-checker inbox story).
func (s *seeder) pendingRequestActions(anchor time.Time) []action {
	pending := []struct {
		tag    string
		slug   string
		by     string
		ver    int
		reason string
		hrs    float64
	}{
		{"req:c1", "credit-decision", actorPriya, 3, "Roll out live bureau pull + Reg B adverse-action codes", 12},
		{"req:a1", "aml-screening", actorAva, 3, "Add structuring heuristics + SAR narrative to prod", 8},
		{"req:d1", "dispute-triage", actorPriya, 2, "Promote the reason-code liability table", 30},
	}
	var acts []action
	for _, p := range pending {
		acts = append(acts, action{at: anchor.Add(-time.Duration(p.hrs * float64(time.Hour))), name: "pending " + p.tag, run: func() {
			var res struct {
				RequestID string `json:"request_id"`
			}
			s.call(p.by, http.MethodPost, "/v1/flows/"+s.flowID(p.slug)+"/deployment-requests",
				map[string]any{"environment": "production", "version": p.ver}, &res)
			s.setID(p.tag, res.RequestID)
		}})
	}
	return acts
}

// strictPromotion is the shared promotion policy: gates tighten toward production.
func strictPromotion() map[string]any {
	stage := func(assertions, noFiring, force, review bool) map[string]any {
		return map[string]any{
			"require_assertions":         assertions,
			"require_no_firing_monitors": noFiring,
			"allow_force":                force,
			"require_review":             review,
		}
	}
	return map[string]any{
		"sandbox":    stage(false, false, true, false),
		"staging":    stage(true, true, true, false),
		"production": stage(true, true, false, true),
	}
}

type monitorSpec struct {
	slug        string
	metric      string
	op          string
	threshold   float64
	description string
}

func monitorSpecs() []monitorSpec {
	return []monitorSpec{
		{"credit-decision", "failure_rate", "gt", 0.05, "Decision failure rate"},
		{"credit-decision", "refer_rate", "gt", 0.4, "Manual-review referral rate"},
		{"credit-decision", "distribution_drift_psi", "gt", 0.25, "Disposition drift (PSI)"},
		{"aml-screening", "volume", "lt", 5, "Screening throughput floor"},
		{"aml-screening", "refer_rate", "gt", 0.25, "SAR referral rate"},
		{"aml-screening", "avg_latency_ms", "gt", 200, "p50 screening latency"},
		{"card-fraud", "decline_rate", "gt", 0.15, "Block rate"},
		{"card-fraud", "avg_latency_ms", "gt", 300, "p50 scoring latency"},
		{"card-fraud", "refer_rate", "gt", 0.3, "Analyst referral rate"},
		{"kyc-onboarding", "refer_rate", "gt", 0.5, "EDD referral rate"},
		{"dispute-triage", "automation_rate", "lt", 0.5, "Auto-refund automation rate"},
		{"merchant-onboarding", "volume", "lt", 20, "Boarding throughput floor"},
		{"collections-hardship", "refer_rate", "gt", 0.4, "Supervisor escalation rate"},
		{"claim-triage", "failure_rate", "gt", 0.1, "Decision failure rate"},
		{"payout-risk", "decline_rate", "gt", 0.2, "Funds-hold rate"},
		{"payout-risk", "avg_latency_ms", "gt", 250, "p50 release latency"},
	}
}

type webhookSpec struct {
	url    string
	note   string
	events []string
}

func webhookSpecs() []webhookSpec {
	return []webhookSpec{
		{"https://hooks.slack.demo/risk-alerts", "Risk team Slack", []string{"monitor"}},
		{"https://pager.demo/aml-oncall", "AML on-call pager", []string{"monitor", "case.sla_breached"}},
		{"https://hooks.pagerduty.demo/payout-oncall", "Payout ops PagerDuty (paused during migration)", []string{"case.sla_breached"}},
	}
}

// assertionSuites are the flow test cases (inputs carry the same enrichment the
// decide traffic sends, so the suites run green through the real core).
func assertionSuites() map[string][]map[string]any {
	kase := func(name string, input map[string]any, expect map[string]any) map[string]any {
		return map[string]any{"name": name, "input": input, "expect": expect}
	}
	return map[string][]map[string]any{
		"credit-decision": {
			kase("prime applicant approves",
				creditInputRaw(120000, 4000, 1000, 20000, 0, 800, 6, 0.9, "Assertion Prime LLC"),
				map[string]any{"approved": true}),
			kase("sub-prime high dti declines",
				creditInputRaw(30000, 26000, 14000, 15000, 3, 600, 1, 0.4, "Assertion Subprime LLC"),
				map[string]any{"approved": false}),
			kase("mid band refers to underwriter",
				creditInputRaw(60000, 28000, 8000, 15000, 1, 660, 3, 0.6, "Assertion Midband LLC"),
				map[string]any{"offered_limit": 6000}),
		},
		"aml-screening": {
			kase("small domestic clears",
				amlInputRaw(2000, "US", "US", 5, 1, 0.2, "Assertion Wire A"),
				map[string]any{"cleared": true}),
			kase("large cross-border refers",
				amlInputRaw(60000, "US", "KY", 10, 2, 0.5, "Assertion Wire B"),
				map[string]any{"cleared": false}),
			kase("sanctions hit cannot clear",
				amlInputRaw(1000, "US", "US", 90, 0, 0.1, "Assertion Wire C"),
				map[string]any{"cleared": false}),
			kase("structuring pattern refers",
				amlInputRaw(9200, "US", "US", 5, 5, 0.4, "Assertion Wire D"),
				map[string]any{"cleared": false}),
		},
		"card-fraud": {
			kase("low velocity allows",
				fraudInputRaw(80, 1, 10, 120, 1, 0, "Assertion Card A"),
				map[string]any{"blocked": false}),
			kase("high velocity blocks",
				fraudInputRaw(1500, 9, 95, 120, 0, 1, "Assertion Card B"),
				map[string]any{"blocked": true}),
		},
		"collections-hardship": {
			kase("qualifying hardship enrolls",
				collectionsInputRaw(5200, 3100, 2, 0, 4, 8400, "Assertion Household A"),
				map[string]any{"enrolled": true}),
			kase("minor dip stays on standard collections",
				collectionsInputRaw(5200, 4900, 1, 0, 2, 4000, "Assertion Household B"),
				map[string]any{"enrolled": false}),
		},
		"claim-triage": {
			kase("low-value first claim fast-tracks",
				claimInputRaw(120, 3000, 1, 0, 400, "Assertion Claim A"),
				map[string]any{"paid": true}),
			kase("lapsed policy denies",
				claimInputRaw(900, 3000, 0, 0, 500, "Assertion Claim B"),
				map[string]any{"paid": false}),
		},
		"payout-risk": {
			kase("routine payout releases",
				payoutInputRaw(2400, 2600, 1, 220, 0.004, "Assertion Payout A"),
				map[string]any{"released": true}),
			kase("young account velocity holds",
				payoutInputRaw(14000, 4000, 5, 12, 0.02, "Assertion Payout B"),
				map[string]any{"released": false}),
		},
	}
}

type sloSpec struct {
	slug            string
	successTarget   float64
	latencyTargetMS int
}

func sloSpecs() []sloSpec {
	return []sloSpec{
		{"credit-decision", 0.95, 400},
		{"aml-screening", 0.97, 200},
		{"card-fraud", 0.99, 300},
		{"kyc-onboarding", 0.92, 400},
		{"dispute-triage", 0.95, 300},
		{"collections-hardship", 0.9, 400},
		{"claim-triage", 0.92, 300},
		{"payout-risk", 0.9, 250},
	}
}

// governanceConfigActions wires promotion policies, SLOs, monitors, webhooks,
// assertions, the payout shadow arm, and the claim-abuse drift threshold.
func (s *seeder) governanceConfigActions(cfg *timeCursor) []action {
	var acts []action
	for _, f := range flowSpecs() {
		acts = append(acts, action{at: cfg.step(time.Minute), name: "promotion policy " + f.slug, run: func() {
			s.call(actorAva, http.MethodPut, "/v1/flows/"+s.flowID(f.slug)+"/promotion-policy",
				map[string]any{"policy": strictPromotion()}, nil)
		}})
	}
	for _, sl := range sloSpecs() {
		acts = append(acts, action{at: cfg.step(time.Minute), name: "slo " + sl.slug, run: func() {
			s.call(actorAva, http.MethodPut, "/v1/flows/"+s.flowID(sl.slug)+"/slo",
				map[string]any{"success_target": sl.successTarget, "latency_target_ms": sl.latencyTargetMS}, nil)
		}})
	}
	for _, m := range monitorSpecs() {
		acts = append(acts, action{at: cfg.step(time.Minute), name: "monitor " + m.slug + " " + m.metric, run: func() {
			s.call(actorPriya, http.MethodPost, "/v1/flows/"+s.flowID(m.slug)+"/monitors", map[string]any{
				"metric": m.metric, "op": m.op, "threshold": m.threshold, "description": m.description,
			}, nil)
		}})
	}
	for _, w := range webhookSpecs() {
		acts = append(acts, action{at: cfg.step(time.Minute), name: "webhook " + w.note, run: func() {
			s.call(actorAva, http.MethodPost, "/v1/webhooks",
				map[string]any{"url": w.url, "note": w.note, "events": w.events}, nil)
		}})
	}
	for slug, cases := range assertionSuites() {
		acts = append(acts, action{at: cfg.step(time.Minute), name: "assertions " + slug, run: func() {
			s.call(actorPriya, http.MethodPut, "/v1/flows/"+s.flowID(slug)+"/assertions",
				map[string]any{"cases": cases}, nil)
		}})
	}
	acts = append(acts, action{at: cfg.step(time.Minute), name: "shadow payout staging", run: func() {
		s.call(actorAva, http.MethodPut, "/v1/flows/"+s.flowID("payout-risk")+"/shadow",
			map[string]any{"environment": "staging", "version": 1}, nil)
	}}, action{at: cfg.step(time.Minute), name: "model monitor claim_fraud", run: func() {
		s.call(actorAva, http.MethodPost, "/v1/models/claim_fraud/monitor", map[string]any{"threshold": 0.1}, nil)
	}}, action{at: cfg.step(time.Minute), name: "adverse-action settings", run: func() {
		// Creditor + consumer-reporting-agency identification an ECOA/FCRA notice
		// must carry (the demo lender pulls Experian, so the CRA is configured).
		s.call(actorAva, http.MethodPut, "/v1/fairlending/settings", map[string]any{ // #nosec G101 -- public creditor/CRA identification in demo seed content, not credentials
			"creditor_name":      "Harborview Bank, N.A.",
			"creditor_address":   "500 Financial Plaza, Columbus, OH 43215",
			"creditor_phone":     "1-800-555-0142",
			"enforcement_agency": "Bureau of Consumer Financial Protection, 1700 G Street NW, Washington, DC 20552",
			"cra_name":           "Experian Information Solutions, Inc.",
			"cra_address":        "P.O. Box 2002, Allen, TX 75013",
			"cra_phone":          "1-888-397-3742",
		}, nil)
	}})
	return acts
}

type policySpec struct {
	tag      string
	name     string
	slug     string
	versions []policyVersion
}

type policyVersion struct {
	by   string
	spec map[string]any
}

func policyRule(when, disposition, code, description string) map[string]any {
	return map[string]any{"when": when, "disposition": disposition, "code": code, "description": description}
}

func policySpecs() []policySpec {
	spec := func(rules ...map[string]any) map[string]any {
		return map[string]any{"rules": rules, "default": "refer"}
	}
	return []policySpec{
		{"policy:credit", "Credit Disposition", "credit-decision", []policyVersion{
			{actorAva, spec(
				policyRule("risk < 30", "approve", "APPROVED", "Meets approval criteria"),
				policyRule("fico_score < 620", "decline", "LOW_SCORE", "Credit score below threshold"),
				policyRule("risk >= 70", "decline", "DELINQUENCY_HISTORY", "Serious delinquency on file"),
				policyRule("risk >= 30", "refer", "DTI_TOO_HIGH", "Debt-to-income ratio too high"),
			)},
			{actorMarcus, spec(
				policyRule("risk < 35", "approve", "APPROVED", "Meets approval criteria"),
				policyRule("fico_score < 620", "decline", "LOW_SCORE", "Credit score below threshold"),
				policyRule("risk >= 70", "decline", "DELINQUENCY_HISTORY", "Serious delinquency on file"),
				policyRule("risk >= 35", "refer", "DTI_TOO_HIGH", "Debt-to-income ratio too high"),
			)},
		}},
		{"policy:aml", "AML Clearing Policy", "aml-screening", []policyVersion{
			{actorMarcus, spec(
				policyRule("aml_score >= 6", "refer", "AML_HIGH", "Refer high AML risk to analyst"),
				policyRule("aml_score < 6", "approve", "AML_CLEAR", "Clear low AML risk"),
			)},
			{actorMarcus, spec(
				policyRule("sanctions_hit == 1", "refer", "SANCTIONS_MATCH", "Confirmed sanctions/watchlist match"),
				policyRule("structuring == 1", "refer", "AML_STRUCTURING", "Sub-threshold structuring pattern"),
				policyRule("aml_score >= 6", "refer", "AML_HIGH", "AML risk above clearing band"),
				policyRule("aml_score < 6", "approve", "AML_CLEAR", "AML risk below clearing band"),
			)},
		}},
		{"policy:fraud", "Card Fraud Policy", "card-fraud", []policyVersion{
			{actorMarcus, spec(
				policyRule("fraud_p >= 80", "decline", "FRAUD_BLOCK", "Block high fraud probability"),
				policyRule("fraud_p >= 40", "refer", "FRAUD_REVIEW", "Refer to fraud analyst"),
				policyRule("fraud_p < 40", "approve", "FRAUD_PASS", "Allow low fraud probability"),
			)},
			{actorMarcus, spec(
				policyRule("fraud_p >= 80", "decline", "FRAUD_BLOCK", "Block high fraud probability"),
				policyRule("fraud_p >= 35", "refer", "FRAUD_REVIEW", "Refer to fraud analyst"),
				policyRule("fraud_p < 35", "approve", "FRAUD_PASS", "Allow low fraud probability"),
			)},
		}},
		{"policy:kyc", "KYC Onboarding Policy", "kyc-onboarding", []policyVersion{
			{actorMarcus, spec(
				policyRule("identity_conf < 60", "refer", "KYC_LOW_CONF", "Identity confidence below threshold"),
				policyRule("identity_conf >= 60", "approve", "KYC_CLEAR", "Identity verified"),
			)},
		}},
		{"policy:dispute", "Chargeback Triage Policy", "dispute-triage", []policyVersion{
			{actorMarcus, spec(
				policyRule("triage >= 50", "refer", "DISPUTE_REVIEW", "High triage score — route to disputes ops"),
				policyRule("triage < 50", "approve", "DISPUTE_AUTO_REFUND", "Below triage band — auto-refund"),
			)},
		}},
		{"policy:merchant", "Merchant Onboarding Policy", "merchant-onboarding", []policyVersion{
			{actorMarcus, spec(
				policyRule("uw_score >= 25", "refer", "UW_HIGH", "Underwriting score above the review gate"),
				policyRule("uw_score < 25", "approve", "UW_PASS", "Underwriting score below the review gate"),
			)},
		}},
		{"policy:collections", "Hardship Program Policy", "collections-hardship", []policyVersion{
			{actorMarcus, spec(
				policyRule("hardship_score >= 70", "refer", "HARDSHIP_ESCALATED", "Concession above authority — supervisor review"),
				policyRule("hardship_score >= 45", "approve", "HARDSHIP_PLAN", "Qualifies for a hardship plan"),
				policyRule("hardship_score < 45", "decline", "HARDSHIP_NOT_ELIGIBLE", "Does not meet hardship criteria"),
			)},
		}},
		{"policy:claim", "Claim Payout Policy", "claim-triage", []policyVersion{
			{actorMarcus, spec(
				policyRule("lapsed == 1", "decline", "POLICY_LAPSED", "Protection plan lapsed before the loss date"),
				policyRule("fraud_p >= 60", "refer", "CLAIM_FRAUD_REVIEW", "Abuse-pattern signals — adjuster review"),
				policyRule("fast_track == 1", "approve", "CLAIM_FAST_TRACK", "Low-value first claim — fast-track payment"),
				policyRule("amount_ratio > 0.5", "refer", "CLAIM_HIGH_SEVERITY", "High severity relative to coverage"),
				policyRule("amount_ratio <= 0.5", "approve", "CLAIM_PAY", "Within coverage — pay"),
			)},
		}},
		{"policy:payout", "Payout Release Policy", "payout-risk", []policyVersion{
			{actorMarcus, spec(
				policyRule(`action == "hold"`, "decline", "PAYOUT_HOLD", "Funds held — risk above release threshold"),
				policyRule(`action == "review"`, "refer", "PAYOUT_REVIEW", "Manual release review"),
				policyRule(`action == "release"`, "approve", "PAYOUT_RELEASE", "Released within risk appetite"),
			)},
		}},
		{"policy:limit", "Limit Increase Policy", "limit-increase", []policyVersion{
			{actorMarcus, spec(
				policyRule("risk < 20 && utilization < 0.6", "approve", "CLI_APPROVED", "Auto-approved limit increase"),
				policyRule("risk < 45", "refer", "CLI_MANUAL", "Manual credit-ops review"),
				policyRule("risk >= 45", "decline", "LOW_SCORE", "Risk score above CLI threshold"),
			)},
		}},
	}
}

func (s *seeder) policyActions(cfg *timeCursor) []action {
	var acts []action
	for _, p := range policySpecs() {
		acts = append(acts, action{at: cfg.step(4 * time.Minute), name: "policy " + p.name, run: func() {
			var res struct {
				PolicyID string `json:"policy_id"`
			}
			s.call(p.versions[0].by, http.MethodPost, "/v1/policies",
				map[string]any{"name": p.name, "flow_slug": p.slug}, &res)
			s.setID(p.tag, res.PolicyID)
		}})
		for vi, v := range p.versions {
			_ = vi
			acts = append(acts, action{at: cfg.step(6 * time.Minute), name: "policy version " + p.name, run: func() {
				s.call(v.by, http.MethodPost, "/v1/policies/"+s.id(p.tag)+"/versions",
					map[string]any{"spec": v.spec}, nil)
			}})
		}
	}
	return acts
}
