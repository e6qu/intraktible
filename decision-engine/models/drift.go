// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"

	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// driftBuckets is the number of probability deciles tracked for drift.
const driftBuckets = 10

// StatsCollection holds per-model prediction-distribution stats for drift.
const StatsCollection = "decision_model_stats"

// Histogram counts predicted probabilities by decile ([0,0.1), …, [0.9,1.0]).
type Histogram [driftBuckets]int

// bucket maps a probability to its decile index, clamped to [0, driftBuckets-1].
// It is defensive against a NaN/±Inf or out-of-range value (int(NaN) is a large
// negative on amd64/arm64): the result is always a valid index, so a malformed
// probability from an external model can never panic the projector with an
// out-of-range Hist[idx] write.
func bucket(p float64) int {
	if math.IsNaN(p) || p <= 0 {
		return 0
	}
	idx := int(p * driftBuckets)
	if idx >= driftBuckets {
		return driftBuckets - 1
	}
	if idx < 0 {
		return 0
	}
	return idx
}

func (h Histogram) total() int {
	n := 0
	for _, c := range h {
		n += c
	}
	return n
}

// PSI is the Population Stability Index of cur against base over the deciles — the
// standard model-drift metric. It returns ok=false when either side is empty (drift
// is undefined). A small epsilon floors each share so an empty bucket can't blow up
// the log. Rule of thumb: <0.1 stable, 0.1–0.25 moderate shift, >0.25 significant.
func PSI(base, cur Histogram) (float64, bool) {
	bt, ct := base.total(), cur.total()
	if bt == 0 || ct == 0 {
		return 0, false
	}
	const eps = 1e-4
	psi := 0.0
	for i := 0; i < driftBuckets; i++ {
		b := math.Max(float64(base[i])/float64(bt), eps)
		c := math.Max(float64(cur[i])/float64(ct), eps)
		psi += (c - b) * math.Log(c/b)
	}
	return psi, true
}

// maxDailyDays caps how many per-day histograms are retained for windowing.
const maxDailyDays = 90

// ModelStats is the materialized per-model prediction distribution: a cumulative
// histogram of predicted probabilities plus per-day histograms (for windowed drift),
// a captured baseline to measure PSI against, and an optional alert threshold.
// Predictions without a probability (e.g. an expression score) are counted but not
// bucketed.
type ModelStats struct {
	Org           string               `json:"org"`
	Workspace     string               `json:"workspace"`
	Name          string               `json:"name"`
	Count         int                  `json:"count"`               // predictions seen with a probability
	Hist          Histogram            `json:"hist"`                // cumulative distribution
	Daily         map[string]Histogram `json:"daily,omitempty"`     // day (YYYY-MM-DD) -> histogram
	HasBaseline   bool                 `json:"has_baseline"`        // a baseline was captured
	BaselineHist  Histogram            `json:"baseline_hist"`       // distribution at capture time
	BaselineCount int                  `json:"baseline_count"`      // predictions in the baseline
	Threshold     float64              `json:"threshold,omitempty"` // PSI alert threshold (0 = none)
	Alerting      bool                 `json:"alerting"`            // last-known firing state (scheduler dedup)
	UpdatedAt     string               `json:"updated_at"`
}

// Firing reports whether the model's PSI over the given window exceeds its
// configured threshold. windowDays > 0 measures the most recent day-buckets; 0
// uses the cumulative histogram. ok is false when drift is undefined (no
// baseline, no threshold, or an empty side) — the scheduler treats that as not
// firing. It shares the windowing + PSI math with Drift so the read endpoint and
// the scheduler never diverge.
func (st ModelStats) Firing(windowDays int) (psi float64, firing, ok bool) {
	if !st.HasBaseline || st.Threshold <= 0 {
		return 0, false, false
	}
	psi, ok = PSI(st.BaselineHist, st.histFor(windowDays))
	if !ok {
		return 0, false, false
	}
	return psi, psi > st.Threshold, true
}

// histFor returns the histogram to measure drift over: a recent window (latest
// windowDays day-buckets) when windowDays > 0, else the all-time cumulative one.
func (st ModelStats) histFor(windowDays int) Histogram {
	if windowDays > 0 {
		return windowHist(st.Daily, windowDays)
	}
	return st.Hist
}

