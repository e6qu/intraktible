// SPDX-License-Identifier: AGPL-3.0-or-later

package cases

import (
	"context"
	"sort"
	"time"

	"github.com/e6qu/intraktible/case-manager/domain"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// Workload is one reviewer's active load against configured capacity.
type Workload struct {
	Actor       string  `json:"actor"`
	Open        int     `json:"open"`
	Capacity    int     `json:"capacity"`
	Utilization float64 `json:"utilization"`
}

// QueueBacklog is one queue's open work and oldest age.
type QueueBacklog struct {
	Queue          string  `json:"queue"`
	Open           int     `json:"open"`
	Capacity       int     `json:"capacity"`
	Utilization    float64 `json:"utilization"`
	OldestAgeHours int64   `json:"oldest_age_hours"`
}

// OperationalAnalytics is the replay-stable Case Manager KPI surface. Durations
// derive from event-recorded timestamps; only current backlog ageing/SLA use now.
type OperationalAnalytics struct {
	Total                  int                     `json:"total"`
	Open                   int                     `json:"open"`
	Completed              int                     `json:"completed"`
	Unassigned             int                     `json:"unassigned"`
	Unrouted               int                     `json:"unrouted"`
	DueSoon                int                     `json:"due_soon"`
	Overdue                int                     `json:"overdue"`
	SLABreached            int                     `json:"sla_breached"`
	AverageFirstActionSecs float64                 `json:"average_first_action_seconds"`
	AverageResolutionSecs  float64                 `json:"average_resolution_seconds"`
	Ageing                 map[string]int          `json:"ageing"`
	ByStatus               map[string]int          `json:"by_status"`
	ByPriority             map[domain.Priority]int `json:"by_priority"`
	Workloads              []Workload              `json:"workloads"`
	Queues                 []QueueBacklog          `json:"queues"`
	QASelected             int                     `json:"qa_selected"`
	QACompleted            int                     `json:"qa_completed"`
	QADisagreements        int                     `json:"qa_disagreements"`
	QAOverrides            int                     `json:"qa_overrides"`
}

// BuildAnalytics computes the KPI surface from case and reviewer projections.
func BuildAnalytics(
	views []CaseView,
	reviewers []ReviewerView,
	queueDefinitions []QueueView,
	now time.Time,
) OperationalAnalytics {
	result := OperationalAnalytics{
		Total: len(views), Ageing: map[string]int{
			"under_24h": 0, "one_to_three_days": 0, "three_to_seven_days": 0, "over_seven_days": 0,
		},
		ByStatus: map[string]int{}, ByPriority: map[domain.Priority]int{},
	}
	firstActionTotal, firstActionCount := 0.0, 0
	resolutionTotal, resolutionCount := 0.0, 0
	workloads := map[string]int{}
	type queueState struct {
		count  int
		oldest time.Time
	}
	queueBacklogs := map[string]queueState{}
	for index := range views {
		view := views[index]
		result.ByStatus[string(view.Status)]++
		result.ByPriority[view.Priority]++
		if !view.FirstActionAt.IsZero() {
			firstActionTotal += view.FirstActionAt.Sub(view.CreatedAt).Seconds()
			firstActionCount++
		}
		if !view.ResolvedAt.IsZero() {
			resolutionTotal += view.ResolvedAt.Sub(view.CreatedAt).Seconds()
			resolutionCount++
		}
		if view.QA != nil {
			result.QASelected++
			if view.QA.Status == "completed" {
				result.QACompleted++
				if !view.QA.Agreement {
					result.QADisagreements++
				}
				if view.QA.Override {
					result.QAOverrides++
				}
			}
		}
		if view.Status == domain.StatusCompleted || !view.ResolvedAt.IsZero() {
			result.Completed++
			continue
		}
		result.Open++
		if view.Assignee == "" {
			result.Unassigned++
		} else {
			workloads[view.Assignee]++
		}
		if view.Queue == "" {
			result.Unrouted++
		}
		AnnotateSLA(&view, now)
		switch view.SLAState {
		case domain.SLADueSoon:
			result.DueSoon++
		case domain.SLAOverdue:
			result.Overdue++
		}
		if view.SLABreached {
			result.SLABreached++
		}
		age := now.Sub(view.CreatedAt)
		switch {
		case age < 24*time.Hour:
			result.Ageing["under_24h"]++
		case age < 72*time.Hour:
			result.Ageing["one_to_three_days"]++
		case age < 7*24*time.Hour:
			result.Ageing["three_to_seven_days"]++
		default:
			result.Ageing["over_seven_days"]++
		}
		queue := view.Queue
		if queue == "" {
			queue = "unrouted"
		}
		state := queueBacklogs[queue]
		state.count++
		if state.oldest.IsZero() || view.CreatedAt.Before(state.oldest) {
			state.oldest = view.CreatedAt
		}
		queueBacklogs[queue] = state
	}
	if firstActionCount > 0 {
		result.AverageFirstActionSecs = firstActionTotal / float64(firstActionCount)
	}
	if resolutionCount > 0 {
		result.AverageResolutionSecs = resolutionTotal / float64(resolutionCount)
	}
	profiles := map[string]domain.ReviewerProfile{}
	for _, reviewer := range reviewers {
		profiles[reviewer.Profile.Actor] = reviewer.Profile
	}
	actors := map[string]bool{}
	for actor := range workloads {
		actors[actor] = true
	}
	for actor := range profiles {
		actors[actor] = true
	}
	for actor := range actors {
		capacity := profiles[actor].Capacity
		utilization := 0.0
		if capacity > 0 {
			utilization = float64(workloads[actor]) / float64(capacity)
		}
		result.Workloads = append(result.Workloads, Workload{
			Actor: actor, Open: workloads[actor], Capacity: capacity, Utilization: utilization,
		})
	}
	sort.Slice(result.Workloads, func(i, j int) bool {
		if result.Workloads[i].Utilization != result.Workloads[j].Utilization {
			return result.Workloads[i].Utilization > result.Workloads[j].Utilization
		}
		return result.Workloads[i].Actor < result.Workloads[j].Actor
	})
	queueCapacity := map[string]int{}
	for _, queue := range queueDefinitions {
		queueCapacity[queue.Definition.Key] = queue.Definition.Capacity
		if _, exists := queueBacklogs[queue.Definition.Key]; !exists {
			queueBacklogs[queue.Definition.Key] = queueState{}
		}
	}
	for queue, state := range queueBacklogs {
		capacity := queueCapacity[queue]
		utilization := 0.0
		if capacity > 0 {
			utilization = float64(state.count) / float64(capacity)
		}
		oldestAge := int64(0)
		if !state.oldest.IsZero() {
			oldestAge = int64(now.Sub(state.oldest).Hours())
		}
		result.Queues = append(result.Queues, QueueBacklog{
			Queue: queue, Open: state.count, Capacity: capacity,
			Utilization: utilization, OldestAgeHours: oldestAge,
		})
	}
	sort.Slice(result.Queues, func(i, j int) bool {
		if result.Queues[i].Open != result.Queues[j].Open {
			return result.Queues[i].Open > result.Queues[j].Open
		}
		return result.Queues[i].Queue < result.Queues[j].Queue
	})
	return result
}

// Analytics reads the tenant projections and computes operational KPIs.
func Analytics(ctx context.Context, st store.Store, id identity.Identity, now time.Time) (OperationalAnalytics, error) {
	views, err := List(ctx, st, id, Filter{})
	if err != nil {
		return OperationalAnalytics{}, err
	}
	reviewers, err := ListReviewers(ctx, st, id)
	if err != nil {
		return OperationalAnalytics{}, err
	}
	queues, err := ListQueues(ctx, st, id)
	if err != nil {
		return OperationalAnalytics{}, err
	}
	return BuildAnalytics(views, reviewers, queues, now), nil
}
