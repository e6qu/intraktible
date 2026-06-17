// SPDX-License-Identifier: AGPL-3.0-or-later

package connectors

import "encoding/json"

// Template is a starting point for a connector: a category, the connector type,
// and a config scaffold the operator edits (replacing the placeholder URL/DSN).
// Credentials are not part of the scaffold — the HTTP connector config carries
// only url+method today; managed secrets remain separate, deferred work.
type Template struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Category    string          `json:"category"`
	Type        string          `json:"type"` // http | sql
	Description string          `json:"description"`
	Config      json.RawMessage `json:"config"`
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
			ID: "credit-bureau", Name: "Credit bureau", Category: "Credit", Type: "http",
			Description: "A bureau scoring endpoint (Experian/Equifax/TransUnion-style). POST the applicant, read the score.",
			Config:      json.RawMessage(`{"url":"https://bureau.example.com/v1/score","method":"POST"}`),
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
			ID: "sql-lookup", Name: "SQL lookup", Category: "Data", Type: "sql",
			Description: "A SQL SELECT with named (:name) placeholders. Built-in driver: sqlite.",
			Config:      json.RawMessage(`{"driver":"sqlite","dsn":"file:reference.db","query":"SELECT score FROM applicants WHERE id = :id"}`),
		},
	}
}
