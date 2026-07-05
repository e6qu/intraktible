// SPDX-License-Identifier: AGPL-3.0-or-later
// The tracing SDK + exporters — native-only: the otel SDK and its exporter
// closure (protobuf, cloud resource detection) would add megabytes to a wasm
// build that has no OTLP endpoint to export to anyway. Instrumentation
// (Tracer) is the light otel API and stays in every build; without Init the
// global tracer is the API's no-op.

//go:build !js

package telemetry

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// Init configures the global TracerProvider from the environment and returns a
// shutdown func that flushes pending spans (always non-nil, safe to defer even on
// error). INTRAKTIBLE_OTEL_EXPORTER selects the exporter: "" (off — no-op tracer),
// "stdout", or "otlp". INTRAKTIBLE_OTEL_SAMPLE_RATIO (0..1, default 1) sets the
// head sampler. A misconfiguration (bad ratio, exporter build failure) is returned
// loudly so the operator fixes it rather than silently losing traces.
func Init(ctx context.Context, version string) (func(context.Context) error, error) {
	noop := func(context.Context) error { return nil }
	kind := os.Getenv("INTRAKTIBLE_OTEL_EXPORTER")
	if kind == "" {
		return noop, nil // tracing disabled — leave the global no-op provider in place
	}

	exp, err := newExporter(ctx, kind)
	if err != nil {
		return noop, err
	}
	sampler, err := newSampler()
	if err != nil {
		return noop, err
	}
	// Merge our service attributes (schemaless, so the SDK's own resource schema
	// wins) onto the SDK defaults (telemetry.sdk.*) — a versioned NewWithAttributes
	// here would conflict with the SDK's bundled semconv schema URL.
	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(
		semconv.ServiceName("intraktible"),
		semconv.ServiceVersion(version),
	))
	if err != nil {
		return noop, fmt.Errorf("telemetry: build resource: %w", err)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sampler)),
	)
	otel.SetTracerProvider(tp)
	// Honor W3C trace-context + baggage on inbound requests and propagate them on
	// outbound calls, so a trace started by an upstream service stitches together.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))
	return tp.Shutdown, nil
}

// newExporter builds the configured span exporter.
func newExporter(ctx context.Context, kind string) (sdktrace.SpanExporter, error) {
	switch kind {
	case "stdout":
		return stdouttrace.New()
	case "otlp":
		// Endpoint, protocol, headers, and TLS come from the standard
		// OTEL_EXPORTER_OTLP_* env vars the exporter reads on its own.
		return otlptracehttp.New(ctx)
	default:
		return nil, fmt.Errorf("telemetry: unknown INTRAKTIBLE_OTEL_EXPORTER %q (want stdout|otlp)", kind)
	}
}

// newSampler reads INTRAKTIBLE_OTEL_SAMPLE_RATIO (default 1 = sample everything).
func newSampler() (sdktrace.Sampler, error) {
	v := os.Getenv("INTRAKTIBLE_OTEL_SAMPLE_RATIO")
	if v == "" {
		return sdktrace.AlwaysSample(), nil
	}
	ratio, err := strconv.ParseFloat(v, 64)
	if err != nil || ratio < 0 || ratio > 1 {
		return nil, fmt.Errorf("telemetry: INTRAKTIBLE_OTEL_SAMPLE_RATIO %q: want a number in [0,1]", v)
	}
	if ratio >= 1 {
		return sdktrace.AlwaysSample(), nil
	}
	return sdktrace.TraceIDRatioBased(ratio), nil
}

// Tracer returns the binary's tracer. It resolves the global provider on each
// call, so it works whether or not Init installed an SDK provider (no-op
// otherwise) and never returns nil.
