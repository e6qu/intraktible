// SPDX-License-Identifier: AGPL-3.0-or-later

package service

import (
	"context"
	"encoding/json"
	"testing"

	modelprojection "github.com/e6qu/intraktible/modeling/projection"
	"github.com/e6qu/intraktible/platform/store"
)

func TestOperationalStateRoundTripAndRefusesMerge(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	source := store.NewMemory()
	snapshot := snapshotBlob{Rows: []sealedSubjectRow{{Subject: "applicant/1", Sealed: []byte("sealed")}}}
	artifact := artifactBlob{
		Content: artifactContent{ModelSpec: json.RawMessage(`{"name":"risk"}`), SnapshotHash: "snap-hash"},
		Hash:    "artifact-hash", Signature: "signature", PublicKey: "public-key",
	}
	materialization := materializationBlob{
		Rows: []sealedSubjectRow{{Subject: "applicant/1", Sealed: []byte("sealed")}},
	}
	if err := store.PutDoc(ctx, source, modelprojection.CollectionSnapshotBlobs, "o/w/s", snapshot); err != nil {
		t.Fatal(err)
	}
	if err := store.PutDoc(ctx, source, artifactBlobCollection, "o/w/a", artifact); err != nil {
		t.Fatal(err)
	}
	if err := store.PutDoc(ctx, source, materializationBlobCollection, "o/w/m", materialization); err != nil {
		t.Fatal(err)
	}

	state, err := ExportOperationalState(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	target := store.NewMemory()
	if err := RestoreOperationalState(ctx, target, state); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.GetDoc[artifactBlob](ctx, target, artifactBlobCollection, "o/w/a"); err != nil || !found {
		t.Fatalf("restored artifact found=%v err=%v", found, err)
	}
	if err := RestoreOperationalState(ctx, target, state); err == nil {
		t.Fatal("second restore unexpectedly overwrote operational content")
	}
}

func TestRestoreOperationalStateRejectsMalformedContent(t *testing.T) {
	t.Parallel()
	err := RestoreOperationalState(context.Background(), store.NewMemory(), OperationalState{
		SnapshotBlobs: []store.Record{{
			Key: "o/w/s", Doc: json.RawMessage(`{"rows":[{"subject":"","sealed":"c2VhbGVk"}]}`),
		}},
	})
	if err == nil {
		t.Fatal("malformed operational content restored")
	}
}
