// SPDX-License-Identifier: AGPL-3.0-or-later

package command_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/e6qu/intraktible/decision-engine/command"
	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/decision-engine/models"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

var modelSpec = json.RawMessage(`{"kind":"logistic","intercept":0,"coefficients":{"x":1}}`)

func idFor(actor string) identity.Identity {
	return identity.Identity{Org: "o", Workspace: "w", Actor: actor}
}

// foldModels rebuilds the model registry read model from the log, so a test can
// assert the governance state the projector materializes.
func foldModels(t *testing.T, log eventlog.Log, st store.Store) {
	t.Helper()
	evs, err := log.Read(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	p := models.Projector{}
	for _, e := range evs {
		if err := p.Apply(context.Background(), e, st); err != nil {
			t.Fatal(err)
		}
	}
}

func readModel(t *testing.T, st store.Store, name string) models.ModelView {
	t.Helper()
	mv, ok, err := models.Read(context.Background(), st, idFor("any"), name)
	if err != nil || !ok {
		t.Fatalf("read model %q: ok=%v err=%v", name, ok, err)
	}
	return mv
}

func TestModelFourEyesApproval(t *testing.T) {
	ctx := context.Background()
	log := eventlog.NewMemory()
	h := command.NewHandler(log)
	ava, marcus := idFor("ava"), idFor("marcus")

	if _, err := h.DefineModel(ctx, ava, "m", modelSpec); err != nil {
		t.Fatal(err)
	}
	reqID, _, err := h.RequestModelApproval(ctx, ava, "m")
	if err != nil {
		t.Fatal(err)
	}

	// A second request while one is pending is refused.
	if _, _, err := h.RequestModelApproval(ctx, ava, "m"); err == nil {
		t.Fatal("expected a second approval request to be refused")
	}
	// The requester (also the author) cannot approve their own model — four-eyes.
	if _, err := h.ApproveModelApproval(ctx, ava, "m", reqID, ""); err == nil {
		t.Fatal("expected four-eyes to block self-approval")
	}
	// A different, adequately-roled actor approves.
	if _, err := h.ApproveModelApproval(ctx, marcus, "m", reqID, ""); err != nil {
		t.Fatal(err)
	}

	st := store.NewMemory()
	foldModels(t, log, st)
	mv := readModel(t, st, "m")
	if !mv.Approved() || mv.Version != 1 || mv.ApprovedVersion != 1 || mv.ApprovedBy != "marcus" {
		t.Fatalf("model view after approval = %+v", mv)
	}

	// The provider's serving gate agrees.
	approved, err := (models.Provider{Store: st}).ApprovedForServing(ctx, ava, "m")
	if err != nil || !approved {
		t.Fatalf("ApprovedForServing = %v, %v", approved, err)
	}
}

func TestModelRedefineInvalidatesApproval(t *testing.T) {
	ctx := context.Background()
	log := eventlog.NewMemory()
	h := command.NewHandler(log)
	ava, marcus := idFor("ava"), idFor("marcus")

	h.DefineModel(ctx, ava, "m", modelSpec)
	reqID, _, _ := h.RequestModelApproval(ctx, ava, "m")
	h.ApproveModelApproval(ctx, marcus, "m", reqID, "")

	// Redefining the model is a new version; the old approval no longer applies.
	if _, err := h.DefineModel(ctx, ava, "m", modelSpec); err != nil {
		t.Fatal(err)
	}
	st := store.NewMemory()
	foldModels(t, log, st)
	mv := readModel(t, st, "m")
	if mv.Version != 2 || mv.ApprovedVersion != 1 || mv.Approved() {
		t.Fatalf("redefine should invalidate approval: %+v", mv)
	}
	approved, _ := (models.Provider{Store: st}).ApprovedForServing(ctx, ava, "m")
	if approved {
		t.Fatal("a redefined (unapproved) version must not be servable")
	}
}

func TestModelApprovalAuthorCannotApprove(t *testing.T) {
	ctx := context.Background()
	log := eventlog.NewMemory()
	h := command.NewHandler(log)
	ava, priya := idFor("ava"), idFor("priya")

	h.DefineModel(ctx, ava, "m", modelSpec)
	// priya requests approval of ava's model; ava (the author) still cannot approve.
	reqID, _, err := h.RequestModelApproval(ctx, priya, "m")
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.ApproveModelApproval(ctx, ava, "m", reqID, "")
	if err == nil || !strings.Contains(err.Error(), "authored") {
		t.Fatalf("expected the author to be blocked from approving, got %v", err)
	}
}

func TestModelReject(t *testing.T) {
	ctx := context.Background()
	log := eventlog.NewMemory()
	h := command.NewHandler(log)
	ava, marcus := idFor("ava"), idFor("marcus")

	h.DefineModel(ctx, ava, "m", modelSpec)
	reqID, _, _ := h.RequestModelApproval(ctx, ava, "m")
	if _, err := h.RejectModelApproval(ctx, marcus, "m", reqID, "insufficient validation"); err != nil {
		t.Fatal(err)
	}
	st := store.NewMemory()
	foldModels(t, log, st)
	mv := readModel(t, st, "m")
	if mv.Approved() || mv.Pending != nil {
		t.Fatalf("after reject: approved=%v pending=%+v", mv.Approved(), mv.Pending)
	}
	// A fresh request can be raised after a rejection.
	if _, _, err := h.RequestModelApproval(ctx, ava, "m"); err != nil {
		t.Fatalf("should be able to re-request after reject: %v", err)
	}
}

func TestRecordModelValidation(t *testing.T) {
	ctx := context.Background()
	log := eventlog.NewMemory()
	h := command.NewHandler(log)
	ava := idFor("ava")

	h.DefineModel(ctx, ava, "m", modelSpec)
	if _, err := h.RecordModelValidation(ctx, ava, "m", events.ModelValidationRecorded{
		Dataset: "backtest", Metrics: map[string]float64{"auc": 0.8}, Validator: "val", Passed: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.RecordModelValidation(ctx, ava, "missing", events.ModelValidationRecorded{}); err == nil {
		t.Fatal("validation on an unknown model should fail")
	}
	st := store.NewMemory()
	foldModels(t, log, st)
	mv := readModel(t, st, "m")
	if len(mv.Validations) != 1 || mv.Validations[0].Metrics["auc"] != 0.8 || mv.Validations[0].Version != 1 {
		t.Fatalf("validations = %+v", mv.Validations)
	}
}
