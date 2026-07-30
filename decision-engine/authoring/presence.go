// SPDX-License-Identifier: AGPL-3.0-or-later

package authoring

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

const (
	minPresenceTTL = 15 * time.Second
	maxPresenceTTL = 2 * time.Minute
)

// RenewPresence creates or replaces the caller's disposable lease for a draft.
func (h *Handler) RenewPresence(
	ctx context.Context,
	id identity.Identity,
	draftID, displayName string,
	revision int,
	selectedID string,
	ttl time.Duration,
) (Presence, error) {
	if err := id.Valid(); err != nil {
		return Presence{}, err
	}
	if ttl < minPresenceTTL || ttl > maxPresenceTTL {
		return Presence{}, fmt.Errorf(
			"authoring: presence ttl must be between %s and %s",
			minPresenceTTL, maxPresenceTTL,
		)
	}
	draft, ok, err := h.foldDraft(ctx, id, draftID)
	if err != nil {
		return Presence{}, err
	}
	if !ok || draft.State != DraftStateActive {
		return Presence{}, errors.New("authoring: presence requires an active draft")
	}
	if revision < 1 || revision > draft.Revision {
		return Presence{}, fmt.Errorf(
			"authoring: presence revision %d is outside draft history 1..%d",
			revision, draft.Revision,
		)
	}
	if len(displayName) > maxTitle || len(selectedID) > maxTitle {
		return Presence{}, errors.New("authoring: presence fields are too long")
	}
	presence := Presence{
		Org: id.Org, Workspace: id.Workspace, DraftID: draftID, Actor: id.Actor,
		DisplayName: strings.TrimSpace(displayName), Revision: revision,
		SelectedID: strings.TrimSpace(selectedID), ExpiresAt: h.now().Add(ttl),
	}
	if err := store.PutDoc(
		ctx, h.store, PresenceCollection,
		presenceKey(id, draftID),
		presence,
	); err != nil {
		return Presence{}, err
	}
	return presence, nil
}

// LeavePresence removes the caller's lease immediately.
func (h *Handler) LeavePresence(
	ctx context.Context,
	id identity.Identity,
	draftID string,
) error {
	if err := id.Valid(); err != nil {
		return err
	}
	return h.store.Delete(ctx, PresenceCollection, presenceKey(id, draftID))
}

// ListPresence returns only unexpired leases for this draft. Expired rows are
// harmless disposable data and are removed by the authoring scheduler.
func (h *Handler) ListPresence(
	ctx context.Context,
	id identity.Identity,
	draftID string,
) ([]Presence, error) {
	if err := id.Valid(); err != nil {
		return nil, err
	}
	prefix := store.Key(id.Org, id.Workspace, draftID+"/")
	items, err := store.ListDocs[Presence](ctx, h.store, PresenceCollection, prefix)
	if err != nil {
		return nil, err
	}
	now := h.now()
	active := items[:0]
	for _, presence := range items {
		if presence.ExpiresAt.After(now) {
			active = append(active, presence)
		}
	}
	return active, nil
}

// SweepPresence deletes expired leases for all tenants. The rows are not
// governed truth, so deletion is safe and replay intentionally does not restore
// them.
func (h *Handler) SweepPresence(ctx context.Context) (int, error) {
	records, err := h.store.List(ctx, PresenceCollection, "")
	if err != nil {
		return 0, err
	}
	now := h.now()
	deleted := 0
	for _, record := range records {
		var presence Presence
		if err := json.Unmarshal(record.Doc, &presence); err != nil {
			return deleted, fmt.Errorf("authoring: decode presence %q: %w", record.Key, err)
		}
		if presence.ExpiresAt.After(now) {
			continue
		}
		if err := h.store.Delete(ctx, PresenceCollection, record.Key); err != nil {
			return deleted, err
		}
		deleted++
	}
	return deleted, nil
}

func presenceKey(id identity.Identity, draftID string) string {
	hash := sha256.Sum256([]byte(id.Actor))
	return store.Key(id.Org, id.Workspace, draftID+"/"+hex.EncodeToString(hash[:]))
}
