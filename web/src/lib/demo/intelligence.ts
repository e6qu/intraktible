// SPDX-License-Identifier: AGPL-3.0-or-later
// Demo-side compute for the "decision intelligence" features — node-traversal stats
// (heatmap), counterfactual search ("what would flip this?"), and coverage/red-team
// fuzzing. The static demo has no Go backend, so these mirror the real endpoints by
// re-running the demo engine: runFlow is pure (it threads a record through the graph
// and never touches the store), so re-running it hundreds of times is safe and the
// results are genuine rather than canned.

import type {
  Flow,
  FlowGraph,
  Decision,
  Disposition,
  FlowNodeStats,
  Counterfactual,
  CounterfactualFlip,
  Coverage
} from '$lib/api';
import { runFlow, pickVersion } from './engine';

type DispCount = { approve: number; decline: number; refer: number };
function emptyDisp(): DispCount {
  return { approve: 0, decline: 0, refer: 0 };
}
function tally(d: DispCount, disp?: Disposition): void {
  if (disp === 'approve' || disp === 'decline' || disp === 'refer') d[disp] += 1;
}

// nodeStats aggregates how often each node of the flow's latest graph was traversed
// across its recorded decisions — the data behind the heatmap (hot vs never-hit nodes).
export function nodeStats(flow: Flow, decisions: Decision[]): FlowNodeStats {
  const decs = decisions.filter((d) => d.flow_id === flow.flow_id && d.status === 'completed');
  const { graph } = pickVersion(flow, 'production');
  const counts = new Map<string, number>();
  const dispositions = emptyDisp();
  for (const d of decs) {
    for (const n of d.nodes ?? []) counts.set(n.node_id, (counts.get(n.node_id) ?? 0) + 1);
    tally(dispositions, d.disposition);
  }
  const total = decs.length;
  const nodes = graph.nodes.map((n) => {
    const count = counts.get(n.id) ?? 0;
    return { node_id: n.id, type: n.type, count, pct: total ? count / total : 0 };
  });
  return { total, dispositions, nodes };
}

// --- Counterfactual: minimal single-field change that flips a non-favorable decision ---

// Dispositions ordered worst -> best, so we can tell whether a re-run improved.
const RANK: Record<string, number> = { decline: 0, refer: 1, approve: 2 };
function isBetter(next: Disposition | undefined, base: Disposition): boolean {
  return next != null && (RANK[next] ?? -1) > (RANK[base] ?? -1);
}

export function counterfactual(flow: Flow, decision: Decision): Counterfactual {
  const base = decision.disposition;
  if (!base || base === 'approve') {
    return { disposition: base ?? 'approve', flips: [], searched: 0 };
  }
  const { graph } = pickVersion(flow, decision.environment ?? 'production');
  const data = (decision.data as Record<string, unknown>) ?? {};
  const numeric = Object.entries(data)
    .filter(([, v]) => typeof v === 'number' && Number.isFinite(v as number))
    .map(([k]) => k);
  let searched = 0;
  const disp = (val: number, field: string): Disposition | undefined => {
    searched += 1;
    return runFlow(flow, graph, { ...data, [field]: val }).disposition;
  };
  const flips: CounterfactualFlip[] = [];
  for (const field of numeric) {
    if (searched > 360) break;
    const flip = searchFlip(field, data[field] as number, base, disp);
    if (flip) flips.push(flip);
  }
  // Smallest relative change first — the easiest lever to pull.
  flips.sort((a, b) => relChange(a) - relChange(b));
  return { disposition: base, flips, searched };
}

function relChange(f: CounterfactualFlip): number {
  return Math.abs((f.to - f.from) / (f.from || 1));
}

// searchFlip scans a field outward in both directions for the nearest value that
// improves the disposition, then binary-searches the boundary. ~22 evals per field.
function searchFlip(
  field: string,
  from: number,
  base: Disposition,
  disp: (val: number, field: string) => Disposition | undefined
): CounterfactualFlip | null {
  const span = Math.max(Math.abs(from) * 8, 50);
  const STEPS = 8;
  for (let i = 1; i <= STEPS; i++) {
    for (const dir of [1, -1]) {
      const val = from + (dir * span * i) / STEPS;
      const d = disp(val, field);
      if (isBetter(d, base)) {
        const r = refineBoundary(from, val, base, field, disp);
        return {
          field,
          from,
          to: round(r.to),
          direction: r.to > from ? 'increase' : 'decrease',
          disposition: r.d
        };
      }
    }
  }
  return null;
}

// refineBoundary narrows [from (not improving) .. found (improving)] to the closest
// value that still improves the disposition.
function refineBoundary(
  from: number,
  found: number,
  base: Disposition,
  field: string,
  disp: (val: number, field: string) => Disposition | undefined
): { to: number; d: Disposition } {
  let a = from;
  let b = found;
  let dB = disp(b, field) as Disposition;
  for (let i = 0; i < 12; i++) {
    const mid = (a + b) / 2;
    const d = disp(mid, field);
    if (isBetter(d, base)) {
      b = mid;
      dB = d as Disposition;
    } else {
      a = mid;
    }
  }
  return { to: b, d: dB };
}

function round(v: number): number {
  return Math.abs(v) >= 100 ? Math.round(v) : Math.round(v * 100) / 100;
}

// --- Coverage / red-team: fuzz synthetic inputs and report node/branch coverage --------

