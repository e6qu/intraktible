// SPDX-License-Identifier: AGPL-3.0-or-later

package policy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
)

// Handler is the policy write side (imperative shell): it validates via the pure
// core, derives version numbers from the policy's own event history, then appends.
// A mutex serializes read-modify-append per instance (correct for the monolith).
type Handler struct {
	log   eventlog.Log
	mu    sync.Mutex
	now   func() time.Time
	newID func() string
}

// NewHandler builds a Handler using the system clock and a random id source.
func NewHandler(log eventlog.Log) *Handler {
	return &Handler{log: log, now: func() time.Time { return time.Now().UTC() }, newID: newID}
}

// WithNow overrides the clock used to stamp recorded events (deterministic
// tests, the demo seeder) and returns the handler.
func (h *Handler) WithNow(now func() time.Time) *Handler {
	h.now = now
	return h
}

func newID() string {
	var b [16]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		panic("decision-engine: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// CreatePolicy registers a new (unversioned) policy bound to a flow slug.
func (h *Handler) CreatePolicy(ctx context.Context, id identity.Identity, name, flowSlug string) (string, eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return "", eventlog.Envelope{}, err
	}
	if name == "" || flowSlug == "" {
		return "", eventlog.Envelope{}, fmt.Errorf("policy: name and flow_slug are required")
	}
	policyID := h.newID()
	e, err := h.append(ctx, id, TypePolicyCreated, Created{PolicyID: policyID, Name: name, FlowSlug: flowSlug})
	if err != nil {
		return "", eventlog.Envelope{}, err
	}
	return policyID, e, nil
}

// PublishVersion validates the spec, computes the next version + etag, and appends
// a PolicyVersionPublished event. It returns the assigned version and etag.
func (h *Handler) PublishVersion(ctx context.Context, id identity.Identity, policyID string, spec Spec) (int, string, eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return 0, "", eventlog.Envelope{}, err
	}
	if err := spec.Validate(); err != nil {
		return 0, "", eventlog.Envelope{}, err
	}
	etag, err := Etag(spec)
	if err != nil {
		return 0, "", eventlog.Envelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()

	state, err := h.fold(ctx, id, policyID)
	if err != nil {
		return 0, "", eventlog.Envelope{}, err
	}
	if !state.exists {
		return 0, "", eventlog.Envelope{}, fmt.Errorf("policy: unknown policy %q", policyID)
	}
	version := state.latest + 1
	e, err := h.append(ctx, id, TypePolicyVersionPublished, VersionPublished{
		PolicyID: policyID, Version: version, Etag: etag, Spec: spec,
	})
	if err != nil {
		return 0, "", eventlog.Envelope{}, err
	}
	return version, etag, e, nil
}

type policyState struct {
	exists                  bool
	latest, approvedVersion int
	name                    string
	latestAuthor            string
	pendingID, pendingBy    string
	pendingVersion          int
}

// fold reads the tenant's policy stream to reconstruct one policy's authoritative
// version and maker-checker state.
func (h *Handler) fold(ctx context.Context, id identity.Identity, policyID string) (policyState, error) {
	evs, err := h.log.ReadTenantStream(ctx, id.Org, id.Workspace, StreamPolicies, 0)
	if err != nil {
		return policyState{}, err
	}
	var state policyState
	for _, ev := range evs {
		switch ev.Type {
		case TypePolicyCreated:
			var p Created
			if err := json.Unmarshal(ev.Payload, &p); err != nil {
				return policyState{}, err
			}
			if p.PolicyID == policyID {
				state.exists = true
				state.name = p.Name
			}
		case TypePolicyVersionPublished:
			var p VersionPublished
			if err := json.Unmarshal(ev.Payload, &p); err != nil {
				return policyState{}, err
			}
			if p.PolicyID == policyID && p.Version > state.latest {
				state.latest, state.latestAuthor = p.Version, ev.Actor
				state.pendingID, state.pendingBy, state.pendingVersion = "", "", 0
			}
		case TypePolicyApprovalRequested:
			var p ApprovalRequested
			if err := json.Unmarshal(ev.Payload, &p); err != nil {
				return policyState{}, err
			}
			if p.PolicyID == policyID && p.Version == state.latest {
				state.pendingID, state.pendingBy, state.pendingVersion = p.RequestID, ev.Actor, p.Version
			}
		case TypePolicyApprovalApproved:
			var p ApprovalApproved
			if err := json.Unmarshal(ev.Payload, &p); err != nil {
				return policyState{}, err
			}
			if p.PolicyID == policyID && p.RequestID == state.pendingID {
				state.approvedVersion = p.Version
				state.pendingID, state.pendingBy, state.pendingVersion = "", "", 0
			}
		case TypePolicyApprovalRejected:
			var p ApprovalRejected
			if err := json.Unmarshal(ev.Payload, &p); err != nil {
				return policyState{}, err
			}
			if p.PolicyID == policyID && p.RequestID == state.pendingID {
				state.pendingID, state.pendingBy, state.pendingVersion = "", "", 0
			}
		}
	}
	return state, nil
}

// RequestApproval submits the policy's latest published version for four-eyes
// review. Publishing remains safe iteration: the prior approved version keeps
// serving outside sandbox until the new request is approved.
func (h *Handler) RequestApproval(ctx context.Context, id identity.Identity, policyID string) (string, eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return "", eventlog.Envelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state, err := h.fold(ctx, id, policyID)
	if err != nil {
		return "", eventlog.Envelope{}, err
	}
	switch {
	case !state.exists:
		return "", eventlog.Envelope{}, fmt.Errorf("policy: unknown policy %q", policyID)
	case state.latest == 0:
		return "", eventlog.Envelope{}, fmt.Errorf("policy: policy %q has no published version", policyID)
	case state.approvedVersion == state.latest:
		return "", eventlog.Envelope{}, fmt.Errorf("policy: policy %q version %d is already approved", policyID, state.latest)
	case state.pendingID != "":
		return "", eventlog.Envelope{}, fmt.Errorf("policy: policy %q already has a pending approval request", policyID)
	}
	requestID := h.newID()
	e, err := h.append(ctx, id, TypePolicyApprovalRequested, ApprovalRequested{
		RequestID: requestID, PolicyID: policyID, Name: state.name, Version: state.latest,
	})
	return requestID, e, err
}

// Approve approves a pending policy request; the checker must differ from both
// the requester and the current version's author.
func (h *Handler) Approve(ctx context.Context, id identity.Identity, policyID, requestID, reason string) (eventlog.Envelope, error) {
	return h.decideApproval(ctx, id, policyID, requestID, reason, true)
}

// Reject rejects a pending policy request without changing the serving version.
func (h *Handler) Reject(ctx context.Context, id identity.Identity, policyID, requestID, reason string) (eventlog.Envelope, error) {
	return h.decideApproval(ctx, id, policyID, requestID, reason, false)
}

func (h *Handler) decideApproval(
	ctx context.Context,
	id identity.Identity,
	policyID, requestID, reason string,
	approve bool,
) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if strings.TrimSpace(reason) == "" {
		return eventlog.Envelope{}, fmt.Errorf("policy: approval decision reason is required")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state, err := h.fold(ctx, id, policyID)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if state.pendingID == "" || state.pendingID != requestID {
		return eventlog.Envelope{}, fmt.Errorf("policy: no pending approval request %q for policy %q", requestID, policyID)
	}
	if state.pendingVersion != state.latest {
		return eventlog.Envelope{}, fmt.Errorf("policy: approval request %q is stale; policy %q has changed", requestID, policyID)
	}
	if approve {
		if id.Actor == state.pendingBy {
			return eventlog.Envelope{}, fmt.Errorf("policy: four-eyes — %q cannot approve their own request", id.Actor)
		}
		if id.Actor == state.latestAuthor {
			return eventlog.Envelope{}, fmt.Errorf("policy: four-eyes — %q authored policy %q version %d and cannot approve it", id.Actor, policyID, state.latest)
		}
	}
	typ := TypePolicyApprovalRejected
	payload, err := json.Marshal(ApprovalRejected{
		RequestID: requestID, PolicyID: policyID, Version: state.pendingVersion, Reason: strings.TrimSpace(reason),
	})
	if approve {
		typ = TypePolicyApprovalApproved
		payload, err = json.Marshal(ApprovalApproved{
			RequestID: requestID, PolicyID: policyID, Version: state.pendingVersion, Reason: strings.TrimSpace(reason),
		})
	}
	if err != nil {
		return eventlog.Envelope{}, fmt.Errorf("policy: marshal approval decision: %w", err)
	}
	return h.log.Append(ctx, eventlog.Envelope{
		Org: id.Org, Workspace: id.Workspace, Actor: id.Actor,
		Stream: StreamPolicies, Type: typ, Time: h.now(), Payload: payload,
		Unique: "policy.approval.decision\x00" + policyID + "\x00" + requestID,
	})
}

func (h *Handler) append(ctx context.Context, id identity.Identity, typ string, payload any) (eventlog.Envelope, error) {
	return eventlog.AppendJSON(ctx, h.log, id.Org, id.Workspace, id.Actor, StreamPolicies, typ, h.now(), payload)
}
