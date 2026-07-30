// SPDX-License-Identifier: AGPL-3.0-or-later

package policy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// Collection is the policies read-model collection.
const Collection = "decision_policies"

// ErrNoApprovedVersion means a policy is bound and published, but governance has
// not approved any immutable version for non-sandbox serving. Callers can map
// this operator-correctable configuration error without hiding store failures.
var ErrNoApprovedVersion = errors.New("decision-engine: no approved policy version")

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

// PendingApproval is the maker's current request for a specific policy version.
type PendingApproval struct {
	RequestID   string    `json:"request_id"`
	Version     int       `json:"version"`
	RequestedBy string    `json:"requested_by"`
	RequestedAt time.Time `json:"requested_at"`
}

// View is the registry entry for one policy: metadata + its versions.
type View struct {
	Org             string           `json:"org"`
	Workspace       string           `json:"workspace"`
	PolicyID        string           `json:"policy_id"`
	Name            string           `json:"name"`
	FlowSlug        string           `json:"flow_slug"`
	Latest          int              `json:"latest"`
	Versions        []VersionView    `json:"versions"`
	ApprovedVersion int              `json:"approved_version"`
	ApprovedBy      string           `json:"approved_by,omitempty"`
	ApprovedAt      time.Time        `json:"approved_at,omitempty"`
	ApprovalSeq     uint64           `json:"approval_seq,omitempty"`
	Pending         *PendingApproval `json:"pending,omitempty"`
	CreatedAt       time.Time        `json:"created_at"`
	UpdatedAt       time.Time        `json:"updated_at"`
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
			// A request pins one immutable version; a newer publication makes that
			// work item stale while the prior ApprovedVersion keeps serving.
			pv.Pending = nil
			pv.UpdatedAt = e.Time
		})
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("decision_policies: published seq %d for unknown policy %q", e.Seq, p.PolicyID)
		}
		return nil
	case TypePolicyApprovalRequested:
		var p ApprovalRequested
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return fmt.Errorf("decision_policies: decode approval requested seq %d: %w", e.Seq, err)
		}
		return updatePolicy(ctx, e, s, p.PolicyID, func(v *View) {
			if p.Version != v.Latest {
				return
			}
			v.Pending = &PendingApproval{
				RequestID: p.RequestID, Version: p.Version,
				RequestedBy: e.Actor, RequestedAt: e.Time,
			}
		})
	case TypePolicyApprovalApproved:
		var p ApprovalApproved
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return fmt.Errorf("decision_policies: decode approval approved seq %d: %w", e.Seq, err)
		}
		return updatePolicy(ctx, e, s, p.PolicyID, func(v *View) {
			if v.Pending == nil || v.Pending.RequestID != p.RequestID {
				return
			}
			v.ApprovedVersion, v.ApprovedBy, v.ApprovedAt = p.Version, e.Actor, e.Time
			v.ApprovalSeq, v.Pending = e.Seq, nil
		})
	case TypePolicyApprovalRejected:
		var p ApprovalRejected
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return fmt.Errorf("decision_policies: decode approval rejected seq %d: %w", e.Seq, err)
		}
		return updatePolicy(ctx, e, s, p.PolicyID, func(v *View) {
			if v.Pending == nil || v.Pending.RequestID != p.RequestID {
				return
			}
			v.Pending = nil
		})
	}
	return nil
}

