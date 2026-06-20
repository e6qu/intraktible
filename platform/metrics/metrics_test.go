// SPDX-License-Identifier: AGPL-3.0-or-later

package metrics_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/intraktible/platform/metrics"
)

func TestHandlerExposesRecordedMetrics(t *testing.T) {
	metrics.RecordHTTP("/v1/flows/{slug}/{env}/decide", http.MethodPost, 200, 12*time.Millisecond)
	metrics.RecordHTTP("", http.MethodGet, 404, time.Millisecond) // unmatched route
	metrics.SetProjectionApplied(42)
	metrics.IncProjectionErrors()
	metrics.RecordSchedulerTick("model_drift", "ok")

	rec := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", http.NoBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`intraktible_http_requests_total{method="POST",route="/v1/flows/{slug}/{env}/decide",status="2xx"}`,
		`route="unmatched"`,                         // empty route bucketed
		"intraktible_http_request_duration_seconds", // histogram registered
		"intraktible_projection_applied_seq 42",     // gauge value
		"intraktible_projection_errors_total",       // counter present
		`intraktible_scheduler_ticks_total{outcome="ok",scheduler="model_drift"}`,
		"go_goroutines", // the Go runtime collector is registered
	} {
		if !strings.Contains(body, want) {
			t.Errorf("exposition missing %q", want)
		}
	}
}
