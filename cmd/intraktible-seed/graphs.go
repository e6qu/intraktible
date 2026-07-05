// SPDX-License-Identifier: AGPL-3.0-or-later

// The flow fleet's graphs, ported from the retired TypeScript demo seed into the
// REAL engine's config dialect: multi-way band edges became chained binary splits
// (yes/no), output-node assignment sugar became explicit assignment nodes,
// manual_review labels are expressions (quoted literals; company_name reads the
// input's company_name field so escalated cases carry real subject names), the
// AML structuring heuristics are Starlark, and every AI node names the agent that
// serves it. Every graph passes the real ValidateGraph/ValidateFlow at publish.
package main

// gnode/gedge/graphOf build the wire-shape graph JSON.
func gnode(id, typ, name, lane string, config map[string]any) map[string]any {
	n := map[string]any{"id": id, "type": typ, "name": name, "lane": lane}
	if config != nil {
		n["config"] = config
	}
	return n
}

func gedge(from, to string) map[string]any { return map[string]any{"from": from, "to": to} }

func branch(from, to, label string) map[string]any {
	return map[string]any{"from": from, "to": to, "branch": label}
}

func graphOf(nodes, edges []map[string]any) map[string]any {
	return map[string]any{"nodes": nodes, "edges": edges}
}

// assigns builds an assignment-node config from target/expr pairs (order kept).
func assigns(pairs ...[2]string) map[string]any {
	out := make([]map[string]any, 0, len(pairs))
	for _, p := range pairs {
		out = append(out, map[string]any{"target": p[0], "expr": p[1]})
	}
	return map[string]any{"assignments": out}
}

// review builds a manual_review config. company_name reads the decide input's
// company_name field; caseType is a quoted literal expression.
func review(caseType string, slaDays int, suspend bool) map[string]any {
	cfg := map[string]any{
		"company_name": "company_name",
		"case_type":    "'" + caseType + "'",
		"sla_days":     slaDays,
	}
	if suspend {
		cfg["suspend"] = true
	}
	return cfg
}

func split(condition string) map[string]any { return map[string]any{"condition": condition} }

func aiNode(agent, output, prompt string) map[string]any {
	return map[string]any{"agent": agent, "output": output, "prompt": prompt}
}

func predict(model, output string) map[string]any {
	return map[string]any{"model": model, "output": output}
}

func connect(connector, output string) map[string]any {
	return map[string]any{"connector": connector, "output": output}
}

// Static prompts the AI nodes send — the scripted provider keys its handcrafted
// outputs on these exact strings.
const (
	promptAdverseAction = "Draft an adverse-action rationale from the risk drivers"
	promptSAR           = "Draft a SAR narrative from the transaction context"
	promptKYCExtract    = "Extract KYC fields from the submitted documents"
	promptFraudExplain  = "Explain the fraud score drivers for the analyst"
	promptDispute       = "Summarize the dispute and recommend representment vs refund"
	promptMerchantMemo  = "Write an underwriting memo for this merchant application"
	promptHardship      = "Summarize the hardship application and the proposed plan for the reviewer"
	promptAdjusterBrief = "Draft an adjuster brief with a pay/deny recommendation"
)

// --- Consumer credit ---------------------------------------------------------

func creditGraphV1() map[string]any {
	return graphOf(
		[]map[string]any{
			gnode("in", "input", "Loan application", "Intake", nil),
			gnode("enrich", "assignment", "Compute DTI", "Score", assigns([2]string{"dti", "(debt / income)"})),
			gnode("score", "predict", "PD model", "Score", predict("credit_pd", "pd")),
			gnode("derive", "assignment", "Risk", "Decide", assigns([2]string{"risk", "predict.pd.probability * 100"})),
			gnode("band", "split", "Risk band", "Decide", split("risk < 50")),
			gnode("approve", "assignment", "Approve", "Decide", assigns([2]string{"approved", "true"})),
			gnode("review", "manual_review", "Refer", "Decide", review("credit_review", 3, false)),
			gnode("out", "output", "Decision", "Decide", map[string]any{}),
		},
		[]map[string]any{
			gedge("in", "enrich"), gedge("enrich", "score"), gedge("score", "derive"), gedge("derive", "band"),
			branch("band", "approve", "yes"), branch("band", "review", "no"),
			gedge("approve", "out"), gedge("review", "out"),
		},
	)
}

func creditCoreNodes(withHold bool) []map[string]any {
	nodes := []map[string]any{
		gnode("in", "input", "Loan application", "Intake", nil),
		gnode("enrich", "assignment", "Enrich bureau features", "Intake", assigns(
			[2]string{"dti", "(debt / income)"},
			[2]string{"utilization", "(revolving_balance / credit_limit)"},
			[2]string{"delinquencies", "delinquencies_24m"},
		)),
		gnode("propensity", "predict", "Repayment propensity", "Score", predict("repayment_propensity", "propensity")),
		gnode("score", "predict", "Probability of default", "Score", predict("credit_pd", "pd")),
		gnode("derive", "assignment", "Derive risk + limit", "Score", assigns(
			[2]string{"risk", "predict.pd.probability * 100"},
			[2]string{"offered_limit", "risk >= 70 ? 0 : ((income - debt) / 12 * 4 < income * 0.1 ? (income - debt) / 12 * 4 : income * 0.1)"},
		)),
		gnode("narrative", "ai", "Adverse-action draft", "Score", aiNode("fraud-explainer", "rationale", promptAdverseAction)),
		gnode("band", "split", "Risk band", "Decide", split("risk < 35")),
		gnode("band2", "split", "Decline gate", "Decide", split("risk >= 70")),
		gnode("review", "manual_review", "Underwriter review", "Decide", review("credit_review", 3, false)),
		gnode("approve", "assignment", "Approve", "Decide", assigns([2]string{"approved", "true"})),
		gnode("decline", "assignment", "Decline", "Decide", assigns([2]string{"approved", "false"})),
		gnode("out", "output", "Credit decision", "Decide", map[string]any{}),
	}
	if withHold {
		nodes = append(nodes,
			gnode("holdgate", "split", "Verification gate", "Decide", split("risk >= 60")),
			gnode("hold", "manual_review", "Underwriting hold", "Decide", review("credit_review", 3, true)),
		)
	}
	return nodes
}

