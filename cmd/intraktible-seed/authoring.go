// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"net/http"
	"net/url"
	"time"
)

func reusableGateGraph() map[string]any {
	return map[string]any{
		"nodes": []map[string]any{
			{"id": "input", "type": "input"},
			{"id": "gate", "type": "rule", "name": "Affordability gate"},
			{"id": "output", "type": "output"},
		},
		"edges": []map[string]any{
			{"from": "input", "to": "gate"},
			{"from": "gate", "to": "output"},
		},
	}
}

func reusableConsumerGraph(componentID string, version int) map[string]any {
	return map[string]any{
		"nodes": []map[string]any{
			{"id": "input", "type": "input"},
			{
				"id": "affordability", "type": "subflow", "name": "Shared affordability",
				"config": map[string]any{"component_id": componentID, "version": version},
			},
			{"id": "output", "type": "output"},
		},
		"edges": []map[string]any{
			{"from": "input", "to": "affordability"},
			{"from": "affordability", "to": "output"},
		},
	}
}

func componentInputSchema(required ...string) map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"risk_score": map[string]any{"type": "number"},
			"segment":    map[string]any{"type": "string"},
			"country":    map[string]any{"type": "string"},
		},
		"required":             required,
		"additionalProperties": true,
	}
}

func componentOutputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"eligible": map[string]any{"type": "boolean"},
			"band":     map[string]any{"type": "string"},
		},
		"required": []string{"eligible"},
	}
}

