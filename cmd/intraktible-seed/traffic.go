// SPDX-License-Identifier: AGPL-3.0-or-later

// Decision traffic: ~420 decisions over ~30 days generated through the REAL
// decide API. Volume follows a day/night rhythm, environments and versions
// follow each flow's deployments (challenger arms included), a handful of
// decisions fail mid-graph on inputs that genuinely fail, three suspend at
// durable human-review holds, and the pre-approved entities are honored on the
// fast path. Every completed decision's disposition is asserted against the
// intended band — drift between the crafted inputs and the real engine aborts
// the seed loudly.
package main

import (
	"fmt"
	"net/http"
	"time"
)

func fmtName(format string, args ...any) string { return fmt.Sprintf(format, args...) }

// decideSlot is one planned decision.
type decideSlot struct {
	i       int
	slug    string
	j       int
	env     string
	band    string // approve | decline | refer
	fail    bool
	suspend bool
	at      time.Time
	company string

	entityType string // set for pre-approval honored slots
	entityID   string

	decisionID string // filled when the decide runs
	caseID     string // filled when the auto-opened case is located
	designated bool   // reserved by a worked case (hygiene must skip it)
}

// trafficPattern is one 30-slot rotation ≈ the fleet's relative volumes (fraud
// and AML dominate, onboarding/servicing flows trickle). 14 rotations ⇒ 420.
var trafficPattern = []string{
	"card-fraud", "aml-screening", "credit-decision", "card-fraud", "dispute-triage",
	"aml-screening", "kyc-onboarding", "card-fraud", "credit-decision", "payout-risk",
	"aml-screening", "card-fraud", "merchant-onboarding", "credit-decision", "aml-screening",
	"card-fraud", "dispute-triage", "kyc-onboarding", "payout-risk", "collections-hardship",
	"card-fraud", "aml-screening", "credit-decision", "claim-triage", "card-fraud",
	"aml-screening", "limit-increase", "dispute-triage", "kyc-onboarding", "card-fraud",
}

const trafficRotations = 14

type trafficProfile struct {
	envs         []string
	dispositions []string
	failAt       map[int]bool
	suspendAt    map[int]bool
	band         func(j int, env, base string) string
	input        func(j int, band string, company string) map[string]any
	failInput    func(j int, company string) map[string]any
	suspendInput func(company string) map[string]any
	companyName  func(j int) string
}

func failSet(js ...int) map[int]bool {
	m := map[int]bool{}
	for _, j := range js {
		m[j] = true
	}
	return m
}

func pool(names []string, j int) string {
	if j < len(names) {
		return names[j]
	}
	return fmtName("%s (%d)", names[j%len(names)], j/len(names)+1)
}

