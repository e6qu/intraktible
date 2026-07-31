// SPDX-License-Identifier: AGPL-3.0-or-later

// Package projection contains replayable modeling read models.
package projection

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	contextevents "github.com/e6qu/intraktible/context-layer/events"
	decisionevents "github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/modeling/domain"
	"github.com/e6qu/intraktible/modeling/events"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

const (
	// CollectionSchemas stores governed source schema aggregates.
	CollectionSchemas = "modeling_schemas"
	// CollectionQualityObservations stores replay-stable ingestion findings.
	CollectionQualityObservations = "modeling_quality_observations"
	// CollectionQualityIncidents stores quality findings requiring operator action.
	CollectionQualityIncidents = "modeling_quality_incidents"
	// CollectionSourceHealth stores event-time watermarks and correction health.
	CollectionSourceHealth = "modeling_source_health"
	// CollectionDatasets stores immutable dataset definition histories.
	CollectionDatasets = "modeling_datasets"
	// CollectionJobs stores durable modeling job leases and outcomes.
	CollectionJobs = "modeling_jobs"
	// CollectionSnapshots stores atomically published snapshot manifests.
	CollectionSnapshots = "modeling_snapshots"
	// CollectionArtifacts stores signed model artifact manifests.
	CollectionArtifacts = "modeling_artifacts"
	// CollectionEvaluations stores independently reproduced evaluation evidence.
	CollectionEvaluations = "modeling_evaluations"
	// CollectionMaterializations stores verified feature backfill manifests.
	CollectionMaterializations = "modeling_materializations"
	// CollectionSnapshotBlobs is operational snapshot content. It is not reset
	// with replay-owned projections; expiry events delete it idempotently.
	CollectionSnapshotBlobs = "modeling_snapshot_blobs"
)

// SourceHealthView is a source's deterministic event-time operational health.
type SourceHealthView struct {
	Org                 string           `json:"org"`
	Workspace           string           `json:"workspace"`
	Ref                 domain.SchemaRef `json:"ref"`
	RecordCount         uint64           `json:"record_count"`
	LateCount           uint64           `json:"late_count"`
	CorrectionCount     uint64           `json:"correction_count"`
	RetractionCount     uint64           `json:"retraction_count"`
	ViolationCount      uint64           `json:"violation_count"`
	Watermark           time.Time        `json:"watermark,omitempty"`
	LastReceivedAt      time.Time        `json:"last_received_at,omitempty"`
	WatermarkLagSeconds int64            `json:"watermark_lag_seconds,omitempty"`
	LastSchemaVersion   int              `json:"last_schema_version,omitempty"`
	UpdatedAt           time.Time        `json:"updated_at"`
}

// QualityObservation is one governed source record's quality result.
type QualityObservation struct {
	Org           string                           `json:"org"`
	Workspace     string                           `json:"workspace"`
	ObservationID string                           `json:"observation_id"`
	Ref           domain.SchemaRef                 `json:"ref"`
	EntityID      string                           `json:"entity_id"`
	SchemaVersion int                              `json:"schema_version"`
	SchemaHash    string                           `json:"schema_hash"`
	Action        domain.QualityAction             `json:"action"`
	Violations    []contextevents.QualityViolation `json:"violations"`
	SourceEventID string                           `json:"source_event_id"`
	SourceSeq     uint64                           `json:"source_seq"`
	ObservedAt    time.Time                        `json:"observed_at"`
}

// QualityIncident is an observation whose policy requires explicit operator
// attention. Warn-only observations remain visible without opening an incident.
type QualityIncident struct {
	QualityObservation
	IncidentType        string     `json:"incident_type"`
	Severity            string     `json:"severity"`
	Status              string     `json:"status"`
	OwnerTeam           string     `json:"owner_team"`
	AffectedFrom        time.Time  `json:"affected_from"`
	AffectedTo          time.Time  `json:"affected_to"`
	AffectedSubjects    []string   `json:"affected_subjects"`
	AffectedAssets      []string   `json:"affected_assets"`
	SupersedesEventID   string     `json:"supersedes_event_id,omitempty"`
	Deadline            *time.Time `json:"deadline,omitempty"`
	AcknowledgedBy      string     `json:"acknowledged_by,omitempty"`
	AcknowledgedAt      *time.Time `json:"acknowledged_at,omitempty"`
	AcknowledgementNote string     `json:"acknowledgement_note,omitempty"`
	ResolvedBy          string     `json:"resolved_by,omitempty"`
	ResolvedAt          *time.Time `json:"resolved_at,omitempty"`
	Resolution          string     `json:"resolution,omitempty"`
}

