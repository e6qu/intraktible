// SPDX-License-Identifier: AGPL-3.0-or-later

// Package service exposes modeling governance over HTTP and adapts approved
// source contracts to Context Layer ingestion.
package service

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	contextdomain "github.com/e6qu/intraktible/context-layer/domain"
	decisionevents "github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/modeling/command"
	"github.com/e6qu/intraktible/modeling/domain"
	modelprojection "github.com/e6qu/intraktible/modeling/projection"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// Service is the modeling HTTP shell and source-contract adapter.
type Service struct {
	cmd           *command.Handler
	store         store.Store
	now           func() time.Time
	workers       workerState
	models        ModelRegistrar
	privateKey    ed25519.PrivateKey
	publicKey     ed25519.PublicKey
	contentSealer ContentSealer
}

// ContentSealer protects each source subject independently so erasure destroys
// that subject's snapshot and materialization content without rewriting audit
// events or lineage manifests.
type ContentSealer interface {
	Seal(
		context.Context,
		identity.Identity,
		string,
		[]byte,
	) ([]byte, error)
	Open(
		context.Context,
		identity.Identity,
		string,
		[]byte,
	) ([]byte, error)
}

// ModelRegistrar is the existing production model registry command surface.
type ModelRegistrar interface {
	DefineModelWithLineage(
		context.Context,
		identity.Identity,
		string,
		json.RawMessage,
		decisionevents.ModelLineage,
		decisionevents.TrainingPublication,
	) (eventlog.Envelope, error)
}

// Option configures a Service.
type Option func(*Service)

// WithNow overrides the evidence clock for deterministic tests and demo builds.
func WithNow(now func() time.Time) Option {
	return func(service *Service) { service.now = now }
}

// WithContentSealer binds model-data rows to the platform subject-key vault.
func WithContentSealer(sealer ContentSealer) Option {
	return func(service *Service) { service.contentSealer = sealer }
}

// WithArtifactSigningKey sets the deployment trust root used by every training
// worker replica. The key must be Ed25519 private-key material.
func WithArtifactSigningKey(privateKey ed25519.PrivateKey) Option {
	return func(service *Service) {
		if len(privateKey) != ed25519.PrivateKeySize {
			panic("modeling: artifact signing key must be an Ed25519 private key")
		}
		service.privateKey = append(ed25519.PrivateKey(nil), privateKey...)
		service.publicKey = append(ed25519.PublicKey(nil), privateKey.Public().(ed25519.PublicKey)...)
	}
}

// New builds the service.
func New(cmd *command.Handler, st store.Store, options ...Option) *Service {
	service := &Service{cmd: cmd, store: st, now: func() time.Time { return time.Now().UTC() }}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic("modeling: crypto/rand unavailable: " + err.Error())
	}
	service.privateKey, service.publicKey = privateKey, publicKey
	for _, option := range options {
		option(service)
	}
	return service
}

// UseModelRegistrar connects training jobs to the existing governed model
// registry. Without it, training requests fail loudly.
func (s *Service) UseModelRegistrar(registrar ModelRegistrar) {
	s.models = registrar
}

