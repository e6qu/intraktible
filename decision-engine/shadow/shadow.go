// SPDX-License-Identifier: AGPL-3.0-or-later

// Package shadow is the Decision Engine's shadow-comparison read model: a
// projector that folds ShadowEvaluated events into a per-flow, per-environment
// divergence report. A shadow version runs alongside live decisions without
// affecting them; this report answers "how often, and where, would promoting
// the shadow change the outcome?"
package shadow

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// Collection is the store collection holding shadow reports.
const Collection = "decision_shadow"

// sampleCap bounds how many diverging decision ids a report retains.
const sampleCap = 10

// ComparisonSample explains one non-matching or errored comparison without
// retaining either output's subject values. ChangedFields contains top-level
// output keys only; the linked live decision remains the value-bearing record.
type ComparisonSample struct {
	DecisionID        string   `json:"decision_id"`
	LiveStatus        string   `json:"live_status"`
	ShadowStatus      string   `json:"shadow_status,omitempty"`
	LiveDisposition   string   `json:"live_disposition,omitempty"`
	ShadowDisposition string   `json:"shadow_disposition,omitempty"`
	LiveCode          string   `json:"live_code,omitempty"`
	ShadowCode        string   `json:"shadow_code,omitempty"`
	LiveReason        string   `json:"live_reason,omitempty"`
	ShadowReason      string   `json:"shadow_reason,omitempty"`
	ChangedFields     []string `json:"changed_fields,omitempty"`
	Error             string   `json:"error,omitempty"`
}

// EnvShadow is one homogeneous comparison cohort. Changing the live version,
// candidate version, comparison basis, or exact policy selection starts a new
// cohort so the aggregate never blends unlike deployment evidence.
type EnvShadow struct {
	LiveVersion    int                     `json:"live_version"`
	ShadowVersion  int                     `json:"shadow_version"`
	MatchBasis     events.ShadowMatchBasis `json:"match_basis"`
	PolicyID       string                  `json:"policy_id,omitempty"`
	PolicyVersion  int                     `json:"policy_version,omitempty"`
	Total          int                     `json:"total"`
	Matched        int                     `json:"matched"`
	Diverged       int                     `json:"diverged"`
	Errored        int                     `json:"errored"`
	SampleDiverged []string                `json:"sample_diverged,omitempty"` // compatibility: diverging live decision ids
	Samples        []ComparisonSample      `json:"samples,omitempty"`
}

// Report is the materialized shadow comparison for one flow, by environment.
type Report struct {
	Org       string               `json:"org"`
	Workspace string               `json:"workspace"`
	FlowID    string               `json:"flow_id"`
	ByEnv     map[string]EnvShadow `json:"by_env"`
	UpdatedAt time.Time            `json:"updated_at"`
}

// Projector folds ShadowEvaluated events into a Report.
type Projector struct{}

// Name identifies the projector.
func (Projector) Name() string { return "decision_shadow" }

// Collections lists the store collection this projector owns.
func (Projector) Collections() []string { return []string{Collection} }

// Apply updates the shadow report for each shadow-evaluation event.
func (Projector) Apply(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	if e.Type != events.TypeShadowEvaluated {
		return nil
	}
	var p events.ShadowEvaluated
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("decision_shadow: decode evaluated seq %d: %w", e.Seq, err)
	}
	if p.FlowID == "" {
		return fmt.Errorf("decision_shadow: event seq %d has no flow id", e.Seq)
	}
	key := store.Key(e.Org, e.Workspace, p.FlowID)
	rep, _, err := store.GetDoc[Report](ctx, s, Collection, key)
	if err != nil {
		return err
	}
	rep.Org, rep.Workspace, rep.FlowID = e.Org, e.Workspace, p.FlowID
	if rep.ByEnv == nil {
		rep.ByEnv = map[string]EnvShadow{}
	}
	env := rep.ByEnv[p.Environment]
	// Any cohort dimension changing restarts the evidence for that environment.
	// Most importantly, deploying a new champion or approving a new policy must
	// not leave the old comparison ratio looking applicable.
	if env.LiveVersion != p.LiveVersion ||
		env.ShadowVersion != p.ShadowVersion ||
		env.MatchBasis != p.MatchBasis ||
		env.PolicyID != p.PolicyID ||
		env.PolicyVersion != p.PolicyVersion {
		env = EnvShadow{
			LiveVersion: p.LiveVersion, ShadowVersion: p.ShadowVersion,
			MatchBasis: p.MatchBasis, PolicyID: p.PolicyID, PolicyVersion: p.PolicyVersion,
		}
	}
	env.Total++
	switch {
	case p.ShadowError != "":
		env.Errored++
		env.addSample(p)
	case p.Matched:
		env.Matched++
	default:
		env.Diverged++
		if len(env.SampleDiverged) < sampleCap {
			env.SampleDiverged = append(env.SampleDiverged, p.DecisionID)
		}
		env.addSample(p)
	}
	rep.ByEnv[p.Environment] = env
	rep.UpdatedAt = e.Time
	return store.PutDoc(ctx, s, Collection, key, rep)
}

func (s *EnvShadow) addSample(p events.ShadowEvaluated) {
	if len(s.Samples) >= sampleCap {
		return
	}
	s.Samples = append(s.Samples, ComparisonSample{
		DecisionID: p.DecisionID,
		LiveStatus: p.LiveStatus, ShadowStatus: p.ShadowStatus,
		LiveDisposition: p.LiveDisposition, ShadowDisposition: p.ShadowDisposition,
		LiveCode: p.LiveCode, ShadowCode: p.ShadowCode,
		LiveReason: p.LiveReason, ShadowReason: p.ShadowReason,
		ChangedFields: p.ChangedFields, Error: p.ShadowError,
	})
}

// Read returns the shadow report for a flow (false when none yet).
func Read(ctx context.Context, s store.Store, id identity.Identity, flowID string) (Report, bool, error) {
	return store.GetDoc[Report](ctx, s, Collection, store.Key(id.Org, id.Workspace, flowID))
}
