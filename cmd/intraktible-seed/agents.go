// SPDX-License-Identifier: AGPL-3.0-or-later

// The agent registry: seven production-ish agents with immutable version
// histories, eval suites with a deliberate red/green mix, and a month of run
// logs whose outputs are handcrafted per prompt (registered on the scripted
// provider, so every recorded run is a real provider round-trip).
package main

import (
	"net/http"
	"time"
)

type agentVersion struct {
	provider string
	model    string
	system   string
	by       string
}

type agentSpec struct {
	name     string
	schema   map[string]any
	tools    []string
	versions []agentVersion // oldest first; the last is the live config
}

func agentSpecs() []agentSpec {
	return []agentSpec{
		{
			name:   "aml-narrative",
			schema: objSchema(map[string]any{"narrative": map[string]any{"type": "string"}}),
			tools:  []string{"lookup_entity", "sanctions_check"},
			versions: []agentVersion{
				{"anthropic", "claude-haiku", "Draft a SAR narrative.", actorAva},
				{"anthropic", "claude-sonnet", "You write concise SAR narratives from transaction context.", actorPriya},
				{"anthropic", "claude-sonnet", "You write concise SAR narratives from transaction context, citing the triggering typology.", actorPriya},
			},
		},
		{
			name: "kyc-extract",
			schema: objSchema(map[string]any{
				"name": map[string]any{"type": "string"}, "dob": map[string]any{"type": "string"},
				"doc_number": map[string]any{"type": "string"},
			}),
			versions: []agentVersion{
				{"anthropic", "claude-haiku", "Extract KYC fields.", actorPriya},
				{"anthropic", "claude-haiku", "Extract structured KYC fields from a submitted identity document.", actorPriya},
			},
		},
		{
			name: "dispute-summarizer",
			schema: objSchema(map[string]any{
				"summary": map[string]any{"type": "string"}, "recommendation": map[string]any{"type": "string"},
			}),
			tools: []string{"lookup_transaction"},
			versions: []agentVersion{
				{"anthropic", "claude-haiku", "Summarize a dispute.", actorPriya},
				{"anthropic", "claude-haiku", "Summarize a cardholder dispute and recommend representment or refund.", actorPriya},
			},
		},
		{
			name: "fraud-explainer",
			versions: []agentVersion{
				{"anthropic", "claude-haiku", "Explain a fraud score.", actorPriya},
				{"anthropic", "claude-sonnet", "Explain a fraud model score in plain language for an analyst.", actorPriya},
			},
		},
		{
			name: "collections-planner",
			schema: objSchema(map[string]any{
				"plan_months": map[string]any{"type": "number"}, "rate_relief": map[string]any{"type": "number"},
				"summary": map[string]any{"type": "string"},
			}),
			tools: []string{"income_verification", "account_lookup"},
			versions: []agentVersion{
				{"anthropic", "claude-sonnet", "Propose a hardship payment plan.", actorPriya},
				{"anthropic", "claude-sonnet", "Propose a hardship payment plan within program guardrails from the verified income change and balance.", actorPriya},
			},
		},
		{
			name: "claims-adjuster-brief",
			schema: objSchema(map[string]any{
				"recommendation": map[string]any{"type": "string"}, "rationale": map[string]any{"type": "string"},
			}),
			tools: []string{"policy_lookup", "claim_history"},
			versions: []agentVersion{
				{"anthropic", "claude-haiku", "Draft an adjuster brief.", actorPriya},
				{"anthropic", "claude-sonnet", "Draft an adjuster brief for a purchase-protection claim: liability, coverage position, recommendation.", actorPriya},
			},
		},
		{
			name:  "merchant-memo",
			tools: []string{"registry_lookup", "web_research"},
			versions: []agentVersion{
				{"anthropic", "claude-opus", "Write an underwriting memo for a merchant application from its risk profile.", actorAva},
			},
		},
	}
}

