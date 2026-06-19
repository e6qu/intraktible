// SPDX-License-Identifier: AGPL-3.0-or-later

package policy

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// Collection is the policies read-model collection.
const Collection = "decision_policies"

// flowIndexCollection maps a flow slug to the ids of the policies bound to it, so
// ActiveForFlow (decide hot path) fetches only those candidates instead of
// scanning every policy. Rebuilt with the projection (reset + replayed).
const flowIndexCollection = "decision_policies_by_flow"

type flowPolicyIndex struct {
	PolicyIDs []string `json:"policy_ids"`
}

// VersionView is one published policy version in the read model.
type VersionView struct {
	Version     int       `json:"version"`
	Etag        string    `json:"etag"`
	Spec        Spec      `json:"spec"`
	PublishedAt time.Time `json:"published_at"`
	PublishedBy string    `json:"published_by"`
}

// View is the registry entry for one policy: metadata + its versions.
type View struct {
	Org       string        `json:"org"`
	Workspace string        `json:"workspace"`
	PolicyID  string        `json:"policy_id"`
	Name      string        `json:"name"`
	FlowSlug  string        `json:"flow_slug"`
	Latest    int           `json:"latest"`
	Versions  []VersionView `json:"versions"`
	CreatedAt time.Time     `json:"created_at"`
	UpdatedAt time.Time     `json:"updated_at"`
}

// Projector folds the policies stream into the read model.
type Projector struct{}

func (Projector) Name() string          { return Collection }
func (Projector) Collections() []string { return []string{Collection, flowIndexCollection} }

func (Projector) Apply(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	switch e.Type {
	case TypePolicyCreated:
		var p Created
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return fmt.Errorf("decision_policies: decode created seq %d: %w", e.Seq, err)
		}
		pv := View{
			Org: e.Org, Workspace: e.Workspace, PolicyID: p.PolicyID,
			Name: p.Name, FlowSlug: p.FlowSlug, CreatedAt: e.Time, UpdatedAt: e.Time,
		}
		if err := store.PutDoc(ctx, s, Collection, store.Key(e.Org, e.Workspace, p.PolicyID), pv); err != nil {
			return err
		}
		return addToFlowIndex(ctx, s, e.Org, e.Workspace, p.FlowSlug, p.PolicyID)
	case TypePolicyVersionPublished:
		var p VersionPublished
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return fmt.Errorf("decision_policies: decode published seq %d: %w", e.Seq, err)
		}
		ok, err := store.UpdateDoc(ctx, s, Collection, store.Key(e.Org, e.Workspace, p.PolicyID), func(pv *View) {
			pv.Versions = append(pv.Versions, VersionView{
				Version: p.Version, Etag: p.Etag, Spec: p.Spec, PublishedAt: e.Time, PublishedBy: e.Actor,
			})
			if p.Version > pv.Latest {
				pv.Latest = p.Version
			}
			pv.UpdatedAt = e.Time
		})
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("decision_policies: published seq %d for unknown policy %q", e.Seq, p.PolicyID)
		}
		return nil
	}
	return nil
}

// Read returns one policy by id for the tenant.
func Read(ctx context.Context, s store.Store, id identity.Identity, policyID string) (View, bool, error) {
	return store.GetDoc[View](ctx, s, Collection, store.Key(id.Org, id.Workspace, policyID))
}

// List returns all policies for the tenant, ordered by store key.
func List(ctx context.Context, s store.Store, id identity.Identity) ([]View, error) {
	return store.ListDocs[View](ctx, s, Collection, store.Key(id.Org, id.Workspace, ""))
}

// ActiveForFlow returns the policy bound to a flow slug and its latest published
// version's spec. It is the decide path's policy lookup. When more than one policy
// is bound to a slug, the most recently updated one wins. Returns ok=false when no
// policy (or no published version) is bound.
func ActiveForFlow(ctx context.Context, s store.Store, id identity.Identity, flowSlug string) (View, VersionView, bool, error) {
	// Fast path: the flow index lists exactly the policies bound to this slug, so
	// only those candidates are fetched (no whole-collection scan). If it resolves
	// an active policy, return it.
	if idx, ok, err := store.GetDoc[flowPolicyIndex](ctx, s, flowIndexCollection, store.Key(id.Org, id.Workspace, flowSlug)); err == nil && ok {
		var best View
		found := false
		for _, pid := range idx.PolicyIDs {
			pv, pok, perr := store.GetDoc[View](ctx, s, Collection, store.Key(id.Org, id.Workspace, pid))
			if perr != nil {
				return View{}, VersionView{}, false, perr
			}
			if !pok || pv.FlowSlug != flowSlug || len(pv.Versions) == 0 {
				continue
			}
			if !found || pv.UpdatedAt.After(best.UpdatedAt) {
				best, found = pv, true
			}
		}
		if found {
			return best, best.Versions[len(best.Versions)-1], true, nil
		}
	}
	// Fallback: scan. Covers a flow with no index entry yet (e.g. a policy created
	// before this index existed, until the next rebuild) and confirms a genuine
	// "no active policy" — correctness never depends on the index.
	pvs, err := List(ctx, s, id)
	if err != nil {
		return View{}, VersionView{}, false, err
	}
	var best View
	found := false
	for _, pv := range pvs {
		if pv.FlowSlug != flowSlug || len(pv.Versions) == 0 {
			continue
		}
		if !found || pv.UpdatedAt.After(best.UpdatedAt) {
			best, found = pv, true
		}
	}
	if !found {
		return View{}, VersionView{}, false, nil
	}
	return best, best.Versions[len(best.Versions)-1], true, nil
}

// addToFlowIndex appends policyID to the flow-slug index (idempotently).
func addToFlowIndex(ctx context.Context, s store.Store, org, workspace, flowSlug, policyID string) error {
	key := store.Key(org, workspace, flowSlug)
	idx, _, err := store.GetDoc[flowPolicyIndex](ctx, s, flowIndexCollection, key)
	if err != nil {
		return err
	}
	for _, p := range idx.PolicyIDs {
		if p == policyID {
			return nil
		}
	}
	idx.PolicyIDs = append(idx.PolicyIDs, policyID)
	return store.PutDoc(ctx, s, flowIndexCollection, key, idx)
}