func creditCoreEdges(withHold bool) []map[string]any {
	edges := []map[string]any{
		gedge("enrich", "propensity"), gedge("propensity", "score"), gedge("score", "derive"),
		gedge("narrative", "band"),
		branch("band", "approve", "yes"), branch("band", "band2", "no"),
		branch("band2", "decline", "yes"),
		gedge("approve", "out"), gedge("decline", "out"), gedge("review", "out"),
	}
	if withHold {
		edges = append(edges,
			branch("band2", "holdgate", "no"),
			branch("holdgate", "hold", "yes"), branch("holdgate", "review", "no"),
			gedge("hold", "out"),
		)
	} else {
		edges = append(edges, branch("band2", "review", "no"))
	}
	return edges
}

func creditGraphV2() map[string]any {
	return graphOf(creditCoreNodes(false),
		append([]map[string]any{gedge("in", "enrich"), gedge("derive", "narrative")}, creditCoreEdges(false)...))
}

// v3 adds the live bureau pull, Reg B adverse-action codes, and the durable
// underwriting hold (a suspending human task for the deep-refer band).
func creditGraphV3() map[string]any {
	nodes := append(creditCoreNodes(true),
		gnode("bureau", "connect", "Experian bureau pull", "Intake", connect("experian", "bureau")),
		gnode("adverse", "reason", "Adverse-action codes", "Score", map[string]any{
			"reasons": []map[string]any{
				{"when": "dti >= 0.43", "code": "DTI_TOO_HIGH", "description": "Debt-to-income ratio too high"},
				{"when": "fico_score < 620", "code": "LOW_SCORE", "description": "Credit score below threshold"},
				{"when": "delinquencies >= 2", "code": "DELINQUENCY_HISTORY", "description": "Serious delinquency on file"},
				{"when": "utilization >= 0.75", "code": "UTILIZATION_HIGH", "description": "Revolving utilization too high"},
			},
		}),
	)
	edges := append([]map[string]any{
		gedge("in", "bureau"), gedge("bureau", "enrich"),
		gedge("derive", "adverse"), gedge("adverse", "narrative"),
	}, creditCoreEdges(true)...)
	return graphOf(nodes, edges)
}

func creditSchema() map[string]any {
	return objSchema(map[string]any{
		"income":               numProp(52000),
		"debt":                 numProp(14000),
		"revolving_balance":    numProp(4200),
		"credit_limit":         numProp(12000),
		"delinquencies_24m":    numProp(0),
		"fico_score":           numProp(668),
		"tenure_years":         numProp(4),
		"employment_stability": numProp(0.8),
		"dti":                  numProp(0.27),
		"utilization":          numProp(0.35),
		"delinquencies":        numProp(0),
		"company_name":         strProp("Sample Applicant LLC"),
	})
}

// --- AML screening -----------------------------------------------------------

func amlGraphV1() map[string]any {
	return graphOf(
		[]map[string]any{
			gnode("in", "input", "Transaction", "Intake", nil),
			gnode("rule", "assignment", "Flag large", "Score", assigns([2]string{"aml_score", "amount / 10000"})),
			gnode("band", "split", "Band", "Decide", split("aml_score >= 2")),
			gnode("review", "manual_review", "Review", "Decide", review("aml_alert", 5, false)),
			gnode("clear", "assignment", "Clear", "Decide", assigns([2]string{"cleared", "true"})),
			gnode("out", "output", "Outcome", "Decide", map[string]any{}),
		},
		[]map[string]any{
			gedge("in", "rule"), gedge("rule", "band"),
			branch("band", "review", "yes"), branch("band", "clear", "no"),
			gedge("clear", "out"), gedge("review", "out"),
		},
	)
}

func amlCoreNodes(withStructuring bool) []map[string]any {
	featPairs := [][2]string{
		{"cross_border", "origin_country != dest_country ? 1 : 0"},
		{"high_value", "amount > 10000 ? 1 : 0"},
	}
	if !withStructuring {
		// v2 predates the structuring heuristics: the flag exists (the clearing
		// policy reads it) but the composite scorer never sets it.
		featPairs = append(featPairs, [2]string{"structuring", "0"})
	}
	return []map[string]any{
		gnode("in", "input", "Wire / transfer", "Intake", nil),
		gnode("feat", "assignment", "Screening features", "Enrich", assigns(featPairs...)),
		gnode("sanctions", "assignment", "Sanctions hit", "Enrich", assigns(
			[2]string{"sanctions_hit", "watchlist_score >= 80 ? 1 : 0"})),
		gnode("score", "predict", "AML risk score", "Score", predict("aml_risk", "aml")),
		gnode("derive", "assignment", "Compose risk", "Score", assigns(
			[2]string{"aml_score", "predict.aml.score + sanctions_hit * 5"})),
		gnode("sar", "ai", "SAR narrative draft", "Score", aiNode("aml-narrative", "narrative", promptSAR)),
		gnode("review", "manual_review", "AML analyst review", "Decide", review("aml_alert", 5, false)),
		gnode("clear", "assignment", "Clear", "Decide", assigns([2]string{"cleared", "true"})),
		gnode("out", "output", "Screening outcome", "Decide", map[string]any{}),
	}
}