// evalCases is each agent's offline-eval suite — a deliberate red/green mix (the
// failing cases are genuine capability gaps, so the eval page reads like a team
// mid-hardening).
func evalCases() map[string][]map[string]any {
	c := func(name, prompt, expect string) map[string]any {
		return map[string]any{"name": name, "prompt": prompt, "mode": "contains", "expect": expect}
	}
	return map[string][]map[string]any{
		"aml-narrative": {
			c("produces narrative", "Wire of $50,000 to a sanctioned region", "narrative"),
			c("handles structuring", "Structuring across 6 deposits under threshold", "narrative"),
			c("names the typology", "Layered transfers through 3 shell companies", "typology"),
		},
		"kyc-extract": {
			c("extracts passport", "Passport, DOB 1990-01-01", "doc_number"),
			c("extracts name field", "Driver license, Jane A. Doe", "name"),
		},
		"dispute-summarizer": {
			c("recommends action", "Chargeback for non-receipt, $210", "recommendation"),
			c("cites network reason code", "Visa 10.4 card-absent fraud dispute, $740", "10.4"),
		},
		"fraud-explainer": {
			c("explains drivers", "Score 88: high velocity, new device", "velocity"),
			c("cites feature attributions", "Score 91: explain with SHAP attributions", "SHAP"),
		},
		"collections-planner": {
			c("proposes a plan", "Income dropped 40% after layoff; balance $12,400", "plan_months"),
			c("includes rate relief", "Medical hardship, 3 missed payments", "rate_relief"),
		},
		"claims-adjuster-brief": {
			c("recommends pay/deny", "Stolen laptop claim, $1,900 against $3,000 coverage", "recommendation"),
			c("flags subrogation", "Damaged in courier transit, carrier at fault", "subrogation"),
		},
		"merchant-memo": {
			c("writes a memo", "Crypto exchange, projected $250k monthly volume", "Recommend"),
		},
	}
}

// runSeed is one recorded agent invocation: a handcrafted output specific to its
// prompt (registered on the scripted provider), or a designated provider error.
type runSeed struct {
	agent      string
	prompt     string
	hrs        float64 // hours before the anchor
	errText    string  // non-empty = the provider errors -> a recorded failed run
	text       string
	structured map[string]any
}

