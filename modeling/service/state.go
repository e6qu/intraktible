// SPDX-License-Identifier: AGPL-3.0-or-later

package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	modelprojection "github.com/e6qu/intraktible/modeling/projection"
	"github.com/e6qu/intraktible/platform/store"
)

// OperationalState contains modeling content that is deliberately stored
// outside the append-only event log. Events retain immutable manifests and
// hashes; these records retain the encrypted snapshot, artifact, and
// materialization bytes those manifests address. Backups and the browser demo
// must restore this state before replay starts.
type OperationalState struct {
	SnapshotBlobs        []store.Record `json:"snapshot_blobs,omitempty"`
	ArtifactBlobs        []store.Record `json:"artifact_blobs,omitempty"`
	MaterializationBlobs []store.Record `json:"materialization_blobs,omitempty"`
}

// ExportOperationalState snapshots modeling-owned operational collections.
func ExportOperationalState(ctx context.Context, st store.Store) (OperationalState, error) {
	snapshots, err := st.List(ctx, modelprojection.CollectionSnapshotBlobs, "")
	if err != nil {
		return OperationalState{}, fmt.Errorf("modeling: export snapshot blobs: %w", err)
	}
	artifacts, err := st.List(ctx, artifactBlobCollection, "")
	if err != nil {
		return OperationalState{}, fmt.Errorf("modeling: export artifact blobs: %w", err)
	}
	materializations, err := st.List(ctx, materializationBlobCollection, "")
	if err != nil {
		return OperationalState{}, fmt.Errorf("modeling: export materialization blobs: %w", err)
	}
	return OperationalState{
		SnapshotBlobs: snapshots, ArtifactBlobs: artifacts,
		MaterializationBlobs: materializations,
	}, nil
}

// RestoreOperationalState restores modeling content into an empty operational
// store. It validates every record and refuses to overwrite existing content.
func RestoreOperationalState(ctx context.Context, st store.Store, state OperationalState) error {
	if err := restoreModelingRecords(
		ctx, st, modelprojection.CollectionSnapshotBlobs, state.SnapshotBlobs, validateSnapshotBlob,
	); err != nil {
		return err
	}
	if err := restoreModelingRecords(
		ctx, st, artifactBlobCollection, state.ArtifactBlobs, validateArtifactBlob,
	); err != nil {
		return err
	}
	return restoreModelingRecords(
		ctx, st, materializationBlobCollection, state.MaterializationBlobs,
		validateMaterializationBlob,
	)
}

func restoreModelingRecords(
	ctx context.Context,
	st store.Store,
	collection string,
	records []store.Record,
	validate func(json.RawMessage) error,
) error {
	seen := make(map[string]struct{}, len(records))
	for _, record := range records {
		if record.Key == "" {
			return fmt.Errorf("modeling: restore %s: empty key", collection)
		}
		if _, duplicate := seen[record.Key]; duplicate {
			return fmt.Errorf("modeling: restore %s: duplicate key %q", collection, record.Key)
		}
		seen[record.Key] = struct{}{}
		if err := validate(record.Doc); err != nil {
			return fmt.Errorf("modeling: restore %s key %q: %w", collection, record.Key, err)
		}
		if _, exists, err := st.Get(ctx, collection, record.Key); err != nil {
			return fmt.Errorf("modeling: restore %s key %q: check existing: %w", collection, record.Key, err)
		} else if exists {
			return fmt.Errorf("modeling: restore %s key %q: operational record already exists", collection, record.Key)
		}
		if err := st.Put(ctx, collection, record.Key, record.Doc); err != nil {
			return fmt.Errorf("modeling: restore %s key %q: %w", collection, record.Key, err)
		}
	}
	return nil
}

func validateSnapshotBlob(doc json.RawMessage) error {
	var blob snapshotBlob
	if err := json.Unmarshal(doc, &blob); err != nil {
		return fmt.Errorf("decode snapshot blob: %w", err)
	}
	return validateSealedRows(blob.Rows)
}

func validateMaterializationBlob(doc json.RawMessage) error {
	var blob materializationBlob
	if err := json.Unmarshal(doc, &blob); err != nil {
		return fmt.Errorf("decode materialization blob: %w", err)
	}
	return validateSealedRows(blob.Rows)
}

func validateSealedRows(rows []sealedSubjectRow) error {
	for index, row := range rows {
		if row.Subject == "" {
			return fmt.Errorf("row %d has empty subject", index)
		}
		if len(row.Sealed) == 0 {
			return fmt.Errorf("row %d has empty ciphertext", index)
		}
	}
	return nil
}

func validateArtifactBlob(doc json.RawMessage) error {
	var blob artifactBlob
	if err := json.Unmarshal(doc, &blob); err != nil {
		return fmt.Errorf("decode artifact blob: %w", err)
	}
	if blob.Hash == "" || blob.Signature == "" || blob.PublicKey == "" {
		return errors.New("artifact cryptographic identity is incomplete")
	}
	if len(blob.Content.ModelSpec) == 0 || blob.Content.SnapshotHash == "" {
		return errors.New("artifact content is incomplete")
	}
	return nil
}
