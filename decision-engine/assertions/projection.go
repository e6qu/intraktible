// SPDX-License-Identifier: AGPL-3.0-or-later

package assertions

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// Collection holds a flow's assertion set (one doc per flow).
const Collection = "decision_assertions"

// View is a flow's stored assertion cases.
type View struct {
	Org       string    `json:"org"`
	Workspace string    `json:"workspace"`
	FlowID    string    `json:"flow_id"`
	Cases     []Case    `json:"cases"`
	UpdatedAt time.Time `json:"updated_at"`
	UpdatedBy string    `json:"updated_by"`
}

// Projector folds the assertions stream into the per-flow case set.
type Projector struct{}

func (Projector) Name() string          { return Collection }
func (Projector) Collections() []string { return []string{Collection} }

func (Projector) Apply(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	if e.Type != TypeSet {
		return nil
	}
	var p Set
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("decision_assertions: decode set seq %d: %w", e.Seq, err)
	}
	v := View{Org: e.Org, Workspace: e.Workspace, FlowID: p.FlowID, Cases: p.Cases, UpdatedAt: e.Time, UpdatedBy: e.Actor}
	return store.PutDoc(ctx, s, Collection, store.Key(e.Org, e.Workspace, p.FlowID), v)
}

// Read returns a flow's assertion set (defaults to empty when unset).
func Read(ctx context.Context, s store.Store, id identity.Identity, flowID string) (View, bool, error) {
	return store.GetDoc[View](ctx, s, Collection, store.Key(id.Org, id.Workspace, flowID))
}
