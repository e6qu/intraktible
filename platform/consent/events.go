// SPDX-License-Identifier: AGPL-3.0-or-later

// Package consent is a purpose-limitation ledger: a data subject's consent to
// process their data for a named purpose, recorded as events so the grant/withdraw
// history is durable and auditable. It answers "may we use this subject's data for
// this purpose right now?" (Has) and "what has this subject consented to?" (List) —
// the record a regulated lender must keep and be able to prove (GDPR Art. 6/7,
// GLBA purpose limitation). Enforcement in the decide path is deliberately separate;
// this is the authoritative record it would consult.
package consent

import "time"

// StreamConsent is the event stream for consent grants and withdrawals.
const StreamConsent = "platform.consent"

// Consent event types.
const (
	TypeConsentGranted   = "platform.consent_granted"
	TypeConsentWithdrawn = "platform.consent_withdrawn"
)

// LawfulBasis is the GDPR Art. 6 basis under which the purpose is processed. It is a
// named type so an unknown basis is rejected at the boundary, not stored.
type LawfulBasis string

const (
	BasisConsent            LawfulBasis = "consent"
	BasisContract           LawfulBasis = "contract"
	BasisLegalObligation    LawfulBasis = "legal_obligation"
	BasisVitalInterests     LawfulBasis = "vital_interests"
	BasisPublicTask         LawfulBasis = "public_task"
	BasisLegitimateInterest LawfulBasis = "legitimate_interest"
)

// Valid reports whether b is a recognized lawful basis.
func (b LawfulBasis) Valid() bool {
	switch b {
	case BasisConsent, BasisContract, BasisLegalObligation, BasisVitalInterests, BasisPublicTask, BasisLegitimateInterest:
		return true
	default:
		return false
	}
}

// Granted records a subject consenting to processing for a purpose under a
// lawful basis, optionally until ExpiresAt (zero = no expiry).
type Granted struct {
	Subject   string      `json:"subject"`
	Purpose   string      `json:"purpose"`
	Basis     LawfulBasis `json:"basis"`
	ExpiresAt *time.Time  `json:"expires_at,omitempty"`
}

// Withdrawn records a subject withdrawing consent for a purpose.
type Withdrawn struct {
	Subject string `json:"subject"`
	Purpose string `json:"purpose"`
	Reason  string `json:"reason,omitempty"`
}
