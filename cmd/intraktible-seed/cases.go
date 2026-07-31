// SPDX-License-Identifier: AGPL-3.0-or-later

// Case work: every manual_review referral opens a REAL case (the engine's
// escalation event, materialized by the Case Manager's projector). Thirty of
// them are worked in detail — assignees, notes, status transitions, SLA
// breaches — mirroring the retired seed's queue; the rest of the backlog is
// triaged by periodic hygiene passes so the queue reads like a staffed team's,
// and a scheduled SLA sweep records due-soon reminders and breaches.
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/e6qu/intraktible/case-manager/cases"
)

type caseNote struct {
	author string
	text   string
	hrs    float64
}

// caseConfigActions publishes the governed operational contract before any
// decision traffic can open a review task. Decision-escalated cases therefore
// resolve the exact definition version that preceded their event in the log.
func (s *seeder) caseConfigActions(cfg *timeCursor) []action {
	caseTypes := []struct {
		key, name string
	}{
		{"aml_alert", "AML alert"},
		{"claim_review", "Claim review"},
		{"credit_review", "Credit review"},
		{"dispute", "Dispute review"},
		{"fraud_review", "Fraud review"},
		{"hardship_review", "Hardship review"},
		{"kyc_review", "KYC review"},
		{"limit_review", "Limit review"},
		{"merchant_review", "Merchant review"},
		{"payout_review", "Payout review"},
	}
	var actions []action
	for _, item := range caseTypes {
		at := cfg.step(2 * time.Minute)
		actions = append(actions, action{at: at, name: "case type " + item.key, run: func() {
			assistAutomations := []map[string]any{}
			if item.key == "aml_alert" {
				assistAutomations = append(assistAutomations, map[string]any{
					"key": "opening_summary", "kind": "summary",
					"template_id": "governed-case-copilot", "environment": "production",
					"evidence_requirements": []string{"supporting_record"},
				})
			}
			s.call(actorPriya, http.MethodPost, "/v1/case-types", map[string]any{
				"key": item.key, "name": item.name, "initial_state": "needs_review",
				"fields": []map[string]any{
					{"key": "applicant", "label": "Applicant", "kind": "string", "pii": true,
						"read_by": []string{"operator", "approver", "admin"}},
					{"key": "risk_score", "label": "Risk score", "kind": "number"},
				},
				"transitions": []map[string]any{
					{"from": "needs_review", "to": "in_progress",
						"roles": []string{"operator", "editor", "approver", "admin"}},
					{"from": "in_progress", "to": "completed",
						"roles": []string{"operator", "editor", "approver", "admin"}},
					{"from": "in_progress", "to": "resolved",
						"roles": []string{"operator", "editor", "approver", "admin"}},
				},
				"dispositions": []map[string]any{
					{"key": "clear", "label": "Clear", "reason_codes": []string{"verified"},
						"terminal_state": "resolved"},
					{"key": "escalate", "label": "Escalate", "reason_codes": []string{"risk_confirmed"},
						"terminal_state": "resolved", "requires_second_review": true},
				},
				"priorities": []string{"normal", "high", "critical"},
				"service_calendar": map[string]any{
					"timezone": "UTC", "weekdays": []int{1, 2, 3, 4, 5},
					"start_hour": 8, "end_hour": 18, "sla_hours": 40, "escalation_hours": 8,
				},
				"evidence_requirements": []map[string]any{
					{"key": "supporting_record", "label": "Supporting record",
						"kinds": []string{"decision", "attachment"}},
				},
				"assist_automations": assistAutomations,
				"layouts": []map[string]any{
					{"role": "operator", "sections": []string{"risk_score", "applicant"},
						"editable": []string{"risk_score"}},
					{"role": "admin", "sections": []string{"risk_score", "applicant"},
						"editable": []string{"risk_score"}},
					{"role": "viewer", "sections": []string{"risk_score"}},
				},
			}, nil)
		}})
	}
	queues := []map[string]any{
		{"key": "aml", "name": "AML investigations", "case_types": []string{"aml_alert"},
			"required_skills": []string{"aml"}, "capacity": 200,
			"assist_automations": []map[string]any{{
				"key": "queue_priority", "kind": "prioritization",
				"template_id": "governed-case-copilot", "environment": "production",
				"evidence_requirements": []string{"supporting_record"},
			}}},
		{"key": "general_review", "name": "General review", "case_types": []string{
			"claim_review", "credit_review", "dispute", "fraud_review", "hardship_review",
			"kyc_review", "limit_review", "merchant_review", "payout_review",
		}, "required_skills": []string{"case_review"}, "capacity": 500},
	}
	for _, queue := range queues {
		key := queue["key"].(string)
		at := cfg.step(2 * time.Minute)
		actions = append(actions, action{at: at, name: "case queue " + key, run: func() {
			s.call(actorPriya, http.MethodPut, "/v1/case-queues/"+key, queue, nil)
		}})
	}
	reviewers := []map[string]any{
		{"actor": actorDiego, "skills": []string{"aml", "case_review"}, "jurisdictions": []string{},
			"capacity": 40, "active": true},
		{"actor": actorMarcus, "skills": []string{"aml", "case_review"}, "jurisdictions": []string{},
			"capacity": 20, "active": true},
		{"actor": actorPriya, "skills": []string{"case_review"}, "jurisdictions": []string{},
			"capacity": 12, "active": true},
	}
	for _, reviewer := range reviewers {
		actor := reviewer["actor"].(string)
		at := cfg.step(2 * time.Minute)
		actions = append(actions, action{at: at, name: "case reviewer " + actor, run: func() {
			s.call(actorAva, http.MethodPut, "/v1/case-reviewers/"+actor, reviewer, nil)
		}})
	}
	return actions
}