// authoringActions leaves a replayable collaboration story in the demo:
// compatible and breaking component contracts, one published review artifact,
// one dependency upgrade awaiting review, and one active autosaved draft.
func (s *seeder) authoringActions(anchor time.Time) []action {
	at := anchor.Add(-96 * time.Hour)
	step := func(name string, run func()) action {
		at = at.Add(10 * time.Minute)
		return action{at: at, name: name, run: run}
	}
	actions := []action{
		step("create reusable affordability gate", func() {
			var response struct {
				ComponentID string `json:"component_id"`
			}
			s.callWithHeaders(
				actorPriya, http.MethodPost, "/v1/authoring/components",
				map[string]string{"Idempotency-Key": "demo-component-affordability"},
				map[string]any{
					"slug": "affordability-gate", "name": "Affordability gate",
					"description": "Shared affordability contract for lending flows.",
				}, &response,
			)
			s.setID("component:affordability", response.ComponentID)
		}),
		step("publish reusable affordability v1", func() {
			s.callWithHeaders(
				actorPriya, http.MethodPost,
				"/v1/authoring/components/"+s.id("component:affordability")+"/versions",
				map[string]string{"Idempotency-Key": "demo-component-affordability-v1"},
				map[string]any{
					"graph":         reusableGateGraph(),
					"input_schema":  componentInputSchema("risk_score"),
					"output_schema": componentOutputSchema(),
				}, nil,
			)
		}),
		step("publish compatible affordability v2", func() {
			s.callWithHeaders(
				actorPriya, http.MethodPost,
				"/v1/authoring/components/"+s.id("component:affordability")+"/versions",
				map[string]string{"Idempotency-Key": "demo-component-affordability-v2"},
				map[string]any{
					"graph":        reusableGateGraph(),
					"input_schema": componentInputSchema("risk_score"),
					"output_schema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"eligible": map[string]any{"type": "boolean"},
							"band":     map[string]any{"type": "string"},
						},
						"required": []string{"eligible", "band"},
					},
				}, nil,
			)
		}),
		step("publish breaking affordability v3", func() {
			s.callWithHeaders(
				actorPriya, http.MethodPost,
				"/v1/authoring/components/"+s.id("component:affordability")+"/versions",
				map[string]string{"Idempotency-Key": "demo-component-affordability-v3"},
				map[string]any{
					"graph":                  reusableGateGraph(),
					"input_schema":           componentInputSchema("risk_score", "country"),
					"output_schema":          componentOutputSchema(),
					"allow_breaking":         true,
					"breaking_change_reason": "Country becomes mandatory; consumers need a reviewed data-binding migration.",
				}, nil,
			)
		}),
		step("create reusable flow draft", func() {
			var response struct {
				DraftID string `json:"draft_id"`
			}
			s.callWithHeaders(
				actorPriya, http.MethodPost, "/v1/authoring/drafts",
				map[string]string{"Idempotency-Key": "demo-limit-component-draft"},
				map[string]any{
					"flow_id": s.flowID("limit-increase"), "base_version": 1,
					"title": "Adopt shared affordability gate",
					"graph": reusableConsumerGraph(s.id("component:affordability"), 1),
				}, &response,
			)
			s.setID("draft:limit-component-v1", response.DraftID)
		}),
		step("autosave reusable flow draft", func() {
			s.call(
				actorPriya, http.MethodPut,
				"/v1/authoring/drafts/"+s.id("draft:limit-component-v1"),
				map[string]any{
					"expected_revision": 1, "title": "Adopt shared affordability gate",
					"graph": reusableConsumerGraph(s.id("component:affordability"), 1),
				}, nil,
			)
		}),
		step("create reusable flow changeset", func() {
			var response struct {
				ChangeSetID string `json:"changeset_id"`
			}
			s.callWithHeaders(
				actorPriya, http.MethodPost, "/v1/authoring/changesets",
				map[string]string{"Idempotency-Key": "demo-limit-component-changeset"},
				map[string]any{
					"draft_id": s.id("draft:limit-component-v1"), "draft_revision": 2,
					"title":           "Adopt shared affordability gate",
					"rationale":       "Replace duplicated lending logic with an exact reusable contract.",
					"required_checks": []string{"flow-validation"},
					"reviewers":       []string{actorMarcus},
				}, &response,
			)
			s.setID("changeset:limit-component-v1", response.ChangeSetID)
		}),
		step("validate reusable flow changeset", func() {
			s.call(
				actorPriya, http.MethodPost,
				"/v1/authoring/changesets/"+s.id("changeset:limit-component-v1")+"/checks",
				map[string]any{"name": "flow-validation", "status": "passed"}, nil,
			)
		}),
		step("submit reusable flow changeset", func() {
			s.call(
				actorPriya, http.MethodPost,
				"/v1/authoring/changesets/"+s.id("changeset:limit-component-v1")+"/submit",
				map[string]any{}, nil,
			)
		}),
		step("approve reusable flow changeset", func() {
			s.call(
				actorMarcus, http.MethodPost,
				"/v1/authoring/changesets/"+s.id("changeset:limit-component-v1")+"/review",
				map[string]any{
					"decision": "approve",
					"reason":   "The exact pin and expanded runtime graph match the validation evidence.",
				}, nil,
			)
		}),
		step("publish reusable flow changeset", func() {
			s.call(
				actorMarcus, http.MethodPost,
				"/v1/authoring/changesets/"+s.id("changeset:limit-component-v1")+"/publish",
				map[string]any{}, nil,
			)
		}),
		step("create compatible dependency upgrade", func() {
			var response struct {
				DraftID string `json:"draft_id"`
			}
			s.callWithHeaders(
				actorPriya, http.MethodPost, "/v1/authoring/drafts",
				map[string]string{"Idempotency-Key": "demo-limit-component-v2-draft"},
				map[string]any{
					"flow_id": s.flowID("limit-increase"), "base_version": 2,
					"title": "Upgrade affordability gate to v2",
					"graph": reusableConsumerGraph(s.id("component:affordability"), 2),
				}, &response,
			)
			s.setID("draft:limit-component-v2", response.DraftID)
		}),
		step("submit compatible dependency upgrade", func() {
			var response struct {
				ChangeSetID string `json:"changeset_id"`
			}
			s.callWithHeaders(
				actorPriya, http.MethodPost, "/v1/authoring/changesets",
				map[string]string{"Idempotency-Key": "demo-limit-component-v2-changeset"},
				map[string]any{
					"draft_id": s.id("draft:limit-component-v2"), "draft_revision": 1,
					"title":           "Upgrade affordability gate to v2",
					"rationale":       "Adopt the compatible output band while preserving the input contract.",
					"required_checks": []string{"flow-validation"},
					"reviewers":       []string{actorMarcus},
				}, &response,
			)
			s.setID("changeset:limit-component-v2", response.ChangeSetID)
			s.call(
				actorPriya, http.MethodPost,
				"/v1/authoring/changesets/"+response.ChangeSetID+"/checks",
				map[string]any{"name": "flow-validation", "status": "passed"}, nil,
			)
			s.call(
				actorPriya, http.MethodPost,
				"/v1/authoring/changesets/"+response.ChangeSetID+"/submit",
				map[string]any{}, nil,
			)
		}),
		step("discuss exact dependency upgrade", func() {
			subject := s.flowID("limit-increase") + ":" + s.id("changeset:limit-component-v2")
			s.call(
				actorPriya, http.MethodPost,
				"/v1/comments/changeset/"+url.PathEscape(subject),
				map[string]any{
					"body": "@marcus.reed The server marks v1→v2 compatible. Please verify the new output band evidence.",
				}, nil,
			)
		}),
		step("create active credit draft", func() {
			var response struct {
				DraftID string `json:"draft_id"`
			}
			s.callWithHeaders(
				actorPriya, http.MethodPost, "/v1/authoring/drafts",
				map[string]string{"Idempotency-Key": "demo-credit-active-draft"},
				map[string]any{
					"flow_id": s.flowID("credit-decision"), "base_version": 3,
					"title":        "Tune near-prime referral threshold",
					"graph":        creditGraphV3(),
					"input_schema": creditSchema(),
				}, &response,
			)
			s.setID("draft:credit-active", response.DraftID)
		}),
		step("autosave active credit draft", func() {
			s.call(
				actorPriya, http.MethodPut,
				"/v1/authoring/drafts/"+s.id("draft:credit-active"),
				map[string]any{
					"expected_revision": 1,
					"title":             "Tune near-prime referral threshold — evidence pending",
					"graph":             creditGraphV3(),
					"input_schema":      creditSchema(),
				}, nil,
			)
		}),
	}
	return actions
}