// DriftReport is the read-side drift view for one model.
type DriftReport struct {
	Model       string    `json:"model"`
	Count       int       `json:"count"`       // predictions in the reported window
	Hist        Histogram `json:"hist"`        // distribution over the window
	WindowDays  int       `json:"window_days"` // 0 = all-time (cumulative)
	HasBaseline bool      `json:"has_baseline"`
	PSI         *float64  `json:"psi,omitempty"`
	Threshold   float64   `json:"threshold,omitempty"`
	Firing      bool      `json:"firing"`   // PSI exceeds the (set) threshold, computed live
	Alerting    bool      `json:"alerting"` // the drift scheduler has pushed an alert (firing edge crossed)
}

// DriftProjector folds Predict-node outputs into per-model probability histograms
// and snapshots a baseline on demand. Its counters are non-idempotent, so it relies
// on the projection runtime's exactly-once checkpointing.
type DriftProjector struct{}

func (DriftProjector) Name() string          { return "decision_model_stats" }
func (DriftProjector) Collections() []string { return []string{StatsCollection} }

func (DriftProjector) Apply(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	switch e.Type {
	case events.TypeNodeEvaluated:
		return applyPredictNode(ctx, e, s)
	case events.TypeModelBaselineCaptured:
		return applyBaseline(ctx, e, s)
	case events.TypeModelMonitorSet:
		return applyMonitorSet(ctx, e, s)
	case events.TypeModelDriftAlerted:
		return applyDriftAlerting(ctx, e, s, true)
	case events.TypeModelDriftResolved:
		return applyDriftAlerting(ctx, e, s, false)
	default:
		return nil
	}
}

// applyDriftAlerting flips a model's last-known firing state (scheduler dedup).
// Alerted and Resolved share the leading {name} field, so one decoder serves both.
func applyDriftAlerting(ctx context.Context, e eventlog.Envelope, s store.Store, alerting bool) error {
	var p events.ModelDriftResolved
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("models: decode drift alert seq %d: %w", e.Seq, err)
	}
	_, err := store.UpdateDoc(ctx, s, StatsCollection, store.Key(e.Org, e.Workspace, p.Name), func(st *ModelStats) {
		st.Alerting = alerting
	})
	return err // an alert/resolve for an unseen model is a no-op
}

// applyPredictNode bumps the histogram for each model named in a Predict node's
// output ({<output>:{score,probability,model}}). Non-predict nodes are ignored.
func applyPredictNode(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p events.NodeEvaluated
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("models: decode node_evaluated seq %d: %w", e.Seq, err)
	}
	if p.NodeType != events.NodePredict || len(p.Output) == 0 {
		return nil
	}
	// The predict output is a map of output-key to a prediction object. Best-effort:
	// an output we can't read that way simply yields no buckets (it must not fail the
	// whole projection stream — drift is observability, not the system of record).
	out := map[string]map[string]any{}
	_ = json.Unmarshal(p.Output, &out)
	for _, pred := range out {
		model, _ := pred["model"].(string)
		// Read via toFloat (handles json.Number) rather than a bare float64 assertion,
		// so a probability decoded under UseNumber is not silently dropped.
		prob, hasProb := toFloat(pred["probability"])
		// Skip a non-finite probability (a misbehaving external model can return
		// NaN/±Inf) — it isn't a meaningful decile and must not pollute the histogram.
		if model == "" || !hasProb || math.IsNaN(prob) || math.IsInf(prob, 0) {
			continue
		}
		if err := bumpModel(ctx, e, s, model, bucket(prob)); err != nil {
			return err
		}
	}
	return nil
}

