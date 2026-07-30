// SPDX-License-Identifier: AGPL-3.0-or-later

package domain

import (
	"context"
	"fmt"

	"github.com/e6qu/intraktible/decision-engine/events"
)

// EffectKind identifies an imperative operation requested by the pure graph
// interpreter. The shell owns authorization, I/O, retries, and durable evidence;
// the core only describes the effect at the exact graph position that was reached.
type EffectKind string

const (
	EffectConnect EffectKind = "connect"
	EffectAI      EffectKind = "ai"
	EffectPredict EffectKind = "predict"
)

// EffectRequest is the complete, deterministic description of an effectful node.
// Input is the record as it exists at this graph position, including every upstream
// assignment/rule/code/effect result. It is data for the shell, never executable I/O.
type EffectRequest struct {
	Kind            EffectKind
	NodeID          string
	Reference       string
	Version         int
	Output          string
	Prompt          string
	RequiresConsent string
	SharesNPI       bool
	Input           map[string]any
}

// ExecutionState is the serializable pure-core state between effects. NextNode and
// Record are sufficient to resume execution; Results and Steps preserve the trace
// and enforce the graph bound when a run crosses multiple shell calls.
type ExecutionState struct {
	NextNode string         `json:"next_node"`
	Record   map[string]any `json:"record"`
	Results  []NodeResult   `json:"results,omitempty"`
	Steps    int            `json:"steps"`
}

// ExecutionStep contains exactly one of Effect or Run. An Effect means the shell
// must perform the requested operation and call ResolveEffect before advancing.
// A Run is terminal or suspended.
type ExecutionStep struct {
	State  ExecutionState
	Effect *EffectRequest
	Run    *Run
}

// StartExecution creates the initial pure state for a graph and caller input.
func StartExecution(g events.Graph, input map[string]any) ExecutionState {
	return ExecutionState{
		NextNode: inputNode(g),
		Record:   cloneContext(input),
		Results:  []NodeResult{},
	}
}

// ResumeExecution creates the state after a reviewer outcome has been injected
// into a suspended human task. Effects downstream of the review are consequently
// requested only after the review and see the reviewer's actual outcome.
func ResumeExecution(s SuspendState, outcome map[string]any) ExecutionState {
	record := cloneContext(s.Record)
	key := s.OutputKey
	if key == "" {
		key = "review"
	}
	record[key] = outcome
	for k, v := range outcome {
		record[k] = v
	}
	return ExecutionState{NextNode: s.Resume, Record: record, Results: []NodeResult{}}
}

// AdvanceExecution evaluates pure nodes until the graph completes, suspends, fails,
// or reaches an effectful node. It performs no I/O and is deterministic for a given
// graph and state.
func AdvanceExecution(runCtx context.Context, g events.Graph, state ExecutionState, obs NodeObserver) ExecutionStep {
	nodes, outgoing := indexGraph(g)
	run := Run{Status: StatusCompleted, Results: state.Results}
	if state.NextNode == "" {
		if state.Steps == 0 {
			r := fail(run, "", "decision-engine: graph has no input node")
			return ExecutionStep{State: state, Run: &r}
		}
		run.Output = cloneContext(state.Record)
		return ExecutionStep{State: state, Run: &run}
	}

	ec := evalContext{ctx: runCtx}
	for state.Steps <= len(g.Nodes) {
		n, ok := nodes[state.NextNode]
		if !ok {
			r := fail(run, state.NextNode, fmt.Sprintf("decision-engine: edge to unknown node %q", state.NextNode))
			return ExecutionStep{State: state, Run: &r}
		}
		if effectNode(n.Type) {
			req, err := requestEffect(n, state.Record)
			if err != nil {
				state.Results = append(state.Results, NodeResult{NodeID: n.ID, Type: n.Type})
				run.Results = state.Results
				r := fail(run, n.ID, err.Error())
				return ExecutionStep{State: state, Run: &r}
			}
			return ExecutionStep{State: state, Effect: &req}
		}

		var (
			output any
			next   string
			err    error
		)
		if obs != nil {
			done := obs.NodeStart(n.ID, n.Type)
			output, next, err = evalNode(ec, n, state.Record, outgoing[n.ID])
			done(err)
		} else {
			output, next, err = evalNode(ec, n, state.Record, outgoing[n.ID])
		}
		state.Results = append(state.Results, NodeResult{NodeID: n.ID, Type: n.Type, Output: toJSON(output)})
		state.Steps++
		run.Results = state.Results
		if err != nil {
			r := fail(run, n.ID, err.Error())
			return ExecutionStep{State: state, Run: &r}
		}
		if n.Type == events.NodeManualReview {
			var cfg manualReviewConfig
			if derr := decodeConfig(n, &cfg); derr == nil && cfg.Suspend {
				run.Status = StatusSuspended
				run.Suspend = &SuspendState{
					NodeID: n.ID, Resume: next, OutputKey: cfg.OutputKey,
					Record: cloneContext(state.Record), Case: caseFrom(output),
				}
				state.NextNode = next
				return ExecutionStep{State: state, Run: &run}
			}
		}
		if n.Type == events.NodeOutput {
			run.Output = asMap(output)
			state.NextNode = ""
			return ExecutionStep{State: state, Run: &run}
		}
		if next == "" {
			r := fail(run, n.ID, fmt.Sprintf("decision-engine: flow dead-ends at non-output node %q", n.ID))
			return ExecutionStep{State: state, Run: &r}
		}
		state.NextNode = next
	}
	r := fail(run, state.NextNode, "decision-engine: execution exceeded the node bound")
	return ExecutionStep{State: state, Run: &r}
}

