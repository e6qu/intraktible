// SPDX-License-Identifier: AGPL-3.0-or-later

package mrm

import (
	"encoding/csv"
	"fmt"
	"strconv"
	"strings"
)

// CSV renders the inventory as a spreadsheet (one row per model). Cells are passed
// through csvSafe to defuse spreadsheet formula injection.
func CSV(rep Report) (string, error) {
	var b strings.Builder
	w := csv.NewWriter(&b)
	header := []string{"kind", "id", "name", "version", "owner", "coverage", "decisions", "success_rate", "issues"}
	if err := w.Write(header); err != nil {
		return "", err
	}
	for _, m := range rep.Models {
		row := []string{
			string(m.Kind),
			csvSafe(m.ID),
			csvSafe(m.Name),
			strconv.Itoa(m.Version),
			csvSafe(m.Owner),
			string(m.Validation.Coverage),
			strconv.Itoa(m.Monitoring.Decisions),
			strconv.FormatFloat(m.Monitoring.SuccessRate, 'f', 3, 64),
			csvSafe(strings.Join(m.Issues, "; ")),
		}
		if err := w.Write(row); err != nil {
			return "", err
		}
	}
	w.Flush()
	return b.String(), w.Error()
}

// csvSafe prefixes a leading formula trigger with a single quote so a spreadsheet
// treats the cell as text, not a formula (mirrors the audit export).
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

// Markdown renders the report as a human-readable model-risk document — the form a
// model-risk team files as evidence. Sections: summary, inventory table, and the
// open governance gaps drawn from each model's issues.
func Markdown(rep Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Model Risk Report — %s/%s\n\n", rep.Org, rep.Workspace)
	fmt.Fprintf(&b, "_Generated %s (SR 11-7 / SS1/23 model inventory, validation & monitoring)._\n\n", rep.GeneratedAt.Format("2006-01-02 15:04 MST"))

	fmt.Fprintf(&b, "## Summary\n\n")
	fmt.Fprintf(&b, "- **%d** models — %d flows, %d predictive, %d agents\n",
		rep.Summary.Total, rep.Summary.ByKind[KindFlow], rep.Summary.ByKind[KindPredictive], rep.Summary.ByKind[KindAgent])
	fmt.Fprintf(&b, "- **%d** deployed · **%d** unvalidated · **%d** with open issues\n\n",
		rep.Summary.Deployed, rep.Summary.Unvalidated, rep.Summary.WithIssues)

	fmt.Fprintf(&b, "## Inventory\n\n")
	fmt.Fprintf(&b, "| Kind | Model | Version | Owner | Validation | Decisions | Success |\n")
	fmt.Fprintf(&b, "|---|---|---|---|---|---|---|\n")
	for _, m := range rep.Models {
		fmt.Fprintf(&b, "| %s | %s | v%d | %s | %s | %d | %s |\n",
			m.Kind, mdCell(m.Name), m.Version, mdCell(m.Owner), m.Validation.Coverage,
			m.Monitoring.Decisions, pct(m.Monitoring.SuccessRate))
	}

	fmt.Fprintf(&b, "\n## Open governance gaps\n\n")
	any := false
	for _, m := range rep.Models {
		if len(m.Issues) == 0 {
			continue
		}
		any = true
		fmt.Fprintf(&b, "- **%s** (%s): %s\n", mdCell(m.Name), m.Kind, strings.Join(m.Issues, "; "))
	}
	if !any {
		fmt.Fprintf(&b, "_No open gaps — every model has validation evidence and is within its monitors._\n")
	}
	return b.String()
}

// mdCell escapes pipes so a value can't break the Markdown table layout.
func mdCell(s string) string {
	if s == "" {
		return "—"
	}
	return strings.ReplaceAll(s, "|", "\\|")
}

func pct(f float64) string {
	if f == 0 {
		return "—"
	}
	return strconv.Itoa(int(f*100+0.5)) + "%"
}