// PendingApproval is the current checker request.
type PendingApproval struct {
	RequestID   string    `json:"request_id"`
	Version     int       `json:"version"`
	RequestedBy string    `json:"requested_by"`
	RequestedAt time.Time `json:"requested_at"`
}

// SchemaVersionView is one immutable version plus its governance state.
type SchemaVersionView struct {
	Version           int               `json:"version"`
	Spec              domain.SchemaSpec `json:"spec"`
	Hash              string            `json:"hash"`
	AuthoredBy        string            `json:"authored_by"`
	AuthoredAt        time.Time         `json:"authored_at"`
	ApprovedBy        string            `json:"approved_by,omitempty"`
	ApprovedAt        *time.Time        `json:"approved_at,omitempty"`
	ApprovalRequestID string            `json:"approval_request_id,omitempty"`
	ApprovalReason    string            `json:"approval_reason,omitempty"`
	RetiredBy         string            `json:"retired_by,omitempty"`
	RetiredAt         *time.Time        `json:"retired_at,omitempty"`
	RetireReason      string            `json:"retire_reason,omitempty"`
}

// SchemaView is a source's complete version and approval history.
type SchemaView struct {
	Org           string              `json:"org"`
	Workspace     string              `json:"workspace"`
	Ref           domain.SchemaRef    `json:"ref"`
	ActiveVersion int                 `json:"active_version"`
	Pending       *PendingApproval    `json:"pending,omitempty"`
	Versions      []SchemaVersionView `json:"versions"`
	UpdatedAt     time.Time           `json:"updated_at"`
}

// Active returns the active, non-retired version.
func (view SchemaView) Active() (SchemaVersionView, bool) {
	for _, version := range view.Versions {
		if version.Version == view.ActiveVersion && version.RetiredAt == nil {
			return version, true
		}
	}
	return SchemaVersionView{}, false
}

// Projector folds the modeling governance stream.
type Projector struct{}

// Name identifies this projector.
func (Projector) Name() string { return "modeling" }

// Collections lists replay-owned collections.
func (Projector) Collections() []string {
	return []string{
		CollectionSchemas, CollectionQualityObservations,
		CollectionQualityIncidents, CollectionSourceHealth,
		CollectionDatasets, CollectionJobs, CollectionSnapshots,
		CollectionArtifacts, CollectionEvaluations,
		CollectionMaterializations,
	}
}