func trafficProfiles() map[string]*trafficProfile {
	profiles := map[string]*trafficProfile{}

	profiles["credit-decision"] = &trafficProfile{
		envs: []string{"production", "production", "production", "staging", "sandbox"},
		dispositions: []string{"approve", "approve", "refer", "approve", "decline", "approve",
			"refer", "approve", "approve", "decline", "refer", "approve"},
		failAt:    failSet(4, 11, 19, 30, 42),
		suspendAt: failSet(3), // the most recent staging run pauses at the underwriting hold
		band: func(j int, env, base string) string {
			// Outside production the deployed v3 carries the suspending hold for the
			// deep-refer band, so pre-production traffic stays out of the refer band
			// (the one designated hold aside).
			if env != "production" && base == "refer" {
				return "approve"
			}
			return base
		},
		input: func(j int, band, company string) map[string]any {
			switch band {
			case "approve":
				return creditInputRaw(96000+float64(j%6)*9000, 10000+float64(j%5)*3000,
					1800+float64(j%4)*700, 18000, 0, 748+float64(j%5)*10,
					3+float64(j%7), 0.7+float64(j%4)*0.07, company)
			case "decline":
				return creditInputRaw(30000+float64(j%4)*4000, 24000+float64(j%4)*2500,
					12200+float64(j%3)*900, 15000, 2+float64(j%2), 582+float64(j%4)*9,
					1+float64(j%3), 0.35+float64(j%3)*0.08, company)
			default: // refer: risk in the 35–60 advisory band (production, pass-through review)
				return creditInputRaw(60000+float64(j%4)*4000, 26000+float64(j%4)*2600,
					7000+float64(j%3)*800, 15000, 1, 648+float64(j%4)*7,
					2+float64(j%5), 0.5+float64(j%3)*0.1, company)
			}
		},
		failInput: func(j int, company string) map[string]any {
			// The upstream application dropped the debt figure: the enrich node's
			// DTI expression genuinely fails mid-graph.
			in := creditInputRaw(52000+float64(j%3)*4000, 14000, 4200, 12000, 0, 668, 4, 0.8, company)
			delete(in, "debt")
			return in
		},
		suspendInput: func(company string) map[string]any {
			// Deep refer band (risk >= 60, still below the 70 decline line): the v3
			// underwriting hold pauses the decision for income verification.
			return creditInputRaw(56000, 28000, 8700, 15000, 1, 620, 2, 0.5, company)
		},
		companyName: func(j int) string {
			return pool([]string{"Sterling Manufacturing", "Beacon Logistics", "Crestview Dental",
				"Juniper Textiles", "Harbor Freight Lines", "Alpine Coffee Roasters",
				"Meridian Auto Parts", "Bluebird Catering", "Quartz Analytics",
				"Redwood Landscaping", "Falcon Print Works", "Lakeside Provisions"}, j)
		},
	}

	profiles["aml-screening"] = &trafficProfile{
		envs: []string{"production", "production", "staging", "production", "sandbox", "staging"},
		dispositions: []string{"approve", "approve", "refer", "approve", "approve", "refer", "approve",
			"refer", "approve", "approve", "refer", "approve", "approve", "refer"},
		failAt: failSet(23, 61),
		input: func(j int, band, company string) map[string]any {
			if band == "refer" {
				if j%2 == 0 {
					// Large cross-border wire to a high-risk corridor.
					dest := "KY"
					if j%4 == 0 {
						dest = "PA"
					}
					return amlInputRaw(42000+float64(j%5)*9000, "US", dest,
						10+float64(j%3)*8, 1, 0.5, company)
				}
				// Confirmed watchlist hit — hard stop under every deployed version.
				dest := "AE"
				if j%4 == 1 {
					dest = "US"
				}
				return amlInputRaw(15000+float64(j%4)*5000, "US", dest,
					84+float64(j%3)*4, 1, 0.3, company)
			}
			return amlInputRaw(1500+float64(j%9)*700, "US", "US",
				5+float64(j%4)*6, float64(j%3), 0.2+float64(j%4)*0.1, company)
		},
		failInput: func(j int, company string) map[string]any {
			// The payment gateway omitted the origin country: the screening-features
			// node's corridor expression genuinely fails.
			in := amlInputRaw(52000, "US", "KY", 10, 2, 0.4, company)
			delete(in, "origin_country")
			return in
		},
		companyName: func(j int) string {
			return pool([]string{"Acme Imports LLC", "Vandelay Trading", "Initech Payroll Services",
				"Massive Dynamic Procurement", "Duff Distribution", "Hooli Payments",
				"Oceanic Freight SA", "Pied Piper Remittance", "Gringotts Custody",
				"Stark Export Partners", "Wonka Confections Ltd", "Globex Treasury"}, j)
		},
	}
	// The sandbox arm runs the v3 structuring heuristics champion: sub-threshold
	// deposit patterns refer there (production's v2 champion clears them — the
	// staging challenger experiment exists to measure exactly that gap).
	baseAML := profiles["aml-screening"].input
	profiles["aml-screening"].input = func(j int, band, company string) map[string]any {
		if band == "refer" && j%6 == 4 { // sandbox slots (envs[4]) run v3 with no challenger
			return amlInputRaw(8600+float64(j%3)*300, "US", "US", 8, 5+float64(j%3), 0.6, company)
		}
		return baseAML(j, band, company)
	}

	profiles["card-fraud"] = &trafficProfile{
		envs: []string{"production", "production", "production", "production", "staging", "sandbox"},
		dispositions: []string{"approve", "approve", "approve", "refer", "approve", "decline",
			"approve", "approve", "refer", "approve"},
		failAt: failSet(37),
		input: func(j int, band, company string) map[string]any {
			switch band {
			case "decline":
				return fraudInputRaw(900+float64(j%7)*260, 6+float64(j%4), 60+float64(j%5)*8,
					120, 0, 0, company)
			case "refer":
				return fraudInputRaw(150+float64(j%5)*40, 5+float64(j%3), 20+float64(j%4)*9,
					120, 0, float64(j%2), company)
			default:
				return fraudInputRaw(40+float64(j%8)*35, float64(j%4), 12+float64(j%5)*9,
					120, float64(j%2), 0, company)
			}
		},
		failInput: func(j int, company string) map[string]any {
			// The device-intel enrichment dropped its score: the velocity+device
			// node's assignment genuinely fails.
			in := fraudInputRaw(240, 6, 45, 120, 0, 0, company)
			delete(in, "device_score")
			return in
		},
		companyName: func(j int) string { return fmtName("Card %04d", 4021+(j*137)%5800) },
	}

	profiles["kyc-onboarding"] = &trafficProfile{
		envs:         []string{"production", "production", "production", "staging", "sandbox"},
		dispositions: []string{"approve", "approve", "refer", "approve", "refer", "approve", "approve"},
		failAt:       failSet(13, 31),
		suspendAt:    failSet(2), // the identity-confidence hard stop pauses at the EDD hold
		input: func(j int, band, company string) map[string]any {
			if band == "refer" {
				if j%2 == 1 {
					return kycInputRaw(82+float64(j%3)*3, 1, company)
				}
				return kycInputRaw(42+float64(j%4)*4, 0, company)
			}
			return kycInputRaw(72+float64(j%6)*4, 0, company)
		},
		failInput: func(j int, company string) map[string]any {
			// The vendor payload dropped the PEP screen result: the PEP node's
			// comparison genuinely fails mid-graph.
			in := kycInputRaw(70, 0, company)
			delete(in, "pep_match")
			return in
		},
		suspendInput: func(company string) map[string]any {
			return kycInputRaw(35, 0, company) // identity confidence below the 40 hard stop
		},
		companyName: func(j int) string {
			return pool([]string{"Globex Lending", "Cyberdyne Onboarding", "Gringotts Onboarding",
				"Ollivanders Trading", "Nakatomi Trading", "Wayne Financial", "Tyrell Holdings",
				"Sirius Capital", "Umbrella Health SA", "Oscorp Ventures"}, j)
		},
	}

	profiles["dispute-triage"] = &trafficProfile{
		envs: []string{"production", "production", "production", "staging", "sandbox"},
		dispositions: []string{"refer", "approve", "refer", "refer", "refer", "refer",
			"approve", "refer"},
		failAt: failSet(17),
		band: func(j int, env, base string) string {
			// Chargeback season: the RECENT six rotations run refer-heavy (small j
			// = newer), which is the genuine drift the captured baseline shows.
			if j >= 18 {
				calm := []string{"refer", "approve", "approve", "refer", "approve", "approve", "approve", "refer"}
				return calm[j%len(calm)]
			}
			return base
		},
		input: func(j int, band, company string) map[string]any {
			reason := []string{"product_not_received", "duplicate", "quality", "fraud"}[j%4]
			if band == "refer" {
				return disputeInputRaw(640+float64(j%5)*120, reason, company)
			}
			return disputeInputRaw(60+float64(j%6)*55, reason, company)
		},
		failInput: func(j int, company string) map[string]any {
			// Intake dropped the network reason code: the liability logic genuinely fails.
			in := disputeInputRaw(820, "fraud", company)
			delete(in, "reason_code")
			return in
		},
		companyName: func(j int) string { return fmtName("Disputes #%04d", 5512+(j*61)%3800) },
	}

	profiles["merchant-onboarding"] = &trafficProfile{
		envs:         []string{"staging", "sandbox", "staging"},
		dispositions: []string{"approve", "refer", "approve", "refer", "approve", "refer"},
		failAt:       failSet(9),
		input: func(j int, band, company string) map[string]any {
			if band == "refer" {
				if j%2 == 1 {
					return merchantInputRaw(150000+float64(j%3)*20000, 52, 1, company)
				}
				return merchantInputRaw(30000+float64(j%4)*10000, 74+float64(j%3)*6, 0, company)
			}
			return merchantInputRaw(20000+float64(j%6)*9000, 20+float64(j%3)*8, float64(j%2), company)
		},
		failInput: func(j int, company string) map[string]any {
			// The application form omitted the cross-border flag: the volume-features
			// node genuinely fails.
			in := merchantInputRaw(90000, 55, 1, company)
			delete(in, "international")
			return in
		},
		companyName: func(j int) string {
			return pool([]string{"Soylent Merchant Co", "Tyrell Merchant", "Bluth Banana Stands",
				"Los Pollos Hermanos", "Central Perk Roasters", "Paper Street Soap Co",
				"Stay Puft Provisions", "Monsters Inc Retail"}, j)
		},
	}

	profiles["collections-hardship"] = &trafficProfile{
		envs: []string{"production", "production", "production", "staging", "sandbox"},
		dispositions: []string{"approve", "refer", "decline", "approve", "approve", "decline",
			"refer", "approve"},
		input: func(j int, band, company string) map[string]any {
			switch band {
			case "refer":
				return collectionsInputRaw(5200, 2300+float64(j%3)*200, 3+float64(j%2),
					float64(j%2), 4+float64(j%3), 12000+float64(j%3)*2500, company)
			case "decline":
				return collectionsInputRaw(5200, 4700+float64(j%3)*100, 1, 0,
					5+float64(j%3), 4200, company)
			default:
				return collectionsInputRaw(5200, 3300+float64(j%3)*100, 2, 0,
					1+float64(j%2), 6000+float64(j%3)*1500, company)
			}
		},
		companyName: func(j int) string {
			return pool([]string{"Vandelay Industries", "Bluth Household", "Kramer Residence",
				"Costanza Household", "Peralta Household", "Wexler Household",
				"Ehrmantraut Household", "Schrute Farms"}, j)
		},
	}

	profiles["claim-triage"] = &trafficProfile{
		envs:         []string{"production", "production", "production", "staging", "sandbox"},
		dispositions: []string{"approve", "refer", "approve", "decline", "approve", "refer", "approve"},
		failAt:       failSet(6),
		input: func(j int, band, company string) map[string]any {
			switch band {
			case "decline":
				return claimInputRaw(500+float64(j%4)*200, 3000, 0, float64(j%3), 300, company)
			case "refer":
				// Recent referrals (small j) skew to the repeat-claimant abuse cohort —
				// the shift the claim_fraud drift baseline was captured before.
				if j < 8 {
					coverage := 2000 + float64(j%3)*500
					return claimInputRaw(coverage*0.9, coverage, 1, 2+float64(j%2),
						10+float64(j%3)*15, company)
				}
				return claimInputRaw(1900+float64(j%4)*300, 3600, 1, 1, 150+float64(j%3)*50, company)
			default:
				if j%2 == 1 {
					return claimInputRaw(60+float64(j%4)*40, 3000+float64(j%3)*1000, 1, 0,
						200+float64(j%5)*60, company)
				}
				return claimInputRaw(380+float64(j%4)*60, 2400+float64(j%3)*800, 1, 1,
					220+float64(j%4)*40, company)
			}
		},
		failInput: func(j int, company string) map[string]any {
			// The policy-system lookup dropped the active flag: the fast-track rules
			// genuinely fail.
			in := claimInputRaw(700, 3000, 1, 1, 210, company)
			delete(in, "policy_active")
			return in
		},
		companyName: func(j int) string {
			return pool([]string{"Claim CLM-2214 · Okafor", "Claim CLM-2190 · Marchetti",
				"Claim CLM-2145 · Watanabe", "Claim CLM-2261 · Petrov", "Claim CLM-2288 · Silva",
				"Claim CLM-2301 · Haddad", "Claim CLM-2317 · Lindqvist"}, j)
		},
	}

	profiles["payout-risk"] = &trafficProfile{
		envs: []string{"production", "production", "staging", "production", "sandbox"},
		dispositions: []string{"approve", "approve", "refer", "approve", "decline", "approve",
			"approve", "refer"},
		failAt:    failSet(11),
		suspendAt: failSet(1), // a large flagged payout pauses at the ops hold
		input: func(j int, band, company string) map[string]any {
			switch band {
			case "decline":
				return payoutInputRaw(14000+float64(j%3)*2000, 4000, 4+float64(j%3),
					6+float64(j%3)*7, 0.02, company)
			case "refer":
				// Kept under the $15k ops-hold line so advisory reviews pass through.
				return payoutInputRaw(10500+float64(j%4)*1400, 6000, 3,
					180+float64(j%3)*30, 0.008+float64(j%2)*0.004, company)
			default:
				return payoutInputRaw(1500+float64(j%5)*400, 2600, 1+float64(j%2),
					220+float64(j%4)*40, 0.002+float64(j%3)*0.002, company)
			}
		},
		failInput: func(j int, company string) map[string]any {
			// The account service omitted the account age: the payout-features node
			// genuinely fails.
			in := payoutInputRaw(12500, 5200, 2, 210, 0.011, company)
			delete(in, "account_age_days")
			return in
		},
		suspendInput: func(company string) map[string]any {
			// A $16k payout at elevated velocity: matrix says review, and the ops
			// hold gate (>= $15k, score >= 45) pauses it durably.
			return payoutInputRaw(16000, 6000, 4, 200, 0.01, company)
		},
		companyName: func(j int) string {
			return pool([]string{"Hooli Marketplace Payout", "Wayne Home Goods Payout",
				"Umbrella Wellness Payout", "Soylent Retail Payout", "Aviato Sellers",
				"Dunder Mifflin Storefront", "Prestige Worldwide Payout", "Genco Olive Oil"}, j)
		},
	}

	profiles["limit-increase"] = &trafficProfile{
		envs:         []string{"sandbox", "staging", "sandbox"},
		dispositions: []string{"approve", "refer", "approve", "decline", "approve", "refer"},
		input: func(j int, band, company string) map[string]any {
			switch band {
			case "decline":
				return limitInputRaw(40000+float64(j%3)*3000, 20000+float64(j%3)*2000,
					9800+float64(j%2)*600, 12000, 1+float64(j%2), 636+float64(j%3)*4, company)
			case "refer":
				return limitInputRaw(70000+float64(j%3)*4000, 24000+float64(j%3)*2000,
					7900+float64(j%3)*300, 12000, 0, 682+float64(j%3)*6, company)
			default:
				return limitInputRaw(95000+float64(j%5)*8000, 9000+float64(j%4)*2000,
					1500+float64(j%3)*500, 12000, 0, 758+float64(j%4)*8, company)
			}
		},
		companyName: func(j int) string { return fmtName("CLI · Card %04d", 7719+(j*97)%2100) },
	}

	return profiles
}