// caseSeed is one worked case, sourced from a real referred (or suspended)
// decision of its flow.
type caseSeed struct {
	tag        string
	name       string
	slug       string
	status     string // needs_review | in_progress | completed
	assignee   string
	suspended  bool
	notes      []caseNote
	createdHrs float64 // pick a source decision at least this old
	updatedHrs float64 // final touch (status change / resolution)
}

func caseSeeds() []caseSeed {
	return []caseSeed{
		{tag: "case:northwind", name: "Northwind Capital", slug: "credit-decision", status: "needs_review",
			notes:      []caseNote{{actorDiego, "Requested two recent pay stubs and bank statements.", 20}},
			createdHrs: 48, updatedHrs: 20},
		{tag: "case:acme", name: "Acme Imports LLC", slug: "aml-screening", status: "in_progress", assignee: actorDiego,
			notes: []caseNote{
				{actorDiego, "Cross-border wire to a high-risk jurisdiction; pulling counterparty KYC.", 30},
				{actorMarcus, "@ava.chen escalate to SAR drafting if the counterparty stays unverified.", 6},
			},
			createdHrs: 70, updatedHrs: 6},
		{tag: "case:globex", name: "Globex Lending", slug: "kyc-onboarding", status: "in_progress", assignee: actorDiego,
			notes:      []caseNote{{actorDiego, "PEP match on a beneficial owner; awaiting adverse-media disposition.", 54}},
			createdHrs: 96, updatedHrs: 54},
		{tag: "case:initech", name: "Initech Finance", slug: "credit-decision", status: "completed", assignee: actorDiego,
			notes:      []caseNote{{actorDiego, "Approved at $18k limit after income verification.", 12}},
			createdHrs: 60, updatedHrs: 12},
		{tag: "case:umbrella-card", name: "Umbrella Card 4821", slug: "card-fraud", status: "needs_review",
			createdHrs: 8, updatedHrs: 8},
		{tag: "case:soylent", name: "Soylent Merchant Co", slug: "merchant-onboarding", status: "in_progress", assignee: actorMarcus,
			notes:      []caseNote{{actorMarcus, "High-risk MCC; requesting processing history and chargeback ratios.", 18}},
			createdHrs: 40, updatedHrs: 18},
		{tag: "case:wayne-dispute", name: "Wayne Disputes #5512", slug: "dispute-triage", status: "in_progress", assignee: actorDiego,
			notes:      []caseNote{{actorDiego, "Compelling evidence on file; preparing representment package.", 26}},
			createdHrs: 36, updatedHrs: 26},
		{tag: "case:stark", name: "Stark Industries", slug: "credit-decision", status: "in_progress", assignee: actorDiego,
			notes:      []caseNote{{actorDiego, "Awaiting guarantor financials.", 14}},
			createdHrs: 50, updatedHrs: 14},
		{tag: "case:hooli-aml", name: "Hooli Payments", slug: "aml-screening", status: "completed", assignee: actorDiego,
			notes:      []caseNote{{actorDiego, "Structuring pattern explained by payroll batch; cleared — no SAR.", 90}},
			createdHrs: 140, updatedHrs: 90},
		{tag: "case:pied-piper", name: "Pied Piper Card 9913", slug: "card-fraud", status: "completed", assignee: actorDiego,
			notes:      []caseNote{{actorDiego, "Account takeover confirmed; card blocked and reissued.", 110}},
			createdHrs: 118, updatedHrs: 110},
		{tag: "case:cyberdyne", name: "Cyberdyne Onboarding", slug: "kyc-onboarding", status: "needs_review",
			createdHrs: 10, updatedHrs: 10},
		{tag: "case:tyrell", name: "Tyrell Merchant", slug: "merchant-onboarding", status: "needs_review",
			notes:      []caseNote{{actorMarcus, "Crypto MCC requires enhanced underwriting; chasing licensing docs.", 100}},
			createdHrs: 150, updatedHrs: 100},
		{tag: "case:oscorp", name: "Oscorp Disputes #7740", slug: "dispute-triage", status: "completed", assignee: actorDiego,
			notes:      []caseNote{{actorDiego, "Low value, product-not-received; refunded.", 160}},
			createdHrs: 180, updatedHrs: 160},
		{tag: "case:aperture", name: "Aperture Capital", slug: "credit-decision", status: "needs_review",
			createdHrs: 5, updatedHrs: 5},
		{tag: "case:vandelay", name: "Vandelay Industries", slug: "collections-hardship", status: "in_progress", assignee: actorPriya,
			notes:      []caseNote{{actorPriya, "Concession above my authority band — needs supervisor countersign.", 22}},
			createdHrs: 44, updatedHrs: 22},
		{tag: "case:bluth", name: "Bluth Household", slug: "collections-hardship", status: "completed", assignee: actorDiego,
			notes:      []caseNote{{actorDiego, "Medical hardship documented; 12-month plan countersigned by Marcus.", 130}},
			createdHrs: 170, updatedHrs: 130},
		{tag: "case:okafor", name: "Claim CLM-2214 · Okafor", slug: "claim-triage", status: "in_progress", assignee: actorDiego,
			notes:      []caseNote{{actorDiego, "Police report attached; verifying purchase date vs policy start.", 9}},
			createdHrs: 28, updatedHrs: 9},
		{tag: "case:marchetti", name: "Claim CLM-2190 · Marchetti", slug: "claim-triage", status: "needs_review",
			createdHrs: 100, updatedHrs: 7},
		{tag: "case:watanabe", name: "Claim CLM-2145 · Watanabe", slug: "claim-triage", status: "completed", assignee: actorMarcus,
			notes:      []caseNote{{actorMarcus, "Severity high but documentation clean; paid at coverage limit.", 200}},
			createdHrs: 240, updatedHrs: 200},
		{tag: "case:hooli-payout", name: "Hooli Marketplace Payout", slug: "payout-risk", status: "in_progress", assignee: actorDiego,
			notes:      []caseNote{{actorDiego, "Volume spike matches their holiday sale; verifying inventory.", 11}},
			createdHrs: 26, updatedHrs: 11},
		{tag: "case:wayne-payout", name: "Wayne Home Goods Payout", slug: "payout-risk", status: "completed", assignee: actorDiego,
			notes:      []caseNote{{actorDiego, "Shipping manifests reconcile; released.", 150}},
			createdHrs: 190, updatedHrs: 150},
		{tag: "case:cli-7719", name: "CLI · Card 7719", slug: "limit-increase", status: "needs_review",
			createdHrs: 15, updatedHrs: 15},
		{tag: "case:massive", name: "Massive Dynamic", slug: "aml-screening", status: "needs_review",
			createdHrs: 12, updatedHrs: 12},
		{tag: "case:duff", name: "Duff Distribution", slug: "aml-screening", status: "in_progress", assignee: actorMarcus,
			notes:      []caseNote{{actorMarcus, "Name-only OFAC match; requesting DOB corroboration before filing.", 30}},
			createdHrs: 110, updatedHrs: 30},
		{tag: "case:sirius", name: "Sirius Cybernetics Card 5150", slug: "card-fraud", status: "in_progress", assignee: actorDiego,
			notes:      []caseNote{{actorDiego, "Cardholder unreachable; second contact attempt logged.", 5}},
			createdHrs: 30, updatedHrs: 5},
		{tag: "case:gringotts", name: "Gringotts Onboarding", slug: "kyc-onboarding", status: "completed", assignee: actorDiego,
			notes:      []caseNote{{actorDiego, "Source-of-funds letter satisfies EDD; verified.", 210}},
			createdHrs: 260, updatedHrs: 210},
		{tag: "case:prestige", name: "Prestige Worldwide Disputes #8103", slug: "dispute-triage", status: "needs_review",
			createdHrs: 18, updatedHrs: 18},
		// The three suspended decisions each have a case a reviewer resumes them from.
		{tag: "case:wonka", name: "Wonka Credit Application", slug: "credit-decision", status: "needs_review",
			suspended: true, createdHrs: 2, updatedHrs: 2},
		{tag: "case:ollivanders", name: "Ollivanders Onboarding", slug: "kyc-onboarding", status: "needs_review",
			assignee: actorDiego, suspended: true,
			notes:      []caseNote{{actorDiego, "Requested certified translation of the registry extract.", 3}},
			createdHrs: 6, updatedHrs: 3},
		{tag: "case:umbrella-payout", name: "Umbrella Wellness Payout", slug: "payout-risk", status: "needs_review",
			suspended: true, createdHrs: 4, updatedHrs: 4},
	}
}

