// SPDX-License-Identifier: AGPL-3.0-or-later

package governance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
)

// BuildCampaign deterministically derives all aggregate statistics and blocking
// policy from immutable trial evidence. Provider execution is an imperative
// concern; grading and release admission remain replayable.
func BuildCampaign(
	campaignID, templateID string,
	release int,
	suite EvalSuite,
	trials []TrialResult,
) (CampaignResult, error) {
	if err := suite.Validate(); err != nil {
		return CampaignResult{}, err
	}
	expected := len(suite.Cases) * suite.Trials
	if len(trials) != expected {
		return CampaignResult{}, fmt.Errorf(
			"agent governance: suite %s@%d requires %d trials, got %d",
			suite.SuiteID, suite.Version, expected, len(trials),
		)
	}
	cases := make(map[string]EvalCase, len(suite.Cases))
	for _, item := range suite.Cases {
		cases[item.CaseID] = item
	}
	seen := make(map[string]bool, expected)
	passed := 0
	requiredFailure := false
	out := append([]TrialResult(nil), trials...)
	for _, trial := range out {
		if err := trial.Validate(); err != nil {
			return CampaignResult{}, err
		}
		item, ok := cases[trial.CaseID]
		if !ok {
			return CampaignResult{}, fmt.Errorf("agent governance: trial references unknown case %q", trial.CaseID)
		}
		if trial.Trial > suite.Trials {
			return CampaignResult{}, fmt.Errorf(
				"agent governance: case %q trial %d exceeds configured count %d",
				trial.CaseID, trial.Trial, suite.Trials,
			)
		}
		if trial.Grade != nil {
			if trial.Grade.Kind != item.Grader {
				return CampaignResult{}, fmt.Errorf(
					"agent governance: case %q grader kind does not match its immutable suite",
					trial.CaseID,
				)
			}
			expectedHash, err := graderDefinitionHash(item)
			if item.Grader == GraderSemantic {
				expectedHash, err = hashJSON(*suite.SemanticGrader)
				rubric := sha256.Sum256([]byte(item.Rubric))
				if trial.Grade.RubricHash != hex.EncodeToString(rubric[:]) {
					return CampaignResult{}, fmt.Errorf(
						"agent governance: case %q semantic rubric lineage does not match",
						trial.CaseID,
					)
				}
				if trial.Grade.Provider != suite.SemanticGrader.Provider ||
					trial.Grade.Model != suite.SemanticGrader.Model {
					return CampaignResult{}, fmt.Errorf(
						"agent governance: case %q semantic grader provider/model does not match",
						trial.CaseID,
					)
				}
			}
			if err != nil {
				return CampaignResult{}, err
			}
			if trial.Grade.GraderHash != expectedHash {
				return CampaignResult{}, fmt.Errorf(
					"agent governance: case %q grader definition hash does not match",
					trial.CaseID,
				)
			}
		}
		key := fmt.Sprintf("%s\x00%d", trial.CaseID, trial.Trial)
		if seen[key] {
			return CampaignResult{}, fmt.Errorf(
				"agent governance: duplicate trial %d for case %q",
				trial.Trial, trial.CaseID,
			)
		}
		seen[key] = true
		if trial.Passed {
			passed++
		} else if item.Severity == SeverityRequired || item.Severity == SeverityCritical {
			requiredFailure = true
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CaseID != out[j].CaseID {
			return out[i].CaseID < out[j].CaseID
		}
		return out[i].Trial < out[j].Trial
	})
	total := len(out)
	passRate := float64(passed) / float64(total)
	variance := passRate * (1 - passRate)
	low, high := wilson95(passed, total)
	blocking := suite.Required &&
		(requiredFailure || passRate < suite.MinPassRate || variance > suite.MaxVariance)
	return CampaignResult{
		CampaignID:     campaignID,
		TemplateID:     templateID,
		Release:        release,
		SuiteID:        suite.SuiteID,
		SuiteVersion:   suite.Version,
		Total:          total,
		Passed:         passed,
		PassRate:       passRate,
		Variance:       variance,
		ConfidenceLow:  low,
		ConfidenceHigh: high,
		Blocking:       blocking,
		Trials:         out,
	}, nil
}

