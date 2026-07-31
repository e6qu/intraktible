// SPDX-License-Identifier: AGPL-3.0-or-later

package governance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// suggestionDifferences returns a deterministic, value-free structural diff.
// Values remain subject-key encrypted; durable learning evidence records only
// which JSON fields the accountable reviewer added, removed, or changed.
func suggestionDifferences(
	suggestion json.RawMessage,
	final json.RawMessage,
) ([]SuggestionDifference, error) {
	before, err := decodeDifferenceObject(suggestion)
	if err != nil {
		return nil, fmt.Errorf("agent governance: decode suggestion for diff: %w", err)
	}
	after, err := decodeDifferenceObject(final)
	if err != nil {
		return nil, fmt.Errorf("agent governance: decode reviewer final for diff: %w", err)
	}
	var differences []SuggestionDifference
	diffObjects("", before, after, &differences)
	sort.Slice(differences, func(i, j int) bool {
		if differences[i].Path != differences[j].Path {
			return differences[i].Path < differences[j].Path
		}
		return differences[i].Kind < differences[j].Kind
	})
	return differences, nil
}

func decodeDifferenceObject(raw json.RawMessage) (map[string]any, error) {
	var target map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&target); err != nil {
		return nil, err
	}
	return target, nil
}

func diffObjects(
	path string,
	before map[string]any,
	after map[string]any,
	differences *[]SuggestionDifference,
) {
	keys := make(map[string]bool, len(before)+len(after))
	for key := range before {
		keys[key] = true
	}
	for key := range after {
		keys[key] = true
	}
	ordered := make([]string, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)
	for _, key := range ordered {
		beforeValue, hadBefore := before[key]
		afterValue, hasAfter := after[key]
		pointer := path + "/" + escapeJSONPointer(key)
		switch {
		case !hadBefore:
			*differences = append(*differences, SuggestionDifference{
				Path: pointer, Kind: SuggestionAdded,
			})
		case !hasAfter:
			*differences = append(*differences, SuggestionDifference{
				Path: pointer, Kind: SuggestionRemoved,
			})
		default:
			beforeObject, beforeIsObject := beforeValue.(map[string]any)
			afterObject, afterIsObject := afterValue.(map[string]any)
			if beforeIsObject && afterIsObject {
				diffObjects(pointer, beforeObject, afterObject, differences)
			} else if !reflect.DeepEqual(beforeValue, afterValue) {
				*differences = append(*differences, SuggestionDifference{
					Path: pointer, Kind: SuggestionChanged,
				})
			}
		}
	}
}

func escapeJSONPointer(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "~", "~0"), "/", "~1")
}
