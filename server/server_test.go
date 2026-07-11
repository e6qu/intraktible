// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"context"
	"testing"
	"time"

	"github.com/e6qu/intraktible/platform/auth"
)

type fakeSweeper struct{ started chan time.Duration }

func (f *fakeSweeper) Run(_ context.Context, interval time.Duration) { f.started <- interval }

// TestStartTimedSweeps proves every configured scheduler starts independently
// on the shared cadence — the regression was the case-manager SLA scheduler
// starting only when the decision-engine's monitor scheduler existed, so
// --modules=case-manager silently never ran SLA sweeps.
func TestStartTimedSweeps(t *testing.T) {
	ctx := context.Background()

	sla := &fakeSweeper{started: make(chan time.Duration, 1)}
	mon := &fakeSweeper{started: make(chan time.Duration, 1)}
	if err := startTimedSweeps(ctx, "1h", []timedSweeper{sla, mon}); err != nil {
		t.Fatal(err)
	}
	for name, s := range map[string]*fakeSweeper{"sla": sla, "monitor": mon} {
		select {
		case d := <-s.started:
			if d != time.Hour {
				t.Fatalf("%s sweeper started with interval %v, want 1h", name, d)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s sweeper never started", name)
		}
	}

	// A lone sweeper (the split-services shape) still starts.
	solo := &fakeSweeper{started: make(chan time.Duration, 1)}
	if err := startTimedSweeps(ctx, "1h", []timedSweeper{solo}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-solo.started:
	case <-time.After(5 * time.Second):
		t.Fatal("lone sweeper never started")
	}

	// Unset interval: sweeps stay off (nothing is spawned before the guard).
	off := &fakeSweeper{started: make(chan time.Duration, 1)}
	if err := startTimedSweeps(ctx, "", []timedSweeper{off}); err != nil {
		t.Fatal(err)
	}
	if len(off.started) != 0 {
		t.Fatal("sweeper must not start when the interval is unset")
	}

	// A malformed or non-positive interval fails loudly.
	for _, bad := range []string{"soon", "-1m", "0s"} {
		if err := startTimedSweeps(ctx, bad, nil); err == nil {
			t.Fatalf("interval %q should be rejected", bad)
		}
	}
}

// The well-known dev admin key is a local-dev convenience and must never be seeded
// onto a durable store — a real deployment uses sqlite/postgres, so it can never
// boot with a known admin credential no matter the flag value.
func TestSeedDevKeyOnlyOnMemoryStore(t *testing.T) {
	const dev = "dev-sandbox-key"
	cases := []struct {
		store string
		want  bool
	}{
		{"memory", true},
		{"sqlite", false},
		{"postgres", false},
	}
	for _, c := range cases {
		kr := auth.NewKeyring()
		if got := seedDevKey(kr, dev, c.store); got != c.want {
			t.Errorf("seedDevKey(store=%q) = %v, want %v", c.store, got, c.want)
		}
		_, resolved := kr.Resolve(dev)
		if resolved != c.want {
			t.Errorf("store=%q: dev key resolvable = %v, want %v", c.store, resolved, c.want)
		}
	}

	// An empty key never seeds, even on memory.
	if seedDevKey(auth.NewKeyring(), "", "memory") {
		t.Error("an empty --dev-api-key must not seed any key")
	}
}

func TestPreflightRefusesInsecureProduction(t *testing.T) {
	t.Setenv("INTRAKTIBLE_ENCRYPTION_KEY", "")
	t.Setenv("INTRAKTIBLE_ALLOW_PLAINTEXT_AT_REST", "")
	cases := []struct {
		name       string
		cfg        Config
		encryption bool
		wantErr    bool
	}{
		{"dev env is always permissive", Config{Env: "development", StoreKind: "memory", LogKind: "memory"}, false, false},
		{"empty env is permissive", Config{StoreKind: "memory"}, false, false},
		{"prod + memory store refused", Config{Env: "production", StoreKind: "memory", LogKind: "postgres"}, true, true},
		{"prod + memory log refused", Config{Env: "production", StoreKind: "postgres", LogKind: "memory"}, true, true},
		{"prod without encryption refused", Config{Env: "production", StoreKind: "postgres", LogKind: "postgres"}, false, true},
		{"prod + durable + encryption ok", Config{Env: "production", StoreKind: "postgres", LogKind: "postgres"}, true, false},
		{"prod + file log ok (warns)", Config{Env: "production", StoreKind: "sqlite", LogKind: "file"}, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := preflight(c.cfg, c.encryption)
			if c.wantErr && err == nil {
				t.Fatal("expected a refusal, got nil")
			}
			if !c.wantErr && err != nil {
				t.Fatalf("unexpected refusal: %v", err)
			}
		})
	}
}

func TestPreflightPlaintextEscapeHatch(t *testing.T) {
	t.Setenv("INTRAKTIBLE_ALLOW_PLAINTEXT_AT_REST", "1")
	if err := preflight(Config{Env: "production", StoreKind: "postgres", LogKind: "postgres"}, false); err != nil {
		t.Fatalf("the explicit plaintext escape hatch should allow boot: %v", err)
	}
}
