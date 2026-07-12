// SPDX-License-Identifier: AGPL-3.0-or-later

package consent_test

import (
	"context"
	"testing"
	"time"

	"github.com/e6qu/intraktible/platform/consent"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

var (
	ctx = context.Background()
	id  = identity.Identity{Org: "o", Workspace: "w", Actor: "operator"}
	t0  = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
)

// build applies every logged consent event through the projector into a fresh store,
// so a test can grant/withdraw via the command and then read the materialized state.
func build(t *testing.T, log eventlog.Log) store.Store {
	t.Helper()
	st := store.NewMemory()
	evs, err := log.Read(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		if err := (consent.Projector{}).Apply(ctx, e, st); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

func handler(now time.Time) (*consent.Handler, eventlog.Log) {
	log := eventlog.NewMemory()
	return consent.NewHandler(log).WithNow(func() time.Time { return now }), log
}

func TestGrantAndWithdraw(t *testing.T) {
	h, log := handler(t0)
	if _, err := h.Grant(ctx, id, consent.GrantCmd{Subject: "cust-1", Purpose: "credit_underwriting", Basis: consent.BasisConsent}); err != nil {
		t.Fatal(err)
	}
	st := build(t, log)
	if ok, _ := consent.Has(ctx, st, id, "cust-1", "credit_underwriting", t0); !ok {
		t.Fatal("expected active consent after grant")
	}
	rec, found, _ := consent.Get(ctx, st, id, "cust-1", "credit_underwriting")
	if !found || rec.Basis != consent.BasisConsent || rec.UpdatedBy != "operator" {
		t.Fatalf("record = %+v", rec)
	}

	// Withdraw revokes it.
	if _, err := h.Withdraw(ctx, id, "cust-1", "credit_underwriting", "customer request"); err != nil {
		t.Fatal(err)
	}
	st = build(t, log)
	if ok, _ := consent.Has(ctx, st, id, "cust-1", "credit_underwriting", t0); ok {
		t.Fatal("consent must be inactive after withdrawal")
	}
	rec, _, _ = consent.Get(ctx, st, id, "cust-1", "credit_underwriting")
	if rec.Granted || rec.WithdrawnAt == nil {
		t.Fatalf("withdrawn record = %+v", rec)
	}
}

func TestConsentExpiry(t *testing.T) {
	h, log := handler(t0)
	exp := t0.Add(24 * time.Hour)
	if _, err := h.Grant(ctx, id, consent.GrantCmd{Subject: "cust-1", Purpose: "marketing", Basis: consent.BasisConsent, ExpiresAt: &exp}); err != nil {
		t.Fatal(err)
	}
	st := build(t, log)
	if ok, _ := consent.Has(ctx, st, id, "cust-1", "marketing", t0.Add(time.Hour)); !ok {
		t.Fatal("consent should be active before expiry")
	}
	if ok, _ := consent.Has(ctx, st, id, "cust-1", "marketing", exp.Add(time.Hour)); ok {
		t.Fatal("consent must be inactive after expiry")
	}
}

func TestGrantValidation(t *testing.T) {
	h, _ := handler(t0)
	if _, err := h.Grant(ctx, id, consent.GrantCmd{Subject: "", Purpose: "p", Basis: consent.BasisConsent}); err == nil {
		t.Error("empty subject should fail")
	}
	if _, err := h.Grant(ctx, id, consent.GrantCmd{Subject: "s", Purpose: "  ", Basis: consent.BasisConsent}); err == nil {
		t.Error("empty purpose should fail")
	}
	if _, err := h.Grant(ctx, id, consent.GrantCmd{Subject: "s", Purpose: "p", Basis: consent.LawfulBasis("guess")}); err == nil {
		t.Error("unknown basis should fail")
	}
	past := t0.Add(-time.Hour)
	if _, err := h.Grant(ctx, id, consent.GrantCmd{Subject: "s", Purpose: "p", Basis: consent.BasisConsent, ExpiresAt: &past}); err == nil {
		t.Error("past expiry should fail")
	}
	// A blank basis defaults to explicit consent.
	if _, err := h.Grant(ctx, id, consent.GrantCmd{Subject: "s", Purpose: "p"}); err != nil {
		t.Fatalf("blank basis should default: %v", err)
	}
	// An unknown collection method is rejected at the boundary.
	if _, err := h.Grant(ctx, id, consent.GrantCmd{Subject: "s", Purpose: "p", Evidence: &consent.Evidence{Method: "telepathy"}}); err == nil {
		t.Error("unknown collection method should fail")
	}
	// A content hash with no algorithm is unverifiable and rejected.
	if _, err := h.Grant(ctx, id, consent.GrantCmd{Subject: "s", Purpose: "p", Evidence: &consent.Evidence{ContentHash: "abc123"}}); err == nil {
		t.Error("content_hash without hash_algo should fail")
	}
}

func TestGrantWithEvidence(t *testing.T) {
	h, log := handler(t0)
	ev := &consent.Evidence{
		Method: consent.MethodESignature, Reference: "loan-app-9931.pdf",
		ContentHash: "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
		HashAlgo:    "sha-256", NoticeVersion: "privacy-notice-2026-Q2",
	}
	if _, err := h.Grant(ctx, id, consent.GrantCmd{
		Subject: "cust-1", Purpose: "credit_underwriting", Basis: consent.BasisContract, Evidence: ev,
	}); err != nil {
		t.Fatal(err)
	}
	st := build(t, log)
	rec, found, _ := consent.Get(ctx, st, id, "cust-1", "credit_underwriting")
	if !found || rec.Evidence == nil {
		t.Fatalf("expected evidence on record, got %+v", rec)
	}
	if rec.Evidence.Method != consent.MethodESignature || rec.Evidence.ContentHash != ev.ContentHash ||
		rec.Evidence.NoticeVersion != "privacy-notice-2026-Q2" {
		t.Fatalf("evidence not preserved: %+v", rec.Evidence)
	}
}

func TestListSubjectPurposes(t *testing.T) {
	h, log := handler(t0)
	h.Grant(ctx, id, consent.GrantCmd{Subject: "cust-1", Purpose: "credit_underwriting", Basis: consent.BasisContract})
	h.Grant(ctx, id, consent.GrantCmd{Subject: "cust-1", Purpose: "marketing", Basis: consent.BasisConsent})
	h.Grant(ctx, id, consent.GrantCmd{Subject: "cust-2", Purpose: "credit_underwriting", Basis: consent.BasisConsent}) // different subject
	st := build(t, log)

	recs, err := consent.List(ctx, st, id, "cust-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 || recs[0].Purpose != "credit_underwriting" || recs[1].Purpose != "marketing" {
		t.Fatalf("cust-1 consents = %+v", recs)
	}
}

func TestWithdrawWithoutGrantIsIdempotent(t *testing.T) {
	h, log := handler(t0)
	if _, err := h.Withdraw(ctx, id, "cust-1", "never_granted", ""); err != nil {
		t.Fatal(err)
	}
	st := build(t, log)
	if ok, _ := consent.Has(ctx, st, id, "cust-1", "never_granted", t0); ok {
		t.Fatal("a purpose only ever withdrawn is not consented")
	}
}