// --- raw input builders (shared with the assertion suites) --------------------

func creditInputRaw(income, debt, revolving, limit, delinq, fico, tenure, stability float64, company string) map[string]any {
	return map[string]any{
		"income": income, "debt": debt, "revolving_balance": revolving, "credit_limit": limit,
		"delinquencies_24m": delinq, "fico_score": fico, "tenure_years": tenure,
		"employment_stability": stability,
		// The caller pre-computes the bureau ratios the PD model scores on (the
		// graph re-derives the same values for the trace).
		"dti": debt / income, "utilization": revolving / limit, "delinquencies": delinq,
		"company_name": company,
	}
}

func amlInputRaw(amount float64, origin, dest string, watchlist, deposits, outflow float64, company string) map[string]any {
	cross := 0.0
	if origin != dest {
		cross = 1
	}
	high := 0.0
	if amount > 10000 {
		high = 1
	}
	return map[string]any{
		"amount": amount, "origin_country": origin, "dest_country": dest,
		"watchlist_score": watchlist, "deposits_30d": deposits, "outflow_ratio": outflow,
		"cross_border": cross, "high_value": high, "company_name": company,
	}
}

func fraudInputRaw(amount, txCount, deviceScore, avgTicket, cardPresent, newDevice float64, company string) map[string]any {
	return map[string]any{
		"amount": amount, "tx_count_1h": txCount, "device_score": deviceScore, "avg_ticket": avgTicket,
		"card_present": cardPresent, "new_device": newDevice,
		"velocity": txCount, "device_risk": deviceScore, "amount_ratio": amount / avgTicket,
		"company_name": company,
	}
}

