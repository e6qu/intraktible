// SPDX-License-Identifier: AGPL-3.0-or-later

// Package population implements durable, replayable population decision and
// backtest jobs. Every input and exact flow/experiment version is captured in
// the creation event; workers lease individual items with durable claims.
package population

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/intraktible/decision-engine/command"
	"github.com/e6qu/intraktible/decision-engine/domain"
	"github.com/e6qu/intraktible/decision-engine/experiments"
	"github.com/e6qu/intraktible/decision-engine/flows"
	"github.com/e6qu/intraktible/platform/entity"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

const (
	Stream     = "decision.population_jobs"
	Collection = "decision_population_jobs"

	TypeCreated         = "decision.population.created"
	TypePaused          = "decision.population.paused"
	TypeResumed         = "decision.population.resumed"
	TypeCancelRequested = "decision.population.cancel_requested"
	TypeRetryRequested  = "decision.population.retry_requested"
	TypeItemClaimed     = "decision.population.item_claimed"
	TypeItemHeartbeat   = "decision.population.item_heartbeat"
	TypeItemSucceeded   = "decision.population.item_succeeded"
	TypeItemFailed      = "decision.population.item_failed"
	TypeCompleted       = "decision.population.completed"
	TypeCancelled       = "decision.population.cancelled"
	TypeExpired         = "decision.population.expired"
)

const (
	maxItems            = 100_000
	maxManifestBytes    = 64 << 20
	defaultAttempts     = 3
	maxAttempts         = 10
	defaultRetention    = 30
	maxRetentionDays    = 3650
	maxJobConcurrency   = 32
	maxWorkerRecoveries = 3
	itemLease           = 30 * time.Second
	workerPoll          = 200 * time.Millisecond
)

// Kind distinguishes recorded production-like decisions from record-free
// immutable-version backtests.
type Kind string

const (
	KindDecision Kind = "decision"
	KindBacktest Kind = "backtest"
)

func (k Kind) Valid() bool { return k == KindDecision || k == KindBacktest }

// State is the durable job lifecycle.
type State string

const (
	StateQueued              State = "queued"
	StateRunning             State = "running"
	StatePaused              State = "paused"
	StateCancelling          State = "cancelling"
	StateCancelled           State = "cancelled"
	StateCompleted           State = "completed"
	StateCompletedWithErrors State = "completed_with_errors"
	StateExpired             State = "expired"
)

func (s State) terminal() bool {
	return s == StateCancelled || s == StateCompleted ||
		s == StateCompletedWithErrors || s == StateExpired
}

// ItemInput is one immutable population member.
type ItemInput struct {
	Data              map[string]any  `json:"data"`
	EntityType        string          `json:"entity_type,omitempty"`
	EntityID          string          `json:"entity_id,omitempty"`
	BusinessReference string          `json:"business_reference,omitempty"`
	CorrelationID     string          `json:"correlation_id,omitempty"`
	Metadata          json.RawMessage `json:"metadata,omitempty"`
}

// ItemManifest pins the exact flow version and cohort assignment.
type ItemManifest struct {
	Index      int                     `json:"index"`
	Input      ItemInput               `json:"input"`
	Version    int                     `json:"version"`
	Assignment *experiments.Assignment `json:"assignment,omitempty"`
}

// Manifest is immutable job input.
type Manifest struct {
	Kind        Kind           `json:"kind"`
	FlowID      string         `json:"flow_id"`
	Slug        string         `json:"slug"`
	Environment string         `json:"environment"`
	Items       []ItemManifest `json:"items"`
}

// CreateCommand is the caller request before versions are resolved.
type CreateCommand struct {
	Kind          Kind        `json:"kind"`
	Slug          string      `json:"slug"`
	Environment   string      `json:"environment"`
	Items         []ItemInput `json:"items"`
	MaxAttempts   int         `json:"max_attempts,omitempty"`
	Concurrency   int         `json:"concurrency,omitempty"`
	RetentionDays int         `json:"retention_days,omitempty"`
}

