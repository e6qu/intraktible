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
	"strconv"
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
	ExperimentID      string   `json:"experiment_id,omitempty"`
	ExperimentCohort  int      `json:"experiment_cohort,omitempty"`
	ExperimentArm     string   `json:"experiment_arm,omitempty"`
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
	Environment      string                  `json:"environment"`
	ExperimentID     string                  `json:"experiment_id,omitempty"`
	ExperimentCohort int                     `json:"experiment_cohort,omitempty"`
	ExperimentArm    string                  `json:"experiment_arm,omitempty"`
	LiveVersion      int                     `json:"live_version"`
	ShadowVersion    int                     `json:"shadow_version"`
	MatchBasis       events.ShadowMatchBasis `json:"match_basis"`
	PolicyID         string                  `json:"policy_id,omitempty"`
	PolicyVersion    int                     `json:"policy_version,omitempty"`
	Total            int                     `json:"total"`
	Matched          int                     `json:"matched"`
	Diverged         int                     `json:"diverged"`
	Errored          int                     `json:"errored"`
	SampleDiverged   []string                `json:"sample_diverged,omitempty"` // compatibility: diverging live decision ids
	Samples          []ComparisonSample      `json:"samples,omitempty"`
}

// Report is the materialized shadow comparison for one flow, by environment.
type Report struct {
	Org       string               `json:"org"`
	Workspace string               `json:"workspace"`
	FlowID    string               `json:"flow_id"`
	ByEnv     map[string]EnvShadow `json:"by_env"`
	Cohorts   map[string]EnvShadow `json:"cohorts,omitempty"`
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
	docKey := store.Key(e.Org, e.Workspace, p.FlowID)
	rep, _, err := store.GetDoc[Report](ctx, s, Collection, docKey)
	if err != nil {
		return err
	}
	rep.Org, rep.Workspace, rep.FlowID = e.Org, e.Workspace, p.FlowID
	if rep.ByEnv == nil {
		rep.ByEnv = map[string]EnvShadow{}
	}
	if rep.Cohorts == nil {
		rep.Cohorts = map[string]EnvShadow{}
		// Upgrade a live projection created before exact shadow-cohort keys existed.
		// A full replay produces the same keys directly.
		for environment, prior := range rep.ByEnv {
			prior.Environment = environment
			rep.Cohorts[cohortKey(prior)] = prior
		}
	}
	next := EnvShadow{
		Environment:  p.Environment,
		ExperimentID: p.ExperimentID, ExperimentCohort: p.ExperimentCohort,
		ExperimentArm: p.ExperimentArm,
		LiveVersion:   p.LiveVersion, ShadowVersion: p.ShadowVersion,
		MatchBasis: p.MatchBasis, PolicyID: p.PolicyID, PolicyVersion: p.PolicyVersion,
	}
	cohortID := cohortKey(next)
	cohort := rep.Cohorts[cohortID]
	if cohort.Environment == "" {
		cohort = next
	}
	cohort.Total++
	switch {
	case p.ShadowError != "":
		cohort.Errored++
		cohort.addSample(p)
	case p.Matched:
		cohort.Matched++
	default:
		cohort.Diverged++
		if len(cohort.SampleDiverged) < sampleCap {
			cohort.SampleDiverged = append(cohort.SampleDiverged, p.DecisionID)
		}
		cohort.addSample(p)
	}
	rep.Cohorts[cohortID] = cohort
	// ByEnv remains the most recently observed exact cohort for compatibility.
	// Cohorts retains all distinct evidence instead of blending or discarding it.
	rep.ByEnv[p.Environment] = cohort
	rep.UpdatedAt = e.Time
	return store.PutDoc(ctx, s, Collection, docKey, rep)
}

func cohortKey(cohort EnvShadow) string {
	return cohort.Environment + "\x00" +
		strconv.Itoa(cohort.LiveVersion) + "\x00" +
		strconv.Itoa(cohort.ShadowVersion) + "\x00" +
		string(cohort.MatchBasis) + "\x00" +
		cohort.PolicyID + "\x00" +
		strconv.Itoa(cohort.PolicyVersion) + "\x00" +
		cohort.ExperimentID + "\x00" +
		strconv.Itoa(cohort.ExperimentCohort) + "\x00" +
		cohort.ExperimentArm
}

func (s *EnvShadow) addSample(p events.ShadowEvaluated) {
	if len(s.Samples) >= sampleCap {
		return
	}
	s.Samples = append(s.Samples, ComparisonSample{
		DecisionID:   p.DecisionID,
		ExperimentID: p.ExperimentID, ExperimentCohort: p.ExperimentCohort,
		ExperimentArm: p.ExperimentArm,
		LiveStatus:    p.LiveStatus, ShadowStatus: p.ShadowStatus,
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
