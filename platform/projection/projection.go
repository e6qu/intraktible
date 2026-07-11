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
	"github.com/e6qu/intraktible/platform/metrics"
	"github.com/e6qu/intraktible/platform/store"
)

// Projector folds events into the store. Implementations must be deterministic:
// applying the same events in order always yields the same state.
type Projector interface {
	Name() string
	// Collections lists the store collections this projector owns; the runtime
	// resets them before a rebuild so replaying into a non-empty store is
	// idempotent. May be empty for a projector that writes nothing.
	Collections() []string
	Apply(ctx context.Context, e eventlog.Envelope, s store.Store) error
}

// checkpointCollection / checkpointKey store the highest seq applied to the
// store. It is NOT owned by any projector (RebuildTo never resets it), so it
// survives a rebuild and drives incremental resume.
const (
	checkpointCollection = "_projection"
	checkpointKey        = "applied_head"
)

type checkpoint struct {
	Seq uint64 `json:"seq"`
}

// Runtime applies a set of projectors to the log.
type Runtime struct {
	log        eventlog.Log
	store      store.Store
	tx         store.TxStore // non-nil when the store supports atomic transactions
	projectors []Projector

	mu      sync.Mutex
	err     error
	applied uint64 // highest seq applied this run (guards against re-apply)
}

// New builds a Runtime. The store must be crash-safe by construction: either a
// store.TxStore (checkpoint advances atomically with each event) or one that
// declares store.Ephemeral (a restart loses everything, so a full rebuild is
// always safe). A durable store that is neither would silently double-count
// non-idempotent projector counters on crash recovery — so it is rejected here,
// loudly, rather than deferring the corruption to a production crash.
func New(log eventlog.Log, st store.Store, projectors ...Projector) *Runtime {
	r := &Runtime{log: log, store: st, projectors: projectors}
	switch s := st.(type) {
	case store.TxStore:
		r.tx = s
	case store.Ephemeral:
		// non-atomic apply path is safe; a crash triggers a full rebuild
	default:
		panic("projection: store must implement store.TxStore (durable) or store.Ephemeral (rebuilt on restart) — a durable non-transactional store would double-count projector counters on crash recovery")
	}
	return r
}

