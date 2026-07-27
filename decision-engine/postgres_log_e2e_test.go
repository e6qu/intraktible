// SPDX-License-Identifier: AGPL-3.0-or-later

package decisionengine_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/e6qu/intraktible/decision-engine/command"
	"github.com/e6qu/intraktible/decision-engine/domain"
	"github.com/e6qu/intraktible/decision-engine/internal/flowtest"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
)

// TestAuthoringOnPostgresLog closes the structural gap that let a broken HA backend
// ship: authoring a flow mints optimistic-concurrency claims, and NOTHING exercised a
// claim-minting path against a real PostgreSQL.
//
// The Postgres log's own tests appended envelopes carrying no claim, and every
// decision-engine test builds its own in-memory or WAL log — so although CI ran the
// whole suite against a live server, the two never met. The result was that
// `--log=postgres`, the backend documented for multi-replica HA and used by the Helm
// chart, the ECS module and the production compose file, returned 400 on every flow
// creation.
//
// This runs the real authoring commands over a real Postgres log, so any future claim
// shape the backend cannot store fails here rather than in someone's cluster.
func TestAuthoringOnPostgresLog(t *testing.T) {
	dsn := os.Getenv("INTRAKTIBLE_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set INTRAKTIBLE_TEST_POSTGRES (a pgx DSN) to exercise authoring on the networked log")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DROP TABLE IF EXISTS events`); err != nil {
		t.Fatal(err)
	}
	pool.Close()

	log, err := eventlog.OpenPostgresLog(ctx, dsn, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()

	h := command.NewHandler(log)
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "author"}

	// Creating a flow claims its slug — the append that used to fail outright.
	flowID, _, err := h.CreateFlow(ctx, id, domain.CreateFlow{Slug: "pg-authoring", Name: "PG Authoring"})
	if err != nil {
		t.Fatalf("create flow on the networked log: %v", err)
	}

	// Publishing claims (flow, version) — a second claim shape, so a fix that only
	// covered the first would still be caught here.
	if _, _, _, err := h.PublishVersion(ctx, id, domain.PublishVersion{
		FlowID: flowID, Graph: flowtest.ConstGraph("v1"),
	}); err != nil {
		t.Fatalf("publish a version on the networked log: %v", err)
	}

	// The claims still do their job: the same slug twice is refused.
	if _, _, err := h.CreateFlow(ctx, id, domain.CreateFlow{Slug: "pg-authoring", Name: "Duplicate"}); err == nil {
		t.Fatal("re-using a flow slug was accepted; the slug claim is not being enforced")
	}

	// And a different slug is not caught by it — the encoding must stay injective, or
	// distinct flows would claim each other's slot.
	if _, _, err := h.CreateFlow(ctx, id, domain.CreateFlow{Slug: "pg-authoring-2", Name: "Second"}); err != nil {
		t.Fatalf("a distinct slug was refused: %v", err)
	}
}
