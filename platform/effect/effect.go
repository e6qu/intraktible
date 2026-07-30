// SPDX-License-Identifier: AGPL-3.0-or-later

// Package effect carries the cross-component contract for imperative work that
// may have to be recovered after a process dies between requesting it and
// recording its result.
package effect

import (
	"context"
	"fmt"
)

// Delivery describes what a recovery worker may honestly do after an effect's
// outcome is indeterminate.
type Delivery string

const (
	// ReplaySafe means repeating the operation has no externally visible side
	// effect and is deterministic for the recorded request.
	ReplaySafe Delivery = "replay_safe"
	// ProviderIdempotent means the provider commits one logical operation for a
	// stable idempotency key even when the transport call is repeated.
	ProviderIdempotent Delivery = "provider_idempotent"
	// AtLeastOnce means the provider cannot prove either property. Recovery must
	// surface an indeterminate attempt instead of silently calling it again.
	AtLeastOnce Delivery = "at_least_once"
)

// Valid reports whether d is one of the recorded wire values.
func (d Delivery) Valid() bool {
	return d == ReplaySafe || d == ProviderIdempotent || d == AtLeastOnce
}

// Request is the durable identity passed to a provider. Attempt increases when a
// recovery worker repeats a replay-safe or provider-idempotent operation; Key is
// stable across attempts and is the value an outbound transport should use.
type Request struct {
	Key     string
	Attempt int
}

type contextKey struct{}

// WithRequest attaches the durable effect identity to a provider call.
func WithRequest(ctx context.Context, request Request) (context.Context, error) {
	if request.Key == "" || request.Attempt <= 0 {
		return nil, fmt.Errorf("effect: key and positive attempt are required")
	}
	return context.WithValue(ctx, contextKey{}, request), nil
}

// FromContext returns the durable effect identity when the call belongs to a
// recorded effect. Ordinary preview/direct provider calls have none.
func FromContext(ctx context.Context) (Request, bool) {
	request, ok := ctx.Value(contextKey{}).(Request)
	return request, ok
}
