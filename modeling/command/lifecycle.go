// SPDX-License-Identifier: AGPL-3.0-or-later

package command

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/e6qu/intraktible/modeling/domain"
	"github.com/e6qu/intraktible/modeling/events"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
)

// ExpireSnapshot records the retention boundary for a published snapshot.
func (h *Handler) ExpireSnapshot(
	ctx context.Context,
	id identity.Identity,
	snapshotID string,
	reason string,
) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if strings.TrimSpace(snapshotID) == "" || strings.TrimSpace(reason) == "" {
		return eventlog.Envelope{}, errors.New(
			"modeling: snapshot_id and expiration reason are required",
		)
	}
	envelopes, err := h.log.ReadTenantStream(
		ctx, id.Org, id.Workspace, events.StreamModeling, 0,
	)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	var published *events.SnapshotPublished
	for _, envelope := range envelopes {
		switch envelope.Type {
		case events.TypeSnapshotPublished:
			var payload events.SnapshotPublished
			if err := decode(envelope, &payload); err != nil {
				return eventlog.Envelope{}, err
			}
			if payload.Manifest.SnapshotID == snapshotID {
				candidate := payload
				published = &candidate
			}
		case events.TypeSnapshotExpired:
			var payload events.SnapshotExpired
			if err := decode(envelope, &payload); err != nil {
				return eventlog.Envelope{}, err
			}
			if payload.SnapshotID == snapshotID {
				return eventlog.Envelope{}, fmt.Errorf(
					"modeling: snapshot %q is already expired", snapshotID,
				)
			}
		}
	}
	if published == nil {
		return eventlog.Envelope{}, fmt.Errorf(
			"modeling: unknown published snapshot %q", snapshotID,
		)
	}
	now := h.now().UTC()
	if now.Before(published.Manifest.ExpiresAt) {
		return eventlog.Envelope{}, fmt.Errorf(
			"modeling: snapshot %q is retained until %s",
			snapshotID, published.Manifest.ExpiresAt.Format(time.RFC3339),
		)
	}
	return h.appendUnique(ctx, id, events.TypeSnapshotExpired, events.SnapshotExpired{
		SnapshotID: snapshotID, StorageRef: published.Manifest.StorageRef,
		ExpiredAt: now, Reason: strings.TrimSpace(reason),
	}, "modeling.snapshot.expire\x00"+snapshotID)
}

// OpenFreshnessIncident records one stale episode. incidentID must be derived
// from the exact schema version and last-received baseline so competing
// scheduler replicas race on one immutable claim.
func (h *Handler) OpenFreshnessIncident(
	ctx context.Context,
	id identity.Identity,
	incidentID string,
	ref domain.SchemaRef,
	version int,
	hash string,
	action domain.QualityAction,
	lastReceivedAt time.Time,
	deadline time.Time,
) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if strings.TrimSpace(incidentID) == "" || ref.Validate() != nil ||
		version <= 0 || strings.TrimSpace(hash) == "" ||
		lastReceivedAt.IsZero() || deadline.IsZero() || !deadline.After(lastReceivedAt) {
		return eventlog.Envelope{}, errors.New(
			"modeling: complete freshness incident evidence is required",
		)
	}
	now := h.now().UTC()
	if now.Before(deadline) {
		return eventlog.Envelope{}, errors.New("modeling: source freshness deadline has not elapsed")
	}
	return h.appendUnique(
		ctx, id, events.TypeSourceFreshnessViolated, events.SourceFreshnessViolated{
			IncidentID: incidentID, Ref: ref, SchemaVersion: version, SchemaHash: hash,
			Action: action, LastReceivedAt: lastReceivedAt.UTC(),
			Deadline: deadline.UTC(), DetectedAt: now,
		},
		"modeling.freshness.open\x00"+incidentID,
	)
}

// RecoverFreshnessIncident records a source's return to its active freshness
// window. Manual incident resolution and automatic recovery share the same
// terminal unique claim.
func (h *Handler) RecoverFreshnessIncident(
	ctx context.Context,
	id identity.Identity,
	incidentID string,
) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if strings.TrimSpace(incidentID) == "" {
		return eventlog.Envelope{}, errors.New("modeling: incident_id is required")
	}
	return h.appendUnique(
		ctx, id, events.TypeSourceFreshnessRecovered,
		events.SourceFreshnessRecovered{
			IncidentID: incidentID, RecoveredAt: h.now().UTC(),
		},
		"modeling.quality.resolve\x00"+incidentID,
	)
}
