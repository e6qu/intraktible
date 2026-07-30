// SPDX-License-Identifier: AGPL-3.0-or-later

package experiments

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/intraktible/decision-engine/flows"
	"github.com/e6qu/intraktible/platform/entity"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// Handler validates lifecycle transitions against authoritative events and
// appends experiment/exposure facts.
type Handler struct {
	log   eventlog.Log
	store store.Store
	now   func() time.Time
	newID func() string
	mu    sync.Mutex
}

// NewHandler creates an experiment command handler.
func NewHandler(log eventlog.Log, st store.Store) *Handler {
	return &Handler{
		log: log, store: st,
		now:   func() time.Time { return time.Now().UTC() },
		newID: randomID,
	}
}

// WithNow overrides the event clock.
func (h *Handler) WithNow(now func() time.Time) *Handler {
	h.now = now
	return h
}

// Create validates the flow/version contract and appends a draft cohort.
func (h *Handler) Create(ctx context.Context, id identity.Identity, spec Spec) (string, eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return "", eventlog.Envelope{}, err
	}
	if err := h.validateSpec(ctx, id, spec); err != nil {
		return "", eventlog.Envelope{}, err
	}
	experimentID := h.newID()
	e, err := eventlog.AppendJSONUnique(
		ctx, h.log, id.Org, id.Workspace, id.Actor, Stream, TypeCreated, h.now(),
		Created{ExperimentID: experimentID, Spec: spec},
		"experiment.id\x00"+id.Org+"\x00"+id.Workspace+"\x00"+experimentID,
	)
	return experimentID, e, err
}

// Update replaces a draft spec and increments its cohort. A cohort is never
// reused, even before exposure, so material configuration always has a new
// stable-assignment namespace.
func (h *Handler) Update(ctx context.Context, id identity.Identity, experimentID string, spec Spec) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if err := h.validateSpec(ctx, id, spec); err != nil {
		return eventlog.Envelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for attempt := 0; attempt < 8; attempt++ {
		agg, ok, err := h.foldOne(ctx, id, experimentID)
		if err != nil {
			return eventlog.Envelope{}, err
		}
		if !ok {
			return eventlog.Envelope{}, fmt.Errorf("experiments: unknown experiment %q", experimentID)
		}
		if agg.state != StateDraft {
			return eventlog.Envelope{}, fmt.Errorf("experiments: only a draft can be updated (state is %s)", agg.state)
		}
		cohort := agg.cohort + 1
		e, err := eventlog.AppendJSONUnique(
			ctx, h.log, id.Org, id.Workspace, id.Actor, Stream, TypeUpdated, h.now(),
			Updated{ExperimentID: experimentID, Cohort: cohort, Spec: spec},
			stateExitClaim(experimentID, agg.stateToken),
		)
		if errors.Is(err, eventlog.ErrConflict) {
			continue
		}
		return e, err
	}
	return eventlog.Envelope{}, fmt.Errorf("experiments: update contention exceeded retry limit")
}