// designateCaseSources binds each worked case to a traffic slot: the youngest
// unused referred slot of its flow that is at least createdHrs old (or the
// flow's designated suspended slot), and stamps the case's subject name onto
// the slot's input so the auto-opened case carries it.
func (s *seeder) designateCaseSources(slots []*decideSlot, anchor time.Time) map[string]*decideSlot {
	bySeed := map[string]*decideSlot{}
	for i := range caseSeeds() {
		cs := caseSeeds()[i]
		var picked *decideSlot
		// Newest-first scan, like the retired picker.
		for k := len(slots) - 1; k >= 0; k-- {
			slot := slots[k]
			if slot.slug != cs.slug || slot.designated || slot.fail {
				continue
			}
			if cs.suspended != slot.suspend {
				continue
			}
			if !cs.suspended && slot.band != "refer" {
				continue
			}
			if anchor.Sub(slot.at).Hours() < cs.createdHrs {
				continue
			}
			picked = slot
			break
		}
		if picked == nil {
			fatalf("case %s: no unused %s decision of %s older than %.0fh", cs.tag,
				map[bool]string{true: "suspended", false: "referred"}[cs.suspended], cs.slug, cs.createdHrs)
		}
		picked.designated = true
		picked.company = cs.name
		bySeed[cs.tag] = picked
	}
	return bySeed
}

