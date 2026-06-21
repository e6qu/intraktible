// SPDX-License-Identifier: AGPL-3.0-or-later

// Package history is the Decision Engine's decision-history read model: a
// projector that folds the decision event stream into per-decision records
// (request, per-node trace, and final response) for querying and node-level
// replay, mirroring the documented DecisionRecord shape.
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

// Collection is the store collection holding decision records.
const Collection = "decision_history"

// NodeRecord is one node's evaluation within a decision.
type NodeRecord struct {
	NodeID string          `json:"node_id"`
	Type   events.NodeType `json:"type"`
	Output json.RawMessage `json:"output,omitempty"`
}

// ReasonCode is one structured adverse-action reason — human-readable
// explainability (ECOA/Reg B, insurance) lifted from a decision's output.
type ReasonCode struct {
	Code        string `json:"code"`
	Description string `json:"description"`
}

// Record is the materialized history of one decision.
type Record struct {
	Org         string `json:"org"`
	Workspace   string `json:"workspace"`
	DecisionID  string `json:"decision_id"`
	FlowID      string `json:"flow_id"`
	Slug        string `json:"slug"`
	Version     int    `json:"version"`
	Environment string `json:"environment"`
	Variant     string `json:"variant,omitempty"` // champion | challenger
	Status      string `json:"status"`            // started | completed | failed
	// EntityType/EntityID identify the decision's subject (when referenced) — the
	// erasure subject under which the recorded PII is sealed.
	EntityType  string          `json:"entity_type,omitempty"`
	EntityID    string          `json:"entity_id,omitempty"`
	Data        json.RawMessage `json:"data,omitempty"`
	Output      json.RawMessage `json:"output,omitempty"`
	ReasonCodes []ReasonCode    `json:"reason_codes,omitempty"`
	// Disposition is the operational policy's outcome (approve|decline|refer) +
	// the policy that assigned it, lifted first-class onto the decision record.
	Disposition       string       `json:"disposition,omitempty"`
	DispositionCode   string       `json:"disposition_code,omitempty"`
	DispositionReason string       `json:"disposition_reason,omitempty"`
	PolicyID          string       `json:"policy_id,omitempty"`
	PolicyVersion     int          `json:"policy_version,omitempty"`
	PreApprovalID     string       `json:"preapproval_id,omitempty"`
	Error             string       `json:"error,omitempty"`
	TimeOrdered       []string     `json:"time_ordered"`
	Nodes             []NodeRecord `json:"nodes"`
	StartedAt         time.Time    `json:"started_at"`
	EndedAt           time.Time    `json:"ended_at,omitempty"`
	DurationMS        int64        `json:"duration_ms,omitempty"`
}

// Projector folds decision events into Record documents.
type Projector struct{}

// Name identifies the projector.
func (Projector) Name() string { return "decision_history" }

// Collections lists the store collection this projector owns.
func (Projector) Collections() []string { return []string{Collection} }

// Apply maintains the decision record across its lifecycle events.
func (Projector) Apply(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	switch e.Type {
	case events.TypeDecisionStarted:
		return applyStarted(ctx, e, s)
	case events.TypeNodeEvaluated:
		return applyNode(ctx, e, s)
	case events.TypeDecisionCompleted:
		return applyCompleted(ctx, e, s)
	case events.TypeDecisionFailed:
		return applyFailed(ctx, e, s)
	default:
		return nil
	}
}

// decode unmarshals an event payload into T, wrapping decode errors with the seq.
func decode[T any](e eventlog.Envelope) (T, error) {
	var p T
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return p, fmt.Errorf("decision_history: decode %s seq %d: %w", e.Type, e.Seq, err)
	}
	return p, nil
}

func applyStarted(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	p, err := decode[events.DecisionStarted](e)
	if err != nil {
		return err
	}
	r := Record{
		Org: e.Org, Workspace: e.Workspace,
		DecisionID: p.DecisionID, FlowID: p.FlowID, Slug: p.Slug,
		Version: p.Version, Environment: p.Environment, Variant: p.Variant, Status: "started",
		EntityType: p.EntityType, EntityID: p.EntityID,
		Data: p.Data, TimeOrdered: []string{}, Nodes: []NodeRecord{}, StartedAt: e.Time,
	}
	return store.PutDoc(ctx, s, Collection, store.Key(e.Org, e.Workspace, r.DecisionID), r)
}

func applyNode(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	p, err := decode[events.NodeEvaluated](e)
	if err != nil {
		return err
	}
	return update(ctx, s, e, p.DecisionID, func(r *Record) {
		r.TimeOrdered = append(r.TimeOrdered, p.NodeID)
		r.Nodes = append(r.Nodes, NodeRecord{NodeID: p.NodeID, Type: p.NodeType, Output: p.Output})
	})
}

