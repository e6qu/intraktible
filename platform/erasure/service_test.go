// SPDX-License-Identifier: AGPL-3.0-or-later

package erasure_test

import (
	"context"
	"net/http"
	"testing"
	"time"

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

// fakeRetention is a RetentionGate that reports a fixed protection state.
type fakeRetention struct{ retained bool }

func (f fakeRetention) Retained(_ context.Context, _ identity.Identity, _ string) (bool, string, error) {
	return f.retained, "kept until 2028-01-01 (ECOA)", nil
}

func TestBulkRetentionHonorsStatutoryGate(t *testing.T) {
	log, st := testutil.NewLogStore(t)
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "admin"}
	clock := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	vault := erasure.NewVault(st).WithNow(func() time.Time { return clock.Add(-60 * 24 * time.Hour) })
	if _, err := vault.Seal(context.Background(), id, "protected", []byte("pii")); err != nil {
		t.Fatal(err)
	}
	vault.WithNow(func() time.Time { return clock })
	svc := erasure.NewService(vault).WithRetentionGate(fakeRetention{retained: true})
	api := testutil.StartAPI(t, log, st, "admin-key", id, svc.Routes)

	var sweep struct {
		Erased            int `json:"erased"`
		StatutoryRetained int `json:"statutory_retained"`
	}
	api.Request(t, http.MethodPost, "/v1/erasure/retention?max_age_days=30", nil, http.StatusOK, &sweep)
	if sweep.Erased != 0 || sweep.StatutoryRetained != 1 {
		t.Fatalf("bulk retention bypassed the statutory gate: %+v", sweep)
	}
	erased, err := vault.Erased(context.Background(), id, "protected")
	if err != nil || erased {
		t.Fatalf("protected subject erased=%v err=%v", erased, err)
	}
}

func TestErasureBlockedByRetention(t *testing.T) {
	log, st := testutil.NewLogStore(t)
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "admin"}

	// A subject still within a statutory retention window cannot be erased — 409.
	blocked := erasure.NewService(erasure.NewVault(st)).WithRetentionGate(fakeRetention{retained: true})
	api := testutil.StartAPI(t, log, st, "admin-key", id, blocked.Routes)
	api.Request(t, http.MethodPost, "/v1/erasure/subjects/ada", nil, http.StatusConflict, nil)

	// Once no record is retained, erasure proceeds.
	free := erasure.NewService(erasure.NewVault(st)).WithRetentionGate(fakeRetention{retained: false})
	api2 := testutil.StartAPI(t, log, st, "admin-key2", id, free.Routes)
	api2.Request(t, http.MethodPost, "/v1/erasure/subjects/bob", nil, http.StatusOK, nil)
}
