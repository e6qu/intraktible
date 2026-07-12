// SPDX-License-Identifier: AGPL-3.0-or-later

package history

import (
	"context"
	"strings"
	"time"

	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// IndexCollection holds the lightweight decision-index entries. A decision's full
// Record carries the input, per-node trace, and output — expensive to load in bulk.
// The index is a small per-decision summary (the fields ListPage filters and sorts
// by), so a paginated list scans these instead of every full record and then loads
// full records only for the page it returns. Generalizes the audit-index pattern.
const IndexCollection = "decision_history_index"

// IndexEntry is one decision's list summary.
type IndexEntry struct {
	Org         string    `json:"org"`
	Workspace   string    `json:"workspace"`
	DecisionID  string    `json:"decision_id"`
	Slug        string    `json:"slug"`
	Environment string    `json:"environment"`
	Variant     string    `json:"variant,omitempty"`
	Status      string    `json:"status"`
	StartedAt   time.Time `json:"started_at"`
}

// applyIndex maintains a decision's index entry across its lifecycle. The full-record
// Projector calls it for every event (it owns IndexCollection too), so the index and
// the record advance in lock-step — registering the one Projector always yields a
// working paginated list. Node and manual-review events don't change any indexed
// field, so they are ignored. Keyed by decision id (stable), so a status transition
// overwrites the entry in place.
func applyIndex(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	switch e.Type {
	case events.TypeDecisionStarted:
		p, err := decode[events.DecisionStarted](e)
		if err != nil {
			return err
		}
		return store.PutDoc(ctx, s, IndexCollection, indexKey(e.Org, e.Workspace, p.DecisionID), IndexEntry{
			Org: e.Org, Workspace: e.Workspace, DecisionID: p.DecisionID,
			Slug: p.Slug, Environment: p.Environment, Variant: p.Variant,
			Status: "started", StartedAt: e.Time,
		})
	case events.TypeDecisionCompleted:
		p, err := decode[events.DecisionCompleted](e)
		if err != nil {
			return err
		}
		return indexStatus(ctx, s, e, p.DecisionID, "completed")
	case events.TypeDecisionFailed:
		p, err := decode[events.DecisionFailed](e)
		if err != nil {
			return err
		}
		return indexStatus(ctx, s, e, p.DecisionID, "failed")
	case events.TypeDecisionSuspended:
		p, err := decode[events.DecisionSuspended](e)
		if err != nil {
			return err
		}
		return indexStatus(ctx, s, e, p.DecisionID, "suspended")
	case events.TypeDecisionResumed:
		p, err := decode[events.DecisionResumed](e)
		if err != nil {
			return err
		}
		return indexStatus(ctx, s, e, p.DecisionID, "started")
	default:
		return nil
	}
}

// indexStatus updates an entry's status. A missing entry (a status event with no
// preceding Started in this rebuild window) is a no-op — the Started event is the
// source of the other fields and always precedes a status change in the same stream.
func indexStatus(ctx context.Context, s store.Store, e eventlog.Envelope, decisionID, status string) error {
	key := indexKey(e.Org, e.Workspace, decisionID)
	entry, ok, err := store.GetDoc[IndexEntry](ctx, s, IndexCollection, key)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	entry.Status = status
	return store.PutDoc(ctx, s, IndexCollection, key, entry)
}

func indexKey(org, workspace, decisionID string) string {
	return store.Key(org, workspace, decisionID)
}

// containsFold reports whether s contains sub, case-insensitively. sub is expected
// already-lowercased (Filter.query normalizes it).
func containsFold(s, sub string) bool {
	return strings.Contains(strings.ToLower(s), sub)
}

// listIndex reads the tenant's index entries (a tenant-prefixed scan of small
// summaries, not the full records) matching f, newest-first.
func listIndex(ctx context.Context, s store.Store, id identity.Identity, f Filter) ([]IndexEntry, error) {
	return store.QueryDocs(ctx, s, IndexCollection, store.Key(id.Org, id.Workspace, ""),
		func(e IndexEntry) bool { return indexMatch(e, f) },
		// Newest-first, decision id as a stable tiebreaker (mirrors List).
		func(a, b IndexEntry) bool {
			if !a.StartedAt.Equal(b.StartedAt) {
				return a.StartedAt.After(b.StartedAt)
			}
			return a.DecisionID > b.DecisionID
		})
}

func indexMatch(e IndexEntry, f Filter) bool {
	switch {
	case f.Slug != "" && e.Slug != f.Slug:
		return false
	case f.Environment != "" && e.Environment != f.Environment:
		return false
	case f.Status != "" && e.Status != f.Status:
		return false
	case f.Variant != "" && e.Variant != f.Variant:
		return false
	case !f.Since.IsZero() && e.StartedAt.Before(f.Since):
		return false
	case !f.Until.IsZero() && e.StartedAt.After(f.Until):
		return false
	case f.query() != "" && !containsFold(e.DecisionID, f.query()):
		return false
	default:
		return true
	}
}
