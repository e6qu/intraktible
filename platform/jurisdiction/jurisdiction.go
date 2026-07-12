// SPDX-License-Identifier: AGPL-3.0-or-later

// Package jurisdiction records which data-protection / fair-lending regimes a workspace
// operates under, so subject-facing artifacts (notably the automated-decision
// explanation) cite the law that actually applies instead of hedging across every
// regime. A workspace serving EU and UK customers cites Article 22 of the EU General
// Data Protection Regulation and Articles 22A–22D of the UK Data (Use and Access) Act
// 2025; a US lender cites reconsideration under the US Equal Credit Opportunity Act.
package jurisdiction

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// Stream is the event stream for jurisdiction settings.
const Stream = "platform.jurisdiction"

// TypeSet records a replacement of the workspace's applicable regimes.
const TypeSet = "platform.jurisdiction_set"

// Collection holds the workspace jurisdiction setting (one doc).
const Collection = "jurisdiction"

const docID = "jurisdiction"

// Regime codes for the data-protection / fair-lending regimes this platform recognizes.
const (
	EU = "eu" // EU General Data Protection Regulation, Article 22
	UK = "uk" // UK Data (Use and Access) Act 2025, Articles 22A–22D
	US = "us" // US Equal Credit Opportunity Act (reconsideration)
)

// validRegime reports whether code is a recognized regime.
func validRegime(code string) bool {
	switch code {
	case EU, UK, US:
		return true
	default:
		return false
	}
}

// DefaultRegimes is what an unconfigured workspace is treated as: every regime applies,
// so an explanation hedges across all of them until the workspace narrows it.
var DefaultRegimes = []string{EU, UK, US}

// set is the event payload replacing the applicable regimes.
type set struct {
	Regimes []string `json:"regimes"`
}

// View is the stored setting with its provenance.
type View struct {
	Org       string    `json:"org"`
	Workspace string    `json:"workspace"`
	Regimes   []string  `json:"regimes"`
	UpdatedAt time.Time `json:"updated_at"`
	UpdatedBy string    `json:"updated_by"`
}

// Handler is the jurisdiction write side.
type Handler struct {
	log eventlog.Log
	now func() time.Time
}

// NewHandler builds a Handler using the system clock.
func NewHandler(log eventlog.Log) *Handler {
	return &Handler{log: log, now: func() time.Time { return time.Now().UTC() }}
}

// WithNow overrides the clock stamped into recorded events (tests, the seeder).
func (h *Handler) WithNow(now func() time.Time) *Handler {
	h.now = now
	return h
}

// Set replaces the workspace's applicable regimes, returning the normalized set it
// recorded (deduplicated, lower-cased, sorted). At least one recognized regime is
// required; an unknown code is rejected at the boundary.
func (h *Handler) Set(ctx context.Context, id identity.Identity, regimes []string) (eventlog.Envelope, []string, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, nil, err
	}
	clean, err := normalize(regimes)
	if err != nil {
		return eventlog.Envelope{}, nil, err
	}
	b, err := json.Marshal(set{Regimes: clean})
	if err != nil {
		return eventlog.Envelope{}, nil, fmt.Errorf("jurisdiction: marshal set: %w", err)
	}
	env, err := h.log.Append(ctx, eventlog.Envelope{
		Org: id.Org, Workspace: id.Workspace, Actor: id.Actor,
		Stream: Stream, Type: TypeSet, Time: h.now(), Payload: b,
	})
	return env, clean, err
}

// normalize lower-cases, validates, deduplicates, and sorts a regime list.
func normalize(regimes []string) ([]string, error) {
	seen := map[string]bool{}
	clean := make([]string, 0, len(regimes))
	for _, code := range regimes {
		code = strings.TrimSpace(strings.ToLower(code))
		if !validRegime(code) {
			return nil, fmt.Errorf("jurisdiction: unknown regime %q", code)
		}
		if !seen[code] {
			seen[code] = true
			clean = append(clean, code)
		}
	}
	if len(clean) == 0 {
		return nil, fmt.Errorf("jurisdiction: at least one regime is required")
	}
	sort.Strings(clean)
	return clean, nil
}

// Projector folds the jurisdiction stream into the per-workspace doc.
type Projector struct{}

// Name identifies the projector.
func (Projector) Name() string { return Collection }

// Collections lists the store collection this projector owns.
func (Projector) Collections() []string { return []string{Collection} }

// Apply maintains the workspace jurisdiction doc from each set event.
func (Projector) Apply(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	if e.Type != TypeSet {
		return nil
	}
	var p set
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("jurisdiction: decode set seq %d: %w", e.Seq, err)
	}
	return store.PutDoc(ctx, s, Collection, store.Key(e.Org, e.Workspace, docID),
		View{Org: e.Org, Workspace: e.Workspace, Regimes: p.Regimes, UpdatedAt: e.Time, UpdatedBy: e.Actor})
}

// Read returns the workspace setting (false when unset).
func Read(ctx context.Context, s store.Store, id identity.Identity) (View, bool, error) {
	return store.GetDoc[View](ctx, s, Collection, store.Key(id.Org, id.Workspace, docID))
}

// Applicable returns the workspace's applicable regimes, or DefaultRegimes when unset —
// the caller (e.g. the decision-explanation renderer) never has to special-case "unset".
func Applicable(ctx context.Context, s store.Store, id identity.Identity) ([]string, error) {
	v, ok, err := Read(ctx, s, id)
	if err != nil {
		return nil, err
	}
	if !ok || len(v.Regimes) == 0 {
		return DefaultRegimes, nil
	}
	return v.Regimes, nil
}