func updatePolicy(
	ctx context.Context,
	e eventlog.Envelope,
	s store.Store,
	policyID string,
	update func(*View),
) error {
	ok, err := store.UpdateDoc(ctx, s, Collection, store.Key(e.Org, e.Workspace, policyID), update)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("decision_policies: %s seq %d for unknown policy %q", e.Type, e.Seq, policyID)
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

// ReadVersion returns one immutable published policy version.
func ReadVersion(ctx context.Context, s store.Store, id identity.Identity, policyID string, version int) (View, VersionView, bool, error) {
	v, ok, err := Read(ctx, s, id, policyID)
	if err != nil || !ok {
		return View{}, VersionView{}, false, err
	}
	for i := range v.Versions {
		if v.Versions[i].Version == version {
			return v, v.Versions[i], true, nil
		}
	}
	return View{}, VersionView{}, false, nil
}

// ActiveForFlow returns the policy bound to a flow slug and its latest published
// version's spec. It is the decide path's policy lookup. When more than one policy
// is bound to a slug, the most recently updated one wins. Returns ok=false when no
// policy (or no published version) is bound.
func ActiveForFlow(ctx context.Context, s store.Store, id identity.Identity, flowSlug string) (View, VersionView, bool, error) {
	candidates, err := policiesForFlow(ctx, s, id, flowSlug)
	if err != nil {
		return View{}, VersionView{}, false, err
	}
	var best View
	found := false
	for _, pv := range candidates {
		if len(pv.Versions) == 0 {
			continue
		}
		if !found || pv.UpdatedAt.After(best.UpdatedAt) {
			best, found = pv, true
		}
	}
	if !found {
		return View{}, VersionView{}, false, nil
	}
	return best, latestVersion(best), true, nil
}

// ApprovedForFlow returns the policy version approved for non-sandbox serving.
// Only an approval changes which of several policies bound to a flow is active;
// publishing an unapproved draft therefore cannot switch production between
// policies. found=false means the flow has no published policy at all. A published
// policy with no approved serving candidate is a loud governance error.
func ApprovedForFlow(ctx context.Context, s store.Store, id identity.Identity, flowSlug string) (View, VersionView, bool, error) {
	candidates, err := policiesForFlow(ctx, s, id, flowSlug)
	if err != nil {
		return View{}, VersionView{}, false, err
	}
	published := false
	var best View
	found := false
	for _, pv := range candidates {
		if len(pv.Versions) == 0 {
			continue
		}
		published = true
		if pv.ApprovedVersion <= 0 {
			continue
		}
		if !found || pv.ApprovalSeq > best.ApprovalSeq {
			best, found = pv, true
		}
	}
	if !found {
		if published {
			return View{}, VersionView{}, false, fmt.Errorf(
				"%w: flow %q has a published policy but no approved version for non-sandbox serving",
				ErrNoApprovedVersion,
				flowSlug,
			)
		}
		return View{}, VersionView{}, false, nil
	}
	for i := range best.Versions {
		if best.Versions[i].Version == best.ApprovedVersion {
			return best, best.Versions[i], true, nil
		}
	}
	return View{}, VersionView{}, false, fmt.Errorf(
		"decision-engine: policy %q approval references missing version %d",
		best.PolicyID, best.ApprovedVersion,
	)
}

func policiesForFlow(ctx context.Context, s store.Store, id identity.Identity, flowSlug string) ([]View, error) {
	// Fast path: the flow index lists exactly the policies bound to this slug, so
	// only those candidates are fetched (no whole-collection scan). A store error is
	// real and surfaces — degrading to the scan would hide a broken store behind a
	// slower query that reads the same store. Only a miss falls through.
	idx, indexed, err := store.GetDoc[flowPolicyIndex](ctx, s, flowIndexCollection, store.Key(id.Org, id.Workspace, flowSlug))
	if err != nil {
		return nil, fmt.Errorf("decision-engine: read policy flow index %q: %w", flowSlug, err)
	}
	if indexed {
		out := make([]View, 0, len(idx.PolicyIDs))
		for _, pid := range idx.PolicyIDs {
			pv, pok, perr := store.GetDoc[View](ctx, s, Collection, store.Key(id.Org, id.Workspace, pid))
			if perr != nil {
				return nil, perr
			}
			if !pok || pv.FlowSlug != flowSlug {
				continue
			}
			out = append(out, pv)
		}
		return out, nil
	}
	// Fallback: scan. Covers a flow with no index entry yet (e.g. a policy created
	// before this index existed, until the next rebuild) and confirms a genuine
	// "no active policy" — correctness never depends on the index.
	pvs, err := List(ctx, s, id)
	if err != nil {
		return nil, err
	}
	out := make([]View, 0)
	for _, pv := range pvs {
		if pv.FlowSlug == flowSlug {
			out = append(out, pv)
		}
	}
	return out, nil
}

// latestVersion returns the policy's latest published version, selected by the
// tracked Latest field rather than array position: Versions is appended in event
// order, which is not guaranteed to be version order, so the last element is not
// reliably the latest. Falls back to the last element only if Latest is absent
// (it never should be for a policy with versions). Every caller already skips a
// policy with no versions, so an empty View yields the zero version rather than
// indexing past the end.
func latestVersion(pv View) VersionView {
	for i := range pv.Versions {
		if pv.Versions[i].Version == pv.Latest {
			return pv.Versions[i]
		}
	}
	if len(pv.Versions) == 0 {
		return VersionView{}
	}
	return pv.Versions[len(pv.Versions)-1]
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