func amlGraphV2() map[string]any {
	nodes := append(amlCoreNodes(false),
		gnode("band", "split", "Risk band", "Decide", split("sanctions_hit == 1 || aml_score >= 6")),
		gnode("outcome", "assignment", "Screening outcome flags", "Decide", assigns(
			[2]string{"cleared", "sanctions_hit == 1 ? false : aml_score < 6"})),
	)
	return graphOf(nodes, []map[string]any{
		gedge("in", "feat"), gedge("feat", "sanctions"), gedge("sanctions", "score"),
		gedge("score", "derive"), gedge("derive", "sar"), gedge("sar", "band"),
		branch("band", "review", "yes"), branch("band", "clear", "no"),
		gedge("review", "outcome"), gedge("clear", "outcome"), gedge("outcome", "out"),
	})
}

// v3 adds the structuring heuristics (Starlark) the champion misses — the
// staging challenger arm measures how many extra referrals they produce.
func amlGraphV3() map[string]any {
	nodes := append(amlCoreNodes(true),
		gnode("struct", "code", "Structuring heuristics", "Enrich", map[string]any{
			"code": "# classic sub-threshold structuring + rapid pass-through\n" +
				"structuring = 1 if data['deposits_30d'] >= 4 and data['amount'] < 10000 else 0\n" +
				"rapid_movement = 1 if data['outflow_ratio'] > 0.9 else 0\n",
		}),
		gnode("band", "split", "Risk band", "Decide", split("sanctions_hit == 1 || structuring == 1 || aml_score >= 6")),
		gnode("outcome", "assignment", "Screening outcome flags", "Decide", assigns(
			[2]string{"cleared", "sanctions_hit != 1 && structuring != 1 && aml_score < 6"})),
	)
	return graphOf(nodes, []map[string]any{
		gedge("in", "feat"), gedge("feat", "sanctions"), gedge("sanctions", "struct"), gedge("struct", "score"),
		gedge("score", "derive"), gedge("derive", "sar"), gedge("sar", "band"),
		branch("band", "review", "yes"), branch("band", "clear", "no"),
		gedge("review", "outcome"), gedge("clear", "outcome"), gedge("outcome", "out"),
	})
}

func amlSchema() map[string]any {
	return objSchema(map[string]any{
		"amount":          numProp(52000),
		"origin_country":  strProp("US"),
		"dest_country":    strProp("KY"),
		"watchlist_score": numProp(10),
		"deposits_30d":    numProp(2),
		"outflow_ratio":   numProp(0.4),
		"cross_border":    numProp(1),
		"high_value":      numProp(1),
		"company_name":    strProp("Sample Trading LLC"),
	})
}

// --- KYC onboarding ----------------------------------------------------------

func kycCoreNodes(withHold bool) []map[string]any {
	nodes := []map[string]any{
		gnode("in", "input", "Onboarding packet", "Intake", nil),
		gnode("extract", "ai", "Document extract", "Enrich", aiNode("kyc-extract", "extracted", promptKYCExtract)),
		gnode("pep", "assignment", "PEP / adverse media", "Enrich", assigns(
			[2]string{"pep_flag", "pep_match >= 1 ? 1 : 0"},
			[2]string{"doc_quality", "doc_score"},
		)),
		gnode("score", "predict", "KYC vendor score", "Score", predict("kyc_score", "kyc")),
		gnode("derive", "assignment", "Identity confidence", "Score", assigns(
			[2]string{"identity_conf", "doc_quality - pep_flag * 40"})),
		gnode("gate", "split", "Verify gate", "Decide", split("identity_conf >= 60")),
		gnode("review", "manual_review", "EDD review", "Decide", review("kyc_review", 2, false)),
		gnode("pass", "assignment", "Verified", "Decide", assigns([2]string{"verified", "true"})),
		gnode("out", "output", "Onboarding result", "Decide", map[string]any{}),
	}
	if withHold {
		nodes = append(nodes,
			gnode("gate2", "split", "EDD hard-stop gate", "Decide", split("identity_conf < 40")),
			gnode("hold", "manual_review", "EDD hold", "Decide", review("kyc_review", 2, true)),
		)
	}
	return nodes
}

func kycCoreEdges(withHold bool) []map[string]any {
	edges := []map[string]any{
		gedge("extract", "pep"), gedge("pep", "score"), gedge("score", "derive"), gedge("derive", "gate"),
		branch("gate", "pass", "yes"),
		gedge("pass", "out"), gedge("review", "out"),
	}
	if withHold {
		edges = append(edges,
			branch("gate", "gate2", "no"),
			branch("gate2", "hold", "yes"), branch("gate2", "review", "no"),
			gedge("hold", "out"),
		)
	} else {
		edges = append(edges, branch("gate", "review", "no"))
	}
	return edges
}

func kycGraphV1() map[string]any {
	return graphOf(kycCoreNodes(false), append([]map[string]any{gedge("in", "extract")}, kycCoreEdges(false)...))
}

