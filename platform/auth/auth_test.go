// SPDX-License-Identifier: AGPL-3.0-or-later

package auth_test

import (
	"testing"

	"github.com/e6qu/intraktible/platform/auth"
	"github.com/e6qu/intraktible/platform/identity"
)

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
	tok := s.Issue(id)
	if tok == "" {
		t.Fatal("Issue must return a non-empty token")
	}
	if tok2 := s.Issue(id); tok2 == tok {
		t.Fatal("each Issue must return a distinct token")
	}
	got, ok := s.Resolve(tok)
	if !ok || got != id {
		t.Fatalf("Resolve: got %+v ok=%v, want %+v true", got, ok, id)
	}
	if _, ok := s.Resolve("nope"); ok {
		t.Fatal("unknown token must not resolve")
	}
}
