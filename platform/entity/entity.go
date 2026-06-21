// SPDX-License-Identifier: AGPL-3.0-or-later

// Package entity holds the branded identifier for a decision's subject — the
// (type, id) pair that points at a Context Layer entity. It lives in platform so
// both the decision engine and the context layer can depend on it without an
// upward import, and the two fields are DISTINCT named types so a caller cannot
// transpose them: passing an ID where a Type is expected fails to compile. A
// transposed pair would otherwise silently produce a wrong-but-valid store key
// (wrong features injected, wrong pre-approval honored, wrong PII subject sealed).
package entity

// Type is an entity's kind (e.g. "applicant", "merchant"); ID is its identifier
// within that kind. Both are plain strings on the wire.
type (
	Type string
	ID   string
)

// Ref is a subject reference: an entity's (type, id). The zero Ref means "no
// subject" (no features/pre-approval/PII sealing applies).
type Ref struct {
	Type Type
	ID   ID
}

// Empty reports whether the ref does not point at an entity (either part blank).
func (r Ref) Empty() bool { return r.Type == "" || r.ID == "" }

// Key joins the ref into the "type/id" string used as a store-key segment and the
// PII-sealing subject. Centralizing it keeps every caller's join identical.
func (r Ref) Key() string { return string(r.Type) + "/" + string(r.ID) }
