// SPDX-License-Identifier: AGPL-3.0-or-later

package cases_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/intraktible/case-manager/cases"
)

func TestCaseViewOmitsUnrecordedLifecycleTimestamps(t *testing.T) {
	active, err := json.Marshal(cases.CaseView{CaseID: "active"})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"deadline", "first_action_at", "resolved_at"} {
		if strings.Contains(string(active), `"`+field+`"`) {
			t.Fatalf("active case invents %s evidence: %s", field, active)
		}
	}

	resolvedAt := time.Date(2026, time.July, 30, 12, 0, 0, 0, time.UTC)
	resolved, err := json.Marshal(cases.CaseView{CaseID: "resolved", ResolvedAt: resolvedAt})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(resolved), `"resolved_at":"2026-07-30T12:00:00Z"`) {
		t.Fatalf("recorded resolution timestamp missing: %s", resolved)
	}
}
