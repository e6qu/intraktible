// SPDX-License-Identifier: AGPL-3.0-or-later

package notifications

import (
	"context"
	"encoding/json"
	"fmt"

	cmevents "github.com/e6qu/intraktible/case-manager/events"
	deevents "github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/store"
)

// KindTask is a notification about a human-review task (a case): newly opened,
// assigned, due soon, or overdue. It pulls a reviewer to a pending decision without
// auto-resolving it — human steps still wait for the human.
const KindTask Kind = "task"

// ReviewerQueue is the recipient an UNASSIGNED task is addressed to: a shared queue any
// review-capable user sees (List includes it for them), so a task nobody owns yet still
// surfaces to someone rather than going silent.
const ReviewerQueue = "@reviewers"

// caseIndexCollection holds the projector's own minimal view of each case (assignee +
// label), so a bare SLA event — which carries only a case id — can be turned into a
// notification addressed to the right person with a readable message.
const caseIndexCollection = "notification_case_index"

type caseIndex struct {
	CaseID           string `json:"case_id"`
	Assignee         string `json:"assignee"`
	CompanyName      string `json:"company_name"`
	CaseType         string `json:"case_type"`
	SourceDecisionID string `json:"source_decision_id,omitempty"`
}

func recipientFor(idx caseIndex) string {
	if idx.Assignee != "" {
		return idx.Assignee
	}
	return ReviewerQueue
}

func label(idx caseIndex) string {
	if idx.CompanyName != "" {
		return idx.CompanyName
	}
	return idx.CaseID
}

// task writes one inbox notification. The source is suffixed (open/assigned/due_soon/
// overdue) so a case's distinct task notifications coexist rather than overwrite.
func task(ctx context.Context, e eventlog.Envelope, s store.Store, recipient, caseID, suffix, message string) error {
	nid := notificationID(recipient, caseID+":"+suffix)
	v := View{
		Org: e.Org, Workspace: e.Workspace, NotificationID: nid, Recipient: recipient, Kind: KindTask,
		SubjectType: "case", SubjectID: caseID, Snippet: message, Author: e.Actor,
		CreatedAt: e.Time, Seq: e.Seq,
	}
	return store.PutDoc(ctx, s, Collection, store.Key(e.Org, e.Workspace, nid), v)
}

func indexAndOpen(ctx context.Context, e eventlog.Envelope, s store.Store, idx caseIndex) error {
	if err := store.PutDoc(ctx, s, caseIndexCollection, store.Key(e.Org, e.Workspace, idx.CaseID), idx); err != nil {
		return err
	}
	// A fresh task nobody owns yet → the reviewer queue.
	return task(ctx, e, s, ReviewerQueue, idx.CaseID, "open",
		fmt.Sprintf("New review task: %s (%s)", label(idx), idx.CaseType))
}

func applyReviewRequested(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p cmevents.ReviewRequested
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("notifications: decode review_requested seq %d: %w", e.Seq, err)
	}
	return indexAndOpen(ctx, e, s, caseIndex{CaseID: p.CaseID, CompanyName: p.CompanyName, CaseType: p.CaseType, SourceDecisionID: p.SourceDecisionID})
}

func applyManualReviewRequested(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p deevents.ManualReviewRequested
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("notifications: decode manual_review_requested seq %d: %w", e.Seq, err)
	}
	return indexAndOpen(ctx, e, s, caseIndex{CaseID: p.CaseID, CompanyName: p.CompanyName, CaseType: p.CaseType, SourceDecisionID: p.DecisionID})
}

func applyCaseAssigned(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p cmevents.CaseAssigned
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("notifications: decode case_assigned seq %d: %w", e.Seq, err)
	}
	if _, err := store.UpdateDoc(ctx, s, caseIndexCollection, store.Key(e.Org, e.Workspace, p.CaseID),
		func(idx *caseIndex) { idx.Assignee = p.Assignee }); err != nil {
		return err
	}
	idx, _, _ := store.GetDoc[caseIndex](ctx, s, caseIndexCollection, store.Key(e.Org, e.Workspace, p.CaseID))
	idx.CaseID = p.CaseID
	return task(ctx, e, s, p.Assignee, p.CaseID, "assigned",
		fmt.Sprintf("Review task assigned to you: %s", label(idx)))
}

// applySLA turns a bare SLA event (due-soon reminder or overdue breach) into a
// notification addressed to the case's assignee, or the reviewer queue if unowned.
func applySLA(ctx context.Context, e eventlog.Envelope, s store.Store, caseID, suffix, verb string) error {
	idx, ok, err := store.GetDoc[caseIndex](ctx, s, caseIndexCollection, store.Key(e.Org, e.Workspace, caseID))
	if err != nil || !ok {
		return err
	}
	idx.CaseID = caseID
	return task(ctx, e, s, recipientFor(idx), caseID, suffix,
		fmt.Sprintf("%s: %s (%s)", verb, label(idx), idx.CaseType))
}

func applySLAReminder(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p cmevents.CaseSLAReminder
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("notifications: decode sla_reminder seq %d: %w", e.Seq, err)
	}
	return applySLA(ctx, e, s, p.CaseID, "due_soon", "Review task due soon")
}

func applySLABreached(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p cmevents.CaseSLABreached
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("notifications: decode sla_breached seq %d: %w", e.Seq, err)
	}
	return applySLA(ctx, e, s, p.CaseID, "overdue", "Review task OVERDUE")
}