func kycGraphV2() map[string]any {
	nodes := append(kycCoreNodes(true),
		gnode("docv", "connect", "Jumio doc verification", "Enrich", connect("jumio-kyc", "docv")))
	return graphOf(nodes, append([]map[string]any{gedge("in", "docv"), gedge("docv", "extract")}, kycCoreEdges(true)...))
}

func kycSchema() map[string]any {
	return objSchema(map[string]any{
		"doc_score":    numProp(72),
		"pep_match":    numProp(0),
		"company_name": strProp("Sample Onboarding Ltd"),
	})
}

// --- Card fraud ---------------------------------------------------------------

func fraudNodes(withExplain, withTrust bool, reviewAt int) []map[string]any {
	deriveExpr := "predict.fraud.probability * 100"
	if withTrust {
		deriveExpr = "predict.fraud.probability * 100 + (trust_adj ?? 0)"
	}
	nodes := []map[string]any{
		gnode("in", "input", "Authorization", "Intake", nil),
		gnode("feat", "assignment", "Velocity + device", "Enrich", assigns(
			[2]string{"velocity", "tx_count_1h"},
			[2]string{"device_risk", "device_score"},
			[2]string{"amount_ratio", "(amount / avg_ticket)"},
		)),
		gnode("score", "predict", "Fraud model", "Score", predict("fraud_score", "fraud")),
		gnode("derive", "assignment", "Fraud probability", "Score", assigns([2]string{"fraud_p", deriveExpr})),
		gnode("band", "split", "Fraud band", "Decide", split("fraud_p >= 80")),
		gnode("band2", "split", "Review band", "Decide", split(fmtName("fraud_p >= %d", reviewAt))),
		gnode("review", "manual_review", "Fraud analyst review", "Decide", review("fraud_review", 1, false)),
		gnode("block", "assignment", "Block", "Decide", assigns([2]string{"blocked", "true"})),
		gnode("allow", "assignment", "Allow", "Decide", assigns([2]string{"blocked", "false"})),
		gnode("out", "output", "Auth decision", "Decide", map[string]any{}),
	}
	if withExplain {
		nodes = append(nodes, gnode("explain", "ai", "Explanation", "Score",
			aiNode("fraud-explainer", "explanation", promptFraudExplain)))
	}
	if withTrust {
		nodes = append(nodes, gnode("trust", "rule", "Trusted-customer rules", "Enrich", map[string]any{
			"rules": []map[string]any{
				// Baseline first so trust_adj is ALWAYS defined — the engine's
				// expression checker rejects a read of a never-assigned name.
				{"when": "true", "then": []map[string]any{{"target": "trust_adj", "expr": "0"}}},
				{"when": "card_present == 1 && tx_count_1h <= 1", "then": []map[string]any{{"target": "trust_adj", "expr": "-8"}}},
				{"when": "new_device == 1", "then": []map[string]any{{"target": "trust_adj", "expr": "6"}}},
			},
		}))
	}
	return nodes
}

func fraudEdges(withExplain, withTrust bool) []map[string]any {
	edges := []map[string]any{gedge("in", "feat")}
	if withTrust {
		edges = append(edges, gedge("feat", "trust"), gedge("trust", "score"))
	} else {
		edges = append(edges, gedge("feat", "score"))
	}
	if withExplain {
		edges = append(edges, gedge("score", "derive"), gedge("derive", "explain"), gedge("explain", "band"))
	} else {
		edges = append(edges, gedge("score", "derive"), gedge("derive", "band"))
	}
	return append(edges,
		branch("band", "block", "yes"), branch("band", "band2", "no"),
		branch("band2", "review", "yes"), branch("band2", "allow", "no"),
		gedge("block", "out"), gedge("allow", "out"), gedge("review", "out"),
	)
}

func fraudGraphV1() map[string]any {
	return graphOf(fraudNodes(false, false, 40), fraudEdges(false, false))
}
func fraudGraphV2() map[string]any {
	return graphOf(fraudNodes(true, false, 40), fraudEdges(true, false))
}
func fraudGraphV3() map[string]any {
	return graphOf(fraudNodes(true, false, 35), fraudEdges(true, false))
}
func fraudGraphV4() map[string]any {
	return graphOf(fraudNodes(true, true, 35), fraudEdges(true, true))
}

func fraudSchema() map[string]any {
	return objSchema(map[string]any{
		"amount":       numProp(240),
		"tx_count_1h":  numProp(6),
		"device_score": numProp(45),
		"avg_ticket":   numProp(120),
		"card_present": numProp(1),
		"new_device":   numProp(0),
		"velocity":     numProp(6),
		"device_risk":  numProp(45),
		"amount_ratio": numProp(2),
		"company_name": strProp("Sample Card 0001"),
	})
}

// --- Dispute / chargeback triage ----------------------------------------------

func disputeSharedTail() []map[string]any {
	return []map[string]any{
		gnode("summary", "ai", "Dispute summary", "Triage", aiNode("dispute-summarizer", "summary", promptDispute)),
		gnode("derive", "assignment", "Triage score", "Triage", assigns(
			[2]string{"triage", "high_value * 50 + liability * 40"})),
		gnode("band", "split", "Triage band", "Decide", split("triage >= 50")),
		gnode("review", "manual_review", "Disputes ops review", "Decide", review("dispute", 7, false)),
		gnode("refund", "assignment", "Auto-refund", "Decide", assigns([2]string{"outcome", `"refund"`})),
		gnode("out", "output", "Disposition", "Decide", map[string]any{}),
	}
}

