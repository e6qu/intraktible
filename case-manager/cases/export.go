// SPDX-License-Identifier: AGPL-3.0-or-later

package cases

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"strings"
)

// ExportAudit serializes the immutable per-case audit ledger in an
// operator-selected portable format. CSV cells are spreadsheet-safe: values
// beginning with a formula sigil are prefixed with an apostrophe.
func ExportAudit(views []CaseView, format string) ([]byte, string, string, error) {
	switch format {
	case "", "csv":
		var buf bytes.Buffer
		writer := csv.NewWriter(&buf)
		if err := writer.Write([]string{"case_id", "case_type", "status", "event", "actor", "at", "detail"}); err != nil {
			return nil, "", "", err
		}
		for _, view := range views {
			for _, entry := range view.Audit {
				row := []string{
					view.CaseID, view.CaseType, string(view.Status), entry.Type,
					entry.Actor, entry.At.UTC().Format("2006-01-02T15:04:05Z07:00"), entry.Detail,
				}
				for i := range row {
					row[i] = spreadsheetSafe(row[i])
				}
				if err := writer.Write(row); err != nil {
					return nil, "", "", err
				}
			}
		}
		writer.Flush()
		if err := writer.Error(); err != nil {
			return nil, "", "", err
		}
		return buf.Bytes(), "text/csv; charset=utf-8", "csv", nil
	case "json":
		out, err := json.MarshalIndent(views, "", "  ")
		if err != nil {
			return nil, "", "", fmt.Errorf("cases: marshal audit export: %w", err)
		}
		return append(out, '\n'), "application/json", "json", nil
	case "markdown":
		var buf strings.Builder
		buf.WriteString("# Case audit export\n\n")
		for _, view := range views {
			fmt.Fprintf(&buf, "## %s — %s\n\n", markdownSafe(view.CaseID), markdownSafe(view.CompanyName))
			buf.WriteString("| Time | Event | Actor | Detail |\n|---|---|---|---|\n")
			for _, entry := range view.Audit {
				fmt.Fprintf(
					&buf, "| %s | %s | %s | %s |\n",
					entry.At.UTC().Format("2006-01-02T15:04:05Z07:00"),
					markdownSafe(entry.Type), markdownSafe(entry.Actor), markdownSafe(entry.Detail),
				)
			}
			buf.WriteByte('\n')
		}
		return []byte(buf.String()), "text/markdown; charset=utf-8", "md", nil
	default:
		return nil, "", "", fmt.Errorf("cases: unsupported export format %q (want csv, json, or markdown)", format)
	}
}

func spreadsheetSafe(value string) string {
	trimmed := strings.TrimLeft(value, " \t\r\n")
	if trimmed != "" && strings.ContainsRune("=+-@", rune(trimmed[0])) {
		return "'" + value
	}
	return value
}

func markdownSafe(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "|", "\\|")
	value = strings.ReplaceAll(value, "\r", " ")
	return strings.ReplaceAll(value, "\n", "<br>")
}
