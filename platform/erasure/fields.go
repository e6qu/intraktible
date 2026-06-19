// SPDX-License-Identifier: AGPL-3.0-or-later

package erasure

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/e6qu/intraktible/platform/identity"
)

const erasedEnvelopeVersion = "intraktible.erased.v1"

// erasedField is the on-the-wire form of a sealed PII field: a self-describing
// envelope so OpenFields can find and open it without the field list.
type erasedField struct {
	Version string `json:"$intraktible_erased"`
	Value   string `json:"value"`
}

// SealFields seals the named fields of a JSON document under subject's key,
// replacing each value with a sealed envelope. It recurses into nested objects
// and arrays and matches field names case-insensitively, mirroring privacy.Mask
// exactly — otherwise nested or differently-cased PII would be masked at the read
// boundary yet left in the clear in the log, surviving erasure. Non-object docs
// and absent fields pass through unchanged. Sealing for an already-erased subject
// fails with ErrErased — an erased subject's PII must not be re-recorded.
func (v *Vault) SealFields(ctx context.Context, id identity.Identity, subject string, doc json.RawMessage, fields map[string]bool) (json.RawMessage, error) {
	if len(doc) == 0 || len(fields) == 0 {
		return doc, nil
	}
	root, ok := decodeJSON(doc)
	if !ok {
		return doc, nil // not JSON — nothing to seal
	}
	sealed, changed, err := v.sealValue(ctx, id, subject, root, lowerKeys(fields))
	if err != nil {
		return nil, err
	}
	if !changed {
		return doc, nil
	}
	return json.Marshal(sealed)
}

func (v *Vault) sealValue(ctx context.Context, id identity.Identity, subject string, val any, fields map[string]bool) (any, bool, error) {
	switch t := val.(type) {
	case map[string]any:
		if isErasedMap(t) {
			return t, false, nil // already an envelope — leave it intact
		}
		changed := false
		for k, child := range t {
			if fields[strings.ToLower(k)] {
				sealed, err := v.sealLeaf(ctx, id, subject, child)
				if err != nil {
					return nil, false, err
				}
				t[k] = sealed
				changed = true
				continue
			}
			nv, ch, err := v.sealValue(ctx, id, subject, child, fields)
			if err != nil {
				return nil, false, err
			}
			t[k] = nv
			changed = changed || ch
		}
		return t, changed, nil
	case []any:
		changed := false
		for i := range t {
			nv, ch, err := v.sealValue(ctx, id, subject, t[i], fields)
			if err != nil {
				return nil, false, err
			}
			t[i] = nv
			changed = changed || ch
		}
		return t, changed, nil
	default:
		return val, false, nil
	}
}

// sealLeaf seals one sensitive value into an envelope, leaving an already-sealed
// value untouched (so resealing a record is idempotent).
func (v *Vault) sealLeaf(ctx context.Context, id identity.Identity, subject string, val any) (any, error) {
	if m, ok := val.(map[string]any); ok && isErasedMap(m) {
		return val, nil
	}
	raw, err := json.Marshal(val)
	if err != nil {
		return nil, fmt.Errorf("erasure: marshal field for sealing: %w", err)
	}
	sealed, err := v.Seal(ctx, id, subject, raw)
	if err != nil {
		return nil, err
	}
	return erasedField{Version: erasedEnvelopeVersion, Value: base64.StdEncoding.EncodeToString(sealed)}, nil
}

// OpenFields reverses SealFields: it opens every sealed envelope anywhere in the
// document, recursing into nested objects and arrays. A field whose subject has
// been erased is replaced with "[erased]" rather than failing, so the rest of the
// record still reads.
func (v *Vault) OpenFields(ctx context.Context, id identity.Identity, subject string, doc json.RawMessage) (json.RawMessage, error) {
	if len(doc) == 0 {
		return doc, nil
	}
	root, ok := decodeJSON(doc)
	if !ok {
		return doc, nil
	}
	opened, changed, err := v.openValue(ctx, id, subject, root)
	if err != nil {
		return nil, err
	}
	if !changed {
		return doc, nil
	}
	return json.Marshal(opened)
}

func (v *Vault) openValue(ctx context.Context, id identity.Identity, subject string, val any) (any, bool, error) {
	switch t := val.(type) {
	case map[string]any:
		if isErasedMap(t) {
			return v.openEnvelope(ctx, id, subject, t)
		}
		changed := false
		for k, child := range t {
			nv, ch, err := v.openValue(ctx, id, subject, child)
			if err != nil {
				return nil, false, err
			}
			t[k] = nv
			changed = changed || ch
		}
		return t, changed, nil
	case []any:
		changed := false
		for i := range t {
			nv, ch, err := v.openValue(ctx, id, subject, t[i])
			if err != nil {
				return nil, false, err
			}
			t[i] = nv
			changed = changed || ch
		}
		return t, changed, nil
	default:
		return val, false, nil
	}
}

func (v *Vault) openEnvelope(ctx context.Context, id identity.Identity, subject string, env map[string]any) (any, bool, error) {
	value, _ := env["value"].(string)
	sealed, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil, false, fmt.Errorf("erasure: decode sealed field: %w", err)
	}
	plain, err := v.Open(ctx, id, subject, sealed)
	if errors.Is(err, ErrErased) {
		return "[erased]", true, nil
	}
	if err != nil {
		return nil, false, err
	}
	var pv any
	if err := json.Unmarshal(plain, &pv); err != nil {
		return nil, false, fmt.Errorf("erasure: decode opened field: %w", err)
	}
	return pv, true, nil
}

// decodeJSON parses doc into a Go value, reporting ok=false for non-JSON input
// so callers pass it through unchanged without surfacing a spurious error.
func decodeJSON(doc json.RawMessage) (any, bool) {
	var root any
	if err := json.Unmarshal(doc, &root); err != nil {
		return nil, false
	}
	return root, true
}

func isErasedMap(m map[string]any) bool {
	ver, ok := m["$intraktible_erased"].(string)
	return ok && ver == erasedEnvelopeVersion
}

func lowerKeys(fields map[string]bool) map[string]bool {
	out := make(map[string]bool, len(fields))
	for k, ok := range fields {
		if ok {
			out[strings.ToLower(strings.TrimSpace(k))] = true
		}
	}
	return out
}
