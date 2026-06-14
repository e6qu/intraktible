// SPDX-License-Identifier: AGPL-3.0-or-later

// Package stats is the hello feature's read model: a projector that folds
// HelloRecorded events into per-(org,workspace) counters.
package stats

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/e6qu/intraktible/hello/events"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// Collection is the store collection holding hello stats.
const Collection = "hello_stats"

// Stats is the materialized read model for one tenant.
type Stats struct {
	Org       string    `json:"org"`
	Workspace string    `json:"workspace"`
	Count     int       `json:"count"`
	LastName  string    `json:"last_name"`
	LastAt    time.Time `json:"last_at"`
}

// Projector folds hello events into Stats.
type Projector struct{}

// Name identifies the projector.
func (Projector) Name() string { return "hello_stats" }

// Apply updates the per-tenant counter. Events of other types are not this
// projector's concern and are skipped (correct routing, not error-swallowing).
func (Projector) Apply(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	if e.Type != events.TypeHelloRecorded {
		return nil
	}
	var hr events.HelloRecorded
	if err := json.Unmarshal(e.Payload, &hr); err != nil {
		return fmt.Errorf("hello_stats: unmarshal seq %d: %w", e.Seq, err)
	}
	key := store.Key(e.Org, e.Workspace, "stats")
	var st Stats
	if doc, ok, err := s.Get(ctx, Collection, key); err != nil {
		return err
	} else if ok {
		if err := json.Unmarshal(doc, &st); err != nil {
			return fmt.Errorf("hello_stats: decode existing: %w", err)
		}
	}
	st.Org, st.Workspace = e.Org, e.Workspace
	st.Count++
	st.LastName, st.LastAt = hr.Name, e.Time
	doc, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("hello_stats: marshal: %w", err)
	}
	return s.Put(ctx, Collection, key, doc)
}

// Read returns the current stats for id's tenant (zero value when none yet).
func Read(ctx context.Context, s store.Store, id identity.Identity) (Stats, error) {
	st := Stats{Org: id.Org, Workspace: id.Workspace}
	doc, ok, err := s.Get(ctx, Collection, store.Key(id.Org, id.Workspace, "stats"))
	if err != nil || !ok {
		return st, err
	}
	if err := json.Unmarshal(doc, &st); err != nil {
		return Stats{}, fmt.Errorf("hello_stats: decode: %w", err)
	}
	return st, nil
}