// Routes registers governed schema endpoints.
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/modeling/schemas", s.defineSchema)
	mux.HandleFunc("GET /v1/modeling/schemas", s.listSchemas)
	mux.HandleFunc("GET /v1/modeling/schemas/{kind}/{entity_type}", s.getSchema)
	mux.HandleFunc(
		"POST /v1/modeling/schemas/{kind}/{entity_type}/versions/{version}/approval-request",
		s.requestApproval,
	)
	mux.HandleFunc("POST /v1/modeling/schema-approval/{request_id}/decision", s.decideApproval)
	mux.HandleFunc(
		"POST /v1/modeling/schemas/{kind}/{entity_type}/versions/{version}/retire",
		s.retireSchema,
	)
	mux.HandleFunc("GET /v1/modeling/quality/observations", s.listQualityObservations)
	mux.HandleFunc("GET /v1/modeling/quality/incidents", s.listQualityIncidents)
	mux.HandleFunc(
		"POST /v1/modeling/quality/incidents/{incident_id}/acknowledge",
		s.acknowledgeQualityIncident,
	)
	mux.HandleFunc(
		"POST /v1/modeling/quality/incidents/{incident_id}/resolve",
		s.resolveQualityIncident,
	)
	mux.HandleFunc("GET /v1/modeling/source-health", s.listSourceHealth)
	mux.HandleFunc("GET /v1/modeling/features", s.listGovernedFeatures)
	mux.HandleFunc("POST /v1/modeling/datasets", s.defineDataset)
	mux.HandleFunc("GET /v1/modeling/datasets", s.listDatasets)
	mux.HandleFunc("GET /v1/modeling/datasets/{name}", s.getDataset)
	mux.HandleFunc(
		"POST /v1/modeling/datasets/{name}/versions/{version}/snapshots",
		s.requestSnapshot,
	)
	mux.HandleFunc("GET /v1/modeling/snapshots", s.listSnapshots)
	mux.HandleFunc("GET /v1/modeling/snapshots/{snapshot_id}", s.getSnapshot)
	mux.HandleFunc("GET /v1/modeling/snapshots/{snapshot_id}/rows", s.getSnapshotRows)
	mux.HandleFunc("GET /v1/modeling/snapshots/{snapshot_id}/export", s.exportSnapshot)
	mux.HandleFunc("GET /v1/modeling/jobs", s.listJobs)
	mux.HandleFunc("GET /v1/modeling/jobs/{job_id}", s.getJob)
	mux.HandleFunc("POST /v1/modeling/jobs/{job_id}/pause", s.pauseJob)
	mux.HandleFunc("POST /v1/modeling/jobs/{job_id}/resume", s.resumeJob)
	mux.HandleFunc("POST /v1/modeling/jobs/{job_id}/retry", s.retryJob)
	mux.HandleFunc("POST /v1/modeling/jobs/{job_id}/cancel", s.cancelJob)
	mux.HandleFunc("POST /v1/modeling/training-jobs", s.requestTraining)
	mux.HandleFunc("POST /v1/modeling/evaluation-jobs", s.requestEvaluation)
	mux.HandleFunc("GET /v1/modeling/evaluations", s.listEvaluations)
	mux.HandleFunc("GET /v1/modeling/evaluations/{evaluation_id}", s.getEvaluation)
	mux.HandleFunc("GET /v1/modeling/artifacts", s.listArtifacts)
	mux.HandleFunc("POST /v1/modeling/artifacts", s.registerArtifact)
	mux.HandleFunc("GET /v1/modeling/artifacts/{artifact_id}", s.getArtifact)
	mux.HandleFunc("GET /v1/modeling/artifacts/{artifact_id}/verify", s.verifyArtifact)
	mux.HandleFunc("POST /v1/modeling/artifacts/{artifact_id}/stage", s.changeArtifactStage)
	mux.HandleFunc("POST /v1/modeling/backfills", s.requestBackfill)
	mux.HandleFunc("GET /v1/modeling/materializations", s.listMaterializations)
	mux.HandleFunc("GET /v1/modeling/materializations/{backfill_id}", s.getMaterialization)
	mux.HandleFunc("GET /v1/modeling/lineage/models/{model_name}", s.modelLineage)
	mux.HandleFunc("GET /v1/modeling/comparisons", s.compareModels)
}

type resolveIncidentRequest struct {
	Reason string `json:"reason"`
}

type acknowledgeIncidentRequest struct {
	Note string `json:"note"`
}

func (s *Service) acknowledgeQualityIncident(w http.ResponseWriter, r *http.Request) {
	var request acknowledgeIncidentRequest
	httpx.Emit(w, r, &request, func(id identity.Identity) (eventlog.Envelope, error) {
		return s.cmd.AcknowledgeQualityIncident(
			r.Context(), id, r.PathValue("incident_id"), request.Note,
		)
	})
}

func (s *Service) resolveQualityIncident(w http.ResponseWriter, r *http.Request) {
	var request resolveIncidentRequest
	httpx.Emit(w, r, &request, func(id identity.Identity) (eventlog.Envelope, error) {
		return s.cmd.ResolveQualityIncident(
			r.Context(), id, r.PathValue("incident_id"), request.Reason,
		)
	})
}

func (s *Service) listSourceHealth(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	health, err := modelprojection.ListSourceHealth(r.Context(), s.store, id)
	httpx.WriteList(w, "sources", health, err)
}

func (s *Service) listQualityObservations(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	observations, err := modelprojection.ListQualityObservations(r.Context(), s.store, id)
	httpx.WriteList(w, "observations", observations, err)
}

func (s *Service) listQualityIncidents(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	incidents, err := modelprojection.ListQualityIncidents(r.Context(), s.store, id)
	httpx.WriteList(w, "incidents", incidents, err)
}

