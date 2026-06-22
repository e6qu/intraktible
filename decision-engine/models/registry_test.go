// SPDX-License-Identifier: AGPL-3.0-or-later

package models_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/decision-engine/models"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

type fakeDoer struct {
	status int
	body   string
	gotReq *http.Request
}

func (f *fakeDoer) Do(req *http.Request) (*http.Response, error) {
	f.gotReq = req
	st := f.status
	if st == 0 {
		st = http.StatusOK
	}
	return &http.Response{StatusCode: st, Body: io.NopCloser(strings.NewReader(f.body))}, nil
}

func seedModel(t *testing.T, st store.Store, id identity.Identity, name, spec string) {
	t.Helper()
	mv := models.ModelView{Org: id.Org, Workspace: id.Workspace, Name: name, Spec: json.RawMessage(spec)}
	if err := store.PutDoc(context.Background(), st, models.Collection, store.Key(id.Org, id.Workspace, name), mv); err != nil {
		t.Fatal(err)
	}
}

func TestProviderInProcessModel(t *testing.T) {
	ctx := context.Background()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	st := store.NewMemory()
	seedModel(t, st, id, "risk", `{"kind":"logistic","intercept":0,"coefficients":{"x":1}}`)

	raw, err := models.Provider{Store: st}.Predict(ctx, id, "risk", map[string]any{"x": 0})
	if err != nil {
		t.Fatal(err)
	}
	var p models.Prediction
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	if p.Probability == nil || *p.Probability != 0.5 { // sigmoid of zero is one half
		t.Fatalf("probability = %v, want 0.5", p.Probability)
	}
}

func TestProviderExternalModel(t *testing.T) {
	ctx := context.Background()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	st := store.NewMemory()
	seedModel(t, st, id, "served", `{"kind":"external","endpoint":"https://models.internal/score"}`)

	doer := &fakeDoer{body: `{"score":2.5,"probability":0.92}`}
	raw, err := models.Provider{Store: st, HTTP: doer}.Predict(ctx, id, "served", map[string]any{"fico": 700})
	if err != nil {
		t.Fatal(err)
	}
	var p models.Prediction
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	if p.Score != 2.5 || p.Probability == nil || *p.Probability != 0.92 {
		t.Fatalf("prediction = %+v", p)
	}
	// The features were POSTed as the request body.
	if doer.gotReq == nil || doer.gotReq.Method != http.MethodPost {
		t.Fatalf("expected a POST, got %+v", doer.gotReq)
	}
}

// A malformed GBM spec (a non-leaf tree with nil children) seeded directly into
// the store bypasses DefineModel's validation; Predict must reject it rather than
// nil-deref in (*Tree).eval.
func TestProviderRejectsInvalidGBMSpec(t *testing.T) {
	ctx := context.Background()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	st := store.NewMemory()
	seedModel(t, st, id, "broken", `{"kind":"gbm","trees":[{"feature":"x","threshold":1}]}`)

	if _, err := (models.Provider{Store: st}).Predict(ctx, id, "broken", map[string]any{"x": 2}); err == nil {
		t.Fatal("expected an error for an invalid GBM spec, not a panic")
	}
}

// An external model returning an out-of-range probability (or non-finite score)
// must be rejected loudly, not recorded — it would corrupt branching + drift.
func TestProviderRejectsBadExternalResult(t *testing.T) {
	ctx := context.Background()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	st := store.NewMemory()
	seedModel(t, st, id, "served", `{"kind":"external","endpoint":"https://x/score"}`)
	for _, body := range []string{`{"score":1.0,"probability":5}`, `{"score":1e999}`} {
		doer := &fakeDoer{body: body}
		if _, err := (models.Provider{Store: st, HTTP: doer}).Predict(ctx, id, "served", nil); err == nil {
			t.Fatalf("expected rejection for external result %q", body)
		}
	}
}

func TestProviderExternalWithoutClientFailsLoudly(t *testing.T) {
	ctx := context.Background()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	st := store.NewMemory()
	seedModel(t, st, id, "served", `{"kind":"external","endpoint":"https://x/score"}`)
	if _, err := (models.Provider{Store: st}).Predict(ctx, id, "served", nil); err == nil {
		t.Fatal("expected an error without an HTTP client")
	}
}

