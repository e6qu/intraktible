// SPDX-License-Identifier: AGPL-3.0-or-later

package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ConnectorType names the kind of external system a connector talks to. It is a
// named type (not a bare string) so an invalid type is caught at the boundary, not
// deep in the build() switch. JSON of a named string type is wire-identical, so
// stored projections and the API stay compatible.
type ConnectorType string

// Connector types. HTTP calls an arbitrary configured REST endpoint (the "Custom
// Connect" case); SQL runs a parameterized query against a configured database;
// MockBureau is a deterministic in-process reference connector. Plaid and Stripe
// are first-class provider adapters (preconfigured base URL + auth scheme).
const (
	ConnectorHTTP         ConnectorType = "http"
	ConnectorSQL          ConnectorType = "sql"
	ConnectorGraphQL      ConnectorType = "graphql"
	ConnectorStatic       ConnectorType = "static"
	ConnectorMockBureau   ConnectorType = "mock_bureau"
	ConnectorPlaid        ConnectorType = "plaid"
	ConnectorStripe       ConnectorType = "stripe"
	ConnectorCreditBureau ConnectorType = "credit_bureau"
	ConnectorSanctions    ConnectorType = "sanctions"
)

var connectorTypes = map[ConnectorType]bool{
	ConnectorHTTP:         true,
	ConnectorSQL:          true,
	ConnectorGraphQL:      true,
	ConnectorStatic:       true,
	ConnectorMockBureau:   true,
	ConnectorPlaid:        true,
	ConnectorStripe:       true,
	ConnectorCreditBureau: true,
	ConnectorSanctions:    true,
}

// Valid reports whether t is a known connector type.
func (t ConnectorType) Valid() bool { return connectorTypes[t] }

// ValidConnectorType reports whether t is a known connector type. It is the
// string-boundary helper for callers that hold a raw request string (the service
// validates the decoded JSON before it has a typed value).
func ValidConnectorType(t string) bool { return ConnectorType(t).Valid() }

// DefineConnector registers (or redefines) a named connector. Config is
// type-specific JSON (http: {"url","method","headers","auth"}; sql:
// {"driver","dsn","query","args"} — sqlite or postgres; plaid:
// {"env","client_id","secret","path"}; stripe: {"secret_key","path"};
// credit_bureau: {"provider","path","auth","score_field",…}; sanctions:
// {"watchlist","threshold"}; mock_bureau: optional {"dataset"}).
type DefineConnector struct {
	Name   string
	Type   ConnectorType
	Config json.RawMessage
}

// Validate requires a name, a known type, and JSON-object config (if present).
func (c DefineConnector) Validate() error {
	if strings.TrimSpace(c.Name) == "" {
		return errors.New("context-layer: connector name is required")
	}
	if !c.Type.Valid() {
		return fmt.Errorf("context-layer: unknown connector type %q (http|graphql|sql|static|plaid|stripe|credit_bureau|sanctions|mock_bureau)", c.Type)
	}
	return validJSONObject("config", c.Config)
}
