// SPDX-License-Identifier: AGPL-3.0-or-later

package service

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/e6qu/intraktible/context-layer/features"
	"github.com/e6qu/intraktible/modeling/domain"
	modelprojection "github.com/e6qu/intraktible/modeling/projection"
	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/identity"
)

// FeatureGovernanceView joins a feature definition to its source contract,
// operational materialization evidence, cost, and governed consumers.
type FeatureGovernanceView struct {
	Definition          features.FeatureView              `json:"definition"`
	SourceRef           domain.SchemaRef                  `json:"source_ref"`
	SourceSchemaVersion int                               `json:"source_schema_version,omitempty"`
	SourceSchemaHash    string                            `json:"source_schema_hash,omitempty"`
	FreshnessSeconds    int64                             `json:"freshness_seconds,omitempty"`
	SourceHealth        *modelprojection.SourceHealthView `json:"source_health,omitempty"`
	Materialization     string                            `json:"materialization_status"`
	LastSuccess         *domain.BackfillManifest          `json:"last_success,omitempty"`
	LastError           string                            `json:"last_error,omitempty"`
	LastJobID           string                            `json:"last_job_id,omitempty"`
	LastJobUpdatedAt    *time.Time                        `json:"last_job_updated_at,omitempty"`
	Cardinality         int                               `json:"cardinality"`
	StorageBytes        int64                             `json:"storage_bytes"`
	ComputeUnits        int64                             `json:"compute_units"`
	EstimatedCostUSD    float64                           `json:"estimated_cost_usd"`
	DownstreamConsumers []string                          `json:"downstream_consumers"`
}

func (s *Service) listGovernedFeatures(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	items, err := s.governedFeatures(r.Context(), id)
	httpx.WriteList(w, "features", items, err)
}

func (s *Service) governedFeatures(
	ctx context.Context,
	id identity.Identity,
) ([]FeatureGovernanceView, error) {
	definitions, err := features.List(ctx, s.store, id, "")
	if err != nil {
		return nil, err
	}
	datasets, err := modelprojection.ListDatasets(ctx, s.store, id)
	if err != nil {
		return nil, err
	}
	jobs, err := modelprojection.ListJobs(ctx, s.store, id)
	if err != nil {
		return nil, err
	}
	materializations, err := modelprojection.ListMaterializations(ctx, s.store, id)
	if err != nil {
		return nil, err
	}
	artifacts, err := modelprojection.ListArtifacts(ctx, s.store, id)
	if err != nil {
		return nil, err
	}
	out := make([]FeatureGovernanceView, 0, len(definitions))
	for _, definition := range definitions {
		ref := domain.SchemaRef{
			Kind: domain.SchemaKindEvent, EntityType: definition.EntityType,
			EventName: definition.EventName,
		}
		view := FeatureGovernanceView{
			Definition: definition, SourceRef: ref, Materialization: "read_through",
			DownstreamConsumers: []string{},
		}
		schema, found, err := modelprojection.ReadSchema(ctx, s.store, id, ref)
		if err != nil {
			return nil, err
		}
		if found {
			if active, ok := schema.Active(); ok {
				view.SourceSchemaVersion, view.SourceSchemaHash =
					active.Version, active.Hash
				view.FreshnessSeconds = active.Spec.Quality.FreshnessSeconds
			}
		}
		health, found, err := modelprojection.ReadSourceHealth(ctx, s.store, id, ref)
		if err != nil {
			return nil, err
		}
		if found {
			view.SourceHealth = &health
		}
		for _, materialization := range materializations {
			if materialization.Manifest.EntityType != definition.EntityType ||
				!containsString(materialization.Manifest.Features, definition.Name) {
				continue
			}
			if view.LastSuccess == nil ||
				materialization.Manifest.PublishedAt.After(view.LastSuccess.PublishedAt) {
				manifest := materialization.Manifest
				view.LastSuccess = &manifest
				view.Cardinality, view.StorageBytes =
					manifest.RowCount, manifest.SizeBytes
			}
		}
		for _, job := range jobs {
			if job.Kind != "backfill" ||
				job.BackfillRequest.EntityType != definition.EntityType ||
				!containsString(job.BackfillRequest.Features, definition.Name) {
				continue
			}
			if view.LastJobUpdatedAt == nil || job.UpdatedAt.After(*view.LastJobUpdatedAt) {
				updatedAt := job.UpdatedAt
				view.LastJobID, view.LastJobUpdatedAt = job.JobID, &updatedAt
				view.Materialization = job.State
				view.LastError = job.Error
				view.ComputeUnits, view.EstimatedCostUSD =
					job.ComputeUnits, job.EstimatedCostUSD
			}
		}
		for _, dataset := range datasets {
			for _, version := range dataset.Versions {
				if version.Spec.EntityType == definition.EntityType &&
					containsString(version.Spec.Features, definition.Name) {
					view.DownstreamConsumers = append(
						view.DownstreamConsumers,
						"dataset:"+dataset.Name+"@"+strconv.Itoa(version.Version),
					)
				}
			}
		}
		for _, artifact := range artifacts {
			if artifact.Origin != domain.ArtifactPlatformTrained {
				continue
			}
			for _, consumer := range view.DownstreamConsumers {
				if consumer == "dataset:"+artifact.Lineage.DatasetName+
					"@"+strconv.Itoa(artifact.Lineage.DatasetVersion) {
					view.DownstreamConsumers = append(
						view.DownstreamConsumers,
						"artifact:"+artifact.ArtifactID,
						"model:"+artifact.ModelName,
					)
					break
				}
			}
		}
		sort.Strings(view.DownstreamConsumers)
		view.DownstreamConsumers = uniqueStrings(view.DownstreamConsumers)
		out = append(out, view)
	}
	return out, nil
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func uniqueStrings(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	out := values[:1]
	for _, value := range values[1:] {
		if value != out[len(out)-1] {
			out = append(out, value)
		}
	}
	return out
}
