// SPDX-License-Identifier: AGPL-3.0-or-later

package cases_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/intraktible/case-manager/cases"
	"github.com/e6qu/intraktible/case-manager/domain"
)

func TestMaskPIIUsesPinnedRolePolicyWithoutMutatingSource(t *testing.T) {
	source := cases.CaseView{Context: json.RawMessage(`{"name":"Ada","score":42}`)}
	definition := domain.CaseTypeDefinition{Fields: []domain.FieldDefinition{
		{Key: "name", PII: true, ReadBy: []string{"operator"}},
		{Key: "score"},
	}}
	masked, err := cases.MaskPII(source, definition, "viewer")
	if err != nil {
		t.Fatal(err)
	}
	if string(masked.Context) != `{"name":"[masked]","score":42}` {
		t.Fatalf("masked context = %s", masked.Context)
	}
	if string(source.Context) != `{"name":"Ada","score":42}` {
		t.Fatalf("source projection was mutated: %s", source.Context)
	}
}

func TestAuditCSVNeutralizesSpreadsheetFormulas(t *testing.T) {
	data, contentType, extension, err := cases.ExportAudit([]cases.CaseView{{
		CaseID: "=cmd", CaseType: "review", Status: "completed",
		Audit: []cases.AuditEntry{{
			Type: "note", Actor: "@attacker", At: time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC),
			Detail: "+SUM(1,1)",
		}},
	}}, "csv")
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "text/csv; charset=utf-8" || extension != "csv" ||
		!strings.Contains(string(data), "'=cmd") || !strings.Contains(string(data), "'@attacker") ||
		!strings.Contains(string(data), "'+SUM(1,1)") {
		t.Fatalf("unsafe export: type=%s ext=%s body=%s", contentType, extension, data)
	}
}
