// SPDX-License-Identifier: AGPL-3.0-or-later

package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

var keyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// Priority is the operational urgency of a case.
type Priority string

const (
	PriorityLow      Priority = "low"
	PriorityNormal   Priority = "normal"
	PriorityHigh     Priority = "high"
	PriorityCritical Priority = "critical"
)

// Valid reports whether p is a supported priority.
func (p Priority) Valid() bool {
	return p == PriorityLow || p == PriorityNormal || p == PriorityHigh || p == PriorityCritical
}

// Rank returns the stable routing order for a priority.
func (p Priority) Rank() int {
	switch p {
	case PriorityCritical:
		return 4
	case PriorityHigh:
		return 3
	case PriorityNormal:
		return 2
	case PriorityLow:
		return 1
	default:
		return 0
	}
}

// FieldKind is the JSON shape accepted for a case field.
type FieldKind string

const (
	FieldString  FieldKind = "string"
	FieldNumber  FieldKind = "number"
	FieldBoolean FieldKind = "boolean"
	FieldObject  FieldKind = "object"
	FieldArray   FieldKind = "array"
)

// Valid reports whether k is a supported JSON field kind.
func (k FieldKind) Valid() bool {
	return k == FieldString || k == FieldNumber || k == FieldBoolean || k == FieldObject || k == FieldArray
}

// FieldDefinition declares one versioned case field and its read policy.
type FieldDefinition struct {
	Key      string    `json:"key"`
	Label    string    `json:"label"`
	Kind     FieldKind `json:"kind"`
	Required bool      `json:"required,omitempty"`
	PII      bool      `json:"pii,omitempty"`
	ReadBy   []string  `json:"read_by,omitempty"`
}

// Transition is one permitted state-machine edge. An empty Roles list permits
// every operator; otherwise the caller's platform role must be listed.
type Transition struct {
	From  CaseStatus `json:"from"`
	To    CaseStatus `json:"to"`
	Roles []string   `json:"roles,omitempty"`
}

// DispositionDefinition declares a review outcome and its accepted reason codes.
type DispositionDefinition struct {
	Key                  string   `json:"key"`
	Label                string   `json:"label"`
	ReasonCodes          []string `json:"reason_codes"`
	TerminalState        string   `json:"terminal_state"`
	RequiresSecondReview bool     `json:"requires_second_review,omitempty"`
}

// EvidenceRequirement declares evidence that must be linked before a terminal
// disposition may be recorded.
type EvidenceRequirement struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Kinds    []string `json:"kinds"`
	Required bool     `json:"required,omitempty"`
}

// ServiceCalendar is a deterministic business-time contract. Weekdays use
// ISO-8601 numbering (1=Monday … 7=Sunday); holiday values are YYYY-MM-DD.
type ServiceCalendar struct {
	Timezone   string   `json:"timezone"`
	Weekdays   []int    `json:"weekdays"`
	StartHour  int      `json:"start_hour"`
	EndHour    int      `json:"end_hour"`
	Holidays   []string `json:"holidays,omitempty"`
	SLAHours   int      `json:"sla_hours"`
	Escalation int      `json:"escalation_hours,omitempty"`
}

// RoleLayout names the case fields a role sees first and may edit.
type RoleLayout struct {
	Role     string   `json:"role"`
	Sections []string `json:"sections"`
	Editable []string `json:"editable,omitempty"`
}

// CaseTypeDefinition is one immutable published case-type version.
type CaseTypeDefinition struct {
	Key          string                  `json:"key"`
	Name         string                  `json:"name"`
	Description  string                  `json:"description,omitempty"`
	InitialState CaseStatus              `json:"initial_state"`
	Fields       []FieldDefinition       `json:"fields"`
	Transitions  []Transition            `json:"transitions"`
	Dispositions []DispositionDefinition `json:"dispositions"`
	Priorities   []Priority              `json:"priorities"`
	Calendar     ServiceCalendar         `json:"service_calendar"`
	Evidence     []EvidenceRequirement   `json:"evidence_requirements"`
	Layouts      []RoleLayout            `json:"layouts"`
}