// Apply folds schema lifecycle events.
func (Projector) Apply(ctx context.Context, envelope eventlog.Envelope, st store.Store) error {
	switch envelope.Type {
	case contextevents.TypeEntityRecorded:
		var payload contextevents.EntityRecorded
		if err := decode(envelope, &payload); err != nil {
			return err
		}
		ref := domain.SchemaRef{
			Kind: domain.SchemaKindEntity, EntityType: payload.EntityType,
		}
		if err := applyQualityEvidence(
			ctx, st, envelope, ref, payload.EntityID, envelope.ID, "",
			envelope.Time, envelope.Time, payload.SchemaEvidence,
		); err != nil {
			return err
		}
		return applySourceEntityHealth(ctx, st, envelope, ref, payload.SchemaEvidence)
	case contextevents.TypeEventRecorded:
		var payload contextevents.EventRecorded
		if err := decode(envelope, &payload); err != nil {
			return err
		}
		ref := domain.SchemaRef{
			Kind: domain.SchemaKindEvent, EntityType: payload.EntityType, EventName: payload.EventName,
		}
		if err := applyQualityEvidence(
			ctx, st, envelope, ref, payload.EntityID, payload.EventID,
			payload.SupersedesEventID, payload.OccurredAt, payload.ReceivedAt,
			payload.SchemaEvidence,
		); err != nil {
			return err
		}
		return applySourceEventHealth(ctx, st, envelope, ref, payload)
	case contextevents.TypeEventRetracted:
		var payload contextevents.EventRetracted
		if err := decode(envelope, &payload); err != nil {
			return err
		}
		return applySourceRetractionHealth(ctx, st, envelope, payload)
	case events.TypeDatasetDefined, events.TypeSnapshotRequested,
		events.TypeTrainingRequested,
		events.TypeEvaluationRequested,
		events.TypeBackfillRequested,
		events.TypeJobClaimed, events.TypeJobHeartbeat, events.TypeJobFailed,
		events.TypeJobProgressed, events.TypeJobPauseRequested,
		events.TypeJobPaused, events.TypeJobResumed, events.TypeJobRetryRequested,
		events.TypeJobCancelRequested, events.TypeJobCancelled,
		events.TypeSnapshotPublished, events.TypeSnapshotExpired,
		events.TypeEvaluationPublished,
		events.TypeBackfillPublished, events.TypeArtifactRegistered,
		events.TypeArtifactStageChanged:
		return applyDatasetEvent(ctx, envelope, st)
	case decisionevents.TypeModelDefined:
		return applyDatasetEvent(ctx, envelope, st)
	case events.TypeQualityIncidentAcknowledged:
		var payload events.QualityIncidentAcknowledged
		if err := decode(envelope, &payload); err != nil {
			return err
		}
		key := store.Key(envelope.Org, envelope.Workspace, payload.IncidentID)
		incident, found, err := store.GetDoc[QualityIncident](
			ctx, st, CollectionQualityIncidents, key,
		)
		if err != nil {
			return err
		}
		if !found || incident.Status != "open" {
			return fmt.Errorf(
				"modeling projection: quality incident %q is not open",
				payload.IncidentID,
			)
		}
		acknowledgedAt := envelope.Time
		incident.Status = "acknowledged"
		incident.AcknowledgedBy = envelope.Actor
		incident.AcknowledgedAt = &acknowledgedAt
		incident.AcknowledgementNote = payload.Note
		return store.PutDoc(ctx, st, CollectionQualityIncidents, key, incident)
	case events.TypeQualityIncidentResolved:
		var payload events.QualityIncidentResolved
		if err := decode(envelope, &payload); err != nil {
			return err
		}
		key := store.Key(envelope.Org, envelope.Workspace, payload.IncidentID)
		incident, found, err := store.GetDoc[QualityIncident](
			ctx, st, CollectionQualityIncidents, key,
		)
		if err != nil {
			return err
		}
		if !found || incident.Status != "acknowledged" {
			return fmt.Errorf(
				"modeling projection: quality incident %q is not acknowledged",
				payload.IncidentID,
			)
		}
		resolvedAt := envelope.Time
		incident.Status, incident.ResolvedBy = "resolved", envelope.Actor
		incident.ResolvedAt, incident.Resolution = &resolvedAt, payload.Reason
		return store.PutDoc(ctx, st, CollectionQualityIncidents, key, incident)
	case events.TypeSourceFreshnessViolated:
		var payload events.SourceFreshnessViolated
		if err := decode(envelope, &payload); err != nil {
			return err
		}
		key := store.Key(envelope.Org, envelope.Workspace, payload.IncidentID)
		owner, assets, err := qualityImpact(ctx, st, envelope, payload.Ref, payload.SchemaVersion)
		if err != nil {
			return err
		}
		return store.PutDoc(ctx, st, CollectionQualityIncidents, key, QualityIncident{
			QualityObservation: QualityObservation{
				Org: envelope.Org, Workspace: envelope.Workspace,
				ObservationID: payload.IncidentID, Ref: payload.Ref,
				SchemaVersion: payload.SchemaVersion, SchemaHash: payload.SchemaHash,
				Action: payload.Action, SourceSeq: envelope.Seq, ObservedAt: payload.DetectedAt,
				Violations: []contextevents.QualityViolation{{
					Code: "freshness",
					Message: fmt.Sprintf(
						"source has received no record since %s (deadline %s)",
						payload.LastReceivedAt.Format(time.RFC3339),
						payload.Deadline.Format(time.RFC3339),
					),
				}},
			},
			IncidentType: "freshness", Severity: qualitySeverity(payload.Action),
			Status: "open", OwnerTeam: owner,
			AffectedFrom: payload.Deadline, AffectedTo: payload.DetectedAt,
			AffectedSubjects: []string{}, AffectedAssets: assets,
			Deadline: &payload.Deadline,
		})
	case events.TypeSourceFreshnessRecovered:
		var payload events.SourceFreshnessRecovered
		if err := decode(envelope, &payload); err != nil {
			return err
		}
		key := store.Key(envelope.Org, envelope.Workspace, payload.IncidentID)
		incident, found, err := store.GetDoc[QualityIncident](
			ctx, st, CollectionQualityIncidents, key,
		)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf(
				"modeling projection: freshness incident %q is missing", payload.IncidentID,
			)
		}
		if incident.Status != "open" && incident.Status != "acknowledged" {
			return nil
		}
		recoveredAt := payload.RecoveredAt
		incident.Status, incident.ResolvedBy = "resolved", envelope.Actor
		incident.ResolvedAt, incident.Resolution = &recoveredAt, "source freshness recovered"
		return store.PutDoc(ctx, st, CollectionQualityIncidents, key, incident)
	case events.TypeSchemaVersionDefined:
		var payload events.SchemaVersionDefined
		if err := decode(envelope, &payload); err != nil {
			return err
		}
		view, err := load(ctx, st, envelope, payload.Ref)
		if err != nil {
			return err
		}
		view.Org, view.Workspace, view.Ref = envelope.Org, envelope.Workspace, payload.Ref
		view.Versions = append(view.Versions, SchemaVersionView{
			Version: payload.Version, Spec: payload.Spec, Hash: payload.Hash,
			AuthoredBy: envelope.Actor, AuthoredAt: envelope.Time,
		})
		view.Pending = nil
		view.UpdatedAt = envelope.Time
		return put(ctx, st, view)
	case events.TypeSchemaApprovalRequested:
		var payload events.SchemaApprovalRequested
		if err := decode(envelope, &payload); err != nil {
			return err
		}
		view, err := load(ctx, st, envelope, payload.Ref)
		if err != nil {
			return err
		}
		if !hasVersion(view, payload.Version) {
			return fmt.Errorf("modeling projection: schema %s version %d does not exist at seq %d",
				payload.Ref.Key(), payload.Version, envelope.Seq)
		}
		view.Pending = &PendingApproval{
			RequestID: payload.RequestID, Version: payload.Version,
			RequestedBy: envelope.Actor, RequestedAt: envelope.Time,
		}
		view.UpdatedAt = envelope.Time
		return put(ctx, st, view)
	case events.TypeSchemaApprovalApproved:
		var payload events.SchemaApprovalApproved
		if err := decode(envelope, &payload); err != nil {
			return err
		}
		view, err := load(ctx, st, envelope, payload.Ref)
		if err != nil {
			return err
		}
		if view.Pending == nil || view.Pending.RequestID != payload.RequestID ||
			view.Pending.Version != payload.Version {
			return fmt.Errorf("modeling projection: approval %q has no matching pending request at seq %d",
				payload.RequestID, envelope.Seq)
		}
		approvedAt := envelope.Time
		version, ok := versionAt(&view, payload.Version)
		if !ok {
			return fmt.Errorf("modeling projection: approved schema %s version %d is missing",
				payload.Ref.Key(), payload.Version)
		}
		version.ApprovedBy, version.ApprovedAt = envelope.Actor, &approvedAt
		version.ApprovalRequestID, version.ApprovalReason =
			payload.RequestID, payload.Reason
		view.ActiveVersion, view.Pending, view.UpdatedAt = payload.Version, nil, envelope.Time
		return put(ctx, st, view)
	case events.TypeSchemaApprovalRejected:
		var payload events.SchemaApprovalRejected
		if err := decode(envelope, &payload); err != nil {
			return err
		}
		view, err := load(ctx, st, envelope, payload.Ref)
		if err != nil {
			return err
		}
		if view.Pending == nil || view.Pending.RequestID != payload.RequestID {
			return fmt.Errorf("modeling projection: rejection %q has no matching pending request at seq %d",
				payload.RequestID, envelope.Seq)
		}
		view.Pending, view.UpdatedAt = nil, envelope.Time
		return put(ctx, st, view)
	case events.TypeSchemaVersionRetired:
		var payload events.SchemaVersionRetired
		if err := decode(envelope, &payload); err != nil {
			return err
		}
		view, err := load(ctx, st, envelope, payload.Ref)
		if err != nil {
			return err
		}
		retiredAt := payload.RetiredAt
		version, ok := versionAt(&view, payload.Version)
		if !ok {
			return fmt.Errorf("modeling projection: retired schema %s version %d is missing",
				payload.Ref.Key(), payload.Version)
		}
		version.RetiredBy, version.RetiredAt, version.RetireReason = envelope.Actor, &retiredAt, payload.Reason
		if view.ActiveVersion == payload.Version {
			view.ActiveVersion = 0
		}
		view.UpdatedAt = envelope.Time
		return put(ctx, st, view)
	default:
		return nil
	}
}