// ResolveEffect feeds one shell-produced result into the effectful node currently
// awaiting it. The result is injected under the node's reserved namespace and the
// node is appended to the trace exactly once.
func ResolveEffect(g events.Graph, state ExecutionState, req EffectRequest, value any) (ExecutionState, error) {
	if state.NextNode == "" || state.NextNode != req.NodeID {
		return state, fmt.Errorf("decision-engine: effect result for node %q does not match awaiting node %q", req.NodeID, state.NextNode)
	}
	nodes, outgoing := indexGraph(g)
	n, ok := nodes[state.NextNode]
	if !ok {
		return state, fmt.Errorf("decision-engine: effect result targets unknown node %q", state.NextNode)
	}
	current, err := requestEffect(n, state.Record)
	if err != nil {
		return state, err
	}
	if current.Kind != req.Kind || current.Reference != req.Reference || current.Version != req.Version || current.Output != req.Output {
		return state, fmt.Errorf("decision-engine: effect result does not match the current configuration of node %q", req.NodeID)
	}

	record := cloneContext(state.Record)
	bucketName := string(req.Kind)
	bucket := map[string]any{}
	if existing, ok := record[bucketName].(map[string]any); ok {
		for k, v := range existing {
			bucket[k] = v
		}
	}
	bucket[req.Output] = value
	record[bucketName] = bucket
	state.Record = record
	state.Results = append(state.Results, NodeResult{
		NodeID: n.ID,
		Type:   n.Type,
		Output: toJSON(map[string]any{req.Output: value}),
	})
	state.Steps++
	state.NextNode = firstEdge(outgoing[n.ID])
	return state, nil
}

// FailEffect turns a shell failure at the currently awaited effect into the same
// explicit failed Run shape as a pure-node error. The shell can then durably record
// the node and terminal failure instead of leaving a started decision stranded.
func FailEffect(state ExecutionState, req EffectRequest, cause error) Run {
	run := Run{Status: StatusCompleted, Results: state.Results}
	run.Results = append(run.Results, NodeResult{
		NodeID: req.NodeID,
		Type:   nodeTypeForEffect(req.Kind),
	})
	return fail(run, req.NodeID, cause.Error())
}

func effectNode(t events.NodeType) bool {
	return t == events.NodeConnect || t == events.NodeAI || t == events.NodePredict
}

func requestEffect(n events.Node, record map[string]any) (EffectRequest, error) {
	req := EffectRequest{NodeID: n.ID, Input: cloneContext(record)}
	switch n.Type {
	case events.NodeConnect:
		var cfg connectConfig
		if err := decodeConfig(n, &cfg); err != nil {
			return EffectRequest{}, err
		}
		if cfg.Connector == "" || cfg.Output == "" {
			return EffectRequest{}, fmt.Errorf("decision-engine: connect node %q needs a connector and an output", n.ID)
		}
		req.Kind, req.Reference, req.Output = EffectConnect, cfg.Connector, cfg.Output
		req.RequiresConsent, req.SharesNPI = cfg.RequiresConsent, cfg.SharesNPI
	case events.NodeAI:
		var cfg aiConfig
		if err := decodeConfig(n, &cfg); err != nil {
			return EffectRequest{}, err
		}
		if cfg.Agent == "" || cfg.Output == "" {
			return EffectRequest{}, fmt.Errorf("decision-engine: ai node %q needs an agent and an output", n.ID)
		}
		req.Kind, req.Reference, req.Version, req.Output, req.Prompt = EffectAI, cfg.Agent, cfg.Version, cfg.Output, cfg.Prompt
	case events.NodePredict:
		var cfg predictConfig
		if err := decodeConfig(n, &cfg); err != nil {
			return EffectRequest{}, err
		}
		if cfg.Model == "" || cfg.Output == "" {
			return EffectRequest{}, fmt.Errorf("decision-engine: predict node %q needs a model and an output", n.ID)
		}
		req.Kind, req.Reference, req.Output = EffectPredict, cfg.Model, cfg.Output
	default:
		return EffectRequest{}, fmt.Errorf("decision-engine: node %q of type %q is not effectful", n.ID, n.Type)
	}
	return req, nil
}
