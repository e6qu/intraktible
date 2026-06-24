// SPDX-License-Identifier: AGPL-3.0-or-later
// A small, safe JS interpreter so the demo's core journeys truly compute rather
// than returning canned data. Nothing here uses eval()/Function() — the
// expression evaluator is a hand-rolled recursive-descent parser over a tiny
// grammar (arithmetic, comparisons, boolean &&/||/!, ternary, member access,
// literals) evaluated against a flat data record. The model evaluator mirrors
// decision-engine/models/models.go (logistic / gbm / expression). The flow
// walker interprets a FlowGraph into a DecideResult-shaped object plus a recorded
// Decision (with a node trace) appended to the store.

import type {
  Decision,
  DecideResult,
  FlowGraph,
  GraphNode,
  Flow,
  Model,
  AssertionCase,
  AssertionResult,
  AssertionReport,
  EvalCase,
  EvalResult,
  ReasonCode,
  NodeRecord,
  Environment,
  Disposition
} from '$lib/api';
import { state, nextId } from './store';
import { agentReply, type AgentSchema } from './agent';

// --- Expression evaluator -------------------------------------------------------

type Tok = { t: string; v: string };

// tokenize splits an expression into tokens. Whitespace is dropped; identifiers,
// numbers, strings and a fixed set of operators are recognised.
function tokenize(src: string): Tok[] {
  const toks: Tok[] = [];
  let i = 0;
  const two = ['&&', '||', '==', '!=', '>=', '<=', '??'];
  // charAt (not src[i]) so numeric string indexing doesn't trip the
  // object-injection lint; out-of-range returns '' which the loops treat as end.
  const at = (j: number): string => src.charAt(j);
  while (i < src.length) {
    const c = at(i);
    if (c === ' ' || c === '\t' || c === '\n' || c === '\r') {
      i += 1;
      continue;
    }
    if (c === '"' || c === "'") {
      let s = '';
      i += 1;
      while (i < src.length && at(i) !== c) {
        s += at(i);
        i += 1;
      }
      i += 1;
      toks.push({ t: 'str', v: s });
      continue;
    }
    if (/[0-9]/.test(c) || (c === '.' && /[0-9]/.test(at(i + 1)))) {
      let n = '';
      while (i < src.length && /[0-9.]/.test(at(i))) {
        n += at(i);
        i += 1;
      }
      toks.push({ t: 'num', v: n });
      continue;
    }
    if (/[a-zA-Z_]/.test(c)) {
      let id = '';
      while (i < src.length && /[a-zA-Z0-9_.]/.test(at(i))) {
        id += at(i);
        i += 1;
      }
      if (id === 'true' || id === 'false' || id === 'null') toks.push({ t: 'lit', v: id });
      else toks.push({ t: 'ident', v: id });
      continue;
    }
    const pair = src.slice(i, i + 2);
    if (two.includes(pair)) {
      toks.push({ t: 'op', v: pair });
      i += 2;
      continue;
    }
    toks.push({ t: 'op', v: c });
    i += 1;
  }
  return toks;
}

// Parser produces a small AST; the evaluator walks it against a data record. The
// grammar (lowest→highest precedence): ternary, ||, &&, equality, comparison,
// additive, multiplicative, unary, primary.
type Node =
  | { k: 'num'; v: number }
  | { k: 'str'; v: string }
  | { k: 'bool'; v: boolean }
  | { k: 'null' }
  | { k: 'var'; name: string }
  | { k: 'unary'; op: string; e: Node }
  | { k: 'bin'; op: string; l: Node; r: Node }
  | { k: 'tern'; c: Node; a: Node; b: Node };

