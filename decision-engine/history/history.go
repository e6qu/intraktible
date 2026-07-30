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

// EffectRecord is one imperative operation requested by the pure graph
// interpreter. It separates provider attempts from node evaluation so an operator
// can see whether I/O was requested, completed, or failed.
type EffectRecord struct {
	EffectID        string          `json:"effect_id"`
	Scope           string          `json:"scope"`
	NodeID          string          `json:"node_id"`
	Kind            string          `json:"kind"`
	Reference       string          `json:"reference"`
	ProviderVersion int             `json:"provider_version,omitempty"`
	OutputKey       string          `json:"output_key"`
	InputHash       string          `json:"input_hash"`
	Attempt         int             `json:"attempt"`
	Delivery        string          `json:"delivery"`
	Status          string          `json:"status"` // requested | succeeded | failed
	Output          json.RawMessage `json:"output,omitempty"`
	Error           string          `json:"error,omitempty"`
	RequestedAt     time.Time       `json:"requested_at"`
	EndedAt         time.Time       `json:"ended_at,omitempty"`
	DurationMS      int64           `json:"duration_ms,omitempty"`
}

// ReasonCode is one structured adverse-action reason — human-readable
// explainability (ECOA/Reg B, insurance) lifted from a decision's output.
type ReasonCode struct {
	Code        string `json:"code"`
	Description string `json:"description"`
}

// Record is the materialized history of one decision.
type Record struct {
	Org                   string `json:"org"`
	Workspace             string `json:"workspace"`
	DecisionID            string `json:"decision_id"`
	Generation            int    `json:"generation"`
	FlowID                string `json:"flow_id"`
	Slug                  string `json:"slug"`
	Version               int    `json:"version"`
	Environment           string `json:"environment"`
	Variant               string `json:"variant,omitempty"` // champion | challenger
	ExperimentID          string `json:"experiment_id,omitempty"`
	ExperimentCohort      int    `json:"experiment_cohort,omitempty"`
	ExperimentArm         string `json:"experiment_arm,omitempty"`
	ExperimentArmName     string `json:"experiment_arm_name,omitempty"`
	ExperimentSubjectHash string `json:"experiment_subject_hash,omitempty"`
	Status                string `json:"status"` // running | retrying | suspended | completed | failed | abandoned
	// EntityType/EntityID identify the decision's subject (when referenced) — the
	// erasure subject under which the recorded PII is sealed.
	EntityType         string                  `json:"entity_type,omitempty"`
	EntityID           string                  `json:"entity_id,omitempty"`
	Data               json.RawMessage         `json:"data,omitempty"`
	Output             json.RawMessage         `json:"output,omitempty"`
	IdempotencyKeyHash string                  `json:"idempotency_key_hash,omitempty"`
	RequestHash        string                  `json:"request_hash,omitempty"`
	BusinessReference  string                  `json:"business_reference,omitempty"`
	CorrelationID      string                  `json:"correlation_id,omitempty"`
	Metadata           json.RawMessage         `json:"metadata,omitempty"`
	Control            events.ExecutionControl `json:"control,omitempty"`
	// Set while Status is "suspended": the human-task node it paused at and the
	// captured instance state needed to resume (cleared on resume).
	SuspendNode  string          `json:"suspend_node,omitempty"`
	SuspendState json.RawMessage `json:"suspend_state,omitempty"`
	ReasonCodes  []ReasonCode    `json:"reason_codes,omitempty"`
	// Disposition is the operational policy's outcome (approve|decline|refer) +
	// the policy that assigned it, lifted first-class onto the decision record.
	Disposition             string `json:"disposition,omitempty"`
	DispositionCode         string `json:"disposition_code,omitempty"`
	DispositionReason       string `json:"disposition_reason,omitempty"`
	PolicyID                string `json:"policy_id,omitempty"`
	PolicyVersion           int    `json:"policy_version,omitempty"`
	PolicySelectionRecorded bool   `json:"policy_selection_recorded,omitempty"`
	PreApprovalID           string `json:"preapproval_id,omitempty"`
	// CaseID links a decision that routed to manual_review to the case it opened,
	// populated from the decision's ManualReviewRequested escalation event.
	CaseID string `json:"case_id,omitempty"`
	// HumanReviewed is set once a suspended decision is resumed by a person — the
	// durable signal that a human was in the loop, so a "solely automated" decision
	// (the Art. 22 / reconsideration predicate) is distinguishable after the fact.
	HumanReviewed      bool           `json:"human_reviewed,omitempty"`
	Error              string         `json:"error,omitempty"`
	RecoveryAttempt    int            `json:"recovery_attempt,omitempty"`
	RecoveryOwner      string         `json:"recovery_owner,omitempty"`
	RecoveryLeaseUntil time.Time      `json:"recovery_lease_until,omitempty"`
	RecoveryAfter      time.Time      `json:"recovery_after,omitempty"`
	LastRecoveryError  string         `json:"last_recovery_error,omitempty"`
	TimeOrdered        []string       `json:"time_ordered"`
	Nodes              []NodeRecord   `json:"nodes"`
	Effects            []EffectRecord `json:"effects,omitempty"`
	StartedAt          time.Time      `json:"started_at"`
	EndedAt            time.Time      `json:"ended_at,omitempty"`
	DurationMS         int64          `json:"duration_ms,omitempty"`
}

