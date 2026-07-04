// SPDX-License-Identifier: AGPL-3.0-or-later
// The PURE core of the demo decision engine: a safe expression interpreter, the
// model evaluators, graph validation, and the flow walker. Nothing here touches the
// demo store — models and policies come in as parameters — so both the runtime
// engine (engine.ts) and the seed builder (store.ts) execute the SAME semantics
// without a circular import. Nothing uses eval()/Function(): the expression
// evaluator is a hand-rolled recursive-descent parser over a tiny grammar
// (arithmetic, comparisons, boolean &&/||/!, ternary, member access, literals)
// evaluated against a flat data record.
//
// PARITY: walkGraph mirrors decision-engine/domain/execute.go node for node,
// message for message — statuses, trace outputs, reason-code accumulation, the
// int/float typing rules of expr-lang, and every failure wording. The battery in
// engine-parity-fixtures.json (generated from the REAL engine by
// decision-engine/domain/parity_fixtures_test.go and replayed by
// engine-parity.test.ts) is the differential proof. Demo-authoring dialect config
// forms the real engine rejects (split edges carrying condition expressions,
// output-node assignments, manual_review literal configs, the code `source` DSL)
// are handled as documented seams next to their Go-form counterparts.

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
  const two = ['&&', '||', '==', '!=', '>=', '<=', '??', '**'];
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
      if (id === 'true' || id === 'false' || id === 'null' || id === 'nil')
        toks.push({ t: 'lit', v: id });
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
// grammar (lowest→highest precedence): ternary, ?? and ||, &&, equality,
// comparison, additive, multiplicative, unary, power, primary.
type Node =
  | { k: 'num'; v: number; int: boolean }
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
    return this.toks.at(this.pos);
  }
  private next(): Tok | undefined {
    const t = this.toks.at(this.pos);
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
    const trailing = this.peek();
    if (trailing) throw new Error(`unexpected token ${trailing.v}`);
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
    return this.power();
  }
  // power binds tighter than unary (matching expr-lang's ** / ^), right-associative.
  private power(): Node {
    const base = this.primary();
    if (this.isOp('**') || this.isOp('^')) {
      const op = this.eat();
      return { k: 'bin', op, l: base, r: this.unary() };
    }
    return base;
  }
  private primary(): Node {
    const t = this.next();
    if (!t) throw new Error('unexpected end of expression');
    if (t.t === 'num') return { k: 'num', v: Number(t.v), int: !t.v.includes('.') };
    if (t.t === 'str') return { k: 'str', v: t.v };
    if (t.t === 'lit') {
      if (t.v === 'null' || t.v === 'nil') return { k: 'null' };
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

// --- Typed evaluation (expr-lang / Starlark parity) -------------------------------
//
// The real engine evaluates expressions with expr-lang, which type-checks against
// the Go value types: JSON numbers are float64, numeric LITERALS are ints, int
// arithmetic stays int, `/` always yields float, `%` is int-only, and conditions
// must be real booleans. The demo mirrors that with a tag threaded through
// evaluation ('int' vs 'float64' — invisible in JSON, but load-bearing for which
// expressions ERROR). The walker's tag map remembers which record fields the
// engine wrote as ints so `x % 2` behaves identically after `x = 7 % 2`.
// Starlark mode (code nodes) differs only where Starlark does: integral values are
// ints (mirroring toStarlark in decision-engine/domain/code.go).

type Tag = 'int' | 'float64' | 'string' | 'bool' | 'nil' | 'unknown' | 'any' | 'map' | 'array';
interface TV {
  v: unknown;
  t: Tag;
}
type EvalMode = 'expr' | 'starlark';
interface EvalEnv {
  rec: Record<string, unknown>;
  tags: Map<string, Tag>;
  mode: EvalMode;
}

// typeName renders a tag the way expr-lang's checker names Go types in its
// "mismatched types" errors ('any'-tagged values fall back to their runtime type).
function typeName(tv: TV): string {
  const t = tv.t === 'any' ? runtimeTag(tv.v) : tv.t;
  if (t === 'nil') return '<nil>';
  if (t === 'map') return 'map[string]interface {}';
  if (t === 'array') return '[]interface {}';
  return t;
}

function runtimeTag(v: unknown): Tag {
  if (v === null || v === undefined) return 'nil';
  if (typeof v === 'number') return 'float64';
  if (typeof v === 'string') return 'string';
  if (typeof v === 'boolean') return 'bool';
  return Array.isArray(v) ? 'array' : 'map';
}

// numKind reports whether a value is numeric for the operators ('any'-tagged
// deep-path numbers count as float64, exactly like Go's runtime promotion).
function numKind(tv: TV): 'int' | 'float64' | null {
  if (tv.t === 'int' || tv.t === 'float64') return tv.t;
  if (tv.t === 'any' && typeof tv.v === 'number') return 'float64';
  return null;
}

function boolish(tv: TV): boolean {
  return tv.t === 'bool' || (tv.t === 'any' && typeof tv.v === 'boolean');
}

function stringish(tv: TV): boolean {
  return tv.t === 'string' || (tv.t === 'any' && typeof tv.v === 'string');
}

function mismatch(op: string, l: TV, r: TV): Error {
  return new Error(`invalid operation: ${op} (mismatched types ${typeName(l)} and ${typeName(r)})`);
}

// eqClass groups tags for equality type-checking: expr-lang rejects == between
// concrete mismatched types (string vs int) but compares dynamically-typed and
// nil operands at runtime.
function eqClass(tv: TV): string | null {
  if (numKind(tv) && tv.t !== 'any') return 'num';
  if (tv.t === 'string') return 'string';
  if (tv.t === 'bool') return 'bool';
  return null;
}

// looseDeepEqual mirrors Go's == on interface values as expr-lang applies it:
// numeric value equality, nil == nil, deep equality for maps/lists.
function looseDeepEqual(a: unknown, b: unknown): boolean {
  if ((a === null || a === undefined) && (b === null || b === undefined)) return true;
  if (a === null || a === undefined || b === null || b === undefined) return false;
  if (typeof a === 'number' && typeof b === 'number') return a === b;
  if (typeof a !== typeof b) return false;
  if (Array.isArray(a) && Array.isArray(b)) {
    return a.length === b.length && a.every((v, i) => looseDeepEqual(v, b.at(i)));
  }
  if (typeof a === 'object' && typeof b === 'object') {
    if (Array.isArray(a) || Array.isArray(b)) return false;
    const ea = Object.entries(a as Record<string, unknown>);
    const mb = new Map(Object.entries(b as Record<string, unknown>));
    return ea.length === mb.size && ea.every(([k, v]) => mb.has(k) && looseDeepEqual(v, mb.get(k)));
  }
  return a === b;
}

// readVar resolves an identifier (dotted path) against the record. An unknown ROOT
// name is a compile error in expr-lang ("unknown name x") because the record IS the
// type environment; a missing DEEP member is nil at runtime.
function readVar(name: string, env: EvalEnv): TV {
  const dot = name.indexOf('.');
  const root = dot === -1 ? name : name.slice(0, dot);
  const entries = new Map(Object.entries(env.rec));
  if (!entries.has(root)) throw new Error(`unknown name ${root}`);
  if (dot !== -1) {
    const v = resolvePath(env.rec, name);
    return { v: v === undefined ? null : v, t: 'any' };
  }
  const v = entries.get(root);
  return { v: v === undefined ? null : v, t: rootTag(v, root, env) };
}

function rootTag(v: unknown, name: string, env: EvalEnv): Tag {
  if (v === null || v === undefined) return 'unknown';
  if (typeof v === 'number') {
    // Starlark converts every integral number to an Int (toStarlark); expr-lang
    // sees JSON numbers as float64 unless the engine itself wrote them as ints.
    if (env.mode === 'starlark') return Number.isInteger(v) ? 'int' : 'float64';
    return env.tags.get(name) === 'int' ? 'int' : 'float64';
  }
  if (typeof v === 'string') return 'string';
  if (typeof v === 'boolean') return 'bool';
  return Array.isArray(v) ? 'array' : 'map';
}

function evalTV(n: Node, env: EvalEnv): TV {
  switch (n.k) {
    case 'num':
      return { v: n.v, t: n.int ? 'int' : 'float64' };
    case 'str':
      return { v: n.v, t: 'string' };
    case 'bool':
      return { v: n.v, t: 'bool' };
    case 'null':
      return { v: null, t: 'nil' };
    case 'var':
      return readVar(n.name, env);
    case 'unary': {
      const e = evalTV(n.e, env);
      if (n.op === '!') {
        if (!boolish(e)) throw new Error(`invalid operation: ! (mismatched type ${typeName(e)})`);
        return { v: !isTruthy(e.v), t: 'bool' };
      }
      const k = numKind(e);
      if (!k) throw new Error(`invalid operation: - (mismatched type ${typeName(e)})`);
      return { v: -num(e.v), t: k };
    }
    case 'tern': {
      // Both branches are evaluated eagerly: expr-lang type-checks the whole
      // expression at compile time, so an unknown name in the untaken branch
      // fails there too (and the language is pure, so evaluation is safe).
      const c = evalTV(n.c, env);
      const a = evalTV(n.a, env);
      const b = evalTV(n.b, env);
      if (!boolish(c))
        throw new Error(`non-bool expression (type ${typeName(c)}) used as condition`);
      return isTruthy(c.v) ? a : b;
    }
    case 'bin':
      return evalBin(n.op, evalTV(n.l, env), evalTV(n.r, env), env);
  }
}

function evalBin(op: string, l: TV, r: TV, env: EvalEnv): TV {
  if (op === '??') {
    return l.v === null || l.v === undefined ? r : l;
  }
  const ln = numKind(l);
  const rn = numKind(r);
  const bothInt = ln === 'int' && rn === 'int';
  switch (op) {
    case '+':
      if (ln && rn) return { v: num(l.v) + num(r.v), t: bothInt ? 'int' : 'float64' };
      if (stringish(l) && stringish(r)) return { v: String(l.v) + String(r.v), t: 'string' };
      throw mismatch(op, l, r);
    case '-':
      if (ln && rn) return { v: num(l.v) - num(r.v), t: bothInt ? 'int' : 'float64' };
      throw mismatch(op, l, r);
    case '*':
      if (ln && rn) return { v: num(l.v) * num(r.v), t: bothInt ? 'int' : 'float64' };
      throw mismatch(op, l, r);
    case '/':
      // Real division in both expr-lang and Starlark: the result is always float.
      if (ln && rn) return { v: num(l.v) / num(r.v), t: 'float64' };
      throw mismatch(op, l, r);
    case '%':
      // expr-lang's % is int-only (float64 % int is a compile error); Starlark
      // allows numeric %. JS % truncates like Go for the shared (non-negative) range.
      if (env.mode === 'starlark' && ln && rn)
        return { v: num(l.v) % num(r.v), t: bothInt ? 'int' : 'float64' };
      if (bothInt) return { v: num(l.v) % num(r.v), t: 'int' };
      throw mismatch(op, l, r);
    case '**':
    case '^':
      if (ln && rn) return { v: Math.pow(num(l.v), num(r.v)), t: 'float64' };
      throw mismatch(op, l, r);
    case '>':
    case '<':
    case '>=':
    case '<=': {
      if ((ln && rn) || (stringish(l) && stringish(r))) {
        const a = l.v as number | string;
        const b = r.v as number | string;
        const v = op === '>' ? a > b : op === '<' ? a < b : op === '>=' ? a >= b : a <= b;
        return { v, t: 'bool' };
      }
      throw mismatch(op, l, r);
    }
    case '==':
    case '!=': {
      const cl = eqClass(l);
      const cr = eqClass(r);
      if (cl && cr && cl !== cr) throw mismatch(op, l, r);
      const eq = looseDeepEqual(l.v, r.v);
      return { v: op === '==' ? eq : !eq, t: 'bool' };
    }
    case '&&':
    case '||': {
      // Both sides are always evaluated (expr-lang type-checks both at compile
      // time even though its VM short-circuits; the language is pure).
      if (!boolish(l) || !boolish(r)) throw mismatch(op, l, r);
      const v = op === '&&' ? isTruthy(l.v) && isTruthy(r.v) : isTruthy(l.v) || isTruthy(r.v);
      return { v, t: 'bool' };
    }
    default:
      throw new Error(`unexpected operator ${op}`);
  }
}

// evalTypedSource is the strict entry point walkGraph uses: it fails loudly with
// the real engine's core messages (empty expression, unknown name, mismatched
// types) instead of swallowing errors.
function evalTypedSource(src: string, env: EvalEnv): TV {
  if (src === '') throw new Error('expression is empty');
  return evalTV(new Parser(tokenize(src)).parse(), env);
}

// evalBoolStrict mirrors the engine's evalBool: any value compiles, but a
// non-boolean result fails with the engine's exact wording.
function evalBoolStrict(src: string, env: EvalEnv): boolean {
  const tv = evalTypedSource(src, env);
  if (typeof tv.v !== 'boolean') {
    throw new Error(`condition ${q(src)} did not evaluate to a boolean`);
  }
  return tv.v;
}

// evalStringStrict mirrors the engine's evalString (manual_review case fields).
function evalStringStrict(src: string, env: EvalEnv): string {
  const tv = evalTypedSource(src, env);
  if (typeof tv.v !== 'string') {
    throw new Error(`expression ${q(src)} did not evaluate to a string`);
  }
  return tv.v;
}

// evalExpr evaluates an expression string against a record, returning undefined on
// a parse/eval error. This is the LENIENT wrapper for surfaces outside the walker
// (policy dispositions, model expressions, demo split-edge conditions) — the
// walker itself uses the strict typed evaluation above and fails loudly.
export function evalExpr(src: string, rec: Record<string, unknown>): unknown {
  try {
    return evalTypedSource(src, { rec, tags: new Map(), mode: 'expr' }).v;
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

// errMsg extracts a thrown error's message (fail fast: anything else is a bug).
function errMsg(e: unknown): string {
  if (e instanceof Error) return e.message;
  return String(e);
}

// hasNonFinite reports whether a value tree contains a non-finite number — Go's
// encoding/json refuses to marshal ±Inf/NaN, so the real engine records the WHOLE
// node output as null when one appears (toJSON in execute.go).
function hasNonFinite(v: unknown): boolean {
  if (typeof v === 'number') return !Number.isFinite(v);
  if (Array.isArray(v)) return v.some(hasNonFinite);
  if (v !== null && typeof v === 'object') {
    return Object.values(v as Record<string, unknown>).some(hasNonFinite);
  }
  return false;
}

// goJSON normalizes a node output the way the engine's toJSON records it: null for
// unmarshalable values (non-finite numbers), a plain JSON tree otherwise.
function goJSON(v: unknown): unknown {
  if (v === undefined) return null;
  if (hasNonFinite(v)) return null;
  return JSON.parse(JSON.stringify(v));
}

// goValue formats a value like Go's %v for the aggregate error messages.
function goValue(v: unknown): string {
  if (typeof v === 'number') {
    if (v === Infinity) return '+Inf';
    if (v === -Infinity) return '-Inf';
    if (Number.isNaN(v)) return 'NaN';
  }
  return String(v);
}

// setField writes one key into an object via Map entries so no dynamic object
// indexing trips the security lint.
function setField(obj: Record<string, unknown>, key: string, val: unknown): void {
  const m = new Map(Object.entries(obj));
  m.set(key, val);
  Object.assign(obj, Object.fromEntries(m));
}

// reasonCodesOf returns a copy of the record's accumulated reason_codes list
// (empty when absent or the wrong shape), mirroring existingReasonCodes in Go.
function reasonCodesOf(rec: Record<string, unknown>): unknown[] {
  const v = new Map(Object.entries(rec)).get('reason_codes');
  return Array.isArray(v) ? [...v] : [];
}

export interface WalkResult {
  status: 'completed' | 'failed' | 'suspended';
  data: Record<string, unknown>;
  output: Record<string, unknown>;
  reasonCodes: ReasonCode[];
  nodes: NodeRecord[];
  caseOpened?: { case_type: string; sla_days: number; company_name?: string };
  suspend?: { node_id: string };
  error?: string;
  failedNode?: string;
}

interface DecisionRowCfg {
  when?: string;
  outputs?: { target?: string; expr?: string }[];
}

// aggregateValues reduces a COLLECT target's values, mirroring the engine's
// aggregateValues (execute.go) check for check and message for message.
function aggregateValues(agg: string, vals: unknown[]): unknown {
  if (agg === '' || agg === 'list') return vals;
  if (agg === 'count') return vals.length;
  if (agg === 'sum' || agg === 'min' || agg === 'max') {
    const nums: number[] = [];
    for (const v of vals) {
      if (typeof v !== 'number') throw new Error(`non-numeric value ${goValue(v)}`);
      if (!Number.isFinite(v)) throw new Error(`non-finite value ${goValue(v)}`);
      nums.push(v);
    }
    if (nums.length === 0) {
      if (agg === 'sum') return 0;
      throw new Error(`${agg} of no values`);
    }
    let acc = nums.at(0) as number;
    for (const f of nums.slice(1)) {
      if (agg === 'sum') acc += f;
      else if (agg === 'min') acc = f < acc ? f : acc;
      else acc = f > acc ? f : acc;
    }
    if (!Number.isFinite(acc)) throw new Error(`${agg} overflowed to a non-finite value`);
    return acc;
  }
  throw new Error(`unknown aggregator ${q(agg)}`);
}

// walkGraph interprets a FlowGraph from its input node, threading a mutable record
// through every node type the real engine executes — with the REAL engine's
// semantics (see the parity note at the top of this file). Models are passed in
// (not read from the store), so the walker is pure and the seed can run it at
// module init.
export function walkGraph(
  graph: FlowGraph,
  input: Record<string, unknown>,
  models: Model[],
  resume?: { outcome: Record<string, unknown> }
): WalkResult {
  const rec: Record<string, unknown> = { ...input };
  const tags = new Map<string, Tag>();
  const env: EvalEnv = { rec, tags, mode: 'expr' };
  const nodes: NodeRecord[] = [];
  const reasonCodes: ReasonCode[] = [];
  let caseOpened: WalkResult['caseOpened'];

  const fail = (failedNode: string, error: string): WalkResult => ({
    status: 'failed',
    data: rec,
    output: {},
    reasonCodes,
    nodes,
    caseOpened,
    error,
    failedNode
  });

  // assignTo writes one derived value into the record (and the node's produced
  // map), remembering its engine type tag for later expressions.
  const assignTo = (produced: Record<string, unknown>, target: string, tv: TV): void => {
    setField(rec, target, tv.v);
    tags.set(target, tv.t);
    setField(produced, target, tv.v);
  };

  const appendReasonCode = (code: ReasonCode): void => {
    setField(rec, 'reason_codes', [...reasonCodesOf(rec), code]);
    reasonCodes.push(code);
  };

  const byId = new Map(graph.nodes.map((n) => [n.id, n]));
  const start = graph.nodes.find((n) => n.type === 'input');
  if (!start) return fail('', 'decision-engine: graph has no input node');

  let cur = start.id;
  // The step bound mirrors the engine's defensive backstop exactly: the graph is
  // acyclic when published, so exceeding len(nodes)+1 steps is a wiring bug.
  for (let step = 0; step <= graph.nodes.length; step += 1) {
    const node = byId.get(cur);
    if (!node) return fail(cur, `decision-engine: edge to unknown node ${q(cur)}`);
    const cfg = nodeConfig(node);
    const outgoing = graph.edges.filter((e) => e.from === node.id);
    let out: unknown = {};
    let next = outgoing.at(0)?.to ?? '';
    let suspendHere = false;
    let finalOutput: Record<string, unknown> | undefined;

    try {
      switch (node.type) {
        case 'input':
          out = {};
          break;

        case 'assignment': {
          const assigns = Array.isArray(cfg.assignments)
            ? (cfg.assignments as { target?: string; expr?: string }[])
            : [];
          const produced: Record<string, unknown> = {};
          for (const a of assigns) {
            const target = String(a.target ?? '');
            let tv: TV;
            try {
              tv = evalTypedSource(String(a.expr ?? ''), env);
            } catch (e) {
              throw new Error(
                `decision-engine: node ${q(node.id)} assignment ${q(target)}: ${errMsg(e)}`
              );
            }
            assignTo(produced, target, tv);
          }
          out = produced;
          break;
        }

        case 'rule': {
          const rules = Array.isArray(cfg.rules)
            ? (cfg.rules as { when?: string; then?: { target?: string; expr?: string }[] }[])
            : [];
          const produced: Record<string, unknown> = {};
          for (let i = 0; i < rules.length; i += 1) {
            const r = rules.at(i);
            if (!r) continue;
            let match: boolean;
            try {
              match = evalBoolStrict(String(r.when ?? ''), env);
            } catch (e) {
              throw new Error(
                `decision-engine: node ${q(node.id)} rule ${i} condition: ${errMsg(e)}`
              );
            }
            if (!match) continue;
            for (const a of r.then ?? []) {
              const target = String(a.target ?? '');
              let tv: TV;
              try {
                tv = evalTypedSource(String(a.expr ?? ''), env);
              } catch (e) {
                throw new Error(
                  `decision-engine: node ${q(node.id)} rule ${i} assignment ${q(target)}: ${errMsg(e)}`
                );
              }
              assignTo(produced, target, tv);
            }
          }
          out = produced;
          break;
        }

        case 'split': {
          if (typeof cfg.condition === 'string') {
            // Real-engine form: a boolean condition selects the "yes"/"no" branch edge.
            let match: boolean;
            try {
              match = evalBoolStrict(cfg.condition, env);
            } catch (e) {
              throw new Error(`decision-engine: node ${q(node.id)} split condition: ${errMsg(e)}`);
            }
            const branch = match ? 'yes' : 'no';
            const edge = outgoing.find((e) => e.branch === branch);
            if (!edge) {
              throw new Error(
                `decision-engine: node ${q(node.id)} split has no ${q(branch)} branch edge`
              );
            }
            out = { branch };
            next = edge.to;
          } else {
            // Demo-authoring dialect (seed graphs): each edge carries a condition
            // expression; the first truthy edge wins, an EXPLICIT unbranched edge is
            // the only default, and no match is a loud flow error — never a silent
            // route down branch[0] (which could auto-approve).
            const taken = outgoing.find((e) => e.branch && isTruthy(evalExpr(e.branch, rec)));
            const chosen = taken ?? outgoing.find((e) => !e.branch);
            if (!chosen) throw new Error(`no branch matched at split "${node.id}"`);
            out = { branch: chosen.branch ?? null };
            next = chosen.to;
          }
          break;
        }

        case 'scorecard': {
          const outKey = typeof cfg.output === 'string' && cfg.output !== '' ? cfg.output : 'score';
          const factors = Array.isArray(cfg.factors)
            ? (cfg.factors as { when?: string; weight?: number }[])
            : [];
          let score = 0;
          for (let i = 0; i < factors.length; i += 1) {
            const f = factors.at(i);
            if (!f) continue;
            let match: boolean;
            try {
              match = evalBoolStrict(String(f.when ?? ''), env);
            } catch (e) {
              throw new Error(`decision-engine: node ${q(node.id)} factor ${i}: ${errMsg(e)}`);
            }
            if (match) score += Number(f.weight ?? 0);
          }
          const produced: Record<string, unknown> = {};
          assignTo(produced, outKey, { v: score, t: 'float64' });
          out = produced;
          break;
        }

        case 'decision_table':
          out = evalDecisionTable(node.id, cfg, env, assignTo);
          break;

        case '2d_matrix': {
          const outKey =
            typeof cfg.output === 'string' && cfg.output !== '' ? cfg.output : 'result';
          const matchAxis = (axis: string, conds: { when?: string }[]): number => {
            for (let i = 0; i < conds.length; i += 1) {
              const c = conds.at(i);
              if (!c) continue;
              let match: boolean;
              try {
                match = evalBoolStrict(String(c.when ?? ''), env);
              } catch (e) {
                throw new Error(`decision-engine: node ${q(node.id)} ${axis} ${i}: ${errMsg(e)}`);
              }
              if (match) return i;
            }
            throw new Error(`decision-engine: node ${q(node.id)} matrix has no matching ${axis}`);
          };
          const rows = Array.isArray(cfg.rows) ? (cfg.rows as { when?: string }[]) : [];
          const cols = Array.isArray(cfg.cols) ? (cfg.cols as { when?: string }[]) : [];
          const cells = Array.isArray(cfg.cells) ? (cfg.cells as unknown[][]) : [];
          const ri = matchAxis('row', rows);
          const ci = matchAxis('col', cols);
          const row = cells.at(ri);
          if (ri >= cells.length || !Array.isArray(row) || ci >= row.length) {
            throw new Error(
              `decision-engine: node ${q(node.id)} matrix cell [${ri}][${ci}] out of range`
            );
          }
          const v = (row as unknown[]).at(ci);
          const produced: Record<string, unknown> = {};
          assignTo(produced, outKey, { v: v === undefined ? null : v, t: 'any' });
          out = produced;
          break;
        }

        case 'code': {
          if (typeof cfg.code === 'string') {
            // Real-engine form: a Starlark script with the context predeclared as the
            // `data` dict. The demo executes the SHARED SUBSET (`name = expression`
            // lines, data['field'] reads, # comments) with Starlark's numeric rules;
            // anything beyond it fails loudly instead of silently diverging.
            if (cfg.code.trim() === '') {
              throw new Error(`decision-engine: node ${q(node.id)} code is empty`);
            }
            const senv: EvalEnv = { rec, tags, mode: 'starlark' };
            const produced: Record<string, unknown> = {};
            for (const line of cfg.code.split('\n')) {
              const stmt = line.trim();
              if (stmt === '' || stmt.startsWith('#')) continue;
              const m = stmt.match(/^([a-zA-Z_][a-zA-Z0-9_]*)\s*=(?!=)\s*(.+)$/);
              if (!m) {
                throw new Error(
                  `decision-engine: node ${q(node.id)} code: unsupported statement ${q(stmt)} (the demo runs the assignment subset of Starlark)`
                );
              }
              const rhs = m[2].replace(/\bdata\[(["'])([a-zA-Z_][a-zA-Z0-9_]*)\1\]/g, '$2');
              let tv: TV;
              try {
                tv = evalTypedSource(rhs, senv);
              } catch (e) {
                throw new Error(`decision-engine: node ${q(node.id)} code: ${errMsg(e)}`);
              }
              assignTo(produced, m[1], tv);
            }
            out = produced;
          } else {
            // Demo-authoring dialect (seed graphs): the same statement DSL under the
            // `source` key with bare record references and expr semantics.
            const source = String(cfg.source ?? '');
            const produced: Record<string, unknown> = {};
            for (const line of source.split('\n')) {
              const stmt = line.trim();
              if (stmt === '' || stmt.startsWith('//') || stmt.startsWith('#')) continue;
              const m = stmt.match(/^([a-zA-Z_][a-zA-Z0-9_]*) *=(?!=) *(.+)$/);
              if (!m) continue;
              let tv: TV;
              try {
                tv = evalTypedSource(m[2], env);
              } catch (e) {
                throw new Error(`decision-engine: node ${q(node.id)} code: ${errMsg(e)}`);
              }
              assignTo(produced, m[1], tv);
            }
            out = produced;
          }
          break;
        }

        case 'reason': {
          const rs = Array.isArray(cfg.reasons)
            ? (cfg.reasons as { when?: string; code?: string; description?: string }[])
            : [];
          const added: ReasonCode[] = [];
          for (let i = 0; i < rs.length; i += 1) {
            const r = rs.at(i);
            if (!r) continue;
            let match: boolean;
            try {
              match = evalBoolStrict(String(r.when ?? ''), env);
            } catch (e) {
              throw new Error(
                `decision-engine: node ${q(node.id)} reason ${i} condition: ${errMsg(e)}`
              );
            }
            if (!match) continue;
            const code = { code: String(r.code ?? ''), description: String(r.description ?? '') };
            added.push(code);
            reasonCodes.push(code);
          }
          // The engine appends to the accumulated list and writes it back even when
          // nothing matched (so reason_codes exists downstream).
          setField(rec, 'reason_codes', [...reasonCodesOf(rec), ...added]);
          out = { reason_codes: added };
          break;
        }

        case 'manual_review': {
          if (typeof cfg.company_name === 'string') {
            // Real-engine form: company_name and case_type are EXPRESSIONS evaluated
            // against the record (quote a literal, e.g. "'aml'"); sla_days is literal.
            let company: string;
            try {
              company = evalStringStrict(cfg.company_name, env);
            } catch (e) {
              throw new Error(`decision-engine: node ${q(node.id)} company_name: ${errMsg(e)}`);
            }
            let caseType: string;
            try {
              caseType = evalStringStrict(String(cfg.case_type ?? ''), env);
            } catch (e) {
              throw new Error(`decision-engine: node ${q(node.id)} case_type: ${errMsg(e)}`);
            }
            const sla = typeof cfg.sla_days === 'number' ? cfg.sla_days : 0;
            const code = { code: 'MANUAL_REVIEW', description: 'Escalated to manual review' };
            appendReasonCode(code);
            caseOpened = { case_type: caseType, sla_days: sla, company_name: company };
            out = {
              company_name: company,
              case_type: caseType,
              sla_days: sla,
              reason_codes: [code]
            };
            if (resume) {
              const outKey = String(cfg.output_key ?? 'review');
              setField(rec, outKey, resume.outcome);
              for (const [k, v] of Object.entries(resume.outcome)) setField(rec, k, v);
            } else if (cfg.suspend === true) {
              suspendHere = true;
            }
          } else {
            // Demo-authoring dialect (seed graphs): literal case fields.
            caseOpened = {
              case_type: String(cfg.case_type ?? 'manual_review'),
              sla_days: typeof cfg.sla_days === 'number' ? cfg.sla_days : 3
            };
            appendReasonCode({
              code: 'MANUAL_REVIEW',
              description: 'Escalated to manual review'
            });
            if (resume) {
              // Resuming: inject the reviewer's outcome so downstream nodes branch on it.
              const outKey = String(cfg.output_key ?? 'review');
              setField(rec, outKey, resume.outcome);
              for (const [k, v] of Object.entries(resume.outcome)) setField(rec, k, v);
              out = { resumed: true, ...resume.outcome };
            } else if (isTruthy(cfg.suspend)) {
              // A durable human task: pause the decision here.
              suspendHere = true;
              out = { suspended: true };
            } else {
              out = { case_opened: true };
            }
          }
          break;
        }

        case 'predict': {
          const outKey = String(cfg.output ?? 'prediction');
          const bucket = new Map(Object.entries((rec.predict as Record<string, unknown>) ?? {}));
          let v: unknown;
          if (bucket.has(outKey)) {
            // Pre-resolved by the caller (the Go shell's seam): echo it, like the
            // engine's preResolved pass-through.
            v = bucket.get(outKey);
          } else {
            // Demo provider seam: the in-browser registry stands in for the shell.
            const modelName = String(cfg.model ?? '');
            const model = models.find((m) => m.name === modelName);
            v = model ? evaluateModel(model, rec) : { score: 0.5, probability: 0.5 };
            bucket.set(outKey, v);
            rec.predict = Object.fromEntries(bucket);
          }
          out = Object.fromEntries([[outKey, v]]);
          break;
        }

        case 'connect': {
          const outKey = String(cfg.output ?? 'connect');
          const bucket = new Map(Object.entries((rec.connect as Record<string, unknown>) ?? {}));
          let v: unknown;
          if (bucket.has(outKey)) {
            v = bucket.get(outKey);
          } else {
            // The real connect node fetches from an external connector; the demo
            // injects plausible, connector-shaped data as its provider seam.
            const connector = String(cfg.connector ?? cfg.name ?? '');
            v = connectorSample(connector);
            bucket.set(outKey, v);
            rec.connect = Object.fromEntries(bucket);
          }
          out = Object.fromEntries([[outKey, v]]);
          break;
        }

        case 'ai': {
          const outKey = String(cfg.output ?? 'ai');
          const bucket = new Map(Object.entries((rec.ai as Record<string, unknown>) ?? {}));
          let v: unknown;
          if (bucket.has(outKey)) {
            v = bucket.get(outKey);
          } else {
            // Demo provider seam: the in-browser agent stands in for Agent Manager.
            const prompt = String(cfg.prompt ?? rec.prompt ?? '');
            v = agentReply(prompt, cfg.schema as AgentSchema | undefined, rec).text;
            bucket.set(outKey, v);
            rec.ai = Object.fromEntries(bucket);
          }
          out = Object.fromEntries([[outKey, v]]);
          break;
        }

        case 'output': {
          const fields = Array.isArray(cfg.fields) ? (cfg.fields as unknown[]).map(String) : [];
          const assigns = Array.isArray(cfg.assignments)
            ? (cfg.assignments as { target?: string; expr?: string }[])
            : [];
          if (fields.length > 0) {
            // Real-engine form: only the named fields form the response; the reserved
            // reason_codes compliance field is always surfaced.
            const entries = new Map(Object.entries(rec));
            const resp: Record<string, unknown> = {};
            for (const f of fields) setField(resp, f, entries.has(f) ? entries.get(f) : null);
            if (entries.has('reason_codes') && !fields.includes('reason_codes')) {
              setField(resp, 'reason_codes', entries.get('reason_codes'));
            }
            out = resp;
            finalOutput = resp;
          } else if (assigns.length > 0) {
            // Demo-authoring dialect (seed graphs): output nodes may enrich the record
            // before emitting the whole of it.
            const produced: Record<string, unknown> = {};
            for (const a of assigns) {
              const target = String(a.target ?? '');
              let tv: TV;
              try {
                tv = evalTypedSource(String(a.expr ?? ''), env);
              } catch (e) {
                throw new Error(
                  `decision-engine: node ${q(node.id)} assignment ${q(target)}: ${errMsg(e)}`
                );
              }
              assignTo(produced, target, tv);
            }
            out = produced;
            finalOutput = { ...rec };
          } else {
            const clone = { ...rec };
            out = clone;
            finalOutput = clone;
          }
          break;
        }

        default:
          throw new Error(
            `decision-engine: node ${q(node.id)} has no execution engine for type ${q(String(node.type))}`
          );
      }
    } catch (e) {
      // The failing node is recorded in the trace with a null output, exactly like
      // the engine (toJSON of a nil output), then the run fails loudly.
      nodes.push({ node_id: node.id, type: node.type, output: null });
      return fail(node.id, errMsg(e));
    }

    nodes.push({ node_id: node.id, type: node.type, output: goJSON(out) });
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
      return {
        status: 'completed',
        data: rec,
        output: finalOutput ?? {},
        reasonCodes,
        nodes,
        caseOpened
      };
    }
    if (!next) {
      // Only an output node completes a run: any other node with nowhere to go is
      // a wiring bug, failed with the real engine's exact runtime message.
      return fail(node.id, `decision-engine: flow dead-ends at non-output node ${q(node.id)}`);
    }
    cur = next;
  }
  return fail(cur, 'decision-engine: execution exceeded the node bound');
}

// evalDecisionTable resolves a Decision Table node under its DMN-style hit policy,
// mirroring evalDecisionTable in execute.go: FIRST/UNIQUE/ANY apply one row's
// outputs, RULE_ORDER/COLLECT gather values per target across matching rows (and
// COLLECT reduces them by the aggregate), and the deprecated mode:"all" applies
// every matching row in order against the live record.
function evalDecisionTable(
  nodeId: string,
  cfg: Record<string, unknown>,
  env: EvalEnv,
  assignTo: (produced: Record<string, unknown>, target: string, tv: TV) => void
): Record<string, unknown> {
  const rows = Array.isArray(cfg.rows) ? (cfg.rows as DecisionRowCfg[]) : [];
  let hit = String(cfg.hit ?? '')
    .trim()
    .toLowerCase();
  if (hit === '') {
    if (
      String(cfg.mode ?? '')
        .trim()
        .toLowerCase() === 'all'
    ) {
      // Deprecated mode:"all": every matching row applies in order (last write wins
      // per target); conditions and outputs share the LIVE record.
      const applied: Record<string, unknown> = {};
      for (let i = 0; i < rows.length; i += 1) {
        const row = rows.at(i);
        if (!row) continue;
        const res = evalTableRow(nodeId, i, row, env, env);
        if (!res) continue;
        for (const [k, tv] of res) assignTo(applied, k, tv);
      }
      return applied;
    }
    hit = 'first';
  }

  // Conditions read the live record; outputs write to a per-row scratch clone so
  // rows stay independent of each other.
  const matched: { idx: number; outputs: Map<string, TV> }[] = [];
  for (let i = 0; i < rows.length; i += 1) {
    const row = rows.at(i);
    if (!row) continue;
    const rowEnv: EvalEnv = { rec: { ...env.rec }, tags: new Map(env.tags), mode: env.mode };
    const res = evalTableRow(nodeId, i, row, env, rowEnv);
    if (!res) continue;
    matched.push({ idx: i, outputs: res });
    if (hit === 'first') break;
  }

  const applied: Record<string, unknown> = {};
  if (hit === 'first' || hit === 'unique' || hit === 'any') {
    if (hit === 'unique' && matched.length > 1) {
      throw new Error(
        `decision-engine: node ${q(nodeId)} UNIQUE hit policy: ${matched.length} rows matched`
      );
    }
    if (hit === 'any') {
      const firstOutputs = matched.at(0)?.outputs;
      for (const m of matched) {
        if (!tableOutputsEqual(m.outputs, firstOutputs)) {
          throw new Error(
            `decision-engine: node ${q(nodeId)} ANY hit policy: matching rows produce conflicting outputs`
          );
        }
      }
    }
    const winner = matched.at(0);
    if (winner) for (const [k, tv] of winner.outputs) assignTo(applied, k, tv);
    return applied;
  }
  if (hit === 'rule_order' || hit === 'collect') {
    const agg =
      hit === 'collect'
        ? String(cfg.aggregate ?? '')
            .trim()
            .toLowerCase()
        : '';
    const lists = new Map<string, unknown[]>();
    const order: string[] = [];
    for (const m of matched) {
      for (const a of rows.at(m.idx)?.outputs ?? []) {
        const target = String(a.target ?? '');
        if (!lists.has(target)) {
          lists.set(target, []);
          order.push(target);
        }
        lists.get(target)?.push(m.outputs.get(target)?.v ?? null);
      }
    }
    for (const target of order) {
      let v: unknown;
      try {
        v = aggregateValues(agg, lists.get(target) ?? []);
      } catch (e) {
        throw new Error(
          `decision-engine: node ${q(nodeId)} COLLECT ${q(agg)} of ${q(target)}: ${errMsg(e)}`
        );
      }
      const t: Tag = agg === 'count' ? 'int' : agg === '' || agg === 'list' ? 'array' : 'float64';
      assignTo(applied, target, { v, t });
    }
    return applied;
  }
  throw new Error(`decision-engine: node ${q(nodeId)} unknown hit policy ${q(hit)}`);
}

// evalTableRow evaluates one row: its condition against condEnv and, on a match,
// its outputs against outEnv (each output visible to later outputs in the same
// row). Returns the row's typed outputs, or null when the row does not match.
function evalTableRow(
  nodeId: string,
  i: number,
  row: DecisionRowCfg,
  condEnv: EvalEnv,
  outEnv: EvalEnv
): Map<string, TV> | null {
  let match: boolean;
  try {
    match = evalBoolStrict(String(row.when ?? ''), condEnv);
  } catch (e) {
    throw new Error(`decision-engine: node ${q(nodeId)} row ${i} condition: ${errMsg(e)}`);
  }
  if (!match) return null;
  const out = new Map<string, TV>();
  for (const a of row.outputs ?? []) {
    const target = String(a.target ?? '');
    let tv: TV;
    try {
      tv = evalTypedSource(String(a.expr ?? ''), outEnv);
    } catch (e) {
      throw new Error(
        `decision-engine: node ${q(nodeId)} row ${i} output ${q(target)}: ${errMsg(e)}`
      );
    }
    setField(outEnv.rec, target, tv.v);
    outEnv.tags.set(target, tv.t);
    out.set(target, tv);
  }
  return out;
}

function tableOutputsEqual(a: Map<string, TV>, b: Map<string, TV> | undefined): boolean {
  if (!b || a.size !== b.size) return false;
  for (const [k, tv] of a) {
    if (!b.has(k) || !looseDeepEqual(tv.v, b.get(k)?.v)) return false;
  }
  return true;
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
