// SPDX-License-Identifier: AGPL-3.0-or-later

package telemetry_test

import (
	"context"
	"testing"

	"github.com/e6qu/intraktible/platform/telemetry"
)

// With no exporter configured, Init is a no-op: it installs nothing and returns a
// usable shutdown. Tracer still works (a no-op tracer), never nil.
func TestInitDisabled(t *testing.T) {
	t.Setenv("INTRAKTIBLE_OTEL_EXPORTER", "")
	shutdown, err := telemetry.Init(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	if shutdown == nil {
		t.Fatal("shutdown must be non-nil even when disabled")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	tr := telemetry.Tracer()
	if tr == nil {
		t.Fatal("Tracer must never be nil")
	}
	_, span := tr.Start(context.Background(), "noop")
	span.End() // must not panic
}

// stdout is a valid exporter and installs a real provider whose shutdown flushes.
func TestInitStdout(t *testing.T) {
	t.Setenv("INTRAKTIBLE_OTEL_EXPORTER", "stdout")
	shutdown, err := telemetry.Init(context.Background(), "v1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = shutdown(context.Background()) })
}

// An unknown exporter is a loud configuration error (don't silently disable).
func TestInitUnknownExporter(t *testing.T) {
	t.Setenv("INTRAKTIBLE_OTEL_EXPORTER", "carrier-pigeon")
	shutdown, err := telemetry.Init(context.Background(), "v1")
	if err == nil {
		t.Fatal("expected an error for an unknown exporter")
	}
	if shutdown == nil {
		t.Fatal("shutdown must be non-nil even on error")
	}
}

func TestInitBadSampleRatio(t *testing.T) {
	t.Setenv("INTRAKTIBLE_OTEL_EXPORTER", "stdout")
	t.Setenv("INTRAKTIBLE_OTEL_SAMPLE_RATIO", "7")
	if _, err := telemetry.Init(context.Background(), "v1"); err == nil {
		t.Fatal("expected an error for an out-of-range sample ratio")
	}
}
