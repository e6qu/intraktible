// SPDX-License-Identifier: AGPL-3.0-or-later

package openapi

import "encoding/json"

// ForFlow builds a self-contained OpenAPI 3.1 document for a single flow's decision
// API: the per-environment decide and decide/batch endpoints, with the flow's
// published input schema (when present) as the request `data` schema. Integrators
// point codegen / Swagger UI at GET /v1/flows/{slug}/openapi.json to get a contract
// specific to one flow rather than the whole platform surface.
func ForFlow(slug, name string, inputSchema json.RawMessage) ([]byte, error) {
	// The decide request's `data` is the flow's input. Use the published JSON Schema
	// verbatim when available, else a permissive object.
	var dataSchema any = map[string]any{
		"type":        "object",
		"description": "The decision input fields for this flow.",
	}
	if len(inputSchema) > 0 {
		var s any
		if err := json.Unmarshal(inputSchema, &s); err == nil {
			dataSchema = s
		}
	}

	// The per-flow contract is a view of the same API, so it carries the same
	// version as the main embedded spec rather than a second hardcoded literal.
	parsed, err := Parse()
	if err != nil {
		return nil, err
	}

	envParam := map[string]any{
		"name": "env", "in": "path", "required": true,
		"description": "Target environment.",
		"schema":      map[string]any{"type": "string", "enum": []string{"sandbox", "production"}},
	}
	decideResponse := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"decision_id":        map[string]any{"type": "string"},
			"status":             map[string]any{"type": "string", "enum": []string{"completed", "failed", "suspended"}},
			"data":               map[string]any{"type": "object"},
			"disposition":        map[string]any{"type": "string"},
			"disposition_reason": map[string]any{"type": "string", "description": "What assigned the disposition (a policy band, or \"pre-approval honored\")."},
			"preapproval_id":     map[string]any{"type": "string", "description": "The grant this decision was served from, when honored from a pre-approval."},
			"error":              map[string]any{"type": "string"},
		},
	}
	jsonBody := func(schema any) map[string]any {
		return map[string]any{
			"required": true,
			"content":  map[string]any{"application/json": map[string]any{"schema": schema}},
		}
	}
	jsonResp := func(desc string, schema any) map[string]any {
		return map[string]any{
			"description": desc,
			"content":     map[string]any{"application/json": map[string]any{"schema": schema}},
		}
	}

	base := "/v1/flows/" + slug + "/{env}"
	doc := map[string]any{
		"openapi": "3.1.0",
		"info": map[string]any{
			"title":       name + " — decision API",
			"version":     parsed.Info.Version,
			"description": "Generated decision contract for flow \"" + slug + "\". Authenticate with an API key (X-Api-Key) scoped to the target environment.",
		},
		"paths": map[string]any{
			base + "/decide": map[string]any{
				"post": map[string]any{
					"summary":    "Decide a single input against this flow",
					"parameters": []any{envParam},
					"requestBody": jsonBody(map[string]any{
						"type": "object", "required": []string{"data"},
						"properties": map[string]any{
							"data":        dataSchema,
							"entity_type": map[string]any{"type": "string"},
							"entity_id":   map[string]any{"type": "string"},
						},
					}),
					"responses": map[string]any{"200": jsonResp("The decision result.", decideResponse)},
				},
			},
			base + "/decide/batch": map[string]any{
				"post": map[string]any{
					"summary":    "Decide an array of input rows against this flow",
					"parameters": []any{envParam},
					"requestBody": jsonBody(map[string]any{
						"type": "object", "required": []string{"dataset"},
						"properties": map[string]any{
							"dataset": map[string]any{
								"type": "array", "items": dataSchema,
								"description": "Up to 500 input rows; each is decided and recorded.",
							},
						},
					}),
					"responses": map[string]any{"200": jsonResp("Per-row decision results with totals.", map[string]any{"type": "object"})},
				},
			},
		},
	}
	return json.MarshalIndent(doc, "", "  ")
}
