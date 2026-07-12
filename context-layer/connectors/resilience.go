// SPDX-License-Identifier: AGPL-3.0-or-later

package connectors

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"encoding/json"
)

// Transient marks a connector error worth retrying — a timeout, a connection
// failure, or an upstream 5xx/429 — as opposed to a permanent one (a 4xx, malformed
// config, or a bad response body) that a retry cannot fix. A connector wraps its
// retryable failures in it; the resilience layer retries only those and trips the
// circuit breaker only on those (a bad request is our fault, not the dependency's).
type Transient struct{ err error }

func (e Transient) Error() string { return e.err.Error() }
func (e Transient) Unwrap() error { return e.err }

// transient wraps err as retryable.
func transient(err error) error { return Transient{err: err} }

// isTransient reports whether err (or a cause it wraps) is retryable.
func isTransient(err error) bool {
	var t Transient
	return errors.As(err, &t)
}

// ErrCircuitOpen is returned (fast, without an I/O attempt) when a connector's
// circuit breaker is open because it has been failing — so a down dependency does
// not make every decision hang through the full timeout + retry budget.
var ErrCircuitOpen = errors.New("context-layer: connector circuit open — failing fast")

// retryPolicy bounds the retry budget for one connector call.
type retryPolicy struct {
	maxAttempts int           // total attempts including the first (1 disables retries)
	baseBackoff time.Duration // backoff before the 2nd attempt; doubles each retry
	maxBackoff  time.Duration // cap on the per-retry backoff
}

var defaultRetry = retryPolicy{maxAttempts: 3, baseBackoff: 100 * time.Millisecond, maxBackoff: 2 * time.Second}

// backoff returns the delay before the attempt+1'th try (exponential, capped).
func (p retryPolicy) backoff(attempt int) time.Duration {
	d := p.baseBackoff << (attempt - 1)
	if d <= 0 || d > p.maxBackoff {
		return p.maxBackoff
	}
	return d
}

type breakerState int

const (
	breakerClosed breakerState = iota
	breakerOpen
	breakerHalfOpen
)

// breaker is one connector's circuit state.
type breaker struct {
	mu                  sync.Mutex
	state               breakerState
	consecutiveFailures int
	openedAt            time.Time
}

// allow reports whether a call may proceed, transitioning open→half-open once the
// cooldown has elapsed (letting a single probe through).
func (b *breaker) allow(now time.Time, cooldown time.Duration) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state == breakerOpen && now.Sub(b.openedAt) >= cooldown {
		b.state = breakerHalfOpen
	}
	return b.state != breakerOpen
}

// record folds a call's outcome into the breaker: a success closes it; a transient
// failure trips it once it reaches the threshold (or immediately from half-open).
func (b *breaker) record(success bool, now time.Time, threshold int) (opened bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if success {
		b.consecutiveFailures = 0
		b.state = breakerClosed
		return false
	}
	wasOpen := b.state == breakerOpen
	b.consecutiveFailures++
	if b.state == breakerHalfOpen || b.consecutiveFailures >= threshold {
		b.state = breakerOpen
		b.openedAt = now
	}
	return b.state == breakerOpen && !wasOpen
}

// breakerRegistry holds one breaker per (tenant, connector) key.
type breakerRegistry struct {
	mu        sync.Mutex
	breakers  map[string]*breaker
	threshold int
	cooldown  time.Duration
	now       func() time.Time
}

func newBreakerRegistry() *breakerRegistry {
	return &breakerRegistry{
		breakers:  map[string]*breaker{},
		threshold: 5,
		cooldown:  30 * time.Second,
		now:       time.Now,
	}
}

func (r *breakerRegistry) get(key string) *breaker {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.breakers[key]
	if !ok {
		b = &breaker{}
		r.breakers[key] = b
	}
	return b
}

// connectorBreakers is the process-wide breaker state (runtime health, like metrics);
// keyed per (tenant, connector) so one failing connector never trips another's.
var connectorBreakers = newBreakerRegistry()

// resilientFetch runs one connector fetch through the circuit breaker and the retry
// budget: it fails fast when the breaker is open, retries only transient failures
// with capped exponential backoff, and folds the call's final outcome back into the
// breaker. Permanent failures return immediately and do not trip the breaker.
func resilientFetch(ctx context.Context, reg *breakerRegistry, policy retryPolicy, key string, fetch func() (json.RawMessage, error)) (json.RawMessage, error) {
	b := reg.get(key)
	if !b.allow(reg.now(), reg.cooldown) {
		return nil, fmt.Errorf("%w: %s", ErrCircuitOpen, key)
	}

	var lastErr error
	for attempt := 1; attempt <= policy.maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		resp, err := fetch()
		if err == nil {
			b.record(true, reg.now(), reg.threshold)
			return resp, nil
		}
		if !isTransient(err) {
			// A permanent error (bad request / bad response) is not the dependency
			// being unhealthy — return it without retrying or tripping the breaker.
			return nil, err
		}
		lastErr = err
		if attempt < policy.maxAttempts {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(policy.backoff(attempt)):
			}
		}
	}
	if opened := b.record(false, reg.now(), reg.threshold); opened {
		slog.Warn("context-layer: connector circuit opened after repeated failures", "connector", key, "err", lastErr)
	}
	return nil, fmt.Errorf("context-layer: connector %s failed after %d attempts: %w", key, policy.maxAttempts, lastErr)
}
