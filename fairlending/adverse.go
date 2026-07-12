// SPDX-License-Identifier: AGPL-3.0-or-later

package fairlending

import (
	"fmt"
	"strings"
	"time"

	"github.com/e6qu/intraktible/decision-engine/history"
	"github.com/e6qu/intraktible/decision-engine/policy"
)

// maxPrincipalReasons is the count of principal reasons Reg B expects an
// adverse-action notice to disclose (§1002.9: the specific principal reasons, and
// the regulation's commentary treats disclosing more than four as not meaningful).
const maxPrincipalReasons = 4

// NoticeOptions parameterizes what a rendered notice must disclose beyond the ECOA
// core. BasedOnConsumerReport adds the FCRA §615(a) disclosure block — used whenever
// the decision drew on a consumer report, which the issuer asserts (they know which
// flow pulled a bureau); it is not inferable from the recorded outcome alone.
type NoticeOptions struct {
	BasedOnConsumerReport bool
}

// Notice renders an adverse-action notice for a declined decision from its recorded
// reason codes and the workspace creditor settings. It errors rather than emit an
// incomplete notice: the decision must be a decline, must carry reason codes, and
// the workspace must have a named creditor. The specific reasons come from the
// decision's own recorded codes — the notice cites what the flow decided, not a
// re-derivation. When opts.BasedOnConsumerReport is set, the FCRA §615(a)
// disclosures are appended and the consumer reporting agency must be configured.
func Notice(rec history.Record, st Settings, opts NoticeOptions, now time.Time) (string, error) {
	if policy.Disposition(rec.Disposition) != policy.Decline {
		return "", fmt.Errorf("adverse-action notice: decision %s was not declined (disposition %q)", rec.DecisionID, rec.Disposition)
	}
	if strings.TrimSpace(st.CreditorName) == "" {
		return "", fmt.Errorf("adverse-action notice: workspace creditor identification is not configured")
	}
	if opts.BasedOnConsumerReport && strings.TrimSpace(st.CRAName) == "" {
		return "", fmt.Errorf("adverse-action notice: decision %s is marked report-based but no consumer reporting agency is configured (FCRA §615(a))", rec.DecisionID)
	}
	reasons := principalReasons(rec)
	if len(reasons) == 0 {
		return "", fmt.Errorf("adverse-action notice: decision %s carries no reason codes to cite", rec.DecisionID)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# Statement of Credit Denial, Termination, or Change\n\n")
	fmt.Fprintf(&b, "Date: %s\n\n", now.Format("2006-01-02"))
	fmt.Fprintf(&b, "**%s**\n", st.CreditorName)
	if st.CreditorAddress != "" {
		fmt.Fprintf(&b, "%s\n", st.CreditorAddress)
	}
	if st.CreditorPhone != "" {
		fmt.Fprintf(&b, "%s\n", st.CreditorPhone)
	}
	fmt.Fprintf(&b, "\nDecision reference: %s\n\n", rec.DecisionID)

	fmt.Fprintf(&b, "## Action taken\n\nAfter reviewing your application, we are unable to approve it at this time.\n\n")

	fmt.Fprintf(&b, "## Principal reason(s) for the decision\n\n")
	for _, r := range reasons {
		fmt.Fprintf(&b, "- %s\n", r)
	}

	if opts.BasedOnConsumerReport {
		fmt.Fprintf(&b, "\n## Our use of a consumer report\n\n")
		fmt.Fprintf(&b, "%s\n", fcraNotice(st))
	}

	fmt.Fprintf(&b, "\n## Your rights under the Equal Credit Opportunity Act\n\n")
	fmt.Fprintf(&b, "%s\n", ecoaNotice(st.EnforcementAgency))
	return b.String(), nil
}

// principalReasons draws the specific reasons from the decision's recorded codes,
// preferring the human-readable description and falling back to the code, capped at
// the Reg B count. DispositionReason, if present, leads as the primary ground.
func principalReasons(rec history.Record) []string {
	var out []string
	if r := strings.TrimSpace(rec.DispositionReason); r != "" {
		out = append(out, r)
	}
	for _, c := range rec.ReasonCodes {
		text := strings.TrimSpace(c.Description)
		if text == "" {
			text = strings.TrimSpace(c.Code)
		}
		if text == "" || containsFold(out, text) {
			continue
		}
		out = append(out, text)
		if len(out) >= maxPrincipalReasons {
			break
		}
	}
	if len(out) > maxPrincipalReasons {
		out = out[:maxPrincipalReasons]
	}
	return out
}

// containsFold reports whether s is already present in xs, case-insensitively, so a
// DispositionReason and an identically-worded reason code are not both listed.
func containsFold(xs []string, s string) bool {
	for _, x := range xs {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

// fcraNotice is the FCRA §615(a) disclosure block a creditor must include when the
// adverse action was based in whole or part on a consumer report: it names and
// locates the reporting agency, states the agency did not make the decision and
// cannot explain it, and gives the applicant's right to a free report (within 60
// days) and to dispute. The CRA identity is required by the caller before this runs.
func fcraNotice(st Settings) string {
	var b strings.Builder
	b.WriteString("Our credit decision was based in whole or in part on information obtained in a report " +
		"from the consumer reporting agency named below. Under the Fair Credit Reporting Act, the " +
		"reporting agency did not make the decision to take the adverse action and cannot give you the " +
		"specific reasons why the action was taken.\n\n")
	fmt.Fprintf(&b, "%s\n", st.CRAName)
	if st.CRAAddress != "" {
		fmt.Fprintf(&b, "%s\n", st.CRAAddress)
	}
	if st.CRAPhone != "" {
		fmt.Fprintf(&b, "%s\n", st.CRAPhone)
	}
	b.WriteString("\nYou have a right to obtain a free copy of your consumer report from the reporting " +
		"agency if you request it no later than 60 days after you receive this notice. You have a right " +
		"to dispute with the reporting agency the accuracy or completeness of any information in your report.")
	return b.String()
}

// ecoaNotice is the statutory ECOA notice (Reg B §1002.9(b)(1)). The enforcement
// agency named in the last sentence varies by creditor; an empty agency renders the
// Federal Trade Commission reference the regulation gives for creditors without a
// more specific federal supervisor.
func ecoaNotice(agency string) string {
	if strings.TrimSpace(agency) == "" {
		agency = "Federal Trade Commission, Equal Credit Opportunity, Washington, DC 20580"
	}
	return "The federal Equal Credit Opportunity Act prohibits creditors from discriminating against " +
		"credit applicants on the basis of race, color, religion, national origin, sex, marital status, " +
		"age (provided the applicant has the capacity to enter into a binding contract); because all or " +
		"part of the applicant's income derives from any public assistance program; or because the " +
		"applicant has in good faith exercised any right under the Consumer Credit Protection Act. The " +
		"federal agency that administers compliance with this law concerning this creditor is: " + agency + "."
}
