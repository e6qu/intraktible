// SPDX-License-Identifier: AGPL-3.0-or-later

package notifications

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	cmevents "github.com/e6qu/intraktible/case-manager/events"
	deevents "github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/decision-engine/experiments"
	"github.com/e6qu/intraktible/decision-engine/monitor"
	"github.com/e6qu/intraktible/decision-engine/policy"
	"github.com/e6qu/intraktible/platform/comments"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// Collection holds per-recipient notifications.
const Collection = "notifications"

// snippetMax caps the stored comment excerpt.
const snippetMax = 140

// Kind names what produced a notification.
type Kind string

const (
	KindMention  Kind = "mention"
	KindAlert    Kind = "alert"
	KindApproval Kind = "approval"
)

// View is one inbox notification.
type View struct {
	Org            string    `json:"org"`
	Workspace      string    `json:"workspace"`
	NotificationID string    `json:"notification_id"`
	Recipient      string    `json:"recipient"`
	Kind           Kind      `json:"kind"`
	SubjectType    string    `json:"subject_type"`
	SubjectID      string    `json:"subject_id"`
	ActionID       string    `json:"action_id,omitempty"`
	Snippet        string    `json:"snippet"`
	Author         string    `json:"author"`
	Read           bool      `json:"read"`
	Resolved       bool      `json:"resolved,omitempty"`
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

func (Projector) Name() string { return Collection }
func (Projector) Collections() []string {
	return []string{Collection, caseIndexCollection, readReceiptCollection}
}

func (Projector) Apply(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	switch e.Type {
	case comments.TypeCommentPosted:
		return applyComment(ctx, e, s)
	case TypeMarkedRead:
		return applyRead(ctx, e, s)
	case cmevents.TypeReviewRequested:
		return applyReviewRequested(ctx, e, s)
	case deevents.TypeManualReviewRequested:
		return applyManualReviewRequested(ctx, e, s)
	case cmevents.TypeCaseAssigned:
		return applyCaseAssigned(ctx, e, s)
	case cmevents.TypeCaseStatusChanged:
		return applyCaseStatusChanged(ctx, e, s)
	case deevents.TypeDecisionResumed:
		return applyDecisionResumed(ctx, e, s)
	case cmevents.TypeCaseSLAReminder:
		return applySLAReminder(ctx, e, s)
	case cmevents.TypeCaseSLABreached:
		return applySLABreached(ctx, e, s)
	case monitor.TypeAlerted:
		return applyMonitorAlerted(ctx, e, s)
	case monitor.TypeResolved:
		return applyMonitorResolved(ctx, e, s)
	case deevents.TypeModelDriftAlerted:
		return applyModelDriftAlerted(ctx, e, s)
	case deevents.TypeModelDriftResolved:
		return applyModelDriftResolved(ctx, e, s)
	case deevents.TypeModelApprovalRequested:
		return applyModelApprovalRequested(ctx, e, s)
	case deevents.TypeModelApprovalApproved:
		return applyModelApprovalApproved(ctx, e, s)
	case deevents.TypeModelApprovalRejected:
		return applyModelApprovalRejected(ctx, e, s)
	case policy.TypePolicyApprovalRequested:
		return applyPolicyApprovalRequested(ctx, e, s)
	case policy.TypePolicyApprovalApproved:
		return applyPolicyApprovalApproved(ctx, e, s)
	case policy.TypePolicyApprovalRejected:
		return applyPolicyApprovalRejected(ctx, e, s)
	case deevents.TypeDeploymentRequested:
		return applyDeploymentRequested(ctx, e, s)
	case deevents.TypeDeploymentApproved:
		return applyDeploymentApproved(ctx, e, s)
	case deevents.TypeDeploymentRejected:
		return applyDeploymentRejected(ctx, e, s)
	case experiments.TypeLaunchRequested:
		return applyExperimentLaunchRequested(ctx, e, s)
	case experiments.TypeLaunchApproved:
		return applyExperimentLaunchApproved(ctx, e, s)
	case experiments.TypeLaunchRejected:
		return applyExperimentLaunchRejected(ctx, e, s)
	case deevents.TypeFlowVersionRolledBack:
		return applyFlowRolledBack(ctx, e, s)
	case deevents.TypeDeployScheduleActivated:
		return applyScheduleActivated(ctx, e, s)
	case deevents.TypeDeployScheduleReverted:
		return applyScheduleReverted(ctx, e, s)
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
			Org: e.Org, Workspace: e.Workspace, NotificationID: nid, Recipient: handle, Kind: KindMention,
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
	if p.Recipient == "" || p.NotificationID == "" ||
		len(p.NotificationID) <= len(p.Recipient) || p.NotificationID[:len(p.Recipient)+1] != p.Recipient+":" {
		return fmt.Errorf("notifications: read seq %d recipient/id mismatch", e.Seq)
	}
	underlyingID := p.NotificationID
	suffix := strings.TrimPrefix(p.NotificationID, p.Recipient+":")
	for _, queue := range []string{ReviewerQueue, OperatorQueue, ApproverQueue} {
		if strings.HasPrefix(suffix, queue+":") {
			underlyingID = suffix
			break
		}
	}
	if _, ok, err := store.GetDoc[View](ctx, s, Collection,
		store.Key(e.Org, e.Workspace, underlyingID)); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("notifications: read seq %d for unknown notification %q", e.Seq, p.NotificationID)
	}
	return store.PutDoc(ctx, s, readReceiptCollection,
		store.Key(e.Org, e.Workspace, p.NotificationID), readReceipt{ReadAt: e.Time})
}

func snippet(body string) string {
	if len(body) <= snippetMax {
		return body
	}
	return body[:snippetMax] + "…"
}

const readReceiptCollection = "notification_reads"

type readReceipt struct {
	ReadAt time.Time `json:"read_at"`
}

// Access names the shared inboxes a caller's role may see.
type Access struct {
	ReviewTasks    bool
	OperatorAlerts bool
	Approvals      bool
}

// List returns the caller's personal notifications plus authorized shared
// queues. Shared items are personalized on read so each user can mark them read
// without hiding the task/alert from everyone else.
func List(ctx context.Context, s store.Store, id identity.Identity, access Access) ([]View, error) {
	out, err := store.ListDocs[View](ctx, s, Collection, store.Key(id.Org, id.Workspace, id.Actor+":"))
	if err != nil {
		return nil, err
	}
	out = unresolved(out)
	appendQueue := func(queueName string) error {
		queue, err := store.ListDocs[View](ctx, s, Collection, store.Key(id.Org, id.Workspace, queueName+":"))
		if err != nil {
			return err
		}
		for _, v := range unresolved(queue) {
			v.NotificationID = notificationID(id.Actor, v.NotificationID)
			v.Recipient = id.Actor
			out = append(out, v)
		}
		return nil
	}
	if access.ReviewTasks {
		if err := appendQueue(ReviewerQueue); err != nil {
			return nil, err
		}
	}
	if access.OperatorAlerts {
		if err := appendQueue(OperatorQueue); err != nil {
			return nil, err
		}
	}
	if access.Approvals {
		if err := appendQueue(ApproverQueue); err != nil {
			return nil, err
		}
	}
	for i := range out {
		receipt, ok, err := store.GetDoc[readReceipt](ctx, s, readReceiptCollection,
			store.Key(id.Org, id.Workspace, out[i].NotificationID))
		if err != nil {
			return nil, err
		}
		if ok && !receipt.ReadAt.IsZero() {
			out[i].Read = true
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].Seq > out[j].Seq // newest-first; tiebreak same-instant deterministically
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}

func unresolved(in []View) []View {
	out := make([]View, 0, len(in))
	for _, v := range in {
		if !v.Resolved {
			out = append(out, v)
		}
	}
	return out
}
