// SPDX-License-Identifier: AGPL-3.0-or-later

package comments

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
)

// maxBody caps a single comment to keep the thread readable and the log tidy.
const maxBody = 4000

// Handler is the comments write side (imperative shell).
type Handler struct {
	log   eventlog.Log
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
		panic("comments: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// Subject identifies what a comment thread is attached to (a decision, case,
// deployment request, …). Grouping the type and id into one value means the pair
// can't be transposed at a call site — a (type, id) string pair silently could.
type Subject struct {
	Type string
	ID   string
}

// parentIsTopLevel folds the comment stream to verify parentID names an existing
// top-level comment (no ParentID of its own) on the same subject.
func (h *Handler) parentIsTopLevel(ctx context.Context, id identity.Identity, subject Subject, parentID string) (bool, error) {
	evs, err := h.log.Read(ctx, 0)
	if err != nil {
		return false, fmt.Errorf("comments: read log: %w", err)
	}
	for _, e := range evs {
		if e.Stream != StreamComments || e.Type != TypeCommentPosted || e.Org != id.Org || e.Workspace != id.Workspace {
			continue
		}
		var p CommentPosted
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			continue
		}
		if p.CommentID == parentID {
			return p.SubjectType == subject.Type && p.SubjectID == subject.ID && p.ParentID == "", nil
		}
	}
	return false, nil // unknown parent
}

// Post appends a comment to the subject's thread. parentID, when set, marks the
// comment as a reply to another comment.
func (h *Handler) Post(ctx context.Context, id identity.Identity, subject Subject, body, parentID string) (string, eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return "", eventlog.Envelope{}, err
	}
	if subject.Type == "" || subject.ID == "" {
		return "", eventlog.Envelope{}, fmt.Errorf("comments: subject_type and subject_id are required")
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return "", eventlog.Envelope{}, fmt.Errorf("comments: comment body is required")
	}
	if len(body) > maxBody {
		return "", eventlog.Envelope{}, fmt.Errorf("comments: comment too long (%d > %d)", len(body), maxBody)
	}
	// A reply's parent must be a real, top-level comment on the SAME subject —
	// otherwise an arbitrary parent_id records an orphan or cross-thread reply, and
	// replies-to-replies break the one-level threading the UI assumes.
	if parentID != "" {
		ok, err := h.parentIsTopLevel(ctx, id, subject, parentID)
		if err != nil {
			return "", eventlog.Envelope{}, err
		}
		if !ok {
			return "", eventlog.Envelope{}, fmt.Errorf("comments: parent %q is not a top-level comment on this subject", parentID)
		}
	}
	cid := h.newID()
	b, err := json.Marshal(CommentPosted{CommentID: cid, SubjectType: subject.Type, SubjectID: subject.ID, Body: body, ParentID: parentID})
	if err != nil {
		return "", eventlog.Envelope{}, fmt.Errorf("comments: marshal comment: %w", err)
	}
	e, err := h.log.Append(ctx, eventlog.Envelope{
		Org: id.Org, Workspace: id.Workspace, Actor: id.Actor,
		Stream: StreamComments, Type: TypeCommentPosted, Time: h.now(), Payload: b,
	})
	if err != nil {
		return "", eventlog.Envelope{}, err
	}
	return cid, e, nil
}
