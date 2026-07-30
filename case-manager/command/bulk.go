// SPDX-License-Identifier: AGPL-3.0-or-later

package command

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/e6qu/intraktible/case-manager/domain"
	"github.com/e6qu/intraktible/case-manager/events"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
)

// BulkOperation is a supported backend-owned case mutation.
type BulkOperation string

const (
	BulkAssign      BulkOperation = "assign"
	BulkStatus      BulkOperation = "status"
	BulkPriority    BulkOperation = "priority"
	BulkDisposition BulkOperation = "disposition"
)

// Valid reports whether o is supported.
func (o BulkOperation) Valid() bool {
	return o == BulkAssign || o == BulkStatus || o == BulkPriority || o == BulkDisposition
}

// BulkRequest is a bounded, resumable batch. Target is assignee/status/priority/
// disposition according to Operation.
type BulkRequest struct {
	Operation  BulkOperation `json:"operation"`
	CaseIDs    []string      `json:"case_ids"`
	Target     string        `json:"target"`
	ReasonCode string        `json:"reason_code,omitempty"`
	Note       string        `json:"note,omitempty"`
	Reassign   bool          `json:"reassign,omitempty"`
	Override   bool          `json:"override,omitempty"`
	Role       string        `json:"-"`
}

// Validate checks the batch boundary before admission.
func (r BulkRequest) Validate() error {
	if !r.Operation.Valid() || strings.TrimSpace(r.Target) == "" {
		return errors.New("case-manager: bulk operation and target are required")
	}
	if len(r.CaseIDs) == 0 || len(r.CaseIDs) > 500 {
		return errors.New("case-manager: bulk case_ids must contain 1..500 items")
	}
	seen := map[string]bool{}
	for _, caseID := range r.CaseIDs {
		if strings.TrimSpace(caseID) == "" || seen[caseID] {
			return errors.New("case-manager: bulk case_ids must be non-empty and unique")
		}
		seen[caseID] = true
	}
	if r.Operation == BulkDisposition && strings.TrimSpace(r.ReasonCode) == "" {
		return errors.New("case-manager: bulk disposition requires reason_code")
	}
	return nil
}