func kycInputRaw(docScore, pepMatch float64, company string) map[string]any {
	return map[string]any{"doc_score": docScore, "pep_match": pepMatch, "company_name": company}
}

func disputeInputRaw(amount float64, reason, company string) map[string]any {
	return map[string]any{"amount": amount, "reason_code": reason, "company_name": company}
}

func merchantInputRaw(volume, mccRisk, international float64, company string) map[string]any {
	high := 0.0
	if volume > 100000 {
		high = 1
	}
	return map[string]any{
		"monthly_volume": volume, "mcc_risk": mccRisk, "international": international,
		"amount": volume, "cross_border": international, "high_value": high,
		"company_name": company,
	}
}

func collectionsInputRaw(prior, current, missed, medical, tenure, balance float64, company string) map[string]any {
	return map[string]any{
		"prior_income": prior, "current_income": current, "missed_payments_6m": missed,
		"medical_event": medical, "tenure_years": tenure, "balance_usd": balance,
		"company_name": company,
	}
}

func claimInputRaw(amount, coverage, active, priorClaims, policyDays float64, company string) map[string]any {
	return map[string]any{
		"amount": amount, "coverage_limit": coverage, "policy_active": active,
		"prior_claims_24m": priorClaims, "days_since_policy_start": policyDays,
		"amount_ratio": amount / coverage, "company_name": company,
	}
}

