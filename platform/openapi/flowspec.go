// SPDX-License-Identifier: AGPL-3.0-or-later

package openapi

import (
	"encoding/json"
	"fmt"
)

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
	// A published schema that will not decode is not a reason to fall back to the
	// permissive object: this document is a contract that integrators point codegen
	// at, and quietly advertising "any object" for a flow whose real schema is
	// unreadable generates clients that compile and then fail against the API.
	if len(inputSchema) > 0 {
		var s any
		if err := json.Unmarshal(inputSchema, &s); err != nil {
			return nil, fmt.Errorf("openapi: flow %q has an undecodable input schema: %w", slug, err)
		}
		dataSchema = s
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
	idempotencyParam := map[string]any{
		"name": "Idempotency-Key", "in": "header", "required": false,
		"description": "Deduplicates one logical submission within this flow and environment.",
		"schema":      map[string]any{"type": "string", "maxLength": 256},
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
					"parameters": []any{envParam, idempotencyParam},
					"requestBody": jsonBody(map[string]any{
						"type": "object", "required": []string{"data"},
						"properties": map[string]any{
							"data":        dataSchema,
							"entity_type": map[string]any{"type": "string"},
							"entity_id":   map[string]any{"type": "string"},
							"business_reference": map[string]any{
								"type": "string", "maxLength": 256,
							},
							"correlation_id": map[string]any{
								"type": "string", "maxLength": 256,
							},
							"metadata": map[string]any{
								"type": "object", "description": "Persisted caller metadata, capped at 16 KiB.",
							},
							"control": map[string]any{
								"type": "object", "additionalProperties": false,
								"properties": map[string]any{
									"timeout_ms": map[string]any{
										"type": "integer", "minimum": 0, "maximum": 120000,
									},
								},
							},
						},
					}),
					"responses": map[string]any{
						"200": jsonResp("The decision result.", decideResponse),
						"400": map[string]any{"description": "Invalid input or conflicting idempotency-key reuse."},
						"409": map[string]any{"description": "The original idempotent invocation is still in progress."},
					},
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
