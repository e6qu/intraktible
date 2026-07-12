// SPDX-License-Identifier: AGPL-3.0-or-later

// Package registers renders the compliance registers a regulated lender must retain
// and produce on examination: the adverse-action register (ECOA/Reg B records of
// adverse actions taken), the reconsideration log (Art. 22 human reviews of automated
// declines), and the consent / lawful-basis register. Each is a read-only export
// (CSV or Markdown) over a domain's existing tenant-wide list — the download end of
// the compliance dashboard.
package registers

import (
	"encoding/csv"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/intraktible/fairlending"
	"github.com/e6qu/intraktible/platform/consent"
	"github.com/e6qu/intraktible/reconsideration"
)

// csvDoc writes a header + rows through encoding/csv with each cell defused against
// spreadsheet formula injection.
func csvDoc(header []string, rows [][]string) (string, error) {
	var b strings.Builder
	w := csv.NewWriter(&b)
	safe := make([]string, len(header))
	for i, h := range header {
		safe[i] = csvSafe(h)
	}
	if err := w.Write(safe); err != nil {
		return "", err
	}
	for _, row := range rows {
		for i := range row {
			row[i] = csvSafe(row[i])
		}
		if err := w.Write(row); err != nil {
			return "", err
		}
	}
	w.Flush()
	return b.String(), w.Error()
}

// csvSafe prefixes a leading formula character with a quote so a spreadsheet does not
// evaluate a cell (CSV injection).
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

// mdCell escapes pipes so a value can't break a Markdown table, rendering empty as a dash.
func mdCell(s string) string {
	if s == "" {
		return "—"
	}
	return strings.ReplaceAll(s, "|", "\\|")
}

func day(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02")
}

func dayPtr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return day(*t)
}

func mdDoc(title, subtitle string, header []string, rows [][]string, empty string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", title)
	if subtitle != "" {
		fmt.Fprintf(&b, "_%s_\n\n", subtitle)
	}
	if len(rows) == 0 {
		fmt.Fprintf(&b, "%s\n", empty)
		return b.String()
	}
	fmt.Fprintf(&b, "| %s |\n", strings.Join(header, " | "))
	fmt.Fprintf(&b, "|%s\n", strings.Repeat("---|", len(header)))
	for _, row := range rows {
		cells := make([]string, len(row))
		for i, c := range row {
			cells[i] = mdCell(c)
		}
		fmt.Fprintf(&b, "| %s |\n", strings.Join(cells, " | "))
	}
	return b.String()
}

// AdverseActionCSV / AdverseActionMarkdown render the register of adverse-action
// notices issued — the ECOA/Reg B record a lender retains of each adverse action taken.
func adverseHeader() []string {
	return []string{"decision_id", "subject", "issued_at", "method", "consumer_report", "principal_reasons", "content_hash", "issued_by"}
}

func adverseRows(items []fairlending.IssuanceView) [][]string {
	rows := make([][]string, 0, len(items))
	for _, v := range items {
		rows = append(rows, []string{
			v.DecisionID, v.Subject, day(v.IssuedAt), string(v.Method),
			strconv.FormatBool(v.BasedOnConsumerReport), strings.Join(v.PrincipalReasons, "; "),
			v.ContentHash, v.IssuedBy,
		})
	}
	return rows
}

func AdverseActionCSV(items []fairlending.IssuanceView) (string, error) {
	return csvDoc(adverseHeader(), adverseRows(items))
}

func AdverseActionMarkdown(items []fairlending.IssuanceView, generated string) string {
	return mdDoc("Adverse-action register", "ECOA / Reg B record of adverse actions taken. Generated "+generated+".",
		adverseHeader(), adverseRows(items), "_No adverse-action notices have been issued._")
}

// Reconsideration register — the Art. 22 human reviews of automated declines.
func reconHeader() []string {
	return []string{"decision_id", "subject", "reviewed_at", "basis", "outcome", "rationale", "reviewed_by"}
}

func reconRows(items []reconsideration.Review) [][]string {
	rows := make([][]string, 0, len(items))
	for _, v := range items {
		rows = append(rows, []string{
			v.DecisionID, v.Subject, day(v.ReviewedAt), string(v.Basis), string(v.Outcome), v.Rationale, v.ReviewedBy,
		})
	}
	return rows
}

func ReconsiderationCSV(items []reconsideration.Review) (string, error) {
	return csvDoc(reconHeader(), reconRows(items))
}

func ReconsiderationMarkdown(items []reconsideration.Review, generated string) string {
	return mdDoc("Human-review register", "GDPR Art. 22 / ECOA reconsideration — human reviews of solely-automated declines. Generated "+generated+".",
		reconHeader(), reconRows(items), "_No human reviews have been recorded._")
}

// Consent register — the lawful basis recorded for each (subject, purpose).
func consentHeader() []string {
	return []string{"subject", "purpose", "basis", "status", "granted_at", "withdrawn_at", "expires_at", "evidence_method", "recorded_by"}
}

func consentRows(items []consent.Record) [][]string {
	rows := make([][]string, 0, len(items))
	for _, r := range items {
		status := "withdrawn"
		if r.Granted {
			status = "active"
		}
		method := ""
		if r.Evidence != nil {
			method = string(r.Evidence.Method)
		}
		rows = append(rows, []string{
			r.Subject, r.Purpose, string(r.Basis), status,
			dayPtr(r.GrantedAt), dayPtr(r.WithdrawnAt), dayPtr(r.ExpiresAt), method, r.UpdatedBy,
		})
	}
	return rows
}

func ConsentCSV(items []consent.Record) (string, error) {
	return csvDoc(consentHeader(), consentRows(items))
}

func ConsentMarkdown(items []consent.Record, generated string) string {
	return mdDoc("Lawful-basis register", "The lawful basis recorded for processing each subject (GDPR Art. 6). Generated "+generated+".",
		consentHeader(), consentRows(items), "_No lawful-basis records exist._")
}
