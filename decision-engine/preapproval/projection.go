// SPDX-License-Identifier: AGPL-3.0-or-later

package preapproval

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// Collection is the pre-approvals read-model collection.
const Collection = "decision_preapprovals"

// Status values for a pre-approval.
const (
	StatusActive  = "active"
	StatusRevoked = "revoked"
)

// View is the current pre-approval for an entity. A new grant supersedes the
// prior one for the same entity; expiry is evaluated at read time (against now),
// so the projection stays clock-free and replay-stable.
type View struct {
	Org           string          `json:"org"`
	Workspace     string          `json:"workspace"`
	PreApprovalID string          `json:"preapproval_id"`
	EntityType    string          `json:"entity_type"`
	EntityID      string          `json:"entity_id"`
	Disposition   string          `json:"disposition"`
	Terms         json.RawMessage `json:"terms,omitempty"`
	PolicyID      string          `json:"policy_id,omitempty"`
	PolicyVersion int             `json:"policy_version,omitempty"`
	FlowSlug      string          `json:"flow_slug,omitempty"`
	ValidUntil    time.Time       `json:"valid_until"`
	Status        string          `json:"status"` // active | revoked
	RevokedReason string          `json:"revoked_reason,omitempty"`
	HonoredCount  int             `json:"honored_count"`
	Note          string          `json:"note,omitempty"`
	GrantedAt     time.Time       `json:"granted_at"`
	GrantedBy     string          `json:"granted_by"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

func entityID(entityType, eid string) string { return entityType + ":" + eid }

// Projector folds the pre-approval stream into the read model.
type Projector struct{}

func (Projector) Name() string          { return Collection }
func (Projector) Collections() []string { return []string{Collection} }

func (Projector) Apply(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	switch e.Type {
	case TypeGranted:
		var p Granted
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return fmt.Errorf("decision_preapprovals: decode granted seq %d: %w", e.Seq, err)
		}
		v := View{
			Org: e.Org, Workspace: e.Workspace, PreApprovalID: p.PreApprovalID,
			EntityType: p.EntityType, EntityID: p.EntityID, Disposition: p.Disposition,
			Terms: p.Terms, PolicyID: p.PolicyID, PolicyVersion: p.PolicyVersion, FlowSlug: p.FlowSlug,
			ValidUntil: p.ValidUntil, Status: StatusActive, Note: p.Note,
			GrantedAt: e.Time, GrantedBy: e.Actor, UpdatedAt: e.Time,
		}
		return store.PutDoc(ctx, s, Collection, store.Key(e.Org, e.Workspace, entityID(p.EntityType, p.EntityID)), v)
	case TypeRevoked:
		var p Revoked
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return fmt.Errorf("decision_preapprovals: decode revoked seq %d: %w", e.Seq, err)
		}
		_, err := store.UpdateDoc(ctx, s, Collection, store.Key(e.Org, e.Workspace, entityID(p.EntityType, p.EntityID)), func(v *View) {
			v.Status, v.RevokedReason, v.UpdatedAt = StatusRevoked, p.Reason, e.Time
		})
		return err // a revoke for an unknown entity is a no-op
	case TypeHonored:
		var p Honored
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return fmt.Errorf("decision_preapprovals: decode honored seq %d: %w", e.Seq, err)
		}
		_, err := store.UpdateDoc(ctx, s, Collection, store.Key(e.Org, e.Workspace, entityID(p.EntityType, p.EntityID)), func(v *View) {
			v.HonoredCount++
			v.UpdatedAt = e.Time
		})
		return err
	}
	return nil
}

// ActiveFor returns the entity's pre-approval when it is active and unexpired at
// `now` — the decide path's honor lookup.
func ActiveFor(ctx context.Context, s store.Store, id identity.Identity, entityType, eid string, now time.Time) (View, bool, error) {
	v, ok, err := store.GetDoc[View](ctx, s, Collection, store.Key(id.Org, id.Workspace, entityID(entityType, eid)))
	if err != nil || !ok {
		return View{}, false, err
	}
	if v.Status != StatusActive || !now.Before(v.ValidUntil) {
		return View{}, false, nil
	}
	return v, true, nil
}

// List returns all pre-approvals for the tenant.
func List(ctx context.Context, s store.Store, id identity.Identity) ([]View, error) {
	return store.ListDocs[View](ctx, s, Collection, store.Key(id.Org, id.Workspace, ""))
}

// Read returns the pre-approval for a specific entity (any status).
func Read(ctx context.Context, s store.Store, id identity.Identity, entityType, eid string) (View, bool, error) {
	return store.GetDoc[View](ctx, s, Collection, store.Key(id.Org, id.Workspace, entityID(entityType, eid)))
}
