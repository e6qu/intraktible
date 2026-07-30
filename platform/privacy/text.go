// SPDX-License-Identifier: AGPL-3.0-or-later

package privacy

import "regexp"

// textPIIPatterns match high-signal PII shapes in unstructured text. Custom
// workspace field classifications cannot protect prose because prose has no
// field names, so review/comment write paths use this narrower boundary.
var textPIIPatterns = []*regexp.Regexp{
	regexp.MustCompile(`[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`),
	regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`),
	regexp.MustCompile(`\b(?:\d[ -]?){13,16}\b`),
	regexp.MustCompile(`\b\+?\d[\d\s().\-]{8,}\d\b`),
}

// ContainsTextPII reports whether free text contains a high-signal email, US
// SSN, card-like number, or phone-like number.
func ContainsTextPII(value string) bool {
	for _, pattern := range textPIIPatterns {
		if pattern.MatchString(value) {
			return true
		}
	}
	return false
}

// RedactTextPII replaces high-signal free-text PII with the platform marker.
func RedactTextPII(value string) string {
	for _, pattern := range textPIIPatterns {
		value = pattern.ReplaceAllString(value, Redacted)
	}
	return value
}