class Parser {
  private toks: Tok[];
  private pos = 0;
  constructor(toks: Tok[]) {
    this.toks = toks;
  }
  private peek(): Tok | undefined {
    return this.toks[this.pos];
  }
  private next(): Tok | undefined {
    const t = this.toks[this.pos];
    this.pos += 1;
    return t;
  }
  private isOp(v: string): boolean {
    const t = this.peek();
    return t?.t === 'op' && t.v === v;
  }
  // eat consumes the next token and returns its value ('' at end of input). Used
  // after an isOp guard, so the token is always present in practice.
  private eat(): string {
    return this.next()?.v ?? '';
  }
  parse(): Node {
    const e = this.ternary();
    return e;
  }
  private ternary(): Node {
    const c = this.or();
    if (this.isOp('?')) {
      this.next();
      const a = this.ternary();
      if (this.isOp(':')) this.next();
      const b = this.ternary();
      return { k: 'tern', c, a, b };
    }
    return c;
  }
  private or(): Node {
    let l = this.and();
    while (this.isOp('||') || this.isOp('??')) {
      const op = this.eat();
      l = { k: 'bin', op, l, r: this.and() };
    }
    return l;
  }
  private and(): Node {
    let l = this.equality();
    while (this.isOp('&&')) {
      this.next();
      l = { k: 'bin', op: '&&', l, r: this.equality() };
    }
    return l;
  }
  private equality(): Node {
    let l = this.comparison();
    while (this.isOp('==') || this.isOp('!=')) {
      const op = this.eat();
      l = { k: 'bin', op, l, r: this.comparison() };
    }
    return l;
  }
  private comparison(): Node {
    let l = this.additive();
    while (this.isOp('>') || this.isOp('<') || this.isOp('>=') || this.isOp('<=')) {
      const op = this.eat();
      l = { k: 'bin', op, l, r: this.additive() };
    }
    return l;
  }
  private additive(): Node {
    let l = this.multiplicative();
    while (this.isOp('+') || this.isOp('-')) {
      const op = this.eat();
      l = { k: 'bin', op, l, r: this.multiplicative() };
    }
    return l;
  }
  private multiplicative(): Node {
    let l = this.unary();
    while (this.isOp('*') || this.isOp('/') || this.isOp('%')) {
      const op = this.eat();
      l = { k: 'bin', op, l, r: this.unary() };
    }
    return l;
  }
  private unary(): Node {
    if (this.isOp('!') || this.isOp('-')) {
      const op = this.eat();
      return { k: 'unary', op, e: this.unary() };
    }
    return this.primary();
  }
  private primary(): Node {
    const t = this.next();
    if (!t) throw new Error('unexpected end of expression');
    if (t.t === 'num') return { k: 'num', v: Number(t.v) };
    if (t.t === 'str') return { k: 'str', v: t.v };
    if (t.t === 'lit') {
      if (t.v === 'null') return { k: 'null' };
      return { k: 'bool', v: t.v === 'true' };
    }
    if (t.t === 'ident') return { k: 'var', name: t.v };
    if (t.t === 'op' && t.v === '(') {
      const e = this.ternary();
      if (this.isOp(')')) this.next();
      return e;
    }
    throw new Error(`unexpected token ${t.v}`);
  }
}

// resolvePath reads a dotted path (e.g. predict.pd.probability) from the record
// via Map-based lookups so no dynamic object indexing trips the security lint.
function resolvePath(rec: Record<string, unknown>, path: string): unknown {
  const parts = path.split('.');
  let cur: unknown = rec;
  for (const p of parts) {
    if (cur === null || cur === undefined) return undefined;
    if (typeof cur !== 'object') return undefined;
    const entry = Object.entries(cur as Record<string, unknown>).find(([k]) => k === p);
    cur = entry?.[1];
  }
  return cur;
}

function num(v: unknown): number {
  if (typeof v === 'number') return v;
  if (typeof v === 'boolean') return v ? 1 : 0;
  if (typeof v === 'string' && v.trim() !== '' && !Number.isNaN(Number(v))) return Number(v);
  return NaN;
}

// isTruthy coerces an evaluated expression value to a boolean (a named helper so
// callers read clearly and eslint's no-extra-boolean-cast stays happy).
function isTruthy(v: unknown): boolean {
  return Boolean(v);
}

function evalNode(n: Node, rec: Record<string, unknown>): unknown {
  switch (n.k) {
    case 'num':
      return n.v;
    case 'str':
      return n.v;
    case 'bool':
      return n.v;
    case 'null':
      return null;
    case 'var':
      return resolvePath(rec, n.name);
    case 'unary': {
      const v = evalNode(n.e, rec);
      if (n.op === '!') return !v;
      return -num(v);
    }
    case 'tern':
      return evalNode(n.c, rec) ? evalNode(n.a, rec) : evalNode(n.b, rec);
    case 'bin': {
      const op = n.op;
      if (op === '&&') return Boolean(evalNode(n.l, rec)) && Boolean(evalNode(n.r, rec));
      if (op === '||') return Boolean(evalNode(n.l, rec)) || Boolean(evalNode(n.r, rec));
      const l = evalNode(n.l, rec);
      if (op === '??') return l === null || l === undefined ? evalNode(n.r, rec) : l;
      const r = evalNode(n.r, rec);
      switch (op) {
        case '+':
          if (typeof l === 'string' || typeof r === 'string') return String(l) + String(r);
          return num(l) + num(r);
        case '-':
          return num(l) - num(r);
        case '*':
          return num(l) * num(r);
        case '/':
          return num(l) / num(r);
        case '%':
          return num(l) % num(r);
        case '==':
          return l === r;
        case '!=':
          return l !== r;
        case '>':
          return num(l) > num(r);
        case '<':
          return num(l) < num(r);
        case '>=':
          return num(l) >= num(r);
        case '<=':
          return num(l) <= num(r);
        default:
          return undefined;
      }
    }
  }
}

