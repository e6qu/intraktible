// SPDX-License-Identifier: AGPL-3.0-or-later

// Package identity carries the org/workspace/actor scope through request context.
// Every event and projection is scoped to (Org, Workspace); see PLAN.md §3.2.
package identity

import (
	"context"
	"errors"
	"strings"
)

// Identity is the authenticated caller's tenancy + actor scope.
type Identity struct {
	Org       string `json:"org"`
	Workspace string `json:"workspace"`
	Actor     string `json:"actor"`
}

// Valid reports whether the identity is fully scoped. Tenancy is mandatory
// (org+workspace scoping from day 1); we fail loudly rather than default.
func (i Identity) Valid() error {
	switch {
	case i.Org == "":
		return errors.New("identity: missing org")
	case i.Workspace == "":
		return errors.New("identity: missing workspace")
	case i.Actor == "":
		return errors.New("identity: missing actor")
	// Tenant isolation across the store is enforced by the "org/workspace/" key
	// prefix, so a '/' in either segment could let one tenant's prefix match
	// another's keys. Reject it rather than rely on every writer to sanitize.
	case strings.ContainsRune(i.Org, '/'):
		return errors.New("identity: org must not contain '/'")
	case strings.ContainsRune(i.Workspace, '/'):
		return errors.New("identity: workspace must not contain '/'")
	default:
		return nil
	}
}

type ctxKey struct{}

// With returns a context carrying id.
func With(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// From extracts the identity; ok is false when none is present.
func From(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(ctxKey{}).(Identity)
	return id, ok
}