// Validate checks a definition without I/O.
func (d CaseTypeDefinition) Validate() error {
	if !keyPattern.MatchString(d.Key) {
		return errors.New("case-manager: case type key must match ^[a-z][a-z0-9_]{0,63}$")
	}
	if strings.TrimSpace(d.Name) == "" {
		return errors.New("case-manager: case type name is required")
	}
	if !ValidStateKey(d.InitialState) {
		return fmt.Errorf("case-manager: invalid initial state %q", d.InitialState)
	}
	if len(d.Transitions) == 0 {
		return errors.New("case-manager: at least one state transition is required")
	}
	states := map[CaseStatus]bool{d.InitialState: true}
	for _, t := range d.Transitions {
		if !ValidStateKey(t.From) || !ValidStateKey(t.To) {
			return fmt.Errorf("case-manager: invalid transition %q → %q", t.From, t.To)
		}
		states[t.From], states[t.To] = true, true
	}
	fields := make(map[string]FieldDefinition, len(d.Fields))
	for _, f := range d.Fields {
		if !keyPattern.MatchString(f.Key) || strings.TrimSpace(f.Label) == "" || !f.Kind.Valid() {
			return fmt.Errorf("case-manager: invalid field definition %q", f.Key)
		}
		if _, exists := fields[f.Key]; exists {
			return fmt.Errorf("case-manager: duplicate field %q", f.Key)
		}
		fields[f.Key] = f
	}
	dispositions := make(map[string]bool, len(d.Dispositions))
	for _, disposition := range d.Dispositions {
		if !keyPattern.MatchString(disposition.Key) || strings.TrimSpace(disposition.Label) == "" {
			return fmt.Errorf("case-manager: invalid disposition %q", disposition.Key)
		}
		if dispositions[disposition.Key] {
			return fmt.Errorf("case-manager: duplicate disposition %q", disposition.Key)
		}
		dispositions[disposition.Key] = true
		if !states[CaseStatus(disposition.TerminalState)] {
			return fmt.Errorf("case-manager: disposition %q targets unknown state %q", disposition.Key, disposition.TerminalState)
		}
		if len(disposition.ReasonCodes) == 0 {
			return fmt.Errorf("case-manager: disposition %q requires reason codes", disposition.Key)
		}
	}
	if len(d.Priorities) == 0 {
		return errors.New("case-manager: case type must permit at least one priority")
	}
	for _, priority := range d.Priorities {
		if !priority.Valid() {
			return fmt.Errorf("case-manager: invalid priority %q", priority)
		}
	}
	if err := d.Calendar.Validate(); err != nil {
		return err
	}
	requirements := map[string]bool{}
	for _, requirement := range d.Evidence {
		if !keyPattern.MatchString(requirement.Key) || strings.TrimSpace(requirement.Label) == "" || len(requirement.Kinds) == 0 {
			return fmt.Errorf("case-manager: invalid evidence requirement %q", requirement.Key)
		}
		if requirements[requirement.Key] {
			return fmt.Errorf("case-manager: duplicate evidence requirement %q", requirement.Key)
		}
		requirements[requirement.Key] = true
	}
	for _, layout := range d.Layouts {
		if strings.TrimSpace(layout.Role) == "" || len(layout.Sections) == 0 {
			return errors.New("case-manager: every role layout requires a role and sections")
		}
		for _, field := range layout.Editable {
			if _, ok := fields[field]; !ok {
				return fmt.Errorf("case-manager: layout for %q edits unknown field %q", layout.Role, field)
			}
		}
	}
	return nil
}

// Validate checks a service calendar without reading zone data; the imperative
// shell resolves Timezone and records any derived deadlines.
func (c ServiceCalendar) Validate() error {
	if strings.TrimSpace(c.Timezone) == "" {
		return errors.New("case-manager: service calendar timezone is required")
	}
	if len(c.Weekdays) == 0 || c.StartHour < 0 || c.StartHour > 23 || c.EndHour <= c.StartHour || c.EndHour > 24 {
		return errors.New("case-manager: invalid service calendar hours")
	}
	seen := map[int]bool{}
	for _, day := range c.Weekdays {
		if day < 1 || day > 7 || seen[day] {
			return errors.New("case-manager: service calendar weekdays must be unique ISO days 1..7")
		}
		seen[day] = true
	}
	if c.SLAHours <= 0 || c.SLAHours > MaxSLADays*24 {
		return fmt.Errorf("case-manager: service calendar sla_hours must be between 1 and %d", MaxSLADays*24)
	}
	if c.Escalation < 0 || c.Escalation > c.SLAHours {
		return errors.New("case-manager: escalation_hours must be between zero and sla_hours")
	}
	return nil
}

