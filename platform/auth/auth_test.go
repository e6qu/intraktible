// SPDX-License-Identifier: AGPL-3.0-or-later

package auth_test

import (
	"testing"

	"github.com/e6qu/intraktible/platform/auth"
	"github.com/e6qu/intraktible/platform/identity"
)

func TestScopeAllows(t *testing.T) {
	cases := []struct {
		scope auth.Scope
		env   string
		want  bool
	}{
		{auth.Sandbox, "sandbox", true},
		{auth.Sandbox, "production", false},
		{auth.Production, "production", true},
		{auth.Production, "sandbox", false},
		{auth.ScopeAll, "sandbox", true},
		{auth.ScopeAll, "production", true},
		{"", "production", false}, // empty scope grants nothing (fail closed)
		{"", "sandbox", false},
		{"dev/*", "dev/pr-12", true},
		{"dev/*", "production", false},
	}
	for _, c := range cases {
		if got := c.scope.Allows(c.env); got != c.want {
			t.Errorf("Scope(%q).Allows(%q) = %v, want %v", c.scope, c.env, got, c.want)
		}
	}
}

func TestValidScope(t *testing.T) {
	for _, s := range []auth.Scope{auth.Sandbox, auth.Production, auth.ScopeAll, "dev/*"} {
		if !auth.ValidScope(s) {
			t.Errorf("ValidScope(%q) = false, want true", s)
		}
	}
	for _, s := range []auth.Scope{"", "bogus", "prod"} {
		if auth.ValidScope(s) {
			t.Errorf("ValidScope(%q) = true, want false", s)
		}
	}
}

func TestKeyringResolve(t *testing.T) {
	kr := auth.NewKeyring()
	want := auth.APIKey{
		ID:       "k1",
		Identity: identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"},
		Scope:    auth.Sandbox,
	}
	kr.Add("secret-123", want)

	got, ok := kr.Resolve("secret-123")
	if !ok {
		t.Fatal("expected to resolve a registered secret")
	}
	if got != want {
		t.Fatalf("resolved %+v, want %+v", got, want)
	}
	if _, ok := kr.Resolve("wrong"); ok {
		t.Fatal("unknown secret must not resolve")
	}
	if _, ok := kr.Resolve(""); ok {
		t.Fatal("empty secret must not resolve")
	}
}

func TestSessionsIssueResolve(t *testing.T) {
	s := auth.NewSessions()
	id := identity.Identity{Org: "o", Workspace: "w", Actor: "u"}
	tok, err := s.Issue(id, auth.RoleEditor)
	if err != nil || tok == "" {
		t.Fatalf("Issue must return a non-empty token: tok=%q err=%v", tok, err)
	}
	if tok2, _ := s.Issue(id, auth.RoleEditor); tok2 == tok {
		t.Fatal("each Issue must return a distinct token")
	}
	got, _, ok := s.Resolve(tok)
	if !ok || got != id {
		t.Fatalf("Resolve: got %+v ok=%v, want %+v true", got, ok, id)
	}
	if _, _, ok := s.Resolve("nope"); ok {
		t.Fatal("unknown token must not resolve")
	}
}
