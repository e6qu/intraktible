// SPDX-License-Identifier: AGPL-3.0-or-later

// Package authoring owns collaborative decision-development state: durable
// revisioned drafts, governed changesets, reusable component versions, semantic
// differences, dependency impact, and disposable presence leases.
package authoring

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/platform/privacy"
)

const (
	maxTitle       = 240
	maxRationale   = 8000
	maxDescription = 2000
	maxCheckName   = 120
	maxEvidence    = 8000
	maxDraftNodes  = 5000
	maxDraftEdges  = 10000
	maxJSONBytes   = 4 << 20
)

var slugPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// DraftState is the lifecycle of a collaborative flow draft.
type DraftState string

const (
	DraftStateActive   DraftState = "active"
	DraftStateArchived DraftState = "archived"
)

// ChangeSetState is the governed review lifecycle for one pinned draft revision.
type ChangeSetState string

const (
	ChangeSetDraft            ChangeSetState = "draft"
	ChangeSetInReview         ChangeSetState = "in_review"
	ChangeSetChangesRequested ChangeSetState = "changes_requested"
	ChangeSetApproved         ChangeSetState = "approved"
	ChangeSetPublishing       ChangeSetState = "publishing"
	ChangeSetPublished        ChangeSetState = "published"
)

// ReviewDecision is a checker decision on an in-review changeset.
type ReviewDecision string

const (
	ReviewApprove        ReviewDecision = "approve"
	ReviewRequestChanges ReviewDecision = "request_changes"
)

// CheckStatus is a required changeset check's result.
type CheckStatus string

const (
	CheckPassed CheckStatus = "passed"
	CheckFailed CheckStatus = "failed"
)

