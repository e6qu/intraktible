// SPDX-License-Identifier: AGPL-3.0-or-later

package erasure

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/e6qu/intraktible/platform/store"
)

// OperationalState is the non-event-sourced key material needed to open sealed
// fields after a restore, plus retention policies maintained by the same
// control plane. It contains live data-encryption keys and must be protected as
// carefully as a database backup. It must never be appended to the immutable
// event log: destroying a subject record's key is what makes crypto-shredding
// irreversible.
//
// The browser demo serializes this state separately from its fictional event
// history. Native durable stores retain these collections directly.
type OperationalState struct {
	Subjects          []store.Record `json:"subjects,omitempty"`
	RetentionPolicies []store.Record `json:"retention_policies,omitempty"`
}

// ExportOperationalState snapshots erasure-owned operational collections.
// The returned records include live subject keys; callers are responsible for
// encrypting and access-controlling any persisted copy.
func ExportOperationalState(ctx context.Context, st store.Store) (OperationalState, error) {
	subjects, err := st.List(ctx, collection, "")
	if err != nil {
		return OperationalState{}, fmt.Errorf("erasure: export subjects: %w", err)
	}
	policies, err := st.List(ctx, policyCollection, "")
	if err != nil {
		return OperationalState{}, fmt.Errorf("erasure: export retention policies: %w", err)
	}
	return OperationalState{Subjects: subjects, RetentionPolicies: policies}, nil
}

// RestoreOperationalState restores a snapshot into an empty operational store.
// It refuses to overwrite any existing record: merging key histories can
// resurrect erased subjects, so callers must select one authoritative snapshot
// before invoking it.
func RestoreOperationalState(
	ctx context.Context,
	st store.Store,
	state OperationalState,
) error {
	if err := restoreRecords(ctx, st, collection, state.Subjects, validateSubjectRecord); err != nil {
		return err
	}
	return restoreRecords(
		ctx, st, policyCollection, state.RetentionPolicies, validatePolicyRecord,
	)
}

func restoreRecords(
	ctx context.Context,
	st store.Store,
	collectionName string,
	records []store.Record,
	validate func(store.Record) error,
) error {
	seen := make(map[string]struct{}, len(records))
	for _, record := range records {
		if _, duplicate := seen[record.Key]; duplicate {
			return fmt.Errorf("erasure: restore %s: duplicate key %q", collectionName, record.Key)
		}
		seen[record.Key] = struct{}{}
		if err := validate(record); err != nil {
			return fmt.Errorf("erasure: restore %s key %q: %w", collectionName, record.Key, err)
		}
		if _, exists, err := st.Get(ctx, collectionName, record.Key); err != nil {
			return fmt.Errorf("erasure: restore %s key %q: check existing: %w", collectionName, record.Key, err)
		} else if exists {
			return fmt.Errorf("erasure: restore %s key %q: operational record already exists", collectionName, record.Key)
		}
		if err := st.Put(ctx, collectionName, record.Key, record.Doc); err != nil {
			return fmt.Errorf("erasure: restore %s key %q: %w", collectionName, record.Key, err)
		}
	}
	return nil
}

func validateSubjectRecord(record store.Record) error {
	var value subject
	if err := decodeExact(record.Doc, &value); err != nil {
		return err
	}
	if value.Subject == "" {
		return errors.New("empty subject")
	}
	if value.Erased == nil && len(value.Key) != 32 {
		return fmt.Errorf("live subject key is %d bytes, want 32", len(value.Key))
	}
	if value.Erased != nil && len(value.Key) != 0 {
		return errors.New("erased subject retains key material")
	}
	return nil
}

func validatePolicyRecord(record store.Record) error {
	var value RetentionPolicy
	if err := decodeExact(record.Doc, &value); err != nil {
		return err
	}
	if value.Org == "" || value.Workspace == "" {
		return errors.New("policy tenant is empty")
	}
	if value.RetentionDays < 0 {
		return errors.New("retention days is negative")
	}
	return nil
}

func decodeExact(doc json.RawMessage, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(doc))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return fmt.Errorf("decode trailing data: %w", err)
	}
	return nil
}
