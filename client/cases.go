// SPDX-License-Identifier: AGPL-3.0-or-later

package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// CaseField defines one typed, optionally PII-protected context field.
type CaseField struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Kind     string   `json:"kind"`
	Required bool     `json:"required,omitempty"`
	PII      bool     `json:"pii,omitempty"`
	ReadBy   []string `json:"read_by,omitempty"`
}

// CaseTransition is one role-gated state-machine edge.
type CaseTransition struct {
	From  string   `json:"from"`
	To    string   `json:"to"`
	Roles []string `json:"roles,omitempty"`
}

// CaseDisposition defines a reasoned terminal outcome.
type CaseDisposition struct {
	Key                  string   `json:"key"`
	Label                string   `json:"label"`
	ReasonCodes          []string `json:"reason_codes"`
	TerminalState        string   `json:"terminal_state"`
	RequiresSecondReview bool     `json:"requires_second_review,omitempty"`
}

// CaseEvidenceRequirement defines evidence required for disposition.
type CaseEvidenceRequirement struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Kinds    []string `json:"kinds"`
	Required bool     `json:"required,omitempty"`
}

// CaseServiceCalendar is the published business-hours SLA contract.
type CaseServiceCalendar struct {
	Timezone   string   `json:"timezone"`
	Weekdays   []int    `json:"weekdays"`
	StartHour  int      `json:"start_hour"`
	EndHour    int      `json:"end_hour"`
	Holidays   []string `json:"holidays,omitempty"`
	SLAHours   int      `json:"sla_hours"`
	Escalation int      `json:"escalation_hours,omitempty"`
}

// CaseRoleLayout controls form field order and editability by role.
type CaseRoleLayout struct {
	Role     string   `json:"role"`
	Sections []string `json:"sections"`
	Editable []string `json:"editable,omitempty"`
}

// CaseTypeDefinition is one immutable versioned case schema.
type CaseTypeDefinition struct {
	Key          string                    `json:"key"`
	Name         string                    `json:"name"`
	Description  string                    `json:"description,omitempty"`
	InitialState string                    `json:"initial_state"`
	Fields       []CaseField               `json:"fields"`
	Transitions  []CaseTransition          `json:"transitions"`
	Dispositions []CaseDisposition         `json:"dispositions"`
	Priorities   []string                  `json:"priorities"`
	Calendar     CaseServiceCalendar       `json:"service_calendar"`
	Evidence     []CaseEvidenceRequirement `json:"evidence_requirements"`
	Layouts      []CaseRoleLayout          `json:"layouts"`
}

// CaseType is one published immutable definition version.
type CaseType struct {
	Key         string             `json:"key"`
	Version     int                `json:"version"`
	Definition  CaseTypeDefinition `json:"definition"`
	PublishedBy string             `json:"published_by"`
	PublishedAt time.Time          `json:"published_at"`
}

// CaseQueue defines deterministic routing eligibility.
type CaseQueue struct {
	Key                 string            `json:"key"`
	Name                string            `json:"name"`
	Order               int               `json:"order,omitempty"`
	CaseTypes           []string          `json:"case_types,omitempty"`
	Priorities          []string          `json:"priorities,omitempty"`
	Jurisdictions       []string          `json:"jurisdictions,omitempty"`
	RequiredSkills      []string          `json:"required_skills,omitempty"`
	Capacity            int               `json:"capacity"`
	EscalationQueue     string            `json:"escalation_queue,omitempty"`
	MinAgeHours         int               `json:"min_age_hours,omitempty"`
	MaxAgeHours         int               `json:"max_age_hours,omitempty"`
	ConflictContextKeys []string          `json:"conflict_context_keys,omitempty"`
	ContextEquals       map[string]string `json:"context_equals,omitempty"`
}

// CaseReviewer is one reviewer routing profile.
type CaseReviewer struct {
	Actor         string   `json:"actor"`
	Skills        []string `json:"skills"`
	Jurisdictions []string `json:"jurisdictions"`
	Capacity      int      `json:"capacity"`
	Active        bool     `json:"active"`
	Conflicts     []string `json:"conflicts,omitempty"`
}

