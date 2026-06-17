// SPDX-License-Identifier: AGPL-3.0-or-later

package privacy

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
)

// Handler is the masking-config write side (imperative shell).
type Handler struct {
	log eventlog.Log
	now func() time.Time
}

// NewHandler builds a Handler using the system clock.
func NewHandler(log eventlog.Log) *Handler {
	return &Handler{log: log, now: func() time.Time { return time.Now().UTC() }}
}

// SetFields replaces the workspace's sensitive-field list (normalized: trimmed,
// lowercased, de-duplicated, sorted). An empty list disables masking.
func (h *Handler) SetFields(ctx context.Context, id identity.Identity, fields []string) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	seen := map[string]bool{}
	norm := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.TrimSpace(strings.ToLower(f))
		if f == "" || seen[f] {
			continue
		}
		seen[f] = true
		norm = append(norm, f)
	}
	sort.Strings(norm)
	b, err := json.Marshal(FieldsSet{Fields: norm})
	if err != nil {
		return eventlog.Envelope{}, fmt.Errorf("privacy: marshal fields: %w", err)
	}
	return h.log.Append(ctx, eventlog.Envelope{
		Org: id.Org, Workspace: id.Workspace, Actor: id.Actor,
		Stream: StreamPrivacy, Type: TypeFieldsSet, Time: h.now(), Payload: b,
	})
}
