// SPDX-License-Identifier: AGPL-3.0-or-later

package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/e6qu/intraktible/case-manager/domain"
	"github.com/e6qu/intraktible/case-manager/events"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
)

// PublishedCaseType is the exact immutable definition version resolved by a
// command or returned to an API caller.
type PublishedCaseType struct {
	Version    int
	Definition domain.CaseTypeDefinition
}

// PublishCaseType appends the next immutable definition version. The version
// claim makes two publishing replicas serialize rather than create two version
// N payloads.
func (h *Handler) PublishCaseType(ctx context.Context, id identity.Identity, definition domain.CaseTypeDefinition) (int, eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return 0, eventlog.Envelope{}, err
	}
	if err := definition.Validate(); err != nil {
		return 0, eventlog.Envelope{}, err
	}
	definitionJSON, err := json.Marshal(definition)
	if err != nil {
		return 0, eventlog.Envelope{}, fmt.Errorf("case-manager: marshal case type definition: %w", err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for attempt := 0; ; attempt++ {
		latest, found, err := h.latestCaseType(ctx, id, definition.Key)
		if err != nil {
			return 0, eventlog.Envelope{}, err
		}
		version := 1
		if found {
			version = latest.Version + 1
		}
		payload, err := json.Marshal(events.CaseTypePublished{
			Key: definition.Key, Version: version, Definition: definitionJSON,
		})
		if err != nil {
			return 0, eventlog.Envelope{}, fmt.Errorf("case-manager: marshal case type publication: %w", err)
		}
		event, err := h.appendUnique(ctx, id, events.TypeCaseTypePublished, payload, caseTypeVersionClaim(definition.Key, version))
		if errors.Is(err, eventlog.ErrConflict) && attempt < maxClaimRetries {
			continue
		}
		return version, event, err
	}
}

// ConfigureQueue replaces a queue's active routing definition.
func (h *Handler) ConfigureQueue(ctx context.Context, id identity.Identity, definition domain.QueueDefinition) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	return appendConfiguration(
		ctx, h, id, definition, "queue definition", "queue configuration", events.TypeQueueConfigured,
		func(raw json.RawMessage) any {
			return events.QueueConfigured{Key: definition.Key, Definition: raw}
		},
	)
}

// ConfigureReviewer replaces one reviewer's active routing profile.
func (h *Handler) ConfigureReviewer(ctx context.Context, id identity.Identity, profile domain.ReviewerProfile) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	return appendConfiguration(
		ctx, h, id, profile, "reviewer profile", "reviewer configuration", events.TypeReviewerConfigured,
		func(raw json.RawMessage) any {
			return events.ReviewerConfigured{Actor: profile.Actor, Profile: raw}
		},
	)
}

type configuration interface {
	Validate() error
}

func marshalConfiguration[T configuration](value T, label string) (json.RawMessage, error) {
	if err := value.Validate(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("case-manager: marshal %s: %w", label, err)
	}
	return raw, nil
}

func appendConfiguration[T configuration](
	ctx context.Context,
	h *Handler,
	id identity.Identity,
	value T,
	valueLabel, eventLabel, eventType string,
	build func(json.RawMessage) any,
) (eventlog.Envelope, error) {
	raw, err := marshalConfiguration(value, valueLabel)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	payload, err := json.Marshal(build(raw))
	if err != nil {
		return eventlog.Envelope{}, fmt.Errorf("case-manager: marshal %s: %w", eventLabel, err)
	}
	return h.append(ctx, id, eventType, payload)
}