func disputeGraphV1() map[string]any {
	nodes := append([]map[string]any{
		gnode("in", "input", "Dispute intake", "Intake", nil),
		gnode("classify", "assignment", "Classify + liability", "Triage", assigns(
			[2]string{"high_value", "amount > 500 ? 1 : 0"},
			[2]string{"liability", `reason_code == "fraud" ? 1 : 0`},
		)),
	}, disputeSharedTail()...)
	return graphOf(nodes, []map[string]any{
		gedge("in", "classify"), gedge("classify", "summary"), gedge("summary", "derive"), gedge("derive", "band"),
		branch("band", "review", "yes"), branch("band", "refund", "no"),
		gedge("refund", "out"), gedge("review", "out"),
	})
}

func disputeGraphV2() map[string]any {
	row := func(when, liability, evidence string) map[string]any {
		return map[string]any{"when": when, "outputs": []map[string]any{
			{"target": "liability", "expr": liability},
			{"target": "evidence", "expr": evidence},
		}}
	}
	nodes := append([]map[string]any{
		gnode("in", "input", "Dispute intake", "Intake", nil),
		gnode("liability", "decision_table", "Reason-code liability", "Triage", map[string]any{
			"hit": "first",
			"rows": []map[string]any{
				row(`reason_code == "fraud"`, "1", `"4837 affidavit + device history"`),
				row(`reason_code == "product_not_received"`, "0", `"carrier tracking + delivery confirmation"`),
				row(`reason_code == "duplicate"`, "0", `"settlement records"`),
				row("true", "0", `"merchant response"`),
			},
		}),
		gnode("value", "assignment", "Value tier", "Triage", assigns([2]string{"high_value", "amount > 500 ? 1 : 0"})),
	}, disputeSharedTail()...)
	return graphOf(nodes, []map[string]any{
		gedge("in", "liability"), gedge("liability", "value"), gedge("value", "summary"),
		gedge("summary", "derive"), gedge("derive", "band"),
		branch("band", "review", "yes"), branch("band", "refund", "no"),
		gedge("refund", "out"), gedge("review", "out"),
	})
}

func disputeSchema() map[string]any {
	return objSchema(map[string]any{
		"amount":       numProp(820),
		"reason_code":  strProp("fraud"),
		"company_name": strProp("Sample Dispute #0001"),
	})
}

// --- Merchant onboarding --------------------------------------------------------

func merchantGraphV1() map[string]any {
	return graphOf(
		[]map[string]any{
			gnode("in", "input", "Merchant application", "Intake", nil),
			gnode("feat", "assignment", "MCC + volume risk", "Enrich", assigns(
				[2]string{"high_risk_mcc", "mcc_risk >= 70 ? 1 : 0"},
				[2]string{"amount", "monthly_volume"},
				[2]string{"cross_border", "international == 1 ? 1 : 0"},
			)),
			gnode("score", "predict", "Merchant risk score", "Score", predict("aml_risk", "mrisk")),
			gnode("derive", "assignment", "Underwriting score", "Score", assigns(
				[2]string{"uw_score", "predict.mrisk.score + high_risk_mcc * 30"})),
			gnode("gate", "split", "Underwriting gate", "Decide", split("uw_score >= 25")),
			gnode("review", "manual_review", "Underwriting review", "Decide", review("merchant_review", 4, false)),
			gnode("approve", "assignment", "Board merchant", "Decide", assigns([2]string{"boarded", "true"})),
			gnode("out", "output", "Boarding result", "Decide", map[string]any{}),
		},
		[]map[string]any{
			gedge("in", "feat"), gedge("feat", "score"), gedge("score", "derive"), gedge("derive", "gate"),
			branch("gate", "review", "yes"), branch("gate", "approve", "no"),
			gedge("approve", "out"), gedge("review", "out"),
		},
	)
}

func merchantGraphV2() map[string]any {
	tier := func(when, adder string) map[string]any {
		return map[string]any{"when": when, "outputs": []map[string]any{{"target": "mcc_adder", "expr": adder}}}
	}
	return graphOf(
		[]map[string]any{
			gnode("in", "input", "Merchant application", "Intake", nil),
			gnode("feat", "assignment", "Volume features", "Enrich", assigns(
				[2]string{"amount", "monthly_volume"},
				[2]string{"high_value", "monthly_volume > 100000 ? 1 : 0"},
				[2]string{"cross_border", "international == 1 ? 1 : 0"},
			)),
			gnode("mcc", "decision_table", "MCC tier adder", "Enrich", map[string]any{
				"hit": "first",
				"rows": []map[string]any{
					tier("mcc_risk >= 70", "30"), tier("mcc_risk >= 40", "15"), tier("true", "0"),
				},
			}),
			gnode("score", "predict", "Merchant risk score", "Score", predict("aml_risk", "mrisk")),
			gnode("derive", "assignment", "Underwriting score", "Score", assigns(
				[2]string{"uw_score", "predict.mrisk.score + mcc_adder"})),
			gnode("memo", "ai", "Underwriting memo", "Score", aiNode("merchant-memo", "memo", promptMerchantMemo)),
			gnode("gate", "split", "Underwriting gate", "Decide", split("uw_score >= 25")),
			gnode("review", "manual_review", "Underwriting review", "Decide", review("merchant_review", 4, false)),
			gnode("approve", "assignment", "Board merchant", "Decide", assigns([2]string{"boarded", "true"})),
			gnode("out", "output", "Boarding result", "Decide", map[string]any{}),
		},
		[]map[string]any{
			gedge("in", "feat"), gedge("feat", "mcc"), gedge("mcc", "score"), gedge("score", "derive"),
			gedge("derive", "memo"), gedge("memo", "gate"),
			branch("gate", "review", "yes"), branch("gate", "approve", "no"),
			gedge("approve", "out"), gedge("review", "out"),
		},
	)
}

