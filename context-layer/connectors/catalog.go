// SPDX-License-Identifier: AGPL-3.0-or-later

package connectors

import (
	"encoding/json"

	"github.com/e6qu/intraktible/context-layer/domain"
)

// Template is a starting point for a connector: a category, the connector type,
// and a config scaffold the operator edits (replacing the placeholder URL/DSN and
// filling in credentials). Credential fields (token/secret/api_key/…) are sealed
// at rest by the connector secret keyring and never served back unredacted.
type Template struct {
	ID          string               `json:"id"`
	Name        string               `json:"name"`
	Category    string               `json:"category"`
	Type        domain.ConnectorType `json:"type"` // a typed connector kind, not a free string
	Description string               `json:"description"`
	Config      json.RawMessage      `json:"config"`
}

// Catalog is the curated set of connector templates for common decisioning
// integrations. It is static data — instantiating one is an ordinary
// DefineConnector with the (edited) scaffold config.
func Catalog() []Template {
	return []Template{
		{
			ID: "rest", Name: "HTTP REST", Category: "Generic", Type: "http",
			Description: "Any JSON HTTP endpoint. Replace the URL; the response is available to Connect nodes.",
			Config:      json.RawMessage(`{"url":"https://api.example.com/resource","method":"GET"}`),
		},
		{
			ID: "rest-bearer", Name: "HTTP REST (bearer token)", Category: "Generic", Type: "http",
			Description: "An authenticated JSON HTTP endpoint. The bearer token is sealed at rest; add custom headers as needed.",
			Config:      json.RawMessage(`{"url":"https://api.example.com/resource","method":"POST","auth":{"type":"bearer","token":""},"headers":{"X-Tenant":""}}`),
		},
		{
			ID: "rest-apikey", Name: "HTTP REST (API-key header)", Category: "Generic", Type: "http",
			Description: "An HTTP endpoint authenticated by an API-key header. The key value is sealed at rest.",
			Config:      json.RawMessage(`{"url":"https://api.example.com/resource","method":"POST","auth":{"type":"header","name":"X-Api-Key","value":""}}`),
		},
		{
			ID: "rest-oauth2", Name: "HTTP REST (OAuth2 client credentials)", Category: "Generic", Type: "http",
			Description: "An HTTP endpoint behind OAuth2 client_credentials. A token is fetched from token_url (cached by its expiry) and sent as a bearer; client_secret is sealed at rest.",
			Config:      json.RawMessage(`{"url":"https://api.example.com/resource","method":"POST","auth":{"type":"oauth2","token_url":"https://idp.example.com/oauth/token","client_id":"","client_secret":"","scope":""}}`),
		},
		{
			ID: "credit-bureau", Name: "Credit bureau", Category: "Credit", Type: "credit_bureau",
			Description: "Experian/Equifax/TransUnion inquiry, normalized to {score, band, reason_codes}. Set the provider, inquiry path, auth, and the response field paths.",
			Config:      json.RawMessage(`{"provider":"experian","path":"/v1/creditreport","auth":{"type":"bearer","token":"…"},"score_field":"riskModel.score","band_field":"grade","reasons_field":"reasonCodes"}`),
		},
		{
			ID: "kyc-aml", Name: "KYC / AML", Category: "Identity", Type: "http",
			Description: "Identity-verification / sanctions-screening endpoint. POST the entity, read the verdict.",
			Config:      json.RawMessage(`{"url":"https://kyc.example.com/v1/verify","method":"POST"}`),
		},
		{
			ID: "fraud-score", Name: "Fraud score", Category: "Risk", Type: "http",
			Description: "A fraud/risk scoring service. POST the transaction, read the risk score.",
			Config:      json.RawMessage(`{"url":"https://fraud.example.com/v1/score","method":"POST"}`),
		},
		{
			ID: "document-ocr", Name: "Document / OCR", Category: "Documents", Type: "http",
			Description: "A document-extraction / OCR endpoint. POST the document reference, read the extracted fields.",
			Config:      json.RawMessage(`{"url":"https://ocr.example.com/v1/extract","method":"POST"}`),
		},
		{
			ID: "income-verification", Name: "Income / employment verification", Category: "Credit", Type: "http",
			Description: "A payroll/employment-verification service (The Work Number-style). POST the applicant, read verified income/employment.",
			Config:      json.RawMessage(`{"url":"https://income.example.com/v1/verify","method":"POST"}`),
		},
		{
			ID: "open-banking", Name: "Open banking / transactions", Category: "Credit", Type: "http",
			Description: "A bank-transaction aggregator (Plaid/Tink/TrueLayer-style). POST the linked account, read cashflow signals.",
			Config:      json.RawMessage(`{"url":"https://banking.example.com/v1/transactions","method":"POST"}`),
		},
		{
			ID: "bank-account-verify", Name: "Bank-account verification", Category: "Payments", Type: "http",
			Description: "Account ownership / micro-deposit verification. POST the account details, read the verification status.",
			Config:      json.RawMessage(`{"url":"https://accounts.example.com/v1/verify","method":"POST"}`),
		},
		{
			ID: "device-ip-risk", Name: "Device / IP risk", Category: "Risk", Type: "http",
			Description: "Device fingerprint + IP reputation. POST the session signals, read a device-risk score.",
			Config:      json.RawMessage(`{"url":"https://device.example.com/v1/risk","method":"POST"}`),
		},
		{
			ID: "email-phone-risk", Name: "Email / phone risk", Category: "Identity", Type: "http",
			Description: "Email + phone reputation/validation (Emailage/Telesign-style). POST the contact, read a risk signal.",
			Config:      json.RawMessage(`{"url":"https://contact.example.com/v1/risk","method":"POST"}`),
		},
		{
			ID: "address-verification", Name: "Address verification", Category: "Identity", Type: "http",
			Description: "Address standardization + validation. POST the address, read the normalized/validated result.",
			Config:      json.RawMessage(`{"url":"https://address.example.com/v1/verify","method":"POST"}`),
		},
		{
			ID: "kyb-business", Name: "KYB / business verification", Category: "Identity", Type: "http",
			Description: "Business-entity verification + beneficial ownership (Middesk-style). POST the business, read its profile.",
			Config:      json.RawMessage(`{"url":"https://kyb.example.com/v1/verify","method":"POST"}`),
		},
		{
			ID: "watchlist-screening", Name: "Watchlist / sanctions screening", Category: "Compliance", Type: "sanctions",
			Description: "In-process PEP/sanctions name screening (OFAC/EU/UN) — fuzzy-matches the subject against a watchlist and returns the hits. No network; the watchlist is the config.",
			Config:      json.RawMessage(`{"threshold":0.85,"watchlist":[{"name":"Example Name","list":"OFAC-SDN","program":"…"}]}`),
		},
		{
			ID: "geo-ip", Name: "Geolocation (IP)", Category: "Risk", Type: "http",
			Description: "IP geolocation + proxy/VPN detection. GET by IP, read country/region + anonymizer flags.",
			Config:      json.RawMessage(`{"url":"https://geo.example.com/v1/lookup","method":"GET"}`),
		},
		{
			ID: "collateral-valuation", Name: "Collateral valuation", Category: "Credit", Type: "http",
			Description: "Asset/collateral valuation (vehicle/property). POST the asset, read its estimated value.",
			Config:      json.RawMessage(`{"url":"https://valuation.example.com/v1/value","method":"POST"}`),
		},
		{
			ID: "sql-lookup", Name: "SQL lookup (sqlite)", Category: "Data", Type: "sql",
			Description: "A SQL SELECT with named (:name) placeholders against a local sqlite database.",
			Config:      json.RawMessage(`{"driver":"sqlite","dsn":"file:reference.db","query":"SELECT score FROM applicants WHERE id = :id"}`),
		},
		{
			ID: "sql-postgres", Name: "SQL lookup (Postgres)", Category: "Data", Type: "sql",
			Description: "A read-only SELECT against Postgres with positional ($1) placeholders. Runs in a read-only transaction, so a connector can never mutate the database.",
			Config:      json.RawMessage(`{"driver":"postgres","dsn":"postgres://user:pass@host:5432/db","query":"SELECT score FROM applicants WHERE id = $1","args":["id"]}`),
		},
		{
			ID: "sql-feature-store", Name: "SQL feature store", Category: "Data", Type: "sql",
			Description: "Read precomputed features for an entity from a local feature table (sqlite).",
			Config:      json.RawMessage(`{"driver":"sqlite","dsn":"file:features.db","query":"SELECT * FROM features WHERE entity_id = :entity_id"}`),
		},
		{
			ID: "graphql", Name: "GraphQL endpoint", Category: "Generic", Type: "graphql",
			Description: "POST a GraphQL query to an endpoint; the decide input becomes the query variables.",
			Config:      json.RawMessage(`{"url":"https://api.example.com/graphql","query":"query($id: ID!) { applicant(id: $id) { score } }"}`),
		},
		{
			ID: "static-flags", Name: "Static / feature flags", Category: "Data", Type: "static",
			Description: "Serve a fixed JSON value (constants, thresholds, feature flags) with no I/O — also handy for stubbing.",
			Config:      json.RawMessage(`{"data":{"min_score":650,"flags":{"new_model":true}}}`),
		},
		{
			ID: "plaid", Name: "Plaid (open banking)", Category: "Credit", Type: "plaid",
			Description: "First-class Plaid adapter. client_id+secret are sealed and injected into the request body; set env + the endpoint path.",
			Config:      json.RawMessage(`{"env":"sandbox","client_id":"","secret":"","path":"/accounts/balance/get"}`),
		},
		{
			ID: "stripe", Name: "Stripe (payments)", Category: "Payments", Type: "stripe",
			Description: "First-class Stripe read adapter. The secret key is sealed and sent as a bearer token; set the resource path (params become the query).",
			Config:      json.RawMessage(`{"secret_key":"","path":"/v1/charges"}`),
		},
	}
}
