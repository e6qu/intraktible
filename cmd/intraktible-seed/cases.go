// SPDX-License-Identifier: AGPL-3.0-or-later

// Case work: every manual_review referral opens a REAL case (the engine's
// escalation event, materialized by the Case Manager's projector). Thirty of
// them are worked in detail — assignees, notes, status transitions, SLA
// breaches — mirroring the retired seed's queue; the rest of the backlog is
// triaged by periodic hygiene passes so the queue reads like a staffed team's,
// and a scheduled SLA sweep records due-soon reminders and breaches.
package main

import (
	"encoding/json"
	"net/http"
	"time"
)

type caseNote struct {
	author string
	text   string
	hrs    float64
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
		{tag: "case:ollivanders", name: "Ollivanders Onboarding", slug: "kyc-onboarding", status: "in_progress",
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
