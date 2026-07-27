// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !js

package eventlog_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/secretbox"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Optimistic-concurrency claims (Envelope.Unique) are enforced differently by every
// backend — a map for memory, a partial unique index for SQLite and Postgres, a
// JetStream Msg-Id dedup window for NATS, and nothing of its own for the encryption
// wrapper, which must pass the claim through untouched.
//
// Each mechanism was tested in isolation. The claim-minting code was tested in
// isolation. They were never tested together, which is precisely how --log=postgres
// shipped unable to store a claim at all. This asserts the SAME contract against
// every backend so a divergence is a test failure rather than a production
// discovery.
//
// The contract: a claim can be made; the same claim again conflicts; a DIFFERENT
// claim sharing a NUL-separated prefix does not (the separator these claims use must
// survive whatever encoding a backend applies).
func assertClaimContract(t *testing.T, log eventlog.Log) {
	t.Helper()
	ctx := context.Background()
	env := func(unique string) eventlog.Envelope {
		return eventlog.Envelope{
			Org: "demo", Workspace: "main", Actor: "author", Stream: "s", Type: "evt",
			Time: time.Unix(1, 0).UTC(), Payload: []byte(`{"k":"v"}`), Unique: unique,
		}
	}

	if _, err := log.Append(ctx, env("flow.slug\x00demo\x00main\x00alpha")); err != nil {
		t.Fatalf("claiming a NUL-separated key failed: %v", err)
	}
	if _, err := log.Append(ctx, env("flow.slug\x00demo\x00main\x00beta")); err != nil {
		t.Fatalf("a distinct claim sharing a NUL prefix was refused: %v", err)
	}
	if _, err := log.Append(ctx, env("flow.slug\x00demo\x00main\x00alpha")); !errors.Is(err, eventlog.ErrConflict) {
		t.Fatalf("re-claiming the same key returned %v, want ErrConflict", err)
	}
}

func TestClaimContractMemory(t *testing.T) {
	assertClaimContract(t, eventlog.NewMemory())
}

func TestClaimContractWAL(t *testing.T) {
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	assertClaimContract(t, log)
}

func TestClaimContractSQLite(t *testing.T) {
	log, err := eventlog.OpenSQLiteLog(t.TempDir(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	assertClaimContract(t, log)
}

func TestClaimContractNATS(t *testing.T) {
	log, err := eventlog.OpenNATSLog(runNATS(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	assertClaimContract(t, log)
}

func TestClaimContractPostgres(t *testing.T) {
	dsn := os.Getenv("INTRAKTIBLE_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set INTRAKTIBLE_TEST_POSTGRES (a pgx DSN) to run the Postgres claim-contract test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DROP TABLE IF EXISTS events`); err != nil {
		pool.Close()
		t.Fatal(err)
	}
	pool.Close()

	log, err := eventlog.OpenPostgresLog(ctx, dsn, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	assertClaimContract(t, log)
}

// Encryption at rest is REQUIRED in production, so the wrapper is the configuration
// every real deployment runs. It seals the payload and must leave the claim alone; if
// it did not, uniqueness would fail only in production and only under encryption.
func TestClaimContractEncrypted(t *testing.T) {
	kr, err := secretbox.KeyringFromKeys(strings.Repeat("a1", 32))
	if err != nil {
		t.Fatal(err)
	}
	if kr == nil {
		t.Fatal("keyring is nil, so the wrapper under test would be a no-op")
	}
	assertClaimContract(t, eventlog.Encrypted(eventlog.NewMemory(), kr))
}