func bumpModel(ctx context.Context, e eventlog.Envelope, s store.Store, model string, idx int) error {
	key := store.Key(e.Org, e.Workspace, model)
	st, ok, err := store.GetDoc[ModelStats](ctx, s, StatsCollection, key)
	if err != nil {
		return err
	}
	if !ok {
		st = ModelStats{Org: e.Org, Workspace: e.Workspace, Name: model}
	}
	st.Hist[idx]++
	st.Count++
	day := e.Time.UTC().Format("2006-01-02")
	if st.Daily == nil {
		st.Daily = map[string]Histogram{}
	}
	d := st.Daily[day]
	d[idx]++
	st.Daily[day] = d
	pruneDaily(st.Daily)
	st.UpdatedAt = e.Time.UTC().Format("2006-01-02T15:04:05Z07:00")
	return store.PutDoc(ctx, s, StatsCollection, key, st)
}

// pruneDaily keeps only the most recent maxDailyDays day-buckets (keys are
// lexicographically sortable dates), bounding the doc size on a long-lived model.
func pruneDaily(daily map[string]Histogram) {
	if len(daily) <= maxDailyDays {
		return
	}
	days := make([]string, 0, len(daily))
	for d := range daily {
		days = append(days, d)
	}
	sort.Strings(days)
	for _, d := range days[:len(days)-maxDailyDays] {
		delete(daily, d)
	}
}

func applyMonitorSet(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p events.ModelMonitorSet
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("models: decode monitor seq %d: %w", e.Seq, err)
	}
	// The model may have no predictions yet, so seed the doc if absent.
	key := store.Key(e.Org, e.Workspace, p.Name)
	st, ok, err := store.GetDoc[ModelStats](ctx, s, StatsCollection, key)
	if err != nil {
		return err
	}
	if !ok {
		st = ModelStats{Org: e.Org, Workspace: e.Workspace, Name: p.Name}
	}
	st.Threshold = p.Threshold
	st.UpdatedAt = e.Time.UTC().Format("2006-01-02T15:04:05Z07:00")
	return store.PutDoc(ctx, s, StatsCollection, key, st)
}

func applyBaseline(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p events.ModelBaselineCaptured
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("models: decode baseline seq %d: %w", e.Seq, err)
	}
	_, err := store.UpdateDoc(ctx, s, StatsCollection, store.Key(e.Org, e.Workspace, p.Name), func(st *ModelStats) {
		st.BaselineHist = st.Hist
		st.BaselineCount = st.Count
		st.HasBaseline = true
		st.UpdatedAt = e.Time.UTC().Format("2006-01-02T15:04:05Z07:00")
	})
	return err
}

// Drift returns the drift report for a model. windowDays > 0 measures only the most
// recent windowDays day-buckets (a windowed view that surfaces a recent shift the
// cumulative view would dilute); 0 uses the all-time cumulative distribution. PSI is
// computed against the captured baseline, and firing is set when it exceeds a
// configured threshold.
func Drift(ctx context.Context, s store.Store, id identity.Identity, model string, windowDays int) (DriftReport, error) {
	st, ok, err := store.GetDoc[ModelStats](ctx, s, StatsCollection, store.Key(id.Org, id.Workspace, model))
	if err != nil {
		return DriftReport{}, err
	}
	rep := DriftReport{Model: model, WindowDays: windowDays}
	if !ok {
		return rep, nil
	}
	hist := st.histFor(windowDays)
	rep.Hist = hist
	rep.Count = hist.total()
	rep.HasBaseline = st.HasBaseline
	rep.Threshold = st.Threshold
	rep.Alerting = st.Alerting
	if st.HasBaseline {
		// PSI is reported whenever a baseline exists (so drift is visible before a
		// threshold is set); firing additionally requires the threshold to be crossed.
		if psi, ok := PSI(st.BaselineHist, hist); ok {
			rep.PSI = &psi
			rep.Firing = st.Threshold > 0 && psi > st.Threshold
		}
	}
	return rep, nil
}

// windowHist sums the most recent windowDays day-buckets (by date, latest first).
func windowHist(daily map[string]Histogram, windowDays int) Histogram {
	days := make([]string, 0, len(daily))
	for d := range daily {
		days = append(days, d)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(days)))
	if len(days) > windowDays {
		days = days[:windowDays]
	}
	var sum Histogram
	for _, d := range days {
		h := daily[d]
		for i := range sum {
			sum[i] += h[i]
		}
	}
	return sum
}
