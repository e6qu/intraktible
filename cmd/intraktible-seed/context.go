// SPDX-License-Identifier: AGPL-3.0-or-later

// Context-layer content: the model registry (real, evaluatable specs — the
// engine's Predict nodes score decisions with these exact coefficients),
// connectors (the ones the graphs fetch are static stubs so decides work
// anywhere, including the wasm sandbox; the rest are display-rich HTTP
// definitions), feature definitions, and the entity store with event timelines
// that fall inside the features' windows.
package main

import (
	"net/http"
	"time"
)

type modelSpec struct {
	name  string
	owner string
	spec  map[string]any
}

func modelSpecs() []modelSpec {
	tree := func(feature string, threshold, left, right float64) map[string]any {
		return map[string]any{
			"feature": feature, "threshold": threshold,
			"left":  map[string]any{"leaf": true, "value": left},
			"right": map[string]any{"leaf": true, "value": right},
		}
	}
	return []modelSpec{
		{"credit_pd", actorAva, map[string]any{
			// FICO carries the intercept: higher score -> lower PD, with DTI /
			// utilization / delinquency drivers. Tuned so a mid-tier applicant lands
			// near 0.5 probability and routes to manual review.
			"kind": "logistic", "intercept": 5.3,
			"coefficients": map[string]any{"dti": 3.0, "utilization": 1.2, "delinquencies": 0.7, "fico_score": -0.012},
		}},
		{"fraud_score", actorMarcus, map[string]any{
			"kind": "gbm", "base": 0.1, "link": "logit",
			"trees": []map[string]any{
				{
					"feature": "velocity", "threshold": 5,
					"left": map[string]any{"leaf": true, "value": -0.6},
					"right": map[string]any{
						"feature": "device_risk", "threshold": 60,
						"left":  map[string]any{"leaf": true, "value": 0.8},
						"right": map[string]any{"leaf": true, "value": 1.9},
					},
				},
				tree("amount_ratio", 3, -0.2, 1.1),
				tree("amount", 900, -0.15, 0.5),
			},
		}},
		{"aml_risk", actorMarcus, map[string]any{
			"kind": "expression", "expr": "amount / 10000 + cross_border * 2 + high_value",
		}},
		{"kyc_score", actorPriya, map[string]any{
			// The vendor's document-confidence score, mirrored as an evaluatable
			// expression (the retired demo faked an external endpoint here; a real
			// endpoint cannot serve the sandboxed deployment).
			"kind": "expression", "expr": "doc_score",
		}},
		{"repayment_propensity", actorAva, map[string]any{
			"kind": "logistic", "intercept": -1.1,
			"coefficients": map[string]any{"tenure_years": 0.4, "employment_stability": 0.9},
		}},
		{"claim_fraud", actorPriya, map[string]any{
			"kind": "gbm", "base": -0.4, "link": "logit",
			"trees": []map[string]any{
				tree("prior_claims_24m", 2, -0.5, 1.2),
				tree("days_since_policy_start", 60, 1.0, -0.3),
				tree("amount_ratio", 0.8, -0.2, 0.7),
			},
		}},
		{"payout_risk", actorMarcus, map[string]any{
			"kind": "expression",
			"expr": "payouts_24h * 6 + payout_ratio * 8 + new_account * 25 + chargeback_rate * 200",
		}},
	}
}

type connectorSpec struct {
	name   string
	typ    string
	config map[string]any
}

func connectorSpecs() []connectorSpec {
	return []connectorSpec{
		// The three connectors the deployed graphs fetch at decide time are static
		// stubs (fixed JSON, no egress) so every decide — seeded or interactive,
		// native or wasm — resolves them for real.
		{"experian", "static", map[string]any{"data": map[string]any{
			"bureau": "experian", "file_status": "match", "tradelines": 9, "inquiries_6m": 1,
		}}},
		{"jumio-kyc", "static", map[string]any{"data": map[string]any{
			"vendor": "jumio", "liveness": "pass", "document_status": "readable",
		}}},
		{"core-banking", "static", map[string]any{"data": map[string]any{
			"ledger": "core-banking", "nsf_12m": 1, "avg_balance_90d": 18400,
		}}},
		{"ofac-sanctions", "http", map[string]any{
			"url": "https://api.sanctions.demo/screen", "method": "POST",
			"headers": map[string]string{"X-Api-Key": "••••••••••••"},
		}},
		{"device-intel", "http", map[string]any{
			"url": "https://api.deviceintel.demo/v1/fingerprint", "method": "POST",
			"headers": map[string]string{"X-Api-Key": "••••••••••••"},
		}},
		{"plaid-transactions", "http", map[string]any{
			"url": "https://api.plaid.demo/transactions/get", "method": "POST",
			"headers": map[string]string{"Plaid-Client-Id": "plaid-client-6614", "Plaid-Secret": "••••••••••••"}, // #nosec G101 -- a redacted placeholder in demo seed content, not a credential
		}},
		{"lexisnexis-kyb", "http", map[string]any{
			"url": "https://api.lexisnexis.demo/kyb/company", "method": "POST",
			"headers": map[string]string{"X-Api-Key": "••••••••••••"},
		}},
	}
}