export function coverage(flow: Flow, graph: FlowGraph, runs: number): Coverage {
  const n = Math.max(1, Math.min(runs || 200, 2000));
  const { fields, thresholds } = discoverInputs(graph);
  const nodeHits = new Map<string, number>();
  const edgeHits = new Map<string, number>();
  const dispositions = emptyDisp();
  // The branch is part of the key: a split has several edges with the same from/to but
  // different conditions, and only the edge whose condition the run took is hit.
  const edgeKey = (from: string, to: string, branch: string) => `${from}>${to}>${branch}`;
  for (let i = 0; i < n; i++) {
    const r = runFlow(flow, graph, synthInput(fields, thresholds, i));
    const trace = r.nodes ?? [];
    for (const nr of trace) nodeHits.set(nr.node_id, (nodeHits.get(nr.node_id) ?? 0) + 1);
    // Consecutive nodes name the edge traversed; a split records its chosen branch in its
    // output, which disambiguates parallel edges between the same two nodes.
    for (let j = 0; j + 1 < trace.length; j++) {
      const chosen = (trace[j].output as { branch?: string } | undefined)?.branch ?? '';
      const k = edgeKey(trace[j].node_id, trace[j + 1].node_id, chosen);
      edgeHits.set(k, (edgeHits.get(k) ?? 0) + 1);
    }
    tally(dispositions, r.disposition);
  }
  const nodes = graph.nodes.map((nd) => ({
    node_id: nd.id,
    type: nd.type,
    hits: nodeHits.get(nd.id) ?? 0
  }));
  const hitsFor = (e: { from: string; to: string; branch?: string }) =>
    edgeHits.get(edgeKey(e.from, e.to, e.branch ?? '')) ?? 0;
  const branches = graph.edges.map((e) => ({
    from: e.from,
    to: e.to,
    branch: e.branch ?? '',
    hits: hitsFor(e)
  }));
  const dead_nodes = nodes.filter((nd) => nd.hits === 0).map((nd) => nd.node_id);
  const dead_branches = graph.edges
    .filter((e) => e.branch && hitsFor(e) === 0)
    .map((e) => ({ from: e.from, to: e.to, branch: e.branch ?? '' }));
  return { runs: n, fields, nodes, branches, dispositions, dead_nodes, dead_branches };
}

// RESERVED also excludes the node namespaces (predict.x / connect.x / ai.x are derived
// outputs, not inputs the caller supplies), so the bare 'predict'/'connect'/'ai' that a
// dotted reference splits into don't leak in as fuzz dimensions.
const RESERVED = new Set([
  'true',
  'false',
  'null',
  'and',
  'or',
  'not',
  'if',
  'else',
  'min',
  'max',
  'abs',
  'round',
  'floor',
  'ceil',
  'predict',
  'connect',
  'ai'
]);

// discoverInputs harvests both the input identifiers a graph branches on (names that
// appear in expressions / conditions but are NOT themselves assigned — a target/output is
// derived) and the numeric thresholds those conditions compare against, so the fuzzer can
// sample right around each gate. Heuristic but matches what the real coverage endpoint does.
function discoverInputs(graph: FlowGraph): { fields: string[]; thresholds: number[] } {
  const found = new Set<string>();
  const targets = new Set<string>();
  const derived = new Set<string>(); // suffixes of a dotted ref (predict.probability) — not inputs
  const thresholds = new Set<number>();
  const harvest = (s: string) => {
    for (const m of s.matchAll(/[a-zA-Z_][a-zA-Z0-9_.]*\.([a-zA-Z_][a-zA-Z0-9_]*)/g)) {
      derived.add(m[1]);
    }
    for (const m of s.matchAll(/[a-zA-Z_][a-zA-Z0-9_]*/g)) {
      if (!RESERVED.has(m[0])) found.add(m[0]);
    }
    for (const m of s.matchAll(/-?\d+(?:\.\d+)?/g)) {
      const v = Number(m[0]);
      if (Number.isFinite(v)) thresholds.add(v);
    }
  };
  for (const e of graph.edges) if (e.branch) harvest(e.branch);
  for (const nd of graph.nodes) {
    const cfg = JSON.stringify(nd.config ?? {});
    for (const m of cfg.matchAll(/"(?:expr|when)"\s*:\s*"([^"]*)"/g)) harvest(m[1]);
    for (const m of cfg.matchAll(/"(?:target|output)"\s*:\s*"([^"]*)"/g)) targets.add(m[1]);
  }
  const fields = [...found]
    .filter((f) => !targets.has(f) && !derived.has(f) && !f.includes('.'))
    .slice(0, 12);
  return { fields, thresholds: [...thresholds] };
}

// rangeFor scales a synthetic value to the field's likely magnitude so score gates and
// money gates both get exercised.
function rangeFor(field: string): number {
  if (/income|amount|balance|limit|revenue|salary|loan|debt/i.test(field)) return 100_000;
  if (/fico|credit_score/i.test(field)) return 850;
  return 100;
}

// synthInput deterministically generates one fuzz input from an index (no Math.random,
// so the endpoint is reproducible — the same flow + runs gives the same coverage). Half
// the time a field is sampled near one of the graph's own thresholds (so both sides of
// each gate get hit — better branch coverage and a realistic disposition spread); the
// rest of the time it's a broad random value over the field's plausible range.
function synthInput(fields: string[], thresholds: number[], seed: number): Record<string, number> {
  let s = ((seed + 1) * 2654435761) % 2147483647;
  const rnd = () => {
    s = (s * 1103515245 + 12345) & 0x7fffffff;
    return s / 0x7fffffff;
  };
  const out: Record<string, number> = {};
  for (const f of fields) {
    if (thresholds.length && rnd() < 0.5) {
      const t = thresholds[Math.floor(rnd() * thresholds.length)];
      const jitter = (rnd() - 0.5) * Math.max(Math.abs(t) * 0.6, 4);
      out[f] = Math.round((t + jitter) * 100) / 100;
    } else {
      out[f] = Math.round(rnd() * rangeFor(f) * 100) / 100;
    }
  }
  return out;
}