type caseView struct {
	CaseID    string          `json:"case_id"`
	Status    string          `json:"status"`
	SourceID  string          `json:"source_decision_id"`
	CreatedAt time.Time       `json:"created_at"`
	Notes     json.RawMessage `json:"notes"`
}

// findCaseByDecision locates the auto-opened case for a decision id.
func (s *seeder) findCaseByDecision(decisionID string) caseView {
	var res struct {
		Cases []caseView `json:"cases"`
	}
	s.call(actorAva, http.MethodGet, "/v1/cases", nil, &res)
	for _, c := range res.Cases {
		if c.SourceID == decisionID {
			return c
		}
	}
	fatalf("no case opened for decision %s", decisionID)
	return caseView{}
}

// caseWorkActions schedules each worked case's assignment, notes, and status
// transitions at believable offsets from its (real) opening time.
func (s *seeder) caseWorkActions(bySeed map[string]*decideSlot, anchor time.Time) []action {
	var acts []action
	for i := range caseSeeds() {
		cs := caseSeeds()[i]
		slot := bySeed[cs.tag]
		after := func(min time.Time, t time.Time) time.Time {
			if t.After(min) {
				return t
			}
			return min.Add(10 * time.Minute)
		}
		cursor := slot.at.Add(35 * time.Minute)
		locate := func() string {
			if slot.caseID == "" {
				c := s.findCaseByDecision(slot.decisionID)
				slot.caseID = c.CaseID
				s.setID(cs.tag, c.CaseID)
			}
			return slot.caseID
		}
		if cs.assignee != "" {
			at := cursor
			acts = append(acts, action{at: at, name: "assign " + cs.tag, run: func() {
				s.call(actorAva, http.MethodPost, "/v1/cases/"+locate()+"/assign",
					map[string]any{"assignee": cs.assignee}, nil)
			}})
			cursor = cursor.Add(15 * time.Minute)
		}
		if cs.status == "in_progress" || cs.status == "completed" {
			by := cs.assignee
			if by == "" {
				by = actorDiego
			}
			at := cursor
			acts = append(acts, action{at: at, name: "start " + cs.tag, run: func() {
				s.call(by, http.MethodPost, "/v1/cases/"+locate()+"/status",
					map[string]any{"status": "in_progress"}, nil)
			}})
			cursor = cursor.Add(15 * time.Minute)
		}
		for _, n := range cs.notes {
			at := after(cursor, anchor.Add(-time.Duration(n.hrs*float64(time.Hour))))
			acts = append(acts, action{at: at, name: "note " + cs.tag, run: func() {
				s.call(n.author, http.MethodPost, "/v1/cases/"+locate()+"/notes",
					map[string]any{"text": n.text}, nil)
			}})
			cursor = at.Add(5 * time.Minute)
		}
		if cs.status == "completed" {
			by := cs.assignee
			if by == "" {
				by = actorDiego
			}
			at := after(cursor, anchor.Add(-time.Duration(cs.updatedHrs*float64(time.Hour))))
			acts = append(acts, action{at: at, name: "complete " + cs.tag, run: func() {
				s.call(by, http.MethodPost, "/v1/cases/"+locate()+"/status",
					map[string]any{"status": "completed"}, nil)
			}})
		}
	}
	return acts
}

