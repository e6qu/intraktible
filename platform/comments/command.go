// SPDX-License-Identifier: AGPL-3.0-or-later

package comments

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
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

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Post appends a comment to the (subjectType, subjectID) thread.
func (h *Handler) Post(ctx context.Context, id identity.Identity, subjectType, subjectID, body string) (string, eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return "", eventlog.Envelope{}, err
	}
	if subjectType == "" || subjectID == "" {
		return "", eventlog.Envelope{}, fmt.Errorf("comments: subject_type and subject_id are required")
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return "", eventlog.Envelope{}, fmt.Errorf("comments: comment body is required")
	}
	if len(body) > maxBody {
		return "", eventlog.Envelope{}, fmt.Errorf("comments: comment too long (%d > %d)", len(body), maxBody)
	}
	cid := h.newID()
	b, err := json.Marshal(CommentPosted{CommentID: cid, SubjectType: subjectType, SubjectID: subjectID, Body: body})
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