// Projector folds decision events into Record documents.
type Projector struct{}

// Name identifies the projector.
func (Projector) Name() string { return "decision_history" }

// Collections lists the store collections this projector owns: the full records and
// the lightweight list index. It owns both so registering this one projector always
// yields a working paginated list (no separate index projector to forget).
func (Projector) Collections() []string { return []string{Collection, IndexCollection} }

// Apply maintains the decision record AND its list-index entry across the decision's
// lifecycle events. Both advance in the same apply, so the index can never drift from
// the records.
func (Projector) Apply(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	if err := applyRecord(ctx, e, s); err != nil {
		return err
	}
	return applyIndex(ctx, e, s)
}

func applyRecord(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	switch e.Type {
	case events.TypeDecisionStarted:
		return applyStarted(ctx, e, s)
	case events.TypeContextPrepared:
		return applyContextPrepared(ctx, e, s)
	case events.TypeNodeEvaluated:
		return applyNode(ctx, e, s)
	case events.TypeEffectRequested:
		return applyEffectRequested(ctx, e, s)
	case events.TypeEffectSucceeded:
		return applyEffectSucceeded(ctx, e, s)
	case events.TypeEffectFailed:
		return applyEffectFailed(ctx, e, s)
	case events.TypeDecisionCompleted:
		return applyCompleted(ctx, e, s)
	case events.TypeDecisionFailed:
		return applyFailed(ctx, e, s)
	case events.TypeDecisionAbandoned:
		return applyAbandoned(ctx, e, s)
	case events.TypeExecutionInterrupted:
		return applyExecutionInterrupted(ctx, e, s)
	case events.TypeRecoveryClaimed:
		return applyRecoveryClaimed(ctx, e, s)
	case events.TypeRecoveryHeartbeat:
		return applyRecoveryHeartbeat(ctx, e, s)
	case events.TypeDecisionSuspended:
		return applySuspended(ctx, e, s)
	case events.TypeDecisionResumed:
		return applyResumed(ctx, e, s)
	case events.TypeManualReviewRequested:
		return applyManualReview(ctx, e, s)
	default:
		return nil
	}
}

func applyRecoveryClaimed(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	p, err := decode[events.DecisionRecoveryClaimed](e)
	if err != nil {
		return err
	}
	return update(ctx, s, e, p.DecisionID, func(r *Record) {
		if r.Status == "running" || r.Status == "retrying" {
			r.Status = "retrying"
		}
		r.RecoveryAttempt, r.RecoveryOwner = p.Attempt, p.Owner
		r.RecoveryLeaseUntil, r.LastRecoveryError = p.LeaseUntil, p.PreviousErr
	})
}

func applyExecutionInterrupted(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	p, err := decode[events.DecisionExecutionInterrupted](e)
	if err != nil {
		return err
	}
	return update(ctx, s, e, p.DecisionID, func(r *Record) {
		if r.Status == "running" {
			r.Status = "retrying"
		}
		r.RecoveryAfter = time.Time{}
		r.LastRecoveryError = p.Error
	})
}

func applyRecoveryHeartbeat(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	p, err := decode[events.DecisionRecoveryHeartbeat](e)
	if err != nil {
		return err
	}
	return update(ctx, s, e, p.DecisionID, func(r *Record) {
		if r.RecoveryOwner == p.Owner && r.RecoveryAttempt == p.Attempt {
			r.RecoveryLeaseUntil = p.LeaseUntil
		}
	})
}