// enterpriseCaseActions leaves one complete governed review trail in the demo:
// exact type pin, automatic route, evidence and immutable attachment metadata,
// reasoned disposition, independent disagreement, and reviewer feedback.
func (s *seeder) enterpriseCaseActions(anchor time.Time) []action {
	var caseID string
	return []action{
		{at: anchor.Add(-5 * time.Hour), name: "open governed QA case", run: func() {
			var result struct {
				CaseID string `json:"case_id"`
			}
			s.call(actorAva, http.MethodPost, "/v1/cases", map[string]any{
				"company_name": "Contoso Payments · QA sample", "case_type": "aml_alert",
				"priority": "high", "jurisdiction": "us", "subject": "customer/contoso-qa",
				"context": map[string]any{"applicant": "Contoso Payments", "risk_score": 82},
			}, &result)
			caseID = result.CaseID
			s.setID("case:enterprise-qa", caseID)
		}},
		{at: anchor.Add(-4*time.Hour - 50*time.Minute), name: "route governed QA case", run: func() {
			s.call(actorAva, http.MethodPost, "/v1/cases/"+caseID+"/route", nil, nil)
		}},
		{at: anchor.Add(-4*time.Hour - 40*time.Minute), name: "start governed QA case", run: func() {
			s.call(actorDiego, http.MethodPost, "/v1/cases/"+caseID+"/status",
				map[string]any{"status": "in_progress"}, nil)
		}},
		{at: anchor.Add(-4 * time.Hour), name: "link governed case evidence", run: func() {
			s.call(actorDiego, http.MethodPost, "/v1/cases/"+caseID+"/evidence", map[string]any{
				"evidence_id": "contoso-decision", "requirement": "supporting_record",
				"kind": "decision", "subject_type": "decision", "subject_id": "contoso-source",
				"label": "Original AML screening decision",
			}, nil)
		}},
		{at: anchor.Add(-3*time.Hour - 55*time.Minute), name: "reconcile governed case assists", run: func() {
			var view cases.CaseView
			s.call(actorDiego, http.MethodGet, "/v1/cases/"+caseID, nil, &view)
			prompt, err := json.Marshal(struct {
				Trust        string               `json:"trust"`
				CaseID       string               `json:"case_id"`
				CaseType     string               `json:"case_type"`
				Jurisdiction string               `json:"jurisdiction,omitempty"`
				Context      json.RawMessage      `json:"case_context,omitempty"`
				Evidence     []cases.EvidenceLink `json:"evidence"`
			}{
				Trust: "governed", CaseID: view.CaseID, CaseType: view.CaseType,
				Jurisdiction: view.Jurisdiction, Context: view.Context,
				Evidence: view.Evidence,
			})
			if err != nil {
				fatalf("encode governed case-assist prompt: %v", err)
			}
			s.prov.object(string(prompt), map[string]any{
				"suggestion": map[string]any{
					"summary":  "The screening alert remains open for analyst review; the original decision is the governed supporting record.",
					"priority": "high",
				},
				"citations": []map[string]any{{
					"evidence_id": "contoso-decision",
					"claim":       "The original AML screening decision is linked as supporting evidence.",
				}},
				"confidence": 0.91,
				"limitations": []string{
					"The suggestion does not replace the accountable reviewer's disposition.",
				},
			})
			summary, err := s.srv.ReconcileCaseAssists(context.Background())
			if err != nil {
				fatalf("reconcile governed case assists: %v", err)
			}
			if summary.AssistEligible != 2 {
				fatalf("reconcile governed case assists: eligible=%d, want 2", summary.AssistEligible)
			}
			var lastStatuses []string
			for attempt := 0; attempt < 1000; attempt++ {
				var response struct {
					Assists []struct {
						AssistID     string `json:"assist_id"`
						Status       string `json:"status"`
						PolicySource *struct {
							PolicyKey string `json:"policy_key"`
						} `json:"policy_source,omitempty"`
					} `json:"assists"`
				}
				s.call(
					actorDiego, http.MethodGet,
					"/v1/cases/"+caseID+"/agent-assists", nil, &response,
				)
				completed := 0
				lastStatuses = lastStatuses[:0]
				for _, assist := range response.Assists {
					lastStatuses = append(lastStatuses, assist.Status)
					if assist.PolicySource != nil && assist.Status == "completed" {
						completed++
						s.setID(
							"assist:"+assist.PolicySource.PolicyKey,
							assist.AssistID,
						)
					}
				}
				if completed == 2 {
					return
				}
				// The durable workers run independently of the scripted clock.
				// Yield enough real execution time under a CPU-constrained build
				// without hammering the in-process API thousands of times per
				// second; the bounded ten-second wait still fails loudly.
				time.Sleep(10 * time.Millisecond)
			}
			fatalf(
				"policy-requested case assists did not complete; last statuses=%v",
				lastStatuses,
			)
		}},
		{at: anchor.Add(-3*time.Hour - 45*time.Minute), name: "register governed attachment", run: func() {
			s.call(actorDiego, http.MethodPost, "/v1/cases/"+caseID+"/attachments", map[string]any{
				"attachment_id": "contoso-registry", "name": "registry-extract.pdf",
				"media_type": "application/pdf", "size": 184320,
				"sha256":      "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				"storage_ref": "s3://demo-evidence/contoso/registry-extract.pdf",
				"requirement": "supporting_record", "subject": "customer/contoso-qa",
				"lawful_basis": "legal_obligation",
			}, nil)
		}},
		{at: anchor.Add(-3*time.Hour - 40*time.Minute), name: "edit governed case summary", run: func() {
			s.call(
				actorDiego, http.MethodPost,
				"/v1/agent-assists/"+s.id("assist:opening_summary")+"/reviewer-action",
				map[string]any{
					"action": "edited",
					"final": map[string]any{
						"summary":  "The alert remains open while the reviewer corroborates beneficial ownership against the newly registered registry extract.",
						"priority": "high",
					},
					"reason":        "The registry extract arrived after generation and materially narrows the review task.",
					"time_saved_ms": 240000,
				},
				nil,
			)
		}},
		{at: anchor.Add(-3*time.Hour - 35*time.Minute), name: "reject governed queue priority", run: func() {
			s.call(
				actorDiego, http.MethodPost,
				"/v1/agent-assists/"+s.id("assist:queue_priority")+"/reviewer-action",
				map[string]any{
					"action": "rejected",
					"reason": "The queue priority duplicated the governed case priority and added no operational value.",
				},
				nil,
			)
		}},
		{at: anchor.Add(-3 * time.Hour), name: "disposition governed QA case", run: func() {
			s.call(actorDiego, http.MethodPost, "/v1/cases/"+caseID+"/disposition", map[string]any{
				"disposition": "clear", "reason_code": "verified",
				"note": "Registry extract is authentic; the alert is a false positive.",
			}, nil)
		}},
		{at: anchor.Add(-2*time.Hour - 45*time.Minute), name: "sample governed QA case", run: func() {
			s.call(actorAva, http.MethodPost, "/v1/cases/"+caseID+"/qa/select", map[string]any{
				"sample_id": "july-qa", "reviewer": actorMarcus, "rate_bps": 10000,
			}, nil)
		}},
		{at: anchor.Add(-2*time.Hour - 30*time.Minute), name: "disagree governed QA case", run: func() {
			s.call(actorMarcus, http.MethodPost, "/v1/cases/"+caseID+"/qa/review", map[string]any{
				"sample_id": "july-qa", "disposition": "escalate", "reason_code": "risk_confirmed",
				"note": "The ownership chain still needs corroboration.", "override": false,
			}, nil)
		}},
		{at: anchor.Add(-2 * time.Hour), name: "feedback governed QA case", run: func() {
			s.call(actorAva, http.MethodPost, "/v1/cases/"+caseID+"/qa/feedback", map[string]any{
				"sample_id": "july-qa",
				"text":      "Confirm beneficial ownership before clearing future registry-only matches.",
			}, nil)
		}},
	}
}