func runSeeds() []runSeed {
	return []runSeed{
		// aml-narrative — the busiest agent (every SAR referral drafts through it).
		{agent: "aml-narrative", prompt: "Wire of $50,000 to a high-risk jurisdiction", hrs: 6, structured: map[string]any{
			"narrative": "Customer initiated a single outbound wire of $50,000 to a beneficiary bank in a high-risk jurisdiction, against a 12-month average of under $3,000/month in account activity. No documented business relationship with the counterparty exists on file. Typology: high-risk corridor transfer. Recommend filing a SAR and applying 90-day enhanced monitoring."}},
		{agent: "aml-narrative", prompt: "Structuring pattern across 6 deposits", hrs: 20, structured: map[string]any{
			"narrative": "Six cash deposits between $8,600 and $9,700 were made across three branches within an 11-day window, each below the $10,000 CTR threshold, followed by a consolidated outbound transfer of $54,100. The branch rotation and amount clustering indicate deliberate threshold avoidance. Typology: structuring. Recommend filing a SAR covering the full deposit series."}},
		{agent: "aml-narrative", prompt: "Rapid pass-through funding from a shell entity", hrs: 34, structured: map[string]any{
			"narrative": "Funds totaling $130,000 arrived from an entity incorporated 6 weeks ago with no operating footprint, and 96% was forwarded to three unrelated accounts within 48 hours, leaving a near-zero balance. The account functions as a conduit rather than a business. Typology: pass-through / funnel account layering. Recommend a SAR and exit review of the relationship."}},
		{agent: "aml-narrative", prompt: "Cash deposits just under the reporting threshold", hrs: 78, structured: map[string]any{
			"narrative": "Recurring cash deposits of $9,000–$9,900 posted on 9 of the last 12 business days into a single account, with no corresponding business revenue declared at onboarding. The consistency of just-sub-threshold amounts is inconsistent with legitimate cash flow. Typology: threshold avoidance (structuring). Recommend a SAR and a source-of-funds request."}},
		{agent: "aml-narrative", prompt: "Cross-border transfer to a PEP-linked account", hrs: 96,
			errText: "provider timeout after 30s (upstream 504)"},
		{agent: "aml-narrative", prompt: "Round-tripping between affiliated accounts", hrs: 150, structured: map[string]any{
			"narrative": "A circular flow of $78,000 moved through three accounts under common beneficial ownership and returned to the originating account within five business days, net of $2,100 in fees, with no goods, services, or investment activity attached. Typology: round-tripping between affiliates. Recommend a SAR citing the absence of economic purpose."}},
		{agent: "aml-narrative", prompt: "Unusual surge in inbound remittances", hrs: 260, structured: map[string]any{
			"narrative": "Inbound remittance volume rose 9x month-over-month, arriving from 14 distinct senders across four countries with no prior relationship to the customer, followed by same-day ATM withdrawals of 80% of received value. The pattern matches a collection account. Typology: money mule / remittance aggregation. Recommend a SAR and account restriction pending contact."}},
		{agent: "aml-narrative", prompt: "Trade-based laundering via over-invoicing", hrs: 410, structured: map[string]any{
			"narrative": "Invoice values on seven trade payments exceed referenced market prices for the declared goods by 40–65%, with settlement routed through an intermediary in a third country unrelated to the shipping route. The mispricing transfers value beyond the goods exchanged. Typology: trade-based money laundering (over-invoicing). Recommend a SAR and trade-document review."}},
		// kyc-extract
		{agent: "kyc-extract", prompt: "Passport, DOB 1990-01-01", hrs: 18,
			structured: map[string]any{"name": "Amelia R. Novak", "dob": "1990-01-01", "doc_number": "P4811940US"}},
		{agent: "kyc-extract", prompt: "Utility bill, address verification", hrs: 44,
			structured: map[string]any{"name": "Marcus T. Oyelaran", "dob": "", "doc_number": "ACCT-118842-ELEC"}},
		{agent: "kyc-extract", prompt: "Company registration extract", hrs: 70,
			structured: map[string]any{"name": "Ollivanders Trading Ltd", "dob": "2011-06-14", "doc_number": "HRB 88291"}},
		{agent: "kyc-extract", prompt: "Driver license, expired", hrs: 96,
			errText: `schema validation failed: missing required field "doc_number"`},
		{agent: "kyc-extract", prompt: "Proof of funds statement", hrs: 122,
			structured: map[string]any{"name": "Ingrid Svensson", "dob": "1984-11-02", "doc_number": "STMT-8817-2026"}},
		{agent: "kyc-extract", prompt: "Residence permit, machine-readable zone", hrs: 300,
			structured: map[string]any{"name": "Yusuf Demir", "dob": "1979-03-22", "doc_number": "RP7724031TR"}},
		// dispute-summarizer
		{agent: "dispute-summarizer", prompt: "Chargeback for non-receipt, $210", hrs: 12, structured: map[string]any{
			"summary":        "Cardholder claims a $210 order never arrived; merchant tracking shows the parcel stalled at the origin facility and no proof of delivery exists.",
			"recommendation": "refund"}},
		{agent: "dispute-summarizer", prompt: "Duplicate charge dispute, $89", hrs: 32, structured: map[string]any{
			"summary":        "Two identical $89 authorizations posted 40 seconds apart with the same order id; the merchant confirms a gateway retry after a timeout.",
			"recommendation": "refund"}},
		{agent: "dispute-summarizer", prompt: "Fraudulent transaction claim, $740", hrs: 52,
			errText: "rate limited by provider (429); retry after 12s"},
		{agent: "dispute-summarizer", prompt: "Subscription not canceled, $29", hrs: 72, structured: map[string]any{
			"summary":        "Cardholder canceled on the 3rd but was billed $29 on the 12th; the merchant cancellation log confirms the request preceded the billing cycle.",
			"recommendation": "refund"}},
		{agent: "dispute-summarizer", prompt: "Quality dispute, $1,200", hrs: 92, structured: map[string]any{
			"summary":        "Cardholder disputes a $1,200 furniture charge citing damage on arrival; the merchant holds a signed delivery acceptance and offered repair, which was refused.",
			"recommendation": "representment"}},
		{agent: "dispute-summarizer", prompt: "Card-absent fraud, $460, device mismatch", hrs: 210, structured: map[string]any{
			"summary":        "A $460 card-absent charge came from a device and IP never seen on the account while the cardholder transacted in-store in another city within the hour.",
			"recommendation": "refund"}},
		// fraud-explainer
		{agent: "fraud-explainer", prompt: "Score 88: high velocity, new device", hrs: 4,
			text: "The 88 is driven primarily by velocity — 9 authorizations in the past hour against a baseline of 1–2 per day — compounded by a first-seen device fingerprint. Amount contributes little. Velocity alone accounts for roughly two-thirds of the elevation; recommend step-up authentication before the next approval."},
		{agent: "fraud-explainer", prompt: "Score 41: mid risk, mismatched geo", hrs: 13,
			text: "A mid-band 41: the dominant driver is the geo mismatch between the IP country and the card home region. Velocity and device history are clean, which holds the score below the 80 block line; passive monitoring is sufficient."},
		{agent: "fraud-explainer", prompt: "Score 12: low risk, trusted device", hrs: 22,
			text: "Low risk at 12: a recognized device with 14 months of history, an in-pattern amount, and no velocity signal. No single feature contributes materially — approve without friction."},
		{agent: "fraud-explainer", prompt: "Score 92: account takeover signals", hrs: 31,
			errText: "context window exceeded: 214k tokens > 200k limit"},
		{agent: "fraud-explainer", prompt: "Score 33: card-present, recurring merchant", hrs: 40,
			text: "A 33 on a card-present transaction at a recurring merchant: the physical read and merchant familiarity suppress the modest amount deviation. Below the review band — no analyst action required."},
		{agent: "fraud-explainer", prompt: "Score 76: ticket 9x average, VPN exit node", hrs: 170,
			text: "The 76 combines a ticket 9x the account average with an IP flagged as a commercial VPN exit node. Either signal alone lands mid-band; together they cross the review threshold. Recommend a hold pending cardholder confirmation."},
		// collections-planner
		{agent: "collections-planner", prompt: "Income dropped 40% after layoff; balance $12,400", hrs: 10, structured: map[string]any{
			"plan_months": 12, "rate_relief": 0.5,
			"summary": "Verified 40% income reduction after involuntary layoff. A 12-month plan at 50% rate relief brings the payment to 31% of current disposable income, within program guardrails."}},
		{agent: "collections-planner", prompt: "Medical hardship, 3 missed payments, balance $6,300", hrs: 36, structured: map[string]any{
			"plan_months": 9, "rate_relief": 0.5,
			"summary": "Documented medical event with three missed payments. Nine months at 50% relief cures the arrears by month four while staying under the 36% payment-to-income cap."}},
		{agent: "collections-planner", prompt: "Divorce settlement pending, income halved", hrs: 60,
			errText: "provider timeout after 30s (upstream 504)"},
		{agent: "collections-planner", prompt: "Seasonal worker, income resumes in March", hrs: 130, structured: map[string]any{
			"plan_months": 6, "rate_relief": 0.25,
			"summary": "Income verified to resume in March; a 6-month bridge at 25% relief with a deferred first payment aligns the step-up with the documented re-employment date."}},
		{agent: "collections-planner", prompt: "Small-business owner, revenue down 60%", hrs: 320, structured: map[string]any{
			"plan_months": 12, "rate_relief": 0.5,
			"summary": "Business bank feeds confirm a 60% revenue decline. The 12-month/50% concession exceeds the standard authority band — route to supervisor countersign per program guardrails."}},
		// claims-adjuster-brief
		{agent: "claims-adjuster-brief", prompt: "Stolen laptop claim, $1,900 against $3,000 coverage", hrs: 16, structured: map[string]any{
			"recommendation": "pay",
			"rationale":      "Police report and purchase receipt are consistent; the $1,900 claim sits at 63% of coverage with no prior claims in 24 months. Severity is high but the documentation is clean."}},
		{agent: "claims-adjuster-brief", prompt: "Cracked phone screen, $240, third claim this year", hrs: 58, structured: map[string]any{
			"recommendation": "refer to SIU",
			"rationale":      "Third claim in twelve months on the same line: individually low-value, but the frequency exceeds the abuse threshold. Hold payment pending a claim-history review."}},
		{agent: "claims-adjuster-brief", prompt: "Damaged in courier transit, carrier at fault", hrs: 110, structured: map[string]any{
			"recommendation": "pay and subrogate",
			"rationale":      "Transit damage is a covered peril and the carrier scan log places the damage in their custody. Pay the insured now and pursue subrogation against the courier."}},
		{agent: "claims-adjuster-brief", prompt: "Claim on a policy 12 days old, near coverage limit", hrs: 140,
			errText: "tool call failed: claim_history returned 503"},
		{agent: "claims-adjuster-brief", prompt: "Water-damaged headphones, receipt attached", hrs: 380, structured: map[string]any{
			"recommendation": "pay",
			"rationale":      "Receipt validates ownership and price; accidental liquid damage is covered, the amount is well under limit, and this is the customer first claim."}},
		// merchant-memo — opus, unpriced in the demo price table (usage without cost).
		{agent: "merchant-memo", prompt: "Crypto exchange, projected $250k monthly volume", hrs: 26,
			text: "Underwriting memo — crypto exchange, projected $250k/month. MCC 6051 lands in the enhanced-review tier: MSB registration verified in two of three operating states, chargeback exposure modeled low, funding-source risk elevated. Recommend approval with a 10% rolling reserve, a $100k monthly cap for the first 90 days, and quarterly licensing re-checks."},
		{agent: "merchant-memo", prompt: "High-risk MCC 7995, offshore directors", hrs: 88,
			text: "Underwriting memo — MCC 7995 (gambling) with two of four directors resident offshore. The registry extract confirms beneficial ownership, but the payout jurisdiction lacks a mutual enforcement treaty. Recommend decline at standard terms; reconsider only with a domestic guarantor entity and a 15% rolling reserve."},
		{agent: "merchant-memo", prompt: "Established grocery chain, 12 locations", hrs: 200,
			text: "Underwriting memo — 12-location grocery chain with nine years of processing history and a 0.02% chargeback ratio. MCC 5411 is low-risk and volume is seasonally stable. Recommend approval at standard MDR with no reserve, on an annual review cycle."},
	}
}

