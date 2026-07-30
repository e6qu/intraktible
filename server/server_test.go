// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/e6qu/intraktible/platform/auth"
	"github.com/e6qu/intraktible/platform/httpx"
)

type fakeSweeper struct{ started chan time.Duration }

func (f *fakeSweeper) Run(_ context.Context, interval time.Duration, _ func(error)) {
	f.started <- interval
}

// TestStartTimedSweeps proves every configured scheduler starts independently
// on the shared cadence — the regression was the case-manager SLA scheduler
// starting only when the decision-engine's monitor scheduler existed, so
// --modules=case-manager silently never ran SLA sweeps.
func TestStartTimedSweeps(t *testing.T) {
	ctx := context.Background()

	sla := &fakeSweeper{started: make(chan time.Duration, 1)}
	mon := &fakeSweeper{started: make(chan time.Duration, 1)}
	sweeps := []namedTimedSweeper{{name: "sla", runner: sla}, {name: "monitor", runner: mon}}
	if err := startTimedSweeps(ctx, "1h", sweeps, newSchedulerHealth(sweeps)); err != nil {
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
	soloSweeps := []namedTimedSweeper{{name: "solo", runner: solo}}
	if err := startTimedSweeps(ctx, "1h", soloSweeps, newSchedulerHealth(soloSweeps)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-solo.started:
	case <-time.After(5 * time.Second):
		t.Fatal("lone sweeper never started")
	}

	// Unset interval: sweeps stay off (nothing is spawned before the guard).
	off := &fakeSweeper{started: make(chan time.Duration, 1)}
	offSweeps := []namedTimedSweeper{{name: "off", runner: off}}
	if err := startTimedSweeps(ctx, "", offSweeps, newSchedulerHealth(offSweeps)); err != nil {
		t.Fatal(err)
	}
	if len(off.started) != 0 {
		t.Fatal("sweeper must not start when the interval is unset")
	}

	// A malformed or non-positive interval fails loudly.
	for _, bad := range []string{"soon", "-1m", "0s"} {
		if err := startTimedSweeps(ctx, bad, nil, newSchedulerHealth(nil)); err == nil {
			t.Fatalf("interval %q should be rejected", bad)
		}
	}
}

type recoveringSweeper struct {
	failed  chan struct{}
	recover chan struct{}
}

func (s *recoveringSweeper) Run(ctx context.Context, _ time.Duration, report func(error)) {
	report(errors.New("store unavailable"))
	close(s.failed)
	select {
	case <-ctx.Done():
		return
	case <-s.recover:
		report(nil)
	}
}

func TestSchedulerFailureDegradesHealthAndReadinessUntilRecovery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scheduler := &recoveringSweeper{failed: make(chan struct{}), recover: make(chan struct{})}
	sweeps := []namedTimedSweeper{{name: "deploy_schedule", runner: scheduler}}
	state := newSchedulerHealth(sweeps)
	if err := startTimedSweeps(ctx, "1h", sweeps, state); err != nil {
		t.Fatal(err)
	}
	select {
	case <-scheduler.failed:
	case <-time.After(5 * time.Second):
		t.Fatal("scheduler did not report its failed tick")
	}

	check := combinedHealth(func() error { return nil }, state.Err)
	if err := check(); err == nil || err.Error() != "scheduler deploy_schedule: store unavailable" {
		t.Fatalf("health error = %v", err)
	}
	for path, handler := range map[string]http.HandlerFunc{
		"/healthz": httpx.Health(check),
		"/readyz":  httpx.Ready(func() uint64 { return 0 }, func() uint64 { return 0 }, check, nil),
	} {
		rec := httptest.NewRecorder()
		handler(rec, httptest.NewRequest(http.MethodGet, path, http.NoBody))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s during scheduler failure = %d, want 503", path, rec.Code)
		}
	}

	close(scheduler.recover)
	deadline := time.Now().Add(5 * time.Second)
	for check() != nil && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if err := check(); err != nil {
		t.Fatalf("health did not recover after a successful tick: %v", err)
	}

	// One scheduler's success must not clear another scheduler's latched failure.
	independent := newSchedulerHealth([]namedTimedSweeper{
		{name: "deploy_schedule"},
		{name: "case_sla"},
	})
	independent.Report("deploy_schedule", errors.New("deploy failed"))
	independent.Report("case_sla", errors.New("SLA failed"))
	independent.Report("deploy_schedule", nil)
	if err := independent.Err(); err == nil || err.Error() != "scheduler case_sla: SLA failed" {
		t.Fatalf("independent scheduler failure was hidden: %v", err)
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
		{"prod + file log refused (would diverge across replicas)", Config{Env: "production", StoreKind: "sqlite", LogKind: "file"}, true, true},
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

// The single-process WAL is only safe when exactly one replica ever runs, which
// the server cannot observe for itself. It refuses by default and boots only on an
// explicit declaration — so the dangerous combination is unreachable by omission.
func TestPreflightFileLogNeedsSingleReplicaDeclaration(t *testing.T) {
	cfg := Config{Env: "production", StoreKind: "sqlite", LogKind: "file"}

	if err := preflight(cfg, true); err == nil {
		t.Fatal("--log=file in production must be refused without an explicit single-replica declaration")
	}

	t.Setenv("INTRAKTIBLE_SINGLE_REPLICA", "1")
	if err := preflight(cfg, true); err != nil {
		t.Fatalf("an explicit single-replica declaration should allow boot: %v", err)
	}
}

func TestPreflightPlaintextEscapeHatch(t *testing.T) {
	t.Setenv("INTRAKTIBLE_ALLOW_PLAINTEXT_AT_REST", "1")
	if err := preflight(Config{Env: "production", StoreKind: "postgres", LogKind: "postgres"}, false); err != nil {
		t.Fatalf("the explicit plaintext escape hatch should allow boot: %v", err)
	}
}

func TestPreflightPlaintextFalseDoesNotOptOut(t *testing.T) {
	t.Setenv("INTRAKTIBLE_ALLOW_PLAINTEXT_AT_REST", "false")
	if err := preflight(
		Config{Env: "production", StoreKind: "postgres", LogKind: "postgres"},
		false,
	); err == nil {
		t.Fatal("an explicit false plaintext switch bypassed encryption-at-rest preflight")
	}
}

func TestRuntimeEnvironmentRejectsMalformedValues(t *testing.T) {
	for _, key := range []string{
		"INTRAKTIBLE_AI_GUARDRAIL_PII",
		"INTRAKTIBLE_AI_GUARDRAIL_BLOCK_INJECTION",
		"INTRAKTIBLE_AI_STUB",
		"INTRAKTIBLE_ALLOW_PLAINTEXT_AT_REST",
		"INTRAKTIBLE_CONNECTOR_ALLOW_PRIVATE",
		"INTRAKTIBLE_SECURE_COOKIES",
		"INTRAKTIBLE_SINGLE_REPLICA",
		"INTRAKTIBLE_TRUST_PROXY",
	} {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, "truth-ish")
			if err := validateBooleanEnv(); err == nil {
				t.Fatalf("%s accepted a malformed boolean", key)
			}
		})
	}

	t.Run("login rps", func(t *testing.T) {
		t.Setenv("INTRAKTIBLE_LOGIN_RATE_LIMIT_RPS", "NaN")
		if _, err := envFloat("INTRAKTIBLE_LOGIN_RATE_LIMIT_RPS", 10); err == nil {
			t.Fatal("NaN login rate accepted")
		}
	})
	t.Run("login burst", func(t *testing.T) {
		t.Setenv("INTRAKTIBLE_LOGIN_RATE_LIMIT_BURST", "-1")
		if _, err := envInt("INTRAKTIBLE_LOGIN_RATE_LIMIT_BURST", 30); err == nil {
			t.Fatal("negative login burst accepted")
		}
	})
	t.Run("drift window", func(t *testing.T) {
		t.Setenv("INTRAKTIBLE_MODEL_DRIFT_WINDOW", "last-week")
		if _, err := driftWindowDays(); err == nil {
			t.Fatal("malformed drift window accepted")
		}
	})
}