type featureSpec struct {
	name        string
	entityType  string
	eventName   string
	aggregation string
	field       string
	windowHours int
}

func featureSpecs() []featureSpec {
	return []featureSpec{
		{"tx_count_30d", "applicant", "transaction", "count", "", 720},
		{"tx_sum_7d", "applicant", "transaction", "sum", "amount", 168},
		{"tx_count_1h", "customer", "authorization", "count", "", 1},
		{"auth_sum_24h", "customer", "authorization", "sum", "amount", 24},
		{"wire_count_7d", "transaction", "wire", "count", "", 168},
		{"wire_sum_30d", "transaction", "wire", "sum", "amount", 720},
		{"chargeback_count_90d", "merchant", "chargeback", "count", "", 2160},
		{"settlement_sum_30d", "merchant", "settlement", "sum", "amount", 720},
		{"payout_sum_7d", "merchant", "payout", "sum", "amount", 168},
		{"login_count_24h", "customer", "login", "count", "", 24},
		{"dispute_count_180d", "customer", "dispute", "count", "", 4320},
		{"claim_count_180d", "customer", "claim", "count", "", 4320},
	}
}

type entityEvent struct {
	name string
	hrs  float64
	data map[string]any
}

type entitySpec struct {
	typ        string
	id         string
	attributes map[string]any
	events     []entityEvent
}

func txEvent(hrs, amount float64, merchant, mcc, channel, geo, currency string) entityEvent {
	return entityEvent{"transaction", hrs, map[string]any{
		"amount": amount, "merchant": merchant, "mcc": mcc, "channel": channel, "geo": geo, "currency": currency,
	}}
}

func authEvent(hrs, amount float64, merchant, mcc, channel, geo, currency string) entityEvent {
	return entityEvent{"authorization", hrs, map[string]any{
		"amount": amount, "merchant": merchant, "mcc": mcc, "channel": channel, "geo": geo, "currency": currency,
	}}
}

func loginEvent(hrs float64, device, ipCountry string) entityEvent {
	return entityEvent{"login", hrs, map[string]any{"device": device, "ip_country": ipCountry}}
}

func wireEvent(hrs, amount float64, dest, currency, reference string) entityEvent {
	return entityEvent{"wire", hrs, map[string]any{"amount": amount, "dest": dest, "currency": currency, "reference": reference}}
}

func settlementEvent(hrs, amount float64, transactions int) entityEvent {
	return entityEvent{"settlement", hrs, map[string]any{"amount": amount, "currency": "USD", "transactions": transactions}}
}

func payoutEvent(hrs, amount float64, rail string) entityEvent {
	return entityEvent{"payout", hrs, map[string]any{"amount": amount, "currency": "USD", "rail": rail}}
}

func chargebackEvent(hrs, amount float64, reason, network string) entityEvent {
	return entityEvent{"chargeback", hrs, map[string]any{"amount": amount, "reason": reason, "network": network}}
}

func claimEvent(hrs float64, data map[string]any) entityEvent { return entityEvent{"claim", hrs, data} }