func applySourceEntityHealth(
	ctx context.Context,
	st store.Store,
	envelope eventlog.Envelope,
	ref domain.SchemaRef,
	evidence contextevents.SchemaEvidence,
) error {
	key := store.Key(envelope.Org, envelope.Workspace, ref.Key())
	view, _, err := store.GetDoc[SourceHealthView](ctx, st, CollectionSourceHealth, key)
	if err != nil {
		return err
	}
	view.Org, view.Workspace, view.Ref = envelope.Org, envelope.Workspace, ref
	view.RecordCount++
	view.ViolationCount += uint64(len(evidence.Violations))
	view.LastSchemaVersion = evidence.Version
	view.Watermark, view.LastReceivedAt = envelope.Time, envelope.Time
	view.WatermarkLagSeconds, view.UpdatedAt = 0, envelope.Time
	return store.PutDoc(ctx, st, CollectionSourceHealth, key, view)
}

func applySourceEventHealth(
	ctx context.Context,
	st store.Store,
	envelope eventlog.Envelope,
	ref domain.SchemaRef,
	payload contextevents.EventRecorded,
) error {
	key := store.Key(envelope.Org, envelope.Workspace, ref.Key())
	view, _, err := store.GetDoc[SourceHealthView](ctx, st, CollectionSourceHealth, key)
	if err != nil {
		return err
	}
	view.Org, view.Workspace, view.Ref = envelope.Org, envelope.Workspace, ref
	view.RecordCount++
	if !view.Watermark.IsZero() && payload.OccurredAt.Before(view.Watermark) {
		view.LateCount++
	}
	if payload.OccurredAt.After(view.Watermark) {
		view.Watermark = payload.OccurredAt
	}
	if payload.SupersedesEventID != "" {
		view.CorrectionCount++
	}
	view.ViolationCount += uint64(len(payload.SchemaEvidence.Violations))
	view.LastSchemaVersion = payload.SchemaEvidence.Version
	view.LastReceivedAt = payload.ReceivedAt
	if view.LastReceivedAt.IsZero() {
		view.LastReceivedAt = envelope.Time
	}
	view.WatermarkLagSeconds = int64(view.LastReceivedAt.Sub(view.Watermark).Seconds())
	if view.WatermarkLagSeconds < 0 {
		view.WatermarkLagSeconds = 0
	}
	view.UpdatedAt = envelope.Time
	return store.PutDoc(ctx, st, CollectionSourceHealth, key, view)
}