// Created captures all immutable execution inputs.
type Created struct {
	JobID              string    `json:"job_id"`
	Manifest           Manifest  `json:"manifest"`
	ManifestHash       string    `json:"manifest_hash"`
	MaxAttempts        int       `json:"max_attempts"`
	Concurrency        int       `json:"concurrency"`
	ExpiresAt          time.Time `json:"expires_at"`
	IdempotencyKeyHash string    `json:"idempotency_key_hash"`
	RequestHash        string    `json:"request_hash"`
}

type Transition struct {
	JobID  string `json:"job_id"`
	Reason string `json:"reason,omitempty"`
}

type RetryRequested struct {
	JobID   string `json:"job_id"`
	Indices []int  `json:"indices"`
}

type ItemClaimed struct {
	JobID      string    `json:"job_id"`
	Index      int       `json:"index"`
	Attempt    int       `json:"attempt"`
	Worker     string    `json:"worker"`
	LeaseUntil time.Time `json:"lease_until"`
}

type ItemHeartbeat struct {
	JobID      string    `json:"job_id"`
	Index      int       `json:"index"`
	Attempt    int       `json:"attempt"`
	Worker     string    `json:"worker"`
	LeaseUntil time.Time `json:"lease_until"`
}

// ItemSucceeded contains the downloadable result manifest row.
type ItemSucceeded struct {
	JobID       string         `json:"job_id"`
	Index       int            `json:"index"`
	Attempt     int            `json:"attempt"`
	DecisionID  string         `json:"decision_id,omitempty"`
	Status      string         `json:"status"`
	Output      map[string]any `json:"output,omitempty"`
	Disposition string         `json:"disposition,omitempty"`
	Error       string         `json:"error,omitempty"`
}

type ItemFailed struct {
	JobID     string `json:"job_id"`
	Index     int    `json:"index"`
	Attempt   int    `json:"attempt"`
	Error     string `json:"error"`
	Retryable bool   `json:"retryable"`
}

type Completed struct {
	JobID     string `json:"job_id"`
	Succeeded int    `json:"succeeded"`
	Failed    int    `json:"failed"`
}

// ItemView is the current leased or terminal item state.
type ItemView struct {
	Index       int            `json:"index"`
	State       string         `json:"state"` // pending | claimed | succeeded | failed
	Attempt     int            `json:"attempt"`
	Worker      string         `json:"worker,omitempty"`
	LeaseUntil  time.Time      `json:"lease_until,omitempty"`
	DecisionID  string         `json:"decision_id,omitempty"`
	Status      string         `json:"status,omitempty"`
	Output      map[string]any `json:"output,omitempty"`
	Disposition string         `json:"disposition,omitempty"`
	Error       string         `json:"error,omitempty"`
	Retryable   bool           `json:"retryable,omitempty"`
}

