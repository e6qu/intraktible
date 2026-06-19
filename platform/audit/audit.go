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
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
)

// Entry is one audit record: an event projected to its accountability fields.
type Entry struct {
	Seq     uint64          `json:"seq"`
	ID      string          `json:"id"`
	Time    time.Time       `json:"time"`
	Actor   string          `json:"actor"`
	Stream  string          `json:"stream"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Query filters an audit read. Zero-value fields are not applied; Since/Until are
// inclusive time bounds; Resource keeps only events whose payload references the
// given id. Limit caps the result to the most recent N matches.
type Query struct {
	Stream   string
	Actor    string
	Type     string
	Resource string
	Since    time.Time
	Until    time.Time
	Limit    int
}

const (
	// DefaultLimit is the result cap when a query omits one.
	DefaultLimit = 200
	// MaxLimit bounds how many records a single read returns.
	MaxLimit = 1000
)

// Read scans the log for the caller's tenant (org+workspace) and returns the
// matching entries most-recent first, capped at the (clamped) query limit. It
// folds the whole log — the same O(n) read the projection rebuild and the
// existence checks use; the SQLite log is the indexed backend for large logs.
func Read(ctx context.Context, log eventlog.Log, id identity.Identity, q Query) ([]Entry, error) {
	evs, err := log.Read(ctx, 0)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(evs))
	for _, e := range evs {
		if e.Org != id.Org || e.Workspace != id.Workspace || !match(e, q) {
			continue
		}
		out = append(out, Entry{
			Seq: e.Seq, ID: e.ID, Time: e.Time, Actor: e.Actor,
			Stream: e.Stream, Type: e.Type, Payload: e.Payload,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq > out[j].Seq })
	if limit := clampLimit(q.Limit); len(out) > limit {
		out = out[:limit]
	}
	return out, nil
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

func match(e eventlog.Envelope, q Query) bool {
	switch {
	case q.Stream != "" && e.Stream != q.Stream:
		return false
	case q.Actor != "" && e.Actor != q.Actor:
		return false
	case q.Type != "" && e.Type != q.Type:
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
