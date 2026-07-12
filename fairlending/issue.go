// SPDX-License-Identifier: AGPL-3.0-or-later

package fairlending

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// StreamAdverseAction is the event stream for adverse-action notice issuances.
const StreamAdverseAction = "fairlending.adverse_action"

// TypeIssued records that an adverse-action notice was issued for a decision.
const TypeIssued = "fairlending.adverse_action_issued"

// IssuanceCollection holds one issuance record per decision (the latest issuance;
// the event log keeps the full trail if a corrected notice is re-issued).
const IssuanceCollection = "fairlending_adverse_action"

// DeliveryMethod is how the notice was delivered to the applicant. It is a named
// type so an unrecognized method is rejected at the boundary, not stored as free
// text — the record must show how the notice reached the applicant (ECOA requires it
// be sent, and the 30-day clock runs to delivery).
type DeliveryMethod string

const (
	DeliveryMail          DeliveryMethod = "mail"
	DeliveryEmail         DeliveryMethod = "email"
	DeliveryInApp         DeliveryMethod = "in_app"
	DeliveryHandDelivered DeliveryMethod = "hand_delivered"
)

// Valid reports whether m is a recognized delivery method.
func (m DeliveryMethod) Valid() bool {
	switch m {
	case DeliveryMail, DeliveryEmail, DeliveryInApp, DeliveryHandDelivered:
		return true
	default:
		return false
	}
}

// Issued is the event recording an adverse-action notice issuance. It is the durable
// proof ECOA/Reg B expects a creditor to keep: which decision, to which subject, on
// what date, by what method, citing which principal reasons, and a hash of the exact
// notice text sent (tamper-evidence — the creditor can later prove what was served).
type Issued struct {
	DecisionID            string         `json:"decision_id"`
	Subject               string         `json:"subject,omitempty"` // "type/id", the same subject key as consent/PII/erasure
	Method                DeliveryMethod `json:"method"`
	BasedOnConsumerReport bool           `json:"based_on_consumer_report"`
	PrincipalReasons      []string       `json:"principal_reasons"`
	ContentHash           string         `json:"content_hash"`
	HashAlgo              string         `json:"hash_algo"`
}

// HashNotice returns the hex SHA-256 of a rendered notice, the fingerprint stored on
// the issuance so the served document is verifiable after the fact.
func HashNotice(notice string) (hash, algo string) {
	sum := sha256.Sum256([]byte(notice))
	return hex.EncodeToString(sum[:]), "sha-256"
}

// IssueCmd is the input to Issue: an already-validated, already-rendered issuance.
// The service renders and validates the notice (it holds the store); Issue records
// the fact. Keeping the render out of the command mirrors the package's other
// commands, which append events and do not read the store.
type IssueCmd struct {
	DecisionID            string
	Subject               string
	Method                DeliveryMethod
	BasedOnConsumerReport bool
	PrincipalReasons      []string
	ContentHash           string
	HashAlgo              string
}

// Issue records that an adverse-action notice was issued. It re-checks the essentials
// (a known delivery method, at least one principal reason, a content hash) so a
// malformed issuance never reaches the log, then appends the event.
func (h *Handler) Issue(ctx context.Context, id identity.Identity, cmd IssueCmd) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	cmd.DecisionID = strings.TrimSpace(cmd.DecisionID)
	if cmd.DecisionID == "" {
		return eventlog.Envelope{}, fmt.Errorf("fairlending: decision_id is required")
	}
	if !cmd.Method.Valid() {
		return eventlog.Envelope{}, fmt.Errorf("fairlending: unknown delivery method %q", cmd.Method)
	}
	if len(cmd.PrincipalReasons) == 0 {
		return eventlog.Envelope{}, fmt.Errorf("fairlending: an issuance must cite at least one principal reason")
	}
	if cmd.ContentHash == "" || cmd.HashAlgo == "" {
		return eventlog.Envelope{}, fmt.Errorf("fairlending: an issuance must carry the notice content hash")
	}
	b, err := json.Marshal(Issued(cmd))
	if err != nil {
		return eventlog.Envelope{}, fmt.Errorf("fairlending: marshal issued: %w", err)
	}
	return h.log.Append(ctx, eventlog.Envelope{
		Org: id.Org, Workspace: id.Workspace, Actor: id.Actor,
		Stream: StreamAdverseAction, Type: TypeIssued, Time: h.now(), Payload: b,
	})
}

// IssuanceView is the stored issuance record with its provenance — what a compliance
// reviewer or an auditor reads to confirm a declined applicant was served their
// notice, when, how, and citing what.
type IssuanceView struct {
	Org                   string         `json:"org"`
	Workspace             string         `json:"workspace"`
	DecisionID            string         `json:"decision_id"`
	Subject               string         `json:"subject,omitempty"`
	Method                DeliveryMethod `json:"method"`
	BasedOnConsumerReport bool           `json:"based_on_consumer_report"`
	PrincipalReasons      []string       `json:"principal_reasons"`
	ContentHash           string         `json:"content_hash"`
	HashAlgo              string         `json:"hash_algo"`
	IssuedAt              time.Time      `json:"issued_at"`
	IssuedBy              string         `json:"issued_by"`
}

// IssuanceProjector folds the issuance stream into a per-decision record.
type IssuanceProjector struct{}

// Name identifies the projector.
func (IssuanceProjector) Name() string { return IssuanceCollection }

// Collections lists the store collection this projector owns.
func (IssuanceProjector) Collections() []string { return []string{IssuanceCollection} }

// Apply maintains the per-decision issuance record. A re-issue overwrites with the
// latest (the log retains earlier issuances for the full trail).
func (IssuanceProjector) Apply(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	if e.Type != TypeIssued {
		return nil
	}
	var p Issued
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("fairlending_adverse_action: decode issued seq %d: %w", e.Seq, err)
	}
	v := IssuanceView{
		Org: e.Org, Workspace: e.Workspace, DecisionID: p.DecisionID, Subject: p.Subject,
		Method: p.Method, BasedOnConsumerReport: p.BasedOnConsumerReport, PrincipalReasons: p.PrincipalReasons,
		ContentHash: p.ContentHash, HashAlgo: p.HashAlgo, IssuedAt: e.Time, IssuedBy: e.Actor,
	}
	return store.PutDoc(ctx, s, IssuanceCollection, store.Key(e.Org, e.Workspace, p.DecisionID), v)
}

// ReadIssuance returns the issuance record for a decision (false when none exists).
func ReadIssuance(ctx context.Context, s store.Store, id identity.Identity, decisionID string) (IssuanceView, bool, error) {
	return store.GetDoc[IssuanceView](ctx, s, IssuanceCollection, store.Key(id.Org, id.Workspace, decisionID))
}

// ListIssuances returns the tenant's issuance records, most recently issued first.
func ListIssuances(ctx context.Context, s store.Store, id identity.Identity) ([]IssuanceView, error) {
	out, err := store.ListDocs[IssuanceView](ctx, s, IssuanceCollection, store.Key(id.Org, id.Workspace, ""))
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].IssuedAt.After(out[j].IssuedAt) })
	return out, nil
}
