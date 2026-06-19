// SPDX-License-Identifier: AGPL-3.0-or-later

package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Connector types. HTTP calls an arbitrary configured REST endpoint (the "Custom
// Connect" case); SQL runs a parameterized query against a configured database;
// MockBureau is a deterministic in-process reference connector.
const (
	ConnectorHTTP       = "http"
	ConnectorSQL        = "sql"
	ConnectorGraphQL    = "graphql"
	ConnectorStatic     = "static"
	ConnectorMockBureau = "mock_bureau"
)

var connectorTypes = map[string]bool{
	ConnectorHTTP:       true,
	ConnectorSQL:        true,
	ConnectorGraphQL:    true,
	ConnectorStatic:     true,
	ConnectorMockBureau: true,
}

// ValidConnectorType reports whether t is a known connector type.
func ValidConnectorType(t string) bool { return connectorTypes[t] }

// DefineConnector registers (or redefines) a named connector. Config is
// type-specific JSON (http: {"url","method"}; sql: {"dsn","query","args"};
// mock_bureau: optional {"dataset"}).
type DefineConnector struct {
	Name   string
	Type   string
	Config json.RawMessage
}

// Validate requires a name, a known type, and JSON-object config (if present).
func (c DefineConnector) Validate() error {
	if strings.TrimSpace(c.Name) == "" {
		return errors.New("context-layer: connector name is required")
	}
	if !ValidConnectorType(c.Type) {
		return fmt.Errorf("context-layer: unknown connector type %q (http|sql|mock_bureau)", c.Type)
	}
	return validJSONObject("config", c.Config)
}
