// SPDX-License-Identifier: AGPL-3.0-or-later

package domain

import (
	"encoding/json"

	"github.com/e6qu/intraktible/platform/schema"
)

// ValidateInput checks a decide call's input against a flow version's input schema
// (a supported JSON-Schema subset; see platform/schema). An empty schema is no
// contract.
func ValidateInput(inputSchema json.RawMessage, data map[string]any) error {
	return schema.ValidateObject(inputSchema, data)
}
