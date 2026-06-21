// SPDX-License-Identifier: AGPL-3.0-or-later

package httpx_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/e6qu/intraktible/platform/httpx"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// Tracing opens a server span named by the MATCHED route template (not the raw
// path), records the status, and continues a propagated trace context.
func TestTracingNamesByRoute(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/flows/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	// RequestID feeds the request-id attribute; Tracing wraps the (route-aware) mux.
	h := httpx.Chain(mux, httpx.RequestID, httpx.Tracing)

	req := httptest.NewRequest(http.MethodGet, "/v1/flows/abc123", http.NoBody)
	h.ServeHTTP(httptest.NewRecorder(), req)

	spans := rec.Ended()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	span := spans[0]
	if span.Name() != "GET /v1/flows/{id}" {
		t.Fatalf("span name = %q, want the route template (not the raw path)", span.Name())
	}
	if span.SpanKind() != trace.SpanKindServer {
		t.Fatalf("span kind = %v, want server", span.SpanKind())
	}
	var sawRoute, sawStatus bool
	for _, a := range span.Attributes() {
		switch string(a.Key) {
		case "http.route":
			sawRoute = a.Value.AsString() == "/v1/flows/{id}"
		case "http.response.status_code":
			sawStatus = a.Value.AsInt64() == int64(http.StatusTeapot)
		}
	}
	if !sawRoute || !sawStatus {
		t.Fatalf("missing route/status attributes: %+v", span.Attributes())
	}
}

// A 5xx response marks the span's status as Error so failing requests stand out.
func TestTracingMarksServerError(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	h := httpx.Chain(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}), httpx.RequestID, httpx.Tracing)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/boom", http.NoBody))

	spans := rec.Ended()
	if len(spans) != 1 || spans[0].Status().Code.String() != "Error" {
		t.Fatalf("expected an Error-status span, got %+v", spans)
	}
}
