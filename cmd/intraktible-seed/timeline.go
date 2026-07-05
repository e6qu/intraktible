// SPDX-License-Identifier: AGPL-3.0-or-later

// Mid-window governance activity: pre-approvals across every lifecycle state
// (honored, expiring, expired, revoked), per-flow access grants, scheduled
// deploys (pending, time-boxed, canceled), key rotation/revocation, drift
// baselines captured before the seeded shifts, monitor checks, and the inbox
// read-marks — all at believable points of the 30-day window.
package main

import (
	"net/http"
	"time"
)

// preApprovalActions grants the eight pre-approvals at their historical times.
// valid_days are chosen so the fleet ends in the retired seed's mix: active,
// expiring-soon, expired, and revoked.
func (s *seeder) preApprovalActions(cfg *timeCursor, anchor time.Time) []action {
	type pa struct {
		tag        string
		entityType string
		entityID   string
		dispo      string
		terms      map[string]any
		policyTag  string
		policyVer  int
		slug       string
		by         string
		grantHrs   float64 // 0 = configuration window
		validDays  int
		note       string
	}
	grants := []pa{
		{"pa:app-1001", "applicant", "APP-1001", "approve", map[string]any{"limit_usd": 25000, "apr": 12.5},
			"policy:credit", 2, "credit-decision", actorAva, 120, 25, "Pre-approved gold-tier applicant"},
		{"pa:app-1002", "applicant", "APP-1002", "decline", nil, "", 0, "credit-decision", actorMarcus, 0, 40, ""},
		{"pa:app-1007", "applicant", "APP-1007", "approve", map[string]any{"limit_usd": 40000, "apr": 9.9},
			"policy:credit", 2, "credit-decision", actorMarcus, 330, 16, "Platinum relationship — expiring soon, renewal queued"},
		{"pa:mer-4400", "merchant", "MER-4400", "approve", map[string]any{"mdr_bps": 240, "monthly_cap_usd": 500000},
			"", 0, "merchant-onboarding", actorMarcus, 80, 63, "Established low-risk retail merchant"},
		{"pa:app-1011", "applicant", "APP-1011", "approve", map[string]any{"limit_usd": 12000, "apr": 15.0},
			"policy:credit", 2, "credit-decision", actorAva, 160, 8, "Promo offer — expires soon"},
		{"pa:txn-9920", "transaction", "TXN-9920", "approve", nil, "", 0, "aml-screening", actorMarcus, 240, 40,
			"Whitelisted recurring payroll corridor"},
		{"pa:mer-4515", "merchant", "MER-4515", "approve", map[string]any{"auto_release_cap_usd": 25000},
			"", 0, "payout-risk", actorMarcus, 0, 0, "Seasonal payout fast-lane"},
		{"pa:app-1019", "applicant", "APP-1019", "approve", nil, "", 0, "kyc-onboarding", actorAva, 0, 35, ""},
	}
	var acts []action
	for _, g := range grants {
		var at time.Time
		if g.grantHrs > 0 {
			at = anchor.Add(-time.Duration(g.grantHrs * float64(time.Hour)))
		} else {
			at = cfg.step(2 * time.Minute)
		}
		acts = append(acts, action{at: at, name: "preapproval " + g.entityID, run: func() {
			validDays := g.validDays
			if validDays == 0 {
				// The seasonal fast-lane: sized so it expires ~3 days before the
				// anchor, after honoring a season of payouts.
				validDays = int(anchor.Add(-73*time.Hour).Sub(s.clk.Now()).Hours() / 24)
			}
			body := map[string]any{
				"entity_type": g.entityType, "entity_id": g.entityID, "disposition": g.dispo,
				"flow_slug": g.slug, "valid_days": validDays,
			}
			if g.terms != nil {
				body["terms"] = g.terms
			}
			if g.policyTag != "" {
				body["policy_id"] = s.id(g.policyTag)
				body["policy_version"] = g.policyVer
			}
			if g.note != "" {
				body["note"] = g.note
			}
			s.call(g.by, http.MethodPost, "/v1/preapprovals", body, nil)
		}})
	}
	revoke := func(hrs float64, entityType, entityID, reason string) action {
		return action{at: anchor.Add(-time.Duration(hrs * float64(time.Hour))), name: "revoke pa " + entityID, run: func() {
			s.call(actorAva, http.MethodPost, "/v1/preapprovals/"+entityType+"/"+entityID+"/revoke",
				map[string]any{"reason": reason}, nil)
		}}
	}
	acts = append(acts,
		revoke(26, "applicant", "APP-1019", "Document reverification failed"),
		revoke(10, "applicant", "APP-1002", "Adverse media match"),
	)
	return acts
}