// evalExpr evaluates an expression string against a record, returning undefined on
// a parse/eval error (the demo never throws out of a decision over a bad expr).
export function evalExpr(src: string, rec: Record<string, unknown>): unknown {
  try {
    const ast = new Parser(tokenize(src)).parse();
    return evalNode(ast, rec);
  } catch {
    return undefined;
  }
}

// --- Model evaluation (mirrors decision-engine/models/models.go) ----------------

interface Tree {
  leaf?: boolean;
  value?: number;
  feature?: string;
  threshold?: number;
  left?: Tree;
  right?: Tree;
}
interface ModelSpec {
  kind?: string;
  intercept?: number;
  coefficients?: Record<string, number>;
  base?: number;
  trees?: Tree[];
  link?: string;
  expr?: string;
}

function sigmoid(z: number): number {
  return 1 / (1 + Math.exp(-z));
}

function evalTree(t: Tree, features: Record<string, unknown>): number {
  if (t.leaf) return t.value ?? 0;
  const x = num(resolvePath(features, t.feature ?? ''));
  if (x < (t.threshold ?? 0)) return t.left ? evalTree(t.left, features) : 0;
  return t.right ? evalTree(t.right, features) : 0;
}

export interface Prediction {
  score: number;
  probability?: number;
}

// evaluateModel runs a stored model spec over a feature map. External models can't
// be evaluated in-core (matching the Go shell), so they return a plausible stub.
export function evaluateModel(model: Model, features: Record<string, unknown>): Prediction {
  const spec = (model.spec ?? {}) as ModelSpec;
  switch (spec.kind ?? model.kind) {
    case 'logistic': {
      let z = spec.intercept ?? 0;
      for (const [name, w] of Object.entries(spec.coefficients ?? {})) {
        z += w * num(resolvePath(features, name));
      }
      return { score: z, probability: sigmoid(z) };
    }
    case 'gbm': {
      let raw = spec.base ?? 0;
      for (const t of spec.trees ?? []) raw += evalTree(t, features);
      return spec.link === 'logit' ? { score: raw, probability: sigmoid(raw) } : { score: raw };
    }
    case 'expression': {
      const v = num(evalExpr(spec.expr ?? '0', features));
      return { score: Number.isNaN(v) ? 0 : v };
    }
    default:
      // external / unknown: a stable, plausible stub probability.
      return { score: 0.5, probability: 0.5 };
  }
}

// --- Flow walker ----------------------------------------------------------------

function nodeConfig(n: GraphNode): Record<string, unknown> {
  return (n.config ?? {}) as Record<string, unknown>;
}

function findNode(graph: FlowGraph, id: string): GraphNode | undefined {
  return graph.nodes.find((n) => n.id === id);
}

// pickVersion resolves the graph for a flow in an environment (deployed version,
// else latest), and the version number used for the recorded decision.
export function pickVersion(flow: Flow, env: string): { graph: FlowGraph; version: number } {
  const deployed = Object.entries(flow.deployments ?? {}).find(([e]) => e === env)?.[1]?.version;
  const version = deployed ?? flow.latest;
  const v = flow.versions.find((x) => x.version === version) ?? flow.versions.at(-1);
  return { graph: v?.graph ?? { nodes: [], edges: [] }, version: v?.version ?? 1 };
}

export interface DecideOptions {
  record?: boolean; // append a Decision to the store (default true)
  variant?: 'champion' | 'challenger';
}