func merchantSchema() map[string]any {
	return objSchema(map[string]any{
		"monthly_volume": numProp(90000),
		"mcc_risk":       numProp(55),
		"international":  numProp(1),
		"amount":         numProp(90000),
		"cross_border":   numProp(1),
		"high_value":     numProp(0),
		"company_name":   strProp("Sample Merchant Co"),
	})
}

// --- Collections hardship --------------------------------------------------------

func collectionsCoreNodes() []map[string]any {
	return []map[string]any{
		gnode("in", "input", "Hardship application", "Intake", nil),
		gnode("verify", "assignment", "Verify income change", "Intake", assigns(
			[2]string{"income_drop", "1 - current_income / prior_income"},
			[2]string{"missed", "missed_payments_6m"},
			// Baseline so the final outcome expression reads a defined flag on the
			// review path too — the engine rejects a read of a never-assigned name.
			[2]string{"enrolled", "false"},
		)),
		gnode("score", "scorecard", "Hardship scorecard", "Assess", map[string]any{
			"output": "hardship_score",
			"factors": []map[string]any{
				{"when": "income_drop >= 0.3", "weight": 30},
				{"when": "missed >= 2", "weight": 20},
				{"when": "medical_event == 1", "weight": 25},
				{"when": "tenure_years >= 3", "weight": 10},
				{"when": "balance_usd > 10000", "weight": 15},
			},
		}),
		gnode("gate", "split", "Program gate", "Resolve", split("hardship_score >= 70")),
		gnode("gate2", "split", "Plan gate", "Resolve", split("hardship_score >= 45")),
		gnode("review", "manual_review", "Hardship supervisor review", "Resolve", review("hardship_review", 5, false)),
		gnode("offer", "assignment", "Offer plan", "Resolve", assigns([2]string{"enrolled", "true"})),
		gnode("standard", "assignment", "Standard collections", "Resolve", assigns([2]string{"enrolled", "false"})),
		gnode("final", "assignment", "Program outcome", "Resolve", assigns(
			[2]string{"outcome", `enrolled ? "hardship_plan" : "standard_collections"`})),
		gnode("out", "output", "Hardship outcome", "Resolve", map[string]any{}),
	}
}

func collectionsGateEdges() []map[string]any {
	return []map[string]any{
		branch("gate", "review", "yes"), branch("gate", "gate2", "no"),
		branch("gate2", "offer", "yes"), branch("gate2", "standard", "no"),
		gedge("review", "final"), gedge("offer", "final"), gedge("standard", "final"), gedge("final", "out"),
	}
}

func collectionsGraphV1() map[string]any {
	return graphOf(collectionsCoreNodes(), append([]map[string]any{
		gedge("in", "verify"), gedge("verify", "score"), gedge("score", "gate"),
	}, collectionsGateEdges()...))
}

func collectionsGraphV2() map[string]any {
	terms := func(when, months, relief string) map[string]any {
		return map[string]any{"when": when, "outputs": []map[string]any{
			{"target": "plan_months", "expr": months},
			{"target": "rate_relief", "expr": relief},
		}}
	}
	nodes := append(collectionsCoreNodes(),
		gnode("plan", "decision_table", "Plan terms", "Assess", map[string]any{
			"hit": "first",
			"rows": []map[string]any{
				terms("hardship_score >= 70", "12", "0.5"),
				terms("hardship_score >= 45", "6", "0.25"),
				terms("true", "0", "0"),
			},
		}),
		gnode("summary", "ai", "Hardship summary", "Assess", aiNode("collections-planner", "summary", promptHardship)),
	)
	return graphOf(nodes, append([]map[string]any{
		gedge("in", "verify"), gedge("verify", "score"), gedge("score", "plan"),
		gedge("plan", "summary"), gedge("summary", "gate"),
	}, collectionsGateEdges()...))
}

func collectionsSchema() map[string]any {
	return objSchema(map[string]any{
		"prior_income":       numProp(5200),
		"current_income":     numProp(3100),
		"missed_payments_6m": numProp(2),
		"medical_event":      numProp(0),
		"tenure_years":       numProp(4),
		"balance_usd":        numProp(8400),
		"company_name":       strProp("Sample Household"),
	})
}

// --- Purchase-protection claim triage ---------------------------------------------

func claimCoreNodes() []map[string]any {
	return []map[string]any{
		gnode("in", "input", "Claim intake", "Intake", nil),
		gnode("ratio", "assignment", "Coverage ratio", "Assess", assigns(
			[2]string{"amount_ratio", "amount / coverage_limit"})),
		gnode("score", "predict", "Claim abuse model", "Assess", predict("claim_fraud", "cfraud")),
		gnode("severity", "assignment", "Abuse probability", "Assess", assigns(
			[2]string{"fraud_p", "predict.cfraud.probability * 100"})),
		gnode("brief", "ai", "Adjuster brief", "Assess", aiNode("claims-adjuster-brief", "brief", promptAdjusterBrief)),
		gnode("review", "manual_review", "Adjuster review", "Decide", review("claim_review", 3, false)),
		gnode("pay", "assignment", "Pay claim", "Decide", assigns(
			[2]string{"paid", "true"}, [2]string{"payout", "amount"})),
		gnode("deny", "assignment", "Deny claim", "Decide", assigns([2]string{"paid", "false"})),
		gnode("out", "output", "Claim outcome", "Decide", map[string]any{}),
	}
}

