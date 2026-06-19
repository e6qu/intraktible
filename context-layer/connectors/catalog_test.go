// SPDX-License-Identifier: AGPL-3.0-or-later

package connectors_test

import (
	"encoding/json"
	"testing"

	"github.com/e6qu/intraktible/context-layer/connectors"
)

func TestCatalogIsWellFormed(t *testing.T) {
	cat := connectors.Catalog()
	if len(cat) < 10 {
		t.Fatalf("expected a broad catalog, got %d", len(cat))
	}
	seen := map[string]bool{}
	for _, tpl := range cat {
		if tpl.ID == "" || tpl.Name == "" || tpl.Category == "" || tpl.Description == "" {
			t.Fatalf("template %q has empty metadata: %+v", tpl.ID, tpl)
		}
		if seen[tpl.ID] {
			t.Fatalf("duplicate template id %q", tpl.ID)
		}
		seen[tpl.ID] = true
		if tpl.Type != "http" && tpl.Type != "sql" {
			t.Fatalf("template %q has unsupported type %q", tpl.ID, tpl.Type)
		}
		var cfg map[string]any
		if err := json.Unmarshal(tpl.Config, &cfg); err != nil {
			t.Fatalf("template %q config is not a JSON object: %v", tpl.ID, err)
		}
		switch tpl.Type {
		case "http":
			if _, ok := cfg["url"]; !ok {
				t.Fatalf("http template %q is missing a url", tpl.ID)
			}
		case "sql":
			if _, ok := cfg["dsn"]; !ok {
				t.Fatalf("sql template %q is missing a dsn", tpl.ID)
			}
			if _, ok := cfg["query"]; !ok {
				t.Fatalf("sql template %q is missing a query", tpl.ID)
			}
		}
	}
}
