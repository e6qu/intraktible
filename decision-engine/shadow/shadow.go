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

// EnvShadow is the comparison summary for one environment's shadow version.
type EnvShadow struct {
	ShadowVersion  int      `json:"shadow_version"`
	Total          int      `json:"total"`
	Matched        int      `json:"matched"`
	Diverged       int      `json:"diverged"`
	Errored        int      `json:"errored"`
	SampleDiverged []string `json:"sample_diverged,omitempty"` // live decision ids
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
	// A new shadow target restarts the comparison for that environment.
	if env.ShadowVersion != p.ShadowVersion {
		env = EnvShadow{ShadowVersion: p.ShadowVersion}
	}
	env.Total++
	switch {
	case p.ShadowError != "":
		env.Errored++
	case p.Matched:
		env.Matched++
	default:
		env.Diverged++
		if len(env.SampleDiverged) < sampleCap {
			env.SampleDiverged = append(env.SampleDiverged, p.DecisionID)
		}
	}
	rep.ByEnv[p.Environment] = env
	rep.UpdatedAt = e.Time
	return store.PutDoc(ctx, s, Collection, key, rep)
}

// Read returns the shadow report for a flow (false when none yet).
func Read(ctx context.Context, s store.Store, id identity.Identity, flowID string) (Report, bool, error) {
	return store.GetDoc[Report](ctx, s, Collection, store.Key(id.Org, id.Workspace, flowID))
}