func claimGraphV1() map[string]any {
	nodes := append(claimCoreNodes(),
		gnode("g1", "split", "Policy active gate", "Decide", split("policy_active == 0")),
		gnode("g2", "split", "Abuse gate", "Decide", split("fraud_p >= 60")),
		gnode("g3", "split", "Severity gate", "Decide", split("amount_ratio > 0.5")),
	)
	return graphOf(nodes, []map[string]any{
		gedge("in", "ratio"), gedge("ratio", "score"), gedge("score", "severity"),
		gedge("severity", "brief"), gedge("brief", "g1"),
		branch("g1", "deny", "yes"), branch("g1", "g2", "no"),
		branch("g2", "review", "yes"), branch("g2", "g3", "no"),
		branch("g3", "review", "yes"), branch("g3", "pay", "no"),
		gedge("review", "out"), gedge("pay", "out"), gedge("deny", "out"),
	})
}

func claimGraphV2() map[string]any {
	nodes := append(claimCoreNodes(),
		gnode("rules", "rule", "Fast-track rules", "Intake", map[string]any{
			"rules": []map[string]any{
				// Baselines first so both flags are ALWAYS defined.
				{"when": "true", "then": []map[string]any{
					{"target": "fast_track", "expr": "0"}, {"target": "lapsed", "expr": "0"},
				}},
				{"when": "amount <= 200 && policy_active == 1 && prior_claims_24m == 0",
					"then": []map[string]any{{"target": "fast_track", "expr": "1"}}},
				{"when": "policy_active == 0", "then": []map[string]any{{"target": "lapsed", "expr": "1"}}},
			},
		}),
		gnode("reasons", "reason", "Denial & referral reasons", "Assess", map[string]any{
			"reasons": []map[string]any{
				{"when": "lapsed == 1", "code": "POLICY_LAPSED", "description": "Protection plan lapsed before the loss date"},
				{"when": "amount_ratio > 1", "code": "OVER_COVERAGE", "description": "Claim exceeds the coverage limit"},
				{"when": "fraud_p >= 60", "code": "CLAIM_FRAUD_SIGNALS", "description": "Model flags abuse-pattern signals"},
			},
		}),
		gnode("g1", "split", "Lapse gate", "Decide", split("lapsed == 1")),
		gnode("g2", "split", "Abuse gate", "Decide", split("fraud_p >= 60")),
		gnode("g3", "split", "Fast-track gate", "Decide", split("fast_track == 1")),
		gnode("g4", "split", "Severity gate", "Decide", split("amount_ratio > 0.5")),
	)
	return graphOf(nodes, []map[string]any{
		gedge("in", "rules"), gedge("rules", "ratio"), gedge("ratio", "score"), gedge("score", "severity"),
		gedge("severity", "reasons"), gedge("reasons", "brief"), gedge("brief", "g1"),
		branch("g1", "deny", "yes"), branch("g1", "g2", "no"),
		branch("g2", "review", "yes"), branch("g2", "g3", "no"),
		branch("g3", "pay", "yes"), branch("g3", "g4", "no"),
		branch("g4", "review", "yes"), branch("g4", "pay", "no"),
		gedge("review", "out"), gedge("pay", "out"), gedge("deny", "out"),
	})
}

func claimSchema() map[string]any {
	return objSchema(map[string]any{
		"amount":                  numProp(1900),
		"coverage_limit":          numProp(3000),
		"policy_active":           numProp(1),
		"prior_claims_24m":        numProp(1),
		"days_since_policy_start": numProp(210),
		"amount_ratio":            numProp(0.63),
		"company_name":            strProp("Claim CLM-0000"),
	})
}

// --- Marketplace payout risk -------------------------------------------------------

func payoutCoreNodes() []map[string]any {
	return []map[string]any{
		gnode("in", "input", "Payout request", "Intake", nil),
		gnode("ledger", "connect", "Core-banking ledger", "Intake", connect("core-banking", "ledger")),
		gnode("feat", "assignment", "Payout features", "Score", assigns(
			[2]string{"payout_ratio", "amount / avg_payout_30d"},
			[2]string{"new_account", "account_age_days < 30 ? 1 : 0"},
			[2]string{"nsf_12m", "connect.ledger.nsf_12m"},
		)),
		gnode("score", "predict", "Payout risk score", "Score", predict("payout_risk", "prisk")),
		gnode("level", "assignment", "Risk level", "Score", assigns([2]string{"payout_score", "predict.prisk.score"})),
		gnode("review", "manual_review", "Payout ops review", "Decide", review("payout_review", 2, false)),
		gnode("hold", "assignment", "Hold funds", "Decide", assigns(
			[2]string{"released", "false"}, [2]string{"hold_reason", `"risk_hold"`})),
		gnode("release", "assignment", "Release payout", "Decide", assigns([2]string{"released", "true"})),
		gnode("out", "output", "Payout decision", "Decide", map[string]any{}),
	}
}