// BusinessDeadline adds a calendar's working hours to start. The caller resolves
// the configured IANA location at the I/O boundary and records the result in the
// opening event, so replay never consults mutable timezone data.
func BusinessDeadline(start time.Time, calendar ServiceCalendar, location *time.Location) (time.Time, error) {
	if err := calendar.Validate(); err != nil {
		return time.Time{}, err
	}
	if location == nil {
		return time.Time{}, errors.New("case-manager: service calendar location is required")
	}
	holidays := make(map[string]bool, len(calendar.Holidays))
	for _, holiday := range calendar.Holidays {
		if _, err := time.Parse("2006-01-02", holiday); err != nil {
			return time.Time{}, fmt.Errorf("case-manager: invalid service-calendar holiday %q", holiday)
		}
		holidays[holiday] = true
	}
	workingDay := func(t time.Time) bool {
		isoDay := int(t.Weekday())
		if isoDay == 0 {
			isoDay = 7
		}
		return slices.Contains(calendar.Weekdays, isoDay) && !holidays[t.Format("2006-01-02")]
	}
	cursor := start.In(location).Truncate(time.Hour)
	if cursor.Before(start.In(location)) {
		cursor = cursor.Add(time.Hour)
	}
	remaining := calendar.SLAHours
	// At most 27 years of hourly steps under the domain cap.
	for steps := 0; remaining > 0 && steps <= MaxSLADays*24; steps++ {
		if workingDay(cursor) && cursor.Hour() >= calendar.StartHour && cursor.Hour() < calendar.EndHour {
			remaining--
			if remaining == 0 {
				return cursor.UTC(), nil
			}
		}
		cursor = cursor.Add(time.Hour)
	}
	return time.Time{}, errors.New("case-manager: service calendar could not produce a bounded deadline")
}

// ValidateContext checks required fields and JSON kinds for one pinned type.
func (d CaseTypeDefinition) ValidateContext(raw json.RawMessage) error {
	var values map[string]any
	if len(raw) == 0 {
		values = map[string]any{}
	} else if err := json.Unmarshal(raw, &values); err != nil {
		return fmt.Errorf("case-manager: context must be a JSON object: %w", err)
	}
	for _, field := range d.Fields {
		value, present := values[field.Key]
		if field.Required && (!present || value == nil) {
			return fmt.Errorf("case-manager: required field %q is missing", field.Key)
		}
		if !present || value == nil {
			continue
		}
		if !matchesKind(field.Kind, value) {
			return fmt.Errorf("case-manager: field %q must be %s", field.Key, field.Kind)
		}
	}
	return nil
}

func matchesKind(kind FieldKind, value any) bool {
	switch kind {
	case FieldString:
		_, ok := value.(string)
		return ok
	case FieldNumber:
		_, ok := value.(float64)
		return ok
	case FieldBoolean:
		_, ok := value.(bool)
		return ok
	case FieldObject:
		_, ok := value.(map[string]any)
		return ok
	case FieldArray:
		_, ok := value.([]any)
		return ok
	default:
		return false
	}
}

// CanTransition reports whether a role may traverse an edge.
func (d CaseTypeDefinition) CanTransition(from, to CaseStatus, role string) bool {
	for _, transition := range d.Transitions {
		if transition.From == from && transition.To == to &&
			(len(transition.Roles) == 0 || slices.Contains(transition.Roles, role)) {
			return true
		}
	}
	return false
}

// AllowsPriority reports whether a priority is part of this published version.
func (d CaseTypeDefinition) AllowsPriority(priority Priority) bool {
	return slices.Contains(d.Priorities, priority)
}

// FindDisposition resolves a disposition by key.
func (d CaseTypeDefinition) FindDisposition(key string) (DispositionDefinition, bool) {
	for _, disposition := range d.Dispositions {
		if disposition.Key == key {
			return disposition, true
		}
	}
	return DispositionDefinition{}, false
}

// QueueDefinition is a durable work queue and its concurrency boundary.
type QueueDefinition struct {
	Key                 string     `json:"key"`
	Name                string     `json:"name"`
	CaseTypes           []string   `json:"case_types,omitempty"`
	Priorities          []Priority `json:"priorities,omitempty"`
	Jurisdictions       []string   `json:"jurisdictions,omitempty"`
	RequiredSkills      []string   `json:"required_skills,omitempty"`
	Capacity            int        `json:"capacity"`
	EscalationQueue     string     `json:"escalation_queue,omitempty"`
	ConflictContextKeys []string   `json:"conflict_context_keys,omitempty"`
}

