// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build js

package connectors

import (
	"context"
	"encoding/json"
	"errors"
)

// The SQL connector needs the sqlite driver, which cannot compile for js/wasm.
// Defining one in a wasm deployment fails loudly at the factory.
type sqlConnector struct{}

func newSQL(json.RawMessage) (sqlConnector, error) {
	return sqlConnector{}, errors.New(`context-layer: the "sql" connector type is not available in wasm builds`)
}

func (sqlConnector) Fetch(context.Context, json.RawMessage) (json.RawMessage, error) {
	return nil, errors.New(`context-layer: the "sql" connector type is not available in wasm builds`)
}
