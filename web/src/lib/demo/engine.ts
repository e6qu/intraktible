// SPDX-License-Identifier: AGPL-3.0-or-later
// The demo decision engine's store-facing layer. The pure interpreter (expression
// evaluator, model evaluation, graph validation, flow walker) lives in walk.ts so
// the seed can execute the same semantics at module init; this module binds it to
// the live store: version/variant resolution over a flow's deployments, policy
// disposition binding, decision recording, assertions, agent-eval scoring and
// backtests. Re-exports the pure core so every existing import keeps one entry
// point ('./engine').

import type {
  Decision,
  DecideResult,
  FlowGraph,
  Flow,
  AssertionCase,
  AssertionResult,
  AssertionReport,
  EvalCase,
  EvalResult,
  ReasonCode,
  Disposition,
  RunStatus,
  Environment
} from '$lib/api';
import { state, nextId, auditDecisionRun } from './store';
import { agentReply, type AgentSchema } from './agent';
import { walkGraph, applyPolicySpec, resolvePath, type WalkResult } from './walk';

export { evalExpr, evaluateModel, validateGraph, resolvePath, num, type Prediction } from './walk';

// pickVersion resolves the graph for a flow in an environment (the deployed champion —
// or, for the challenger variant, the deployed challenger version — else latest), and
// the version number used for the recorded decision.
export function pickVersion(
  flow: Flow,
  env: string,
  variant: 'champion' | 'challenger' = 'champion'
): { graph: FlowGraph; version: number } {
  const dep = Object.entries(flow.deployments ?? {}).find(([e]) => e === env)?.[1];
  const deployed =
    variant === 'challenger' && dep?.challenger_version ? dep.challenger_version : dep?.version;
  const version = deployed ?? flow.latest;
  const v = flow.versions.find((x) => x.version === version) ?? flow.versions.at(-1);
  return { graph: v?.graph ?? { nodes: [], edges: [] }, version: v?.version ?? 1 };
}

// The A/B routing draw in [0,100), mirroring the real engine's rollPercent. Tests pin
// it via setRollPercent so champion/challenger routing is deterministic.
const defaultRoll = (): number => Math.floor(Math.random() * 100);
let roll = defaultRoll;
export function setRollPercent(fn: (() => number) | null): void {
  roll = fn ?? defaultRoll;
}

// resolveVariant mirrors the real engine's resolveVersion draw: when the environment's
// deployment carries a challenger, challenger_pct percent of traffic runs it; everything
// else runs the champion.
export function resolveVariant(flow: Flow, env: string): 'champion' | 'challenger' {
  const dep = Object.entries(flow.deployments ?? {}).find(([e]) => e === env)?.[1];
  return dep?.challenger_version && roll() < (dep.challenger_pct ?? 0) ? 'challenger' : 'champion';
}

export interface DecideOptions {
  record?: boolean; // append a Decision to the store (default true)
  variant?: 'champion' | 'challenger';
}

// runFlow walks a FlowGraph against the live store's models, then binds the flow's
// policy (by flow_slug) to derive a disposition over the terminal record.
export function runFlow(
  flow: Flow,
  graph: FlowGraph,
  input: Record<string, unknown>,
  resume?: { outcome: Record<string, unknown> }
): WalkResult & { disposition?: Disposition; dispositionReason?: string } {
  const run = walkGraph(graph, input, state.models, resume);
  if (run.status !== 'completed') return run;
  const banded = dispositionFor(flow.slug, run.data, run.reasonCodes);
  return { ...run, disposition: banded.disposition, dispositionReason: banded.reason };
}

// dispositionFor evaluates the flow's bound policy (matched by flow_slug) over the
// record, appending the matched rule's reason code. Returns empty when no policy is
// bound (the flow's own output stands).
function dispositionFor(
  slug: string,
  rec: Record<string, unknown>,
  reasonCodes: ReasonCode[]
): { disposition?: Disposition; reason?: string } {
  const policy = state.policies.find((p) => p.flow_slug === slug);
  if (!policy) return {};
  const spec =
    policy.versions.find((v) => v.version === policy.latest)?.spec ?? policy.versions.at(-1)?.spec;
  if (!spec) return {};
  return applyPolicySpec(spec, rec, reasonCodes);
}

