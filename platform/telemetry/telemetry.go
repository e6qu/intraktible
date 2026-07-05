// SPDX-License-Identifier: AGPL-3.0-or-later

// Package telemetry wires distributed tracing (OpenTelemetry). It owns the
// process's TracerProvider and exposes a single named Tracer the imperative
// shell uses to span requests and the decide path. Tracing is an effect: only
// the shell (HTTP middleware, the decide handler) opens spans — the deterministic
// core never imports this package (the per-node observer is a pure domain
// interface the shell adapts).
//
// Off by default: with INTRAKTIBLE_OTEL_EXPORTER unset, Init installs nothing and
// the global Tracer is OpenTelemetry's no-op, so spans cost effectively nothing.
// Set it to "stdout" (spans to the process log, for local inspection) or "otlp"
// (export over OTLP/HTTP to a collector — endpoint/headers come from the standard
// OTEL_EXPORTER_OTLP_* environment variables the exporter reads natively).
package telemetry

import (
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
)

// scope names the instrumentation library for spans this binary emits.
const scope = "github.com/e6qu/intraktible"

func Tracer() trace.Tracer { return otel.Tracer(scope) }
