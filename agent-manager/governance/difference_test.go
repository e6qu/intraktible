// SPDX-License-Identifier: AGPL-3.0-or-later

package governance

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

func TestSuggestionDifferencesAreDeterministicAndValueFree(t *testing.T) {
	differences, err := suggestionDifferences(
		json.RawMessage(`{"summary":"draft","nested":{"score":4,"keep":true},"remove":"secret"}`),
		json.RawMessage(`{"summary":"reviewed","nested":{"score":5,"keep":true},"add":"private"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []SuggestionDifference{
		{Path: "/add", Kind: SuggestionAdded},
		{Path: "/nested/score", Kind: SuggestionChanged},
		{Path: "/remove", Kind: SuggestionRemoved},
		{Path: "/summary", Kind: SuggestionChanged},
	}
	if !reflect.DeepEqual(differences, want) {
		t.Fatalf("differences = %+v, want %+v", differences, want)
	}
	encoded, err := json.Marshal(differences)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"draft", "reviewed", "secret", "private"} {
		if bytes.Contains(encoded, []byte(forbidden)) {
			t.Fatalf("diff leaked value %q: %s", forbidden, encoded)
		}
	}
}