func applyContextPrepared(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	p, err := decode[events.DecisionContextPrepared](e)
	if err != nil {
		return err
	}
	return update(ctx, s, e, p.DecisionID, func(r *Record) {
		r.Data = p.Data
	})
}

func applyEffectRequested(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	p, err := decode[events.DecisionEffectRequested](e)
	if err != nil {
		return err
	}
	return update(ctx, s, e, p.DecisionID, func(r *Record) {
		r.Effects = append(r.Effects, EffectRecord{
			EffectID: p.EffectID, Scope: p.Scope, NodeID: p.NodeID, Kind: p.Kind,
			Reference: p.Reference, ProviderVersion: p.ProviderVersion,
			OutputKey: p.Output, InputHash: p.InputHash, Attempt: p.Attempt,
			Delivery: p.Delivery, Status: "requested", RequestedAt: e.Time,
		})
	})
}

func applyEffectSucceeded(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	p, err := decode[events.DecisionEffectSucceeded](e)
	if err != nil {
		return err
	}
	return updateEffect(ctx, e, s, p.DecisionID, p.EffectID, p.Attempt, func(effect *EffectRecord) {
		effect.Status, effect.Output, effect.EndedAt, effect.DurationMS = "succeeded", p.Output, e.Time, p.DurationMS
	})
}

func applyEffectFailed(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	p, err := decode[events.DecisionEffectFailed](e)
	if err != nil {
		return err
	}
	return updateEffect(ctx, e, s, p.DecisionID, p.EffectID, p.Attempt, func(effect *EffectRecord) {
		effect.Status, effect.Error, effect.EndedAt, effect.DurationMS = "failed", p.Error, e.Time, p.DurationMS
	})
}

func updateEffect(
	ctx context.Context,
	e eventlog.Envelope,
	s store.Store,
	decisionID, effectID string,
	attempt int,
	mutate func(*EffectRecord),
) error {
	key := store.Key(e.Org, e.Workspace, decisionID)
	record, ok, err := store.GetDoc[Record](ctx, s, Collection, key)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("decision_history: effect event seq %d for unknown decision %q", e.Seq, decisionID)
	}
	for i := len(record.Effects) - 1; i >= 0; i-- {
		effect := &record.Effects[i]
		if effect.EffectID == effectID && effect.Attempt == attempt {
			if effect.Status != "requested" {
				return fmt.Errorf(
					"decision_history: effect %q attempt %d is already %s",
					effectID, attempt, effect.Status,
				)
			}
			mutate(effect)
			return store.PutDoc(ctx, s, Collection, key, record)
		}
	}
	return fmt.Errorf(
		"decision_history: effect completion seq %d has no request for %q attempt %d",
		e.Seq, effectID, attempt,
	)
}

func applySuspended(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	p, err := decode[events.DecisionSuspended](e)
	if err != nil {
		return err
	}
	return update(ctx, s, e, p.DecisionID, func(r *Record) {
		r.Status = "suspended"
		r.RecoveryAfter, r.RecoveryLeaseUntil = time.Time{}, time.Time{}
		r.RecoveryOwner = ""
		r.SuspendNode = p.NodeID
		r.SuspendState = p.State
		if p.CaseID != "" {
			r.CaseID = p.CaseID
		}
	})
}

func applyResumed(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	p, err := decode[events.DecisionResumed](e)
	if err != nil {
		return err
	}
	// Back to running; the following DecisionCompleted/Failed sets the terminal status.
	return update(ctx, s, e, p.DecisionID, func(r *Record) {
		r.Status = "running"
		r.SuspendState = nil
		r.HumanReviewed = true
		r.Generation++
		r.RecoveryAfter = p.RecoveryAfter
	})
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
		DecisionID: p.DecisionID, Generation: 1, FlowID: p.FlowID, Slug: p.Slug,
		Version: p.Version, Environment: p.Environment, Variant: p.Variant, Status: "running",
		ExperimentID: p.ExperimentID, ExperimentCohort: p.ExperimentCohort,
		ExperimentArm: p.ExperimentArm, ExperimentArmName: p.ExperimentArmName,
		ExperimentSubjectHash: p.ExperimentSubjectHash,
		EntityType:            p.EntityType, EntityID: p.EntityID,
		IdempotencyKeyHash: p.IdempotencyKeyHash, RequestHash: p.RequestHash,
		BusinessReference: p.BusinessReference, CorrelationID: p.CorrelationID,
		Metadata: p.Metadata, Control: p.Control,
		RecoveryAfter: p.RecoveryAfter,
		PolicyID:      p.PolicyID, PolicyVersion: p.PolicyVersion,
		PreApprovalID:           p.PreApprovalID,
		PolicySelectionRecorded: p.PolicySelectionRecorded,
		Data:                    p.Data, TimeOrdered: []string{}, Nodes: []NodeRecord{}, StartedAt: e.Time,
	}
	return store.PutDoc(ctx, s, Collection, store.Key(e.Org, e.Workspace, r.DecisionID), r)
}

