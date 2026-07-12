// SPDX-License-Identifier: AGPL-3.0-or-later

package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

var fastRetry = retryPolicy{maxAttempts: 3, baseBackoff: time.Millisecond, maxBackoff: time.Millisecond}
var noRetry = retryPolicy{maxAttempts: 1, baseBackoff: time.Millisecond, maxBackoff: time.Millisecond}

func fixedClock(t *time.Time) func() time.Time { return func() time.Time { return *t } }

func TestIsTransient(t *testing.T) {
	if !isTransient(transient(errors.New("x"))) {
		t.Fatal("transient() must be classified transient")
	}
	if isTransient(errors.New("plain")) {
		t.Fatal("a plain error is permanent")
	}
	// A transient wrapped with %w further up still classifies (errors.As unwraps).
	wrapped := fmt.Errorf("outer: %w", transient(errors.New("inner")))
	if !isTransient(wrapped) {
		t.Fatal("a %w-wrapped transient must still classify as transient")
	}
}

func TestResilientFetchRetriesTransientThenSucceeds(t *testing.T) {
	reg := newBreakerRegistry()
	calls := 0
	resp, err := resilientFetch(context.Background(), reg, fastRetry, "k", func() (json.RawMessage, error) {
		calls++
		if calls < 3 {
			return nil, transient(errors.New("blip"))
		}
		return json.RawMessage(`{"ok":true}`), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(resp) != `{"ok":true}` || calls != 3 {
		t.Fatalf("resp=%s calls=%d", resp, calls)
	}
}

func TestResilientFetchDoesNotRetryPermanent(t *testing.T) {
	reg := newBreakerRegistry()
	calls := 0
	_, err := resilientFetch(context.Background(), reg, fastRetry, "k", func() (json.RawMessage, error) {
		calls++
		return nil, errors.New("400 bad request")
	})
	if err == nil || calls != 1 {
		t.Fatalf("permanent error should not retry: err=%v calls=%d", err, calls)
	}
}

func TestResilientFetchExhaustsRetries(t *testing.T) {
	reg := newBreakerRegistry()
	calls := 0
	_, err := resilientFetch(context.Background(), reg, fastRetry, "k", func() (json.RawMessage, error) {
		calls++
		return nil, transient(errors.New("down"))
	})
	if err == nil || calls != fastRetry.maxAttempts {
		t.Fatalf("expected exhaustion after %d attempts, got err=%v calls=%d", fastRetry.maxAttempts, err, calls)
	}
}

func TestCircuitOpensAndFailsFast(t *testing.T) {
	now := time.Unix(1000, 0)
	reg := newBreakerRegistry()
	reg.threshold = 2
	reg.now = fixedClock(&now)

	fail := func() (json.RawMessage, error) { return nil, transient(errors.New("down")) }
	for i := 0; i < 2; i++ {
		if _, err := resilientFetch(context.Background(), reg, noRetry, "k", fail); err == nil {
			t.Fatal("expected failure")
		}
	}
	// The circuit is now open: the next call fails fast without invoking fetch.
	calls := 0
	_, err := resilientFetch(context.Background(), reg, noRetry, "k", func() (json.RawMessage, error) {
		calls++
		return json.RawMessage(`{}`), nil
	})
	if !errors.Is(err, ErrCircuitOpen) || calls != 0 {
		t.Fatalf("expected fast open-circuit failure: err=%v calls=%d", err, calls)
	}
}

func TestCircuitHalfOpenRecovers(t *testing.T) {
	now := time.Unix(1000, 0)
	reg := newBreakerRegistry()
	reg.threshold = 1
	reg.cooldown = 10 * time.Second
	reg.now = fixedClock(&now)

	fail := func() (json.RawMessage, error) { return nil, transient(errors.New("down")) }
	if _, err := resilientFetch(context.Background(), reg, noRetry, "k", fail); err == nil {
		t.Fatal("expected failure to open the circuit")
	}
	// Before the cooldown elapses, calls fail fast.
	if _, err := resilientFetch(context.Background(), reg, noRetry, "k", fail); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("expected open circuit, got %v", err)
	}
	// After the cooldown, a probe is allowed; a success closes the circuit.
	now = now.Add(11 * time.Second)
	ok := func() (json.RawMessage, error) { return json.RawMessage(`{"ok":1}`), nil }
	if _, err := resilientFetch(context.Background(), reg, noRetry, "k", ok); err != nil {
		t.Fatalf("half-open probe should be allowed: %v", err)
	}
	// Closed again: normal calls proceed.
	calls := 0
	if _, err := resilientFetch(context.Background(), reg, noRetry, "k", func() (json.RawMessage, error) {
		calls++
		return json.RawMessage(`{}`), nil
	}); err != nil || calls != 1 {
		t.Fatalf("circuit should be closed: err=%v calls=%d", err, calls)
	}
}

func TestCircuitClosesOnSuccess(t *testing.T) {
	now := time.Unix(1000, 0)
	reg := newBreakerRegistry()
	reg.threshold = 3
	reg.now = fixedClock(&now)
	fail := func() (json.RawMessage, error) { return nil, transient(errors.New("down")) }
	ok := func() (json.RawMessage, error) { return json.RawMessage(`{}`), nil }

	resilientFetch(context.Background(), reg, noRetry, "k", fail) // 1 failure
	resilientFetch(context.Background(), reg, noRetry, "k", ok)   // success resets the counter
	// Two more failures must not open (counter reset), threshold is 3.
	resilientFetch(context.Background(), reg, noRetry, "k", fail)
	if _, err := resilientFetch(context.Background(), reg, noRetry, "k", fail); errors.Is(err, ErrCircuitOpen) {
		t.Fatal("success should have reset the failure counter; circuit opened too early")
	}
}

func TestPerConnectorIsolation(t *testing.T) {
	now := time.Unix(1000, 0)
	reg := newBreakerRegistry()
	reg.threshold = 1
	reg.now = fixedClock(&now)
	fail := func() (json.RawMessage, error) { return nil, transient(errors.New("down")) }
	resilientFetch(context.Background(), reg, noRetry, "a", fail) // opens "a"

	// A different connector "b" is unaffected.
	calls := 0
	if _, err := resilientFetch(context.Background(), reg, noRetry, "b", func() (json.RawMessage, error) {
		calls++
		return json.RawMessage(`{}`), nil
	}); err != nil || calls != 1 {
		t.Fatalf("connector b should be unaffected by a's open circuit: err=%v calls=%d", err, calls)
	}
}

func TestContextCancelStopsRetries(t *testing.T) {
	reg := newBreakerRegistry()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	_, err := resilientFetch(ctx, reg, fastRetry, "k", func() (json.RawMessage, error) {
		calls++
		return nil, transient(errors.New("down"))
	})
	if err == nil || calls != 0 {
		t.Fatalf("a cancelled context should not attempt: err=%v calls=%d", err, calls)
	}
}
