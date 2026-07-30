// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"net/http"
	"net/url"
	"time"
)

const demoExperimentDecisions = 40

func experimentSpec(flowID, environment, name, salt string) map[string]any {
	return map[string]any{
		"name": name, "hypothesis": "A clearer onboarding treatment increases qualified conversion",
		"owner": actorPriya, "flow_id": flowID, "environment": environment,
		"subject_key_expression": "company_name", "salt": salt,
		"arms": []map[string]any{
			{"key": "control", "name": "Current onboarding", "kind": "champion", "version": 2, "allocation_bps": 5000},
			{"key": "clear-terms", "name": "Prior onboarding", "kind": "challenger", "version": 1, "allocation_bps": 5000},
		},
		"primary_metric": map[string]any{
			"key": "qualified_conversion", "name": "Qualified conversion",
			"kind": "binary", "direction": "increase",
		},
		"guardrails": []map[string]any{{
			"key": "early_delinquency", "name": "Early delinquency",
			"kind": "binary", "direction": "decrease", "max_regression": 0.05,
		}},
		"minimum_sample_per_arm": 2, "minimum_effect": 0.05,
		"confidence": 0.95, "observation_window_days": 30,
	}
}

// experimentActions creates one completed evidence-rich sandbox cohort and one
// production launch awaiting an independent checker. Every exposure is produced
// by the real decide path and every observed fact is recorded through the
// general outcome API, so the demo's experiment screens are replayed product
// state rather than presentation fixtures.
func (s *seeder) experimentActions(anchor time.Time) []action {
	return []action{{
		at: anchor.Add(-5 * time.Hour), name: "governed experiment evidence", run: func() {
			var created struct {
				ExperimentID string `json:"experiment_id"`
			}
			s.call(actorPriya, http.MethodPost, "/v1/experiments",
				experimentSpec(s.flowID("merchant-onboarding"), "sandbox", "Merchant onboarding copy", "merchant-onboarding-copy-v1"),
				&created)
			s.setID("experiment:affordability", created.ExperimentID)
			s.call(actorPriya, http.MethodPost,
				"/v1/experiments/"+url.PathEscape(created.ExperimentID)+"/start", nil, nil)

			armCounts := map[string]int{"control": 0, "clear-terms": 0}
			profile := trafficProfiles()["merchant-onboarding"]
			for i := 0; i < demoExperimentDecisions; i++ {
				input := profile.input(i, "approve", fmtName("Experiment Applicant %02d", i+1))
				var decision struct {
					DecisionID   string `json:"decision_id"`
					Status       string `json:"status"`
					ExperimentID string `json:"experiment_id"`
					Arm          string `json:"experiment_arm"`
				}
				s.call(actorSvcCI, http.MethodPost, "/v1/flows/merchant-onboarding/sandbox/decide",
					map[string]any{"data": input}, &decision)
				if decision.Status != "completed" || decision.ExperimentID != created.ExperimentID ||
					(decision.Arm != "control" && decision.Arm != "clear-terms") {
					fatalf("demo experiment decision %d has incoherent treatment: %+v", i, decision)
				}
				armIndex := armCounts[decision.Arm]
				armCounts[decision.Arm]++
				converted := 0.0
				if (decision.Arm == "control" && armIndex%5 == 0) ||
					(decision.Arm == "clear-terms" && armIndex%5 != 0) {
					converted = 1
				}
				earlyDelinquency := 0.0
				if armIndex%10 == 0 {
					earlyDelinquency = 1
				}
				record := func(metric string, value float64) {
					s.callWithHeaders(actorAva, http.MethodPost, "/v1/outcomes",
						map[string]string{"Idempotency-Key": fmtName("demo-%s-%02d", metric, i)},
						map[string]any{
							"decision_id": decision.DecisionID, "key": metric,
							"kind": "binary", "value": value,
							"event_time": s.clk.Now().Format(time.RFC3339Nano),
							"source": map[string]any{
								"system":    "demo-warehouse",
								"record_id": fmtName("experiment-%02d-%s", i, metric),
								"lineage":   "analytics.experiment_outcomes_v2",
							},
							"label_version": "qualified-conversion-v2",
						}, nil)
				}
				record("qualified_conversion", converted)
				record("early_delinquency", earlyDelinquency)
			}
			if armCounts["control"] < 5 || armCounts["clear-terms"] < 5 {
				fatalf("demo experiment underpowered allocation: %+v", armCounts)
			}
			s.call(actorPriya, http.MethodPost,
				"/v1/experiments/"+url.PathEscape(created.ExperimentID)+"/complete",
				map[string]string{"reason": "Thirty-day observation window closed"}, nil)
			var analysis struct {
				Status string `json:"status"`
				Reason string `json:"reason"`
			}
			s.call(actorPriya, http.MethodGet,
				"/v1/experiments/"+url.PathEscape(created.ExperimentID)+"/analysis", nil, &analysis)
			if analysis.Status != "winner" {
				fatalf("demo experiment analysis = %q (%s), want winner; allocation=%+v",
					analysis.Status, analysis.Reason, armCounts)
			}
		},
	}, {
		at: anchor.Add(-4 * time.Hour), name: "pending production experiment", run: func() {
			var created struct {
				ExperimentID string `json:"experiment_id"`
			}
			s.call(actorPriya, http.MethodPost, "/v1/experiments",
				experimentSpec(s.flowID("kyc-onboarding"), "production", "Identity verification disclosure", "identity-disclosure-v1"),
				&created)
			s.setID("experiment:income-copy", created.ExperimentID)
			s.call(actorPriya, http.MethodPost,
				"/v1/experiments/"+url.PathEscape(created.ExperimentID)+"/start", nil, nil)
		},
	}}
}

// populationActions leaves a completed, downloadable, version-pinned backtest
// in the operator surface. The assembled server's real worker owns execution.
func (s *seeder) populationActions(anchor time.Time) []action {
	return []action{{
		at: anchor.Add(-3 * time.Hour), name: "durable population backtest", run: func() {
			items := make([]map[string]any, 12)
			profile := trafficProfiles()["merchant-onboarding"]
			for i := range items {
				items[i] = map[string]any{
					"data": profile.input(i, "approve", fmtName("Backtest Applicant %02d", i+1)),
				}
			}
			var created struct {
				JobID string `json:"job_id"`
			}
			s.callWithHeaders(actorAva, http.MethodPost, "/v1/population-jobs",
				map[string]string{"Idempotency-Key": "demo-merchant-backtest-v1"},
				map[string]any{
					"kind": "backtest", "slug": "merchant-onboarding", "environment": "sandbox",
					"items": items, "max_attempts": 2, "concurrency": 4, "retention_days": 90,
				}, &created)
			s.setID("population:merchant-backtest", created.JobID)
			for poll := 0; poll < 200; poll++ {
				var job struct {
					State     string `json:"state"`
					Succeeded int    `json:"succeeded"`
					Failed    int    `json:"failed"`
				}
				s.call(actorAva, http.MethodGet,
					"/v1/population-jobs/"+url.PathEscape(created.JobID), nil, &job)
				if job.State == "completed" {
					if job.Succeeded != len(items) || job.Failed != 0 {
						fatalf("demo population job result: %+v", job)
					}
					return
				}
				if job.State == "completed_with_errors" || job.State == "cancelled" {
					fatalf("demo population job terminal failure: %+v", job)
				}
				time.Sleep(10 * time.Millisecond)
			}
			fatalf("demo population job did not complete")
		},
	}}
}
