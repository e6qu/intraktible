// SPDX-License-Identifier: AGPL-3.0-or-later

// Package analytics is the Decision Engine's metrics read model: a projector that
// folds the decision event stream into per-flow counters (volume, outcome, and
// champion/challenger breakdown), the "analytics-lite" of PLAN.md §4.1.
package analytics

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/e6qu/intraktible/decision-engine/domain"
	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// Collection is the store collection holding flow metrics.
const Collection = "decision_metrics"

// VariantStats are the per-variant (champion/challenger) outcome counts.
type VariantStats struct {
	Started   int `json:"started"`
	Completed int `json:"completed"`
	Failed    int `json:"failed"`
}

// FlowMetrics is the materialized metrics for one flow.
type FlowMetrics struct {
	Org             string                  `json:"org"`
	Workspace       string                  `json:"workspace"`
	FlowID          string                  `json:"flow_id"`
	Total           int                     `json:"total"`
	Completed       int                     `json:"completed"`
	Failed          int                     `json:"failed"`
	TotalDurationMS int64                   `json:"total_duration_ms"`
	AvgDurationMS   int64                   `json:"avg_duration_ms"`
	ByEnvironment   map[string]int          `json:"by_environment"`
	ByVersion       map[int]int             `json:"by_version"`
	ByVariant       map[string]VariantStats `json:"by_variant"`
	// ByDisposition counts completed decisions by the policy's disposition
	// (approve|decline|refer); approve+decline over the total is the automation rate.
	ByDisposition map[string]int `json:"by_disposition"`
	UpdatedAt     time.Time      `json:"updated_at"`
}

// Projector folds decision events into FlowMetrics.
type Projector struct{}

// Name identifies the projector.
func (Projector) Name() string { return "decision_metrics" }

// Collections lists the store collection this projector owns.
func (Projector) Collections() []string { return []string{Collection} }

// Apply updates the per-flow metrics for each decision lifecycle event.
func (Projector) Apply(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	switch e.Type {
	case events.TypeDecisionStarted:
		return applyStarted(ctx, e, s)
	case events.TypeDecisionCompleted:
		return applyCompleted(ctx, e, s)
	case events.TypeDecisionFailed:
		return applyFailed(ctx, e, s)
	default:
		return nil
	}
}

func applyStarted(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p events.DecisionStarted
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("decision_metrics: decode started seq %d: %w", e.Seq, err)
	}
	return update(ctx, s, e, p.FlowID, func(m *FlowMetrics) {
		m.Total++
		m.ByEnvironment[p.Environment]++
		m.ByVersion[p.Version]++
		bump(m, p.Variant, func(v *VariantStats) { v.Started++ })
	})
}

func applyCompleted(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p events.DecisionCompleted
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("decision_metrics: decode completed seq %d: %w", e.Seq, err)
	}
	return update(ctx, s, e, p.FlowID, func(m *FlowMetrics) {
		m.Completed++
		m.TotalDurationMS += p.DurationMS
		// Round to nearest ms (Completed is ≥1 here); plain integer division
		// truncates and biases the reported average systematically low.
		m.AvgDurationMS = (m.TotalDurationMS + int64(m.Completed)/2) / int64(m.Completed)
		bump(m, p.Variant, func(v *VariantStats) { v.Completed++ })
		if p.Disposition != "" {
			m.ByDisposition[p.Disposition]++
		}
	})
}

func applyFailed(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p events.DecisionFailed
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("decision_metrics: decode failed seq %d: %w", e.Seq, err)
	}
	return update(ctx, s, e, p.FlowID, func(m *FlowMetrics) {
		m.Failed++
		bump(m, p.Variant, func(v *VariantStats) { v.Failed++ })
	})
}

// bump mutates the stats for a variant, defaulting an unset variant to champion.
func bump(m *FlowMetrics, variant string, mutate func(*VariantStats)) {
	if variant == "" {
		variant = string(domain.VariantChampion)
	}
	v := m.ByVariant[variant]
	mutate(&v)
	m.ByVariant[variant] = v
}

