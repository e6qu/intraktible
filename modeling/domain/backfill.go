// SPDX-License-Identifier: AGPL-3.0-or-later

package domain

import (
	"errors"
	"strings"
	"time"
)

// BackfillRequest pins a point-in-time feature materialization.
type BackfillRequest struct {
	EntityType     string    `json:"entity_type"`
	Features       []string  `json:"features"`
	AsOf           time.Time `json:"as_of"`
	KnowledgeAt    time.Time `json:"knowledge_at"`
	IdempotencyKey string    `json:"idempotency_key"`
}

// Validate checks the durable backfill request.
func (request BackfillRequest) Validate() error {
	switch {
	case strings.TrimSpace(request.EntityType) == "":
		return errors.New("modeling: backfill entity_type is required")
	case len(request.Features) == 0:
		return errors.New("modeling: backfill requires at least one feature")
	case request.AsOf.IsZero() || request.KnowledgeAt.IsZero():
		return errors.New("modeling: backfill as_of and knowledge_at are required")
	case request.KnowledgeAt.Before(request.AsOf):
		return errors.New("modeling: backfill knowledge_at must not be before as_of")
	case len(strings.TrimSpace(request.IdempotencyKey)) < 8:
		return errors.New("modeling: backfill idempotency_key must be at least 8 characters")
	}
	seen := map[string]bool{}
	for _, feature := range request.Features {
		if strings.TrimSpace(feature) == "" || seen[feature] {
			return errors.New("modeling: backfill feature names must be non-empty and unique")
		}
		seen[feature] = true
	}
	return nil
}

// MaterializedFeatureRow is one backfilled entity value set.
type MaterializedFeatureRow struct {
	EntityID string             `json:"entity_id"`
	Values   map[string]float64 `json:"values"`
}

// BackfillManifest describes one verified immutable materialization.
type BackfillManifest struct {
	BackfillID      string         `json:"backfill_id"`
	EntityType      string         `json:"entity_type"`
	Features        []string       `json:"features"`
	FeatureVersions map[string]int `json:"feature_versions"`
	AsOf            time.Time      `json:"as_of"`
	KnowledgeAt     time.Time      `json:"knowledge_at"`
	RowCount        int            `json:"row_count"`
	SizeBytes       int64          `json:"size_bytes"`
	RowsHash        string         `json:"rows_hash"`
	StorageRef      string         `json:"storage_ref"`
	PublishedAt     time.Time      `json:"published_at"`
}
