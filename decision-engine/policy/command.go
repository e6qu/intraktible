// SPDX-License-Identifier: AGPL-3.0-or-later

package policy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
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

	exists, latest, err := h.fold(ctx, id, policyID)
	if err != nil {
		return 0, "", eventlog.Envelope{}, err
	}
	if !exists {
		return 0, "", eventlog.Envelope{}, fmt.Errorf("policy: unknown policy %q", policyID)
	}
	version := latest + 1
	e, err := h.append(ctx, id, TypePolicyVersionPublished, VersionPublished{
		PolicyID: policyID, Version: version, Etag: etag, Spec: spec,
	})
	if err != nil {
		return 0, "", eventlog.Envelope{}, err
	}
	return version, etag, e, nil
}

// fold reads the policies stream for the tenant to learn whether the policy exists
// and its highest published version (the source of truth is the log).
func (h *Handler) fold(ctx context.Context, id identity.Identity, policyID string) (bool, int, error) {
	evs, err := h.log.Read(ctx, 0)
	if err != nil {
		return false, 0, err
	}
	exists, latest := false, 0
	for _, ev := range evs {
		if ev.Org != id.Org || ev.Workspace != id.Workspace || ev.Stream != StreamPolicies {
			continue
		}
		switch ev.Type {
		case TypePolicyCreated:
			var p Created
			if err := json.Unmarshal(ev.Payload, &p); err != nil {
				return false, 0, err
			}
			if p.PolicyID == policyID {
				exists = true
			}
		case TypePolicyVersionPublished:
			var p VersionPublished
			if err := json.Unmarshal(ev.Payload, &p); err != nil {
				return false, 0, err
			}
			if p.PolicyID == policyID && p.Version > latest {
				latest = p.Version
			}
		}
	}
	return exists, latest, nil
}

func (h *Handler) append(ctx context.Context, id identity.Identity, typ string, payload any) (eventlog.Envelope, error) {
	return eventlog.AppendJSON(ctx, h.log, id.Org, id.Workspace, id.Actor, StreamPolicies, typ, h.now(), payload)
}