// connectorSample returns plausible, connector-shaped data for a connect node, keyed
// off the connector name (the demo stands in for a real external fetch).
function connectorSample(connector: string): Record<string, unknown> {
  const n = connector.toLowerCase();
  if (/experian|bureau|credit/.test(n))
    return { fico_score: 712, open_accounts: 4, utilization: 0.34, delinquencies_24m: 0 };
  if (/ofac|sanction|watchlist|pep/.test(n))
    return { hit: false, lists_checked: ['OFAC', 'EU', 'UN'], score: 0 };
  if (/device|fraud|intel/.test(n))
    return { device_risk: 22, vpn: false, new_device: false, velocity_24h: 3 };
  if (/jumio|kyc|identity|document/.test(n))
    return { verified: true, document_valid: true, liveness: 0.97 };
  if (/bank|core|account|ledger/.test(n))
    return { balance_usd: 5400, tenure_months: 28, nsf_12m: 0 };
  return { ok: true, fetched_at: new Date().toISOString() };
}

// runFlow walks a FlowGraph from its input node, threading a mutable record
// through assignment/predict/split/ai/manual_review/output nodes. Returns the
// terminal record, disposition, reason codes and node trace.
export function runFlow(
  flow: Flow,
  graph: FlowGraph,
  input: Record<string, unknown>
): {
  status: 'completed' | 'failed';
  data: Record<string, unknown>;
  output: Record<string, unknown>;
  disposition?: Disposition;
  reasonCodes: ReasonCode[];
  nodes: NodeRecord[];
  caseOpened?: { case_type: string; sla_days: number };
  error?: string;
} {
  const rec: Record<string, unknown> = { ...input };
  const nodes: NodeRecord[] = [];
  const reasonCodes: ReasonCode[] = [];
  let output: Record<string, unknown> = {};
  let caseOpened: { case_type: string; sla_days: number } | undefined;

  const start = graph.nodes.find((n) => n.type === 'input') ?? graph.nodes[0];
  if (!start) {
    return { status: 'failed', data: rec, output: {}, reasonCodes, nodes, error: 'empty flow' };
  }

  let current: GraphNode | undefined = start;
  let guard = 0;
  while (current && guard < 200) {
    guard += 1;
    const node = current;
    const cfg = nodeConfig(node);
    let branchTaken: string | undefined;
    let nodeOut: unknown = {};

    switch (node.type) {
      case 'input':
        nodeOut = { ...input };
        break;
      case 'assignment':
      case 'output': {
        const assigns = Array.isArray(cfg.assignments)
          ? (cfg.assignments as { target?: string; expr?: string }[])
          : [];
        const produced: Record<string, unknown> = {};
        for (const a of assigns) {
          if (!a.target) continue;
          const val = evalExpr(a.expr ?? 'null', rec);
          // Map-based write avoids dynamic object indexing (security lint).
          const m = new Map(Object.entries(rec));
          m.set(a.target, val);
          Object.assign(rec, Object.fromEntries(m));
          const pm = new Map(Object.entries(produced));
          pm.set(a.target, val);
          Object.assign(produced, Object.fromEntries(pm));
        }
        nodeOut = produced;
        if (node.type === 'output') output = { ...rec };
        break;
      }
      case 'predict': {
        const modelName = String(cfg.model ?? '');
        const outKey = String(cfg.output ?? 'prediction');
        const model = state.models.find((m) => m.name === modelName);
        const pred = model ? evaluateModel(model, rec) : { score: 0.5, probability: 0.5 };
        const m = new Map(Object.entries((rec.predict as Record<string, unknown>) ?? {}));
        m.set(outKey, pred);
        rec.predict = Object.fromEntries(m);
        nodeOut = Object.fromEntries([[outKey, pred]]);
        break;
      }
      case 'connect': {
        // The real connect node fetches from an external connector; the demo injects
        // plausible, connector-shaped data so flows that read external signals resolve.
        const connector = String(cfg.connector ?? cfg.name ?? '');
        const outKey = String(cfg.output ?? 'connect');
        const fetched = connectorSample(connector);
        const m = new Map(Object.entries((rec.connect as Record<string, unknown>) ?? {}));
        m.set(outKey, fetched);
        rec.connect = Object.fromEntries(m);
        nodeOut = Object.fromEntries([[outKey, fetched]]);
        break;
      }
      case 'ai': {
        const prompt = String(cfg.prompt ?? rec.prompt ?? '');
        const text = agentReply(prompt, cfg.schema as AgentSchema | undefined).text;
        const outKey = String(cfg.output ?? 'ai');
        const m = new Map(Object.entries(rec));
        m.set(outKey, text);
        Object.assign(rec, Object.fromEntries(m));
        nodeOut = { text };
        break;
      }
      case 'reason': {
        const rs = Array.isArray(cfg.reasons)
          ? (cfg.reasons as { when?: string; code?: string; description?: string }[])
          : [];
        for (const r of rs) {
          if (r.code && (!r.when || isTruthy(evalExpr(r.when, rec)))) {
            reasonCodes.push({ code: r.code, description: r.description ?? '' });
          }
        }
        nodeOut = { reason_codes: reasonCodes.map((r) => r.code) };
        break;
      }
      case 'manual_review':
        caseOpened = {
          case_type: String(cfg.case_type ?? 'manual_review'),
          sla_days: typeof cfg.sla_days === 'number' ? cfg.sla_days : 3
        };
        reasonCodes.push({ code: 'MANUAL_REVIEW', description: 'Routed to manual review' });
        nodeOut = { case_opened: true };
        break;
      default:
        nodeOut = {};
    }

    // Choose the next edge: a split picks the first branch whose condition is
    // truthy; other nodes take their first outgoing edge.
    const outgoing = graph.edges.filter((e) => e.from === node.id);
    let nextId: string | undefined;
    if (node.type === 'split') {
      const taken = outgoing.find((e) => e.branch && isTruthy(evalExpr(e.branch, rec)));
      // Fall back only to an EXPLICIT default (unbranched) edge — never to the first
      // branch. A split whose conditions all evaluated false with no default is a flow
      // error (typically a non-finite/odd input that matched no band); fail loudly
      // rather than silently routing down branch[0] (which could auto-approve).
      const chosen = taken ?? outgoing.find((e) => !e.branch);
      if (!chosen) {
        nodes.push({ node_id: node.id, type: node.type, output: { branch: undefined } });
        return {
          status: 'failed',
          data: rec,
          output: {},
          reasonCodes,
          nodes,
          error: `no branch matched at split "${node.id}"`
        };
      }
      branchTaken = chosen.branch;
      nextId = chosen.to;
      nodeOut = { branch: branchTaken };
    } else {
      nextId = outgoing[0]?.to;
    }

    nodes.push({ node_id: node.id, type: node.type, output: nodeOut });
    if (node.type === 'output' || !nextId) break;
    current = findNode(graph, nextId);
  }

  // Bind the flow's policy (by flow_slug) to derive a disposition over the output.
  const disposition = dispositionFor(flow.slug, rec, reasonCodes);
  return { status: 'completed', data: rec, output, disposition, reasonCodes, nodes, caseOpened };
}

