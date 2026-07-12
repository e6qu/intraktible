// SPDX-License-Identifier: AGPL-3.0-or-later

// Package consent is a purpose-limitation ledger: a data subject's consent to
// process their data for a named purpose, recorded as events so the grant/withdraw
// history is durable and auditable. It answers "may we use this subject's data for
// this purpose right now?" (Has) and "what has this subject consented to?" (List) —
// the record a regulated lender must keep and be able to prove (GDPR Art. 6/7,
// GLBA purpose limitation). Enforcement in the decide path is deliberately separate;
// this is the authoritative record it would consult.
package consent

import (
	"fmt"
	"time"
)

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

// CollectionMethod records how the authorization was obtained. It is a named type so
// an unrecognized method is rejected at the boundary rather than stored as free text —
// the same discipline as LawfulBasis, and what an auditor asks for ("how did you
// obtain this?", EDPB 05/2020 §108; ICO records-of-consent "how they consented").
type CollectionMethod string

const (
	MethodESignature   CollectionMethod = "e_signature"      // signed electronically (E-SIGN/UETA, eIDAS)
	MethodWetSignature CollectionMethod = "wet_signature"    // ink signature, later scanned
	MethodScanned      CollectionMethod = "scanned_document" // an uploaded/scanned authorization
	MethodClickThrough CollectionMethod = "click_through"    // an in-product affirmative act
	MethodVerbal       CollectionMethod = "verbal"           // recorded call / phone confirmation
	MethodAPIAssertion CollectionMethod = "api_assertion"    // asserted by the controller via the API/flow
)

// Valid reports whether m is a recognized collection method.
func (m CollectionMethod) Valid() bool {
	switch m {
	case MethodESignature, MethodWetSignature, MethodScanned, MethodClickThrough, MethodVerbal, MethodAPIAssertion:
		return true
	default:
		return false
	}
}

// Evidence is the proof backing a grant — what a regulator asks the controller to
// produce (GDPR Art. 7(1) "demonstrate"; FCRA authorization; ICO records-of-consent).
// The signed artifact itself stays in the controller's system of record (respecting
// data residency — the bytes never leave their machine); we hold a tamper-evident
// reference: the content hash lets them later prove the document they hold is the one
// recorded here. NoticeVersion pins the disclosure the subject actually saw, the
// single most-cited defensibility field (reproduce exactly what was agreed to).
type Evidence struct {
	Method        CollectionMethod `json:"method,omitempty"`
	Reference     string           `json:"reference,omitempty"`      // locator in the controller's system of record (filename, doc id, URL)
	ContentHash   string           `json:"content_hash,omitempty"`   // hex digest of the artifact (tamper-evidence)
	HashAlgo      string           `json:"hash_algo,omitempty"`      // e.g. "sha-256"
	NoticeVersion string           `json:"notice_version,omitempty"` // the disclosure/notice version shown to the subject
}

// Zero reports whether no evidence was supplied.
func (e Evidence) Zero() bool { return e == Evidence{} }

// Validate rejects a malformed evidence payload: an unknown method, or a content hash
// without the algorithm that produced it (an unlabeled digest is not verifiable).
func (e Evidence) Validate() error {
	if e.Method != "" && !e.Method.Valid() {
		return fmt.Errorf("consent: unknown collection method %q", e.Method)
	}
	if e.ContentHash != "" && e.HashAlgo == "" {
		return fmt.Errorf("consent: content_hash requires hash_algo")
	}
	return nil
}

// Granted records a subject consenting to processing for a purpose under a
// lawful basis, optionally until ExpiresAt (zero = no expiry), with optional
// Evidence backing the grant.
type Granted struct {
	Subject   string      `json:"subject"`
	Purpose   string      `json:"purpose"`
	Basis     LawfulBasis `json:"basis"`
	ExpiresAt *time.Time  `json:"expires_at,omitempty"`
	Evidence  *Evidence   `json:"evidence,omitempty"`
}

// Withdrawn records a subject withdrawing consent for a purpose.
type Withdrawn struct {
	Subject string `json:"subject"`
	Purpose string `json:"purpose"`
	Reason  string `json:"reason,omitempty"`
}