// Start starts a sandbox/staging cohort directly. Production creates a pending
// maker-checker request and returns its request id.
func (h *Handler) Start(ctx context.Context, id identity.Identity, experimentID string) (requestID string, event eventlog.Envelope, err error) {
	if err := id.Valid(); err != nil {
		return "", eventlog.Envelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	agg, ok, err := h.foldOne(ctx, id, experimentID)
	if err != nil {
		return "", eventlog.Envelope{}, err
	}
	if !ok {
		return "", eventlog.Envelope{}, fmt.Errorf("experiments: unknown experiment %q", experimentID)
	}
	if agg.state != StateDraft {
		return "", eventlog.Envelope{}, fmt.Errorf("experiments: cannot start from %s", agg.state)
	}
	if err := h.validateLaunch(ctx, id, experimentID, agg); err != nil {
		return "", eventlog.Envelope{}, err
	}
	if agg.spec.Environment == "production" {
		requestID = h.newID()
		event, err = eventlog.AppendJSONUnique(
			ctx, h.log, id.Org, id.Workspace, id.Actor, Stream, TypeLaunchRequested, h.now(),
			LaunchRequested{ExperimentID: experimentID, RequestID: requestID, Cohort: agg.cohort},
			stateExitClaim(experimentID, agg.stateToken),
		)
		return requestID, event, err
	}
	event, err = h.appendTransition(ctx, id, TypeStarted, experimentID, agg.cohort, agg.stateToken, "")
	return "", event, err
}

// ApproveLaunch is the checker side of a production launch.
func (h *Handler) ApproveLaunch(ctx context.Context, id identity.Identity, experimentID, requestID, reason string) (eventlog.Envelope, error) {
	return h.decideLaunch(ctx, id, experimentID, requestID, reason, true)
}

// RejectLaunch is the checker rejection path.
func (h *Handler) RejectLaunch(ctx context.Context, id identity.Identity, experimentID, requestID, reason string) (eventlog.Envelope, error) {
	return h.decideLaunch(ctx, id, experimentID, requestID, reason, false)
}

func (h *Handler) decideLaunch(ctx context.Context, id identity.Identity, experimentID, requestID, reason string, approve bool) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	agg, ok, err := h.foldOne(ctx, id, experimentID)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if !ok {
		return eventlog.Envelope{}, fmt.Errorf("experiments: unknown experiment %q", experimentID)
	}
	if agg.state != StatePendingLaunch || agg.requestID != requestID {
		return eventlog.Envelope{}, fmt.Errorf("experiments: launch request %q is not pending", requestID)
	}
	if id.Actor == agg.requestedBy || id.Actor == agg.createdBy {
		return eventlog.Envelope{}, fmt.Errorf("experiments: maker-checker requires a different approver")
	}
	if approve {
		if err := h.validateLaunch(ctx, id, experimentID, agg); err != nil {
			return eventlog.Envelope{}, err
		}
		return eventlog.AppendJSONUnique(
			ctx, h.log, id.Org, id.Workspace, id.Actor, Stream, TypeLaunchApproved, h.now(),
			LaunchApproved{
				ExperimentID: experimentID, RequestID: requestID,
				Cohort: agg.cohort, Reason: strings.TrimSpace(reason),
			},
			launchDecisionClaim(experimentID, requestID),
		)
	}
	return eventlog.AppendJSONUnique(
		ctx, h.log, id.Org, id.Workspace, id.Actor, Stream, TypeLaunchRejected, h.now(),
		LaunchRejected{
			ExperimentID: experimentID, RequestID: requestID,
			Cohort: agg.cohort, Reason: strings.TrimSpace(reason),
		},
		launchDecisionClaim(experimentID, requestID),
	)
}

// Pause stops new assignments while retaining the cohort for a later resume.
func (h *Handler) Pause(ctx context.Context, id identity.Identity, experimentID, reason string) (eventlog.Envelope, error) {
	return h.transition(ctx, id, experimentID, StateRunning, TypePaused, reason)
}

// Resume restarts assignment into the same immutable cohort.
func (h *Handler) Resume(ctx context.Context, id identity.Identity, experimentID, reason string) (eventlog.Envelope, error) {
	return h.transition(ctx, id, experimentID, StatePaused, TypeResumed, reason)
}

// Complete closes a running or paused cohort for analysis.
func (h *Handler) Complete(ctx context.Context, id identity.Identity, experimentID, reason string) (eventlog.Envelope, error) {
	return h.transitionAny(ctx, id, experimentID, []State{StateRunning, StatePaused}, TypeCompleted, reason)
}

// Cancel terminates a non-terminal experiment.
func (h *Handler) Cancel(ctx context.Context, id identity.Identity, experimentID, reason string) (eventlog.Envelope, error) {
	return h.transitionAny(ctx, id, experimentID, []State{StateDraft, StatePendingLaunch, StateRunning, StatePaused}, TypeCancelled, reason)
}

func (h *Handler) transition(ctx context.Context, id identity.Identity, experimentID string, expected State, typ, reason string) (eventlog.Envelope, error) {
	return h.transitionAny(ctx, id, experimentID, []State{expected}, typ, reason)
}

