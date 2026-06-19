// SPDX-License-Identifier: AGPL-3.0-or-later

package erasure_test

import (
	"net/http"
	"testing"

	"github.com/e6qu/intraktible/platform/erasure"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/testutil"
)

func TestErasureServiceOverHTTP(t *testing.T) {
	log, st := testutil.NewLogStore(t)
	svc := erasure.NewService(erasure.NewVault(st))
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "admin"}
	api := testutil.StartAPI(t, log, st, "admin-key", id, svc.Routes)

	// A fresh subject is not erased.
	var status struct {
		Subject string `json:"subject"`
		Erased  bool   `json:"erased"`
	}
	api.Request(t, http.MethodGet, "/v1/erasure/subjects/ada", nil, http.StatusOK, &status)
	if status.Erased {
		t.Fatal("subject should not be erased initially")
	}

	// Fulfil a right-to-erasure request.
	api.Request(t, http.MethodPost, "/v1/erasure/subjects/ada", nil, http.StatusOK, nil)
	api.Request(t, http.MethodGet, "/v1/erasure/subjects/ada", nil, http.StatusOK, &status)
	if !status.Erased {
		t.Fatal("subject should be erased after the request")
	}

	var listed struct {
		Erased []string `json:"erased"`
	}
	api.Request(t, http.MethodGet, "/v1/erasure/subjects", nil, http.StatusOK, &listed)
	if len(listed.Erased) != 1 || listed.Erased[0] != "ada" {
		t.Fatalf("erased list = %v", listed.Erased)
	}

	// Retention needs a positive max_age_days.
	api.Request(t, http.MethodPost, "/v1/erasure/retention", nil, http.StatusBadRequest, nil)
	var sweep struct {
		Erased int `json:"erased"`
	}
	api.Request(t, http.MethodPost, "/v1/erasure/retention?max_age_days=30", nil, http.StatusOK, &sweep)
	if sweep.Erased != 0 {
		t.Fatalf("fresh subjects should survive a 30-day retention sweep, erased=%d", sweep.Erased)
	}
}
