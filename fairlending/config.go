// SPDX-License-Identifier: AGPL-3.0-or-later

package fairlending

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/e6qu/intraktible/decision-engine/policy"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// StreamConfig is the event stream for a flow's fair-lending configuration.
const StreamConfig = "fairlending.config"

// TypeConfigSet records a replacement of a flow's fair-lending config.
const TypeConfigSet = "fairlending.config_set"

// ConfigCollection holds a flow's fair-lending config (one doc per flow).
const ConfigCollection = "fairlending_config"

// configSet is the event payload replacing a flow's config.
type configSet struct {
	FlowID    string             `json:"flow_id"`
	Attribute string             `json:"attribute"`
	Favorable policy.Disposition `json:"favorable"`
	Threshold float64            `json:"threshold"`
}

// ConfigView is a flow's stored fair-lending config: the protected-class input
// field, the disposition treated as favorable, and the AIR threshold. It turns the
// disparate-impact screen from an ad-hoc query into a first-class flow artifact the
// report and the governance surface both read.
type ConfigView struct {
	Org       string             `json:"org"`
	Workspace string             `json:"workspace"`
	FlowID    string             `json:"flow_id"`
	Attribute string             `json:"attribute"`
	Favorable policy.Disposition `json:"favorable"`
	Threshold float64            `json:"threshold"`
	UpdatedAt time.Time          `json:"updated_at"`
	UpdatedBy string             `json:"updated_by"`
}

// ConfigProjector folds the config stream into the per-flow config doc.
type ConfigProjector struct{}

// Name identifies the projector.
func (ConfigProjector) Name() string { return ConfigCollection }

// Collections lists the store collection this projector owns.
func (ConfigProjector) Collections() []string { return []string{ConfigCollection} }

// Apply maintains a flow's config doc from each config-set event.
func (ConfigProjector) Apply(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	if e.Type != TypeConfigSet {
		return nil
	}
	var p configSet
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("fairlending_config: decode set seq %d: %w", e.Seq, err)
	}
	v := ConfigView{
		Org: e.Org, Workspace: e.Workspace, FlowID: p.FlowID,
		Attribute: p.Attribute, Favorable: p.Favorable, Threshold: p.Threshold,
		UpdatedAt: e.Time, UpdatedBy: e.Actor,
	}
	return store.PutDoc(ctx, s, ConfigCollection, store.Key(e.Org, e.Workspace, p.FlowID), v)
}

// ReadConfig returns a flow's stored fair-lending config (false when none set).
func ReadConfig(ctx context.Context, s store.Store, id identity.Identity, flowID string) (ConfigView, bool, error) {
	return store.GetDoc[ConfigView](ctx, s, ConfigCollection, store.Key(id.Org, id.Workspace, flowID))
}

// Handler is the fair-lending write side (imperative shell): it records per-flow
// config and workspace adverse-action settings.
type Handler struct {
	log eventlog.Log
	now func() time.Time
}

// NewHandler builds a Handler using the system clock.
func NewHandler(log eventlog.Log) *Handler {
	return &Handler{log: log, now: func() time.Time { return time.Now().UTC() }}
}

// WithNow overrides the clock used to stamp recorded events (deterministic tests,
// the demo seeder) and returns the handler.
func (h *Handler) WithNow(now func() time.Time) *Handler {
	h.now = now
	return h
}

// SetConfig replaces a flow's fair-lending config. attribute is required; favorable
// defaults to approve and must be a real disposition; threshold must be in (0,1] (0
// means "use the four-fifths default"). The config is not validated against live
// data here — an attribute that no decision carries simply yields an all-excluded
// report, which the report itself makes visible.
func (h *Handler) SetConfig(ctx context.Context, id identity.Identity, flowID, attribute string, favorable policy.Disposition, threshold float64) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if strings.TrimSpace(flowID) == "" {
		return eventlog.Envelope{}, fmt.Errorf("fairlending: flow_id is required")
	}
	attribute = strings.TrimSpace(attribute)
	if attribute == "" {
		return eventlog.Envelope{}, fmt.Errorf("fairlending: attribute is required")
	}
	if favorable == "" {
		favorable = policy.Approve
	}
	if favorable != policy.Approve && favorable != policy.Decline && favorable != policy.Refer {
		return eventlog.Envelope{}, fmt.Errorf("fairlending: favorable must be approve, decline, or refer")
	}
	if threshold < 0 || threshold > 1 {
		return eventlog.Envelope{}, fmt.Errorf("fairlending: threshold must be between 0 and 1")
	}
	b, err := json.Marshal(configSet{FlowID: flowID, Attribute: attribute, Favorable: favorable, Threshold: threshold})
	if err != nil {
		return eventlog.Envelope{}, fmt.Errorf("fairlending: marshal config: %w", err)
	}
	return h.log.Append(ctx, eventlog.Envelope{
		Org: id.Org, Workspace: id.Workspace, Actor: id.Actor,
		Stream: StreamConfig, Type: TypeConfigSet, Time: h.now(), Payload: b,
	})
}