// grantActions restrict change control on the sensitive flows mid-window (after
// the fleet's production approvals, so the earlier maker-checker history stays
// valid — a grant list, once present, is enforced on every later write).
func (s *seeder) grantActions(anchor time.Time) []action {
	grants := []struct {
		slug  string
		actor string
		env   string
		hrs   float64
	}{
		{"credit-decision", actorPriya, "*", 180},
		{"credit-decision", actorDiego, "sandbox", 100},
		{"aml-screening", actorDiego, "staging", 60},
		{"card-fraud", actorPriya, "*", 40},
		{"payout-risk", actorDiego, "staging", 50},
	}
	var acts []action
	for _, g := range grants {
		acts = append(acts, action{at: anchor.Add(-time.Duration(g.hrs * float64(time.Hour))), name: "grant " + g.slug + " " + g.actor, run: func() {
			s.call(actorAva, http.MethodPost, "/v1/flows/"+s.flowID(g.slug)+"/grants",
				map[string]any{"actor": g.actor, "environment": g.env}, nil)
		}})
	}
	return acts
}

// scheduleActions stage the scheduled deploys: a pending staging rollout, a
// time-boxed sandbox trial, and a canceled production schedule from earlier in
// the window.
func (s *seeder) scheduleActions(anchor time.Time) []action {
	return []action{
		{at: anchor.Add(-520 * time.Hour), name: "schedule collections", run: func() {
			var res struct {
				ScheduleID string `json:"schedule_id"`
			}
			s.call(actorMarcus, http.MethodPost, "/v1/flows/"+s.flowID("collections-hardship")+"/deployments/schedule",
				map[string]any{"environment": "staging", "version": 2,
					"at": anchor.Add(-500 * time.Hour).Format(time.RFC3339)}, &res)
			s.setID("schedule:collections", res.ScheduleID)
		}},
		{at: anchor.Add(-505 * time.Hour), name: "cancel collections schedule", run: func() {
			s.call(actorMarcus, http.MethodDelete,
				"/v1/flows/"+s.flowID("collections-hardship")+"/deployments/schedules/"+s.id("schedule:collections"), nil, nil)
		}},
		{at: anchor.Add(-10 * time.Hour), name: "schedule credit staging", run: func() {
			// The credit flow is grant-restricted by now; the admin schedules it.
			s.call(actorAva, http.MethodPost, "/v1/flows/"+s.flowID("credit-decision")+"/deployments/schedule",
				map[string]any{"environment": "staging", "version": 3,
					"at": anchor.Add(48 * time.Hour).Format(time.RFC3339)}, nil)
		}},
		{at: anchor.Add(-6 * time.Hour), name: "schedule fraud sandbox", run: func() {
			s.call(actorAva, http.MethodPost, "/v1/flows/"+s.flowID("card-fraud")+"/deployments/schedule",
				map[string]any{"environment": "sandbox", "version": 4,
					"at":    anchor.Add(24 * time.Hour).Format(time.RFC3339),
					"until": anchor.Add(120 * time.Hour).Format(time.RFC3339)}, nil)
		}},
	}
}

// keyLifecycleActions rotate the analytics key and revoke the decommissioned
// partner key mid-window.
func (s *seeder) keyLifecycleActions(anchor time.Time) []action {
	return []action{
		{at: anchor.Add(-300 * time.Hour), name: "revoke partner key", run: func() {
			s.call(actorAva, http.MethodDelete, "/v1/api-keys/"+s.id("key:svc-partner"), nil, nil)
		}},
		{at: anchor.Add(-120 * time.Hour), name: "rotate analytics key", run: func() {
			var res struct {
				Secret string `json:"secret"`
			}
			s.call(actorAva, http.MethodPost, "/v1/api-keys/"+s.id("key:svc-bi")+"/rotate",
				map[string]any{}, &res)
			s.keys[actorSvcBI] = res.Secret
		}},
	}
}