// BulkItemResult is one authoritative item outcome.
type BulkItemResult struct {
	CaseID  string `json:"case_id"`
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

// BulkResult is the durable batch manifest.
type BulkResult struct {
	BatchID        string           `json:"batch_id"`
	IdempotencyKey string           `json:"idempotency_key"`
	RequestHash    string           `json:"request_hash"`
	Operation      BulkOperation    `json:"operation"`
	Status         string           `json:"status"`
	Succeeded      int              `json:"succeeded"`
	Failed         int              `json:"failed"`
	Items          []BulkItemResult `json:"items"`
}

// Bulk durably admits and executes a bounded batch. Retrying the same key and
// request returns/resumes the same logical manifest; conflicting key reuse fails.
func (h *Handler) Bulk(ctx context.Context, id identity.Identity, idempotencyKey string, request BulkRequest) (BulkResult, error) {
	if err := id.Valid(); err != nil {
		return BulkResult{}, err
	}
	if strings.TrimSpace(idempotencyKey) == "" || len(idempotencyKey) > 256 {
		return BulkResult{}, errors.New("case-manager: Idempotency-Key is required and must be at most 256 bytes")
	}
	if err := request.Validate(); err != nil {
		return BulkResult{}, err
	}
	request.CaseIDs = append([]string(nil), request.CaseIDs...)
	slices.Sort(request.CaseIDs)
	hash, err := bulkRequestHash(request)
	if err != nil {
		return BulkResult{}, err
	}
	existing, found, err := h.bulkByKey(ctx, id, idempotencyKey)
	if err != nil {
		return BulkResult{}, err
	}
	if found {
		if existing.RequestHash != hash {
			return BulkResult{}, errors.New("case-manager: conflicting reuse of bulk Idempotency-Key")
		}
		if existing.Status == "completed" {
			return existing, nil
		}
	} else {
		batchID := h.newID()
		payload, err := json.Marshal(events.CaseBulkStarted{
			BatchID: batchID, IdempotencyKey: idempotencyKey, RequestHash: hash,
			Operation: string(request.Operation), CaseIDs: request.CaseIDs,
		})
		if err != nil {
			return BulkResult{}, fmt.Errorf("case-manager: marshal bulk start: %w", err)
		}
		if _, err := h.appendUnique(ctx, id, events.TypeCaseBulkStarted, payload, bulkAdmissionClaim(idempotencyKey)); err != nil {
			if !errors.Is(err, eventlog.ErrConflict) {
				return BulkResult{}, err
			}
			existing, found, err = h.bulkByKey(ctx, id, idempotencyKey)
			if err != nil || !found {
				return BulkResult{}, fmt.Errorf("case-manager: resolve concurrent bulk admission: %w", err)
			}
			if existing.RequestHash != hash {
				return BulkResult{}, errors.New("case-manager: conflicting reuse of bulk Idempotency-Key")
			}
		} else {
			existing = BulkResult{
				BatchID: batchID, IdempotencyKey: idempotencyKey, RequestHash: hash,
				Operation: request.Operation, Status: "running", Items: []BulkItemResult{},
			}
		}
	}

	recorded := make(map[string]bool, len(existing.Items))
	for _, item := range existing.Items {
		recorded[item.CaseID] = true
	}
	for _, caseID := range request.CaseIDs {
		if recorded[caseID] {
			continue
		}
		itemErr := h.applyBulkItem(ctx, id, request, caseID)
		result := events.CaseBulkItemRecorded{
			BatchID: existing.BatchID, CaseID: caseID, Success: itemErr == nil,
		}
		if itemErr != nil {
			result.Error = itemErr.Error()
		}
		payload, err := json.Marshal(result)
		if err != nil {
			return BulkResult{}, fmt.Errorf("case-manager: marshal bulk item: %w", err)
		}
		if _, err := h.appendUnique(ctx, id, events.TypeCaseBulkItemRecorded, payload, bulkItemClaim(existing.BatchID, caseID)); err != nil &&
			!errors.Is(err, eventlog.ErrConflict) {
			return BulkResult{}, err
		}
	}
	current, found, err := h.bulkByKey(ctx, id, idempotencyKey)
	if err != nil || !found {
		return BulkResult{}, fmt.Errorf("case-manager: rebuild bulk result: %w", err)
	}
	if len(current.Items) != len(request.CaseIDs) {
		return BulkResult{}, fmt.Errorf("case-manager: bulk %q has %d/%d recorded item results", current.BatchID, len(current.Items), len(request.CaseIDs))
	}
	if current.Status != "completed" {
		payload, err := json.Marshal(events.CaseBulkCompleted{
			BatchID: current.BatchID, Succeeded: current.Succeeded, Failed: current.Failed,
		})
		if err != nil {
			return BulkResult{}, fmt.Errorf("case-manager: marshal bulk completion: %w", err)
		}
		if _, err := h.appendUnique(ctx, id, events.TypeCaseBulkCompleted, payload, bulkCompletionClaim(current.BatchID)); err != nil &&
			!errors.Is(err, eventlog.ErrConflict) {
			return BulkResult{}, err
		}
	}
	return h.mustBulkByKey(ctx, id, idempotencyKey)
}

func (h *Handler) applyBulkItem(ctx context.Context, id identity.Identity, request BulkRequest, caseID string) error {
	state, err := h.workState(ctx, id, caseID)
	if err != nil {
		return err
	}
	if bulkDesiredState(state, request) {
		return nil
	}
	switch request.Operation {
	case BulkAssign:
		_, err = h.AssignCase(ctx, id, domain.AssignCase{CaseID: caseID, Assignee: request.Target, Reassign: request.Reassign})
	case BulkStatus:
		_, err = h.SetStatus(ctx, id, domain.SetStatus{CaseID: caseID, Status: domain.CaseStatus(request.Target), Role: request.Role})
	case BulkPriority:
		_, err = h.SetPriority(ctx, id, caseID, domain.Priority(request.Target))
	case BulkDisposition:
		_, err = h.RecordDisposition(ctx, id, caseID, request.Target, request.ReasonCode, request.Note, request.Role, request.Override)
	default:
		return fmt.Errorf("case-manager: unsupported bulk operation %q", request.Operation)
	}
	if err == nil {
		return nil
	}
	// A recovery replica may lose the item's lifecycle/assignment claim after the
	// winner applied the desired state but before recording the item result.
	latest, foldErr := h.workState(ctx, id, caseID)
	if foldErr == nil && bulkDesiredState(latest, request) {
		return nil
	}
	return err
}

func bulkDesiredState(state workState, request BulkRequest) bool {
	switch request.Operation {
	case BulkAssign:
		return state.assignee == request.Target
	case BulkStatus:
		return string(state.status) == request.Target
	case BulkPriority:
		return string(state.priority) == request.Target
	case BulkDisposition:
		return state.disposition == request.Target && state.reasonCode == request.ReasonCode
	default:
		return false
	}
}

func bulkRequestHash(request BulkRequest) (string, error) {
	request.Role = ""
	raw, err := json.Marshal(request)
	if err != nil {
		return "", fmt.Errorf("case-manager: marshal bulk request: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func (h *Handler) mustBulkByKey(ctx context.Context, id identity.Identity, key string) (BulkResult, error) {
	result, found, err := h.bulkByKey(ctx, id, key)
	if err != nil {
		return BulkResult{}, err
	}
	if !found {
		return BulkResult{}, fmt.Errorf("case-manager: bulk key %q disappeared", key)
	}
	return result, nil
}

func (h *Handler) bulkByKey(ctx context.Context, id identity.Identity, key string) (BulkResult, bool, error) {
	recorded, err := h.log.Read(ctx, 0)
	if err != nil {
		return BulkResult{}, false, fmt.Errorf("case-manager: read bulk state: %w", err)
	}
	var result BulkResult
	started := false
	for _, event := range recorded {
		if event.Org != id.Org || event.Workspace != id.Workspace {
			continue
		}
		switch event.Type {
		case events.TypeCaseBulkStarted:
			var payload events.CaseBulkStarted
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return BulkResult{}, false, fmt.Errorf("case-manager: decode bulk start seq %d: %w", event.Seq, err)
			}
			if payload.IdempotencyKey == key {
				started = true
				result = BulkResult{
					BatchID: payload.BatchID, IdempotencyKey: key, RequestHash: payload.RequestHash,
					Operation: BulkOperation(payload.Operation), Status: "running", Items: []BulkItemResult{},
				}
			}
		case events.TypeCaseBulkItemRecorded:
			if !started {
				continue
			}
			var payload events.CaseBulkItemRecorded
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return BulkResult{}, false, fmt.Errorf("case-manager: decode bulk item seq %d: %w", event.Seq, err)
			}
			if payload.BatchID == result.BatchID {
				result.Items = append(result.Items, BulkItemResult{
					CaseID: payload.CaseID, Success: payload.Success, Error: payload.Error,
				})
				if payload.Success {
					result.Succeeded++
				} else {
					result.Failed++
				}
			}
		case events.TypeCaseBulkCompleted:
			if !started {
				continue
			}
			var payload events.CaseBulkCompleted
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return BulkResult{}, false, fmt.Errorf("case-manager: decode bulk completion seq %d: %w", event.Seq, err)
			}
			if payload.BatchID == result.BatchID {
				if payload.Succeeded != result.Succeeded || payload.Failed != result.Failed {
					return BulkResult{}, false, fmt.Errorf("case-manager: bulk %q completion counters do not match items", payload.BatchID)
				}
				result.Status = "completed"
			}
		}
	}
	slices.SortFunc(result.Items, func(a, b BulkItemResult) int { return strings.Compare(a.CaseID, b.CaseID) })
	return result, started, nil
}

func bulkAdmissionClaim(key string) string { return "case.bulk\x00" + key }
func bulkItemClaim(batchID, caseID string) string {
	return "case.bulk_item\x00" + batchID + "\x00" + caseID
}
func bulkCompletionClaim(batchID string) string { return "case.bulk_complete\x00" + batchID }