func payoutInputRaw(amount, avgPayout, payouts24h, accountAge, chargebackRate float64, company string) map[string]any {
	newAccount := 0.0
	if accountAge < 30 {
		newAccount = 1
	}
	return map[string]any{
		"amount": amount, "avg_payout_30d": avgPayout, "payouts_24h": payouts24h,
		"account_age_days": accountAge, "chargeback_rate": chargebackRate,
		"payout_ratio": amount / avgPayout, "new_account": newAccount,
		"company_name": company,
	}
}

func limitInputRaw(income, debt, revolving, limit, delinq, fico float64, company string) map[string]any {
	return map[string]any{
		"income": income, "debt": debt, "revolving_balance": revolving, "credit_limit": limit,
		"delinquencies_24m": delinq, "fico_score": fico,
		"dti": debt / income, "utilization": revolving / limit, "delinquencies": delinq,
		"company_name": company,
	}
}

// buildTrafficPlan lays out every decision slot with the ported day/night rhythm
// (newest first, like the retired generator), then returns them oldest-first.
func buildTrafficPlan(anchor time.Time) []*decideSlot {
	profiles := trafficProfiles()
	counters := map[string]int{}
	hoursAgo := 1.2
	total := len(trafficPattern) * trafficRotations
	slots := make([]*decideSlot, 0, total)
	for i := 1; i <= total; i++ {
		slug := trafficPattern[(i-1)%len(trafficPattern)]
		p := profiles[slug]
		j := counters[slug]
		counters[slug] = j + 1

		at := anchor.Add(-time.Duration(hoursAgo * float64(time.Hour)))
		hour := at.Hour()
		if hour < 7 || hour >= 21 {
			hoursAgo += 3.4 + float64(i%5)*0.8
		} else {
			hoursAgo += 0.75 + float64(i%4)*0.35
		}

		env := p.envs[j%len(p.envs)]
		band := p.dispositions[j%len(p.dispositions)]
		if p.band != nil {
			band = p.band(j, env, band)
		}
		slot := &decideSlot{i: i, slug: slug, j: j, env: env, band: band, at: at,
			company: p.companyName(j)}
		switch {
		case p.suspendAt[j]:
			slot.suspend, slot.band = true, "refer"
		case p.failAt[j]:
			slot.fail = true
		}
		slots = append(slots, slot)
	}
	// Oldest first for the executable timeline.
	for l, r := 0, len(slots)-1; l < r; l, r = l+1, r-1 {
		slots[l], slots[r] = slots[r], slots[l]
	}
	return slots
}

