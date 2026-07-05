// SPDX-License-Identifier: AGPL-3.0-or-later

// Package eval is the Agent Manager's offline evaluation harness: a set of cases
// (a prompt + an expectation) stored per agent and run on demand against the
// agent's provider, scored pass/fail. Like the decision-engine backtest/assertions,
// it RECORDS NOTHING — runs are computed on demand and returned in the response, so
// an eval over a non-deterministic, billable model never pollutes the run log. It is
// the golden-dataset check an operator runs before publishing a new agent version.
package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/e6qu/intraktible/agent-manager/agents"
	"github.com/e6qu/intraktible/platform/ai"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// Stream + storage for eval cases (the dataset is event-sourced; results are not).
const (
	Stream     = "agents.evals"
	Collection = "agent_evals"
	TypeSet    = "agents.evals_set"
	maxCases   = 200
)

// Mode is how a case's output is scored.
type Mode string

const (
	ModeContains   Mode = "contains"    // the output text contains Expect
	ModeEquals     Mode = "equals"      // the output text equals Expect (trimmed)
	ModeJSONSubset Mode = "json_subset" // the structured output is a superset of ExpectJSON
)

// Valid reports whether m is a known mode.
func (m Mode) Valid() bool { return m == ModeContains || m == ModeEquals || m == ModeJSONSubset }

// Case is one eval: a prompt fed to the agent and the expectation its output must
// meet. Mode selects the scorer (default contains).
type Case struct {
	Name       string          `json:"name"`
	Prompt     string          `json:"prompt"`
	Mode       Mode            `json:"mode,omitempty"`
	Expect     string          `json:"expect,omitempty"`      // text for contains/equals
	ExpectJSON json.RawMessage `json:"expect_json,omitempty"` // object for json_subset
}

// Set is the whole-set replacement of an agent's eval cases.
type Set struct {
	Agent string `json:"agent"`
	Cases []Case `json:"cases"`
}

// Result is one case's outcome after running it through the provider.
type Result struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Status string `json:"status"`           // the run status (completed|failed)
	Output string `json:"output,omitempty"` // text or structured JSON the model returned
	Detail string `json:"detail,omitempty"` // why it failed (mismatch / error)
}

// Report is the rollup of an eval run.
type Report struct {
	Total   int      `json:"total"`
	Passed  int      `json:"passed"`
	Failed  int      `json:"failed"`
	Version int      `json:"version"` // the agent version evaluated (0 = latest)
	Results []Result `json:"results"`
}

