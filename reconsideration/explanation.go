// SPDX-License-Identifier: AGPL-3.0-or-later

package reconsideration

import (
	"fmt"
	"strings"
	"time"

	"github.com/e6qu/intraktible/decision-engine/history"
)

// SolelyAutomated reports whether a decision was reached with no person in the loop —
// no manual-review case and no human resume. This is the Art. 22 trigger condition.
func SolelyAutomated(rec history.Record) bool {
	return rec.CaseID == "" && !rec.HumanReviewed
}

// Explain renders a subject-facing explanation of an automated decision — the GDPR
// Art. 15(1)(h) / Art. 22 "meaningful information about the logic" plus the subject's
// Art. 22(3) rights (human intervention, contest, explanation). Distinct from the ECOA
// adverse-action notice: that is a US decline letter; this is the data-subject rights
// explanation, and it names the reconsideration channel a review is recorded through.
// review is the recorded human review, or nil when none exists.
func Explain(rec history.Record, review *Review, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# How this decision was made\n\n")
	fmt.Fprintf(&b, "Decision reference: %s  \n", rec.DecisionID)
	at := rec.EndedAt
	if at.IsZero() {
		at = rec.StartedAt
	}
	if !at.IsZero() {
		fmt.Fprintf(&b, "Date: %s\n", at.Format("2006-01-02"))
	}

	b.WriteString("\n## Automated processing\n\n")
	if SolelyAutomated(rec) {
		b.WriteString("This decision was made by solely automated means — no person was involved in reaching it. ")
	} else {
		b.WriteString("A person was involved in this decision; it was not reached by automated processing alone. ")
	}
	b.WriteString("Where a solely automated decision has a legal or similarly significant effect, it is subject to " +
		"Article 22 of the General Data Protection Regulation (and, in the United Kingdom, Articles 22A to 22D of " +
		"the Data (Use and Access) Act 2025).\n\n")

	b.WriteString("## The outcome\n\n")
	if rec.Disposition != "" {
		fmt.Fprintf(&b, "Outcome: **%s**.", rec.Disposition)
		if r := strings.TrimSpace(rec.DispositionReason); r != "" {
			fmt.Fprintf(&b, " %s.", strings.TrimRight(r, "."))
		}
		b.WriteString("\n\n")
	} else {
		b.WriteString("This decision did not assign an outcome.\n\n")
	}

	b.WriteString("## The main factors\n\n")
	if len(rec.ReasonCodes) == 0 {
		b.WriteString("No specific factors were recorded for this decision.\n\n")
	} else {
		b.WriteString("These are the principal factors that drove the outcome:\n\n")
		for _, rc := range rec.ReasonCodes {
			text := strings.TrimSpace(rc.Description)
			if text == "" {
				text = strings.TrimSpace(rc.Code)
			}
			fmt.Fprintf(&b, "- %s\n", text)
		}
		b.WriteString("\n")
	}

	b.WriteString("## Your rights\n\n")
	b.WriteString("Under Article 22(3) of the General Data Protection Regulation you have the right to:\n\n")
	b.WriteString("- **obtain human intervention** — ask that a person review this decision;\n")
	b.WriteString("- **express your point of view and contest** the outcome;\n")
	b.WriteString("- **obtain an explanation** of how the decision was reached.\n\n")
	if review != nil {
		fmt.Fprintf(&b, "A human reviewer %s this decision on %s", review.Outcome, review.ReviewedAt.Format("2006-01-02"))
		if r := strings.TrimSpace(review.Rationale); r != "" {
			fmt.Fprintf(&b, ": %s", strings.TrimRight(r, "."))
		}
		b.WriteString(".\n")
	} else if SolelyAutomated(rec) {
		b.WriteString("No human review has been recorded for this decision yet; you may request one.\n")
	}
	return b.String()
}
