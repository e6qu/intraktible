// SPDX-License-Identifier: AGPL-3.0-or-later

package comments

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// Collection holds comments, keyed so a subject's thread lists by prefix.
const Collection = "comments"

// View is one materialized comment.
type View struct {
	Org         string    `json:"org"`
	Workspace   string    `json:"workspace"`
	CommentID   string    `json:"comment_id"`
	SubjectType string    `json:"subject_type"`
	SubjectID   string    `json:"subject_id"`
	Body        string    `json:"body"`
	ParentID    string    `json:"parent_id,omitempty"`
	Author      string    `json:"author"`
	At          time.Time `json:"at"`
	// Seq is the event-log sequence — a strict monotonic tiebreaker so two comments
	// at the same wall-clock instant (coarse clocks, batch import) always sort in a
	// stable, deterministic order rather than however sort.Slice happens to land.
	Seq uint64 `json:"seq"`
}

// docID keys a comment under its subject so List can prefix-scan one thread.
func docID(subjectType, subjectID, commentID string) string {
	return subjectType + ":" + subjectID + ":" + commentID
}

func subjectPrefix(subjectType, subjectID string) string {
	return subjectType + ":" + subjectID + ":"
}

// Projector folds the comment stream into per-subject threads.
type Projector struct{}

func (Projector) Name() string          { return Collection }
func (Projector) Collections() []string { return []string{Collection} }

func (Projector) Apply(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	if e.Type != TypeCommentPosted {
		return nil
	}
	var p CommentPosted
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("comments: decode posted seq %d: %w", e.Seq, err)
	}
	v := View{
		Org: e.Org, Workspace: e.Workspace, CommentID: p.CommentID,
		SubjectType: p.SubjectType, SubjectID: p.SubjectID, Body: p.Body, ParentID: p.ParentID,
		Author: e.Actor, At: e.Time, Seq: e.Seq,
	}
	key := store.Key(e.Org, e.Workspace, docID(p.SubjectType, p.SubjectID, p.CommentID))
	return store.PutDoc(ctx, s, Collection, key, v)
}

// List returns a subject's thread, oldest first (chronological conversation).
func List(ctx context.Context, s store.Store, id identity.Identity, subjectType, subjectID string) ([]View, error) {
	prefix := store.Key(id.Org, id.Workspace, subjectPrefix(subjectType, subjectID))
	out, err := store.ListDocs[View](ctx, s, Collection, prefix)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].At.Equal(out[j].At) {
			return out[i].Seq < out[j].Seq // tiebreak same-instant comments deterministically
		}
		return out[i].At.Before(out[j].At)
	})
	return out, nil
}