// runningPrompt is the async run left mid-flight in the exported history: the
// scripted provider blocks it until after export, so the seed carries a genuine
// "running" run (the replayed server's crash-recovery re-enqueues it at boot).
const runningPrompt = "Nutraceutical subscription merchant, 2.9% chargeback rate"

// aiNodeOutputs registers the decide-time AI-node completions: one handcrafted
// output per node prompt, matching the serving agent's output schema.
func (s *seeder) registerAINodeOutputs() {
	s.prov.text(promptAdverseAction,
		"Primary adverse-action drivers: the debt-to-income ratio exceeds the program threshold and revolving utilization is elevated for the requested line. Cite DTI_TOO_HIGH as the principal reason; score and utilization are contributing factors, not independent grounds.")
	s.prov.object(promptSAR, map[string]any{
		"narrative": "Automated screening flagged this transfer for analyst review: the composite risk score and corridor profile exceed the clearing band. Draft narrative covers originator history, counterparty verification status, and the triggering typology for the SAR filing decision.",
	})
	s.prov.object(promptKYCExtract, map[string]any{
		"name": "As submitted on the identity document", "dob": "1985-01-01", "doc_number": "DOC-EXTRACTED-001",
	})
	s.prov.text(promptFraudExplain,
		"The score is driven by short-window authorization velocity and the device reputation percentile; ticket size relative to the account average is a secondary contributor. Recommend the banded action — the drivers are behavioral, not merchant-specific.")
	s.prov.object(promptDispute, map[string]any{
		"summary":        "Cardholder dispute triaged from intake: reason code, liability assignment, and evidence requirements resolved from network rules for the disposition below.",
		"recommendation": "review evidence before representment",
	})
	s.prov.text(promptMerchantMemo,
		"Underwriting memo: projected volume, MCC tier, and cross-border exposure summarized against program appetite. Recommendation follows the underwriting score band; see the referral queue for the reviewer decision trail.")
	s.prov.object(promptHardship, map[string]any{
		"plan_months": 6, "rate_relief": 0.25,
		"summary": "Verified income change and balance qualify for the standard relief band; terms proposed from the plan table pending the program gate.",
	})
	s.prov.object(promptAdjusterBrief, map[string]any{
		"recommendation": "per triage band",
		"rationale":      "Coverage position, abuse-model probability, and severity ratio summarized for the adjuster; documentation checklist attached to the case.",
	})
}

