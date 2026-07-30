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
	if err := definition.Validate(); err != nil {
		return eventlog.Envelope{}, err
	}
	raw, err := json.Marshal(definition)
	if err != nil {
		return eventlog.Envelope{}, fmt.Errorf("case-manager: marshal queue definition: %w", err)
	}
	payload, err := json.Marshal(events.QueueConfigured{Key: definition.Key, Definition: raw})
	if err != nil {
		return eventlog.Envelope{}, fmt.Errorf("case-manager: marshal queue configuration: %w", err)
	}
	return h.append(ctx, id, events.TypeQueueConfigured, payload)
}

// ConfigureReviewer replaces one reviewer's active routing profile.
func (h *Handler) ConfigureReviewer(ctx context.Context, id identity.Identity, profile domain.ReviewerProfile) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if err := profile.Validate(); err != nil {
		return eventlog.Envelope{}, err
	}
	raw, err := json.Marshal(profile)
	if err != nil {
		return eventlog.Envelope{}, fmt.Errorf("case-manager: marshal reviewer profile: %w", err)
	}
	payload, err := json.Marshal(events.ReviewerConfigured{Actor: profile.Actor, Profile: raw})
	if err != nil {
		return eventlog.Envelope{}, fmt.Errorf("case-manager: marshal reviewer configuration: %w", err)
	}
	return h.append(ctx, id, events.TypeReviewerConfigured, payload)
}

// RouteCase atomically records one deterministic queue/assignee choice. A case
// with no matching queue fails loudly and remains visible as a routing failure.
func (h *Handler) RouteCase(ctx context.Context, id identity.Identity, caseID string) (domain.RoutingDecision, eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return domain.RoutingDecision{}, eventlog.Envelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	states, err := h.caseStates(ctx, id)
	if err != nil {
		return domain.RoutingDecision{}, eventlog.Envelope{}, err
	}
	state, found := states[caseID]
	if !found {
		return domain.RoutingDecision{}, eventlog.Envelope{}, fmt.Errorf("case-manager: unknown case %q", caseID)
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
	for _, candidate := range states {
		if candidate.status != domain.StatusCompleted && candidate.assignee != "" {
			openByActor[candidate.assignee]++
		}
	}
	decision, err := domain.Route(domain.RoutingInput{
		CaseID: caseID, CaseType: state.caseType, Priority: state.priority,
		Jurisdiction: state.jurisdiction, Context: contextValues,
		Queues: config.Queues, Reviewers: config.Reviewers, OpenByActor: openByActor,
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
	claim := routeClaim(caseID)
	if decision.Assignee != "" {
		claim = assignClaim(caseID, state.assignCount)
	}
	event, err := h.appendUnique(ctx, id, events.TypeCaseRouted, payload, claim)
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
		if state.queue == "" && state.status != domain.StatusCompleted {
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
			var definition domain.QueueDefinition
			if err := json.Unmarshal(payload.Definition, &definition); err != nil {
				return routingSnapshot{}, fmt.Errorf("case-manager: decode queue definition seq %d: %w", event.Seq, err)
			}
			if err := definition.Validate(); err != nil {
				return routingSnapshot{}, fmt.Errorf("case-manager: invalid queue definition seq %d: %w", event.Seq, err)
			}
			queues[payload.Key] = definition
		case events.TypeReviewerConfigured:
			var payload events.ReviewerConfigured
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return routingSnapshot{}, fmt.Errorf("case-manager: decode reviewer seq %d: %w", event.Seq, err)
			}
			var profile domain.ReviewerProfile
			if err := json.Unmarshal(payload.Profile, &profile); err != nil {
				return routingSnapshot{}, fmt.Errorf("case-manager: decode reviewer profile seq %d: %w", event.Seq, err)
			}
			if err := profile.Validate(); err != nil {
				return routingSnapshot{}, fmt.Errorf("case-manager: invalid reviewer profile seq %d: %w", event.Seq, err)
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

func routeClaim(caseID string) string { return "case.route\x00" + caseID }