func applyAbandoned(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	p, err := decode[events.DecisionAbandoned](e)
	if err != nil {
		return err
	}
	return update(ctx, s, e, p.DecisionID, func(r *Record) {
		r.Status, r.Error, r.EndedAt = "abandoned", p.Error, e.Time
		r.RecoveryAfter, r.RecoveryLeaseUntil = time.Time{}, time.Time{}
		r.RecoveryOwner = ""
		r.RecoveryAttempt, r.LastRecoveryError = p.Attempt, p.Error
	})
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
		r.RecoveryAfter, r.RecoveryLeaseUntil = time.Time{}, time.Time{}
		r.RecoveryOwner = ""
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
		r.RecoveryAfter, r.RecoveryLeaseUntil = time.Time{}, time.Time{}
		r.RecoveryOwner = ""
	})
}

// applyManualReview links the decision to the case its manual_review node opened.
// The event is emitted after the terminal event on the same stream, so the record
// already exists.
func applyManualReview(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	p, err := decode[events.ManualReviewRequested](e)
	if err != nil {
		return err
	}
	return update(ctx, s, e, p.DecisionID, func(r *Record) {
		r.CaseID = p.CaseID
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
// "any"; Query matches decision id, business reference, correlation id, and entity
// id (substring, case-insensitive).
type Filter struct {
	Slug              string
	Environment       string
	Status            string
	Variant           string
	EntityType        string
	EntityID          string
	BusinessReference string
	CorrelationID     string
	Metadata          map[string]string
	Query             string
	Since             time.Time
	Until             time.Time
	Limit             int // 0 = default page size
	Offset            int
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

// query returns the normalized decision-id search term.
func (f Filter) query() string { return strings.ToLower(strings.TrimSpace(f.Query)) }

// ListPage returns the tenant's decisions matching f, newest-first, paginated. It
// filters and sorts over the lightweight index (a tenant-prefixed scan of per-decision
// summaries), then loads the FULL records only for the window it returns — so listing
// a large tenant's decisions never loads every full record's input/output/node trace
// into memory. (The store has no limit pushdown, so the index scan is still O(tenant
// entries), but of small entries; the expensive full-record load is bounded to the
// page.)
func ListPage(ctx context.Context, s store.Store, id identity.Identity, f Filter) (Page, error) {
	entries, err := listIndex(ctx, s, id, f)
	if err != nil {
		return Page{}, err
	}
	total := len(entries)
	// limit <= 0 means "no pagination" — the window is every match (aggregating
	// callers and the dashboard want the full set); a positive limit paginates.
	limit, lo, hi := total, 0, total
	if f.Limit > 0 {
		limit = f.Limit
		if limit > MaxPageSize {
			limit = MaxPageSize
		}
		lo = f.Offset
		if lo < 0 {
			lo = 0
		}
		if lo > total {
			lo = total
		}
		hi = lo + limit
		if hi > total {
			hi = total
		}
	}
	window := entries[lo:hi]
	records := make([]Record, 0, len(window))
	for _, e := range window {
		r, ok, err := store.GetDoc[Record](ctx, s, Collection, store.Key(id.Org, id.Workspace, e.DecisionID))
		if err != nil {
			return Page{}, err
		}
		// The index and the record projections fold the same events in lock-step, so
		// an index entry with no record is a projection inconsistency — fail loud, do
		// not silently drop the row.
		if !ok {
			return Page{}, fmt.Errorf("history: index entry %q has no record (projections inconsistent)", e.DecisionID)
		}
		records = append(records, r)
	}
	return Page{Records: records, Total: total, Limit: limit, Offset: lo}, nil
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