// seedAgentActions returns the timeline actions for agent definitions (spread
// through the configuration window), eval suites, and the run log.
func (s *seeder) agentConfigActions(cfg *timeCursor) []action {
	var acts []action
	for _, spec := range agentSpecs() {
		for vi, v := range spec.versions {
			at := cfg.step(4 * time.Minute)
			acts = append(acts, action{at: at, name: "agent " + spec.name, run: func() {
				body := map[string]any{
					"name": spec.name, "provider": v.provider, "model": v.model, "system": v.system,
				}
				if spec.schema != nil {
					body["schema"] = spec.schema
				}
				if len(spec.tools) > 0 {
					body["tools"] = spec.tools
				}
				s.call(v.by, http.MethodPost, "/v1/agents", body, nil)
			}})
			_ = vi
		}
		at := cfg.step(2 * time.Minute)
		acts = append(acts, action{at: at, name: "evals " + spec.name, run: func() {
			s.call(actorPriya, http.MethodPut, "/v1/agents/"+spec.name+"/evals",
				map[string]any{"cases": evalCases()[spec.name]}, nil)
		}})
	}
	return acts
}

// agentRunActions schedules the recorded run log across the traffic window and
// registers every prompt's scripted output (or designated failure).
func (s *seeder) agentRunActions(anchor time.Time) []action {
	runners := []string{actorDiego, actorPriya, actorMarcus}
	var acts []action
	for i, r := range runSeeds() {
		switch {
		case r.errText != "":
			s.prov.fail(r.prompt, r.errText)
		case r.structured != nil:
			s.prov.object(r.prompt, r.structured)
		default:
			s.prov.text(r.prompt, r.text)
		}
		runner := runners[i%len(runners)]
		acts = append(acts, action{at: anchor.Add(-time.Duration(r.hrs * float64(time.Hour))), name: "run " + r.agent, run: func() {
			var res struct {
				RunID  string `json:"run_id"`
				Status string `json:"status"`
				Error  string `json:"error"`
			}
			s.call(runner, http.MethodPost, "/v1/agents/"+r.agent+"/run", map[string]any{"prompt": r.prompt}, &res)
			wantFailed := r.errText != ""
			if wantFailed && res.Status != "failed" {
				fatalf("run %q: expected a failed run, got %q", r.prompt, res.Status)
			}
			if !wantFailed && res.Status != "completed" {
				fatalf("run %q: expected completed, got %q (%s)", r.prompt, res.Status, res.Error)
			}
		}})
	}
	return acts
}

// startRunningRun fires the perpetually-running async run: StartRun records the
// AgentRunStarted event synchronously and the worker then parks inside the
// scripted provider's gate, so the exported history carries the run mid-flight
// ("running"). The replayed server's crash recovery re-enqueues it at boot —
// exactly what a real interrupted deployment does.
func (s *seeder) startRunningRun() {
	s.prov.block(runningPrompt, "seed export interrupted the run")
	var res struct {
		RunID string `json:"run_id"`
	}
	s.call(actorPriya, http.MethodPost, "/v1/agents/merchant-memo/run",
		map[string]any{"prompt": runningPrompt, "async": true}, &res)
	if res.RunID == "" {
		fatalf("running run: no run_id returned")
	}
}
