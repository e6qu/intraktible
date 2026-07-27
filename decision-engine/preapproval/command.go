// SPDX-License-Identifier: AGPL-3.0-or-later

package preapproval

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/e6qu/intraktible/decision-engine/policy"
	"github.com/e6qu/intraktible/platform/entity"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
)

// Handler is the pre-approval write side (imperative shell).
type Handler struct {
	log   eventlog.Log
	now   func() time.Time
	newID func() string
	mu    sync.Mutex
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

// GrantCmd grants a pre-approval for an entity, valid for ValidDays.
type GrantCmd struct {
	EntityType    string
	EntityID      string
	Disposition   string // approve | decline (default approve)
	Terms         json.RawMessage
	PolicyID      string
	PolicyVersion int
	FlowSlug      string
	ValidDays     int
	Note          string
}

// Grant records a Granted event, computing ValidUntil from the clock (an effect,
// so it is recorded and replay-stable).
func (h *Handler) Grant(ctx context.Context, id identity.Identity, cmd GrantCmd) (string, eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return "", eventlog.Envelope{}, err
	}
	if cmd.EntityType == "" || cmd.EntityID == "" {
		return "", eventlog.Envelope{}, fmt.Errorf("preapproval: entity_type and entity_id are required")
	}
	// A pre-approval pre-decides a policy disposition (the shared policy.Disposition
	// vocabulary) — but only the terminal approve/decline, never refer (you cannot
	// pre-refer to a human). Default to approve.
	disp := policy.Disposition(cmd.Disposition)
	if disp == "" {
		disp = policy.Approve
	}
	if disp != policy.Approve && disp != policy.Decline {
		return "", eventlog.Envelope{}, fmt.Errorf("preapproval: invalid disposition %q (approve|decline)", disp)
	}
	if cmd.ValidDays <= 0 {
		return "", eventlog.Envelope{}, fmt.Errorf("preapproval: valid_days must be positive")
	}
	// Cap the horizon so the duration multiply below cannot overflow int64 ns into a
	// negative/garbage ValidUntil (10 years is well beyond any real pre-approval).
	if cmd.ValidDays > 3650 {
		return "", eventlog.Envelope{}, fmt.Errorf("preapproval: valid_days too large (max 3650)")
	}
	id2 := h.newID()
	e, err := h.append(ctx, id, TypeGranted, Granted{
		PreApprovalID: id2, EntityType: cmd.EntityType, EntityID: cmd.EntityID,
		Disposition: string(disp), Terms: cmd.Terms, PolicyID: cmd.PolicyID, PolicyVersion: cmd.PolicyVersion,
		FlowSlug: cmd.FlowSlug, ValidUntil: h.now().Add(time.Duration(cmd.ValidDays) * 24 * time.Hour),
		Note: cmd.Note,
	})
	if err != nil {
		return "", eventlog.Envelope{}, err
	}
	return id2, e, nil
}

// Revoke invalidates an entity's current pre-approval.
func (h *Handler) Revoke(ctx context.Context, id identity.Identity, ref entity.Ref, reason string) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if ref.Empty() {
		return eventlog.Envelope{}, fmt.Errorf("preapproval: entity_type and entity_id are required")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	currentID, active, err := h.current(ctx, id, ref)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if currentID == "" {
		return eventlog.Envelope{}, fmt.Errorf("preapproval: no pre-approval for %s:%s", ref.Type, ref.ID)
	}
	if !active {
		return eventlog.Envelope{}, fmt.Errorf("preapproval: pre-approval %q is already revoked", currentID)
	}
	payload := Revoked{
		PreApprovalID: currentID,
		EntityType:    string(ref.Type),
		EntityID:      string(ref.ID),
		Reason:        reason,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return eventlog.Envelope{}, fmt.Errorf("preapproval: marshal revoke: %w", err)
	}
	e, err := h.log.Append(ctx, eventlog.Envelope{
		Org: id.Org, Workspace: id.Workspace, Actor: id.Actor,
		Stream: StreamPreApprovals, Type: TypeRevoked, Time: h.now(), Payload: raw,
		Unique: "preapproval.revoke\x00" + id.Org + "\x00" + id.Workspace + "\x00" + currentID,
	})
	if errors.Is(err, eventlog.ErrConflict) {
		return eventlog.Envelope{}, fmt.Errorf("preapproval: pre-approval %q is already revoked", currentID)
	}
	return e, err
}

// current folds the entity's lifecycle from the authoritative log. The unique
// revoke claim below resolves cross-process races; this fold supplies the
// operator-facing validation and the exact grant id the revoke must target.
func (h *Handler) current(
	ctx context.Context,
	id identity.Identity,
	ref entity.Ref,
) (preApprovalID string, active bool, err error) {
	evs, err := h.log.ReadTenantStream(ctx, id.Org, id.Workspace, StreamPreApprovals, 0)
	if err != nil {
		return "", false, fmt.Errorf("preapproval: read lifecycle: %w", err)
	}
	for _, e := range evs {
		switch e.Type {
		case TypeGranted:
			var p Granted
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return "", false, fmt.Errorf("preapproval: decode granted seq %d: %w", e.Seq, err)
			}
			if p.EntityType == string(ref.Type) && p.EntityID == string(ref.ID) {
				preApprovalID, active = p.PreApprovalID, true
			}
		case TypeRevoked:
			var p Revoked
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return "", false, fmt.Errorf("preapproval: decode revoked seq %d: %w", e.Seq, err)
			}
			if p.EntityType != string(ref.Type) || p.EntityID != string(ref.ID) {
				continue
			}
			if p.PreApprovalID != "" && p.PreApprovalID != preApprovalID {
				return "", false, fmt.Errorf(
					"preapproval: revoke seq %d targets %q but current pre-approval is %q",
					e.Seq, p.PreApprovalID, preApprovalID,
				)
			}
			active = false
		case TypeHonored:
			var p Honored
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return "", false, fmt.Errorf("preapproval: decode honored seq %d: %w", e.Seq, err)
			}
		}
	}
	return preApprovalID, active, nil
}

func (h *Handler) append(ctx context.Context, id identity.Identity, typ string, payload any) (eventlog.Envelope, error) {
	return eventlog.AppendJSON(ctx, h.log, id.Org, id.Workspace, id.Actor, StreamPreApprovals, typ, h.now(), payload)
}
