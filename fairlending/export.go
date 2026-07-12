// SPDX-License-Identifier: AGPL-3.0-or-later

package fairlending

import (
	"encoding/csv"
	"fmt"
	"strconv"
	"strings"
)

// CSV renders the group tally as a spreadsheet (one row per protected-class value).
// Cells are passed through csvSafe to defuse spreadsheet formula injection.
func CSV(rep Report) (string, error) {
	var b strings.Builder
	w := csv.NewWriter(&b)
	header := []string{"value", "total", "favorable", "adverse", "rate", "air", "reference", "flagged", "small_sample"}
	if err := w.Write(header); err != nil {
		return "", err
	}
	for _, g := range rep.Groups {
		row := []string{
			csvSafe(g.Value),
			strconv.Itoa(g.Total),
			strconv.Itoa(g.Favorable),
			strconv.Itoa(g.Adverse),
			strconv.FormatFloat(g.Rate, 'f', 4, 64),
			strconv.FormatFloat(g.AIR, 'f', 4, 64),
			strconv.FormatBool(g.Reference),
			strconv.FormatBool(g.Flagged),
			strconv.FormatBool(g.SmallSample),
		}
		if err := w.Write(row); err != nil {
			return "", err
		}
	}
	w.Flush()
	return b.String(), w.Error()
}

// csvSafe prefixes a leading formula character with a quote so a spreadsheet does
// not evaluate a value (CSV injection).
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

// Markdown renders the report as a human-readable disparate-impact document. It
// states the parameters, the reference group, the four-fifths verdict, and what was
// excluded — so a reader can see the basis of the numbers, not just the numbers.
func Markdown(rep Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Disparate-impact report — %s/%s\n\n", rep.Org, rep.Workspace)
	fmt.Fprintf(&b, "_Generated %s. Adverse-impact ratio (four-fifths rule, ECOA / Reg B)._\n\n", rep.GeneratedAt.Format("2006-01-02 15:04 MST"))

	fmt.Fprintf(&b, "## Parameters\n\n")
	fmt.Fprintf(&b, "- Flow: `%s`\n", mdCell(rep.FlowID))
	fmt.Fprintf(&b, "- Protected attribute: `%s`\n", mdCell(rep.Attribute))
	fmt.Fprintf(&b, "- Favorable outcome: `%s`\n", rep.Favorable)
	if rep.Environment != "" {
		fmt.Fprintf(&b, "- Environment: `%s`\n", rep.Environment)
	}
	fmt.Fprintf(&b, "- Scored decisions: **%d** (excluded: %d — referred, no disposition, or attribute absent)\n\n", rep.Decisions, rep.Excluded)

	verdict := "insufficient data"
	if rep.Groups2Plus {
		if rep.Passes {
			verdict = "passes (all groups ≥ 0.80)"
		} else {
			verdict = "FLAGGED (a group is below 0.80)"
		}
	}
	fmt.Fprintf(&b, "## Verdict: %s\n\n", verdict)
	if rep.Reference != "" {
		fmt.Fprintf(&b, "Reference group (highest favorable rate): `%s`. Lowest AIR: %s.\n\n", mdCell(rep.Reference), airStr(rep.MinAIR))
	}

	fmt.Fprintf(&b, "| Group | Decisions | Favorable | Rate | AIR | Note |\n")
	fmt.Fprintf(&b, "|---|---|---|---|---|---|\n")
	for _, g := range rep.Groups {
		note := ""
		switch {
		case g.Reference:
			note = "reference"
		case g.Flagged:
			note = "flagged"
		}
		if g.SmallSample {
			if note != "" {
				note += ", "
			}
			note += "small sample"
		}
		fmt.Fprintf(&b, "| %s | %d | %d | %s | %s | %s |\n",
			mdCell(g.Value), g.Total, g.Favorable, pct(g.Rate), airStr(g.AIR), mdCell(note))
	}
	if len(rep.Groups) == 0 {
		fmt.Fprintf(&b, "\n_No decisions matched the flow, attribute, and favorable/decline outcomes._\n")
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
	return strconv.Itoa(int(f*100+0.5)) + "%"
}

func airStr(f float64) string {
	return strconv.FormatFloat(f, 'f', 2, 64)
}