func (s *Service) defineSchema(w http.ResponseWriter, r *http.Request) {
	var spec domain.SchemaSpec
	httpx.Emit(w, r, &spec, func(id identity.Identity) (eventlog.Envelope, error) {
		return s.cmd.DefineSchema(r.Context(), id, spec)
	})
}

func (s *Service) listSchemas(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	views, err := modelprojection.ListSchemas(r.Context(), s.store, id)
	httpx.WriteList(w, "schemas", views, err)
}

func (s *Service) getSchema(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	ref, err := refFromRequest(r)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	view, found, err := modelprojection.ReadSchema(r.Context(), s.store, id, ref)
	httpx.WriteOne(w, view, found, err, "schema not found")
}

func (s *Service) requestApproval(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	ref, version, err := versionRefFromRequest(r)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	requestID, envelope, err := s.cmd.RequestSchemaApproval(r.Context(), id, ref, version)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, map[string]any{
		"event_id": envelope.ID, "seq": envelope.Seq, "request_id": requestID,
	})
}

type approvalDecisionRequest struct {
	Ref     domain.SchemaRef `json:"ref"`
	Approve bool             `json:"approve"`
	Reason  string           `json:"reason,omitempty"`
}

func (s *Service) decideApproval(w http.ResponseWriter, r *http.Request) {
	var request approvalDecisionRequest
	httpx.Emit(w, r, &request, func(id identity.Identity) (eventlog.Envelope, error) {
		return s.cmd.DecideSchemaApproval(
			r.Context(), id, request.Ref, r.PathValue("request_id"), request.Approve, request.Reason,
		)
	})
}

type retireRequest struct {
	Reason string `json:"reason"`
}

func (s *Service) retireSchema(w http.ResponseWriter, r *http.Request) {
	var request retireRequest
	httpx.Emit(w, r, &request, func(id identity.Identity) (eventlog.Envelope, error) {
		ref, version, err := versionRefFromRequest(r)
		if err != nil {
			return eventlog.Envelope{}, err
		}
		dependants, err := s.schemaDependents(r.Context(), id, ref)
		if err != nil {
			return eventlog.Envelope{}, err
		}
		if len(dependants) > 0 {
			return eventlog.Envelope{}, dependantsError(dependants)
		}
		return s.cmd.RetireSchema(r.Context(), id, ref, version, request.Reason)
	})
}

func refFromRequest(r *http.Request) (domain.SchemaRef, error) {
	ref := domain.SchemaRef{
		Kind: domain.SchemaKind(r.PathValue("kind")), EntityType: r.PathValue("entity_type"),
		EventName: strings.TrimSpace(r.URL.Query().Get("event_name")),
	}
	if err := ref.Validate(); err != nil {
		return domain.SchemaRef{}, err
	}
	return ref, nil
}

func versionRefFromRequest(r *http.Request) (domain.SchemaRef, int, error) {
	ref, err := refFromRequest(r)
	if err != nil {
		return domain.SchemaRef{}, 0, err
	}
	version, err := strconv.Atoi(r.PathValue("version"))
	if err != nil || version <= 0 {
		return domain.SchemaRef{}, 0, errors.New("modeling: version must be a positive integer")
	}
	return ref, version, nil
}

// ValidateEntity implements the Context Layer source-contract port.
func (s *Service) ValidateEntity(
	ctx context.Context,
	id identity.Identity,
	entityType string,
	version int,
	document json.RawMessage,
) (contextdomain.SchemaEvidence, error) {
	return s.validateDocument(ctx, id, domain.SchemaRef{
		Kind: domain.SchemaKindEntity, EntityType: entityType,
	}, version, document)
}