func payoutGraphV1() map[string]any {
	nodes := append(payoutCoreNodes(),
		gnode("gate", "split", "Release gate", "Decide", split("payout_score >= 60")),
		gnode("gate2", "split", "Review gate", "Decide", split("payout_score >= 30")),
	)
	return graphOf(nodes, []map[string]any{
		gedge("in", "ledger"), gedge("ledger", "feat"), gedge("feat", "score"), gedge("score", "level"),
		gedge("level", "gate"),
		branch("gate", "hold", "yes"), branch("gate", "gate2", "no"),
		branch("gate2", "review", "yes"), branch("gate2", "release", "no"),
		gedge("hold", "out"), gedge("review", "out"), gedge("release", "out"),
	})
}

// v2 routes through a risk × amount matrix (medium-risk small payouts
// auto-release) and adds the suspending ops hold for large flagged payouts.
func payoutGraphV2() map[string]any {
	nodes := append(payoutCoreNodes(),
		gnode("matrix", "2d_matrix", "Risk × amount action", "Decide", map[string]any{
			"output": "action",
			"rows": []map[string]any{
				{"when": "payout_score >= 60"}, {"when": "payout_score >= 30"}, {"when": "payout_score < 30"},
			},
			"cols": []map[string]any{{"when": "amount >= 10000"}, {"when": "amount < 10000"}},
			"cells": [][]string{
				{"hold", "hold"},
				{"review", "release"},
				{"release", "release"},
			},
		}),
		gnode("gate", "split", "Hold gate", "Decide", split(`action == "hold"`)),
		gnode("gate2", "split", "Review gate", "Decide", split(`action == "review"`)),
		gnode("gate3", "split", "Ops hold gate", "Decide", split("amount >= 15000 && payout_score >= 45")),
		gnode("ops_hold", "manual_review", "Payout ops hold", "Decide", review("payout_review", 2, true)),
	)
	return graphOf(nodes, []map[string]any{
		gedge("in", "ledger"), gedge("ledger", "feat"), gedge("feat", "score"), gedge("score", "level"),
		gedge("level", "matrix"), gedge("matrix", "gate"),
		branch("gate", "hold", "yes"), branch("gate", "gate2", "no"),
		branch("gate2", "gate3", "yes"), branch("gate2", "release", "no"),
		branch("gate3", "ops_hold", "yes"), branch("gate3", "review", "no"),
		gedge("hold", "out"), gedge("review", "out"), gedge("release", "out"), gedge("ops_hold", "out"),
	})
}

func payoutSchema() map[string]any {
	return objSchema(map[string]any{
		"amount":           numProp(12500),
		"avg_payout_30d":   numProp(5200),
		"payouts_24h":      numProp(2),
		"account_age_days": numProp(210),
		"chargeback_rate":  numProp(0.011),
		"payout_ratio":     numProp(2.4),
		"new_account":      numProp(0),
		"company_name":     strProp("Sample Marketplace Seller"),
	})
}

// --- Card limit increase -----------------------------------------------------------

func limitGraphV1() map[string]any {
	return graphOf(
		[]map[string]any{
			gnode("in", "input", "CLI request", "Intake", nil),
			gnode("usage", "assignment", "Usage features", "Score", assigns(
				[2]string{"utilization", "revolving_balance / credit_limit"},
				[2]string{"dti", "debt / income"},
				[2]string{"delinquencies", "delinquencies_24m"},
			)),
			gnode("score", "predict", "PD model", "Score", predict("credit_pd", "pd")),
			gnode("derive", "assignment", "Risk + proposed limit", "Score", assigns(
				[2]string{"risk", "predict.pd.probability * 100"},
				[2]string{"proposed_limit", "risk < 20 ? credit_limit * 1.5 : credit_limit * 1.25"},
			)),
			gnode("gate", "split", "CLI gate", "Decide", split("risk < 20 && utilization < 0.6")),
			gnode("gate2", "split", "Review gate", "Decide", split("risk < 45")),
			gnode("review", "manual_review", "Credit ops review", "Decide", review("limit_review", 2, false)),
			gnode("grant", "assignment", "Grant increase", "Decide", assigns([2]string{"granted", "true"})),
			gnode("refuse", "assignment", "Keep current limit", "Decide", assigns([2]string{"granted", "false"})),
			gnode("out", "output", "CLI decision", "Decide", map[string]any{}),
		},
		[]map[string]any{
			gedge("in", "usage"), gedge("usage", "score"), gedge("score", "derive"), gedge("derive", "gate"),
			branch("gate", "grant", "yes"), branch("gate", "gate2", "no"),
			branch("gate2", "review", "yes"), branch("gate2", "refuse", "no"),
			gedge("grant", "out"), gedge("review", "out"), gedge("refuse", "out"),
		},
	)
}

func limitSchema() map[string]any {
	return objSchema(map[string]any{
		"income":            numProp(74000),
		"debt":              numProp(24000),
		"revolving_balance": numProp(7900),
		"credit_limit":      numProp(12000),
		"delinquencies_24m": numProp(0),
		"fico_score":        numProp(690),
		"dti":               numProp(0.32),
		"utilization":       numProp(0.66),
		"delinquencies":     numProp(0),
		"company_name":      strProp("CLI · Card 0000"),
	})
}

// --- schema helpers -----------------------------------------------------------------

func objSchema(props map[string]any) map[string]any {
	return map[string]any{"type": "object", "properties": props}
}

func numProp(example float64) map[string]any {
	return map[string]any{"type": "number", "example": example}
}
func strProp(example string) map[string]any {
	return map[string]any{"type": "string", "example": example}
}
