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

// EvidenceKind is a governed relationship or external attachment category.
type EvidenceKind string

const (
	EvidenceDecision   EvidenceKind = "decision"
	EvidenceEntity     EvidenceKind = "entity"
	EvidenceAgentRun   EvidenceKind = "agent_run"
	EvidenceConnector  EvidenceKind = "connector"
	EvidenceCase       EvidenceKind = "case"
	EvidenceAlert      EvidenceKind = "alert"
	EvidenceAttachment EvidenceKind = "attachment"
)

// Valid reports whether k is a supported case-evidence category.
func (k EvidenceKind) Valid() bool {
	return k == EvidenceDecision || k == EvidenceEntity || k == EvidenceAgentRun ||
		k == EvidenceConnector || k == EvidenceCase || k == EvidenceAlert || k == EvidenceAttachment
}

// Linkable reports whether k identifies another record rather than attached
// metadata, which uses the dedicated attachment command.
func (k EvidenceKind) Linkable() bool {
	return k.Valid() && k != EvidenceAttachment
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

// CaseAssistKind is the bounded governed-assistance task requested by an
// immutable case-type or routing-queue policy.
type CaseAssistKind string

const (
	CaseAssistSummary          CaseAssistKind = "summary"
	CaseAssistEvidenceExtract  CaseAssistKind = "evidence_extraction"
	CaseAssistPrioritization   CaseAssistKind = "prioritization"
	CaseAssistNextBestAction   CaseAssistKind = "next_best_action"
	CaseAssistDraftDisposition CaseAssistKind = "draft_disposition"
)

func (k CaseAssistKind) Valid() bool {
	switch k {
	case CaseAssistSummary, CaseAssistEvidenceExtract, CaseAssistPrioritization,
		CaseAssistNextBestAction, CaseAssistDraftDisposition:
		return true
	default:
		return false
	}
}

// CaseAssistEnvironment is the exact deployment environment a policy may use.
type CaseAssistEnvironment string

const (
	CaseAssistSandbox    CaseAssistEnvironment = "sandbox"
	CaseAssistStaging    CaseAssistEnvironment = "staging"
	CaseAssistProduction CaseAssistEnvironment = "production"
)

func (e CaseAssistEnvironment) Valid() bool {
	return e == CaseAssistSandbox || e == CaseAssistStaging ||
		e == CaseAssistProduction
}

// AssistAutomation declares an eligible asynchronous assist. Template policy
// is immutable with the case type or queue, while the active exact release is
// resolved and recorded at admission. Case work never waits for this policy.
type AssistAutomation struct {
	Key                  string                `json:"key"`
	Kind                 CaseAssistKind        `json:"kind"`
	TemplateID           string                `json:"template_id"`
	Environment          CaseAssistEnvironment `json:"environment"`
	EvidenceRequirements []string              `json:"evidence_requirements"`
}

func (a AssistAutomation) Validate() error {
	if !keyPattern.MatchString(a.Key) {
		return errors.New(
			"case-manager: assist policy key must match ^[a-z][a-z0-9_]{0,63}$",
		)
	}
	if !a.Kind.Valid() || strings.TrimSpace(a.TemplateID) == "" ||
		!a.Environment.Valid() {
		return errors.New(
			"case-manager: assist policy kind, template_id, and environment are required",
		)
	}
	if len(a.EvidenceRequirements) == 0 {
		return errors.New(
			"case-manager: assist policy requires at least one governed evidence requirement",
		)
	}
	if duplicate, found := duplicateString(a.EvidenceRequirements); found {
		return fmt.Errorf(
			"case-manager: assist policy %q has blank or duplicate evidence requirement %q",
			a.Key, duplicate,
		)
	}
	return nil
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
	Assists      []AssistAutomation      `json:"assist_automations,omitempty"`
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
		if duplicate, found := duplicateString(t.Roles); found {
			return fmt.Errorf(
				"case-manager: transition %q → %q has blank or duplicate role %q",
				t.From, t.To, duplicate,
			)
		}
		for _, role := range t.Roles {
			if !validPlatformRole(role) {
				return fmt.Errorf("case-manager: transition %q → %q has invalid role %q", t.From, t.To, role)
			}
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
		readers := map[string]bool{}
		for _, role := range f.ReadBy {
			if !validPlatformRole(role) || readers[role] {
				return fmt.Errorf("case-manager: field %q has invalid or duplicate read role %q", f.Key, role)
			}
			readers[role] = true
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
		reasons := make(map[string]bool, len(disposition.ReasonCodes))
		for _, reason := range disposition.ReasonCodes {
			if !keyPattern.MatchString(reason) {
				return fmt.Errorf("case-manager: disposition %q has invalid reason code %q", disposition.Key, reason)
			}
			if reasons[reason] {
				return fmt.Errorf("case-manager: disposition %q has duplicate reason code %q", disposition.Key, reason)
			}
			reasons[reason] = true
		}
	}
	if d.IsTerminal(d.InitialState) {
		return fmt.Errorf("case-manager: initial state %q must have an outgoing transition and cannot be terminal", d.InitialState)
	}
	if len(d.Priorities) == 0 {
		return errors.New("case-manager: case type must permit at least one priority")
	}
	priorities := make(map[Priority]bool, len(d.Priorities))
	for _, priority := range d.Priorities {
		if !priority.Valid() {
			return fmt.Errorf("case-manager: invalid priority %q", priority)
		}
		if priorities[priority] {
			return fmt.Errorf("case-manager: duplicate priority %q", priority)
		}
		priorities[priority] = true
	}
	if err := d.Calendar.Validate(); err != nil {
		return err
	}
	requirements := map[string]EvidenceRequirement{}
	for _, requirement := range d.Evidence {
		if !keyPattern.MatchString(requirement.Key) || strings.TrimSpace(requirement.Label) == "" || len(requirement.Kinds) == 0 {
			return fmt.Errorf("case-manager: invalid evidence requirement %q", requirement.Key)
		}
		if _, exists := requirements[requirement.Key]; exists {
			return fmt.Errorf("case-manager: duplicate evidence requirement %q", requirement.Key)
		}
		kinds := map[string]bool{}
		for _, kind := range requirement.Kinds {
			if !EvidenceKind(kind).Valid() {
				return fmt.Errorf("case-manager: evidence requirement %q has invalid kind %q", requirement.Key, kind)
			}
			if kinds[kind] {
				return fmt.Errorf("case-manager: evidence requirement %q repeats kind %q", requirement.Key, kind)
			}
			kinds[kind] = true
		}
		requirements[requirement.Key] = requirement
	}
	assistKeys := map[string]bool{}
	for _, assist := range d.Assists {
		if err := assist.Validate(); err != nil {
			return err
		}
		if assistKeys[assist.Key] {
			return fmt.Errorf("case-manager: duplicate assist policy %q", assist.Key)
		}
		assistKeys[assist.Key] = true
		for _, requirement := range assist.EvidenceRequirements {
			configured, exists := requirements[requirement]
			if !exists {
				return fmt.Errorf(
					"case-manager: assist policy %q references unknown evidence requirement %q",
					assist.Key, requirement,
				)
			}
			linkable := false
			for _, kind := range configured.Kinds {
				if EvidenceKind(kind).Linkable() {
					linkable = true
					break
				}
			}
			if !linkable {
				return fmt.Errorf(
					"case-manager: assist policy %q evidence requirement %q has no linkable kind",
					assist.Key, requirement,
				)
			}
		}
	}
	layouts := make(map[string]bool, len(d.Layouts))
	for _, layout := range d.Layouts {
		if !validPlatformRole(layout.Role) || len(layout.Sections) == 0 {
			return errors.New("case-manager: every role layout requires a role and sections")
		}
		if layouts[layout.Role] {
			return fmt.Errorf("case-manager: duplicate layout for role %q", layout.Role)
		}
		layouts[layout.Role] = true
		sections := make(map[string]bool, len(layout.Sections))
		for _, field := range layout.Sections {
			if _, ok := fields[field]; !ok {
				return fmt.Errorf("case-manager: layout for %q shows unknown field %q", layout.Role, field)
			}
			if sections[field] {
				return fmt.Errorf("case-manager: layout for %q repeats field %q", layout.Role, field)
			}
			sections[field] = true
		}
		editable := map[string]bool{}
		for _, field := range layout.Editable {
			if _, ok := fields[field]; !ok {
				return fmt.Errorf("case-manager: layout for %q edits unknown field %q", layout.Role, field)
			}
			if !sections[field] {
				return fmt.Errorf("case-manager: layout for %q edits hidden field %q", layout.Role, field)
			}
			if editable[field] {
				return fmt.Errorf("case-manager: layout for %q repeats editable field %q", layout.Role, field)
			}
			editable[field] = true
			definition := fields[field]
			if definition.PII && !slices.Contains(definition.ReadBy, layout.Role) {
				return fmt.Errorf("case-manager: layout for %q edits unreadable PII field %q", layout.Role, field)
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
	holidays := map[string]bool{}
	for _, holiday := range c.Holidays {
		if _, err := time.Parse("2006-01-02", holiday); err != nil {
			return fmt.Errorf("case-manager: invalid service-calendar holiday %q", holiday)
		}
		if holidays[holiday] {
			return fmt.Errorf("case-manager: duplicate service-calendar holiday %q", holiday)
		}
		holidays[holiday] = true
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

// PatchContext applies a JSON-object patch without mutating either input. Null is
// an explicit field value, not a delete marker; required-field validation decides
// whether the resulting value is permitted.
func PatchContext(current, patch json.RawMessage) (json.RawMessage, error) {
	values := map[string]any{}
	if len(current) > 0 {
		if err := json.Unmarshal(current, &values); err != nil {
			return nil, fmt.Errorf("case-manager: decode current context: %w", err)
		}
		if values == nil {
			values = map[string]any{}
		}
	}
	var changes map[string]any
	if len(patch) == 0 {
		return nil, errors.New("case-manager: field update must be a JSON object")
	}
	if err := json.Unmarshal(patch, &changes); err != nil {
		return nil, fmt.Errorf("case-manager: decode field update: %w", err)
	}
	if changes == nil {
		return nil, errors.New("case-manager: field update must be a JSON object")
	}
	if len(changes) == 0 {
		return nil, errors.New("case-manager: field update cannot be empty")
	}
	for key, value := range changes {
		values[key] = value
	}
	merged, err := json.Marshal(values)
	if err != nil {
		return nil, fmt.Errorf("case-manager: encode updated context: %w", err)
	}
	return merged, nil
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

// IsDispositionTerminal reports whether state is reserved for a reasoned
// disposition. Generic status changes must not bypass evidence, reason-code,
// and second-review gates by transitioning directly into one of these states.
func (d CaseTypeDefinition) IsDispositionTerminal(state CaseStatus) bool {
	for _, disposition := range d.Dispositions {
		if CaseStatus(disposition.TerminalState) == state {
			return true
		}
	}
	return false
}

// IsTerminal reports whether a state closes operational work. Published
// disposition targets are terminal even if a later administrative transition
// is configured; a state with no outgoing transition is also terminal.
func (d CaseTypeDefinition) IsTerminal(state CaseStatus) bool {
	if d.IsDispositionTerminal(state) {
		return true
	}
	for _, transition := range d.Transitions {
		if transition.From == state {
			return false
		}
	}
	return true
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
	Key                 string             `json:"key"`
	Name                string             `json:"name"`
	Order               int                `json:"order,omitempty"`
	CaseTypes           []string           `json:"case_types,omitempty"`
	Priorities          []Priority         `json:"priorities,omitempty"`
	Jurisdictions       []string           `json:"jurisdictions,omitempty"`
	RequiredSkills      []string           `json:"required_skills,omitempty"`
	Capacity            int                `json:"capacity"`
	EscalationQueue     string             `json:"escalation_queue,omitempty"`
	MinAgeHours         int                `json:"min_age_hours,omitempty"`
	MaxAgeHours         int                `json:"max_age_hours,omitempty"`
	ConflictContextKeys []string           `json:"conflict_context_keys,omitempty"`
	ContextEquals       map[string]string  `json:"context_equals,omitempty"`
	Assists             []AssistAutomation `json:"assist_automations,omitempty"`
}

// Validate checks queue configuration.
func (q QueueDefinition) Validate() error {
	if !keyPattern.MatchString(q.Key) || strings.TrimSpace(q.Name) == "" {
		return errors.New("case-manager: invalid queue key or name")
	}
	if q.Capacity < 1 || q.Capacity > 100000 {
		return errors.New("case-manager: queue capacity must be between 1 and 100000")
	}
	if q.Order < 0 || q.Order > 100000 {
		return fmt.Errorf("case-manager: queue %q order must be between zero and 100000", q.Key)
	}
	for _, priority := range q.Priorities {
		if !priority.Valid() {
			return fmt.Errorf("case-manager: queue %q has invalid priority %q", q.Key, priority)
		}
	}
	if duplicate, found := duplicateString(q.CaseTypes); found {
		return fmt.Errorf("case-manager: queue %q has blank or duplicate case type %q", q.Key, duplicate)
	}
	prioritySeen := map[Priority]bool{}
	for _, priority := range q.Priorities {
		if prioritySeen[priority] {
			return fmt.Errorf("case-manager: queue %q has duplicate priority %q", q.Key, priority)
		}
		prioritySeen[priority] = true
	}
	for _, group := range []struct {
		label  string
		values []string
	}{
		{label: "jurisdiction", values: q.Jurisdictions},
		{label: "required skill", values: q.RequiredSkills},
		{label: "conflict context key", values: q.ConflictContextKeys},
	} {
		if duplicate, found := duplicateString(group.values); found {
			return fmt.Errorf("case-manager: queue %q has blank or duplicate %s %q", q.Key, group.label, duplicate)
		}
	}
	assistKeys := map[string]bool{}
	for _, assist := range q.Assists {
		if err := assist.Validate(); err != nil {
			return fmt.Errorf("case-manager: queue %q: %w", q.Key, err)
		}
		if assistKeys[assist.Key] {
			return fmt.Errorf(
				"case-manager: queue %q has duplicate assist policy %q", q.Key, assist.Key,
			)
		}
		assistKeys[assist.Key] = true
	}
	if q.MinAgeHours < 0 || q.MaxAgeHours < 0 {
		return fmt.Errorf("case-manager: queue %q age bounds cannot be negative", q.Key)
	}
	if q.MaxAgeHours > 0 && q.MinAgeHours > q.MaxAgeHours {
		return fmt.Errorf("case-manager: queue %q min_age_hours exceeds max_age_hours", q.Key)
	}
	if q.EscalationQueue != "" && !keyPattern.MatchString(q.EscalationQueue) {
		return fmt.Errorf("case-manager: queue %q has invalid escalation queue %q", q.Key, q.EscalationQueue)
	}
	if q.EscalationQueue == q.Key {
		return fmt.Errorf("case-manager: queue %q cannot escalate to itself", q.Key)
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
	for _, group := range []struct {
		label  string
		values []string
	}{
		{label: "skill", values: p.Skills},
		{label: "jurisdiction", values: p.Jurisdictions},
		{label: "conflict", values: p.Conflicts},
	} {
		if duplicate, found := duplicateString(group.values); found {
			return fmt.Errorf("case-manager: reviewer %q has blank or duplicate %s %q", p.Actor, group.label, duplicate)
		}
	}
	return nil
}

func duplicateString(values []string) (string, bool) {
	seen := map[string]bool{}
	for _, value := range values {
		if strings.TrimSpace(value) == "" || seen[value] {
			return value, true
		}
		seen[value] = true
	}
	return "", false
}

func validPlatformRole(role string) bool {
	return role == "viewer" || role == "operator" || role == "editor" ||
		role == "approver" || role == "admin"
}

// RoutingInput is the pure router's case/reviewer workload snapshot.
type RoutingInput struct {
	CaseID       string
	CaseType     string
	Priority     Priority
	Jurisdiction string
	Context      map[string]any
	CreatedAt    time.Time
	Now          time.Time
	Queues       []QueueDefinition
	Reviewers    []ReviewerProfile
	OpenByActor  map[string]int
	OpenByQueue  map[string]int
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
	slices.SortFunc(queues, func(a, b QueueDefinition) int {
		if a.Order != b.Order {
			return a.Order - b.Order
		}
		return strings.Compare(a.Key, b.Key)
	})
	capacityBlocked := false
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
		if !contextMatches(queue.ContextEquals, input.Context) {
			continue
		}
		if queue.MinAgeHours > 0 || queue.MaxAgeHours > 0 {
			if input.CreatedAt.IsZero() || input.Now.IsZero() || input.Now.Before(input.CreatedAt) {
				return RoutingDecision{}, errors.New("case-manager: age-based routing requires a valid created_at and now")
			}
			ageHours := int(input.Now.Sub(input.CreatedAt) / time.Hour)
			if ageHours < queue.MinAgeHours || (queue.MaxAgeHours > 0 && ageHours > queue.MaxAgeHours) {
				continue
			}
		}
		if input.OpenByQueue[queue.Key] >= queue.Capacity {
			capacityBlocked = true
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
	if capacityBlocked {
		return RoutingDecision{}, errors.New("case-manager: all matching routing queues are at capacity")
	}
	return RoutingDecision{}, errors.New("case-manager: no routing queue matched the case")
}

func contextMatches(required map[string]string, context map[string]any) bool {
	for key, want := range required {
		got, ok := context[key]
		if !ok || fmt.Sprint(got) != want {
			return false
		}
	}
	return true
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
