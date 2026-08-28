// SPDX-License-Identifier: AGPL-3.0-or-later

package monitoring

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testToken = "intraktible-monitoring-token-00000000000000000000"

func TestTokenDigestFromEnvironmentValidatesAndDropsPlaintext(t *testing.T) {
	t.Setenv("INTRAKTIBLE_MONITORING_TOKEN", "short")
	if _, err := TokenDigestFromEnvironment(); err == nil {
		t.Fatal("weak monitoring token accepted")
	}
	t.Setenv("INTRAKTIBLE_MONITORING_TOKEN", testToken)
	digest, err := TokenDigestFromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if digest == nil || strings.Contains(string(digest[:]), testToken) {
		t.Fatal("validated monitoring token was not reduced to a digest")
	}
}

func TestHandlerRequiresExactBearerAndPublishesProjectionEvidence(t *testing.T) {
	t.Setenv("INTRAKTIBLE_MONITORING_TOKEN", testToken)
	digest, err := TokenDigestFromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	handler := Handler(digest, func() State {
		return State{Health: "healthy", ProjectionApplied: 8, EventHead: 13}
	})

	for _, authorization := range []string{"", "bearer " + testToken, "Bearer wrong-monitoring-token-00000000000000000000"} {
		request := httptest.NewRequest(http.MethodGet, "/monitoring/observation", http.NoBody)
		request.Header.Set("Authorization", authorization)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("authorization %q status = %d, want 401", authorization, response.Code)
		}
		if response.Header().Get("WWW-Authenticate") != `Bearer realm="intraktible-monitoring"` || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("unauthorized headers = %#v", response.Header())
		}
	}

	request := httptest.NewRequest(http.MethodGet, "/monitoring/observation", http.NoBody)
	request.Header.Set("Authorization", "Bearer "+testToken)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/json" || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("response status=%d headers=%#v body=%s", response.Code, response.Header(), response.Body.String())
	}
	var document map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if document["schema_version"] != SchemaVersion {
		t.Fatalf("schema_version = %v", document["schema_version"])
	}
	if _, present := document["cost_estimate"]; present {
		t.Fatal("application observation fabricated a cost estimate")
	}
	resource := document["resources"].([]any)[0].(map[string]any)
	if resource["health"] != "unhealthy" || resource["kind"] != "application" {
		t.Fatalf("resource = %#v", resource)
	}
	metrics := resource["metrics"].([]any)
	if len(metrics) != 6 || metrics[2].(map[string]any)["value"] != float64(5) {
		t.Fatalf("metrics = %#v", metrics)
	}
}