// ValidateEvent implements the Context Layer source-contract port.
func (s *Service) ValidateEvent(
	ctx context.Context,
	id identity.Identity,
	entityType string,
	eventName string,
	occurredAt time.Time,
	version int,
	document json.RawMessage,
) (contextdomain.SchemaEvidence, error) {
	evidence, err := s.validateDocument(ctx, id, domain.SchemaRef{
		Kind: domain.SchemaKindEvent, EntityType: entityType, EventName: eventName,
	}, version, document)
	if err != nil || evidence.Version == 0 {
		return evidence, err
	}
	view, found, err := modelprojection.ReadSchema(ctx, s.store, id, domain.SchemaRef{
		Kind: domain.SchemaKindEvent, EntityType: entityType, EventName: eventName,
	})
	if err != nil {
		return contextdomain.SchemaEvidence{}, err
	}
	active, ok := view.Active()
	if !found || !ok {
		return contextdomain.SchemaEvidence{}, fmt.Errorf(
			"modeling: active event schema disappeared during validation",
		)
	}
	if active.Spec.Quality.FreshnessSeconds > 0 {
		age := evidence.EvaluatedAt.Sub(occurredAt)
		if age > time.Duration(active.Spec.Quality.FreshnessSeconds)*time.Second {
			evidence.Violations = append(evidence.Violations, contextdomain.QualityViolation{
				Code: "freshness",
				Message: fmt.Sprintf(
					"event age %s exceeds freshness contract %s",
					age.Round(time.Second),
					(time.Duration(active.Spec.Quality.FreshnessSeconds) * time.Second).String(),
				),
			})
			if active.Spec.Quality.Action == domain.QualityBlock {
				return contextdomain.SchemaEvidence{}, fmt.Errorf(
					"modeling: source %s rejected by freshness contract",
					active.Spec.Ref.Key(),
				)
			}
		}
	}
	return evidence, nil
}

func (s *Service) validateDocument(
	ctx context.Context,
	id identity.Identity,
	ref domain.SchemaRef,
	requestedVersion int,
	document json.RawMessage,
) (contextdomain.SchemaEvidence, error) {
	if err := ref.Validate(); err != nil {
		return contextdomain.SchemaEvidence{}, err
	}
	view, found, err := modelprojection.ReadSchema(ctx, s.store, id, ref)
	if err != nil {
		return contextdomain.SchemaEvidence{}, err
	}
	if !found || view.ActiveVersion == 0 {
		if requestedVersion != 0 {
			return contextdomain.SchemaEvidence{}, fmt.Errorf(
				"modeling: source %s has no active schema; schema_version must be omitted", ref.Key(),
			)
		}
		return contextdomain.SchemaEvidence{}, nil
	}
	active, ok := view.Active()
	if !ok {
		return contextdomain.SchemaEvidence{}, fmt.Errorf(
			"modeling: source %s active schema version %d is unavailable", ref.Key(), view.ActiveVersion,
		)
	}
	if requestedVersion != active.Version {
		return contextdomain.SchemaEvidence{}, fmt.Errorf(
			"modeling: source %s requires active schema_version %d, got %d",
			ref.Key(), active.Version, requestedVersion,
		)
	}
	violations, err := domain.ValidateDocument(active.Spec, document)
	if err != nil {
		return contextdomain.SchemaEvidence{}, err
	}
	if len(violations) > 0 &&
		(active.Spec.Quality.Action == domain.QualityBlock ||
			active.Spec.Quality.Action == domain.QualityApprovedStale) {
		messages := make([]string, len(violations))
		for index, violation := range violations {
			messages[index] = violation.Message
		}
		return contextdomain.SchemaEvidence{}, fmt.Errorf(
			"modeling: source %s rejected by schema %d: %s",
			ref.Key(), active.Version, strings.Join(messages, "; "),
		)
	}
	evidence := contextdomain.SchemaEvidence{
		Version: active.Version, Hash: active.Hash, Action: string(active.Spec.Quality.Action),
		EvaluatedAt:          s.now(),
		PolicyApprovalID:     active.ApprovalRequestID,
		PolicyApprovedBy:     active.ApprovedBy,
		PolicyApprovalReason: active.ApprovalReason,
	}
	if active.ApprovedAt != nil {
		evidence.PolicyApprovedAt = *active.ApprovedAt
	}
	evidence.Violations = make([]contextdomain.QualityViolation, len(violations))
	for index, violation := range violations {
		evidence.Violations[index] = contextdomain.QualityViolation{
			Code: violation.Code, Field: violation.Field, Message: violation.Message,
		}
	}
	if tupleHash, present, err := domain.UniqueTupleHash(active.Spec, document); err != nil {
		return contextdomain.SchemaEvidence{}, err
	} else if present {
		evidence.UniqueClaim = "source.unique\x00" + ref.Key() + "\x00" + tupleHash
	}
	relationships, err := domain.RelationshipValues(active.Spec, document)
	if err != nil {
		return contextdomain.SchemaEvidence{}, err
	}
	evidence.RelationshipChecks = make([]contextdomain.RelationshipCheck, len(relationships))
	for index, relationship := range relationships {
		evidence.RelationshipChecks[index] = contextdomain.RelationshipCheck{
			Field: relationship.Field, TargetEntityType: relationship.TargetEntityType,
			TargetEntityID: relationship.TargetEntityID,
		}
	}
	return evidence, nil
}