func graderDefinitionHash(item EvalCase) (string, error) {
	return hashJSON(struct {
		Kind             GraderKind
		ExpectText       string
		ExpectJSON       json.RawMessage
		AllowedCitations []string
		Rubric           string
		MinScore         float64
	}{
		item.Grader, item.ExpectText, item.ExpectJSON, item.AllowedCitations,
		item.Rubric, item.MinScore,
	})
}

func wilson95(successes, total int) (float64, float64) {
	if total == 0 {
		return 0, 0
	}
	const z = 1.959963984540054
	n := float64(total)
	p := float64(successes) / n
	denominator := 1 + z*z/n
	center := (p + z*z/(2*n)) / denominator
	margin := z * math.Sqrt((p*(1-p)+z*z/(4*n))/n) / denominator
	return math.Max(0, center-margin), math.Min(1, center+margin)
}

func AssessCampaign(
	result CampaignResult,
	suite EvalSuite,
	adjudications []TrialAdjudication,
) (CampaignAssessment, error) {
	if err := result.Validate(); err != nil {
		return CampaignAssessment{}, err
	}
	if err := suite.Validate(); err != nil {
		return CampaignAssessment{}, err
	}
	if result.SuiteID != suite.SuiteID || result.SuiteVersion != suite.Version {
		return CampaignAssessment{}, fmt.Errorf(
			"agent governance: campaign %q does not match suite %s@%d",
			result.CampaignID, suite.SuiteID, suite.Version,
		)
	}
	cases := make(map[string]EvalCase, len(suite.Cases))
	for _, item := range suite.Cases {
		cases[item.CaseID] = item
	}
	decisions := make(map[string]bool, len(adjudications))
	for _, adjudication := range adjudications {
		if err := adjudication.Validate(); err != nil {
			return CampaignAssessment{}, err
		}
		if adjudication.CampaignID != result.CampaignID {
			return CampaignAssessment{}, fmt.Errorf(
				"agent governance: adjudication references campaign %q, want %q",
				adjudication.CampaignID, result.CampaignID,
			)
		}
		key := trialRef(adjudication.CaseID, adjudication.Trial)
		if _, exists := decisions[key]; exists {
			return CampaignAssessment{}, fmt.Errorf(
				"agent governance: trial %s was adjudicated more than once", key,
			)
		}
		decisions[key] = adjudication.Passed
	}
	passed := 0
	requiredFailure := false
	for _, trial := range result.Trials {
		item, found := cases[trial.CaseID]
		if !found {
			return CampaignAssessment{}, fmt.Errorf(
				"agent governance: trial references unknown case %q", trial.CaseID,
			)
		}
		effectivePassed := trial.Passed
		if adjudicated, found := decisions[trialRef(trial.CaseID, trial.Trial)]; found {
			effectivePassed = adjudicated
		}
		if effectivePassed {
			passed++
		} else if item.Severity == SeverityRequired || item.Severity == SeverityCritical {
			requiredFailure = true
		}
	}
	if len(decisions) > 0 {
		trials := make(map[string]bool, len(result.Trials))
		for _, trial := range result.Trials {
			trials[trialRef(trial.CaseID, trial.Trial)] = true
		}
		for key := range decisions {
			if !trials[key] {
				return CampaignAssessment{}, fmt.Errorf(
					"agent governance: adjudication references unknown trial %s", key,
				)
			}
		}
	}
	total := len(result.Trials)
	passRate := float64(passed) / float64(total)
	variance := passRate * (1 - passRate)
	low, high := wilson95(passed, total)
	return CampaignAssessment{
		Total: total, Passed: passed, PassRate: passRate, Variance: variance,
		ConfidenceLow: low, ConfidenceHigh: high,
		Blocking: suite.Required &&
			(requiredFailure || passRate < suite.MinPassRate || variance > suite.MaxVariance),
	}, nil
}

