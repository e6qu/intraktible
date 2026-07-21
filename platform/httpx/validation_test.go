// SPDX-License-Identifier: AGPL-3.0-or-later

package httpx_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/e6qu/intraktible/platform/auth"
	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/identity"
)

func TestValidationHandlerRedirectsAnonymousAndNonSessionCredentials(t *testing.T) {
	t.Setenv("APPLICATION_RELEASE_REVISION", "0123456789ab")
	handler := httpx.ValidationHandler(auth.NewSessions())
	for name, decorate := range map[string]func(*http.Request){
		"anonymous": func(*http.Request) {},
		"api key": func(r *http.Request) {
			r.Header.Set("X-Api-Key", "validator-secret-must-not-be-accepted")
		},
		"bearer": func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer validator-secret-must-not-be-accepted")
		},
		"basic": func(r *http.Request) {
			r.SetBasicAuth("validator", "validator-secret-must-not-be-accepted")
		},
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/auth/validation", http.NoBody)
			decorate(req)
			rec := httptest.NewRecorder()
			handler(rec, req)
			if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/v1/auth/signed-out" {
				t.Fatalf("validation without application session -> %d location=%q", rec.Code, rec.Header().Get("Location"))
			}
			if rec.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("Cache-Control = %q, want no-store", rec.Header().Get("Cache-Control"))
			}
		})
	}
}

func TestValidationHandlerRendersVerifiedIdentityRoleRevisionAndLogout(t *testing.T) {
	t.Setenv("APPLICATION_RELEASE_REVISION", "0123456789abcdef")
	for _, tc := range []struct {
		name string
		role auth.Role
		want string
	}{
		{name: "non-admin is developer", role: auth.RoleApprover, want: "developer"},
		{name: "admin is admin", role: auth.RoleAdmin, want: "admin"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sessions := auth.NewSessions()
			token, err := sessions.Issue(identity.Identity{
				Org: "e6qu", Workspace: "dev", Actor: "ada@example.test",
				Username: `ada<script>`, Email: `ada+validation@example.test`,
			}, tc.role, auth.ScopeAll)
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodGet, "/auth/validation", http.NoBody)
			req.AddCookie(&http.Cookie{Name: "session", Value: token})
			rec := httptest.NewRecorder()
			httpx.ValidationHandler(sessions)(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("validation -> %d body=%s", rec.Code, rec.Body.String())
			}
			body := rec.Body.String()
			for _, exact := range []string{
				`data-testid="validation-username">ada&lt;script&gt;</dd>`,
				`data-testid="validation-email">ada&#43;validation@example.test</dd>`,
				`data-testid="validation-role">` + tc.want + `</dd>`,
				`data-testid="validation-release">0123456789abcdef</dd>`,
				`<form id="validation-sign-out" action="/v1/logout" method="post">`,
				`<button type="submit">Sign out</button>`,
			} {
				if !strings.Contains(body, exact) {
					t.Errorf("validation page omitted %q", exact)
				}
			}
			if strings.Contains(body, `ada<script>`) {
				t.Fatal("validation page emitted unescaped identity claims")
			}
			if got := rec.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
				t.Fatalf("Content-Type = %q", got)
			}
		})
	}
}

func TestValidationHandlerFailsLoudWithoutVerifiedProfileOrRevision(t *testing.T) {
	for _, tc := range []struct {
		name     string
		revision string
		id       identity.Identity
	}{
		{name: "missing profile", revision: "0123456789ab", id: identity.Identity{Org: "e6qu", Workspace: "dev", Actor: "actor"}},
		{name: "missing immutable revision", revision: "unknown", id: identity.Identity{Org: "e6qu", Workspace: "dev", Actor: "actor", Username: "ada", Email: "ada@example.test"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("APPLICATION_RELEASE_REVISION", tc.revision)
			sessions := auth.NewSessions()
			token, err := sessions.Issue(tc.id, auth.RoleAdmin, auth.ScopeAll)
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodGet, "/auth/validation", http.NoBody)
			req.AddCookie(&http.Cookie{Name: "session", Value: token})
			rec := httptest.NewRecorder()
			httpx.ValidationHandler(sessions)(rec, req)
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("incomplete validation metadata -> %d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}
