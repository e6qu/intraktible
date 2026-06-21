// SPDX-License-Identifier: AGPL-3.0-or-later

package audit_test

import (
	"encoding/csv"
	"strings"
	"testing"

	"github.com/e6qu/intraktible/platform/audit"
)

// FuzzAuditCSV asserts the audit CSV export is robust against adversarial,
// attacker-influenced fields (actor/stream/type): the output always re-parses to the
// right shape (header + one row per entry, equal column counts), and every cell is
// neutralized against spreadsheet formula injection — no parsed cell begins with a
// formula trigger (= + - @ tab CR) unless it carries the leading-apostrophe guard.
func FuzzAuditCSV(f *testing.F) {
	f.Add("alice", "flows", "flow.created")
	f.Add("=cmd|'/c calc'", "x", "y")
	f.Add("\trow", "@evil", "-1+1")
	f.Add("normal", "", "with,comma\nand newline")
	f.Fuzz(func(t *testing.T, actor, stream, typ string) {
		out, err := audit.CSV([]audit.Entry{{Seq: 1, ID: "id", Actor: actor, Stream: stream, Type: typ}})
		if err != nil {
			t.Fatalf("CSV write error: %v", err)
		}
		records, err := csv.NewReader(strings.NewReader(out)).ReadAll()
		if err != nil {
			t.Fatalf("CSV output does not re-parse: %v\noutput=%q", err, out)
		}
		if len(records) != 2 { // header + the single entry
			t.Fatalf("expected header + 1 row, got %d records", len(records))
		}
		for _, rec := range records {
			for _, cell := range rec {
				if cell == "" {
					continue
				}
				switch cell[0] {
				case '=', '+', '-', '@', '\t', '\r':
					t.Fatalf("cell begins with an un-neutralized formula trigger: %q", cell)
				}
			}
		}
	})
}