func entitySpecs() []entitySpec {
	return []entitySpec{
		{
			// HERO — pa_1's pre-approved gold-tier applicant.
			typ: "applicant", id: "APP-1001",
			attributes: map[string]any{
				"name": "Jane Doe", "email": "jane.doe@example.com", "dob": "1988-04-12",
				"address": "1184 Maple Ave, Columbus, OH", "country": "US", "segment": "gold",
				"kyc_level": "verified", "risk_rating": "low", "since": "2019-03-08",
			},
			events: []entityEvent{
				txEvent(640, 2350, "Delta Air Lines", "3058", "ecom", "US", "USD"),
				txEvent(480, 890, "Whole Foods", "5411", "card_present", "US", "USD"),
				loginEvent(470, "iPhone 15 · iOS 18", "US"),
				txEvent(300, 1200, "Best Buy", "5732", "ecom", "US", "USD"),
				txEvent(210, 340, "Shell Oil", "5541", "card_present", "US", "USD"),
				txEvent(100, 4500, "Apple Store", "5732", "ecom", "US", "USD"),
				txEvent(60, 95, "Trader Joes", "5411", "card_present", "US", "USD"),
				txEvent(20, 610, "Marriott Hotels", "3509", "ecom", "US", "USD"),
				loginEvent(12, "MacBook · Safari 18", "US"),
				loginEvent(2, "iPhone 15 · iOS 18", "US"),
			},
		},
		{
			typ: "applicant", id: "APP-1002",
			attributes: map[string]any{
				"name": "John Roe", "email": "john.roe@example.co.uk", "dob": "1975-09-03",
				"country": "GB", "segment": "standard", "kyc_level": "verified", "risk_rating": "high",
			},
			events: []entityEvent{
				txEvent(200, 50000, "Sothebys", "5971", "ecom", "GB", "GBP"),
				txEvent(130, 1450, "Harrods", "5311", "card_present", "GB", "GBP"),
				loginEvent(48, "Windows · Chrome 126", "GB"),
				loginEvent(30, "Windows · Chrome 126", "CY"),
			},
		},
		{
			// HERO — pa_3's platinum relationship with the expiring renewal.
			typ: "applicant", id: "APP-1007",
			attributes: map[string]any{
				"name": "Mei Lin", "email": "mei.lin@example.sg", "dob": "1979-11-30",
				"address": "12 Marina Blvd, Singapore", "country": "SG", "segment": "platinum",
				"kyc_level": "enhanced", "risk_rating": "low", "since": "2015-07-21",
			},
			events: []entityEvent{
				txEvent(600, 22000, "Singapore Airlines", "3012", "ecom", "SG", "SGD"),
				txEvent(420, 3800, "Takashimaya", "5311", "card_present", "SG", "SGD"),
				loginEvent(336, "iPad · iPadOS 18", "SG"),
				txEvent(330, 9800, "Cartier", "5944", "card_present", "SG", "SGD"),
				txEvent(250, 520, "Cold Storage", "5411", "card_present", "SG", "SGD"),
				txEvent(150, 15000, "Raffles Fine Art", "5971", "ecom", "SG", "SGD"),
				txEvent(90, 6200, "Mandarin Oriental", "3603", "ecom", "HK", "HKD"),
				loginEvent(48, "iPhone 16 Pro · iOS 18", "SG"),
				txEvent(30, 1450, "Grab", "4121", "ecom", "SG", "SGD"),
				loginEvent(6, "iPhone 16 Pro · iOS 18", "SG"),
			},
		},
		{
			typ: "applicant", id: "APP-1011",
			attributes: map[string]any{
				"name": "Carlos Reyes", "email": "carlos.reyes@example.mx", "dob": "1993-02-17",
				"country": "MX", "segment": "standard", "kyc_level": "verified", "risk_rating": "medium",
			},
			events: []entityEvent{
				txEvent(340, 880, "Liverpool", "5311", "card_present", "MX", "MXN"),
				txEvent(160, 2300, "Aeromexico", "3011", "ecom", "MX", "MXN"),
				loginEvent(88, "Android 15 · Samsung S24", "MX"),
				txEvent(26, 410, "Oxxo", "5499", "card_present", "MX", "MXN"),
			},
		},
		{
			typ: "applicant", id: "APP-1019",
			attributes: map[string]any{
				"name": "Fatima Al-Sayed", "email": "fatima.alsayed@example.ae", "dob": "1986-07-09",
				"country": "AE", "segment": "standard", "kyc_level": "reverification_failed", "risk_rating": "high",
			},
			events: []entityEvent{
				txEvent(260, 7200, "Dubai Duty Free", "5309", "card_present", "AE", "AED"),
				txEvent(190, 3100, "Emirates", "3040", "ecom", "AE", "AED"),
				txEvent(40, 1900, "Gold Souk Traders", "5944", "card_present", "AE", "AED"),
				loginEvent(40, "iPhone 14 · iOS 17", "AE"),
				loginEvent(18, "Windows · Edge 126", "TR"),
			},
		},
		{
			typ: "applicant", id: "APP-1023",
			attributes: map[string]any{
				"name": "Tomasz Nowak", "email": "tomasz.nowak@example.pl", "dob": "1990-12-01",
				"country": "PL", "segment": "gold", "kyc_level": "verified", "risk_rating": "low",
			},
			events: []entityEvent{
				txEvent(380, 3400, "LOT Polish Airlines", "3050", "ecom", "PL", "PLN"),
				txEvent(120, 5100, "Media Markt", "5732", "card_present", "PL", "PLN"),
				txEvent(60, 780, "Allegro", "5999", "ecom", "PL", "PLN"),
				loginEvent(9, "Android 15 · Pixel 8", "PL"),
			},
		},
		{
			typ: "customer", id: "CUST-7781",
			attributes: map[string]any{
				"name": "Ada Stark", "email": "ada.stark@example.com", "tenure_years": 6,
				"card_present": true, "country": "US", "risk_rating": "low",
			},
			events: []entityEvent{
				authEvent(500, 120, "REI", "5941", "card_present", "US", "USD"),
				authEvent(200, 95, "Safeway", "5411", "card_present", "US", "USD"),
				authEvent(8, 1290, "Alaska Airlines", "3057", "ecom", "US", "USD"),
				loginEvent(8, "iPhone 15 · iOS 18", "US"),
			},
		},
		{
			typ: "customer", id: "CUST-7790",
			attributes: map[string]any{
				"name": "Bruce Pied", "email": "bruce.pied@example.com", "tenure_years": 1,
				"card_present": false, "country": "US", "risk_rating": "high",
			},
			events: []entityEvent{
				authEvent(118, 60, "Steam", "5816", "ecom", "US", "USD"),
				authEvent(112, 2400, "Newegg", "5732", "ecom", "RO", "USD"),
				{"dispute", 110, map[string]any{"amount": 2400, "reason": "fraud", "network": "visa"}},
				loginEvent(111, "Windows · Firefox 127", "RO"),
				loginEvent(96, "iPhone 13 · iOS 17", "US"),
			},
		},
		{
			// HERO — the Okafor claim case (CLM-2214, stolen laptop) reads off this record.
			typ: "customer", id: "CUST-7804",
			attributes: map[string]any{
				"name": "Nina Okafor", "email": "nina.okafor@example.com", "dob": "1991-06-25",
				"address": "88 Lakeshore Dr, Chicago, IL", "country": "US", "tenure_years": 3,
				"card_present": true, "kyc_level": "verified", "risk_rating": "low",
			},
			events: []entityEvent{
				authEvent(600, 1900, "Best Buy", "5732", "card_present", "US", "USD"),
				authEvent(400, 64, "Peets Coffee", "5814", "card_present", "US", "USD"),
				loginEvent(380, "Pixel 9 · Android 15", "US"),
				authEvent(180, 132, "Target", "5310", "card_present", "US", "USD"),
				authEvent(90, 240, "Walgreens", "5912", "card_present", "US", "USD"),
				claimEvent(64, map[string]any{"amount": 1900, "reason": "theft", "item": "laptop", "police_report": true}),
				authEvent(16, 88, "Uber Eats", "5814", "ecom", "US", "USD"),
				loginEvent(16, "Pixel 9 · Android 15", "US"),
				authEvent(3, 41, "Shell Oil", "5541", "card_present", "US", "USD"),
			},
		},
		{
			typ: "customer", id: "CUST-7811",
			attributes: map[string]any{
				"name": "Ken Watanabe", "email": "ken.watanabe@example.jp", "tenure_years": 8,
				"card_present": true, "country": "JP", "risk_rating": "low",
			},
			events: []entityEvent{
				authEvent(300, 410, "Bic Camera", "5732", "card_present", "JP", "JPY"),
				claimEvent(250, map[string]any{"amount": 2900, "reason": "accidental_damage", "item": "camera lens"}),
				authEvent(30, 65, "Lawson", "5411", "card_present", "JP", "JPY"),
				loginEvent(5, "iPhone 15 · iOS 18", "JP"),
			},
		},
		{
			typ: "customer", id: "CUST-7825",
			attributes: map[string]any{
				"name": "Sofia Marchetti", "email": "sofia.marchetti@example.it", "tenure_years": 2,
				"card_present": false, "country": "IT", "risk_rating": "medium",
			},
			events: []entityEvent{
				claimEvent(700, map[string]any{"amount": 240, "reason": "accidental_damage", "item": "phone screen"}),
				authEvent(140, 520, "Zalando", "5651", "ecom", "IT", "EUR"),
				{"dispute", 96, map[string]any{"amount": 520, "reason": "quality", "network": "mastercard"}},
				claimEvent(60, map[string]any{"amount": 310, "reason": "accidental_damage", "item": "phone — repeat claimant"}),
				loginEvent(58, "iPhone 12 · iOS 17", "IT"),
			},
		},
		{
			// HERO — pa_4's pre-approved low-risk retail merchant.
			typ: "merchant", id: "MER-4400",
			attributes: map[string]any{
				"name": "Soylent Retail", "mcc": "5411", "risk": "low", "incorporation": "2012-05-17",
				"address": "400 Industrial Way, Fresno, CA", "country": "US", "kyc_level": "verified", "since": "2021-02-03",
			},
			events: []entityEvent{
				settlementEvent(640, 208000, 7910),
				settlementEvent(470, 231000, 8480),
				settlementEvent(300, 220000, 8122),
				payoutEvent(280, 175000, "ACH"),
				chargebackEvent(260, 415, "product_not_received", "visa"),
				settlementEvent(120, 245000, 9034),
				payoutEvent(100, 180000, "ACH"),
				chargebackEvent(48, 740, "fraud", "mastercard"),
				settlementEvent(24, 238000, 8761),
				payoutEvent(6, 190000, "RTP"),
			},
		},
		{
			typ: "merchant", id: "MER-4471",
			attributes: map[string]any{
				"name": "Tyrell Digital", "mcc": "6051", "risk": "high", "incorporation": "2023-01-30",
				"country": "US", "kyc_level": "enhanced_underwriting",
			},
			events: []entityEvent{
				settlementEvent(150, 90000, 410),
				chargebackEvent(40, 1200, "fraud", "visa"),
				settlementEvent(26, 112000, 522),
				chargebackEvent(10, 2600, "fraud", "visa"),
			},
		},
		{
			typ: "merchant", id: "MER-4488",
			attributes: map[string]any{
				"name": "Wayne Home Goods", "mcc": "5712", "risk": "low", "incorporation": "2009-08-11",
				"country": "US", "kyc_level": "verified",
			},
			events: []entityEvent{
				settlementEvent(400, 64000, 310),
				settlementEvent(96, 71000, 342),
				payoutEvent(72, 41000, "ACH"),
				payoutEvent(24, 12500, "ACH"),
			},
		},
		{
			typ: "merchant", id: "MER-4502",
			attributes: map[string]any{
				"name": "Umbrella Wellness", "mcc": "8099", "risk": "medium", "incorporation": "2019-04-02",
				"country": "US", "kyc_level": "verified",
			},
			events: []entityEvent{
				settlementEvent(210, 38000, 205),
				chargebackEvent(130, 310, "quality", "visa"),
				settlementEvent(50, 46000, 238),
				chargebackEvent(20, 95, "duplicate", "mastercard"),
				payoutEvent(14, 29500, "ACH"),
			},
		},
		{
			// HERO — pa_7's expired payout fast-lane; the Hooli payout case's volume
			// spike (their holiday sale) shows in the recent settlements and payouts.
			typ: "merchant", id: "MER-4515",
			attributes: map[string]any{
				"name": "Hooli Marketplace", "mcc": "5999", "risk": "medium", "incorporation": "2016-09-02",
				"address": "1 Hooli Way, Palo Alto, CA", "country": "US", "kyc_level": "verified", "since": "2022-11-14",
			},
			events: []entityEvent{
				settlementEvent(340, 152000, 3120),
				settlementEvent(260, 139000, 2870),
				chargebackEvent(220, 2100, "quality", "visa"),
				payoutEvent(180, 87000, "ACH"),
				settlementEvent(120, 186000, 3910),
				payoutEvent(60, 98000, "ACH"),
				settlementEvent(40, 243000, 5230),
				chargebackEvent(30, 380, "product_not_received", "mastercard"),
				payoutEvent(12, 14000, "RTP"),
				payoutEvent(4, 121000, "ACH"),
			},
		},
		{
			// HERO — pa_6's whitelisted recurring payroll corridor: a steady weekly batch.
			typ: "transaction", id: "TXN-9920",
			attributes: map[string]any{
				"corridor": "US→US", "type": "payroll", "recurring": true,
				"originator": "Initech Payroll Services", "beneficiary_bank": "First National Bank",
				"currency": "USD", "purpose": "weekly payroll batch",
			},
			events: []entityEvent{
				wireEvent(696, 31800, "US", "USD", "PAYROLL-2418"),
				wireEvent(600, 32450, "US", "USD", "PAYROLL-2419"),
				wireEvent(432, 31950, "US", "USD", "PAYROLL-2420"),
				wireEvent(336, 32000, "US", "USD", "PAYROLL-2421"),
				wireEvent(240, 32000, "US", "USD", "PAYROLL-2422"),
				wireEvent(168, 32000, "US", "USD", "PAYROLL-2423"),
				wireEvent(96, 32600, "US", "USD", "PAYROLL-2424"),
				wireEvent(48, 31700, "US", "USD", "PAYROLL-2425"),
				wireEvent(8, 32150, "US", "USD", "PAYROLL-2426"),
			},
		},
		{
			typ: "transaction", id: "TXN-9931",
			attributes: map[string]any{
				"corridor": "US→KY", "type": "wire", "recurring": false,
				"originator": "Acme Imports LLC", "currency": "USD",
			},
			events: []entityEvent{
				wireEvent(300, 39000, "KY", "USD", "INV-0781"),
				wireEvent(120, 48000, "KY", "USD", "INV-0802"),
				wireEvent(74, 44500, "KY", "USD", "INV-0809"),
				wireEvent(30, 51000, "KY", "USD", "INV-0815"),
			},
		},
		{
			typ: "transaction", id: "TXN-9944",
			attributes: map[string]any{
				"corridor": "US→US", "type": "vendor_batch", "recurring": true,
				"originator": "Massive Dynamic Procurement", "currency": "USD",
			},
			events: []entityEvent{
				wireEvent(200, 8600, "US", "USD", "VB-1141"),
				wireEvent(130, 9200, "US", "USD", "VB-1150"),
				wireEvent(55, 8900, "US", "USD", "VB-1158"),
				wireEvent(12, 9400, "US", "USD", "VB-1163"),
			},
		},
		{
			typ: "transaction", id: "TXN-9958",
			attributes: map[string]any{
				"corridor": "DE→US", "type": "invoice", "recurring": false,
				"originator": "Rhein Maschinenbau GmbH", "currency": "EUR",
			},
			events: []entityEvent{
				wireEvent(380, 24800, "US", "EUR", "RE-2026-031"),
				wireEvent(20, 27500, "US", "EUR", "RE-2026-047"),
			},
		},
	}
}