// CaseQueueView is the active queue plus its configuration audit metadata.
type CaseQueueView struct {
	Definition   CaseQueue `json:"definition"`
	ConfiguredBy string    `json:"configured_by"`
	ConfiguredAt time.Time `json:"configured_at"`
}

// CaseReviewerView is the active reviewer profile plus configuration metadata.
type CaseReviewerView struct {
	Profile      CaseReviewer `json:"profile"`
	ConfiguredBy string       `json:"configured_by"`
	ConfiguredAt time.Time    `json:"configured_at"`
}

// ReviewCase is the complete operational case view. Extensible nested evidence,
// QA, audit, and governance shapes remain raw JSON to preserve forward
// compatibility while the top-level routing and lifecycle fields stay typed.
type ReviewCase struct {
	CaseID             string            `json:"case_id"`
	CompanyName        string            `json:"company_name"`
	CaseType           string            `json:"case_type"`
	CaseTypeVersion    int               `json:"case_type_version"`
	Status             string            `json:"status"`
	Priority           string            `json:"priority"`
	Queue              string            `json:"queue,omitempty"`
	RoutingExplanation string            `json:"routing_explanation,omitempty"`
	Assignee           string            `json:"assignee,omitempty"`
	Jurisdiction       string            `json:"jurisdiction,omitempty"`
	Subject            string            `json:"subject,omitempty"`
	Deadline           time.Time         `json:"deadline,omitempty"`
	DaysLeft           int               `json:"days_left"`
	SLAState           string            `json:"sla_state,omitempty"`
	Context            json.RawMessage   `json:"context,omitempty"`
	SourceDecisionID   string            `json:"source_decision_id,omitempty"`
	Disposition        string            `json:"disposition,omitempty"`
	ReasonCode         string            `json:"reason_code,omitempty"`
	Evidence           []json.RawMessage `json:"evidence"`
	Attachments        []json.RawMessage `json:"attachments"`
	QA                 json.RawMessage   `json:"qa,omitempty"`
	Audit              []json.RawMessage `json:"audit"`
	CreatedAt          time.Time         `json:"created_at"`
	UpdatedAt          time.Time         `json:"updated_at"`
}

// CaseFilter selects the operational queue.
type CaseFilter struct {
	Status       string `json:"status,omitempty"`
	Type         string `json:"type,omitempty"`
	Assignee     string `json:"assignee,omitempty"`
	Queue        string `json:"queue,omitempty"`
	Priority     string `json:"priority,omitempty"`
	Jurisdiction string `json:"jurisdiction,omitempty"`
	Subject      string `json:"subject,omitempty"`
	Query        string `json:"q,omitempty"`
	SLAState     string `json:"sla_state,omitempty"`
}

func (f CaseFilter) encode() string {
	query := url.Values{}
	for key, value := range map[string]string{
		"status": f.Status, "type": f.Type, "assignee": f.Assignee, "queue": f.Queue,
		"priority": f.Priority, "jurisdiction": f.Jurisdiction, "subject": f.Subject,
		"q": f.Query, "sla_state": f.SLAState,
	} {
		if value != "" {
			query.Set(key, value)
		}
	}
	if encoded := query.Encode(); encoded != "" {
		return "?" + encoded
	}
	return ""
}

// CaseCreate opens a governed case under the latest published type.
type CaseCreate struct {
	CompanyName      string          `json:"company_name"`
	CaseType         string          `json:"case_type"`
	Priority         string          `json:"priority,omitempty"`
	Jurisdiction     string          `json:"jurisdiction,omitempty"`
	Subject          string          `json:"subject,omitempty"`
	SLADays          int             `json:"sla_days,omitempty"`
	Context          json.RawMessage `json:"context,omitempty"`
	SourceDecisionID string          `json:"source_decision_id,omitempty"`
}

