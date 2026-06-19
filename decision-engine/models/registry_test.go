// SPDX-License-Identifier: AGPL-3.0-or-later

package models_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/e6qu/intraktible/decision-engine/models"
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