// Validate checks queue configuration.
func (q QueueDefinition) Validate() error {
	if !keyPattern.MatchString(q.Key) || strings.TrimSpace(q.Name) == "" {
		return errors.New("case-manager: invalid queue key or name")
	}
	if q.Capacity < 1 || q.Capacity > 100000 {
		return errors.New("case-manager: queue capacity must be between 1 and 100000")
	}
	for _, priority := range q.Priorities {
		if !priority.Valid() {
			return fmt.Errorf("case-manager: queue %q has invalid priority %q", q.Key, priority)
		}
	}
	return nil
}

// ReviewerProfile is the routing configuration for one reviewer.
type ReviewerProfile struct {
	Actor         string   `json:"actor"`
	Skills        []string `json:"skills"`
	Jurisdictions []string `json:"jurisdictions"`
	Capacity      int      `json:"capacity"`
	Active        bool     `json:"active"`
	Conflicts     []string `json:"conflicts,omitempty"`
}

// Validate checks reviewer configuration.
func (p ReviewerProfile) Validate() error {
	if strings.TrimSpace(p.Actor) == "" || p.Capacity < 1 || p.Capacity > 10000 {
		return errors.New("case-manager: reviewer actor and capacity are required")
	}
	return nil
}

// RoutingInput is the pure router's case/reviewer workload snapshot.
type RoutingInput struct {
	CaseID       string
	CaseType     string
	Priority     Priority
	Jurisdiction string
	Context      map[string]any
	Queues       []QueueDefinition
	Reviewers    []ReviewerProfile
	OpenByActor  map[string]int
}

// RoutingDecision is the reproducible queue and assignee chosen by Route.
type RoutingDecision struct {
	Queue       string `json:"queue"`
	Assignee    string `json:"assignee,omitempty"`
	Explanation string `json:"explanation"`
}

// Route deterministically selects the first matching queue and the least-loaded
// eligible reviewer (actor is the stable tie-breaker).
func Route(input RoutingInput) (RoutingDecision, error) {
	queues := append([]QueueDefinition(nil), input.Queues...)
	slices.SortFunc(queues, func(a, b QueueDefinition) int { return strings.Compare(a.Key, b.Key) })
	for _, queue := range queues {
		if len(queue.CaseTypes) > 0 && !slices.Contains(queue.CaseTypes, input.CaseType) {
			continue
		}
		if len(queue.Priorities) > 0 && !slices.Contains(queue.Priorities, input.Priority) {
			continue
		}
		if len(queue.Jurisdictions) > 0 && !slices.Contains(queue.Jurisdictions, input.Jurisdiction) {
			continue
		}
		var candidates []ReviewerProfile
		for _, reviewer := range input.Reviewers {
			if !reviewer.Active || input.OpenByActor[reviewer.Actor] >= reviewer.Capacity {
				continue
			}
			if len(queue.Jurisdictions) > 0 && !slices.Contains(reviewer.Jurisdictions, input.Jurisdiction) {
				continue
			}
			if !containsAll(reviewer.Skills, queue.RequiredSkills) || reviewerConflicted(reviewer, queue, input.Context) {
				continue
			}
			candidates = append(candidates, reviewer)
		}
		slices.SortFunc(candidates, func(a, b ReviewerProfile) int {
			if input.OpenByActor[a.Actor] != input.OpenByActor[b.Actor] {
				return input.OpenByActor[a.Actor] - input.OpenByActor[b.Actor]
			}
			return strings.Compare(a.Actor, b.Actor)
		})
		if len(candidates) == 0 {
			return RoutingDecision{Queue: queue.Key, Explanation: "queue matched; no eligible reviewer capacity"}, nil
		}
		return RoutingDecision{
			Queue: queue.Key, Assignee: candidates[0].Actor,
			Explanation: "matched queue " + queue.Key + "; selected least-loaded eligible reviewer",
		}, nil
	}
	return RoutingDecision{}, errors.New("case-manager: no routing queue matched the case")
}

func containsAll(have, required []string) bool {
	for _, item := range required {
		if !slices.Contains(have, item) {
			return false
		}
	}
	return true
}

func reviewerConflicted(reviewer ReviewerProfile, queue QueueDefinition, context map[string]any) bool {
	for _, key := range queue.ConflictContextKeys {
		value, ok := context[key].(string)
		if ok && slices.Contains(reviewer.Conflicts, value) {
			return true
		}
	}
	return false
}