// Start brings the projections up to date — incrementally resuming from the
// stored checkpoint when the durable store has one, else fully rebuilding from
// offset 0 — then consumes live events until ctx is cancelled. It returns after
// the initial catch-up so the server starts with current state. A live apply
// error stops the consumer and is surfaced via Err (fail loudly).
func (r *Runtime) Start(ctx context.Context) error {
	sub, cancel := r.log.Subscribe()
	if err := r.bootstrap(ctx); err != nil {
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
				if err := r.applyOne(ctx, e); err != nil {
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

// bootstrap either resumes from the checkpoint (a transactional durable store
// that has applied events before) or fully rebuilds from offset 0.
func (r *Runtime) bootstrap(ctx context.Context) error {
	cp, _, err := store.GetDoc[checkpoint](ctx, r.store, checkpointCollection, checkpointKey)
	if err != nil {
		return fmt.Errorf("projection: read checkpoint: %w", err)
	}
	head := r.log.Head()
	// Incremental resume: only when the store can apply transactionally (so the
	// checkpoint advances atomically with each event) and the checkpoint is a
	// valid prefix of the log.
	if r.tx != nil && cp.Seq > 0 && cp.Seq <= head {
		r.setApplied(cp.Seq)
		evs, rerr := r.log.Read(ctx, cp.Seq+1)
		if rerr != nil {
			return fmt.Errorf("projection: read log for resume: %w", rerr)
		}
		for _, e := range evs {
			if err := r.applyOne(ctx, e); err != nil {
				return fmt.Errorf("projection: resume at seq %d: %w", e.Seq, err)
			}
		}
		return nil
	}
	// Full rebuild from scratch — bounded to the head snapshot so events appended
	// during boot are applied only once, by the live consumer (not re-applied here
	// and then again off the bus).
	if _, err := r.RebuildTo(ctx, head); err != nil {
		return err
	}
	r.setApplied(head)
	return r.writeCheckpoint(ctx, r.store, head)
}

// applyOne applies a delivered event. Already-applied events are no-ops (the
// boot-rebuild/live-bus overlap and resume can never double-apply). On a gap —
// the in-process bus drops to a slow subscriber, or a backend delivers out of
// order — it backfills the missing range from the authoritative, ordered log so
// the read model never silently skips an event.
func (r *Runtime) applyOne(ctx context.Context, e eventlog.Envelope) error {
	r.mu.Lock()
	applied := r.applied
	r.mu.Unlock()
	switch {
	case e.Seq <= applied:
		return nil
	case e.Seq == applied+1:
		return r.applyContiguous(ctx, e)
	}
	// Gap: re-read from the checkpoint and apply the missing events in order. The
	// delivered event is within this range (its Seq <= the log head we read), so
	// it is applied here and is a no-op when the bus redelivers it.
	evs, err := r.log.Read(ctx, applied+1)
	if err != nil {
		return fmt.Errorf("projection: backfill from seq %d: %w", applied+1, err)
	}
	for _, be := range evs {
		r.mu.Lock()
		a := r.applied
		r.mu.Unlock()
		if be.Seq <= a {
			continue
		}
		if err := r.applyContiguous(ctx, be); err != nil {
			return err
		}
	}
	return nil
}

// applyContiguous applies a single next-in-sequence event and advances the
// checkpoint, atomically when the store supports transactions. It REFUSES a
// non-contiguous event (Seq != applied+1): the caller must only reach here with the
// immediate successor, so a gap means a lower seq isn't yet visible in the log (e.g.
// an uncommitted Postgres BIGSERIAL row that a higher seq committed ahead of).
// Advancing the checkpoint past that gap would permanently skip the missing event from
// the incremental read model, so we fail LOUD instead — the runtime surfaces the error
// (/healthz degraded) and re-applies the range once the lower seq is visible.
func (r *Runtime) applyContiguous(ctx context.Context, e eventlog.Envelope) error {
	r.mu.Lock()
	applied := r.applied
	r.mu.Unlock()
	if e.Seq != applied+1 {
		return fmt.Errorf("projection: refusing to apply seq %d over applied head %d — a lower seq is not yet visible in the log; advancing past it would skip an event", e.Seq, applied)
	}
	if r.tx == nil {
		// Ephemeral store (memory): no atomicity needed — a crash loses everything
		// and the next boot fully rebuilds.
		if err := r.applyAll(ctx, e, r.store); err != nil {
			return err
		}
		if err := r.writeCheckpoint(ctx, r.store, e.Seq); err != nil {
			return err
		}
		r.setApplied(e.Seq)
		return nil
	}
	tx, err := r.tx.Begin(ctx)
	if err != nil {
		return fmt.Errorf("projection: begin tx: %w", err)
	}
	if err := r.applyAll(ctx, e, tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := r.writeCheckpoint(ctx, tx, e.Seq); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("projection: commit seq %d: %w", e.Seq, err)
	}
	r.setApplied(e.Seq)
	return nil
}

func (r *Runtime) writeCheckpoint(ctx context.Context, s store.Store, seq uint64) error {
	if err := store.PutDoc(ctx, s, checkpointCollection, checkpointKey, checkpoint{Seq: seq}); err != nil {
		return fmt.Errorf("projection: write checkpoint: %w", err)
	}
	return nil
}

func (r *Runtime) setApplied(seq uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if seq > r.applied {
		r.applied = seq
		metrics.SetProjectionApplied(seq)
	}
}

// RebuildTo replays the durable log into the projections, applying only events
// with Seq <= upTo (upTo 0 means all), and returns the number applied. It is the
// basis for operator rebuild and log-based rollback: rebuilding into a fresh store
// as of an earlier seq yields the exact state at that point without mutating the
// append-only log. (The MVP store is empty at boot, so a full re-read reconstructs
// state deterministically; durable stores Reset per collection first.)
func (r *Runtime) RebuildTo(ctx context.Context, upTo uint64) (int, error) {
	// Reset each projector's collections first so a rebuild into a non-empty
	// store (a durable store, or a repeated rebuild) is idempotent.
	for _, p := range r.projectors {
		for _, c := range p.Collections() {
			if err := r.store.Reset(ctx, c); err != nil {
				return 0, fmt.Errorf("projection: reset %s: %w", c, err)
			}
		}
	}
	events, err := r.log.Read(ctx, 0)
	if err != nil {
		return 0, fmt.Errorf("projection: read log: %w", err)
	}
	applied := 0
	for _, e := range events {
		if upTo != 0 && e.Seq > upTo {
			break
		}
		if err := r.applyAll(ctx, e, r.store); err != nil {
			return applied, fmt.Errorf("projection: rebuild at seq %d: %w", e.Seq, err)
		}
		applied++
	}
	return applied, nil
}

func (r *Runtime) applyAll(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	for _, p := range r.projectors {
		if err := p.Apply(ctx, e, s); err != nil {
			return fmt.Errorf("projector %q: %w", p.Name(), err)
		}
	}
	return nil
}

func (r *Runtime) setErr(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.err = err
	metrics.IncProjectionErrors()
}

// Err returns the first live-apply error, if any.
func (r *Runtime) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

// Applied returns the highest event Seq applied to the store this run. Tests use
// it to wait for read-after-write consistency; live reads are eventually
// consistent by design (bounded by bus/poll lag).
func (r *Runtime) Applied() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.applied
}