// DraftInput creates a durable authoring draft.
type DraftInput struct {
	FlowID      string          `json:"flow_id"`
	BaseVersion int             `json:"base_version"`
	Title       string          `json:"title"`
	Graph       events.Graph    `json:"graph"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

// SaveDraftInput replaces the full snapshot at one expected revision.
type SaveDraftInput struct {
	ExpectedRevision int             `json:"expected_revision"`
	Title            string          `json:"title"`
	Graph            events.Graph    `json:"graph"`
	InputSchema      json.RawMessage `json:"input_schema,omitempty"`
}

// RebaseDraftInput records an explicit new base plus the editor's resolved
// snapshot. It uses the same optimistic revision precondition as autosave.
type RebaseDraftInput struct {
	ExpectedRevision int             `json:"expected_revision"`
	BaseVersion      int             `json:"base_version"`
	Title            string          `json:"title"`
	Graph            events.Graph    `json:"graph"`
	InputSchema      json.RawMessage `json:"input_schema,omitempty"`
}

// ComponentInput registers a reusable component identity.
type ComponentInput struct {
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// ComponentVersionInput publishes an immutable reusable component version.
type ComponentVersionInput struct {
	Graph                events.Graph    `json:"graph"`
	InputSchema          json.RawMessage `json:"input_schema,omitempty"`
	OutputSchema         json.RawMessage `json:"output_schema,omitempty"`
	AllowBreaking        bool            `json:"allow_breaking,omitempty"`
	BreakingChangeReason string          `json:"breaking_change_reason,omitempty"`
}

// ComponentUpgradeInput creates governed drafts for selected current flow
// consumers. Component consumers must first publish their own explicit version;
// a transitive pin is never silently rewritten inside another asset.
type ComponentUpgradeInput struct {
	FromVersion int      `json:"from_version"`
	ToVersion   int      `json:"to_version"`
	FlowIDs     []string `json:"flow_ids"`
	Title       string   `json:"title,omitempty"`
}

// ChangeSetInput pins one draft revision for review.
type ChangeSetInput struct {
	DraftID        string   `json:"draft_id"`
	DraftRevision  int      `json:"draft_revision"`
	Title          string   `json:"title"`
	Rationale      string   `json:"rationale,omitempty"`
	RequiredChecks []string `json:"required_checks,omitempty"`
	Reviewers      []string `json:"reviewers,omitempty"`
}

// CheckInput records reproducible evidence for one named required check.
type CheckInput struct {
	Name     string      `json:"name"`
	Status   CheckStatus `json:"status"`
	Evidence string      `json:"evidence,omitempty"`
}

// ReviewInput is the checker decision and explanation.
type ReviewInput struct {
	Decision ReviewDecision `json:"decision"`
	Reason   string         `json:"reason,omitempty"`
}

// SubflowConfig is the only accepted config for an authoring subflow node.
// Exact version pins are mandatory; mutable "latest" references are forbidden.
type SubflowConfig struct {
	ComponentID   string `json:"component_id,omitempty"`
	ComponentSlug string `json:"component_slug,omitempty"`
	Version       int    `json:"version"`
}

func (in DraftInput) validate() error {
	if strings.TrimSpace(in.FlowID) == "" {
		return errors.New("authoring: flow_id is required")
	}
	if in.BaseVersion < 0 {
		return errors.New("authoring: base_version must be non-negative")
	}
	return validateDraftContent(in.Title, in.Graph, in.InputSchema)
}

func (in SaveDraftInput) validate() error {
	if in.ExpectedRevision < 1 {
		return errors.New("authoring: expected_revision must be positive")
	}
	return validateDraftContent(in.Title, in.Graph, in.InputSchema)
}

func (in RebaseDraftInput) validate() error {
	if in.ExpectedRevision < 1 {
		return errors.New("authoring: expected_revision must be positive")
	}
	if in.BaseVersion < 0 {
		return errors.New("authoring: base_version must be non-negative")
	}
	return validateDraftContent(in.Title, in.Graph, in.InputSchema)
}

func validateDraftContent(
	title string,
	graph events.Graph,
	inputSchema json.RawMessage,
) error {
	title = strings.TrimSpace(title)
	if title == "" {
		return errors.New("authoring: draft title is required")
	}
	if len(title) > maxTitle {
		return fmt.Errorf("authoring: title too long (%d > %d)", len(title), maxTitle)
	}
	if err := validateDraftGraph(graph); err != nil {
		return err
	}
	return validateBoundedJSON("input_schema", inputSchema)
}

// Drafts deliberately accept incomplete topology so an autosave can preserve
// the real intermediate canvas while a node is unconnected or a branch is
// being rearranged. Changeset creation is the validation boundary that requires
// a complete, acyclic, executable graph.
func validateDraftGraph(graph events.Graph) error {
	if len(graph.Nodes) > maxDraftNodes {
		return fmt.Errorf(
			"authoring: draft has too many nodes (%d > %d)",
			len(graph.Nodes), maxDraftNodes,
		)
	}
	if len(graph.Edges) > maxDraftEdges {
		return fmt.Errorf(
			"authoring: draft has too many edges (%d > %d)",
			len(graph.Edges), maxDraftEdges,
		)
	}
	nodes := make(map[string]struct{}, len(graph.Nodes))
	configBytes := 0
	for _, node := range graph.Nodes {
		if strings.TrimSpace(node.ID) == "" {
			return errors.New("authoring: draft node id is required")
		}
		if _, exists := nodes[node.ID]; exists {
			return fmt.Errorf("authoring: duplicate draft node id %q", node.ID)
		}
		if !knownAuthoringNodeType(node.Type) {
			return fmt.Errorf("authoring: node %q has unknown type %q", node.ID, node.Type)
		}
		if len(node.Config) > 0 && !json.Valid(node.Config) {
			return fmt.Errorf("authoring: node %q config is not valid JSON", node.ID)
		}
		configBytes += len(node.Config)
		if configBytes > maxJSONBytes {
			return fmt.Errorf("authoring: draft node configuration exceeds %d bytes", maxJSONBytes)
		}
		nodes[node.ID] = struct{}{}
	}
	for _, edge := range graph.Edges {
		if _, ok := nodes[edge.From]; !ok {
			return fmt.Errorf("authoring: edge from unknown draft node %q", edge.From)
		}
		if _, ok := nodes[edge.To]; !ok {
			return fmt.Errorf("authoring: edge to unknown draft node %q", edge.To)
		}
	}
	return nil
}

func knownAuthoringNodeType(nodeType events.NodeType) bool {
	switch nodeType {
	case events.NodeInput, events.NodeRule, events.NodeSplit,
		events.NodeAssignment, events.NodeScorecard, events.NodeDecisionTable,
		events.NodeMatrix2D, events.NodeCode, events.NodeAI,
		events.NodeConnect, events.NodePredict, events.NodeManualReview,
		events.NodeReason, events.NodeOutput, events.NodeSubflow:
		return true
	default:
		return false
	}
}

func validateBoundedJSON(name string, raw json.RawMessage) error {
	if len(raw) > maxJSONBytes {
		return fmt.Errorf("authoring: %s exceeds %d bytes", name, maxJSONBytes)
	}
	if len(raw) > 0 && !json.Valid(raw) {
		return fmt.Errorf("authoring: %s is not valid JSON", name)
	}
	return nil
}

func (in ComponentInput) validate() error {
	if !slugPattern.MatchString(in.Slug) {
		return fmt.Errorf("authoring: invalid component slug %q", in.Slug)
	}
	if strings.TrimSpace(in.Name) == "" {
		return errors.New("authoring: component name is required")
	}
	if len(in.Description) > maxDescription {
		return fmt.Errorf("authoring: description too long (%d > %d)", len(in.Description), maxDescription)
	}
	return nil
}

func (in ComponentVersionInput) validate() error {
	if err := validateBoundedJSON("input_schema", in.InputSchema); err != nil {
		return err
	}
	if err := validateBoundedJSON("output_schema", in.OutputSchema); err != nil {
		return err
	}
	if err := validateAuthoringGraph(in.Graph); err != nil {
		return err
	}
	outputs := 0
	for _, node := range in.Graph.Nodes {
		if node.Type == events.NodeOutput {
			outputs++
		}
	}
	if outputs != 1 {
		return fmt.Errorf("authoring: reusable component needs exactly one output node, got %d", outputs)
	}
	if in.AllowBreaking && strings.TrimSpace(in.BreakingChangeReason) == "" {
		return errors.New("authoring: breaking_change_reason is required when allow_breaking is true")
	}
	if len(in.BreakingChangeReason) > maxRationale {
		return fmt.Errorf(
			"authoring: breaking change reason too long (%d > %d)",
			len(in.BreakingChangeReason), maxRationale,
		)
	}
	return nil
}

func (in ComponentUpgradeInput) validate() error {
	if in.FromVersion < 1 || in.ToVersion < 1 || in.FromVersion == in.ToVersion {
		return errors.New("authoring: from_version and to_version must be different positive integers")
	}
	if len(in.FlowIDs) == 0 || len(in.FlowIDs) > 100 {
		return errors.New("authoring: flow_ids must contain 1..100 selected consumers")
	}
	if len(in.Title) > maxTitle {
		return fmt.Errorf("authoring: title too long (%d > %d)", len(in.Title), maxTitle)
	}
	return nil
}

func (in ChangeSetInput) validate() error {
	if strings.TrimSpace(in.DraftID) == "" {
		return errors.New("authoring: draft_id is required")
	}
	if in.DraftRevision < 1 {
		return errors.New("authoring: draft_revision must be positive")
	}
	if strings.TrimSpace(in.Title) == "" {
		return errors.New("authoring: changeset title is required")
	}
	if len(in.Title) > maxTitle {
		return fmt.Errorf("authoring: title too long (%d > %d)", len(in.Title), maxTitle)
	}
	if len(in.Rationale) > maxRationale {
		return fmt.Errorf("authoring: rationale too long (%d > %d)", len(in.Rationale), maxRationale)
	}
	if privacy.ContainsTextPII(in.Rationale) {
		return errors.New(
			"authoring: rationale contains sensitive personal data; use an immutable evidence reference",
		)
	}
	seen := make(map[string]bool, len(in.RequiredChecks))
	for _, name := range in.RequiredChecks {
		name = strings.TrimSpace(name)
		if name == "" || len(name) > maxCheckName {
			return errors.New("authoring: required check names must be non-empty and bounded")
		}
		if seen[name] {
			return fmt.Errorf("authoring: duplicate required check %q", name)
		}
		seen[name] = true
	}
	return nil
}

func (in CheckInput) validate() error {
	if strings.TrimSpace(in.Name) == "" || len(in.Name) > maxCheckName {
		return errors.New("authoring: check name must be non-empty and bounded")
	}
	if in.Status != CheckPassed && in.Status != CheckFailed {
		return fmt.Errorf("authoring: invalid check status %q", in.Status)
	}
	if len(in.Evidence) > maxEvidence {
		return fmt.Errorf("authoring: check evidence too long (%d > %d)", len(in.Evidence), maxEvidence)
	}
	if privacy.ContainsTextPII(in.Evidence) {
		return errors.New(
			"authoring: check evidence contains sensitive personal data; use an immutable evidence reference",
		)
	}
	return nil
}

func (in ReviewInput) validate() error {
	if in.Decision != ReviewApprove && in.Decision != ReviewRequestChanges {
		return fmt.Errorf("authoring: invalid review decision %q", in.Decision)
	}
	if len(in.Reason) > maxRationale {
		return fmt.Errorf("authoring: review reason too long (%d > %d)", len(in.Reason), maxRationale)
	}
	if privacy.ContainsTextPII(in.Reason) {
		return errors.New(
			"authoring: review reason contains sensitive personal data; use an immutable evidence reference",
		)
	}
	if in.Decision == ReviewRequestChanges && strings.TrimSpace(in.Reason) == "" {
		return errors.New("authoring: request_changes requires a reason")
	}
	return nil
}

// RevisionConflict is returned with the authoritative current snapshot when a
// stale editor attempts to save. HTTP maps it to 409 so clients can present a
// three-way merge instead of discarding either side.
type RevisionConflict struct {
	Current DraftView
}

func (e *RevisionConflict) Error() string {
	return fmt.Sprintf(
		"authoring: draft revision conflict: current is %d",
		e.Current.Revision,
	)
}

// CompatibilityError carries the exact reasons a component version cannot be
// treated as an automatic upgrade. HTTP maps it to 409 for migration tooling.
type CompatibilityError struct {
	Report CompatibilityReport
}

// IdempotencyConflict means a retry identity was reused for different content.
type IdempotencyConflict struct {
	Operation string
}

func (e *IdempotencyConflict) Error() string {
	return fmt.Sprintf(
		"authoring: idempotency key was already used with a different %s request",
		e.Operation,
	)
}

func (e *CompatibilityError) Error() string {
	return fmt.Sprintf(
		"authoring: component v%d is incompatible with v%d (%d issue(s)); "+
			"set allow_breaking with a reason to publish it as a manual migration",
		e.Report.ToVersion, e.Report.FromVersion, len(e.Report.Issues),
	)
}

// Presence is a disposable editor lease. It is intentionally not an event:
// replay rebuilds governed content and drops transient collaboration sessions.
type Presence struct {
	Org         string    `json:"org"`
	Workspace   string    `json:"workspace"`
	DraftID     string    `json:"draft_id"`
	Actor       string    `json:"actor"`
	DisplayName string    `json:"display_name,omitempty"`
	Revision    int       `json:"revision"`
	SelectedID  string    `json:"selected_id,omitempty"`
	ExpiresAt   time.Time `json:"expires_at"`
}