// assignPreApprovalRefs marks approve slots that decide against pre-approved
// entities (honored on the fast path) once the grant is live.
func assignPreApprovalRefs(slots []*decideSlot, anchor time.Time) {
	type honored struct {
		slug       string
		entityType string
		entityID   string
		count      int
		after      time.Time // grant must exist
		before     time.Time // grant must still be valid
	}
	wants := []honored{
		{"credit-decision", "applicant", "APP-1001", 3, anchor.Add(-119 * time.Hour), anchor},
		{"credit-decision", "applicant", "APP-1007", 6, anchor.Add(-329 * time.Hour), anchor},
		{"aml-screening", "transaction", "TXN-9920", 12, anchor.Add(-239 * time.Hour), anchor},
		{"merchant-onboarding", "merchant", "MER-4400", 1, anchor.Add(-79 * time.Hour), anchor},
		{"payout-risk", "merchant", "MER-4515", 9, time.Time{}, anchor.Add(-74 * time.Hour)},
	}
	for _, w := range wants {
		assigned := 0
		for _, slot := range slots {
			if assigned == w.count {
				break
			}
			if slot.slug != w.slug || slot.band != "approve" || slot.fail || slot.suspend ||
				slot.entityID != "" {
				continue
			}
			if !w.after.IsZero() && !slot.at.After(w.after) {
				continue
			}
			if !slot.at.Before(w.before) {
				continue
			}
			slot.entityType, slot.entityID = w.entityType, w.entityID
			assigned++
		}
		if assigned != w.count {
			fatalf("pre-approval %s/%s: only %d of %d honored slots available", w.entityType, w.entityID, assigned, w.count)
		}
	}
}

