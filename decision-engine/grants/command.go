// SPDX-License-Identifier: AGPL-3.0-or-later

package grants

import (
	"context"
	"fmt"
	"time"

	"github.com/e6qu/intraktible/decision-engine/domain"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
)

// Handler is the grant write side: it appends grant events.
type Handler struct {
	log   eventlog.Log
	now   func() time.Time
	newID func() string
}

// NewHandler builds a grant command handler.
func NewHandler(log eventlog.Log) *Handler {
	return &Handler{log: log, now: func() time.Time { return time.Now().UTC() }, newID: NewID}
}

// Add grants actor change-control access on a flow in an environment ("*" = all).
func (h *Handler) Add(ctx context.Context, id identity.Identity, flowID, actor, env string) (string, eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return "", eventlog.Envelope{}, err
	}
	if flowID == "" || actor == "" {
		return "", eventlog.Envelope{}, fmt.Errorf("grants: flow_id and actor are required")
	}
	if env != "*" && !domain.ValidEnvironment(env) {
		return "", eventlog.Envelope{}, fmt.Errorf("grants: environment must be sandbox|staging|production or *")
	}
	gid := h.newID()
	e, err := h.append(ctx, id, TypeGrantAdded, GrantAdded{GrantID: gid, FlowID: flowID, Actor: actor, Environment: env})
	if err != nil {
		return "", eventlog.Envelope{}, err
	}
	return gid, e, nil
}

// Revoke removes a grant.
func (h *Handler) Revoke(ctx context.Context, id identity.Identity, flowID, grantID string) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if grantID == "" {
		return eventlog.Envelope{}, fmt.Errorf("grants: grant_id is required")
	}
	return h.append(ctx, id, TypeGrantRevoked, GrantRevoked{GrantID: grantID, FlowID: flowID})
}

func (h *Handler) append(ctx context.Context, id identity.Identity, typ string, payload any) (eventlog.Envelope, error) {
	return eventlog.AppendJSON(ctx, h.log, id.Org, id.Workspace, id.Actor, Stream, typ, h.now(), payload)
}
