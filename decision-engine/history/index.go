// SPDX-License-Identifier: AGPL-3.0-or-later

package history

import (
	"context"
	"encoding/json"
	"fmt"
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
	Org               string            `json:"org"`
	Workspace         string            `json:"workspace"`
	DecisionID        string            `json:"decision_id"`
	Slug              string            `json:"slug"`
	Environment       string            `json:"environment"`
	Variant           string            `json:"variant,omitempty"`
	ExperimentID      string            `json:"experiment_id,omitempty"`
	ExperimentCohort  int               `json:"experiment_cohort,omitempty"`
	ExperimentArm     string            `json:"experiment_arm,omitempty"`
	Status            string            `json:"status"`
	EntityType        string            `json:"entity_type,omitempty"`
	EntityID          string            `json:"entity_id,omitempty"`
	BusinessReference string            `json:"business_reference,omitempty"`
	CorrelationID     string            `json:"correlation_id,omitempty"`
	Metadata          map[string]string `json:"metadata,omitempty"`
	RecoveryAttempt   int               `json:"recovery_attempt,omitempty"`
	RecoveryAttempts  int               `json:"recovery_attempts,omitempty"`
	RecoveryOwner     string            `json:"recovery_owner,omitempty"`
	LastRecoveryError string            `json:"last_recovery_error,omitempty"`
	StartedAt         time.Time         `json:"started_at"`
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
		metadata, err := indexMetadata(p.Metadata)
		if err != nil {
			return fmt.Errorf("decision history index: decision %q metadata: %w", p.DecisionID, err)
		}
		return store.PutDoc(ctx, s, IndexCollection, indexKey(e.Org, e.Workspace, p.DecisionID), IndexEntry{
			Org: e.Org, Workspace: e.Workspace, DecisionID: p.DecisionID,
			Slug: p.Slug, Environment: p.Environment, Variant: p.Variant,
			ExperimentID: p.ExperimentID, ExperimentCohort: p.ExperimentCohort,
			ExperimentArm: p.ExperimentArm,
			EntityType:    p.EntityType, EntityID: p.EntityID,
			BusinessReference: p.BusinessReference, CorrelationID: p.CorrelationID,
			Metadata: metadata,
			Status:   "running", StartedAt: e.Time,
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
	case events.TypeDecisionAbandoned:
		p, err := decode[events.DecisionAbandoned](e)
		if err != nil {
			return err
		}
		return updateIndex(ctx, s, e, p.DecisionID, func(entry *IndexEntry) {
			entry.Status, entry.RecoveryAttempt = "abandoned", p.Attempt
			entry.LastRecoveryError, entry.RecoveryOwner = p.Error, ""
		})
	case events.TypeRecoveryClaimed:
		p, err := decode[events.DecisionRecoveryClaimed](e)
		if err != nil {
			return err
		}
		return updateIndex(ctx, s, e, p.DecisionID, func(entry *IndexEntry) {
			if entry.Status == "running" || entry.Status == "retrying" {
				entry.Status = "retrying"
			}
			entry.RecoveryAttempt, entry.RecoveryOwner = p.Attempt, p.Owner
			entry.RecoveryAttempts++
			entry.LastRecoveryError = p.PreviousErr
		})
	case events.TypeExecutionInterrupted:
		p, err := decode[events.DecisionExecutionInterrupted](e)
		if err != nil {
			return err
		}
		return updateIndex(ctx, s, e, p.DecisionID, func(entry *IndexEntry) {
			if entry.Status == "running" {
				entry.Status = "retrying"
			}
			entry.LastRecoveryError = p.Error
		})
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
		return indexStatus(ctx, s, e, p.DecisionID, "running")
	default:
		return nil
	}
}

func updateIndex(
	ctx context.Context,
	s store.Store,
	e eventlog.Envelope,
	decisionID string,
	mutate func(*IndexEntry),
) error {
	key := indexKey(e.Org, e.Workspace, decisionID)
	entry, ok, err := store.GetDoc[IndexEntry](ctx, s, IndexCollection, key)
	if err != nil || !ok {
		return err
	}
	mutate(&entry)
	return store.PutDoc(ctx, s, IndexCollection, key, entry)
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
	case f.EntityType != "" && e.EntityType != f.EntityType:
		return false
	case f.EntityID != "" && e.EntityID != f.EntityID:
		return false
	case f.BusinessReference != "" && e.BusinessReference != f.BusinessReference:
		return false
	case f.CorrelationID != "" && e.CorrelationID != f.CorrelationID:
		return false
	case !metadataMatches(e.Metadata, f.Metadata):
		return false
	case !f.Since.IsZero() && e.StartedAt.Before(f.Since):
		return false
	case !f.Until.IsZero() && e.StartedAt.After(f.Until):
		return false
	case f.query() != "" &&
		!containsFold(e.DecisionID, f.query()) &&
		!containsFold(e.BusinessReference, f.query()) &&
		!containsFold(e.CorrelationID, f.query()) &&
		!containsFold(e.EntityID, f.query()):
		return false
	default:
		return true
	}
}

