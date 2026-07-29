// SPDX-License-Identifier: AGPL-3.0-or-later

package fairlending_test

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/intraktible/decision-engine/history"
	"github.com/e6qu/intraktible/decision-engine/policy"
	"github.com/e6qu/intraktible/fairlending"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
	"github.com/e6qu/intraktible/platform/testutil"
)

func downloadArtifact(t *testing.T, api *testutil.API, path string) string {
	t.Helper()
	// Any harness request first waits for the live projector watermark.
	api.Request(t, http.MethodGet, "/v1/adverse-actions", nil, http.StatusOK, nil)
	req, err := http.NewRequest(http.MethodGet, api.Server.URL+path, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Api-Key", api.Key)
	resp, err := api.Server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET %s -> %d: %s", path, resp.StatusCode, body)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestIssuedArtifactSurvivesSettingsChangesAndReplay(t *testing.T) {
	now := time.Date(2026, 7, 29, 9, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	log, st := testutil.NewLogStore(t)
	id := identity.Identity{Org: "acme", Workspace: "risk", Actor: "operator"}
	cmd := fairlending.NewHandler(log).WithNow(clock)
	svc := fairlending.New(cmd, st).WithNow(clock)
	if err := store.PutDoc(t.Context(), st, history.Collection, store.Key(id.Org, id.Workspace, "dec-1"), history.Record{
		Org: id.Org, Workspace: id.Workspace, DecisionID: "dec-1", FlowID: "flow-1", Slug: "underwrite",
		Status: "completed", Disposition: string(policy.Decline), EntityType: "applicant", EntityID: "APP-1",
		DispositionReason: "Debt-to-income ratio above program limit",
		ReasonCodes:       []history.ReasonCode{{Code: "DTI", Description: "Debt-to-income ratio above program limit"}},
		EndedAt:           now,
	}); err != nil {
		t.Fatal(err)
	}
	api := testutil.StartAPI(t, log, st, "operator-key", id, svc.Routes,
		fairlending.SettingsProjector{}, fairlending.IssuanceProjector{})

	api.Request(t, http.MethodPut, "/v1/fairlending/settings", fairlending.Settings{
		CreditorName: "Original Bank", CreditorAddress: "1 First Street",
	}, http.StatusOK, nil)
	previewAtIssue := downloadArtifact(t, api, "/v1/decisions/dec-1/adverse-action")
	api.Request(t, http.MethodPost, "/v1/decisions/dec-1/adverse-action/issue", map[string]any{
		"method": "mail", "based_on_consumer_report": false,
	}, http.StatusOK, nil)
	issued := downloadArtifact(t, api, "/v1/decisions/dec-1/adverse-action/issued")
	if issued != previewAtIssue {
		t.Fatalf("issued artifact differs from served preview\npreview:\n%s\nissued:\n%s", previewAtIssue, issued)
	}

	var rendered struct {
		Issuance fairlending.IssuanceView `json:"issuance"`
	}
	api.Request(t, http.MethodGet, "/v1/decisions/dec-1/adverse-action?format=json", nil, http.StatusOK, &rendered)
	hash, algo := fairlending.HashNotice(issued)
	if rendered.Issuance.ContentHash != hash || rendered.Issuance.HashAlgo != algo {
		t.Fatalf("issuance hash = %s/%s, artifact hash = %s/%s",
			rendered.Issuance.HashAlgo, rendered.Issuance.ContentHash, algo, hash)
	}

	now = now.AddDate(0, 0, 1)
	api.Request(t, http.MethodPut, "/v1/fairlending/settings", fairlending.Settings{
		CreditorName: "Renamed Bank", CreditorAddress: "99 New Street",
	}, http.StatusOK, nil)
	current := downloadArtifact(t, api, "/v1/decisions/dec-1/adverse-action")
	if current == issued || !strings.Contains(current, "Renamed Bank") || !strings.Contains(current, "2026-07-30") {
		t.Fatalf("current preview did not reflect changed settings/clock:\n%s", current)
	}
	if got := downloadArtifact(t, api, "/v1/decisions/dec-1/adverse-action/issued"); got != issued {
		t.Fatalf("issued artifact changed after settings update:\n%s", got)
	}

	// A fresh projection store rebuilt solely from the append-only log reproduces
	// the same artifact; it does not need the original mutable settings snapshot.
	replayed := store.NewMemory()
	replaySvc := fairlending.New(fairlending.NewHandler(log).WithNow(clock), replayed).WithNow(clock)
	replayAPI := testutil.StartAPI(t, log, replayed, "replay-key", id, replaySvc.Routes,
		fairlending.SettingsProjector{}, fairlending.IssuanceProjector{})
	if got := downloadArtifact(t, replayAPI, "/v1/decisions/dec-1/adverse-action/issued"); got != issued {
		t.Fatalf("replayed issued artifact changed:\n%s", got)
	}

	artifact, found, err := fairlending.ReadIssuedArtifact(t.Context(), st, id, "dec-1")
	if err != nil || !found {
		t.Fatalf("read artifact for integrity test: found=%v err=%v", found, err)
	}
	artifact.Artifact += "\ntampered"
	if err := store.PutDoc(t.Context(), st, fairlending.ArtifactCollection,
		store.Key(id.Org, id.Workspace, "dec-1"), artifact); err != nil {
		t.Fatal(err)
	}
	api.Request(t, http.MethodGet, "/v1/decisions/dec-1/adverse-action/issued",
		nil, http.StatusInternalServerError, nil)
}
