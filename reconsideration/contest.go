// SPDX-License-Identifier: AGPL-3.0-or-later

package reconsideration

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// TypeContestReceived records that a data subject contested an automated decision —
// exercising the right to contest under Article 22(3) of the EU General Data Protection
// Regulation, or requesting reconsideration under the US Equal Credit Opportunity Act.
// It opens the contest; recording a human review (TypeRecorded) for the same decision
// resolves it. The two together are the request → outcome pair the reconsideration
// record alone did not capture.
const TypeContestReceived = "reconsideration.contest_received"

// ContestCollection holds one contest record per decision.
const ContestCollection = "reconsideration_contest"

// Channel is how the subject lodged the contest. A named type so an unrecognized
// channel is rejected at the boundary rather than stored as free text.
type Channel string

const (
	ChannelPhone        Channel = "phone"
	ChannelLetter       Channel = "letter"
	ChannelEmail        Channel = "email"
	ChannelOnlinePortal Channel = "online_portal"
	ChannelInPerson     Channel = "in_person"
)

// Valid reports whether c is a recognized channel.
func (c Channel) Valid() bool {
	switch c {
	case ChannelPhone, ChannelLetter, ChannelEmail, ChannelOnlinePortal, ChannelInPerson:
		return true
	default:
		return false
	}
}

// ContestReceived is the event opening a contest.
type ContestReceived struct {
	DecisionID string  `json:"decision_id"`
	Subject    string  `json:"subject,omitempty"`
	Channel    Channel `json:"channel"`
	Note       string  `json:"note,omitempty"`
}

// ContestCmd is the input to RecordContest.
type ContestCmd struct {
	DecisionID string
	Subject    string
	Channel    Channel
	Note       string
}

// RecordContest logs that a subject contested a decision. The decision's eligibility is
// checked by the service (it holds the store); this records the fact, re-checking the
// channel so a malformed contest never reaches the log.
func (h *Handler) RecordContest(ctx context.Context, id identity.Identity, cmd ContestCmd) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	cmd.DecisionID = strings.TrimSpace(cmd.DecisionID)
	if cmd.DecisionID == "" {
		return eventlog.Envelope{}, fmt.Errorf("reconsideration: decision_id is required")
	}
	if !cmd.Channel.Valid() {
		return eventlog.Envelope{}, fmt.Errorf("reconsideration: unknown contest channel %q", cmd.Channel)
	}
	b, err := json.Marshal(ContestReceived{
		DecisionID: cmd.DecisionID, Subject: cmd.Subject, Channel: cmd.Channel, Note: strings.TrimSpace(cmd.Note),
	})
	if err != nil {
		return eventlog.Envelope{}, fmt.Errorf("reconsideration: marshal contest: %w", err)
	}
	return h.log.Append(ctx, eventlog.Envelope{
		Org: id.Org, Workspace: id.Workspace, Actor: id.Actor,
		Stream: Stream, Type: TypeContestReceived, Time: h.now(), Payload: b,
	})
}

// Contest is the stored state of a decision's contest.
type Contest struct {
	Org        string     `json:"org"`
	Workspace  string     `json:"workspace"`
	DecisionID string     `json:"decision_id"`
	Subject    string     `json:"subject,omitempty"`
	Channel    Channel    `json:"channel"`
	Note       string     `json:"note,omitempty"`
	ReceivedAt time.Time  `json:"received_at"`
	ReceivedBy string     `json:"received_by"`
	Resolved   bool       `json:"resolved"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
}

// ContestProjector folds contests and their resolving reviews into a per-decision
// contest record. It owns its own collection; a review (TypeRecorded) resolves a
// contest if one is open, but never creates one.
type ContestProjector struct{}

// Name identifies the projector.
func (ContestProjector) Name() string { return ContestCollection }

// Collections lists the store collection this projector owns.
func (ContestProjector) Collections() []string { return []string{ContestCollection} }

// Apply opens a contest on ContestReceived and resolves it on a recorded human review.
func (ContestProjector) Apply(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	switch e.Type {
	case TypeContestReceived:
		p, err := decode[ContestReceived](e)
		if err != nil {
			return err
		}
		c := Contest{
			Org: e.Org, Workspace: e.Workspace, DecisionID: p.DecisionID, Subject: p.Subject,
			Channel: p.Channel, Note: p.Note, ReceivedAt: e.Time, ReceivedBy: e.Actor,
		}
		return store.PutDoc(ctx, s, ContestCollection, store.Key(e.Org, e.Workspace, p.DecisionID), c)
	case TypeRecorded:
		p, err := decode[Recorded](e)
		if err != nil {
			return err
		}
		c, ok, err := store.GetDoc[Contest](ctx, s, ContestCollection, store.Key(e.Org, e.Workspace, p.DecisionID))
		if err != nil || !ok {
			return err // no open contest for this decision (a proactive review) — nothing to resolve
		}
		at := e.Time
		c.Resolved, c.ResolvedAt = true, &at
		return store.PutDoc(ctx, s, ContestCollection, store.Key(e.Org, e.Workspace, p.DecisionID), c)
	default:
		return nil
	}
}

// ReadContest returns the contest for a decision (false when none exists).
func ReadContest(ctx context.Context, s store.Store, id identity.Identity, decisionID string) (Contest, bool, error) {
	return store.GetDoc[Contest](ctx, s, ContestCollection, store.Key(id.Org, id.Workspace, decisionID))
}

// ListContests returns the tenant's contests, most recently received first.
func ListContests(ctx context.Context, s store.Store, id identity.Identity) ([]Contest, error) {
	return store.ListByTime(ctx, s, ContestCollection, store.Key(id.Org, id.Workspace, ""),
		func(c Contest) time.Time { return c.ReceivedAt }, func(c Contest) string { return c.DecisionID }, true)
}
