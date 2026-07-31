// SPDX-License-Identifier: AGPL-3.0-or-later

package governance

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestOptionalProjectionTimesOmitZeroValues(t *testing.T) {
	body, err := json.Marshal(struct {
		Review   ReviewView       `json:"review"`
		Release  ReleaseView      `json:"release"`
		Binding  DeploymentView   `json:"binding"`
		Assist   AssistView       `json:"assist"`
		Incident IncidentView     `json:"incident"`
		Approval ToolApprovalView `json:"approval"`
	}{
		Review: ReviewView{ExpiresAt: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{
		"reviewed_at", "expired_at", "retired_at", "activated_at", "paused_at",
		"resumed_at", "completed_at", "lease_until", "resolved_at", "decided_at",
	} {
		if strings.Contains(string(body), `"`+field+`"`) {
			t.Fatalf("zero optional time %q leaked onto the wire: %s", field, body)
		}
	}
	if !strings.Contains(string(body), `"expires_at":"2026-08-01T00:00:00Z"`) {
		t.Fatalf("required review expiry is missing: %s", body)
	}
}