func applySourceRetractionHealth(
	ctx context.Context,
	st store.Store,
	envelope eventlog.Envelope,
	payload contextevents.EventRetracted,
) error {
	ref := domain.SchemaRef{
		Kind: domain.SchemaKindEvent, EntityType: payload.EntityType, EventName: payload.EventName,
	}
	key := store.Key(envelope.Org, envelope.Workspace, ref.Key())
	view, found, err := store.GetDoc[SourceHealthView](ctx, st, CollectionSourceHealth, key)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("modeling projection: retraction health source %s is missing at seq %d",
			ref.Key(), envelope.Seq)
	}
	view.RetractionCount++
	view.UpdatedAt = envelope.Time
	return store.PutDoc(ctx, st, CollectionSourceHealth, key, view)
}

func applyQualityEvidence(
	ctx context.Context,
	st store.Store,
	envelope eventlog.Envelope,
	ref domain.SchemaRef,
	entityID string,
	sourceEventID string,
	supersedesEventID string,
	affectedFrom time.Time,
	affectedTo time.Time,
	evidence contextevents.SchemaEvidence,
) error {
	if evidence.Version == 0 || len(evidence.Violations) == 0 {
		return nil
	}
	observation := QualityObservation{
		Org: envelope.Org, Workspace: envelope.Workspace,
		ObservationID: envelope.ID, Ref: ref, EntityID: entityID,
		SchemaVersion: evidence.Version, SchemaHash: evidence.Hash,
		Action: domain.QualityAction(evidence.Action), Violations: evidence.Violations,
		SourceEventID: sourceEventID, SourceSeq: envelope.Seq, ObservedAt: evidence.EvaluatedAt,
	}
	key := store.Key(envelope.Org, envelope.Workspace, envelope.ID)
	if err := store.PutDoc(ctx, st, CollectionQualityObservations, key, observation); err != nil {
		return err
	}
	if observation.Action != domain.QualityRefer &&
		observation.Action != domain.QualityApprovedStale {
		return nil
	}
	owner, assets, err := qualityImpact(ctx, st, envelope, ref, evidence.Version)
	if err != nil {
		return err
	}
	if affectedFrom.IsZero() {
		affectedFrom = envelope.Time
	}
	if affectedTo.IsZero() {
		affectedTo = envelope.Time
	}
	if affectedTo.Before(affectedFrom) {
		affectedTo = affectedFrom
	}
	return store.PutDoc(ctx, st, CollectionQualityIncidents, key, QualityIncident{
		QualityObservation: observation,
		IncidentType:       "record", Severity: qualitySeverity(observation.Action),
		Status: "open", OwnerTeam: owner,
		AffectedFrom: affectedFrom, AffectedTo: affectedTo,
		AffectedSubjects: []string{ref.EntityType + "/" + entityID},
		AffectedAssets:   assets, SupersedesEventID: supersedesEventID,
	})
}

