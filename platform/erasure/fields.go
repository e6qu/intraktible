// SPDX-License-Identifier: AGPL-3.0-or-later

package erasure

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/e6qu/intraktible/platform/identity"
)

const erasedEnvelopeVersion = "intraktible.erased.v1"

// erasedField is the on-the-wire form of a sealed PII field: a self-describing
// envelope so OpenFields can find and open it without the field list.
type erasedField struct {
	Version string `json:"$intraktible_erased"`
	Value   string `json:"value"`
}

// SealFields seals the named top-level fields of a JSON object under subject's
// key, replacing each value with a sealed envelope. Non-object docs and absent
// fields pass through unchanged. Sealing for an already-erased subject fails with
// ErrErased — an erased subject's PII must not be re-recorded.
func (v *Vault) SealFields(ctx context.Context, id identity.Identity, subject string, doc json.RawMessage, fields map[string]bool) (json.RawMessage, error) {
	if len(doc) == 0 || len(fields) == 0 {
		return doc, nil
	}
	m, ok := jsonObject(doc)
	if !ok {
		return doc, nil // not a JSON object — nothing to seal
	}
	changed := false
	for k, raw := range m {
		if !fields[k] || isErased(raw) {
			continue
		}
		sealed, err := v.Seal(ctx, id, subject, raw)
		if err != nil {
			return nil, err
		}
		env, err := json.Marshal(erasedField{Version: erasedEnvelopeVersion, Value: base64.StdEncoding.EncodeToString(sealed)})
		if err != nil {
			return nil, fmt.Errorf("erasure: seal field %q: %w", k, err)
		}
		m[k] = env
		changed = true
	}
	if !changed {
		return doc, nil
	}
	return json.Marshal(m)
}

// OpenFields reverses SealFields: it opens every sealed envelope in the object.
// A field whose subject has been erased is replaced with "[erased]" rather than
// failing, so the rest of the record still reads.
func (v *Vault) OpenFields(ctx context.Context, id identity.Identity, subject string, doc json.RawMessage) (json.RawMessage, error) {
	if len(doc) == 0 {
		return doc, nil
	}
	m, ok := jsonObject(doc)
	if !ok {
		return doc, nil
	}
	changed := false
	for k, raw := range m {
		var env erasedField
		if json.Unmarshal(raw, &env) != nil || env.Version != erasedEnvelopeVersion {
			continue
		}
		sealed, err := base64.StdEncoding.DecodeString(env.Value)
		if err != nil {
			return nil, fmt.Errorf("erasure: decode field %q: %w", k, err)
		}
		plain, err := v.Open(ctx, id, subject, sealed)
		if errors.Is(err, ErrErased) {
			m[k] = json.RawMessage(`"[erased]"`)
			changed = true
			continue
		}
		if err != nil {
			return nil, err
		}
		m[k] = plain
		changed = true
	}
	if !changed {
		return doc, nil
	}
	return json.Marshal(m)
}

// jsonObject decodes doc as a JSON object, reporting ok=false for anything else
// (so callers pass non-objects through unchanged, without a spurious error).
func jsonObject(doc json.RawMessage) (map[string]json.RawMessage, bool) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(doc, &m); err != nil || m == nil {
		return nil, false
	}
	return m, true
}

func isErased(raw json.RawMessage) bool {
	var env erasedField
	return json.Unmarshal(raw, &env) == nil && env.Version == erasedEnvelopeVersion
}