// hygieneActions periodically triage the undesignated backlog: open referral
// cases older than six hours get assigned and closed with a short note, so the
// queue reads like a staffed operation instead of an ever-growing pile.
func (s *seeder) hygieneActions(start, end time.Time, designated func(decisionID string) bool) []action {
	closers := []string{actorDiego, actorDiego, actorMarcus, actorPriya}
	noteFor := map[string]string{ // #nosec G101 -- reviewer note copy in demo seed content, not credentials
		"credit_review":   "Income docs verified; decision stands.",
		"aml_alert":       "Reviewed against the corridor profile; cleared, no SAR.",
		"kyc_review":      "EDD checklist complete; identity verified.",
		"fraud_review":    "Cardholder confirmed the activity; released.",
		"dispute":         "Evidence reviewed; disposition confirmed.",
		"merchant_review": "Underwriting file complete; boarded at standard terms.",
		"hardship_review": "Plan terms countersigned within authority.",
		"claim_review":    "Documentation consistent; adjusted per policy.",
		"payout_review":   "Ledger reconciles; released.",
		"limit_review":    "Manual review complete; limit decision recorded.",
	}
	var acts []action
	pass := 0
	for t := start.Add(8 * time.Hour); t.Before(end.Add(-6 * time.Hour)); t = t.Add(8 * time.Hour) {
		pass++
		closer := closers[pass%len(closers)]
		acts = append(acts, action{at: t, name: "case hygiene", run: func() {
			var res struct {
				Cases []struct {
					CaseID    string    `json:"case_id"`
					CaseType  string    `json:"case_type"`
					Status    string    `json:"status"`
					SourceID  string    `json:"source_decision_id"`
					CreatedAt time.Time `json:"created_at"`
				} `json:"cases"`
			}
			s.call(actorAva, http.MethodGet, "/v1/cases?status=needs_review", nil, &res)
			closed := 0
			for _, c := range res.Cases {
				if designated(c.SourceID) || t.Sub(c.CreatedAt) < 6*time.Hour {
					continue
				}
				s.call(actorAva, http.MethodPost, "/v1/cases/"+c.CaseID+"/assign",
					map[string]any{"assignee": closer}, nil)
				s.call(closer, http.MethodPost, "/v1/cases/"+c.CaseID+"/status",
					map[string]any{"status": "in_progress"}, nil)
				if closed%2 == 0 {
					if note, ok := noteFor[c.CaseType]; ok {
						s.call(closer, http.MethodPost, "/v1/cases/"+c.CaseID+"/notes",
							map[string]any{"text": note}, nil)
					}
				}
				s.call(closer, http.MethodPost, "/v1/cases/"+c.CaseID+"/status",
					map[string]any{"status": "completed"}, nil)
				closed++
			}
		}})
	}
	return acts
}

// slaSweepActions run the SLA sweeper on a cron-like cadence, recording due-soon
// reminders and breaches exactly as the native scheduler would.
func (s *seeder) slaSweepActions(start, end time.Time) []action {
	var acts []action
	for t := start.Add(12 * time.Hour); t.Before(end); t = t.Add(12 * time.Hour) {
		acts = append(acts, action{at: t, name: "sla sweep", run: func() {
			s.call(actorSvcSched, http.MethodPost, "/v1/cases/sla-sweep", nil, nil)
		}})
	}
	return acts
}