func TestProviderExternalNon200FailsLoudly(t *testing.T) {
	ctx := context.Background()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	st := store.NewMemory()
	seedModel(t, st, id, "served", `{"kind":"external","endpoint":"https://x/score"}`)
	doer := &fakeDoer{status: http.StatusBadGateway, body: "down"}
	if _, err := (models.Provider{Store: st, HTTP: doer}).Predict(ctx, id, "served", nil); err == nil {
		t.Fatal("expected an error on a non-200 response")
	}
}

func TestWindowedDriftAndMonitor(t *testing.T) {
	ctx := context.Background()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	st := store.NewMemory()

	// Baseline concentrated in bucket 5; an old day matches it, the most recent day
	// has shifted entirely to bucket 0. A monitor threshold of 0.25 is set.
	stats := models.ModelStats{
		Org: id.Org, Workspace: id.Workspace, Name: "risk",
		Hist:          models.Histogram{10, 0, 0, 0, 0, 10},
		Daily:         map[string]models.Histogram{"2026-06-01": {0, 0, 0, 0, 0, 10}, "2026-06-20": {10}},
		HasBaseline:   true,
		BaselineHist:  models.Histogram{0, 0, 0, 0, 0, 20},
		BaselineCount: 20,
		Threshold:     0.25,
	}
	if err := store.PutDoc(ctx, st, models.StatsCollection, store.Key(id.Org, id.Workspace, "risk"), stats); err != nil {
		t.Fatal(err)
	}

	// A 1-day window sees only the shifted recent day → large PSI → firing.
	win, err := models.Drift(ctx, st, id, "risk", 1)
	if err != nil {
		t.Fatal(err)
	}
	if win.WindowDays != 1 || win.Count != 10 {
		t.Fatalf("windowed report = %+v", win)
	}
	if win.PSI == nil || !win.Firing {
		t.Fatalf("expected a firing windowed PSI, got %+v", win)
	}

	// All-time dilutes the shift (half the mass still matches the baseline).
	all, err := models.Drift(ctx, st, id, "risk", 0)
	if err != nil {
		t.Fatal(err)
	}
	if all.Count != 20 {
		t.Fatalf("all-time count = %d, want 20", all.Count)
	}
	if all.PSI == nil || *all.PSI >= *win.PSI {
		t.Fatalf("expected the windowed shift to read higher than all-time: window=%v all=%v", win.PSI, all.PSI)
	}
}

// The projector records the defining actor as the model's owner, so MRM can
// surface accountability for a predictive model the same way it does for flows
// and agents — and so a replay of prior events backfills it with no migration.
func TestProjectorRecordsOwnerFromActor(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemory()
	payload, err := json.Marshal(events.ModelDefined{
		Name: "risk", Spec: json.RawMessage(`{"kind":"logistic","intercept":0,"coefficients":{"x":1}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	env := eventlog.Envelope{
		Org: "demo", Workspace: "main", Actor: "dana", Seq: 1,
		Type: events.TypeModelDefined, Payload: payload, Time: time.Now().UTC(),
	}
	if err := (models.Projector{}).Apply(ctx, env, st); err != nil {
		t.Fatal(err)
	}
	mv, ok, err := models.Read(ctx, st, identity.Identity{Org: "demo", Workspace: "main", Actor: "x"}, "risk")
	if err != nil || !ok {
		t.Fatalf("read model: ok=%v err=%v", ok, err)
	}
	if mv.Owner != "dana" {
		t.Fatalf("owner = %q, want dana", mv.Owner)
	}
}

func TestExternalSpecValidation(t *testing.T) {
	good, _ := models.ParseSpec(json.RawMessage(`{"kind":"external","endpoint":"https://x/score"}`))
	if err := good.Validate(); err != nil {
		t.Fatalf("valid external rejected: %v", err)
	}
	bad, _ := models.ParseSpec(json.RawMessage(`{"kind":"external","endpoint":"ftp://x"}`))
	if err := bad.Validate(); err == nil {
		t.Fatal("expected a non-http endpoint to be rejected")
	}
}