func CompareCampaigns(
	baseline CampaignView,
	challenger CampaignView,
	suite EvalSuite,
) (CampaignComparison, error) {
	if baseline.SuiteID != challenger.SuiteID ||
		baseline.SuiteVersion != challenger.SuiteVersion ||
		baseline.SuiteID != suite.SuiteID || baseline.SuiteVersion != suite.Version {
		return CampaignComparison{}, fmt.Errorf(
			"agent governance: comparison campaigns must use the exact same suite version",
		)
	}
	baselineAssessment, err := AssessCampaign(
		baseline.CampaignResult, suite, baseline.Adjudications,
	)
	if err != nil {
		return CampaignComparison{}, err
	}
	challengerAssessment, err := AssessCampaign(
		challenger.CampaignResult, suite, challenger.Adjudications,
	)
	if err != nil {
		return CampaignComparison{}, err
	}
	type counts struct {
		baseline, challenger int
		trials               int
	}
	byCase := make(map[string]*counts, len(suite.Cases))
	for _, item := range suite.Cases {
		byCase[item.CaseID] = &counts{trials: suite.Trials}
	}
	for _, trial := range baseline.Trials {
		if effectiveTrialPassed(trial, baseline.Adjudications) {
			byCase[trial.CaseID].baseline++
		}
	}
	for _, trial := range challenger.Trials {
		if effectiveTrialPassed(trial, challenger.Adjudications) {
			byCase[trial.CaseID].challenger++
		}
	}
	segments := make(map[string]string, len(suite.Cases))
	for _, item := range suite.Cases {
		segments[item.CaseID] = item.Segment
	}
	rows := make([]CampaignComparisonRow, 0, len(byCase))
	regressions, improvements := 0, 0
	for caseID, count := range byCase {
		baselineRate := float64(count.baseline) / float64(count.trials)
		challengerRate := float64(count.challenger) / float64(count.trials)
		delta := challengerRate - baselineRate
		row := CampaignComparisonRow{
			CaseID: caseID, Segment: segments[caseID], Trials: count.trials,
			BaselinePassRate: baselineRate, ChallengerPassRate: challengerRate,
			DeltaPassRate: delta, Regression: delta < 0,
		}
		if delta < 0 {
			regressions++
		} else if delta > 0 {
			improvements++
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].CaseID < rows[j].CaseID })
	delta := challengerAssessment.PassRate - baselineAssessment.PassRate
	margin := 1.959963984540054 * math.Sqrt(
		baselineAssessment.PassRate*(1-baselineAssessment.PassRate)/
			float64(baselineAssessment.Total)+
			challengerAssessment.PassRate*(1-challengerAssessment.PassRate)/
				float64(challengerAssessment.Total),
	)
	return CampaignComparison{
		SuiteID: suite.SuiteID, SuiteVersion: suite.Version,
		BaselineCampaignID: baseline.CampaignID,
		BaselineTemplateID: baseline.TemplateID, BaselineRelease: baseline.Release,
		ChallengerCampaignID: challenger.CampaignID,
		ChallengerTemplateID: challenger.TemplateID, ChallengerRelease: challenger.Release,
		Baseline: baselineAssessment, Challenger: challengerAssessment,
		DeltaPassRate: delta, DeltaConfidenceLow: math.Max(-1, delta-margin),
		DeltaConfidenceHigh: math.Min(1, delta+margin),
		BaselineCostUSD:     sumTrialCost(baseline.Trials),
		ChallengerCostUSD:   sumTrialCost(challenger.Trials),
		BaselineLatencyMS:   sumTrialLatency(baseline.Trials),
		ChallengerLatencyMS: sumTrialLatency(challenger.Trials),
		Regressions:         regressions, Improvements: improvements, Rows: rows,
	}, nil
}

func effectiveTrialPassed(trial TrialResult, adjudications []TrialAdjudication) bool {
	for _, adjudication := range adjudications {
		if adjudication.CaseID == trial.CaseID && adjudication.Trial == trial.Trial {
			return adjudication.Passed
		}
	}
	return trial.Passed
}

func sumTrialCost(trials []TrialResult) float64 {
	total := 0.0
	for _, trial := range trials {
		total += trial.CostUSD
		if trial.Grade != nil {
			total += trial.Grade.CostUSD
		}
	}
	return total
}

func sumTrialLatency(trials []TrialResult) int64 {
	var total int64
	for _, trial := range trials {
		total += trial.LatencyMS
		if trial.Grade != nil {
			total += trial.Grade.LatencyMS
		}
	}
	return total
}

func trialRef(caseID string, trial int) string {
	return fmt.Sprintf("%s/%d", caseID, trial)
}