// indexMetadata retains the top-level scalar subset promised by the history
// query contract. Nested objects and arrays remain in the full decision record,
// but are deliberately not treated as indexed values.
func indexMetadata(raw json.RawMessage) (map[string]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	indexed := make(map[string]string)
	for key, value := range fields {
		value = json.RawMessage(strings.TrimSpace(string(value)))
		if len(value) == 0 {
			return nil, fmt.Errorf("field %q has an empty JSON value", key)
		}
		switch value[0] {
		case '{', '[':
			continue
		case '"':
			var text string
			if err := json.Unmarshal(value, &text); err != nil {
				return nil, fmt.Errorf("field %q: %w", key, err)
			}
			indexed[key] = text
		default:
			var scalar any
			if err := json.Unmarshal(value, &scalar); err != nil {
				return nil, fmt.Errorf("field %q: %w", key, err)
			}
			if scalar == nil {
				indexed[key] = "null"
			} else {
				indexed[key] = string(value)
			}
		}
	}
	if len(indexed) == 0 {
		return nil, nil
	}
	return indexed, nil
}

func metadataMatches(indexed, wanted map[string]string) bool {
	for key, value := range wanted {
		if indexed[key] != value {
			return false
		}
	}
	return true
}

// ExecutionAttention is one recent decision that is still active, under
// recovery, or stopped for manual reconciliation.
type ExecutionAttention struct {
	DecisionID        string `json:"decision_id"`
	Slug              string `json:"slug"`
	Status            string `json:"status"`
	RecoveryAttempt   int    `json:"recovery_attempt,omitempty"`
	RecoveryOwner     string `json:"recovery_owner,omitempty"`
	LastRecoveryError string `json:"last_recovery_error,omitempty"`
}

// ExecutionSummary is the lightweight operator roll-up for durable decisions.
type ExecutionSummary struct {
	Total            int                  `json:"total"`
	Running          int                  `json:"running"`
	Retrying         int                  `json:"retrying"`
	Suspended        int                  `json:"suspended"`
	Completed        int                  `json:"completed"`
	Failed           int                  `json:"failed"`
	Abandoned        int                  `json:"abandoned"`
	RecoveryAttempts int                  `json:"recovery_attempts"`
	Attention        []ExecutionAttention `json:"attention"`
}

// SummarizeExecution scans only the compact decision index. Attention is capped
// newest-first so the operator page stays bounded even when a tenant retains a
// long history of manually reconciled decisions.
func SummarizeExecution(ctx context.Context, s store.Store, id identity.Identity) (ExecutionSummary, error) {
	entries, err := listIndex(ctx, s, id, Filter{})
	if err != nil {
		return ExecutionSummary{}, err
	}
	summary := ExecutionSummary{Total: len(entries), Attention: []ExecutionAttention{}}
	for _, entry := range entries {
		summary.RecoveryAttempts += entry.RecoveryAttempts
		switch entry.Status {
		case "running":
			summary.Running++
		case "retrying":
			summary.Retrying++
		case "suspended":
			summary.Suspended++
		case "completed":
			summary.Completed++
		case "failed":
			summary.Failed++
		case "abandoned":
			summary.Abandoned++
		default:
			return ExecutionSummary{}, fmt.Errorf(
				"decision history index: decision %q has unknown status %q",
				entry.DecisionID, entry.Status,
			)
		}
		if len(summary.Attention) < 10 &&
			(entry.Status == "running" || entry.Status == "retrying" || entry.Status == "abandoned") {
			summary.Attention = append(summary.Attention, ExecutionAttention{
				DecisionID: entry.DecisionID, Slug: entry.Slug, Status: entry.Status,
				RecoveryAttempt: entry.RecoveryAttempt, RecoveryOwner: entry.RecoveryOwner,
				LastRecoveryError: entry.LastRecoveryError,
			})
		}
	}
	return summary, nil
}