// Read returns the metrics for a flow (zero-valued maps when none yet).
func Read(ctx context.Context, s store.Store, id identity.Identity, flowID string) (FlowMetrics, bool, error) {
	return store.GetDoc[FlowMetrics](ctx, s, Collection, store.Key(id.Org, id.Workspace, flowID))
}

// SLOAttainment reports a flow's measured performance against its objectives.
// Measured over the flow's CUMULATIVE counts (the metrics read model is all-time,
// not windowed), so it answers "since inception" — a rolling-window SLO would need
// time-bucketed metrics, noted as a follow-up.
type SLOAttainment struct {
	Decisions int `json:"decisions"` // dispositioned volume: completed + failed
	// Availability (success rate = completed / (completed + failed)).
	SuccessRate     float64 `json:"success_rate"`
	SuccessTarget   float64 `json:"success_target"`
	SuccessMet      bool    `json:"success_met"`
	ErrorBudget     float64 `json:"error_budget"`     // allowed failure fraction = 1 - target
	BudgetRemaining float64 `json:"budget_remaining"` // 1 = full budget, <0 = over budget
	// Latency (average decision duration vs the target; LatencyTargetMS 0 = no objective).
	AvgLatencyMS    int64 `json:"avg_latency_ms"`
	LatencyTargetMS int64 `json:"latency_target_ms"`
	LatencyMet      bool  `json:"latency_met"`
}

// Attainment computes a flow's SLO attainment from its metrics against the given
// targets. successTarget is the minimum success fraction in [0,1]; latencyTargetMS
// is the max average latency (0 = no latency objective). With no decisions yet,
// objectives are reported met (nothing has breached them).
func Attainment(m FlowMetrics, successTarget float64, latencyTargetMS int64) SLOAttainment {
	a := SLOAttainment{
		Decisions: m.Completed + m.Failed, SuccessTarget: successTarget,
		AvgLatencyMS: m.AvgDurationMS, LatencyTargetMS: latencyTargetMS,
		SuccessMet: true, LatencyMet: true, BudgetRemaining: 1,
	}
	a.ErrorBudget = 1 - successTarget
	if a.Decisions > 0 {
		a.SuccessRate = float64(m.Completed) / float64(a.Decisions)
		a.SuccessMet = a.SuccessRate >= successTarget
		failureRate := float64(m.Failed) / float64(a.Decisions)
		if a.ErrorBudget > 0 {
			// Fraction of the failure budget still unspent (negative once exhausted).
			a.BudgetRemaining = (a.ErrorBudget - failureRate) / a.ErrorBudget
		} else if failureRate > 0 {
			// A 100%-success target leaves no budget; any failure is over budget.
			a.BudgetRemaining = 0
		}
	}
	if latencyTargetMS > 0 {
		a.LatencyMet = m.AvgDurationMS <= latencyTargetMS
	}
	return a
}

func update(ctx context.Context, s store.Store, e eventlog.Envelope, flowID string, mutate func(*FlowMetrics)) error {
	if flowID == "" {
		return fmt.Errorf("decision_metrics: event seq %d has no flow id", e.Seq)
	}
	key := store.Key(e.Org, e.Workspace, flowID)
	m, _, err := store.GetDoc[FlowMetrics](ctx, s, Collection, key)
	if err != nil {
		return err
	}
	m.Org, m.Workspace, m.FlowID = e.Org, e.Workspace, flowID
	if m.ByEnvironment == nil {
		m.ByEnvironment = map[string]int{}
	}
	if m.ByVersion == nil {
		m.ByVersion = map[int]int{}
	}
	if m.ByVariant == nil {
		m.ByVariant = map[string]VariantStats{}
	}
	if m.ByDisposition == nil {
		m.ByDisposition = map[string]int{}
	}
	mutate(&m)
	m.UpdatedAt = e.Time
	return store.PutDoc(ctx, s, Collection, key, m)
}