// CaseBulkRequest is a bounded backend-owned mutation.
type CaseBulkRequest struct {
	Operation  string   `json:"operation"`
	CaseIDs    []string `json:"case_ids"`
	Target     string   `json:"target"`
	ReasonCode string   `json:"reason_code,omitempty"`
	Note       string   `json:"note,omitempty"`
	Reassign   bool     `json:"reassign,omitempty"`
	Override   bool     `json:"override,omitempty"`
}

// CaseBulkResult is the durable authoritative per-item manifest.
type CaseBulkResult struct {
	BatchID   string            `json:"batch_id"`
	Operation string            `json:"operation"`
	Status    string            `json:"status"`
	Succeeded int               `json:"succeeded"`
	Failed    int               `json:"failed"`
	Items     []json.RawMessage `json:"items"`
}

// CaseSavedView is an actor-owned reusable queue query.
type CaseSavedView struct {
	ViewID    string     `json:"view_id"`
	Name      string     `json:"name"`
	Owner     string     `json:"owner"`
	Query     CaseFilter `json:"query"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// CaseDuplicateGroup is a candidate set for human review, never an auto-merge.
type CaseDuplicateGroup struct {
	Key     string   `json:"key"`
	Reason  string   `json:"reason"`
	CaseIDs []string `json:"case_ids"`
}

// ListCases searches the tenant's operational queue.
func (c *Client) ListCases(ctx context.Context, filter CaseFilter) ([]ReviewCase, error) {
	out, err := do[struct {
		Cases []ReviewCase `json:"cases"`
	}](ctx, c, http.MethodGet, "/v1/cases"+filter.encode(), nil)
	return out.Cases, err
}

// GetCase reads one case.
func (c *Client) GetCase(ctx context.Context, caseID string) (ReviewCase, error) {
	return do[ReviewCase](ctx, c, http.MethodGet, "/v1/cases/"+url.PathEscape(caseID), nil)
}

// CreateCase opens a governed review case.
func (c *Client) CreateCase(ctx context.Context, request CaseCreate) (string, error) {
	out, err := do[struct {
		CaseID string `json:"case_id"`
	}](ctx, c, http.MethodPost, "/v1/cases", request)
	return out.CaseID, err
}

// ListCaseTypes returns the active published case-type versions.
func (c *Client) ListCaseTypes(ctx context.Context) ([]CaseType, error) {
	out, err := do[struct {
		CaseTypes []CaseType `json:"case_types"`
	}](ctx, c, http.MethodGet, "/v1/case-types", nil)
	return out.CaseTypes, err
}

// PublishCaseType publishes the next immutable definition version.
func (c *Client) PublishCaseType(ctx context.Context, definition CaseTypeDefinition) (int, error) {
	out, err := do[struct {
		Version int `json:"version"`
	}](ctx, c, http.MethodPost, "/v1/case-types", definition)
	return out.Version, err
}

// GetCaseType reads an exact immutable version.
func (c *Client) GetCaseType(ctx context.Context, key string, version int) (CaseType, error) {
	return do[CaseType](
		ctx, c, http.MethodGet,
		"/v1/case-types/"+url.PathEscape(key)+"/versions/"+strconv.Itoa(version), nil,
	)
}

// ConfigureCaseQueue replaces one active routing queue definition.
func (c *Client) ConfigureCaseQueue(ctx context.Context, queue CaseQueue) error {
	_, err := do[map[string]any](
		ctx, c, http.MethodPut, "/v1/case-queues/"+url.PathEscape(queue.Key), queue,
	)
	return err
}

// ListCaseQueues returns active deterministic routing definitions.
func (c *Client) ListCaseQueues(ctx context.Context) ([]CaseQueueView, error) {
	out, err := do[struct {
		Queues []CaseQueueView `json:"queues"`
	}](ctx, c, http.MethodGet, "/v1/case-queues", nil)
	return out.Queues, err
}

// ConfigureCaseReviewer replaces one active reviewer profile.
func (c *Client) ConfigureCaseReviewer(ctx context.Context, reviewer CaseReviewer) error {
	_, err := do[map[string]any](
		ctx, c, http.MethodPut, "/v1/case-reviewers/"+url.PathEscape(reviewer.Actor), reviewer,
	)
	return err
}

// ListCaseReviewers returns active reviewer profiles (admin).
func (c *Client) ListCaseReviewers(ctx context.Context) ([]CaseReviewerView, error) {
	out, err := do[struct {
		Reviewers []CaseReviewerView `json:"reviewers"`
	}](ctx, c, http.MethodGet, "/v1/case-reviewers", nil)
	return out.Reviewers, err
}

// BulkCases executes or resumes one idempotent bounded batch.
func (c *Client) BulkCases(
	ctx context.Context,
	request CaseBulkRequest,
	idempotencyKey string,
) (CaseBulkResult, error) {
	return doWithHeaders[CaseBulkResult](
		ctx, c, http.MethodPost, "/v1/cases/bulk", request,
		map[string]string{"Idempotency-Key": idempotencyKey},
	)
}

// ListCaseSavedViews lists the caller's reusable queue queries.
func (c *Client) ListCaseSavedViews(ctx context.Context) ([]CaseSavedView, error) {
	out, err := do[struct {
		Views []CaseSavedView `json:"views"`
	}](ctx, c, http.MethodGet, "/v1/case-views", nil)
	return out.Views, err
}

// SaveCaseView creates or replaces an actor-owned reusable queue query.
func (c *Client) SaveCaseView(ctx context.Context, viewID, name string, filter CaseFilter) (string, error) {
	out, err := do[struct {
		ViewID string `json:"view_id"`
	}](ctx, c, http.MethodPost, "/v1/case-views", map[string]any{
		"view_id": viewID, "name": name, "query": filter,
	})
	return out.ViewID, err
}

// DeleteCaseView removes an actor-owned reusable queue query.
func (c *Client) DeleteCaseView(ctx context.Context, viewID string) error {
	_, err := do[map[string]any](
		ctx, c, http.MethodDelete, "/v1/case-views/"+url.PathEscape(viewID), nil,
	)
	return err
}

// AssignCase atomically claims or explicitly reassigns a case.
func (c *Client) AssignCase(ctx context.Context, caseID, assignee string, reassign bool) error {
	return c.caseAction(ctx, caseID, "assign", map[string]any{"assignee": assignee, "reassign": reassign})
}

// SetCaseStatus traverses a permitted state-machine edge.
func (c *Client) SetCaseStatus(ctx context.Context, caseID, status string) error {
	return c.caseAction(ctx, caseID, "status", map[string]string{"status": status})
}

// SetCasePriority changes operational urgency under the pinned type.
func (c *Client) SetCasePriority(ctx context.Context, caseID, priority string) error {
	return c.caseAction(ctx, caseID, "priority", map[string]string{"priority": priority})
}

// UpdateCaseFields applies a role-governed partial field update.
func (c *Client) UpdateCaseFields(ctx context.Context, caseID string, fields map[string]any) error {
	_, err := do[map[string]any](
		ctx, c, http.MethodPatch, "/v1/cases/"+url.PathEscape(caseID)+"/fields",
		map[string]any{"fields": fields},
	)
	return err
}

// DispositionCase records a reasoned outcome after evidence gates pass.
func (c *Client) DispositionCase(
	ctx context.Context,
	caseID, disposition, reasonCode, note string,
	override bool,
) error {
	return c.caseAction(ctx, caseID, "disposition", map[string]any{
		"disposition": disposition, "reason_code": reasonCode, "note": note, "override": override,
	})
}

// LinkCaseEvidence records an immutable typed relationship to external evidence.
func (c *Client) LinkCaseEvidence(ctx context.Context, caseID string, evidence any) error {
	return c.caseAction(ctx, caseID, "evidence", evidence)
}

// RegisterCaseAttachment records immutable metadata for an approved artifact store.
func (c *Client) RegisterCaseAttachment(ctx context.Context, caseID string, attachment any) error {
	return c.caseAction(ctx, caseID, "attachments", attachment)
}

// AccessCaseAttachment audits an artifact access boundary.
func (c *Client) AccessCaseAttachment(
	ctx context.Context,
	caseID, attachmentID, purpose string,
) (string, error) {
	out, err := do[struct {
		StorageRef string `json:"storage_ref"`
	}](
		ctx, c, http.MethodPost,
		"/v1/cases/"+url.PathEscape(caseID)+"/attachments/"+url.PathEscape(attachmentID)+"/access",
		map[string]string{"purpose": purpose},
	)
	return out.StorageRef, err
}

// RouteCase applies configured queue and reviewer routing atomically.
func (c *Client) RouteCase(ctx context.Context, caseID string) (json.RawMessage, error) {
	out, err := do[struct {
		Decision json.RawMessage `json:"decision"`
	}](ctx, c, http.MethodPost, "/v1/cases/"+url.PathEscape(caseID)+"/route", nil)
	return out.Decision, err
}

// RebalanceCases moves only unrouted, inactive-owner, and capacity-overflow work.
func (c *Client) RebalanceCases(ctx context.Context) (json.RawMessage, error) {
	return do[json.RawMessage](ctx, c, http.MethodPost, "/v1/cases/rebalance", nil)
}

// SelectCaseQA deterministically samples and assigns an independent reviewer.
func (c *Client) SelectCaseQA(
	ctx context.Context,
	caseID, sampleID, reviewer string,
	rateBPS int,
) (bool, error) {
	out, err := do[struct {
		Selected bool `json:"selected"`
	}](ctx, c, http.MethodPost, "/v1/cases/"+url.PathEscape(caseID)+"/qa/select", map[string]any{
		"sample_id": sampleID, "reviewer": reviewer, "rate_bps": rateBPS,
	})
	return out.Selected, err
}

// ReviewCaseQA records an independent agreement, disagreement, or override.
func (c *Client) ReviewCaseQA(
	ctx context.Context,
	caseID, sampleID, disposition, reasonCode, note string,
	override bool,
) error {
	return c.caseAction(ctx, caseID, "qa/review", map[string]any{
		"sample_id": sampleID, "disposition": disposition, "reason_code": reasonCode,
		"note": note, "override": override,
	})
}

// RetryCaseWebhook starts a new escalation-delivery round after dead-lettering.
func (c *Client) RetryCaseWebhook(ctx context.Context, caseID, reason string) error {
	return c.caseAction(ctx, caseID, "webhook/retry", map[string]string{"reason": reason})
}

// CaseDuplicates lists deterministic candidates and never auto-merges them.
func (c *Client) CaseDuplicates(ctx context.Context) ([]CaseDuplicateGroup, error) {
	out, err := do[struct {
		Groups []CaseDuplicateGroup `json:"duplicate_groups"`
	}](ctx, c, http.MethodGet, "/v1/cases/duplicates", nil)
	return out.Groups, err
}

func (c *Client) caseAction(ctx context.Context, caseID, action string, body any) error {
	_, err := do[map[string]any](
		ctx, c, http.MethodPost, "/v1/cases/"+url.PathEscape(caseID)+"/"+action, body,
	)
	return err
}

// CaseAnalytics returns the replay-derived operational KPI document.
func (c *Client) CaseAnalytics(ctx context.Context) (json.RawMessage, error) {
	return do[json.RawMessage](ctx, c, http.MethodGet, "/v1/cases/analytics", nil)
}

// ValidatedCaseOutcomes returns only agreement- or override-backed evaluation labels.
func (c *Client) ValidatedCaseOutcomes(ctx context.Context) ([]json.RawMessage, error) {
	out, err := do[struct {
		Outcomes []json.RawMessage `json:"validated_outcomes"`
	}](ctx, c, http.MethodGet, "/v1/case-validated-outcomes", nil)
	return out.Outcomes, err
}

// ExportCaseAudit downloads a filtered CSV, JSON, or Markdown audit ledger.
func (c *Client) ExportCaseAudit(ctx context.Context, format string, filter CaseFilter) ([]byte, error) {
	query := filter.encode()
	separator := "?"
	if query != "" {
		separator = "&"
	}
	return doBytes(ctx, c, "/v1/cases/export"+query+separator+"format="+url.QueryEscape(format))
}
