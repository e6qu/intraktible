// SPDX-License-Identifier: AGPL-3.0-or-later

package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// AuthoringDraft is the current accepted full-snapshot editor revision.
type AuthoringDraft struct {
	DraftID     string          `json:"draft_id"`
	FlowID      string          `json:"flow_id"`
	BaseVersion int             `json:"base_version"`
	Revision    int             `json:"revision"`
	State       string          `json:"state"`
	Title       string          `json:"title"`
	Graph       json.RawMessage `json:"graph"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
	CreatedBy   string          `json:"created_by"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedBy   string          `json:"updated_by"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

// DraftWrite is a create payload. FlowID is required for create.
type DraftWrite struct {
	FlowID      string          `json:"flow_id,omitempty"`
	BaseVersion int             `json:"base_version"`
	Title       string          `json:"title"`
	Graph       json.RawMessage `json:"graph"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

// DraftSave is an optimistic autosave payload.
type DraftSave struct {
	ExpectedRevision int             `json:"expected_revision"`
	Title            string          `json:"title"`
	Graph            json.RawMessage `json:"graph"`
	InputSchema      json.RawMessage `json:"input_schema,omitempty"`
}

// DraftRebase records an explicit new published base with the resolved source.
type DraftRebase struct {
	ExpectedRevision int             `json:"expected_revision"`
	BaseVersion      int             `json:"base_version"`
	Title            string          `json:"title"`
	Graph            json.RawMessage `json:"graph"`
	InputSchema      json.RawMessage `json:"input_schema,omitempty"`
}

// AuthoringDraftRevision is one immutable autosave checkpoint.
type AuthoringDraftRevision struct {
	DraftID     string          `json:"draft_id"`
	FlowID      string          `json:"flow_id"`
	BaseVersion int             `json:"base_version"`
	Revision    int             `json:"revision"`
	Title       string          `json:"title"`
	Graph       json.RawMessage `json:"graph"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
	Actor       string          `json:"actor"`
	At          time.Time       `json:"at"`
	Rebased     bool            `json:"rebased,omitempty"`
}

// DraftPresence is one disposable collaborator lease.
type DraftPresence struct {
	DraftID     string    `json:"draft_id"`
	Actor       string    `json:"actor"`
	DisplayName string    `json:"display_name,omitempty"`
	Revision    int       `json:"revision"`
	SelectedID  string    `json:"selected_id,omitempty"`
	RenewedAt   time.Time `json:"renewed_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// DraftConflict extracts the authoritative remote snapshot from a 409.
func DraftConflict(err error) (AuthoringDraft, bool) {
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict {
		return AuthoringDraft{}, false
	}
	var body struct {
		Current AuthoringDraft `json:"current"`
	}
	if json.Unmarshal(apiErr.Body, &body) != nil || body.Current.DraftID == "" {
		return AuthoringDraft{}, false
	}
	return body.Current, true
}

// ChangeSet is one immutable reviewed proposal.
type ChangeSet struct {
	ChangeSetID      string                     `json:"changeset_id"`
	FlowID           string                     `json:"flow_id"`
	BaseVersion      int                        `json:"base_version"`
	DraftID          string                     `json:"draft_id"`
	DraftRevision    int                        `json:"draft_revision"`
	Title            string                     `json:"title"`
	Rationale        string                     `json:"rationale,omitempty"`
	State            string                     `json:"state"`
	SourceGraph      json.RawMessage            `json:"source_graph"`
	Graph            json.RawMessage            `json:"graph"`
	InputSchema      json.RawMessage            `json:"input_schema,omitempty"`
	Dependencies     []FlowDependency           `json:"dependencies,omitempty"`
	ProposedEtag     string                     `json:"proposed_etag"`
	RequiredChecks   []string                   `json:"required_checks,omitempty"`
	Reviewers        []string                   `json:"reviewers,omitempty"`
	Checks           map[string]json.RawMessage `json:"checks,omitempty"`
	CreatedBy        string                     `json:"created_by"`
	SubmittedBy      string                     `json:"submitted_by,omitempty"`
	PublishedVersion int                        `json:"published_version,omitempty"`
}

// FlowDependency is one exact immutable reusable-component pin.
type FlowDependency struct {
	ComponentID string `json:"component_id"`
	Version     int    `json:"version"`
	Etag        string `json:"etag"`
}

// ReusableComponent is the registry identity and its immutable versions.
type ReusableComponent struct {
	ComponentID string                     `json:"component_id"`
	Slug        string                     `json:"slug"`
	Name        string                     `json:"name"`
	Description string                     `json:"description,omitempty"`
	Latest      int                        `json:"latest"`
	Versions    []ReusableComponentVersion `json:"versions,omitempty"`
	Retired     bool                       `json:"retired"`
}

// ReusableComponentVersion is one immutable source plus its migration evidence.
type ReusableComponentVersion struct {
	Version              int                          `json:"version"`
	Etag                 string                       `json:"etag"`
	InputSchema          json.RawMessage              `json:"input_schema,omitempty"`
	OutputSchema         json.RawMessage              `json:"output_schema,omitempty"`
	Compatibility        ComponentCompatibilityReport `json:"compatibility"`
	BreakingChangeReason string                       `json:"breaking_change_reason,omitempty"`
}

// ComponentCompatibilityReport explains whether exact pins can upgrade safely.
type ComponentCompatibilityReport struct {
	FromVersion int                           `json:"from_version,omitempty"`
	ToVersion   int                           `json:"to_version"`
	Status      string                        `json:"status"`
	Issues      []ComponentCompatibilityIssue `json:"issues,omitempty"`
}

// ComponentCompatibilityIssue is one blocking contract difference.
type ComponentCompatibilityIssue struct {
	Path    string `json:"path"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// CanonicalFlow is the versioned repository source accepted by authoring import.
type CanonicalFlow struct {
	FormatVersion string          `json:"format_version"`
	Kind          string          `json:"kind"`
	Slug          string          `json:"slug"`
	Name          string          `json:"name"`
	Description   string          `json:"description,omitempty"`
	Graph         json.RawMessage `json:"graph"`
	InputSchema   json.RawMessage `json:"input_schema,omitempty"`
}

// AuthoringImportResult identifies the durable draft created by canonical import.
type AuthoringImportResult struct {
	FlowID          string                   `json:"flow_id"`
	DraftID         string                   `json:"draft_id"`
	Revision        int                      `json:"revision"`
	Created         bool                     `json:"created"`
	MigrationReport CanonicalMigrationReport `json:"migration_report"`
	EventID         string                   `json:"event_id,omitempty"`
	Seq             uint64                   `json:"seq,omitempty"`
}

// CanonicalMigrationReport lists target-workspace identifier rewrites.
type CanonicalMigrationReport struct {
	Rewrites []CanonicalRewrite `json:"rewrites"`
}

// CanonicalRewrite is one explicit portable-source migration.
type CanonicalRewrite struct {
	Path   string `json:"path"`
	From   string `json:"from"`
	To     string `json:"to"`
	Reason string `json:"reason"`
}

// ImportCanonicalFlow creates an idempotent durable draft. It never publishes
// or deploys the source outside the normal changeset review path.
func (c *Client) ImportCanonicalFlow(
	ctx context.Context,
	flow CanonicalFlow,
	idempotencyKey string,
) (AuthoringImportResult, error) {
	return doWithHeaders[AuthoringImportResult](
		ctx, c, http.MethodPost, "/v1/authoring/import", flow,
		map[string]string{"Idempotency-Key": idempotencyKey},
	)
}

// ImportCanonicalBundle validates and imports one reviewable draft per flow.
func (c *Client) ImportCanonicalBundle(
	ctx context.Context,
	flows []CanonicalFlow,
	idempotencyKey string,
) ([]AuthoringImportResult, error) {
	out, err := doWithHeaders[struct {
		Imports []AuthoringImportResult `json:"imports"`
	}](
		ctx, c, http.MethodPost, "/v1/authoring/import-bundle",
		struct {
			FormatVersion string          `json:"format_version"`
			Kind          string          `json:"kind"`
			Flows         []CanonicalFlow `json:"flows"`
		}{
			FormatVersion: "intraktible.authoring/v1",
			Kind:          "bundle",
			Flows:         flows,
		},
		map[string]string{"Idempotency-Key": idempotencyKey},
	)
	return out.Imports, err
}

func (c *Client) CreateDraft(
	ctx context.Context,
	draft DraftWrite,
	idempotencyKey string,
) (string, error) {
	out, err := doWithHeaders[struct {
		DraftID string `json:"draft_id"`
	}](ctx, c, http.MethodPost, "/v1/authoring/drafts", draft,
		map[string]string{"Idempotency-Key": idempotencyKey})
	return out.DraftID, err
}

func (c *Client) ListDrafts(ctx context.Context, flowID string) ([]AuthoringDraft, error) {
	path := "/v1/authoring/drafts"
	if flowID != "" {
		path += "?flow_id=" + url.QueryEscape(flowID)
	}
	out, err := do[struct {
		Drafts []AuthoringDraft `json:"drafts"`
	}](ctx, c, http.MethodGet, path, nil)
	return out.Drafts, err
}

func (c *Client) SaveDraft(
	ctx context.Context,
	draftID string,
	save DraftSave,
) (int, error) {
	out, err := do[struct {
		Revision int `json:"revision"`
	}](
		ctx, c, http.MethodPut,
		"/v1/authoring/drafts/"+url.PathEscape(draftID), save,
	)
	return out.Revision, err
}

// GetDraft returns the authoritative accepted draft snapshot.
func (c *Client) GetDraft(ctx context.Context, draftID string) (AuthoringDraft, error) {
	return do[AuthoringDraft](
		ctx, c, http.MethodGet,
		"/v1/authoring/drafts/"+url.PathEscape(draftID), nil,
	)
}

// RebaseDraft records the resolved graph against a newer published base.
func (c *Client) RebaseDraft(
	ctx context.Context,
	draftID string,
	rebase DraftRebase,
) (int, error) {
	out, err := do[struct {
		Revision int `json:"revision"`
	}](
		ctx, c, http.MethodPost,
		"/v1/authoring/drafts/"+url.PathEscape(draftID)+"/rebase", rebase,
	)
	return out.Revision, err
}

// ListDraftRevisions returns immutable autosave checkpoints.
func (c *Client) ListDraftRevisions(
	ctx context.Context,
	draftID string,
) ([]AuthoringDraftRevision, error) {
	out, err := do[struct {
		Revisions []AuthoringDraftRevision `json:"revisions"`
	}](
		ctx, c, http.MethodGet,
		"/v1/authoring/drafts/"+url.PathEscape(draftID)+"/revisions", nil,
	)
	return out.Revisions, err
}

// ArchiveDraft explicitly closes the active working draft.
func (c *Client) ArchiveDraft(ctx context.Context, draftID string) error {
	_, err := do[map[string]any](
		ctx, c, http.MethodDelete,
		"/v1/authoring/drafts/"+url.PathEscape(draftID), nil,
	)
	return err
}

// RenewDraftPresence upserts this actor's disposable collaboration lease.
func (c *Client) RenewDraftPresence(
	ctx context.Context,
	draftID string,
	input any,
) (DraftPresence, error) {
	return do[DraftPresence](
		ctx, c, http.MethodPut,
		"/v1/authoring/drafts/"+url.PathEscape(draftID)+"/presence", input,
	)
}

// ListDraftPresence returns only unexpired collaborator leases.
func (c *Client) ListDraftPresence(
	ctx context.Context,
	draftID string,
) ([]DraftPresence, error) {
	out, err := do[struct {
		Presence []DraftPresence `json:"presence"`
	}](
		ctx, c, http.MethodGet,
		"/v1/authoring/drafts/"+url.PathEscape(draftID)+"/presence", nil,
	)
	return out.Presence, err
}

// LeaveDraftPresence removes this actor's disposable collaboration lease.
func (c *Client) LeaveDraftPresence(ctx context.Context, draftID string) error {
	_, err := do[struct{}](
		ctx, c, http.MethodDelete,
		"/v1/authoring/drafts/"+url.PathEscape(draftID)+"/presence", nil,
	)
	return err
}

func (c *Client) CreateChangeSet(
	ctx context.Context,
	input any,
	idempotencyKey string,
) (string, error) {
	out, err := doWithHeaders[struct {
		ChangeSetID string `json:"changeset_id"`
	}](ctx, c, http.MethodPost, "/v1/authoring/changesets", input,
		map[string]string{"Idempotency-Key": idempotencyKey})
	return out.ChangeSetID, err
}

func (c *Client) ListChangeSets(ctx context.Context, flowID string) ([]ChangeSet, error) {
	path := "/v1/authoring/changesets"
	if flowID != "" {
		path += "?flow_id=" + url.QueryEscape(flowID)
	}
	out, err := do[struct {
		ChangeSets []ChangeSet `json:"changesets"`
	}](ctx, c, http.MethodGet, path, nil)
	return out.ChangeSets, err
}

// GetChangeSet reads one immutable review artifact and its current evidence.
func (c *Client) GetChangeSet(ctx context.Context, changeSetID string) (ChangeSet, error) {
	return do[ChangeSet](
		ctx, c, http.MethodGet,
		"/v1/authoring/changesets/"+url.PathEscape(changeSetID), nil,
	)
}

// ChangeSetDiff returns the server-owned execution-relevant semantic diff.
func (c *Client) ChangeSetDiff(
	ctx context.Context,
	changeSetID string,
) (json.RawMessage, error) {
	return do[json.RawMessage](
		ctx, c, http.MethodGet,
		"/v1/authoring/changesets/"+url.PathEscape(changeSetID)+"/diff", nil,
	)
}

func (c *Client) ChangeSetAction(
	ctx context.Context,
	changeSetID, action string,
	body any,
) (json.RawMessage, error) {
	return do[json.RawMessage](
		ctx, c, http.MethodPost,
		"/v1/authoring/changesets/"+url.PathEscape(changeSetID)+"/"+url.PathEscape(action),
		body,
	)
}

func (c *Client) ListReusableComponents(ctx context.Context) ([]ReusableComponent, error) {
	out, err := do[struct {
		Components []ReusableComponent `json:"components"`
	}](ctx, c, http.MethodGet, "/v1/authoring/components", nil)
	return out.Components, err
}

// CreateReusableComponent registers an idempotent component identity.
func (c *Client) CreateReusableComponent(
	ctx context.Context,
	input any,
	idempotencyKey string,
) (string, error) {
	out, err := doWithHeaders[struct {
		ComponentID string `json:"component_id"`
	}](
		ctx, c, http.MethodPost, "/v1/authoring/components", input,
		map[string]string{"Idempotency-Key": idempotencyKey},
	)
	return out.ComponentID, err
}

// PublishReusableComponent publishes an immutable contract-aware version.
func (c *Client) PublishReusableComponent(
	ctx context.Context,
	componentID string,
	input any,
	idempotencyKey string,
) (int, string, error) {
	out, err := doWithHeaders[struct {
		Version int    `json:"version"`
		Etag    string `json:"etag"`
	}](
		ctx, c, http.MethodPost,
		"/v1/authoring/components/"+url.PathEscape(componentID)+"/versions",
		input, map[string]string{"Idempotency-Key": idempotencyKey},
	)
	return out.Version, out.Etag, err
}

// AssessComponentCompatibility returns migration blockers and affected consumers.
func (c *Client) AssessComponentCompatibility(
	ctx context.Context,
	componentID string,
	fromVersion, toVersion int,
) (json.RawMessage, error) {
	path := "/v1/authoring/components/" + url.PathEscape(componentID) +
		"/compatibility?from_version=" + strconv.Itoa(fromVersion) +
		"&to_version=" + strconv.Itoa(toVersion)
	return do[json.RawMessage](ctx, c, http.MethodGet, path, nil)
}

// CreateComponentUpgradeDrafts creates selected compatible flow migrations as
// ordinary governed drafts; it never republishes consumers.
func (c *Client) CreateComponentUpgradeDrafts(
	ctx context.Context,
	componentID string,
	input any,
	idempotencyKey string,
) (json.RawMessage, error) {
	return doWithHeaders[json.RawMessage](
		ctx, c, http.MethodPost,
		"/v1/authoring/components/"+url.PathEscape(componentID)+"/upgrade-drafts",
		input, map[string]string{"Idempotency-Key": idempotencyKey},
	)
}

func (c *Client) ComponentConsumers(
	ctx context.Context,
	componentID string,
	version int,
) ([]json.RawMessage, error) {
	out, err := do[struct {
		Consumers []json.RawMessage `json:"consumers"`
	}](
		ctx, c, http.MethodGet,
		"/v1/authoring/components/"+url.PathEscape(componentID)+
			"/versions/"+strconv.Itoa(version)+"/consumers", nil,
	)
	return out.Consumers, err
}
