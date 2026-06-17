// SPDX-License-Identifier: AGPL-3.0-or-later

package privacy

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// Collection holds the per-workspace masking config; configKey is its single doc.
const (
	Collection = "privacy_config"
	configKey  = "config"
)

// View is a workspace's masking configuration.
type View struct {
	Org       string    `json:"org"`
	Workspace string    `json:"workspace"`
	Fields    []string  `json:"fields"`
	UpdatedAt time.Time `json:"updated_at"`
	UpdatedBy string    `json:"updated_by"`
}

// Projector folds the privacy stream into the per-workspace config doc.
type Projector struct{}

func (Projector) Name() string          { return Collection }
func (Projector) Collections() []string { return []string{Collection} }

func (Projector) Apply(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	if e.Type != TypeFieldsSet {
		return nil
	}
	var p FieldsSet
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("privacy_config: decode fields seq %d: %w", e.Seq, err)
	}
	v := View{Org: e.Org, Workspace: e.Workspace, Fields: p.Fields, UpdatedAt: e.Time, UpdatedBy: e.Actor}
	return store.PutDoc(ctx, s, Collection, store.Key(e.Org, e.Workspace, configKey), v)
}

// Read returns a workspace's masking config (defaults to empty when unset).
func Read(ctx context.Context, s store.Store, id identity.Identity) (View, bool, error) {
	return store.GetDoc[View](ctx, s, Collection, store.Key(id.Org, id.Workspace, configKey))
}

// Fields returns the workspace's sensitive-field lookup set (empty when unset),
// the read-boundary masking input. A store error yields an empty set + the error.
func Fields(ctx context.Context, s store.Store, id identity.Identity) (map[string]bool, error) {
	v, ok, err := Read(ctx, s, id)
	if err != nil || !ok {
		return map[string]bool{}, err
	}
	return FieldSet(v.Fields), nil
}