// RouteCase atomically records one deterministic queue/assignee choice. A case
// with no matching queue fails loudly and remains visible as a routing failure.
func (h *Handler) RouteCase(ctx context.Context, id identity.Identity, caseID string) (domain.RoutingDecision, eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return domain.RoutingDecision{}, eventlog.Envelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	routingRevision, err := h.routingRevision(ctx, id)
	if err != nil {
		return domain.RoutingDecision{}, eventlog.Envelope{}, err
	}
	states, err := h.caseStates(ctx, id)
	if err != nil {
		return domain.RoutingDecision{}, eventlog.Envelope{}, err
	}
	state, found := states[caseID]
	if !found {
		return domain.RoutingDecision{}, eventlog.Envelope{}, fmt.Errorf("case-manager: unknown case %q", caseID)
	}
	if state.terminal {
		return domain.RoutingDecision{}, eventlog.Envelope{}, fmt.Errorf("case-manager: terminal case %q cannot be routed", caseID)
	}
	if state.queue != "" {
		return domain.RoutingDecision{}, eventlog.Envelope{}, fmt.Errorf("case-manager: case %q is already routed to %q", caseID, state.queue)
	}
	config, err := h.routingConfig(ctx, id)
	if err != nil {
		return domain.RoutingDecision{}, eventlog.Envelope{}, err
	}
	contextValues := map[string]any{}
	if len(state.context) > 0 {
		if err := json.Unmarshal(state.context, &contextValues); err != nil {
			return domain.RoutingDecision{}, eventlog.Envelope{}, fmt.Errorf("case-manager: decode case %q context for routing: %w", caseID, err)
		}
	}
	openByActor := map[string]int{}
	openByQueue := map[string]int{}
	for _, candidate := range states {
		if candidate.terminal {
			continue
		}
		if candidate.assignee != "" {
			openByActor[candidate.assignee]++
		}
		if candidate.queue != "" {
			openByQueue[candidate.queue]++
		}
	}
	decision, err := domain.Route(domain.RoutingInput{
		CaseID: caseID, CaseType: state.caseType, Priority: state.priority,
		Jurisdiction: state.jurisdiction, Context: contextValues,
		CreatedAt: state.createdAt, Now: h.now(),
		Queues: config.Queues, Reviewers: config.Reviewers,
		OpenByActor: openByActor, OpenByQueue: openByQueue,
	})
	if err != nil {
		return domain.RoutingDecision{}, eventlog.Envelope{}, err
	}
	payload, err := json.Marshal(events.CaseRouted{
		CaseID: caseID, Queue: decision.Queue, Assignee: decision.Assignee, Explanation: decision.Explanation,
	})
	if err != nil {
		return domain.RoutingDecision{}, eventlog.Envelope{}, fmt.Errorf("case-manager: marshal route: %w", err)
	}
	event, err := h.appendUnique(
		ctx, id, events.TypeCaseRouted, payload,
		routingClaim(id, routingRevision),
	)
	if err != nil {
		if errors.Is(err, eventlog.ErrConflict) {
			return domain.RoutingDecision{}, eventlog.Envelope{}, fmt.Errorf("case-manager: case %q was routed by another replica: %w", caseID, err)
		}
		return domain.RoutingDecision{}, eventlog.Envelope{}, err
	}
	return decision, event, nil
}