func applyCompleted(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	p, err := decode[events.DecisionCompleted](e)
	if err != nil {
		return err
	}
	return update(ctx, s, e, p.DecisionID, func(r *Record) {
		r.Status, r.Output, r.EndedAt, r.DurationMS = "completed", p.Output, e.Time, p.DurationMS
		r.ReasonCodes = extractReasonCodes(p.Output)
		r.Disposition, r.DispositionCode, r.DispositionReason = p.Disposition, p.DispositionCode, p.DispositionReason
		r.PolicyID, r.PolicyVersion, r.PreApprovalID = p.PolicyID, p.PolicyVersion, p.PreApprovalID
	})
}

// extractReasonCodes lifts the reserved reason_codes field out of a decision's
// output into the first-class, structured field on the record.
func extractReasonCodes(output json.RawMessage) []ReasonCode {
	if len(output) == 0 {
		return nil
	}
	var wrapper struct {
		ReasonCodes []ReasonCode `json:"reason_codes"`
	}
	if err := json.Unmarshal(output, &wrapper); err != nil {
		return nil
	}
	return wrapper.ReasonCodes
}

func applyFailed(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	p, err := decode[events.DecisionFailed](e)
	if err != nil {
		return err
	}
	return update(ctx, s, e, p.DecisionID, func(r *Record) {
		r.Status, r.Error, r.EndedAt, r.DurationMS = "failed", p.Error, e.Time, p.DurationMS
	})
}

// Read returns one decision record for id's tenant.
func Read(ctx context.Context, s store.Store, id identity.Identity, decisionID string) (Record, bool, error) {
	return store.GetDoc[Record](ctx, s, Collection, store.Key(id.Org, id.Workspace, decisionID))
}

// List returns decisions for id's tenant, most recent first. DecisionID is the
// tiebreaker so two decisions recorded in the same instant (plausible under
// concurrent /decide) order identically across calls — otherwise ListPage's
// pagination could skip or duplicate a record at a page boundary.
func List(ctx context.Context, s store.Store, id identity.Identity) ([]Record, error) {
	return store.ListByTime(ctx, s, Collection, store.Key(id.Org, id.Workspace, ""),
		func(r Record) time.Time { return r.StartedAt }, func(r Record) string { return r.DecisionID }, true)
}

// Filter narrows a decision-history query. Empty string fields and zero times are
// "any"; Query matches the decision id (substring, case-insensitive).
type Filter struct {
	Slug        string
	Environment string
	Status      string
	Variant     string
	Query       string
	Since       time.Time
	Until       time.Time
	Limit       int // 0 = default page size
	Offset      int
}

// MaxPageSize caps a paginated decision-history page.
const MaxPageSize = 200

// Page is one page of decision records plus the total matching the filter.
type Page struct {
	Records []Record `json:"decisions"`
	Total   int      `json:"total"`
	Limit   int      `json:"limit"`
	Offset  int      `json:"offset"`
}

// ListPage returns the tenant's decisions matching f, newest-first, paginated.
// (The store has no field index, so it lists the tenant's records and filters in
// memory — same read cost as List; pagination bounds the response, not the scan.)
func ListPage(ctx context.Context, s store.Store, id identity.Identity, f Filter) (Page, error) {
	all, err := List(ctx, s, id)
	if err != nil {
		return Page{}, err
	}
	q := strings.ToLower(strings.TrimSpace(f.Query))
	matched := make([]Record, 0, len(all))
	for _, r := range all {
		if f.Slug != "" && r.Slug != f.Slug {
			continue
		}
		if f.Environment != "" && r.Environment != f.Environment {
			continue
		}
		if f.Status != "" && r.Status != f.Status {
			continue
		}
		if f.Variant != "" && r.Variant != f.Variant {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(r.DecisionID), q) {
			continue
		}
		if !f.Since.IsZero() && r.StartedAt.Before(f.Since) {
			continue
		}
		if !f.Until.IsZero() && r.StartedAt.After(f.Until) {
			continue
		}
		matched = append(matched, r)
	}
	total := len(matched)
	// limit <= 0 means "no pagination" — return all matches (legacy callers and the
	// dashboard, which aggregate over the full set). A positive limit paginates.
	if f.Limit <= 0 {
		return Page{Records: matched, Total: total, Limit: total, Offset: 0}, nil
	}
	limit := f.Limit
	if limit > MaxPageSize {
		limit = MaxPageSize
	}
	lo := f.Offset
	if lo < 0 {
		lo = 0
	}
	if lo > total {
		lo = total
	}
	hi := lo + limit
	if hi > total {
		hi = total
	}
	return Page{Records: matched[lo:hi], Total: total, Limit: limit, Offset: lo}, nil
}

func update(ctx context.Context, s store.Store, e eventlog.Envelope, decisionID string, mutate func(*Record)) error {
	key := store.Key(e.Org, e.Workspace, decisionID)
	r, ok, err := store.GetDoc[Record](ctx, s, Collection, key)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("decision_history: event seq %d for unknown decision %q", e.Seq, decisionID)
	}
	mutate(&r)
	return store.PutDoc(ctx, s, Collection, key, r)
}
