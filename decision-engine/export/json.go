// SPDX-License-Identifier: AGPL-3.0-or-later

package export

import (
	"encoding/json"

	"github.com/e6qu/intraktible/decision-engine/events"
)

// FlowExport is the portable, round-trippable JSON form of a flow version: the
// graph and input schema exactly as they would be re-published, plus identifying
// metadata. The {graph, input_schema} subset is precisely what
// `POST /v1/flows/{id}/versions` accepts, so an export can be re-imported as-is.
type FlowExport struct {
	Slug        string          `json:"slug"`
	Name        string          `json:"name"`
	Version     int             `json:"version"`
	Etag        string          `json:"etag"`
	Graph       events.Graph    `json:"graph"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

// JSON renders a flow version as pretty-printed JSON (trailing newline included).
func JSON(f FlowExport) (string, error) {
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b) + "\n", nil
}
