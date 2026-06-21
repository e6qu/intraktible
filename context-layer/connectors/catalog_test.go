// SPDX-License-Identifier: AGPL-3.0-or-later

package connectors_test

import (
	"encoding/json"
	"testing"

	"github.com/e6qu/intraktible/context-layer/connectors"
	"github.com/e6qu/intraktible/context-layer/domain"
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
		if !domain.ValidConnectorType(string(tpl.Type)) {
			t.Fatalf("template %q has unsupported type %q", tpl.ID, tpl.Type)
		}
		var cfg map[string]any
		if err := json.Unmarshal(tpl.Config, &cfg); err != nil {
			t.Fatalf("template %q config is not a JSON object: %v", tpl.ID, err)
		}
		need := func(field string) {
			if _, ok := cfg[field]; !ok {
				t.Fatalf("%s template %q is missing %q", tpl.Type, tpl.ID, field)
			}
		}
		switch tpl.Type {
		case "http":
			need("url")
		case "graphql":
			need("url")
			need("query")
		case "sql":
			need("dsn")
			need("query")
		case "static":
			need("data")
		}
	}
}