// View is the stored eval-case set for an agent.
type View struct {
	Org       string    `json:"org"`
	Workspace string    `json:"workspace"`
	Agent     string    `json:"agent"`
	Cases     []Case    `json:"cases"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Projector folds the eval stream into per-agent case sets.
type Projector struct{}

func (Projector) Name() string          { return Collection }
func (Projector) Collections() []string { return []string{Collection} }

func (Projector) Apply(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	if e.Type != TypeSet {
		return nil
	}
	var p Set
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("agent_evals: decode set seq %d: %w", e.Seq, err)
	}
	return store.PutDoc(ctx, s, Collection, store.Key(e.Org, e.Workspace, p.Agent), View{
		Org: e.Org, Workspace: e.Workspace, Agent: p.Agent, Cases: p.Cases, UpdatedAt: e.Time,
	})
}

// Read returns an agent's stored eval cases.
func Read(ctx context.Context, s store.Store, id identity.Identity, agent string) (View, bool, error) {
	return store.GetDoc[View](ctx, s, Collection, store.Key(id.Org, id.Workspace, agent))
}

// Handler is the eval write side: it persists the case set.
type Handler struct {
	log eventlog.Log
	now func() time.Time
}

// NewHandler builds an eval command handler.
func NewHandler(log eventlog.Log) *Handler {
	return &Handler{log: log, now: func() time.Time { return time.Now().UTC() }}
}

// WithNow overrides the clock (deterministic tests, the demo seeder) and
// returns the handler.
func (h *Handler) WithNow(now func() time.Time) *Handler {
	h.now = now
	return h
}

// SetCases replaces an agent's eval cases (whole-set, like assertions).
func (h *Handler) SetCases(ctx context.Context, id identity.Identity, agent string, cases []Case) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if agent == "" {
		return eventlog.Envelope{}, fmt.Errorf("eval: agent is required")
	}
	if len(cases) > maxCases {
		return eventlog.Envelope{}, fmt.Errorf("eval: too many cases (%d > %d)", len(cases), maxCases)
	}
	for i, c := range cases {
		if c.Name == "" {
			return eventlog.Envelope{}, fmt.Errorf("eval: case %d has no name", i)
		}
		if c.Mode != "" && !c.Mode.Valid() {
			return eventlog.Envelope{}, fmt.Errorf("eval: case %q has invalid mode %q", c.Name, c.Mode)
		}
	}
	return eventlog.AppendJSON(ctx, h.log, id.Org, id.Workspace, id.Actor, Stream, TypeSet, h.now(), Set{Agent: agent, Cases: cases})
}

// Run executes an agent's eval cases against a resolved version (0 = latest) and
// returns a scored report — recording nothing. A misconfigured provider/tool errors
// out; a per-case provider failure is a failed case, not an error.
func Run(ctx context.Context, s store.Store, reg *ai.Registry, tb agents.Toolbox, id identity.Identity, agent string, version int, cases []Case) (Report, error) {
	cfg, ok, err := agents.ReadConfig(ctx, s, id, agent, version)
	if err != nil {
		return Report{}, err
	}
	if !ok {
		return Report{}, fmt.Errorf("eval: unknown agent %q version %d", agent, version)
	}
	rep := Report{Total: len(cases), Version: version, Results: make([]Result, 0, len(cases))}
	for _, c := range cases {
		out, ierr := agents.InvokeConfig(ctx, reg, tb, id, cfg, c.Prompt)
		if ierr != nil {
			return Report{}, ierr // misconfiguration (unknown provider/tool) — fail loudly
		}
		res := score(c, out)
		if res.Passed {
			rep.Passed++
		} else {
			rep.Failed++
		}
		rep.Results = append(rep.Results, res)
	}
	return rep, nil
}

// score evaluates one case's outcome against its expectation.
func score(c Case, out agents.Outcome) Result {
	res := Result{Name: c.Name, Status: string(out.Status)}
	if out.Error != "" {
		res.Detail = out.Error
		return res
	}
	mode := c.Mode
	if mode == "" {
		mode = ModeContains
	}
	switch mode {
	case ModeContains:
		res.Output = out.Text
		res.Passed = strings.Contains(out.Text, c.Expect)
		if !res.Passed {
			res.Detail = fmt.Sprintf("output does not contain %q", c.Expect)
		}
	case ModeEquals:
		res.Output = out.Text
		res.Passed = strings.TrimSpace(out.Text) == strings.TrimSpace(c.Expect)
		if !res.Passed {
			res.Detail = fmt.Sprintf("output %q != expected %q", out.Text, c.Expect)
		}
	case ModeJSONSubset:
		res.Output = string(out.Structured)
		miss := jsonSubsetMismatch(c.ExpectJSON, out.Structured)
		res.Passed = len(miss) == 0
		if !res.Passed {
			res.Detail = "missing/mismatched fields: " + strings.Join(miss, ", ")
		}
	}
	return res
}

// jsonSubsetMismatch returns the expected keys absent or unequal in got (every key
// in expect must be present in got and deep-equal); extra fields in got are ignored.
// Mirrors the decision-engine assertions subset matcher.
func jsonSubsetMismatch(expect, got json.RawMessage) []string {
	var want, have map[string]any
	if err := json.Unmarshal(expect, &want); err != nil {
		return []string{"expectation is not a JSON object"}
	}
	if err := json.Unmarshal(got, &have); err != nil {
		return []string{"output is not a JSON object"}
	}
	var miss []string
	for k, wv := range want {
		gv, ok := have[k]
		if !ok || !reflect.DeepEqual(wv, gv) {
			miss = append(miss, k)
		}
	}
	return miss
}
