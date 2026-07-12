// SPDX-License-Identifier: AGPL-3.0-or-later

package decisionengine_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/e6qu/intraktible/decision-engine/command"
	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// fakeConnector returns a fixed bureau response; it records how many times it was
// actually fetched, so a test can prove the consent gate blocks the pull BEFORE I/O.
type fakeConnector struct{ fetched int }

func (f *fakeConnector) Fetch(_ context.Context, _ identity.Identity, _ string, _ json.RawMessage) (json.RawMessage, error) {
	f.fetched++
	return json.RawMessage(`{"score":700}`), nil
}

// fakeConsent is an in-memory ConsentGate.
type fakeConsent struct {
	has      map[string]bool
	recorded []string
}

func newFakeConsent() *fakeConsent { return &fakeConsent{has: map[string]bool{}} }

func (f *fakeConsent) HasConsent(_ context.Context, _ identity.Identity, subject, purpose string) (bool, error) {
	return f.has[subject+"\x00"+purpose], nil
}
func (f *fakeConsent) RecordConsent(_ context.Context, _ identity.Identity, subject, purpose, _ string) error {
	f.has[subject+"\x00"+purpose] = true
	f.recorded = append(f.recorded, subject+"/"+purpose)
	return nil
}

// bureauConsentGraph: input -> connect(bureau, requires_consent) -> output.
func bureauConsentGraph() events.Graph {
	return events.Graph{
		Nodes: []events.Node{
			{ID: "in", Type: events.NodeInput},
			{ID: "pull", Type: events.NodeConnect, Config: json.RawMessage(
				`{"connector":"bureau","output":"cr","requires_consent":"credit_underwriting"}`)},
			{ID: "out", Type: events.NodeOutput},
		},
		Edges: []events.Edge{{From: "in", To: "pull"}, {From: "pull", To: "out"}},
	}
}

// consentEnv bundles one consent test's wiring (a struct keeps the setup helper under
// the results-count lint and reads clearly at the call site).
type consentEnv struct {
	ctx  context.Context
	dh   *command.DecideHandler
	id   identity.Identity
	conn *fakeConnector
	gate *fakeConsent
}

func consentTestSetup(t *testing.T) consentEnv {
	t.Helper()
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "bank"}
	st := store.NewMemory()
	publishFlow(t, ctx, log, st, id, "credit", "Credit", bureauConsentGraph())
	conn, gate := &fakeConnector{}, newFakeConsent()
	dh := command.NewDecideHandler(log, st, command.WithConnectors(conn), command.WithConsent(gate))
	return consentEnv{ctx: ctx, dh: dh, id: id, conn: conn, gate: gate}
}

func applicant() command.EntityRef { return command.EntityRef{Type: "applicant", ID: "a1"} }

func TestConsentGateBlocksPullWithoutConsent(t *testing.T) {
	e := consentTestSetup(t)

	// A subject with no consent and no consent block — the bureau must NOT be pulled.
	_, err := e.dh.Decide(e.ctx, e.id, "credit", "sandbox", map[string]any{"amount": 1000}, applicant())
	if err == nil || !strings.Contains(err.Error(), "consent") {
		t.Fatalf("expected a consent error, got %v", err)
	}
	if e.conn.fetched != 0 {
		t.Fatalf("the bureau was fetched %d times without permissible purpose", e.conn.fetched)
	}
}

func TestConsentCapturedInRequestPermitsPull(t *testing.T) {
	e := consentTestSetup(t)

	// The bank asserts, in the request, the consent it obtained from its customer.
	res, err := e.dh.Decide(e.ctx, e.id, "credit", "sandbox", map[string]any{
		"amount":  1000,
		"consent": map[string]any{"purposes": []string{"credit_underwriting"}, "basis": "consent"},
	}, applicant())
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "completed" {
		t.Fatalf("status=%s err=%s", res.Status, res.Error)
	}
	if e.conn.fetched != 1 {
		t.Fatalf("bureau fetched %d times, want 1", e.conn.fetched)
	}
	if len(e.gate.recorded) != 1 || e.gate.recorded[0] != "applicant/a1/credit_underwriting" {
		t.Fatalf("recorded consent = %v", e.gate.recorded)
	}
}

func TestStandingConsentPermitsPull(t *testing.T) {
	e := consentTestSetup(t)
	e.gate.has["applicant/a1\x00credit_underwriting"] = true // granted earlier (via the API)

	res, err := e.dh.Decide(e.ctx, e.id, "credit", "sandbox", map[string]any{"amount": 1000}, applicant())
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "completed" || e.conn.fetched != 1 {
		t.Fatalf("status=%s fetched=%d", res.Status, e.conn.fetched)
	}
}

func TestConsentRequiredButNoSubjectFails(t *testing.T) {
	e := consentTestSetup(t)

	// No entity ref → no subject to bear consent → the pull is refused.
	_, err := e.dh.Decide(e.ctx, e.id, "credit", "sandbox", map[string]any{"amount": 1000}, command.EntityRef{})
	if err == nil || !strings.Contains(err.Error(), "no subject") {
		t.Fatalf("expected a no-subject error, got %v", err)
	}
	if e.conn.fetched != 0 {
		t.Fatal("bureau must not be fetched without a subject")
	}
}
