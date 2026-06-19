// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import (
	"context"
	"encoding/json"
	"fmt"
	"math"

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
func bucket(p float64) int {
	if p < 0 {
		p = 0
	}
	idx := int(p * driftBuckets)
	if idx >= driftBuckets {
		idx = driftBuckets - 1
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

// ModelStats is the materialized per-model prediction distribution: a rolling
// histogram of predicted probabilities, plus a captured baseline to measure PSI
// against. Predictions without a probability (e.g. an expression score) are counted
// but not bucketed.
type ModelStats struct {
	Org           string    `json:"org"`
	Workspace     string    `json:"workspace"`
	Name          string    `json:"name"`
	Count         int       `json:"count"`          // predictions seen with a probability
	Hist          Histogram `json:"hist"`           // current distribution
	HasBaseline   bool      `json:"has_baseline"`   // a baseline was captured
	BaselineHist  Histogram `json:"baseline_hist"`  // distribution at capture time
	BaselineCount int       `json:"baseline_count"` // predictions in the baseline
	UpdatedAt     string    `json:"updated_at"`
}

// DriftReport is the read-side drift view for one model.
type DriftReport struct {
	Model       string    `json:"model"`
	Count       int       `json:"count"`
	Hist        Histogram `json:"hist"`
	HasBaseline bool      `json:"has_baseline"`
	PSI         *float64  `json:"psi,omitempty"`
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
	default:
		return nil
	}
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
		prob, hasProb := pred["probability"].(float64)
		if model == "" || !hasProb {
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

// Drift returns the current drift report for a model (PSI vs the baseline when one
// has been captured).
func Drift(ctx context.Context, s store.Store, id identity.Identity, model string) (DriftReport, error) {
	st, ok, err := store.GetDoc[ModelStats](ctx, s, StatsCollection, store.Key(id.Org, id.Workspace, model))
	if err != nil {
		return DriftReport{}, err
	}
	rep := DriftReport{Model: model}
	if !ok {
		return rep, nil
	}
	rep.Count = st.Count
	rep.Hist = st.Hist
	rep.HasBaseline = st.HasBaseline
	if st.HasBaseline {
		if psi, ok := PSI(st.BaselineHist, st.Hist); ok {
			rep.PSI = &psi
		}
	}
	return rep, nil
}
