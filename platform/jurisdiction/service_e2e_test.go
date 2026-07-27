// SPDX-License-Identifier: AGPL-3.0-or-later

package jurisdiction_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/jurisdiction"
	"github.com/e6qu/intraktible/platform/store"
	"github.com/e6qu/intraktible/platform/testutil"
)

// The workspace's operating jurisdiction decides which law an automated-decision
// explanation cites, so it is load-bearing for a compliance artifact rather than a
// display preference. It had domain tests but no coverage of the HTTP surface — and
// the surface is where the two behaviors that matter live: the fail-safe default
// applied to an unconfigured workspace, and the refusal of an unrecognized regime.

func startJurisdictionAPI(t *testing.T) *testutil.API {
	t.Helper()
	log := eventlog.NewMemory()
	st := store.NewMemory()
	h := jurisdiction.NewHandler(log).WithNow(func() time.Time { return t0 })
	svc := jurisdiction.New(h, st)
	return testutil.StartAPI(t, log, st, "k", id, svc.Routes, jurisdiction.Projector{})
}

type jurisdictionView struct {
	Regimes    []string `json:"regimes"`
	Configured bool     `json:"configured"`
}

// An unconfigured workspace must report EVERY regime as applicable, not none.
// Defaulting to the empty set would let an explanation cite no law at all and read
// as compliant; defaulting to all of them is the fail-safe direction, and it is
// reported as `configured: false` so the UI can still prompt for a real answer.
func TestJurisdictionDefaultsToEveryRegimeOverHTTP(t *testing.T) {
	api := startJurisdictionAPI(t)

	var got jurisdictionView
	api.Request(t, http.MethodGet, "/v1/compliance/jurisdiction", nil, http.StatusOK, &got)

	if got.Configured {
		t.Fatal("configured = true on a workspace that has never been set")
	}
	if len(got.Regimes) != len(jurisdiction.DefaultRegimes) {
		t.Fatalf("regimes = %v, want every regime (%v) for an unconfigured workspace",
			got.Regimes, jurisdiction.DefaultRegimes)
	}
}

func TestJurisdictionSetAndReadOverHTTP(t *testing.T) {
	api := startJurisdictionAPI(t)

	api.Request(t, http.MethodPut, "/v1/compliance/jurisdiction",
		map[string]any{"regimes": []string{"us"}}, http.StatusOK, nil)

	var got jurisdictionView
	api.Request(t, http.MethodGet, "/v1/compliance/jurisdiction", nil, http.StatusOK, &got)
	if !got.Configured {
		t.Fatal("configured = false after an explicit set")
	}
	if len(got.Regimes) != 1 || got.Regimes[0] != jurisdiction.US {
		t.Fatalf("regimes = %v, want just [us]", got.Regimes)
	}
}

// An unrecognized regime must be refused rather than stored. Accepting it would put
// a value in the config that no explanation can cite, so the workspace would look
// configured while producing the same output as unconfigured.
func TestJurisdictionRefusesUnknownRegimeOverHTTP(t *testing.T) {
	api := startJurisdictionAPI(t)

	api.Request(t, http.MethodPut, "/v1/compliance/jurisdiction",
		map[string]any{"regimes": []string{"atlantis"}}, http.StatusBadRequest, nil)

	// And the refusal left nothing behind.
	var got jurisdictionView
	api.Request(t, http.MethodGet, "/v1/compliance/jurisdiction", nil, http.StatusOK, &got)
	if got.Configured {
		t.Fatal("a refused set still marked the workspace configured")
	}
}

// Clearing the list is not a way to end up citing no law: an empty set falls back to
// the same fail-safe default as never having configured one.
func TestJurisdictionEmptySetFallsBackToDefaultOverHTTP(t *testing.T) {
	api := startJurisdictionAPI(t)

	status := api.RequestStatus(t, http.MethodPut, "/v1/compliance/jurisdiction",
		map[string]any{"regimes": []string{}}, nil)

	var got jurisdictionView
	api.Request(t, http.MethodGet, "/v1/compliance/jurisdiction", nil, http.StatusOK, &got)
	if len(got.Regimes) == 0 {
		t.Fatalf("an empty set (PUT returned %d) left no applicable regime — an explanation would cite no law", status)
	}
}