func qualitySeverity(action domain.QualityAction) string {
	if action == domain.QualityApprovedStale {
		return "critical"
	}
	return "high"
}

func qualityImpact(
	ctx context.Context,
	st store.Store,
	envelope eventlog.Envelope,
	ref domain.SchemaRef,
	schemaVersion int,
) (string, []string, error) {
	schema, found, err := store.GetDoc[SchemaView](
		ctx, st, CollectionSchemas, store.Key(envelope.Org, envelope.Workspace, ref.Key()),
	)
	if err != nil {
		return "", nil, err
	}
	if !found {
		return "", nil, fmt.Errorf(
			"modeling projection: quality incident source schema %s is missing",
			ref.Key(),
		)
	}
	version, ok := versionAt(&schema, schemaVersion)
	if !ok {
		return "", nil, fmt.Errorf(
			"modeling projection: quality incident schema %s version %d is missing",
			ref.Key(), schemaVersion,
		)
	}
	assets := []string{"schema:" + ref.Key()}
	datasets, err := store.ListDocs[DatasetView](
		ctx, st, CollectionDatasets, store.Key(envelope.Org, envelope.Workspace, ""),
	)
	if err != nil {
		return "", nil, err
	}
	affectedDatasets := map[string]bool{}
	for _, dataset := range datasets {
		for _, datasetVersion := range dataset.Versions {
			if datasetVersion.Spec.EntityType == ref.EntityType {
				affectedDatasets[dataset.Name] = true
				break
			}
		}
	}
	for datasetName := range affectedDatasets {
		assets = append(assets, "dataset:"+datasetName)
	}
	artifacts, err := store.ListDocs[ArtifactView](
		ctx, st, CollectionArtifacts, store.Key(envelope.Org, envelope.Workspace, ""),
	)
	if err != nil {
		return "", nil, err
	}
	for _, artifact := range artifacts {
		if affectedDatasets[artifact.Lineage.DatasetName] {
			assets = append(assets, "model:"+artifact.ModelName)
		}
	}
	sort.Strings(assets)
	return version.Spec.OwnerTeam, assets, nil
}