// View is the operational job projection.
type View struct {
	Org          string     `json:"org"`
	Workspace    string     `json:"workspace"`
	JobID        string     `json:"job_id"`
	State        State      `json:"state"`
	StateToken   string     `json:"state_token"`
	Manifest     Manifest   `json:"manifest"`
	ManifestHash string     `json:"manifest_hash"`
	MaxAttempts  int        `json:"max_attempts"`
	Concurrency  int        `json:"concurrency"`
	Items        []ItemView `json:"items"`
	Total        int        `json:"total"`
	Pending      int        `json:"pending"`
	Running      int        `json:"running"`
	Succeeded    int        `json:"succeeded"`
	Failed       int        `json:"failed"`
	CreatedBy    string     `json:"created_by"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	ExpiresAt    time.Time  `json:"expires_at"`
}

// Projector folds jobs and per-item leases.
type Projector struct{}

func (Projector) Name() string          { return "decision_population_jobs" }
func (Projector) Collections() []string { return []string{Collection} }

func (Projector) Apply(ctx context.Context, event eventlog.Envelope, st store.Store) error {
	switch event.Type {
	case TypeCreated:
		var payload Created
		if err := decode(event, &payload); err != nil {
			return err
		}
		items := make([]ItemView, len(payload.Manifest.Items))
		for i := range items {
			items[i] = ItemView{Index: i, State: "pending"}
		}
		return store.PutDoc(ctx, st, Collection, store.Key(event.Org, event.Workspace, payload.JobID), View{
			Org: event.Org, Workspace: event.Workspace, JobID: payload.JobID,
			State: StateQueued, Manifest: payload.Manifest, ManifestHash: payload.ManifestHash,
			StateToken:  eventToken(event),
			MaxAttempts: payload.MaxAttempts, Concurrency: payload.Concurrency,
			Items: items, Total: len(items), Pending: len(items),
			CreatedBy: event.Actor, CreatedAt: event.Time, UpdatedAt: event.Time,
			ExpiresAt: payload.ExpiresAt,
		})
	case TypePaused, TypeResumed, TypeCancelRequested, TypeCancelled, TypeExpired:
		var payload Transition
		if err := decode(event, &payload); err != nil {
			return err
		}
		return mutateView(ctx, st, event, payload.JobID, func(view *View) error {
			switch event.Type {
			case TypePaused:
				if view.State != StateQueued && view.State != StateRunning {
					return fmt.Errorf("population: cannot pause job in %s", view.State)
				}
				view.State = StatePaused
			case TypeResumed:
				if view.State != StatePaused && view.State != StateCompletedWithErrors {
					return fmt.Errorf("population: cannot resume job in %s", view.State)
				}
				view.State = StateQueued
			case TypeCancelRequested:
				if view.State.terminal() {
					return fmt.Errorf("population: cannot cancel job in %s", view.State)
				}
				view.State = StateCancelling
			case TypeCancelled:
				view.State = StateCancelled
			case TypeExpired:
				if !view.State.terminal() {
					return fmt.Errorf("population: cannot expire non-terminal job")
				}
				view.State = StateExpired
				for i := range view.Items {
					view.Items[i].Output = nil
				}
			}
			view.StateToken = eventToken(event)
			return nil
		})
	case TypeRetryRequested:
		var payload RetryRequested
		if err := decode(event, &payload); err != nil {
			return err
		}
		return mutateView(ctx, st, event, payload.JobID, func(view *View) error {
			for _, index := range payload.Indices {
				item, err := itemAt(view, index)
				if err != nil {
					return err
				}
				item.State, item.Attempt, item.Error, item.Retryable = "pending", 0, "", false
			}
			view.State = StateQueued
			view.StateToken = eventToken(event)
			recount(view)
			return nil
		})
	case TypeItemClaimed:
		var payload ItemClaimed
		if err := decode(event, &payload); err != nil {
			return err
		}
		return mutateView(ctx, st, event, payload.JobID, func(view *View) error {
			item, err := itemAt(view, payload.Index)
			if err != nil {
				return err
			}
			// A worker may finish after its lease expires while another replica
			// has already read the expired projection and appended a successor
			// claim. Never reopen a terminal item in that event ordering.
			if item.State == "succeeded" || (item.State == "failed" && !item.Retryable) {
				return nil
			}
			if payload.Attempt <= item.Attempt {
				return nil
			}
			item.State, item.Attempt, item.Worker, item.LeaseUntil =
				"claimed", payload.Attempt, payload.Worker, payload.LeaseUntil
			if view.State == StateQueued {
				view.State = StateRunning
				view.StateToken = eventToken(event)
			}
			recount(view)
			return nil
		})
	case TypeItemHeartbeat:
		var payload ItemHeartbeat
		if err := decode(event, &payload); err != nil {
			return err
		}
		return mutateView(ctx, st, event, payload.JobID, func(view *View) error {
			item, err := itemAt(view, payload.Index)
			if err != nil {
				return err
			}
			if item.State == "claimed" && item.Attempt == payload.Attempt && item.Worker == payload.Worker {
				item.LeaseUntil = payload.LeaseUntil
			}
			return nil
		})
	case TypeItemSucceeded:
		var payload ItemSucceeded
		if err := decode(event, &payload); err != nil {
			return err
		}
		return mutateView(ctx, st, event, payload.JobID, func(view *View) error {
			item, err := itemAt(view, payload.Index)
			if err != nil {
				return err
			}
			// Expired workers can append an auditable late outcome after a
			// successor has claimed the item. Fencing is projection-based: only
			// the active attempt mutates the result manifest.
			if item.State == "succeeded" || item.State == "failed" || payload.Attempt < item.Attempt {
				return nil
			}
			if item.State != "claimed" || item.Attempt != payload.Attempt {
				return fmt.Errorf("population: success has no matching claim")
			}
			*item = ItemView{
				Index: payload.Index, State: "succeeded", Attempt: payload.Attempt,
				DecisionID: payload.DecisionID, Status: payload.Status, Output: payload.Output,
				Disposition: payload.Disposition, Error: payload.Error,
			}
			recount(view)
			return nil
		})
	case TypeItemFailed:
		var payload ItemFailed
		if err := decode(event, &payload); err != nil {
			return err
		}
		return mutateView(ctx, st, event, payload.JobID, func(view *View) error {
			item, err := itemAt(view, payload.Index)
			if err != nil {
				return err
			}
			if item.State == "succeeded" || item.State == "failed" || payload.Attempt < item.Attempt {
				return nil
			}
			if item.State != "claimed" || item.Attempt != payload.Attempt {
				return fmt.Errorf("population: failure has no matching claim")
			}
			item.State, item.Error, item.Retryable, item.Worker, item.LeaseUntil =
				"failed", payload.Error, payload.Retryable, "", time.Time{}
			recount(view)
			return nil
		})
	case TypeCompleted:
		var payload Completed
		if err := decode(event, &payload); err != nil {
			return err
		}
		return mutateView(ctx, st, event, payload.JobID, func(view *View) error {
			if view.Pending != 0 || view.Running != 0 {
				return fmt.Errorf("population: completed job still has unfinished items")
			}
			if payload.Succeeded != view.Succeeded || payload.Failed != view.Failed {
				return fmt.Errorf("population: completion totals do not match projection")
			}
			if view.Failed > 0 {
				view.State = StateCompletedWithErrors
			} else {
				view.State = StateCompleted
			}
			view.StateToken = eventToken(event)
			return nil
		})
	default:
		return nil
	}
}

func mutateView(ctx context.Context, st store.Store, event eventlog.Envelope, jobID string, fn func(*View) error) error {
	key := store.Key(event.Org, event.Workspace, jobID)
	view, ok, err := store.GetDoc[View](ctx, st, Collection, key)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("population: event %s for unknown job %q", event.Type, jobID)
	}
	if err := fn(&view); err != nil {
		return err
	}
	view.UpdatedAt = event.Time
	return store.PutDoc(ctx, st, Collection, key, view)
}

func itemAt(view *View, index int) (*ItemView, error) {
	if index < 0 || index >= len(view.Items) {
		return nil, fmt.Errorf("population: item index %d is out of range", index)
	}
	return &view.Items[index], nil
}

func recount(view *View) {
	view.Pending, view.Running, view.Succeeded, view.Failed = 0, 0, 0, 0
	for _, item := range view.Items {
		switch item.State {
		case "pending":
			view.Pending++
		case "claimed":
			view.Running++
		case "succeeded":
			view.Succeeded++
		case "failed":
			if item.Retryable && item.Attempt < view.MaxAttempts {
				view.Pending++
			} else {
				view.Failed++
			}
		}
	}
}

func decode(event eventlog.Envelope, target any) error {
	if err := json.Unmarshal(event.Payload, target); err != nil {
		return fmt.Errorf("population: decode %s seq %d: %w", event.Type, event.Seq, err)
	}
	return nil
}

func eventToken(event eventlog.Envelope) string {
	if event.ID != "" {
		return event.ID
	}
	return strconv.FormatUint(event.Seq, 10)
}

// Read returns one tenant job.
func Read(ctx context.Context, st store.Store, id identity.Identity, jobID string) (View, bool, error) {
	return store.GetDoc[View](ctx, st, Collection, store.Key(id.Org, id.Workspace, jobID))
}

// List returns newest jobs first.
func List(ctx context.Context, st store.Store, id identity.Identity) ([]View, error) {
	return store.QueryDocs(
		ctx, st, Collection, store.Key(id.Org, id.Workspace, ""),
		func(View) bool { return true },
		func(a, b View) bool {
			if !a.CreatedAt.Equal(b.CreatedAt) {
				return a.CreatedAt.After(b.CreatedAt)
			}
			return a.JobID < b.JobID
		},
	)
}

// Handler owns job commands and the worker pool.
type Handler struct {
	log         eventlog.Log
	store       store.Store
	decide      *command.DecideHandler
	experiments *experiments.Handler
	now         func() time.Time
	newID       func() string
	mu          sync.Mutex
	workers     sync.WaitGroup
}

func NewHandler(log eventlog.Log, st store.Store, decide *command.DecideHandler, experimentsHandler *experiments.Handler) *Handler {
	return &Handler{
		log: log, store: st, decide: decide, experiments: experimentsHandler,
		now: func() time.Time { return time.Now().UTC() }, newID: randomID,
	}
}

func (h *Handler) WithNow(now func() time.Time) *Handler {
	h.now = now
	return h
}

// Create resolves every item to an exact version/cohort and records the
// immutable input manifest. Idempotent retries return the original job.
func (h *Handler) Create(ctx context.Context, id identity.Identity, command CreateCommand, idempotencyKey string) (Created, eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return Created{}, eventlog.Envelope{}, err
	}
	command = defaults(command)
	if err := validateCreate(command); err != nil {
		return Created{}, eventlog.Envelope{}, err
	}
	keyHash, err := idempotencyHash(idempotencyKey)
	if err != nil {
		return Created{}, eventlog.Envelope{}, err
	}
	requestHash, err := jsonHash(command)
	if err != nil {
		return Created{}, eventlog.Envelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if prior, found, err := h.byIdempotency(ctx, id, keyHash); err != nil {
		return Created{}, eventlog.Envelope{}, err
	} else if found {
		if prior.RequestHash != requestHash {
			return Created{}, eventlog.Envelope{}, fmt.Errorf("population: idempotency key used with a different request")
		}
		return prior, eventlog.Envelope{}, nil
	}
	flow, found, err := flows.BySlug(ctx, h.store, id, command.Slug)
	if err != nil {
		return Created{}, eventlog.Envelope{}, err
	}
	if !found || len(flow.Versions) == 0 {
		return Created{}, eventlog.Envelope{}, fmt.Errorf("population: unknown or unpublished flow %q", command.Slug)
	}
	baseVersion := flow.Latest
	if deployment, deployed := flow.Deployments[command.Environment]; deployed && deployment.Version > 0 {
		if deployment.ChallengerVersion > 0 {
			return Created{}, eventlog.Envelope{}, fmt.Errorf("population: legacy challenger deployments cannot be snapshotted; use a governed experiment")
		}
		baseVersion = deployment.Version
	} else if command.Environment != string(domain.EnvSandbox) {
		return Created{}, eventlog.Envelope{}, fmt.Errorf("population: flow has no %s deployment", command.Environment)
	}
	manifest := Manifest{
		Kind: command.Kind, FlowID: flow.FlowID, Slug: flow.Slug,
		Environment: command.Environment, Items: make([]ItemManifest, len(command.Items)),
	}
	for index, input := range command.Items {
		item := ItemManifest{Index: index, Input: input, Version: baseVersion}
		ref := entity.Ref{Type: entity.Type(input.EntityType), ID: entity.ID(input.EntityID)}
		if h.experiments != nil {
			assignment, assigned, err := h.experiments.Resolve(
				ctx, id, flow.FlowID, command.Environment, input.Data, ref,
			)
			if err != nil {
				return Created{}, eventlog.Envelope{}, fmt.Errorf("population: item %d: %w", index, err)
			}
			if assigned {
				assignedTreatment := assignment
				item.Version, item.Assignment = assignment.Version, &assignedTreatment
			}
		}
		manifest.Items[index] = item
	}
	manifestRaw, err := json.Marshal(manifest)
	if err != nil {
		return Created{}, eventlog.Envelope{}, fmt.Errorf("population: marshal manifest: %w", err)
	}
	if len(manifestRaw) > maxManifestBytes {
		return Created{}, eventlog.Envelope{}, fmt.Errorf("population: manifest is %d bytes; maximum is %d", len(manifestRaw), maxManifestBytes)
	}
	manifestDigest := sha256.Sum256(manifestRaw)
	payload := Created{
		JobID: h.newID(), Manifest: manifest, ManifestHash: hex.EncodeToString(manifestDigest[:]),
		MaxAttempts: command.MaxAttempts, Concurrency: command.Concurrency,
		ExpiresAt:          h.now().AddDate(0, 0, command.RetentionDays),
		IdempotencyKeyHash: keyHash, RequestHash: requestHash,
	}
	event, err := eventlog.AppendJSONUnique(
		ctx, h.log, id.Org, id.Workspace, id.Actor, Stream, TypeCreated, h.now(),
		payload, "population.idempotency\x00"+keyHash,
	)
	if errors.Is(err, eventlog.ErrConflict) {
		prior, found, foldErr := h.byIdempotency(ctx, id, keyHash)
		if foldErr != nil {
			return Created{}, eventlog.Envelope{}, foldErr
		}
		if found && prior.RequestHash == requestHash {
			return prior, eventlog.Envelope{}, nil
		}
	}
	return payload, event, err
}

func defaults(command CreateCommand) CreateCommand {
	if command.Kind == "" {
		command.Kind = KindDecision
	}
	if command.MaxAttempts == 0 {
		command.MaxAttempts = defaultAttempts
	}
	if command.Concurrency == 0 {
		command.Concurrency = 4
	}
	if command.RetentionDays == 0 {
		command.RetentionDays = defaultRetention
	}
	return command
}

func validateCreate(command CreateCommand) error {
	switch {
	case !command.Kind.Valid():
		return fmt.Errorf("population: invalid kind %q", command.Kind)
	case strings.TrimSpace(command.Slug) == "":
		return fmt.Errorf("population: slug is required")
	case !domain.ValidEnvironment(command.Environment):
		return fmt.Errorf("population: invalid environment %q", command.Environment)
	case len(command.Items) == 0:
		return fmt.Errorf("population: items are required")
	case len(command.Items) > maxItems:
		return fmt.Errorf("population: %d items exceeds maximum %d", len(command.Items), maxItems)
	case command.MaxAttempts < 1 || command.MaxAttempts > maxAttempts:
		return fmt.Errorf("population: max_attempts must be between 1 and %d", maxAttempts)
	case command.Concurrency < 1 || command.Concurrency > maxJobConcurrency:
		return fmt.Errorf("population: concurrency must be between 1 and %d", maxJobConcurrency)
	case command.RetentionDays < 1 || command.RetentionDays > maxRetentionDays:
		return fmt.Errorf("population: retention_days must be between 1 and %d", maxRetentionDays)
	}
	for index, item := range command.Items {
		if (item.EntityType == "") != (item.EntityID == "") {
			return fmt.Errorf("population: item %d must supply entity_type and entity_id together", index)
		}
		if item.Data == nil {
			command.Items[index].Data = map[string]any{}
		}
	}
	return nil
}

func (h *Handler) Pause(ctx context.Context, id identity.Identity, jobID, reason string) (eventlog.Envelope, error) {
	return h.transition(ctx, id, jobID, []State{StateQueued, StateRunning}, TypePaused, reason)
}

func (h *Handler) Resume(ctx context.Context, id identity.Identity, jobID, reason string) (eventlog.Envelope, error) {
	return h.transition(ctx, id, jobID, []State{StatePaused}, TypeResumed, reason)
}

func (h *Handler) Cancel(ctx context.Context, id identity.Identity, jobID, reason string) (eventlog.Envelope, error) {
	return h.transition(ctx, id, jobID, []State{StateQueued, StateRunning, StatePaused}, TypeCancelRequested, reason)
}

func (h *Handler) transition(ctx context.Context, id identity.Identity, jobID string, expected []State, typ, reason string) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	view, found, err := Read(ctx, h.store, id, jobID)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if !found {
		return eventlog.Envelope{}, fmt.Errorf("population: unknown job %q", jobID)
	}
	valid := false
	for _, state := range expected {
		valid = valid || state == view.State
	}
	if !valid {
		return eventlog.Envelope{}, fmt.Errorf("population: cannot apply %s from %s", typ, view.State)
	}
	return eventlog.AppendJSONUnique(
		ctx, h.log, id.Org, id.Workspace, id.Actor, Stream, typ, h.now(),
		Transition{JobID: jobID, Reason: strings.TrimSpace(reason)},
		"population.state.exit\x00"+jobID+"\x00"+view.StateToken,
	)
}

// Retry resets selected exhausted failures (all when indices is empty).
func (h *Handler) Retry(ctx context.Context, id identity.Identity, jobID string, indices []int) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	view, found, err := Read(ctx, h.store, id, jobID)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if !found {
		return eventlog.Envelope{}, fmt.Errorf("population: unknown job %q", jobID)
	}
	if view.State != StateCompletedWithErrors {
		return eventlog.Envelope{}, fmt.Errorf("population: only completed_with_errors jobs can retry")
	}
	if len(indices) == 0 {
		for _, item := range view.Items {
			if item.State == "failed" {
				indices = append(indices, item.Index)
			}
		}
	}
	sort.Ints(indices)
	for _, index := range indices {
		item, err := itemAt(&view, index)
		if err != nil {
			return eventlog.Envelope{}, err
		}
		if item.State != "failed" {
			return eventlog.Envelope{}, fmt.Errorf("population: item %d is not failed", index)
		}
	}
	return eventlog.AppendJSONUnique(
		ctx, h.log, id.Org, id.Workspace, id.Actor, Stream, TypeRetryRequested, h.now(),
		RetryRequested{JobID: jobID, Indices: indices},
		"population.state.exit\x00"+jobID+"\x00"+view.StateToken,
	)
}

func (h *Handler) byIdempotency(ctx context.Context, id identity.Identity, keyHash string) (Created, bool, error) {
	events, err := h.log.ReadTenantStream(ctx, id.Org, id.Workspace, Stream, 0)
	if err != nil {
		return Created{}, false, err
	}
	for _, event := range events {
		if event.Type != TypeCreated {
			continue
		}
		var payload Created
		if err := decode(event, &payload); err != nil {
			return Created{}, false, err
		}
		if payload.IdempotencyKeyHash == keyHash {
			return payload, true, nil
		}
	}
	return Created{}, false, nil
}

func idempotencyHash(key string) (string, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return "", fmt.Errorf("population: Idempotency-Key is required")
	}
	if len(key) > 256 {
		return "", fmt.Errorf("population: Idempotency-Key exceeds 256 bytes")
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:]), nil
}

func jsonHash(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func randomID() string {
	var bytes [16]byte
	if _, err := io.ReadFull(rand.Reader, bytes[:]); err != nil {
		panic("population: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(bytes[:])
}
