// SPDX-License-Identifier: AGPL-3.0-or-later

// Package analytics is the Decision Engine's metrics read model: a projector that
// folds the decision event stream into per-flow counters (volume, outcome, and
// champion/challenger breakdown), the "analytics-lite" of PLAN.md §4.1.
package analytics

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
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
	// Daily holds per-UTC-day dispositioned outcomes so SLO attainment can be measured
	// over a rolling window, not only all-time. Bounded to the most recent
	// maxDailyBuckets days (pruned relative to the newest day seen, so it replays
	// identically). Sorted ascending by date.
	Daily     []DailyBucket `json:"daily,omitempty"`
	UpdatedAt time.Time     `json:"updated_at"`
}

// DailyBucket is one UTC day of a flow's dispositioned outcomes.
type DailyBucket struct {
	Date            string `json:"date"` // YYYY-MM-DD, UTC
	Completed       int    `json:"completed"`
	Failed          int    `json:"failed"`
	TotalDurationMS int64  `json:"total_duration_ms"`
}

// maxDailyBuckets bounds the retained daily history — also the maximum rolling SLO
// window. Pruning is relative to the newest bucket, not the wall clock, so a replay
// of the same event log yields byte-identical metrics.
const maxDailyBuckets = events.MaxSLOWindowDays

// dayKey is the UTC calendar day an event falls in, the daily-bucket key.
func dayKey(t time.Time) string { return t.UTC().Format("2006-01-02") }

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
		bumpDay(m, dayKey(e.Time), func(b *DailyBucket) {
			b.Completed++
			b.TotalDurationMS += p.DurationMS
		})
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
		bumpDay(m, dayKey(e.Time), func(b *DailyBucket) { b.Failed++ })
	})
}

// bumpDay mutates the bucket for the given UTC day, creating it (kept date-sorted) and
// pruning to the most recent maxDailyBuckets days.
func bumpDay(m *FlowMetrics, day string, mutate func(*DailyBucket)) {
	for i := range m.Daily {
		if m.Daily[i].Date == day {
			mutate(&m.Daily[i])
			return
		}
	}
	b := DailyBucket{Date: day}
	mutate(&b)
	m.Daily = append(m.Daily, b)
	sort.Slice(m.Daily, func(i, j int) bool { return m.Daily[i].Date < m.Daily[j].Date })
	if len(m.Daily) > maxDailyBuckets {
		m.Daily = m.Daily[len(m.Daily)-maxDailyBuckets:]
	}
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

// SLOAttainment reports a flow's measured performance against its objectives, over
// either the flow's all-time counts (WindowDays 0) or a rolling window of the most
// recent WindowDays (so a long-lived flow's recent breach isn't diluted by its
// lifetime history). See AttainmentWindow.
type SLOAttainment struct {
	// WindowDays is the rolling window the attainment was measured over (0 = all-time).
	WindowDays int `json:"window_days"`
	Decisions  int `json:"decisions"` // dispositioned volume: completed + failed
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

// AttainmentWindow computes attainment over the last windowDays UTC days ending at
// now; windowDays <= 0 (or unset) falls back to the all-time Attainment. The window is
// capped at the retained history (maxDailyBuckets). Summing the daily buckets in range
// keeps a recent breach from being averaged away by a flow's whole lifetime.
func AttainmentWindow(m FlowMetrics, successTarget float64, latencyTargetMS int64, now time.Time, windowDays int) SLOAttainment {
	if windowDays <= 0 {
		return Attainment(m, successTarget, latencyTargetMS)
	}
	if windowDays > maxDailyBuckets {
		windowDays = maxDailyBuckets
	}
	// Inclusive lower bound: today (UTC) and the prior windowDays-1 days.
	cutoff := dayKey(now.AddDate(0, 0, -(windowDays - 1)))
	var wm FlowMetrics
	for _, b := range m.Daily {
		if b.Date >= cutoff {
			wm.Completed += b.Completed
			wm.Failed += b.Failed
			wm.TotalDurationMS += b.TotalDurationMS
		}
	}
	if wm.Completed > 0 {
		wm.AvgDurationMS = (wm.TotalDurationMS + int64(wm.Completed)/2) / int64(wm.Completed)
	}
	a := Attainment(wm, successTarget, latencyTargetMS)
	a.WindowDays = windowDays
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
