// SPDX-License-Identifier: AGPL-3.0-or-later

package governance

import (
	"encoding/csv"
	"encoding/json"
	"strconv"
	"strings"
)

type CampaignExport struct {
	Format          string       `json:"format"`
	Campaign        CampaignView `json:"campaign"`
	Suite           EvalSuite    `json:"suite"`
	ReleaseSpecHash string       `json:"release_spec_hash"`
}

func CampaignExportJSON(
	campaign CampaignView,
	suite EvalSuite,
	releaseSpecHash string,
) (string, error) {
	document := CampaignExport{
		Format: "intraktible.agent-evaluation/v1", Campaign: campaign,
		Suite: suite, ReleaseSpecHash: releaseSpecHash,
	}
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return "", err
	}
	return string(encoded) + "\n", nil
}

func CampaignExportCSV(campaign CampaignView, suite EvalSuite) (string, error) {
	var body strings.Builder
	writer := csv.NewWriter(&body)
	if err := writer.Write([]string{
		"campaign_id", "template_id", "release", "suite_id", "suite_version",
		"dataset_hash", "case_id", "segment", "trial", "original_status",
		"original_passed", "grader_detail", "grader_kind", "grader_score",
		"grader_passed", "grader_hash", "rubric_hash", "grader_invocation_id",
		"grader_provider", "grader_model", "grader_attempts", "grader_latency_ms",
		"grader_prompt_tokens", "grader_output_tokens", "grader_cost_usd",
		"grader_output_hash", "effective_passed", "adjudicated_by", "adjudicated_at",
		"adjudication_reason", "output_hash", "invocation_id", "provider", "model",
		"attempts", "latency_ms", "prompt_tokens", "output_tokens", "cost_usd",
		"citations", "tool_calls",
	}); err != nil {
		return "", err
	}
	segments := make(map[string]string, len(suite.Cases))
	for _, item := range suite.Cases {
		segments[item.CaseID] = item.Segment
	}
	adjudications := make(map[string]TrialAdjudication, len(campaign.Adjudications))
	for _, adjudication := range campaign.Adjudications {
		adjudications[trialRef(adjudication.CaseID, adjudication.Trial)] = adjudication
	}
	for _, trial := range campaign.Trials {
		adjudication, adjudicated := adjudications[trialRef(trial.CaseID, trial.Trial)]
		actor, at, reason := "", "", ""
		if adjudicated {
			actor = adjudication.AdjudicatedBy
			at = adjudication.AdjudicatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z")
			reason = adjudication.Reason
		}
		grade := GradeEvidence{}
		if trial.Grade != nil {
			grade = *trial.Grade
		}
		row := []string{
			campaign.CampaignID, campaign.TemplateID, strconv.Itoa(campaign.Release),
			campaign.SuiteID, strconv.Itoa(campaign.SuiteVersion), suite.DatasetHash,
			trial.CaseID, segments[trial.CaseID], strconv.Itoa(trial.Trial),
			trial.Status, strconv.FormatBool(trial.Passed), trial.Detail,
			string(grade.Kind), strconv.FormatFloat(grade.Score, 'f', 8, 64),
			strconv.FormatBool(grade.Passed), grade.GraderHash, grade.RubricHash,
			grade.InvocationID, grade.Provider, grade.Model, strconv.Itoa(grade.Attempts),
			strconv.FormatInt(grade.LatencyMS, 10), strconv.Itoa(grade.PromptTokens),
			strconv.Itoa(grade.OutputTokens), strconv.FormatFloat(grade.CostUSD, 'f', 8, 64),
			grade.OutputHash,
			strconv.FormatBool(effectiveTrialPassed(trial, campaign.Adjudications)),
			actor, at, reason, trial.OutputHash, trial.InvocationID, trial.Provider,
			trial.Model, strconv.Itoa(trial.Attempts), strconv.FormatInt(trial.LatencyMS, 10),
			strconv.Itoa(trial.PromptTokens), strconv.Itoa(trial.OutputTokens),
			strconv.FormatFloat(trial.CostUSD, 'f', 8, 64),
			strings.Join(trial.Citations, ";"), strings.Join(trial.ToolCalls, ";"),
		}
		for index := range row {
			row[index] = safeCSVCell(row[index])
		}
		if err := writer.Write(row); err != nil {
			return "", err
		}
	}
	writer.Flush()
	return body.String(), writer.Error()
}

func safeCSVCell(value string) string {
	if value == "" {
		return value
	}
	switch value[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + value
	default:
		return value
	}
}