// baselineActions capture drift baselines: the dispute flow's disposition mix
// and the claim-abuse model's score distribution are captured just before their
// seeded regime shifts, so both genuinely drift; the rest are captured late and
// sit comfortably inside their thresholds.
func (s *seeder) baselineActions(slots []*decideSlot, anchor time.Time) []action {
	// The moment just before the newest old-regime decision of a flow.
	boundary := func(slug string, firstNewJ int) time.Time {
		var oldest, newest time.Time
		for _, slot := range slots {
			if slot.slug != slug {
				continue
			}
			if slot.j == firstNewJ-1 && (newest.IsZero() || slot.at.Before(newest)) {
				newest = slot.at
			}
			if slot.j == firstNewJ {
				oldest = slot.at
			}
		}
		if oldest.IsZero() || newest.IsZero() {
			fatalf("baseline boundary for %s at j=%d not found", slug, firstNewJ)
		}
		return oldest.Add(newest.Sub(oldest) / 2)
	}
	var acts []action
	acts = append(acts, action{at: boundary("dispute-triage", 18), name: "baseline dispute flow", run: func() {
		s.call(actorMarcus, http.MethodPost, "/v1/flows/"+s.flowID("dispute-triage")+"/baseline", nil, nil)
	}}, action{at: boundary("claim-triage", 8), name: "baseline claim_fraud model", run: func() {
		s.call(actorMarcus, http.MethodPost, "/v1/models/claim_fraud/baseline", nil, nil)
	}})
	for i, slug := range []string{"credit-decision", "aml-screening", "card-fraud", "payout-risk"} {
		acts = append(acts, action{at: anchor.Add(-time.Duration(50-i) * time.Hour), name: "baseline " + slug, run: func() {
			s.call(actorMarcus, http.MethodPost, "/v1/flows/"+s.flowID(slug)+"/baseline", nil, nil)
		}})
	}
	for i, model := range []string{"credit_pd", "fraud_score", "aml_risk", "kyc_score", "repayment_propensity", "payout_risk"} {
		acts = append(acts, action{at: anchor.Add(-time.Duration(55-i) * time.Hour), name: "baseline model " + model, run: func() {
			s.call(actorMarcus, http.MethodPost, "/v1/models/"+model+"/baseline", nil, nil)
		}})
	}
	return acts
}

// monitorCheckActions run the on-demand monitor evaluation over every monitored
// flow near the end of the window: firing rules push to the configured webhooks
// (recorded deliveries — the demo endpoints don't resolve, exactly like a
// misconfigured channel in a real workspace).
func (s *seeder) monitorCheckActions(anchor time.Time) []action {
	checks := []struct {
		slug string
		hrs  float64
	}{
		{"credit-decision", 6}, {"aml-screening", 5.5}, {"dispute-triage", 5},
		{"merchant-onboarding", 4.5}, {"payout-risk", 4}, {"card-fraud", 3},
		{"kyc-onboarding", 2.8}, {"collections-hardship", 2.6}, {"claim-triage", 2.4},
	}
	var acts []action
	for _, c := range checks {
		acts = append(acts, action{at: anchor.Add(-time.Duration(c.hrs * float64(time.Hour))), name: "monitor check " + c.slug, run: func() {
			// The monitors surface is author-gated (editor+); the admin runs the
			// on-demand checks.
			s.call(actorAva, http.MethodPost, "/v1/flows/"+s.flowID(c.slug)+"/monitors/check", nil, nil)
		}})
	}
	return acts
}

// inboxActions mark a few of Ava's oldest notifications read, so the inbox
// carries the usual read/unread mix.
func (s *seeder) inboxActions(anchor time.Time) []action {
	return []action{{at: anchor.Add(-1 * time.Hour), name: "mark notifications read", run: func() {
		var res struct {
			Notifications []struct {
				NotificationID string `json:"notification_id"`
				Recipient      string `json:"recipient"`
			} `json:"notifications"`
		}
		s.call(actorAva, http.MethodGet, "/v1/notifications", nil, &res)
		marked := 0
		for i := len(res.Notifications) - 1; i >= 0 && marked < 3; i-- {
			n := res.Notifications[i]
			if n.Recipient != actorAva {
				continue // the shared reviewer queue can't be marked from one inbox
			}
			s.call(actorAva, http.MethodPost, "/v1/notifications/"+n.NotificationID+"/read", nil, nil)
			marked++
		}
	}}}
}