// latencyScale shapes each flow's recorded durations to its cost profile
// (bureau pulls, LLM narratives, ledger reads dominate; pure logic is cheap).
// The AML and payout profiles deliberately sit past their latency monitors.
var latencyScale = map[string]int{
	"credit-decision":      5,
	"aml-screening":        5,
	"kyc-onboarding":       5,
	"card-fraud":           4,
	"dispute-triage":       4,
	"merchant-onboarding":  4,
	"collections-hardship": 5,
	"claim-triage":         4,
	"payout-risk":          8,
	"limit-increase":       3,
}

// decideCaller picks the identity each environment's traffic runs under.
func decideCaller(env string) string {
	switch env {
	case "production":
		return actorSvcProd
	case "staging":
		return actorDiego
	default:
		return actorSvcCI
	}
}

// decisionActions turns the plan into decide calls with per-slot assertions.
func (s *seeder) decisionActions(slots []*decideSlot) []action {
	profiles := trafficProfiles()
	acts := make([]action, 0, len(slots))
	for _, slot := range slots {
		p := profiles[slot.slug]
		acts = append(acts, action{at: slot.at, name: fmtName("decide %s #%d", slot.slug, slot.i), run: func() {
			s.clk.SetScale(latencyScale[slot.slug])
			defer s.clk.SetScale(1)
			var input map[string]any
			switch {
			case slot.fail:
				input = p.failInput(slot.j, slot.company)
			case slot.suspend:
				input = p.suspendInput(slot.company)
			default:
				input = p.input(slot.j, slot.band, slot.company)
			}
			body := map[string]any{"data": input}
			if slot.entityID != "" {
				body["entity_type"] = slot.entityType
				body["entity_id"] = slot.entityID
			}
			var res struct {
				DecisionID    string `json:"decision_id"`
				Status        string `json:"status"`
				Disposition   string `json:"disposition"`
				PreApprovalID string `json:"preapproval_id"`
				Error         string `json:"error"`
			}
			s.call(decideCaller(slot.env), http.MethodPost,
				"/v1/flows/"+slot.slug+"/"+slot.env+"/decide", body, &res)
			slot.decisionID = res.DecisionID
			switch {
			case slot.fail:
				if res.Status != "failed" {
					fatalf("decide %s j=%d: expected a failed decision, got %s/%s", slot.slug, slot.j, res.Status, res.Disposition)
				}
			case slot.suspend:
				if res.Status != "suspended" {
					fatalf("decide %s j=%d: expected suspended, got %s/%s (%s)", slot.slug, slot.j, res.Status, res.Disposition, res.Error)
				}
			default:
				if res.Status != "completed" {
					fatalf("decide %s j=%d (%s): expected completed, got %s: %s", slot.slug, slot.j, slot.env, res.Status, res.Error)
				}
				if res.Disposition != slot.band {
					fatalf("decide %s j=%d (%s): disposition %q, want %q", slot.slug, slot.j, slot.env, res.Disposition, slot.band)
				}
				if slot.entityID != "" && res.PreApprovalID == "" {
					fatalf("decide %s j=%d: expected the %s pre-approval to be honored", slot.slug, slot.j, slot.entityID)
				}
			}
		}})
	}
	return acts
}
