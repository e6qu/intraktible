// SPDX-License-Identifier: AGPL-3.0-or-later

// Package grants is fine-grained, per-flow / per-environment access control layered
// over the global RBAC roles. It is OPT-IN and backward-compatible: a flow with no
// grants behaves exactly as before (global roles decide). Once a flow has any grant,
// the change-control actions on it (deploy / rollback / schedule / promote / approve
// / publish) additionally require the caller to be a grantee for that environment —
// so an org can restrict who may change a specific high-stakes flow. Admins always
// pass (you can't lock yourself out). Grants are event-sourced for a durable,
// rebuildable audit trail.
package grants

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// Stream is the event stream for per-flow access grants.
const Stream = "decision.grants"

// Collection holds grant documents (keyed by grant id).
const Collection = "decision_flow_grants"

// Event types.
const (
	TypeGrantAdded   = "decision.grant.added"
	TypeGrantRevoked = "decision.grant.revoked"
)

// GrantAdded records a grant of change-control access on a flow to an actor in an
// environment ("*" = all environments).
type GrantAdded struct {
	GrantID     string `json:"grant_id"`
	FlowID      string `json:"flow_id"`
	Actor       string `json:"actor"`
	Environment string `json:"environment"`
}

// GrantRevoked removes a grant.
type GrantRevoked struct {
	GrantID string `json:"grant_id"`
	FlowID  string `json:"flow_id"`
}

// View is one stored grant.
type View struct {
	Org         string    `json:"org"`
	Workspace   string    `json:"workspace"`
	GrantID     string    `json:"grant_id"`
	FlowID      string    `json:"flow_id"`
	Actor       string    `json:"actor"`
	Environment string    `json:"environment"`
	CreatedBy   string    `json:"created_by"`
	CreatedAt   time.Time `json:"created_at"`
	Seq         uint64    `json:"seq"`
}

// matches reports whether the grant authorizes actor in env.
func (v View) matches(actor, env string) bool {
	return v.Actor == actor && (v.Environment == "*" || v.Environment == env)
}

// Projector folds the grant stream into View documents.
type Projector struct{}

func (Projector) Name() string          { return Collection }
func (Projector) Collections() []string { return []string{Collection} }

func (Projector) Apply(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	switch e.Type {
	case TypeGrantAdded:
		var p GrantAdded
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return fmt.Errorf("decision_flow_grants: decode added seq %d: %w", e.Seq, err)
		}
		return store.PutDoc(ctx, s, Collection, store.Key(e.Org, e.Workspace, p.GrantID), View{
			Org: e.Org, Workspace: e.Workspace, GrantID: p.GrantID, FlowID: p.FlowID,
			Actor: p.Actor, Environment: p.Environment, CreatedBy: e.Actor, CreatedAt: e.Time, Seq: e.Seq,
		})
	case TypeGrantRevoked:
		var p GrantRevoked
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return fmt.Errorf("decision_flow_grants: decode revoked seq %d: %w", e.Seq, err)
		}
		return s.Delete(ctx, Collection, store.Key(e.Org, e.Workspace, p.GrantID))
	}
	return nil
}

// ForFlow returns a flow's grants (newest first).
func ForFlow(ctx context.Context, s store.Store, id identity.Identity, flowID string) ([]View, error) {
	return store.QueryDocs(ctx, s, Collection, store.Key(id.Org, id.Workspace, ""),
		func(v View) bool { return v.FlowID == flowID },
		func(a, b View) bool { return a.Seq > b.Seq })
}

// Allowed reports whether actor may take a change-control action on flowID in env.
// The rule is opt-in: a flow with no grants is allowed (global RBAC already gated
// the request); otherwise at least one grant must match the actor + environment.
func Allowed(ctx context.Context, s store.Store, id identity.Identity, flowID, env, actor string) (bool, error) {
	gs, err := ForFlow(ctx, s, id, flowID)
	if err != nil {
		return false, err
	}
	if len(gs) == 0 {
		return true, nil
	}
	for _, g := range gs {
		if g.matches(actor, env) {
			return true, nil
		}
	}
	return false, nil
}

// AllowedAny reports whether actor may take a change-control action on flowID that
// is not tied to a specific environment (publish, cancel a schedule). Opt-in like
// Allowed: a flow with no grants is allowed; otherwise the actor must hold at least
// one grant for the flow (in any environment).
func AllowedAny(ctx context.Context, s store.Store, id identity.Identity, flowID, actor string) (bool, error) {
	gs, err := ForFlow(ctx, s, id, flowID)
	if err != nil {
		return false, err
	}
	if len(gs) == 0 {
		return true, nil
	}
	for _, g := range gs {
		if g.Actor == actor {
			return true, nil
		}
	}
	return false, nil
}

// NewID returns a random grant id.
func NewID() string {
	var b [16]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		panic("decision-engine: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}
