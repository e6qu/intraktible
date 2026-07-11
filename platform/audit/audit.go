// SPDX-License-Identifier: AGPL-3.0-or-later

// Package audit is the immutable audit surface: a tenant-scoped, filterable read
// over the append-only event log that answers "who did what, when". The log
// already records every state change with an actor, timestamp, stream, and
// payload; this package exposes that as a first-class, exportable query rather
// than leaving it to the operator-only `intraktible log` CLI.
package audit

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// Collection holds the indexed audit entries. Instead of folding the whole (multi-
// tenant) event log on every audit query, a projector writes one compact Entry per
// event keyed by (org, workspace, seq); a query then does an INDEXED prefix range
// scan of just the caller's tenant — the O(n)→indexed fix for large logs.
const Collection = "audit_entries"

// Entry is one audit record: an event projected to its accountability fields.
type Entry struct {
	Seq       uint64          `json:"seq"`
	Org       string          `json:"org"`
	Workspace string          `json:"workspace"`
	ID        string          `json:"id"`
	Time      time.Time       `json:"time"`
	Actor     string          `json:"actor"`
	Stream    string          `json:"stream"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

// Projector folds every event into the audit index. It owns no domain state — it is
// a pure re-indexing of the log — so it fits any module set and rebuilds from seq 0.
type Projector struct{}

// Name identifies the projector.
func (Projector) Name() string { return "audit_entries" }

// Collections lists the store collection this projector owns.
func (Projector) Collections() []string { return []string{Collection} }

// Apply writes one audit Entry per event. Idempotent by key (seq), so a replay
// rewrites the same doc.
func (Projector) Apply(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	entry := Entry{
		Seq: e.Seq, Org: e.Org, Workspace: e.Workspace, ID: e.ID, Time: e.Time,
		Actor: e.Actor, Stream: e.Stream, Type: e.Type, Payload: e.Payload,
	}
	return store.PutDoc(ctx, s, Collection, entryKey(e.Org, e.Workspace, e.Seq), entry)
}

// entryKey keys an entry under its tenant with a zero-padded seq, so the store's
// lexical key order matches numeric seq order (newest-first by reverse scan).
func entryKey(org, workspace string, seq uint64) string {
	return store.Key(org, workspace, fmt.Sprintf("%020d", seq))
}

// Query filters an audit read. Zero-value fields are not applied; Since/Until are
// inclusive time bounds; Resource keeps only events whose payload references the
// given id. Limit caps the result to the most recent N matches.
type Query struct {
	Stream      string
	Actor       string
	Type        string
	Resource    string
	Since       time.Time
	Until       time.Time
	ExcludeType string // drop events of this type (e.g. the high-volume node-evaluated noise)
	Limit       int
	Offset      int
}

// Page is one page of audit entries plus the total matching the filter (before
// limit/offset), so the UI can paginate.
type Page struct {
	Entries []Entry `json:"entries"`
	Total   int     `json:"total"`
	Limit   int     `json:"limit"`
	Offset  int     `json:"offset"`
}

const (
	// DefaultLimit is the result cap when a query omits one.
	DefaultLimit = 200
	// MaxLimit bounds how many records a single read returns.
	MaxLimit = 1000
)

// Read returns the caller's tenant's matching audit entries, most-recent first,
// capped at the (clamped) query limit. It reads the indexed audit projection —
// a tenant-prefixed range scan of pre-derived entries — not a fold of the whole log.
func Read(ctx context.Context, s store.Store, id identity.Identity, q Query) ([]Entry, error) {
	out, err := matched(ctx, s, id, q)
	if err != nil {
		return nil, err
	}
	if limit := clampLimit(q.Limit); len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// ReadPage is Read with offset+total, for the paginated UI. Total is the full
// match count (before limit/offset); Entries is the requested window.
func ReadPage(ctx context.Context, s store.Store, id identity.Identity, q Query) (Page, error) {
	all, err := matched(ctx, s, id, q)
	if err != nil {
		return Page{}, err
	}
	total := len(all)
	limit := clampLimit(q.Limit)
	lo := q.Offset
	if lo < 0 {
		lo = 0
	}
	if lo > total {
		lo = total
	}
	hi := lo + limit
	if hi > total {
		hi = total
	}
	return Page{Entries: all[lo:hi], Total: total, Limit: limit, Offset: lo}, nil
}

// matched reads the caller's tenant's audit entries from the index (an indexed
// prefix scan, not a whole-log fold) and returns those matching q, most-recent first.
func matched(ctx context.Context, s store.Store, id identity.Identity, q Query) ([]Entry, error) {
	return store.QueryDocs(ctx, s, Collection, store.Key(id.Org, id.Workspace, ""),
		func(e Entry) bool { return match(e, q) },
		func(a, b Entry) bool { return a.Seq > b.Seq })
}

func clampLimit(n int) int {
	switch {
	case n <= 0:
		return DefaultLimit
	case n > MaxLimit:
		return MaxLimit
	default:
		return n
	}
}

func match(e Entry, q Query) bool {
	switch {
	case q.Stream != "" && e.Stream != q.Stream:
		return false
	case q.Actor != "" && e.Actor != q.Actor:
		return false
	case q.Type != "" && e.Type != q.Type:
		return false
	case q.ExcludeType != "" && e.Type == q.ExcludeType:
		return false
	case !q.Since.IsZero() && e.Time.Before(q.Since):
		return false
	case !q.Until.IsZero() && e.Time.After(q.Until):
		return false
	case q.Resource != "" && !payloadReferences(e.Payload, q.Resource):
		return false
	default:
		return true
	}
}

// payloadReferences reports whether the event payload references id as any JSON
// string value (a flow_id / case_id / decision_id / …) — the generic way to
// scope the trail to one resource without knowing each event type's schema.
func payloadReferences(payload json.RawMessage, id string) bool {
	var v any
	if err := json.Unmarshal(payload, &v); err != nil {
		return false
	}
	return jsonContainsValue(v, id)
}

func jsonContainsValue(v any, id string) bool {
	switch t := v.(type) {
	case string:
		return t == id
	case []any:
		for _, e := range t {
			if jsonContainsValue(e, id) {
				return true
			}
		}
	case map[string]any:
		for _, e := range t {
			if jsonContainsValue(e, id) {
				return true
			}
		}
	}
	return false
}

// CSV renders entries as a CSV document (header + one row per entry) for a
// self-contained export; the payload is kept as a JSON-string column.
func CSV(entries []Entry) (string, error) {
	var b strings.Builder
	w := csv.NewWriter(&b)
	_ = w.Write([]string{"seq", "time", "actor", "stream", "type", "payload"})
	for _, e := range entries {
		_ = w.Write([]string{
			strconv.FormatUint(e.Seq, 10),
			e.Time.UTC().Format(time.RFC3339),
			csvSafe(e.Actor), csvSafe(e.Stream), csvSafe(e.Type), csvSafe(string(e.Payload)),
		})
	}
	w.Flush()
	return b.String(), w.Error()
}

// csvSafe defuses spreadsheet formula injection: a cell whose first character is
// one a spreadsheet treats as a formula (=, +, -, @) or a leading control char
// is prefixed with a single quote so it is read as literal text. encoding/csv
// already escapes delimiters and quotes, but not this.
func csvSafe(s string) string {
	if s == "" {
		return s
	}
	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + s
	}
	return s
}