func load(
	ctx context.Context,
	st store.Store,
	envelope eventlog.Envelope,
	ref domain.SchemaRef,
) (SchemaView, error) {
	view, _, err := store.GetDoc[SchemaView](
		ctx, st, CollectionSchemas, store.Key(envelope.Org, envelope.Workspace, ref.Key()),
	)
	return view, err
}

func put(ctx context.Context, st store.Store, view SchemaView) error {
	sort.Slice(view.Versions, func(i, j int) bool {
		return view.Versions[i].Version < view.Versions[j].Version
	})
	return store.PutDoc(ctx, st, CollectionSchemas, store.Key(view.Org, view.Workspace, view.Ref.Key()), view)
}

func versionAt(view *SchemaView, wanted int) (*SchemaVersionView, bool) {
	for index := range view.Versions {
		if view.Versions[index].Version == wanted {
			return &view.Versions[index], true
		}
	}
	return nil, false
}

func hasVersion(view SchemaView, wanted int) bool {
	_, ok := versionAt(&view, wanted)
	return ok
}

func decode(envelope eventlog.Envelope, payload any) error {
	if err := json.Unmarshal(envelope.Payload, payload); err != nil {
		return fmt.Errorf("modeling projection: decode %s seq %d: %w", envelope.Type, envelope.Seq, err)
	}
	return nil
}

// ReadSchema returns one tenant schema.
func ReadSchema(
	ctx context.Context,
	st store.Store,
	id identity.Identity,
	ref domain.SchemaRef,
) (SchemaView, bool, error) {
	return store.GetDoc[SchemaView](ctx, st, CollectionSchemas, store.Key(id.Org, id.Workspace, ref.Key()))
}

// ListSchemas returns tenant schemas in stable source order.
func ListSchemas(ctx context.Context, st store.Store, id identity.Identity) ([]SchemaView, error) {
	return store.QueryDocs(ctx, st, CollectionSchemas, store.Key(id.Org, id.Workspace, ""), nil,
		func(left, right SchemaView) bool { return left.Ref.Key() < right.Ref.Key() })
}

// ListQualityObservations returns quality findings newest first.
func ListQualityObservations(
	ctx context.Context,
	st store.Store,
	id identity.Identity,
) ([]QualityObservation, error) {
	return store.QueryDocs(ctx, st, CollectionQualityObservations, store.Key(id.Org, id.Workspace, ""), nil,
		func(left, right QualityObservation) bool { return left.SourceSeq > right.SourceSeq })
}

// ListQualityIncidents returns incidents newest first.
func ListQualityIncidents(
	ctx context.Context,
	st store.Store,
	id identity.Identity,
) ([]QualityIncident, error) {
	return store.QueryDocs(ctx, st, CollectionQualityIncidents, store.Key(id.Org, id.Workspace, ""), nil,
		func(left, right QualityIncident) bool { return left.SourceSeq > right.SourceSeq })
}

// ListSourceHealth returns event-time watermarks and correction health.
func ListSourceHealth(
	ctx context.Context,
	st store.Store,
	id identity.Identity,
) ([]SourceHealthView, error) {
	return store.QueryDocs(ctx, st, CollectionSourceHealth, store.Key(id.Org, id.Workspace, ""), nil,
		func(left, right SourceHealthView) bool { return left.Ref.Key() < right.Ref.Key() })
}

// ReadSourceHealth returns the current health watermark for one source.
func ReadSourceHealth(
	ctx context.Context,
	st store.Store,
	id identity.Identity,
	ref domain.SchemaRef,
) (SourceHealthView, bool, error) {
	return store.GetDoc[SourceHealthView](
		ctx, st, CollectionSourceHealth, store.Key(id.Org, id.Workspace, ref.Key()),
	)
}
