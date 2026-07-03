// SPDX-License-Identifier: AGPL-3.0-or-later
// The PURE core of the demo decision engine: a safe expression interpreter, the
// model evaluators, graph validation, and the flow walker. Nothing here touches the
// demo store — models and policies come in as parameters — so both the runtime
// engine (engine.ts) and the seed builder (store.ts) execute the SAME semantics
// without a circular import. Nothing uses eval()/Function(): the expression
// evaluator is a hand-rolled recursive-descent parser over a tiny grammar
// (arithmetic, comparisons, boolean &&/||/!, ternary, member access, literals)
// evaluated against a flat data record. The model evaluator mirrors
// decision-engine/models/models.go (logistic / gbm / expression).

import type {
  FlowGraph,
  GraphNode,
  Model,
  ReasonCode,
  NodeRecord,
  Disposition,
  PolicySpec
} from '$lib/api';
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
export function resolvePath(rec: Record<string, unknown>, path: string): unknown {
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

export function num(v: unknown): number {
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
        // Skip a missing / non-numeric feature rather than poisoning the whole sum
        // with NaN (which serializes as null in the trace). Behaviour is unchanged
        // when every feature is present.
        const term = w * num(resolvePath(features, name));
        if (Number.isFinite(term)) z += term;
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

// --- Graph validation (mirrors decision-engine/domain/flow.go ValidateGraph) -----

const NODE_TYPES = new Set([
  'input',
  'rule',
  'split',
  'assignment',
  'scorecard',
  'decision_table',
  '2d_matrix',
  'code',
  'ai',
  'connect',
  'predict',
  'manual_review',
  'reason',
  'output'
]);

// q renders an id the way Go's %q does, so the demo's 400 bodies read byte-for-byte
// like the real service's.
function q(s: string): string {
  return JSON.stringify(s);
}

// validateGraph is the demo's publish gate, mirroring the real ValidateGraph check
// for check — and message for message: unique non-empty node ids of known types,
// exactly one input, at least one output, edges between existing distinct nodes,
// acyclicity (Kahn's), every non-output node with an outgoing edge, and every node
// reachable from the input. Returns the exact error string the real 400 body
// carries, or null for a publishable graph.
export function validateGraph(graph: FlowGraph): string | null {
  const nodes = graph.nodes ?? [];
  const edges = graph.edges ?? [];
  if (nodes.length === 0) return 'decision-engine: graph has no nodes';
  const ids = new Set<string>();
  let inputs = 0;
  let outputs = 0;
  for (const n of nodes) {
    const id = String(n.id ?? '');
    if (id.trim() === '') return 'decision-engine: node with empty id';
    if (ids.has(id)) return `decision-engine: duplicate node id ${q(id)}`;
    const type = String(n.type ?? '');
    if (!NODE_TYPES.has(type)) return `decision-engine: node ${q(id)} has unknown type ${q(type)}`;
    ids.add(id);
    if (type === 'input') inputs += 1;
    if (type === 'output') outputs += 1;
  }
  if (inputs !== 1) return `decision-engine: graph needs exactly one input node, got ${inputs}`;
  if (outputs < 1) return 'decision-engine: graph needs at least one output node';

  const indeg = new Map<string, number>();
  for (const id of ids) indeg.set(id, 0);
  const adj = new Map<string, string[]>();
  for (const e of edges) {
    if (!ids.has(e.from)) return `decision-engine: edge from unknown node ${q(e.from)}`;
    if (!ids.has(e.to)) return `decision-engine: edge to unknown node ${q(e.to)}`;
    if (e.from === e.to) return `decision-engine: self-loop on node ${q(e.from)}`;
    adj.set(e.from, [...(adj.get(e.from) ?? []), e.to]);
    indeg.set(e.to, (indeg.get(e.to) ?? 0) + 1);
  }
  const queue = nodes.filter((n) => indeg.get(n.id) === 0).map((n) => n.id);
  for (let i = 0; i < queue.length; i += 1) {
    for (const to of adj.get(queue.at(i) ?? '') ?? []) {
      const d = (indeg.get(to) ?? 0) - 1;
      indeg.set(to, d);
      if (d === 0) queue.push(to);
    }
  }
  if (queue.length !== nodes.length) return 'decision-engine: graph has a cycle';

  let start = '';
  for (const n of nodes) {
    if (n.type === 'input') start = n.id;
    if (n.type !== 'output' && !adj.has(n.id))
      return `decision-engine: node ${q(n.id)} dead-ends — every non-output node needs an outgoing edge`;
  }
  const reached = new Set([start]);
  const frontier = [start];
  for (let i = 0; i < frontier.length; i += 1) {
    for (const next of adj.get(frontier.at(i) ?? '') ?? []) {
      if (!reached.has(next)) {
        reached.add(next);
        frontier.push(next);
      }
    }
  }
  for (const n of nodes) {
    if (!reached.has(n.id))
      return `decision-engine: node ${q(n.id)} is unreachable from the input — connect it or delete it`;
  }
  return null;
}

// --- Flow walker ----------------------------------------------------------------

function nodeConfig(n: GraphNode): Record<string, unknown> {
  return (n.config ?? {}) as Record<string, unknown>;
}

function findNode(graph: FlowGraph, id: string): GraphNode | undefined {
  return graph.nodes.find((n) => n.id === id);
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

export interface WalkResult {
  status: 'completed' | 'failed' | 'suspended';
  data: Record<string, unknown>;
  output: Record<string, unknown>;
  reasonCodes: ReasonCode[];
  nodes: NodeRecord[];
  caseOpened?: { case_type: string; sla_days: number };
  suspend?: { node_id: string };
  error?: string;
}

// walkGraph interprets a FlowGraph from its input node, threading a mutable record
// through every node type the real engine executes. Models are passed in (not read
// from the store), so the walker is pure and the seed can run it at module init.
export function walkGraph(
  graph: FlowGraph,
  input: Record<string, unknown>,
  models: Model[],
  resume?: { outcome: Record<string, unknown> }
): WalkResult {
  const rec: Record<string, unknown> = { ...input };
  const nodes: NodeRecord[] = [];
  const reasonCodes: ReasonCode[] = [];
  let output: Record<string, unknown> = {};
  let caseOpened: { case_type: string; sla_days: number } | undefined;
  let suspendHere = false;

  const start = graph.nodes.find((n) => n.type === 'input') ?? graph.nodes[0];
  if (!start) {
    return { status: 'failed', data: rec, output: {}, reasonCodes, nodes, error: 'empty flow' };
  }

  // assign writes one derived value into the record (and the node's produced map),
  // via Map entries so no dynamic object indexing trips the security lint.
  const assign = (produced: Record<string, unknown>, target: string, val: unknown): void => {
    const m = new Map(Object.entries(rec));
    m.set(target, val);
    Object.assign(rec, Object.fromEntries(m));
    const pm = new Map(Object.entries(produced));
    pm.set(target, val);
    Object.assign(produced, Object.fromEntries(pm));
  };

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
          assign(produced, a.target, evalExpr(a.expr ?? 'null', rec));
        }
        nodeOut = produced;
        if (node.type === 'output') output = { ...rec };
        break;
      }
      case 'predict': {
        const modelName = String(cfg.model ?? '');
        const outKey = String(cfg.output ?? 'prediction');
        const model = models.find((m) => m.name === modelName);
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
        const text = agentReply(prompt, cfg.schema as AgentSchema | undefined, rec).text;
        const outKey = String(cfg.output ?? 'ai');
        const m = new Map(Object.entries(rec));
        m.set(outKey, text);
        Object.assign(rec, Object.fromEntries(m));
        nodeOut = { text };
        break;
      }
      case 'code': {
        // A code node in the demo runs a tiny statement DSL: one `target = expr` per
        // line (comments and blank lines skipped), each expression evaluated by the
        // same safe interpreter — never eval(). Mirrors what the real sandboxed
        // snippet computes without shipping a JS runtime in the demo.
        const source = String(cfg.source ?? '');
        const produced: Record<string, unknown> = {};
        for (const line of source.split('\n')) {
          const stmt = line.trim();
          if (stmt === '' || stmt.startsWith('//') || stmt.startsWith('#')) continue;
          const m = stmt.match(/^([a-zA-Z_][a-zA-Z0-9_]*) *=(?!=) *(.+)$/);
          if (!m) continue;
          assign(produced, m[1], evalExpr(m[2], rec));
        }
        nodeOut = produced;
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
      case 'manual_review': {
        caseOpened = {
          case_type: String(cfg.case_type ?? 'manual_review'),
          sla_days: typeof cfg.sla_days === 'number' ? cfg.sla_days : 3
        };
        reasonCodes.push({ code: 'MANUAL_REVIEW', description: 'Routed to manual review' });
        if (resume) {
          // Resuming: inject the reviewer's outcome so downstream nodes branch on it.
          const outKey = String(cfg.output_key ?? 'review');
          const m = new Map(Object.entries(rec));
          m.set(outKey, resume.outcome);
          Object.assign(rec, Object.fromEntries(m));
          for (const [k, v] of Object.entries(resume.outcome)) {
            const mm = new Map(Object.entries(rec));
            mm.set(k, v);
            Object.assign(rec, Object.fromEntries(mm));
          }
          nodeOut = { resumed: true, ...resume.outcome };
        } else if (cfg.suspend) {
          // A durable human task: pause the decision here (handled after the trace push).
          suspendHere = true;
          nodeOut = { suspended: true };
        } else {
          nodeOut = { case_opened: true };
        }
        break;
      }
      case 'rule': {
        // Each rule whose `when` holds applies its `then` assignments to the record.
        const rules = Array.isArray(cfg.rules)
          ? (cfg.rules as { when?: string; then?: { target?: string; expr?: string }[] }[])
          : [];
        const produced: Record<string, unknown> = {};
        for (const r of rules) {
          if (r.when && !isTruthy(evalExpr(r.when, rec))) continue;
          for (const a of r.then ?? []) {
            if (!a.target) continue;
            assign(produced, a.target, evalExpr(a.expr ?? 'null', rec));
          }
        }
        nodeOut = produced;
        break;
      }
      case 'scorecard': {
        // Additive weight-sum of the factors whose condition holds.
        const outKey = String(cfg.output ?? 'score');
        const factors = Array.isArray(cfg.factors)
          ? (cfg.factors as { when?: string; weight?: number }[])
          : [];
        let score = 0;
        for (const f of factors) {
          if (!f.when || isTruthy(evalExpr(f.when, rec))) score += Number(f.weight ?? 0);
        }
        const produced: Record<string, unknown> = {};
        assign(produced, outKey, score);
        nodeOut = produced;
        break;
      }
      case 'decision_table': {
        // DMN-style: first matching row wins, unless hit="collect" which sums every
        // matching row's outputs into the target (the demo's two common hit policies).
        const rows = Array.isArray(cfg.rows)
          ? (cfg.rows as { when?: string; outputs?: { target?: string; expr?: string }[] }[])
          : [];
        const collect = String(cfg.hit ?? 'first') === 'collect';
        const produced: Record<string, unknown> = {};
        for (const r of rows) {
          if (r.when && !isTruthy(evalExpr(r.when, rec))) continue;
          for (const o of r.outputs ?? []) {
            if (!o.target) continue;
            const v = evalExpr(o.expr ?? 'null', rec);
            const val = collect
              ? Number(new Map(Object.entries(rec)).get(o.target) ?? 0) + Number(v)
              : v;
            assign(produced, o.target, val);
          }
          if (!collect) break;
        }
        nodeOut = produced;
        break;
      }
      case '2d_matrix': {
        // Grid lookup: the first matching row index × the first matching col index
        // selects a cell (e.g. risk × ticket-size → action).
        const outKey = String(cfg.output ?? 'matrix');
        const rows = Array.isArray(cfg.rows) ? (cfg.rows as { when?: string }[]) : [];
        const cols = Array.isArray(cfg.cols) ? (cfg.cols as { when?: string }[]) : [];
        const cells = Array.isArray(cfg.cells) ? (cfg.cells as unknown[][]) : [];
        const ri = rows.findIndex((r) => !r.when || isTruthy(evalExpr(r.when, rec)));
        const ci = cols.findIndex((c) => !c.when || isTruthy(evalExpr(c.when, rec)));
        const row = ri >= 0 ? cells.at(ri) : undefined;
        const cell = Array.isArray(row) && ci >= 0 ? (row as unknown[]).at(ci) : undefined;
        const produced: Record<string, unknown> = {};
        assign(produced, outKey, cell);
        nodeOut = produced;
        break;
      }
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
    if (suspendHere) {
      // Paused at a durable human task: no terminal output yet; resume continues here.
      return {
        status: 'suspended',
        data: rec,
        output: {},
        reasonCodes,
        nodes,
        caseOpened,
        suspend: { node_id: node.id }
      };
    }
    if (node.type === 'output') {
      current = undefined;
      break;
    }
    if (!nextId) {
      // Only an output node completes a run: any other node with nowhere to go is
      // a wiring bug, failed with the real engine's exact runtime message.
      return {
        status: 'failed',
        data: rec,
        output: {},
        reasonCodes,
        nodes,
        error: `decision-engine: flow dead-ends at non-output node ${q(node.id)}`
      };
    }
    current = findNode(graph, nextId);
  }

  // The step bound is a defensive backstop (the real engine fails with "execution
  // exceeded the node bound"): exhausting it with a node still pending is a cycle,
  // not a completed decision.
  if (current) {
    return {
      status: 'failed',
      data: rec,
      output: {},
      reasonCodes,
      nodes,
      error: `exceeded step bound (cycle?) at "${current.id}"`
    };
  }

  return { status: 'completed', data: rec, output, reasonCodes, nodes, caseOpened };
}

// applyPolicySpec evaluates a disposition policy over the terminal record, appending
// the matched rule's reason code. The matched band's description becomes the
// disposition_reason the real decide response carries.
export function applyPolicySpec(
  spec: PolicySpec,
  rec: Record<string, unknown>,
  reasonCodes: ReasonCode[]
): { disposition?: Disposition; reason?: string } {
  for (const rule of spec.rules) {
    if (isTruthy(evalExpr(rule.when, rec))) {
      if (rule.code) reasonCodes.push({ code: rule.code, description: rule.description ?? '' });
      return { disposition: rule.disposition, reason: rule.description };
    }
  }
  return { disposition: (spec.default as Disposition) ?? 'refer' };
}
