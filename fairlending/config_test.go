// SPDX-License-Identifier: AGPL-3.0-or-later

package fairlending_test

import (
	"strings"
	"testing"
	"time"

	"github.com/e6qu/intraktible/decision-engine/history"
	"github.com/e6qu/intraktible/decision-engine/policy"
	"github.com/e6qu/intraktible/fairlending"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/store"
)

func TestConfigRoundTrip(t *testing.T) {
	log := eventlog.NewMemory()
	st := store.NewMemory()
	h := fairlending.NewHandler(log).WithNow(func() time.Time { return now })

	e, err := h.SetConfig(ctx, id, "f1", "applicant.gender", policy.Approve, 0.75)
	if err != nil {
		t.Fatal(err)
	}
	if err := (fairlending.ConfigProjector{}).Apply(ctx, e, st); err != nil {
		t.Fatal(err)
	}
	cfg, found, err := fairlending.ReadConfig(ctx, st, id, "f1")
	if err != nil || !found {
		t.Fatalf("read config: found=%v err=%v", found, err)
	}
	if cfg.Attribute != "applicant.gender" || cfg.Favorable != policy.Approve || cfg.Threshold != 0.75 {
		t.Fatalf("config = %+v", cfg)
	}
	if cfg.UpdatedBy != id.Actor {
		t.Fatalf("updated_by = %q", cfg.UpdatedBy)
	}
}

func TestSetConfigValidates(t *testing.T) {
	h := fairlending.NewHandler(eventlog.NewMemory())
	cases := []struct {
		name, flow, attr string
		fav              policy.Disposition
		threshold        float64
	}{
		{"no flow", "", "x", policy.Approve, 0},
		{"no attribute", "f1", "  ", policy.Approve, 0},
		{"bad favorable", "f1", "x", policy.Disposition("maybe"), 0},
		{"threshold too high", "f1", "x", policy.Approve, 1.5},
		{"threshold negative", "f1", "x", policy.Approve, -0.1},
	}
	for _, c := range cases {
		if _, err := h.SetConfig(ctx, id, c.flow, c.attr, c.fav, c.threshold); err == nil {
			t.Errorf("%s: expected error", c.name)
		}
	}
	// A blank favorable is allowed and defaults to approve.
	e, err := h.SetConfig(ctx, id, "f1", "applicant.gender", "", 0)
	if err != nil {
		t.Fatalf("blank favorable should default: %v", err)
	}
	if e.Type != fairlending.TypeConfigSet {
		t.Fatalf("event type = %q", e.Type)
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	log := eventlog.NewMemory()
	st := store.NewMemory()
	h := fairlending.NewHandler(log).WithNow(func() time.Time { return now })

	if _, err := h.SetSettings(ctx, id, fairlending.Settings{}); err == nil {
		t.Fatal("settings without a creditor name should be rejected")
	}
	e, err := h.SetSettings(ctx, id, fairlending.Settings{
		CreditorName: "Acme Bank", CreditorAddress: "1 Main St", CreditorPhone: "555-0100",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := (fairlending.SettingsProjector{}).Apply(ctx, e, st); err != nil {
		t.Fatal(err)
	}
	v, found, err := fairlending.ReadSettings(ctx, st, id)
	if err != nil || !found {
		t.Fatalf("read settings: found=%v err=%v", found, err)
	}
	if v.CreditorName != "Acme Bank" || v.CreditorPhone != "555-0100" {
		t.Fatalf("settings = %+v", v)
	}
}

func TestNoticeRenders(t *testing.T) {
	rec := history.Record{
		DecisionID:        "dec-1",
		Status:            "completed",
		Disposition:       string(policy.Decline),
		DispositionReason: "Debt-to-income ratio above program limit",
		ReasonCodes: []history.ReasonCode{
			{Code: "DTI_TOO_HIGH", Description: "Debt-to-income ratio above program limit"},
			{Code: "UTIL_HIGH", Description: "Revolving utilization is elevated"},
		},
	}
	st := fairlending.Settings{CreditorName: "Acme Bank", CreditorAddress: "1 Main St"}
	notice, err := fairlending.Notice(rec, st, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Acme Bank",
		"Debt-to-income ratio above program limit",
		"Revolving utilization is elevated",
		"Equal Credit Opportunity Act",
		"dec-1",
	} {
		if !strings.Contains(notice, want) {
			t.Errorf("notice missing %q:\n%s", want, notice)
		}
	}
	// The DispositionReason and the identically-worded reason code are not duplicated.
	if n := strings.Count(notice, "Debt-to-income ratio above program limit"); n != 1 {
		t.Fatalf("expected the DTI reason once, got %d", n)
	}
}

func TestNoticeErrors(t *testing.T) {
	base := history.Record{
		DecisionID: "dec-1", Disposition: string(policy.Decline),
		ReasonCodes: []history.ReasonCode{{Code: "X", Description: "reason"}},
	}
	st := fairlending.Settings{CreditorName: "Acme Bank"}

	approved := base
	approved.Disposition = string(policy.Approve)
	if _, err := fairlending.Notice(approved, st, now); err == nil {
		t.Error("an approved decision has no adverse-action notice")
	}
	if _, err := fairlending.Notice(base, fairlending.Settings{}, now); err == nil {
		t.Error("a notice without a creditor should be rejected")
	}
	noReasons := base
	noReasons.ReasonCodes = nil
	noReasons.DispositionReason = ""
	if _, err := fairlending.Notice(noReasons, st, now); err == nil {
		t.Error("a notice with no reasons should be rejected")
	}
}

func TestNoticeCapsReasons(t *testing.T) {
	rec := history.Record{DecisionID: "dec-1", Disposition: string(policy.Decline)}
	for i := 0; i < 8; i++ {
		rec.ReasonCodes = append(rec.ReasonCodes, history.ReasonCode{
			Code: string(rune('A' + i)), Description: "reason " + string(rune('A'+i)),
		})
	}
	notice, err := fairlending.Notice(rec, fairlending.Settings{CreditorName: "Acme"}, now)
	if err != nil {
		t.Fatal(err)
	}
	// Reg B discloses at most four principal reasons.
	if n := strings.Count(notice, "- reason "); n != 4 {
		t.Fatalf("expected 4 principal reasons, got %d:\n%s", n, notice)
	}
}
