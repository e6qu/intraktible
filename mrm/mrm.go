// SPDX-License-Identifier: AGPL-3.0-or-later

// Package mrm packages the platform's model-risk-management artifact (SR 11-7 /
// PRA SS1/23): a single, exportable report that inventories every "model" — a
// decision flow, a predictive model, or an AI agent — alongside its validation
// evidence (assertions / eval cases / baseline / shadow divergence) and ongoing
// monitoring (decision volume, success rate, firing monitors, drift, SLO), and
// flags the governance gaps (an unvalidated or alerting model). It is a read-only
// aggregation over the existing read models — no new events, no I/O beyond running
// the pure assertion suite — so it always reflects the live state.
package mrm

import (
	"context"
	"fmt"
	"time"

	"github.com/e6qu/intraktible/agent-manager/agents"
	"github.com/e6qu/intraktible/agent-manager/eval"
	"github.com/e6qu/intraktible/decision-engine/analytics"
	"github.com/e6qu/intraktible/decision-engine/assertions"
	"github.com/e6qu/intraktible/decision-engine/flows"
	"github.com/e6qu/intraktible/decision-engine/models"
	"github.com/e6qu/intraktible/decision-engine/monitor"
	"github.com/e6qu/intraktible/decision-engine/shadow"
	"github.com/e6qu/intraktible/fairlending"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// Kind classifies an inventoried model.
type Kind string

const (
	KindFlow       Kind = "flow"             // a decision flow (versioned decision logic)
	KindPredictive Kind = "predictive_model" // a registered predictive model
	KindAgent      Kind = "agent"            // an AI agent (versioned prompt/model)
)

// Coverage summarizes a model's validation posture.
type Coverage string

const (
	CoverageTested  Coverage = "tested"  // validation evidence exists and passes
	CoverageFailing Coverage = "failing" // validation exists but is failing
	CoverageNone    Coverage = "none"    // no validation evidence
)

// Validation is the evidence a model has been tested.
type Validation struct {
	Coverage         Coverage `json:"coverage"`
	HasAssertions    bool     `json:"has_assertions,omitempty"`
	AssertionsTotal  int      `json:"assertions_total,omitempty"`
	AssertionsPassed int      `json:"assertions_passed,omitempty"`
	HasEvalCases     bool     `json:"has_eval_cases,omitempty"`
	EvalCases        int      `json:"eval_cases,omitempty"`
	HasBaseline      bool     `json:"has_baseline,omitempty"` // predictive-model drift reference captured
	ShadowDiverged   int      `json:"shadow_diverged,omitempty"`
}

// Monitoring is a model's live operational health.
type Monitoring struct {
	Decisions      int      `json:"decisions"`
	SuccessRate    float64  `json:"success_rate"`
	FiringMonitors []string `json:"firing_monitors,omitempty"`
	DriftPSI       *float64 `json:"drift_psi,omitempty"`
	DriftFiring    bool     `json:"drift_firing,omitempty"`
	SLOMet         *bool    `json:"slo_met,omitempty"`
}

// Model is one inventory entry.
type Model struct {
	Kind        Kind           `json:"kind"`
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Version     int            `json:"version"`
	Owner       string         `json:"owner,omitempty"` // last publisher/definer (model-owner proxy)
	Deployments map[string]int `json:"deployments,omitempty"`
	Validation  Validation     `json:"validation"`
	Monitoring  Monitoring     `json:"monitoring"`
	Issues      []string       `json:"issues,omitempty"` // governance gaps surfaced for review
	UpdatedAt   time.Time      `json:"updated_at"`
}

// Summary is the inventory roll-up.
type Summary struct {
	Total       int          `json:"total"`
	ByKind      map[Kind]int `json:"by_kind"`
	Deployed    int          `json:"deployed"`    // flows live in any environment
	Unvalidated int          `json:"unvalidated"` // models with no validation evidence
	WithIssues  int          `json:"with_issues"` // models with at least one flagged gap
}

// Report is the full MRM artifact.
type Report struct {
	GeneratedAt time.Time `json:"generated_at"`
	Org         string    `json:"org"`
	Workspace   string    `json:"workspace"`
	Summary     Summary   `json:"summary"`
	Models      []Model   `json:"models"`
}

// Build aggregates the live read models into an MRM report. now is injected so the
// generated-at stamp is deterministic in tests.
func Build(ctx context.Context, s store.Store, id identity.Identity, now time.Time) (Report, error) {
	rep := Report{GeneratedAt: now, Org: id.Org, Workspace: id.Workspace}
	rep.Summary.ByKind = map[Kind]int{}

	if err := buildFlows(ctx, s, id, &rep); err != nil {
		return Report{}, err
	}
	if err := buildModels(ctx, s, id, &rep); err != nil {
		return Report{}, err
	}
	if err := buildAgents(ctx, s, id, &rep); err != nil {
		return Report{}, err
	}

	for _, m := range rep.Models {
		rep.Summary.Total++
		rep.Summary.ByKind[m.Kind]++
		if len(m.Deployments) > 0 {
			rep.Summary.Deployed++
		}
		if m.Validation.Coverage == CoverageNone {
			rep.Summary.Unvalidated++
		}
		if len(m.Issues) > 0 {
			rep.Summary.WithIssues++
		}
	}
	return rep, nil
}

func buildFlows(ctx context.Context, s store.Store, id identity.Identity, rep *Report) error {
	fvs, err := flows.List(ctx, s, id)
	if err != nil {
		return err
	}
	for _, fv := range fvs {
		m := Model{
			Kind: KindFlow, ID: fv.FlowID, Name: fv.Name, Version: fv.Latest, UpdatedAt: fv.UpdatedAt,
		}
		if n := len(fv.Versions); n > 0 {
			m.Owner = fv.Versions[n-1].PublishedBy
		}
		if len(fv.Deployments) > 0 {
			m.Deployments = map[string]int{}
			for env, d := range fv.Deployments {
				m.Deployments[env] = d.Version
			}
		}
		m.Validation = flowValidation(ctx, s, id, fv)
		m.Monitoring = flowMonitoring(ctx, s, id, fv)
		m.Issues = flowIssues(fv, m)
		if issue := fairLendingIssue(ctx, s, id, fv.FlowID, rep.GeneratedAt); issue != "" {
			m.Issues = append(m.Issues, issue)
		}
		rep.Models = append(rep.Models, m)
	}
	return nil
}

func flowValidation(ctx context.Context, s store.Store, id identity.Identity, fv flows.FlowView) Validation {
	var v Validation
	if av, ok, _ := assertions.Read(ctx, s, id, fv.FlowID); ok && len(av.Cases) > 0 {
		v.HasAssertions = true
		if rep, err := assertions.RunForFlow(ctx, s, id, fv.FlowID, fv.Latest); err == nil {
			v.AssertionsTotal, v.AssertionsPassed = rep.Total, rep.Passed
		}
	}
	if sr, ok, _ := shadow.Read(ctx, s, id, fv.FlowID); ok {
		for _, env := range sr.ByEnv {
			v.ShadowDiverged += env.Diverged
		}
	}
	v.Coverage = coverage(v.HasAssertions, v.AssertionsTotal, v.AssertionsPassed)
	return v
}

func flowMonitoring(ctx context.Context, s store.Store, id identity.Identity, fv flows.FlowView) Monitoring {
	var mon Monitoring
	if m, ok, _ := analytics.Read(ctx, s, id, fv.FlowID); ok {
		resolved := m.Completed + m.Failed
		mon.Decisions = resolved
		if resolved > 0 {
			mon.SuccessRate = float64(m.Completed) / float64(resolved)
		}
		if fv.SLO != nil {
			a := analytics.Attainment(m, fv.SLO.SuccessTarget, fv.SLO.LatencyTargetMS)
			met := a.SuccessMet && a.LatencyMet
			mon.SLOMet = &met
		}
	}
	snap, err := monitor.LoadSnapshot(ctx, s, id, fv.FlowID)
	if err == nil {
		if rules, err := monitor.ListByFlow(ctx, s, id, fv.FlowID); err == nil {
			for _, r := range rules {
				if monitor.Evaluate(snap, r.Rule()).Firing {
					mon.FiringMonitors = append(mon.FiringMonitors, r.Metric)
				}
			}
		}
		if d := monitor.ComputeDrift(snap); d.HasBaseline && d.HasCurrent {
			psi := d.PSI
			mon.DriftPSI = &psi
		}
	}
	return mon
}

func flowIssues(fv flows.FlowView, m Model) []string {
	var issues []string
	if fv.Latest == 0 {
		issues = append(issues, "no published version")
	}
	if !m.Validation.HasAssertions {
		issues = append(issues, "no validation assertions defined")
	} else if m.Validation.AssertionsTotal > 0 && m.Validation.AssertionsPassed < m.Validation.AssertionsTotal {
		issues = append(issues, "assertions failing")
	}
	if len(m.Monitoring.FiringMonitors) > 0 {
		issues = append(issues, "monitor firing")
	}
	if m.Monitoring.SLOMet != nil && !*m.Monitoring.SLOMet {
		issues = append(issues, "SLO breaching")
	}
	if m.Validation.ShadowDiverged > 0 {
		issues = append(issues, "shadow version diverging")
	}
	return issues
}

// fairLendingIssue runs the flow's disparate-impact screen when a fair-lending
// config is set and reports a governance gap if the four-fifths threshold fails. A
// flow with no config is silent (nothing to check); a build error is silent here so
// the whole MRM report does not fail on one flow's screen — the dedicated
// /fairlending surface is where the error would show.
func fairLendingIssue(ctx context.Context, s store.Store, id identity.Identity, flowID string, now time.Time) string {
	cfg, found, err := fairlending.ReadConfig(ctx, s, id, flowID)
	if err != nil || !found {
		return ""
	}
	report, err := fairlending.Build(ctx, s, id, fairlending.Params{
		FlowID: flowID, Attribute: cfg.Attribute, Favorable: cfg.Favorable, Threshold: cfg.Threshold,
	}, now)
	if err != nil {
		return ""
	}
	if report.Groups2Plus && !report.Passes {
		return fmt.Sprintf("fair-lending AIR %.2f below %.2f for %q", report.MinAIR, report.Threshold, cfg.Attribute)
	}
	return ""
}

func buildModels(ctx context.Context, s store.Store, id identity.Identity, rep *Report) error {
	mvs, err := models.List(ctx, s, id)
	if err != nil {
		return err
	}
	for _, mv := range mvs {
		m := Model{Kind: KindPredictive, ID: mv.Name, Name: mv.Name, Owner: mv.Owner, Version: mv.Version}
		if t, err := time.Parse(time.RFC3339, mv.UpdatedAt); err == nil {
			m.UpdatedAt = t
		}
		// Four-eyes governance parity with flows: an unapproved model version is a
		// governance gap (it cannot serve production decisions), and a model with no
		// validation evidence is another — both surface like any other MRM issue.
		if !mv.Approved() {
			if mv.Pending != nil {
				m.Issues = append(m.Issues, "model approval pending review")
			} else {
				m.Issues = append(m.Issues, "model version not approved (four-eyes)")
			}
		}
		if len(mv.Validations) == 0 {
			m.Issues = append(m.Issues, "no validation evidence recorded")
		}
		d, err := models.Drift(ctx, s, id, mv.Name, 0)
		if err == nil {
			m.Validation.HasBaseline = d.HasBaseline
			m.Monitoring.DriftPSI = d.PSI
			m.Monitoring.DriftFiring = d.Firing
		}
		// A predictive model's validation reference is its captured baseline.
		if m.Validation.HasBaseline {
			m.Validation.Coverage = CoverageTested
		} else {
			m.Validation.Coverage = CoverageNone
			m.Issues = append(m.Issues, "no drift baseline captured")
		}
		if m.Monitoring.DriftFiring {
			m.Issues = append(m.Issues, "drift alert (PSI over threshold)")
		}
		rep.Models = append(rep.Models, m)
	}
	return nil
}

func buildAgents(ctx context.Context, s store.Store, id identity.Identity, rep *Report) error {
	avs, err := agents.List(ctx, s, id)
	if err != nil {
		return err
	}
	for _, av := range avs {
		m := Model{Kind: KindAgent, ID: av.Name, Name: av.Name, Version: av.Latest, UpdatedAt: av.UpdatedAt}
		if n := len(av.Versions); n > 0 {
			m.Owner = av.Versions[n-1].PublishedBy
		}
		m.Monitoring.Decisions = av.Runs
		// Success is completed over terminal runs — an agent whose runs all
		// completed must not read as 0% in the inventory.
		runs, err := agents.ListRuns(ctx, s, id, av.Name)
		if err != nil {
			return err
		}
		sum := agents.SummarizeRuns(runs)
		if terminal := sum.Completed + sum.Failed; terminal > 0 {
			m.Monitoring.SuccessRate = float64(sum.Completed) / float64(terminal)
		}
		if ev, ok, _ := eval.Read(ctx, s, id, av.Name); ok && len(ev.Cases) > 0 {
			m.Validation.HasEvalCases = true
			m.Validation.EvalCases = len(ev.Cases)
			m.Validation.Coverage = CoverageTested
		} else {
			m.Validation.Coverage = CoverageNone
			m.Issues = append(m.Issues, "no eval cases defined")
		}
		rep.Models = append(rep.Models, m)
	}
	return nil
}

// coverage classifies validation posture from the assertion outcome.
func coverage(has bool, total, passed int) Coverage {
	if !has {
		return CoverageNone
	}
	if total > 0 && passed < total {
		return CoverageFailing
	}
	return CoverageTested
}