func TestRuntimeEnvironmentDefaultsOnlyWhenAbsent(t *testing.T) {
	t.Setenv("INTRAKTIBLE_LOGIN_RATE_LIMIT_RPS", "")
	t.Setenv("INTRAKTIBLE_LOGIN_RATE_LIMIT_BURST", "")
	t.Setenv("INTRAKTIBLE_MODEL_DRIFT_WINDOW", "")
	rps, err := envFloat("INTRAKTIBLE_LOGIN_RATE_LIMIT_RPS", 10)
	if err != nil || rps != 10 {
		t.Fatalf("default login rps = %v, %v", rps, err)
	}
	burst, err := envInt("INTRAKTIBLE_LOGIN_RATE_LIMIT_BURST", 30)
	if err != nil || burst != 30 {
		t.Fatalf("default login burst = %v, %v", burst, err)
	}
	window, err := driftWindowDays()
	if err != nil || window != 0 {
		t.Fatalf("default drift window = %v, %v", window, err)
	}
}

func TestProcessRoleValidation(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: "", want: processRoleAll},
		{input: "all", want: processRoleAll},
		{input: " API ", want: processRoleAPI},
		{input: "worker", want: processRoleWorker},
		{input: "scheduler", want: processRoleScheduler},
	}
	for _, test := range tests {
		got, err := normalizeProcessRole(test.input)
		want := test.want
		if err != nil || got != want {
			t.Fatalf("normalizeProcessRole(%q) = %q, %v; want %q", test.input, got, err, want)
		}
	}
	if _, err := normalizeProcessRole("api-and-worker"); err == nil {
		t.Fatal("combined unknown process role was accepted")
	}
}

func TestDecisionRecoveryIntervalMustBePositive(t *testing.T) {
	t.Setenv("INTRAKTIBLE_DECISION_RECOVERY_INTERVAL", "0s")
	if _, err := positiveEnvDuration(
		"INTRAKTIBLE_DECISION_RECOVERY_INTERVAL",
		time.Second,
	); err == nil {
		t.Fatal("zero recovery interval was accepted")
	}
	t.Setenv("INTRAKTIBLE_DECISION_RECOVERY_INTERVAL", "250ms")
	got, err := positiveEnvDuration(
		"INTRAKTIBLE_DECISION_RECOVERY_INTERVAL",
		time.Second,
	)
	if err != nil || got != 250*time.Millisecond {
		t.Fatalf("recovery interval = %v, %v", got, err)
	}
}