// RoutePending routes every unrouted open case for one tenant in stable urgency
// order. Individual failures are returned so operators can see and repair bad
// configuration without hiding successfully routed work.
func (h *Handler) RoutePending(ctx context.Context, id identity.Identity) (map[string]string, []string, error) {
	if err := id.Valid(); err != nil {
		return nil, nil, err
	}
	config, err := h.routingConfig(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	// No queue configuration means this tenant intentionally uses manual queue
	// management; routing is opt-in and does not manufacture a hidden default.
	if len(config.Queues) == 0 {
		return map[string]string{}, []string{}, nil
	}
	h.mu.Lock()
	states, err := h.caseStates(ctx, id)
	h.mu.Unlock()
	if err != nil {
		return nil, nil, err
	}
	type candidate struct {
		id       string
		priority domain.Priority
		created  time.Time
	}
	var pending []candidate
	for caseID, state := range states {
		if state.queue == "" && !state.terminal {
			pending = append(pending, candidate{id: caseID, priority: state.priority, created: state.createdAt})
		}
	}
	sort.Slice(pending, func(i, j int) bool {
		if pending[i].priority.Rank() != pending[j].priority.Rank() {
			return pending[i].priority.Rank() > pending[j].priority.Rank()
		}
		if !pending[i].created.Equal(pending[j].created) {
			return pending[i].created.Before(pending[j].created)
		}
		return pending[i].id < pending[j].id
	})
	routed := map[string]string{}
	var failures []string
	for _, item := range pending {
		decision, _, err := h.RouteCase(ctx, id, item.id)
		if err != nil {
			if errors.Is(err, eventlog.ErrConflict) {
				continue
			}
			failures = append(failures, item.id+": "+err.Error())
			continue
		}
		routed[item.id] = decision.Queue
	}
	return routed, failures, nil
}

// EscalateBreached moves newly breached work into each source queue's configured
// escalation queue and atomically assigns an eligible reviewer. The transition is
// claimed by case/source/target so competing scheduler replicas cannot move the
// same case twice.
func (h *Handler) EscalateBreached(
	ctx context.Context,
	id identity.Identity,
	caseIDs []string,
) (map[string]string, []string, error) {
	if err := id.Valid(); err != nil {
		return nil, nil, err
	}
	config, err := h.routingConfig(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	queues := make(map[string]domain.QueueDefinition, len(config.Queues))
	for _, queue := range config.Queues {
		queues[queue.Key] = queue
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	routingRevision, err := h.routingRevision(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	states, err := h.caseStates(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	loads := map[string]int{}
	queueLoads := map[string]int{}
	for _, state := range states {
		if state.terminal {
			continue
		}
		if state.assignee != "" {
			loads[state.assignee]++
		}
		if state.queue != "" {
			queueLoads[state.queue]++
		}
	}
	now := h.now()
	moved := map[string]string{}
	var failures []string
	sorted := append([]string(nil), caseIDs...)
	if len(sorted) == 0 {
		for caseID, state := range states {
			if state.breached && !state.terminal {
				sorted = append(sorted, caseID)
			}
		}
	}
	sort.Strings(sorted)
	for _, caseID := range sorted {
		state, found := states[caseID]
		if !found {
			failures = append(failures, caseID+": case disappeared before queue escalation")
			continue
		}
		if state.terminal || !state.breached {
			continue
		}
		source, found := queues[state.queue]
		if !found || source.EscalationQueue == "" {
			continue
		}
		target, found := queues[source.EscalationQueue]
		if !found {
			failures = append(failures, caseID+": escalation queue "+source.EscalationQueue+" is not configured")
			continue
		}
		contextValues := map[string]any{}
		if len(state.context) > 0 {
			if err := json.Unmarshal(state.context, &contextValues); err != nil {
				return moved, failures, fmt.Errorf("case-manager: decode case %q context for escalation: %w", caseID, err)
			}
		}
		if state.assignee != "" {
			loads[state.assignee]--
		}
		if state.queue != "" {
			queueLoads[state.queue]--
		}
		decision, routeErr := domain.Route(domain.RoutingInput{
			CaseID: caseID, CaseType: state.caseType, Priority: state.priority,
			Jurisdiction: state.jurisdiction, Context: contextValues,
			CreatedAt: state.createdAt, Now: now,
			Queues: []domain.QueueDefinition{target}, Reviewers: config.Reviewers,
			OpenByActor: loads, OpenByQueue: queueLoads,
		})
		if routeErr != nil || decision.Assignee == "" {
			if state.assignee != "" {
				loads[state.assignee]++
			}
			if state.queue != "" {
				queueLoads[state.queue]++
			}
			message := "no eligible escalation reviewer capacity"
			if routeErr != nil {
				message = routeErr.Error()
			}
			failures = append(failures, caseID+": "+message)
			continue
		}
		decision.Explanation = "SLA breach from queue " + state.queue + "; " + decision.Explanation
		payload, err := json.Marshal(events.CaseRouted{
			CaseID: caseID, Queue: decision.Queue,
			Assignee: decision.Assignee, Explanation: decision.Explanation,
		})
		if err != nil {
			return moved, failures, fmt.Errorf("case-manager: marshal escalation route: %w", err)
		}
		if _, err := h.appendUnique(
			ctx, id, events.TypeCaseRouted, payload,
			routingClaim(id, routingRevision),
		); err != nil {
			if errors.Is(err, eventlog.ErrConflict) {
				// Another replica changed the workload snapshot. Stop this
				// batch; the next scheduler tick re-folds authoritative loads.
				return moved, failures, nil
			}
			return moved, failures, err
		}
		routingRevision++
		loads[decision.Assignee]++
		queueLoads[decision.Queue]++
		moved[caseID] = decision.Queue
	}
	return moved, failures, nil
}

// Rebalance moves unassigned, inactive-reviewer, and over-capacity work through
// the deterministic router. Higher-priority/older work keeps existing capacity;
// lower-priority/newer overflow is moved first.
func (h *Handler) Rebalance(ctx context.Context, id identity.Identity) (map[string]string, []string, error) {
	if err := id.Valid(); err != nil {
		return nil, nil, err
	}
	config, err := h.routingConfig(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if len(config.Queues) == 0 {
		return nil, nil, errors.New("case-manager: queue rebalance requires routing configuration")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	routingRevision, err := h.routingRevision(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	states, err := h.caseStates(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	profiles := map[string]domain.ReviewerProfile{}
	for _, profile := range config.Reviewers {
		profiles[profile.Actor] = profile
	}
	assigned := map[string][]string{}
	for caseID, state := range states {
		if !state.terminal && state.assignee != "" {
			assigned[state.assignee] = append(assigned[state.assignee], caseID)
		}
	}
	move := map[string]bool{}
	for actor, caseIDs := range assigned {
		profile, exists := profiles[actor]
		if !exists || !profile.Active {
			for _, caseID := range caseIDs {
				move[caseID] = true
			}
			continue
		}
		sort.Slice(caseIDs, func(i, j int) bool {
			a, b := states[caseIDs[i]], states[caseIDs[j]]
			if a.priority.Rank() != b.priority.Rank() {
				return a.priority.Rank() > b.priority.Rank()
			}
			if !a.createdAt.Equal(b.createdAt) {
				return a.createdAt.Before(b.createdAt)
			}
			return caseIDs[i] < caseIDs[j]
		})
		for index := profile.Capacity; index < len(caseIDs); index++ {
			move[caseIDs[index]] = true
		}
	}
	for caseID, state := range states {
		if !state.terminal && state.assignee == "" {
			move[caseID] = true
		}
	}
	loads := map[string]int{}
	queueLoads := map[string]int{}
	for actor, caseIDs := range assigned {
		for _, caseID := range caseIDs {
			if !move[caseID] {
				loads[actor]++
			}
		}
	}
	for caseID, state := range states {
		if !state.terminal && state.queue != "" && !move[caseID] {
			queueLoads[state.queue]++
		}
	}
	now := h.now()
	caseIDs := make([]string, 0, len(move))
	for caseID := range move {
		caseIDs = append(caseIDs, caseID)
	}
	sort.Slice(caseIDs, func(i, j int) bool {
		a, b := states[caseIDs[i]], states[caseIDs[j]]
		if a.priority.Rank() != b.priority.Rank() {
			return a.priority.Rank() > b.priority.Rank()
		}
		if !a.createdAt.Equal(b.createdAt) {
			return a.createdAt.Before(b.createdAt)
		}
		return caseIDs[i] < caseIDs[j]
	})
	moved := map[string]string{}
	var failures []string
	for _, caseID := range caseIDs {
		state := states[caseID]
		contextValues := map[string]any{}
		if len(state.context) > 0 {
			if err := json.Unmarshal(state.context, &contextValues); err != nil {
				return moved, failures, fmt.Errorf("case-manager: decode case %q context for rebalance: %w", caseID, err)
			}
		}
		decision, err := domain.Route(domain.RoutingInput{
			CaseID: caseID, CaseType: state.caseType, Priority: state.priority,
			Jurisdiction: state.jurisdiction, Context: contextValues,
			CreatedAt: state.createdAt, Now: now,
			Queues: config.Queues, Reviewers: config.Reviewers,
			OpenByActor: loads, OpenByQueue: queueLoads,
		})
		if err != nil || decision.Assignee == "" {
			message := "no eligible reviewer capacity"
			if err != nil {
				message = err.Error()
			}
			failures = append(failures, caseID+": "+message)
			continue
		}
		if decision.Assignee == state.assignee && decision.Queue == state.queue {
			loads[decision.Assignee]++
			continue
		}
		decision.Explanation = "queue rebalance; " + decision.Explanation
		payload, err := json.Marshal(events.CaseRouted{
			CaseID: caseID, Queue: decision.Queue,
			Assignee: decision.Assignee, Explanation: decision.Explanation,
		})
		if err != nil {
			return moved, failures, fmt.Errorf("case-manager: marshal rebalance route: %w", err)
		}
		if _, err := h.appendUnique(
			ctx, id, events.TypeCaseRouted, payload,
			routingClaim(id, routingRevision),
		); err != nil {
			if errors.Is(err, eventlog.ErrConflict) {
				// Do not route later items using queue/reviewer loads derived
				// before the winning replica's append.
				return moved, failures, nil
			}
			return moved, failures, err
		}
		routingRevision++
		loads[decision.Assignee]++
		queueLoads[decision.Queue]++
		moved[caseID] = decision.Assignee
	}
	return moved, failures, nil
}

type routingSnapshot struct {
	Queues    []domain.QueueDefinition
	Reviewers []domain.ReviewerProfile
}

func (h *Handler) routingConfig(ctx context.Context, id identity.Identity) (routingSnapshot, error) {
	eventsInLog, err := h.log.Read(ctx, 0)
	if err != nil {
		return routingSnapshot{}, fmt.Errorf("case-manager: read routing config: %w", err)
	}
	queues := map[string]domain.QueueDefinition{}
	reviewers := map[string]domain.ReviewerProfile{}
	for _, event := range eventsInLog {
		if event.Org != id.Org || event.Workspace != id.Workspace {
			continue
		}
		switch event.Type {
		case events.TypeQueueConfigured:
			var payload events.QueueConfigured
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return routingSnapshot{}, fmt.Errorf("case-manager: decode queue seq %d: %w", event.Seq, err)
			}
			definition, err := decodeRecordedConfiguration[domain.QueueDefinition](
				payload.Definition, event.Seq, "queue definition",
			)
			if err != nil {
				return routingSnapshot{}, err
			}
			queues[payload.Key] = definition
		case events.TypeReviewerConfigured:
			var payload events.ReviewerConfigured
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return routingSnapshot{}, fmt.Errorf("case-manager: decode reviewer seq %d: %w", event.Seq, err)
			}
			profile, err := decodeRecordedConfiguration[domain.ReviewerProfile](
				payload.Profile, event.Seq, "reviewer profile",
			)
			if err != nil {
				return routingSnapshot{}, err
			}
			reviewers[payload.Actor] = profile
		}
	}
	snapshot := routingSnapshot{
		Queues:    make([]domain.QueueDefinition, 0, len(queues)),
		Reviewers: make([]domain.ReviewerProfile, 0, len(reviewers)),
	}
	for _, queue := range queues {
		snapshot.Queues = append(snapshot.Queues, queue)
	}
	for _, reviewer := range reviewers {
		snapshot.Reviewers = append(snapshot.Reviewers, reviewer)
	}
	return snapshot, nil
}

func decodeRecordedConfiguration[T configuration](raw json.RawMessage, seq uint64, label string) (T, error) {
	var value T
	if err := json.Unmarshal(raw, &value); err != nil {
		return value, fmt.Errorf("case-manager: decode %s seq %d: %w", label, seq, err)
	}
	if err := value.Validate(); err != nil {
		return value, fmt.Errorf("case-manager: invalid %s seq %d: %w", label, seq, err)
	}
	return value, nil
}

func (h *Handler) latestCaseType(ctx context.Context, id identity.Identity, key string) (PublishedCaseType, bool, error) {
	eventsInLog, err := h.log.Read(ctx, 0)
	if err != nil {
		return PublishedCaseType{}, false, fmt.Errorf("case-manager: read case types: %w", err)
	}
	var latest PublishedCaseType
	for _, event := range eventsInLog {
		if event.Org != id.Org || event.Workspace != id.Workspace || event.Type != events.TypeCaseTypePublished {
			continue
		}
		var payload events.CaseTypePublished
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return PublishedCaseType{}, false, fmt.Errorf("case-manager: decode case type seq %d: %w", event.Seq, err)
		}
		if payload.Key != key || payload.Version <= latest.Version {
			continue
		}
		var definition domain.CaseTypeDefinition
		if err := json.Unmarshal(payload.Definition, &definition); err != nil {
			return PublishedCaseType{}, false, fmt.Errorf("case-manager: decode case type definition seq %d: %w", event.Seq, err)
		}
		if err := definition.Validate(); err != nil {
			return PublishedCaseType{}, false, fmt.Errorf("case-manager: invalid case type definition seq %d: %w", event.Seq, err)
		}
		latest = PublishedCaseType{Version: payload.Version, Definition: definition}
	}
	return latest, latest.Version > 0, nil
}

func caseTypeVersionClaim(key string, version int) string {
	return "case.type\x00" + key + "\x00" + strconv.Itoa(version)
}

func (h *Handler) routingRevision(ctx context.Context, id identity.Identity) (int, error) {
	recorded, err := h.log.Read(ctx, 0)
	if err != nil {
		return 0, fmt.Errorf("case-manager: read routing revision: %w", err)
	}
	revision := 0
	for _, event := range recorded {
		if event.Org == id.Org && event.Workspace == id.Workspace && event.Type == events.TypeCaseRouted {
			revision++
		}
	}
	return revision, nil
}

func routingClaim(id identity.Identity, revision int) string {
	return "case.routing\x00" + id.Org + "\x00" + id.Workspace + "\x00" + strconv.Itoa(revision)
}