func (h *Handler) transitionAny(ctx context.Context, id identity.Identity, experimentID string, expected []State, typ, reason string) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	agg, ok, err := h.foldOne(ctx, id, experimentID)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if !ok {
		return eventlog.Envelope{}, fmt.Errorf("experiments: unknown experiment %q", experimentID)
	}
	valid := false
	for _, state := range expected {
		valid = valid || state == agg.state
	}
	if !valid {
		return eventlog.Envelope{}, fmt.Errorf("experiments: cannot apply %s from %s", typ, agg.state)
	}
	unique := stateExitClaim(experimentID, agg.stateToken)
	if agg.state == StatePendingLaunch && typ == TypeCancelled {
		unique = launchDecisionClaim(experimentID, agg.requestID)
	}
	return h.appendTransition(ctx, id, typ, experimentID, agg.cohort, unique, reason)
}

func (h *Handler) appendTransition(ctx context.Context, id identity.Identity, typ, experimentID string, cohort int, unique, reason string) (eventlog.Envelope, error) {
	return eventlog.AppendJSONUnique(
		ctx, h.log, id.Org, id.Workspace, id.Actor, Stream, typ, h.now(),
		Transition{ExperimentID: experimentID, Cohort: cohort, Reason: strings.TrimSpace(reason)},
		unique,
	)
}

// Resolve assigns the subject into the current active cohort.
func (h *Handler) Resolve(ctx context.Context, id identity.Identity, flowID, environment string, data map[string]any, ref entity.Ref) (Assignment, bool, error) {
	return Resolve(ctx, h.store, id, flowID, environment, data, ref, h.now())
}

// RecordExposure appends the reached-treatment fact once per decision.
func (h *Handler) RecordExposure(ctx context.Context, id identity.Identity, decisionID, flowID, environment string, assignment Assignment) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if decisionID == "" || assignment.ExperimentID == "" || assignment.Cohort < 1 || assignment.Version < 1 {
		return eventlog.Envelope{}, fmt.Errorf("experiments: incomplete exposure")
	}
	now := h.now()
	return eventlog.AppendJSONUnique(
		ctx, h.log, id.Org, id.Workspace, id.Actor, Stream, TypeExposureRecorded, now,
		ExposureRecorded{
			ExperimentID: assignment.ExperimentID, Cohort: assignment.Cohort,
			DecisionID: decisionID, FlowID: flowID, Environment: environment,
			ArmKey: assignment.ArmKey, ArmName: assignment.ArmName, ArmKind: assignment.ArmKind,
			Version: assignment.Version, SubjectHash: assignment.SubjectHash, ReachedAt: now,
		},
		"experiment.exposure\x00"+decisionID,
	)
}

func (h *Handler) validateSpec(ctx context.Context, id identity.Identity, spec Spec) error {
	if err := spec.Validate(); err != nil {
		return err
	}
	flow, ok, err := flows.Read(ctx, h.store, id, spec.FlowID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("experiments: unknown flow %q", spec.FlowID)
	}
	versions := make(map[int]bool, len(flow.Versions))
	for _, version := range flow.Versions {
		versions[version.Version] = true
	}
	for _, arm := range spec.Arms {
		if !versions[arm.Version] {
			return fmt.Errorf("experiments: arm %q references unpublished flow version %d", arm.Key, arm.Version)
		}
	}
	return nil
}

func (h *Handler) validateLaunch(ctx context.Context, id identity.Identity, experimentID string, target aggregate) error {
	flow, ok, err := flows.Read(ctx, h.store, id, target.spec.FlowID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("experiments: unknown flow %q", target.spec.FlowID)
	}
	champion := 0
	for _, arm := range target.spec.Arms {
		if arm.Kind == ArmChampion {
			champion = arm.Version
		}
	}
	if target.spec.Environment != "sandbox" {
		deployment, ok := flow.Deployments[target.spec.Environment]
		if !ok || deployment.Version == 0 {
			return fmt.Errorf("experiments: flow has no %s deployment", target.spec.Environment)
		}
		if deployment.Version != champion {
			return fmt.Errorf(
				"experiments: champion version %d does not match deployed version %d",
				champion, deployment.Version,
			)
		}
	}
	if deployment, ok := flow.Deployments[target.spec.Environment]; ok && deployment.ChallengerVersion != 0 {
		return fmt.Errorf("experiments: remove the legacy challenger deployment before starting a governed experiment")
	}
	all, err := h.foldAll(ctx, id)
	if err != nil {
		return err
	}
	for id, other := range all {
		if id == experimentID {
			continue
		}
		if (other.state == StateRunning || other.state == StatePendingLaunch) &&
			other.spec.FlowID == target.spec.FlowID &&
			other.spec.Environment == target.spec.Environment {
			return fmt.Errorf(
				"experiments: experiment %q already owns flow %q in %s",
				id, target.spec.FlowID, target.spec.Environment,
			)
		}
	}
	return nil
}