// contextConfigActions registers connectors, features, models, and the entity
// records in the configuration window.
func (s *seeder) contextConfigActions(cfg *timeCursor) []action {
	var acts []action
	for _, c := range connectorSpecs() {
		acts = append(acts, action{at: cfg.step(2 * time.Minute), name: "connector " + c.name, run: func() {
			s.call(actorPriya, http.MethodPost, "/v1/context/connectors",
				map[string]any{"name": c.name, "type": c.typ, "config": c.config}, nil)
		}})
	}
	for _, f := range featureSpecs() {
		acts = append(acts, action{at: cfg.step(time.Minute), name: "feature " + f.name, run: func() {
			body := map[string]any{
				"name": f.name, "entity_type": f.entityType, "event_name": f.eventName,
				"aggregation": f.aggregation, "window_hours": f.windowHours,
			}
			if f.field != "" {
				body["field"] = f.field
			}
			s.call(actorPriya, http.MethodPost, "/v1/context/features", body, nil)
		}})
	}
	for _, m := range modelSpecs() {
		acts = append(acts, action{at: cfg.step(2 * time.Minute), name: "model " + m.name, run: func() {
			s.call(m.owner, http.MethodPost, "/v1/models", map[string]any{"name": m.name, "spec": m.spec}, nil)
		}})
	}
	for _, e := range entitySpecs() {
		acts = append(acts, action{at: cfg.step(time.Minute), name: "entity " + e.id, run: func() {
			s.call(actorDiego, http.MethodPost, "/v1/context/entities",
				map[string]any{"entity_type": e.typ, "entity_id": e.id, "attributes": e.attributes}, nil)
		}})
	}
	return acts
}

// entityEventActions ingests every entity's event timeline at its occurrence time.
func (s *seeder) entityEventActions(anchor time.Time) []action {
	var acts []action
	for _, e := range entitySpecs() {
		for _, ev := range e.events {
			at := anchor.Add(-time.Duration(ev.hrs * float64(time.Hour)))
			acts = append(acts, action{at: at, name: "event " + e.id + "/" + ev.name, run: func() {
				s.call(actorDiego, http.MethodPost, "/v1/context/events", map[string]any{
					"entity_type": e.typ, "entity_id": e.id, "event_name": ev.name,
					"data": ev.data, "occurred_at": at.Format(time.RFC3339),
				}, nil)
			}})
		}
	}
	return acts
}