// decide runs a flow for an environment, records a Decision (unless record:false)
// and returns a DecideResult plus the recorded decision id. Opens a case when a
// manual_review node fired.
export function decideFlow(
  flow: Flow,
  env: Environment,
  input: Record<string, unknown>,
  opts: DecideOptions = {}
): { result: DecideResult; decision?: Decision } {
  // A/B routing: a deployed challenger receives challenger_pct percent of the
  // environment's traffic (the real engine's resolveVersion) unless the caller
  // forces a variant.
  const variant = opts.variant ?? resolveVariant(flow, env);
  const { graph, version } = pickVersion(flow, env, variant);
  const startedAt = new Date().toISOString();
  const run = runFlow(flow, graph, input);
  const decisionId = nextId('dec');
  const durationMs = 20 + Math.floor(Math.random() * 60);
  const result: DecideResult = {
    decision_id: decisionId,
    status: run.status,
    // The real engine's record accumulates the compliance trail under reason_codes,
    // so the decide response's data carries it (the verdict UI renders it as badges).
    data: { ...run.output, reason_codes: run.reasonCodes },
    disposition: run.disposition,
    disposition_reason: run.dispositionReason,
    error: run.error
  };

  if (opts.record === false) {
    return { result };
  }

  const decision: Decision = {
    decision_id: decisionId,
    flow_id: flow.flow_id,
    slug: flow.slug,
    version,
    environment: env,
    variant,
    status: run.status,
    data: run.data,
    output: run.output,
    reason_codes: run.reasonCodes,
    disposition: run.disposition,
    disposition_reason: run.dispositionReason,
    policy_id: state.policies.find((p) => p.flow_slug === flow.slug)?.policy_id,
    nodes: run.nodes,
    started_at: startedAt,
    ended_at: new Date().toISOString(),
    duration_ms: durationMs
  };
  state.decisions.unshift(decision);

  if (run.caseOpened) {
    const caseId = nextId('case');
    state.cases.unshift({
      case_id: caseId,
      company_name: String(
        run.caseOpened.company_name ?? input.company_name ?? input.applicant_id ?? 'Applicant'
      ),
      case_type: run.caseOpened.case_type,
      status: 'needs_review',
      sla_days: run.caseOpened.sla_days,
      days_left: run.caseOpened.sla_days,
      sla_state: 'on_track',
      source_decision_id: decisionId,
      context: run.output,
      notes: [],
      audit: [
        {
          type: 'case.opened',
          actor: 'system',
          at: startedAt,
          detail: `from decision ${decisionId}`
        }
      ],
      created_at: startedAt,
      updated_at: startedAt
    });
    // Link the decision back to the case it opened, so the trace can navigate to it.
    decision.case_id = caseId;
  }

  // Journal the run into the event log the way the real engine does: started, one
  // node_evaluated per trace node, then the terminal event.
  auditDecisionRun(decision);

  return { result, decision };
}

// --- Assertions -----------------------------------------------------------------

// matchExpect checks a flow's actual output against an expected subset (deep
// equality on each declared key).
function matchSubset(got: Record<string, unknown>, expect: Record<string, unknown>): string[] {
  const mismatches: string[] = [];
  for (const [k, want] of Object.entries(expect)) {
    const actual = resolvePath(got, k);
    if (JSON.stringify(actual) !== JSON.stringify(want)) {
      mismatches.push(`${k}: expected ${JSON.stringify(want)}, got ${JSON.stringify(actual)}`);
    }
  }
  return mismatches;
}

export function runAssertionsFor(flow: Flow, cases: AssertionCase[]): AssertionReport {
  const results: AssertionResult[] = cases.map((c) => {
    const { result } = decideFlow(flow, 'sandbox', c.input, { record: false });
    const got = (result.data ?? {}) as Record<string, unknown>;
    const mismatch = matchSubset(got, c.expect);
    return {
      name: c.name,
      passed: mismatch.length === 0 && result.status === 'completed',
      status: result.status,
      got,
      mismatch: mismatch.length ? mismatch : undefined
    };
  });
  const passed = results.filter((r) => r.passed).length;
  return { total: results.length, passed, failed: results.length - passed, results };
}

// --- Agent eval scoring ---------------------------------------------------------

function jsonSubsetMatch(output: string, expect: unknown): boolean {
  try {
    const got = JSON.parse(output) as Record<string, unknown>;
    return matchSubset(got, expect as Record<string, unknown>).length === 0;
  } catch {
    return false;
  }
}

export function scoreEvalCases(
  cases: EvalCase[],
  _version: number,
  schema?: AgentSchema
): EvalResult[] {
  return cases.map((c) => {
    const output = agentReply(c.prompt, schema).text;
    let passed = false;
    const mode = c.mode ?? 'contains';
    if (mode === 'contains') passed = output.includes(c.expect ?? '');
    else if (mode === 'equals') passed = output === (c.expect ?? '');
    else passed = jsonSubsetMatch(output, c.expect_json);
    return {
      name: c.name,
      passed,
      status: 'completed',
      output,
      detail: passed ? undefined : `expected (${mode}) ${c.expect ?? JSON.stringify(c.expect_json)}`
    };
  });
}

// --- Backtest -------------------------------------------------------------------

export interface BacktestInput {
  dataset: Record<string, unknown>[];
  compareVersion?: number;
}

// backtestFlowDataset runs a dataset through the flow without recording, comparing
// against a candidate version when provided. Mirrors the BacktestReport shape.
export function backtestFlowDataset(flow: Flow, input: BacktestInput) {
  const baseGraph = pickVersion(flow, 'production');
  const compareV = input.compareVersion
    ? flow.versions.find((v) => v.version === input.compareVersion)
    : undefined;
  let baseCompleted = 0;
  let baseFailed = 0;
  let candCompleted = 0;
  let candFailed = 0;
  let changed = 0;
  const records = input.dataset.map((row, index) => {
    const base = runFlow(flow, baseGraph.graph, row);
    if (base.status === 'completed') baseCompleted += 1;
    else baseFailed += 1;
    const rec: {
      index: number;
      baseline: { status: RunStatus; output?: Record<string, unknown> };
      candidate?: { status: RunStatus; output?: Record<string, unknown> };
      changed?: boolean;
    } = { index, baseline: { status: base.status, output: base.output } };
    if (compareV) {
      const cand = runFlow(flow, compareV.graph, row);
      if (cand.status === 'completed') candCompleted += 1;
      else candFailed += 1;
      const diff = JSON.stringify(base.output) !== JSON.stringify(cand.output);
      if (diff) changed += 1;
      rec.candidate = { status: cand.status, output: cand.output };
      rec.changed = diff;
    }
    return rec;
  });
  return {
    summary: {
      total: input.dataset.length,
      compare: Boolean(compareV),
      baseline_completed: baseCompleted,
      baseline_failed: baseFailed,
      candidate_completed: compareV ? candCompleted : undefined,
      candidate_failed: compareV ? candFailed : undefined,
      changed
    },
    records
  };
}
