// SPDX-License-Identifier: AGPL-3.0-or-later

package notifications

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/e6qu/intraktible/platform/comments"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// Collection holds per-recipient notifications.
const Collection = "notifications"

// snippetMax caps the stored comment excerpt.
const snippetMax = 140

// View is one inbox notification.
type View struct {
	Org            string    `json:"org"`
	Workspace      string    `json:"workspace"`
	NotificationID string    `json:"notification_id"`
	Recipient      string    `json:"recipient"`
	Kind           string    `json:"kind"` // "mention"
	SubjectType    string    `json:"subject_type"`
	SubjectID      string    `json:"subject_id"`
	Snippet        string    `json:"snippet"`
	Author         string    `json:"author"`
	Read           bool      `json:"read"`
	CreatedAt      time.Time `json:"created_at"`
	// Seq is the event-log sequence at creation — a strict monotonic tiebreaker so
	// same-instant notifications sort deterministically rather than arbitrarily.
	Seq uint64 `json:"seq"`
}

// notificationID is recipient-scoped so List prefix-scans one inbox and a recipient
// can only mark their own read (see command.MarkRead).
func notificationID(recipient, source string) string { return recipient + ":" + source }

// Projector folds comment mentions into per-recipient notifications and applies
// read marks. It reads two streams (comments + notifications); the runtime fans
// every event to every projector, so it just filters by type.
type Projector struct{}

func (Projector) Name() string          { return Collection }
func (Projector) Collections() []string { return []string{Collection} }

func (Projector) Apply(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	switch e.Type {
	case comments.TypeCommentPosted:
		return applyComment(ctx, e, s)
	case TypeMarkedRead:
		return applyRead(ctx, e, s)
	}
	return nil
}

func applyComment(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p comments.CommentPosted
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("notifications: decode comment seq %d: %w", e.Seq, err)
	}
	for _, handle := range ParseMentions(p.Body) {
		if handle == e.Actor {
			continue // don't notify yourself for your own mention
		}
		nid := notificationID(handle, p.CommentID)
		v := View{
			Org: e.Org, Workspace: e.Workspace, NotificationID: nid, Recipient: handle, Kind: "mention",
			SubjectType: p.SubjectType, SubjectID: p.SubjectID, Snippet: snippet(p.Body),
			Author: e.Actor, CreatedAt: e.Time, Seq: e.Seq,
		}
		if err := store.PutDoc(ctx, s, Collection, store.Key(e.Org, e.Workspace, nid), v); err != nil {
			return err
		}
	}
	return nil
}

func applyRead(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p MarkedRead
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("notifications: decode read seq %d: %w", e.Seq, err)
	}
	_, err := store.UpdateDoc(ctx, s, Collection, store.Key(e.Org, e.Workspace, p.NotificationID), func(v *View) {
		if v.Recipient == p.Recipient {
			v.Read = true
		}
	})
	return err // a read mark for an unknown notification is a no-op
}

func snippet(body string) string {
	if len(body) <= snippetMax {
		return body
	}
	return body[:snippetMax] + "…"
}

// List returns the caller's notifications, newest first (unread naturally surface
// via the count; ordering is by time).
func List(ctx context.Context, s store.Store, id identity.Identity) ([]View, error) {
	out, err := store.ListDocs[View](ctx, s, Collection, store.Key(id.Org, id.Workspace, id.Actor+":"))
	if err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].Seq > out[j].Seq // newest-first; tiebreak same-instant deterministically
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}
