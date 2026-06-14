// SPDX-License-Identifier: AGPL-3.0-or-later

// Package projection runs the read side: it folds the event log into the
// materialized store. Projections are rebuilt from offset 0 at
// boot and kept current via the in-process bus, giving replay + rebuildability.
package projection

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/store"
)

// Projector folds events into the store. Implementations must be deterministic:
// applying the same events in order always yields the same state.
type Projector interface {
	Name() string
	Apply(ctx context.Context, e eventlog.Envelope, s store.Store) error
}

// Runtime applies a set of projectors to the log.
type Runtime struct {
	log        eventlog.Log
	store      store.Store
	projectors []Projector

	mu  sync.Mutex
	err error
}

// New builds a Runtime.
func New(log eventlog.Log, st store.Store, projectors ...Projector) *Runtime {
	return &Runtime{log: log, store: st, projectors: projectors}
}

// Start rebuilds projections from offset 0, then consumes live events until ctx
// is cancelled. It returns after the initial rebuild so the server starts with
// current state. A live apply error stops the consumer and is surfaced via Err
// (fail loudly — we never silently drop an event).
func (r *Runtime) Start(ctx context.Context) error {
	sub, cancel := r.log.Subscribe()
	if err := r.rebuild(ctx); err != nil {
		cancel()
		return err
	}
	go func() {
		defer cancel()
		for {
			select {
			case <-ctx.Done():
				return
			case e, ok := <-sub:
				if !ok {
					return
				}
				if err := r.applyAll(ctx, e); err != nil {
					r.setErr(err)
					slog.Error("projection: live apply failed; consumer stopped",
						"seq", e.Seq, "type", e.Type, "err", err)
					return
				}
			}
		}
	}()
	return nil
}

func (r *Runtime) rebuild(ctx context.Context) error {
	for _, p := range r.projectors {
		// Reset is collection-scoped and owned by each projector via Apply;
		// for the MVP the store is ephemeral and empty at boot, so a full
		// re-read reconstructs state deterministically.
		_ = p
	}
	events, err := r.log.Read(ctx, 0)
	if err != nil {
		return fmt.Errorf("projection: read log: %w", err)
	}
	for _, e := range events {
		if err := r.applyAll(ctx, e); err != nil {
			return fmt.Errorf("projection: rebuild at seq %d: %w", e.Seq, err)
		}
	}
	return nil
}

func (r *Runtime) applyAll(ctx context.Context, e eventlog.Envelope) error {
	for _, p := range r.projectors {
		if err := p.Apply(ctx, e, r.store); err != nil {
			return fmt.Errorf("projector %q: %w", p.Name(), err)
		}
	}
	return nil
}

func (r *Runtime) setErr(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.err = err
}

// Err returns the first live-apply error, if any.
func (r *Runtime) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}