type aggregate struct {
	spec        Spec
	cohort      int
	state       State
	createdBy   string
	requestID   string
	requestedBy string
	stateToken  string
}

func (h *Handler) foldOne(ctx context.Context, id identity.Identity, experimentID string) (aggregate, bool, error) {
	all, err := h.foldAll(ctx, id)
	if err != nil {
		return aggregate{}, false, err
	}
	agg, ok := all[experimentID]
	return agg, ok, nil
}

func (h *Handler) foldAll(ctx context.Context, id identity.Identity) (map[string]aggregate, error) {
	events, err := h.log.ReadTenantStream(ctx, id.Org, id.Workspace, Stream, 0)
	if err != nil {
		return nil, err
	}
	all := make(map[string]aggregate)
	for _, event := range events {
		switch event.Type {
		case TypeCreated:
			p, err := decodeEvent[Created](event)
			if err != nil {
				return nil, err
			}
			all[p.ExperimentID] = aggregate{
				spec: p.Spec, cohort: 1, state: StateDraft, createdBy: event.Actor,
				stateToken: event.ID,
			}
		case TypeUpdated:
			p, err := decodeEvent[Updated](event)
			if err != nil {
				return nil, err
			}
			agg, ok := all[p.ExperimentID]
			if !ok {
				return nil, fmt.Errorf("experiments: update for unknown experiment %q", p.ExperimentID)
			}
			agg.spec, agg.cohort, agg.requestID, agg.requestedBy = p.Spec, p.Cohort, "", ""
			agg.stateToken = event.ID
			all[p.ExperimentID] = agg
		case TypeLaunchRequested:
			p, err := decodeEvent[LaunchRequested](event)
			if err != nil {
				return nil, err
			}
			agg := all[p.ExperimentID]
			agg.state, agg.requestID, agg.requestedBy = StatePendingLaunch, p.RequestID, event.Actor
			agg.stateToken = event.ID
			all[p.ExperimentID] = agg
		case TypeLaunchApproved:
			p, err := decodeEvent[LaunchApproved](event)
			if err != nil {
				return nil, err
			}
			agg := all[p.ExperimentID]
			agg.state = StateRunning
			agg.stateToken = event.ID
			all[p.ExperimentID] = agg
		case TypeLaunchRejected:
			p, err := decodeEvent[LaunchRejected](event)
			if err != nil {
				return nil, err
			}
			agg := all[p.ExperimentID]
			agg.state = StateDraft
			agg.stateToken = event.ID
			all[p.ExperimentID] = agg
		case TypeStarted, TypePaused, TypeResumed, TypeCompleted, TypeCancelled:
			p, err := decodeEvent[Transition](event)
			if err != nil {
				return nil, err
			}
			agg := all[p.ExperimentID]
			switch event.Type {
			case TypeStarted, TypeResumed:
				agg.state = StateRunning
			case TypePaused:
				agg.state = StatePaused
			case TypeCompleted:
				agg.state = StateCompleted
			case TypeCancelled:
				agg.state = StateCancelled
			}
			agg.stateToken = event.ID
			all[p.ExperimentID] = agg
		}
	}
	return all, nil
}

func stateExitClaim(experimentID, token string) string {
	return "experiment.state.exit\x00" + experimentID + "\x00" + token
}

func launchDecisionClaim(experimentID, requestID string) string {
	return "experiment.launch.decision\x00" + experimentID + "\x00" + requestID
}

func randomID() string {
	var bytes [16]byte
	if _, err := io.ReadFull(rand.Reader, bytes[:]); err != nil {
		panic("experiments: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(bytes[:])
}
