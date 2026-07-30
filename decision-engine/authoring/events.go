// SPDX-License-Identifier: AGPL-3.0-or-later

package authoring

import (
	"encoding/json"

	"github.com/e6qu/intraktible/decision-engine/events"
)

const Stream = "decision.authoring"

const (
	TypeDraftCreated              = "decision.authoring.draft_created"
	TypeDraftSaved                = "decision.authoring.draft_saved"
	TypeDraftRebased              = "decision.authoring.draft_rebased"
	TypeDraftArchived             = "decision.authoring.draft_archived"
	TypeDraftExported             = "decision.authoring.draft_exported"
	TypeComponentCreated          = "decision.authoring.component_created"
	TypeComponentVersionPublished = "decision.authoring.component_version_published"
	TypeComponentRetired          = "decision.authoring.component_retired"
	TypeChangeSetCreated          = "decision.authoring.changeset_created"
	TypeChangeSetSubmitted        = "decision.authoring.changeset_submitted"
	TypeChangeSetCheckRecorded    = "decision.authoring.changeset_check_recorded"
	TypeChangeSetReviewed         = "decision.authoring.changeset_reviewed"
	TypeChangeSetReviewReminded   = "decision.authoring.changeset_review_reminded"
	TypeChangeSetPublishRequested = "decision.authoring.changeset_publish_requested"
)

type DraftCreated struct {
	DraftID             string          `json:"draft_id"`
	FlowID              string          `json:"flow_id"`
	BaseVersion         int             `json:"base_version"`
	Revision            int             `json:"revision"`
	Title               string          `json:"title"`
	Graph               events.Graph    `json:"graph"`
	InputSchema         json.RawMessage `json:"input_schema,omitempty"`
	ImportKeyHash       string          `json:"import_key_hash,omitempty"`
	ImportRequestHash   string          `json:"import_request_hash,omitempty"`
	ImportCreatedFlow   bool            `json:"import_created_flow,omitempty"`
	MutationKeyHash     string          `json:"mutation_key_hash,omitempty"`
	MutationRequestHash string          `json:"mutation_request_hash,omitempty"`
}

type DraftSaved struct {
	DraftID     string          `json:"draft_id"`
	Revision    int             `json:"revision"`
	Title       string          `json:"title"`
	Graph       events.Graph    `json:"graph"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

type DraftRebased struct {
	DraftID     string          `json:"draft_id"`
	Revision    int             `json:"revision"`
	BaseVersion int             `json:"base_version"`
	Title       string          `json:"title"`
	Graph       events.Graph    `json:"graph"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

type DraftArchived struct {
	DraftID string `json:"draft_id"`
	Reason  string `json:"reason,omitempty"`
}

// DraftExported records access to canonical source that may contain sensitive
// embedded examples/defaults. The exported bytes stay out of the audit log.
type DraftExported struct {
	DraftID  string `json:"draft_id"`
	FlowID   string `json:"flow_id"`
	Revision int    `json:"revision"`
	Format   string `json:"format"`
}

type ComponentCreated struct {
	ComponentID         string `json:"component_id"`
	Slug                string `json:"slug"`
	Name                string `json:"name"`
	Description         string `json:"description,omitempty"`
	MutationKeyHash     string `json:"mutation_key_hash,omitempty"`
	MutationRequestHash string `json:"mutation_request_hash,omitempty"`
}

type ComponentVersionPublished struct {
	ComponentID         string                  `json:"component_id"`
	Version             int                     `json:"version"`
	Etag                string                  `json:"etag"`
	SourceGraph         events.Graph            `json:"source_graph"`
	Graph               events.Graph            `json:"graph"`
	InputSchema         json.RawMessage         `json:"input_schema,omitempty"`
	OutputSchema        json.RawMessage         `json:"output_schema,omitempty"`
	Dependencies        []events.FlowDependency `json:"dependencies,omitempty"`
	Compatibility       CompatibilityReport     `json:"compatibility"`
	BreakingReason      string                  `json:"breaking_change_reason,omitempty"`
	MutationKeyHash     string                  `json:"mutation_key_hash,omitempty"`
	MutationRequestHash string                  `json:"mutation_request_hash,omitempty"`
}

type ComponentRetired struct {
	ComponentID string `json:"component_id"`
}

type ChangeSetCreated struct {
	ChangeSetID         string                  `json:"changeset_id"`
	FlowID              string                  `json:"flow_id"`
	BaseVersion         int                     `json:"base_version"`
	DraftID             string                  `json:"draft_id"`
	DraftRevision       int                     `json:"draft_revision"`
	Title               string                  `json:"title"`
	Rationale           string                  `json:"rationale,omitempty"`
	SourceGraph         events.Graph            `json:"source_graph"`
	Graph               events.Graph            `json:"graph"`
	InputSchema         json.RawMessage         `json:"input_schema,omitempty"`
	Dependencies        []events.FlowDependency `json:"dependencies,omitempty"`
	ProposedEtag        string                  `json:"proposed_etag"`
	RequiredChecks      []string                `json:"required_checks,omitempty"`
	Reviewers           []string                `json:"reviewers,omitempty"`
	MutationKeyHash     string                  `json:"mutation_key_hash,omitempty"`
	MutationRequestHash string                  `json:"mutation_request_hash,omitempty"`
}

type ChangeSetSubmitted struct {
	ChangeSetID string   `json:"changeset_id"`
	FlowID      string   `json:"flow_id"`
	Title       string   `json:"title"`
	CreatedBy   string   `json:"created_by"`
	Reviewers   []string `json:"reviewers,omitempty"`
}

type ChangeSetCheckRecorded struct {
	ChangeSetID string      `json:"changeset_id"`
	Name        string      `json:"name"`
	Status      CheckStatus `json:"status"`
	Evidence    string      `json:"evidence,omitempty"`
}

type ChangeSetReviewed struct {
	ChangeSetID string         `json:"changeset_id"`
	FlowID      string         `json:"flow_id"`
	Title       string         `json:"title"`
	CreatedBy   string         `json:"created_by"`
	Decision    ReviewDecision `json:"decision"`
	Reason      string         `json:"reason,omitempty"`
}

type ChangeSetReviewReminded struct {
	ChangeSetID string   `json:"changeset_id"`
	FlowID      string   `json:"flow_id"`
	Title       string   `json:"title"`
	CreatedBy   string   `json:"created_by"`
	Reviewers   []string `json:"reviewers,omitempty"`
}

type ChangeSetPublishRequested struct {
	ChangeSetID string `json:"changeset_id"`
}