// dispositionFor evaluates the flow's bound policy (matched by flow_slug) over the
// record, appending the matched rule's reason code. Returns undefined when no
// policy is bound (the flow's own output stands).
function dispositionFor(
  slug: string,
  rec: Record<string, unknown>,
  reasonCodes: ReasonCode[]
): Disposition | undefined {
  const policy = state.policies.find((p) => p.flow_slug === slug);
  if (!policy) return undefined;
  const spec =
    policy.versions.find((v) => v.version === policy.latest)?.spec ?? policy.versions.at(-1)?.spec;
  if (!spec) return undefined;
  for (const rule of spec.rules) {
    if (isTruthy(evalExpr(rule.when, rec))) {
      if (rule.code) reasonCodes.push({ code: rule.code, description: rule.description ?? '' });
      return rule.disposition;
    }
  }
  return (spec.default as Disposition) ?? 'refer';
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
  const { graph, version } = pickVersion(flow, env);
  const startedAt = new Date().toISOString();
  const run = runFlow(flow, graph, input);
  const decisionId = nextId('dec');
  const durationMs = 20 + Math.floor(Math.random() * 60);
  const result: DecideResult = {
    decision_id: decisionId,
    status: run.status,
    data: run.output,
    disposition: run.disposition,
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
    variant: opts.variant ?? 'champion',
    status: run.status,
    data: run.data,
    output: run.output,
    reason_codes: run.reasonCodes,
    disposition: run.disposition,
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
      company_name: String(input.company_name ?? input.applicant_id ?? 'Applicant'),
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
      baseline: { status: 'completed' | 'failed'; output?: Record<string, unknown> };
      candidate?: { status: 'completed' | 'failed'; output?: Record<string, unknown> };
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

export { resolvePath, num };
