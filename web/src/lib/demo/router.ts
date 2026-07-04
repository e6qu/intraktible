// SPDX-License-Identifier: AGPL-3.0-or-later
// The demo backend's request router. handleDemo maps (method, path) to a handler
// that reads/mutates the in-memory store and returns the wrapped shape each
// api.ts function expects. Path-param routes are matched by a small pattern
// matcher (no dynamic object indexing — variable-key lookups use Map/find to keep
// eslint-plugin-security happy). Anything not matched here falls through to the
// install layer's safe default.

import type {
  Flow,
  FlowVersion,
  Decision,
  Case,
  Agent,
  Model,
  Policy,
  AuditEntry,
  ManagedApiKey,
  ScheduledDeploy,
  FlowGrant,
  FlowGraph,
  Environment,
  Disposition,
  CaseStatus,
  MonitorMetric,
  MonitorOp,
  Role,
  Scope,
  MrmModel
} from '$lib/api';
import {
  state,
  nextId,
  driftReportFor,
  psi,
  ahead,
  pushAudit,
  auditDecisionRun,
  auditRunSteps,
  auditRunEnd
} from './store';
import {
  decideFlow,
  runAssertionsFor,
  scoreEvalCases,
  backtestFlowDataset,
  runFlow,
  evalExpr,
  evaluateModel,
  pickVersion,
  validateGraph
} from './engine';
import { nodeStats, counterfactual, coverage } from './intelligence';
import { agentReply } from './agent';

export interface DemoResponse {
  status: number;
  body: unknown;
  text?: string;
}

type Body = Record<string, unknown>;
type Query = URLSearchParams;
type Handler = (m: RegExpMatchArray, body: Body, query: Query) => DemoResponse;

function ok(body: unknown): DemoResponse {
  return { status: 200, body };
}
function text(t: string): DemoResponse {
  return { status: 200, body: null, text: t };
}
function notFound(): DemoResponse {
  return { status: 404, body: { error: 'not found' } };
}
function unauthorized(): DemoResponse {
  return { status: 401, body: { error: 'not authenticated' } };
}
function badRequest(error: string): DemoResponse {
  return { status: 400, body: { error } };
}
function forbidden(error: string): DemoResponse {
  return { status: 403, body: { error } };
}

// RBAC ranks, mirroring the platform's roles (viewer < operator < editor < approver
// < admin). roleAtLeast lets handlers gate writes by the switched demo user's role so
// maker-checker (and read-only viewers) behave like the real backend.
const ROLE_RANK = new Map<string, number>([
  ['viewer', 1],
  ['operator', 2],
  ['editor', 3],
  ['approver', 4],
  ['admin', 5]
]);
function roleAtLeast(min: string): boolean {
  const have = ROLE_RANK.get(state.identity.role ?? '') ?? 0;
  return have >= (ROLE_RANK.get(min) ?? 99);
}

const ENVIRONMENTS = new Set(['sandbox', 'staging', 'production']);
function isEnvironment(e: string): boolean {
  return ENVIRONMENTS.has(e);
}

function findFlow(idOrSlug: string): Flow | undefined {
  return state.flows.find((f) => f.flow_id === idOrSlug || f.slug === idOrSlug);
}

// sameGraph compares two flow graphs by their LOGIC only (node id/type/name/config + edge
// from/to/branch), ignoring canvas positions — so a re-publish with no logic change is a
// no-op rather than a new duplicate version.
function sameGraph(a: FlowGraph, b: FlowGraph): boolean {
  const norm = (g: FlowGraph) =>
    JSON.stringify({
      nodes: (g.nodes ?? []).map((n) => ({
        id: n.id,
        type: n.type,
        name: n.name,
        config: n.config
      })),
      edges: (g.edges ?? []).map((e) => ({ from: e.from, to: e.to, branch: e.branch }))
    });
  return norm(a) === norm(b);
}

// normJson canonicalizes a JSON value (recursively sorting object keys) so two
// input schemas that differ only in key order compare equal — mirroring the
// canonical JSON the real backend's etag hashes.
function normJson(v: unknown): string {
  if (Array.isArray(v)) return '[' + v.map(normJson).join(',') + ']';
  if (v && typeof v === 'object') {
    return (
      '{' +
      Object.entries(v as Record<string, unknown>)
        .sort(([a], [b]) => a.localeCompare(b))
        .map(([k, val]) => JSON.stringify(k) + ':' + normJson(val))
        .join(',') +
      '}'
    );
  }
  return JSON.stringify(v) ?? 'null';
}

// sameSchema compares two input schemas by content. The real backend's etag covers
// graph AND input_schema, so a schema-only change must publish a new version.
function sameSchema(a: unknown, b: unknown): boolean {
  return normJson(a ?? null) === normJson(b ?? null);
}

// --- Route table ----------------------------------------------------------------
// Each entry is [method, RegExp over the pathname, handler]. The first match wins;
// patterns capture path params as numbered groups.

interface Route {
  method: string;
  re: RegExp;
  fn: Handler;
}

const routes: Route[] = [];
function route(method: string, pattern: string, fn: Handler): void {
  // Convert :param segments into capture groups. The pattern is always a literal
  // route string defined in this file (never user input), so the dynamic RegExp is
  // safe — disable the SAST rule for this single, audited construction.
  // eslint-disable-next-line security/detect-non-literal-regexp
  const re = new RegExp('^' + pattern.replace(/:[a-zA-Z]+/g, '([^/]+)') + '$');
  routes.push({ method, re, fn });
}

// --- Auth -----------------------------------------------------------------------
// Sign-out is real in the demo: logout flips this flag so /v1/me 401s (refreshUser then
// clears the user and the shell shows the logged-out screen) until the next login. It is
// module-scoped (not persisted), so a fresh page load starts signed in, like a new visit.
let loggedOut = false;
route('POST', '/v1/login', () => {
  loggedOut = false;
  return ok(state.identity);
});
route('GET', '/v1/me', () => (loggedOut ? unauthorized() : ok(state.identity)));
route('POST', '/v1/logout', () => {
  loggedOut = true;
  return ok({});
});
route('GET', '/v1/auth/oidc/providers', () => ok({ providers: [] }));
route('GET', '/v1/auth/saml/providers', () => ok({ providers: [] }));

// --- Hello (legacy demo endpoint) -----------------------------------------------
route('GET', '/v1/hello/stats', () =>
  ok({
    org: 'demo',
    workspace: 'main',
    count: 3,
    last_name: 'Ada',
    last_at: new Date().toISOString()
  })
);
route('POST', '/v1/hello', (_m, body) =>
  ok({ event_id: nextId('evt'), seq: state.seq++, name: body.name })
);

// --- Flows ----------------------------------------------------------------------
route('GET', '/v1/flows', () => ok({ flows: state.flows }));
route('POST', '/v1/flows', (_m, body) => {
  const slug = String(body.slug ?? '').trim();
  if (!slug) return badRequest('slug is required');
  if (state.flows.some((f) => f.slug === slug))
    return badRequest(`a flow with slug "${slug}" already exists`);
  const name = String(body.name ?? slug);
  const flowId = nextId('flow');
  const emptyGraph = { nodes: [{ id: 'in', type: 'input' as const, name: 'Input' }], edges: [] };
  state.flows.push({
    flow_id: flowId,
    slug,
    name,
    latest: 1,
    versions: [
      {
        version: 1,
        etag: 'e1',
        graph: emptyGraph,
        published_at: new Date().toISOString(),
        published_by: state.identity.actor
      }
    ],
    deployments: {}
  });
  pushAudit('decision.flow.created', 'decision.flows', { flow_id: flowId, slug, name });
  return ok({ flow_id: flowId });
});
// importFlowDoc upserts one exported flow document, mirroring the real backend's
// ImportFlow command: a new slug creates the flow and publishes v1; an existing slug
// publishes the imported graph + input_schema as a new version — unless the latest
// version already carries that exact content ("already at vN — no change"), which
// makes a re-import a no-op. So export→edit→re-import round-trips.
interface FlowImportOutcome {
  flow_id?: string;
  slug: string;
  version?: number;
  etag?: string;
  created: boolean;
  published: boolean;
  error?: string;
}
function importFlowDoc(doc: Body): FlowImportOutcome {
  const slug = String(doc.slug ?? '').trim();
  if (!slug) return { slug, created: false, published: false, error: 'slug is required' };
  const name = String(doc.name ?? slug);
  // The real ImportFlow publishes through the same graph validation as a direct
  // publish. An absent graph decodes to the zero graph (as Go's json decode does),
  // which validation rejects loudly with "graph has no nodes".
  const graph = (doc.graph as FlowGraph | undefined) ?? { nodes: [], edges: [] };
  const invalid = validateGraph(graph);
  if (invalid) return { slug, created: false, published: false, error: invalid };
  const existing = state.flows.find((f) => f.slug === slug);
  if (existing) {
    const latest = existing.versions.find((v) => v.version === existing.latest);
    if (!latest) throw new Error(`flow ${slug} has no version ${existing.latest}`);
    if (sameGraph(latest.graph, graph) && sameSchema(latest.input_schema, doc.input_schema)) {
      return {
        flow_id: existing.flow_id,
        slug,
        version: latest.version,
        etag: latest.etag,
        created: false,
        published: false
      };
    }
    const version = existing.latest + 1;
    const v: FlowVersion = {
      version,
      etag: `etag-v${version}`,
      graph,
      input_schema: doc.input_schema,
      published_at: new Date().toISOString(),
      published_by: state.identity.actor
    };
    existing.versions.push(v);
    existing.latest = version;
    pushAudit('decision.flow.version_published', 'decision.flows', {
      flow_id: existing.flow_id,
      version,
      imported: true
    });
    return {
      flow_id: existing.flow_id,
      slug,
      version,
      etag: v.etag,
      created: false,
      published: true
    };
  }
  const flowId = nextId('flow');
  state.flows.push({
    flow_id: flowId,
    slug,
    name,
    latest: 1,
    versions: [
      {
        version: 1,
        etag: 'e1',
        graph,
        input_schema: doc.input_schema,
        published_at: new Date().toISOString(),
        published_by: state.identity.actor
      }
    ],
    deployments: {}
  });
  pushAudit('decision.flow.created', 'decision.flows', {
    flow_id: flowId,
    slug,
    name,
    imported: true
  });
  return { flow_id: flowId, slug, version: 1, etag: 'e1', created: true, published: true };
}
route('POST', '/v1/flows/import', (_m, body) => {
  const out = importFlowDoc(body);
  if (out.error) return badRequest(out.error);
  return ok(out);
});
route('POST', '/v1/flows/import-bundle', (_m, body) => {
  // Import each flow in the bundle with the same upsert semantics as /import: an
  // existing slug with a DIFFERENT graph/schema is updated (published), an identical
  // one is unchanged, and a bad document fails only its own row.
  const flows = Array.isArray((body as Body).flows) ? ((body as Body).flows as Body[]) : [];
  const results: FlowImportOutcome[] = [];
  let published = 0;
  let failed = 0;
  let unchanged = 0;
  for (const f of flows) {
    const out = importFlowDoc(f);
    results.push(out);
    if (out.error) failed += 1;
    else if (out.published) published += 1;
    else unchanged += 1;
  }
  return ok({ results, published, failed, unchanged });
});
route('GET', '/v1/flows/:id', (m) => {
  const flow = findFlow(m[1]);
  return flow ? ok(flow) : notFound();
});
route('GET', '/v1/flows/:id/export', (m, _b, q) => {
  const flow = findFlow(m[1]);
  if (!flow) return notFound();
  const format = q.get('format') ?? 'mermaid';
  return text(exportGraph(flow, format));
});
route('POST', '/v1/flows/:id/versions', (m, body) => {
  const flow = findFlow(m[1]);
  if (!flow) return notFound();
  // A missing graph decodes to the zero graph (as Go's json decode does), which the
  // publish gate then rejects with the real backend's "graph has no nodes" — the demo
  // never silently publishes an empty version.
  const graph = (body.graph as FlowVersion['graph']) ?? { nodes: [], edges: [] };
  const invalid = validateGraph(graph);
  if (invalid) return badRequest(invalid);
  const latest = flow.versions.find((vv) => vv.version === flow.latest);
  // A no-op publish (logic identical to the latest version, ignoring canvas positions)
  // returns the current version instead of stacking duplicate versions — matching the
  // /engine import path, which already says "already at vN — no change". The real
  // backend's etag hashes the input_schema too, so a schema-only change publishes.
  if (
    latest &&
    sameGraph(latest.graph, graph) &&
    sameSchema(latest.input_schema, body.input_schema)
  ) {
    return ok({ version: latest.version, etag: latest.etag, published: false });
  }
  const version = flow.latest + 1;
  const v: FlowVersion = {
    version,
    // Match the seed etag style (etag-…) so a freshly-published version doesn't read as
    // a different format ("e4") than the seeded ones ("etag-c3").
    etag: `etag-v${version}`,
    graph,
    input_schema: body.input_schema,
    published_at: new Date().toISOString(),
    published_by: state.identity.actor
  };
  flow.versions.push(v);
  flow.latest = version;
  pushAudit('decision.flow.version_published', 'decision.flows', {
    flow_id: flow.flow_id,
    version
  });
  return ok({ version, etag: v.etag, published: true });
});
route('GET', '/v1/flows/:id/metrics', (m) => {
  const flow = findFlow(m[1]);
  if (!flow) return notFound();
  return ok(flowMetrics(flow));
});
route('GET', '/v1/flows/:id/slo', (m) => {
  const flow = findFlow(m[1]);
  if (!flow) return notFound();
  const slo = state.flowSlos.get(flow.flow_id) ?? null;
  if (!slo) return ok({ slo: null });
  const metrics = flowMetrics(flow);
  const successRate = metrics.total ? metrics.completed / metrics.total : 1;
  const errorBudget = 1 - slo.success_target;
  const remaining = errorBudget ? 1 - (1 - successRate) / errorBudget : 1;
  return ok({
    slo,
    attainment: {
      decisions: metrics.total,
      success_rate: Math.round(successRate * 1000) / 1000,
      success_target: slo.success_target,
      success_met: successRate >= slo.success_target,
      error_budget: errorBudget,
      budget_remaining: Math.round(remaining * 1000) / 1000,
      avg_latency_ms: metrics.avg_duration_ms,
      latency_target_ms: slo.latency_target_ms,
      latency_met: slo.latency_target_ms === 0 || metrics.avg_duration_ms <= slo.latency_target_ms
    }
  });
});
route('PUT', '/v1/flows/:id/slo', (m, body) => {
  const flow = findFlow(m[1]);
  if (!flow) return notFound();
  if (!roleAtLeast('operator')) return forbidden('setting an SLO requires the operator role');
  const success = Number(body.success_target ?? 0);
  // Clearing (target 0) REMOVES the objective so the card reads "no objective set",
  // rather than persisting a degenerate always-passing 0% target.
  if (!success) {
    state.flowSlos.delete(flow.flow_id);
    return ok({});
  }
  state.flowSlos.set(flow.flow_id, {
    success_target: success,
    latency_target_ms: Number(body.latency_target_ms ?? 0)
  });
  return ok({});
});
// monitorStatus evaluates a monitor against the flow's live analytics, so a monitor a
// user just created actually computes (it used to be pinned to "no data" forever).
// Metrics that need data the demo can't derive live (e.g. drift without a baseline)
// report not-computable, and the caller keeps any seeded status for those.
function monitorStatus(
  flow: Flow,
  metric: MonitorMetric,
  op: MonitorOp,
  threshold: number
): { actual: number; computable: boolean; firing: boolean } {
  const mtr = flowMetrics(flow);
  const total = mtr.total;
  const disp = mtr.by_disposition as Record<string, number>;
  let actual = 0;
  let computable = total > 0;
  switch (metric) {
    case 'failure_rate':
      actual = total ? mtr.failed / total : 0;
      break;
    case 'refer_rate':
      actual = total ? (disp.refer ?? 0) / total : 0;
      break;
    case 'decline_rate':
      actual = total ? (disp.decline ?? 0) / total : 0;
      break;
    case 'automation_rate':
      actual = total ? (total - (disp.refer ?? 0)) / total : 0;
      break;
    case 'volume':
      actual = total;
      computable = true;
      break;
    case 'avg_latency_ms':
      actual = mtr.avg_duration_ms;
      break;
    case 'distribution_drift_psi': {
      const dr = driftReportFor(flow.flow_id);
      actual = dr.psi ?? 0;
      computable = dr.has_baseline && dr.has_current;
      break;
    }
    default:
      computable = false;
  }
  actual = Math.round(actual * 1000) / 1000;
  const firing = computable && (op === 'lt' ? actual < threshold : actual > threshold);
  return { actual, computable, firing };
}

route('GET', '/v1/flows/:id/monitors', (m) => {
  const flow = findFlow(m[1]);
  if (!flow) return notFound();
  const monitors = (state.monitors.get(flow.flow_id) ?? []).map((mon) => {
    const s = monitorStatus(flow, mon.metric, mon.op, mon.threshold);
    return { ...mon, status: s.computable ? s : mon.status };
  });
  return ok({ monitors });
});
route('POST', '/v1/flows/:id/monitors', (m, body) => {
  const flow = findFlow(m[1]);
  if (!flow) return notFound();
  const list = state.monitors.get(flow.flow_id) ?? [];
  const monitorId = nextId('mon');
  list.push({
    monitor_id: monitorId,
    flow_id: flow.flow_id,
    metric: body.metric as MonitorMetric,
    op: body.op as MonitorOp,
    threshold: Number(body.threshold ?? 0),
    description: body.description ? String(body.description) : undefined,
    status: { actual: 0, computable: false, firing: false }
  });
  state.monitors.set(flow.flow_id, list);
  return ok({ monitor_id: monitorId });
});
route('DELETE', '/v1/flows/:id/monitors/:mid', (m) => {
  const flow = findFlow(m[1]);
  if (!flow) return notFound();
  state.monitors.set(
    flow.flow_id,
    (state.monitors.get(flow.flow_id) ?? []).filter((x) => x.monitor_id !== m[2])
  );
  return ok({});
});
route('POST', '/v1/flows/:id/monitors/check', (m) => {
  const flow = findFlow(m[1]);
  if (!flow) return notFound();
  const fired = (state.monitors.get(flow.flow_id) ?? [])
    .map((x) => ({ x, s: monitorStatus(flow, x.metric, x.op, x.threshold) }))
    .filter(({ x, s }) => (s.computable ? s.firing : x.status.firing))
    .map(({ x, s }) => ({
      monitor_id: x.monitor_id,
      metric: x.metric,
      op: x.op,
      threshold: x.threshold,
      actual: s.computable ? s.actual : x.status.actual,
      description: x.description
    }));
  return ok({
    flow_id: flow.flow_id,
    checked: (state.monitors.get(flow.flow_id) ?? []).length,
    fired,
    deliveries: fired.length
      ? [{ webhook_id: 'wh_1', url: state.webhooks[0]?.url ?? '', ok: true, status: 200 }]
      : []
  });
});
route('GET', '/v1/flows/:id/assertions', (m) => {
  const flow = findFlow(m[1]);
  if (!flow) return notFound();
  return ok({ cases: state.assertions.get(flow.flow_id) ?? [] });
});
route('PUT', '/v1/flows/:id/assertions', (m, body) => {
  const flow = findFlow(m[1]);
  if (!flow) return notFound();
  state.assertions.set(flow.flow_id, Array.isArray(body.cases) ? (body.cases as Case[] & []) : []);
  return ok({});
});
route('POST', '/v1/flows/:id/assertions/run', (m) => {
  const flow = findFlow(m[1]);
  if (!flow) return notFound();
  return ok(runAssertionsFor(flow, state.assertions.get(flow.flow_id) ?? []));
});
route('POST', '/v1/flows/:id/baseline', (m) => {
  const flow = findFlow(m[1]);
  if (!flow) return notFound();
  // Capture SHARES plus the count at capture time, like the real engine's
  // DistributionOf (Baseline{Approve, Decline, Refer, Total}).
  const counts = new Map<string, number>([
    ['approve', 0],
    ['decline', 0],
    ['refer', 0]
  ]);
  let total = 0;
  for (const d of state.decisions) {
    if (d.flow_id === flow.flow_id && d.disposition) {
      counts.set(d.disposition, (counts.get(d.disposition) ?? 0) + 1);
      total += 1;
    }
  }
  const t = total || 1;
  state.flowBaselines.set(flow.flow_id, {
    approve: (counts.get('approve') ?? 0) / t,
    decline: (counts.get('decline') ?? 0) / t,
    refer: (counts.get('refer') ?? 0) / t,
    total
  });
  return ok({});
});
route('GET', '/v1/flows/:id/drift', (m) => {
  const flow = findFlow(m[1]);
  if (!flow) return notFound();
  return ok(driftReportFor(flow.flow_id));
});
route('POST', '/v1/flows/:id/deployments', (m, body) => {
  const flow = findFlow(m[1]);
  if (!flow) return notFound();
  const env = String(body.environment) as Environment;
  if (env === 'production')
    return { status: 400, body: { error: 'production requires a deployment request' } };
  setDeployment(
    flow,
    env,
    Number(body.version),
    body.challenger_version as number | undefined,
    body.challenger_pct as number | undefined
  );
  pushAudit('decision.flow.version_deployed', 'decision.flows', {
    flow_id: flow.flow_id,
    environment: env,
    version: body.version
  });
  return ok({});
});
route('POST', '/v1/flows/:id/deployments/rollback', (m, body) => {
  const flow = findFlow(m[1]);
  if (!flow) return notFound();
  const env = String(body.environment);
  const dep = new Map(Object.entries(flow.deployments ?? {})).get(env);
  if (!dep) return badRequest(`nothing deployed to ${env} to roll back`);
  // Restore the version that was live before the current one; only if that's unknown
  // fall back to current-1.
  const prev = dep.previous_version ?? Math.max(1, dep.version - 1);
  setDeployment(flow, env as Environment, prev);
  pushAudit('decision.flow.version_rolled_back', 'decision.flows', {
    flow_id: flow.flow_id,
    environment: env,
    version: prev
  });
  return ok({ version: prev });
});
route('POST', '/v1/flows/:id/deployments/schedule', (m, body) => {
  const flow = findFlow(m[1]);
  if (!flow) return notFound();
  const list = state.schedules.get(flow.flow_id) ?? [];
  const scheduleId = nextId('sch');
  const sched: ScheduledDeploy = {
    schedule_id: scheduleId,
    flow_id: flow.flow_id,
    environment: String(body.environment),
    version: Number(body.version),
    at: String(body.at),
    until: body.until ? String(body.until) : undefined,
    status: 'pending',
    created_at: new Date().toISOString()
  };
  list.push(sched);
  state.schedules.set(flow.flow_id, list);
  return ok({ schedule_id: scheduleId });
});
route('GET', '/v1/flows/:id/deployments/schedules', (m) => {
  const flow = findFlow(m[1]);
  if (!flow) return notFound();
  return ok({ schedules: state.schedules.get(flow.flow_id) ?? [] });
});
route('DELETE', '/v1/flows/:id/deployments/schedules/:sid', (m) => {
  const flow = findFlow(m[1]);
  if (!flow) return notFound();
  state.schedules.set(
    flow.flow_id,
    (state.schedules.get(flow.flow_id) ?? []).filter((s) => s.schedule_id !== m[2])
  );
  return ok({});
});
route('GET', '/v1/flows/:id/grants', (m) => {
  const flow = findFlow(m[1]);
  if (!flow) return notFound();
  return ok({ grants: state.grants.get(flow.flow_id) ?? [] });
});
route('POST', '/v1/flows/:id/grants', (m, body) => {
  const flow = findFlow(m[1]);
  if (!flow) return notFound();
  const list = state.grants.get(flow.flow_id) ?? [];
  const grantId = nextId('grant');
  const grant: FlowGrant = {
    grant_id: grantId,
    flow_id: flow.flow_id,
    actor: String(body.actor ?? ''),
    environment: String(body.environment ?? '*'),
    created_by: state.identity.actor,
    created_at: new Date().toISOString()
  };
  list.push(grant);
  state.grants.set(flow.flow_id, list);
  return ok({ grant_id: grantId });
});
route('DELETE', '/v1/flows/:id/grants/:gid', (m) => {
  const flow = findFlow(m[1]);
  if (!flow) return notFound();
  state.grants.set(
    flow.flow_id,
    (state.grants.get(flow.flow_id) ?? []).filter((g) => g.grant_id !== m[2])
  );
  return ok({});
});
route('POST', '/v1/flows/:id/promote', (m, body) => {
  const flow = findFlow(m[1]);
  if (!flow) return notFound();
  const to = String(body.to);
  const from = String(body.from);
  const fromDep = new Map(Object.entries(flow.deployments ?? {})).get(from);
  // Promote the version actually live in the source env; refuse if nothing is there
  // (silently promoting `latest` would push a version that was never validated in
  // the source environment past the gates).
  if (!fromDep) return badRequest(`nothing deployed to ${from} to promote from`);
  const version = fromDep.version;
  if (to === 'production') {
    addRequest(flow, 'production', version);
    return ok({
      promoted: false,
      pending: true,
      request_id: flow.deployment_requests?.at(-1)?.request_id,
      version
    });
  }
  setDeployment(flow, to as Environment, version);
  return ok({ promoted: true, version });
});
route('PUT', '/v1/flows/:id/promotion-policy', (m, body) => {
  const flow = findFlow(m[1]);
  if (!flow) return notFound();
  flow.promotion_policy = (body.policy as Flow['promotion_policy']) ?? flow.promotion_policy;
  return ok({ policy: flow.promotion_policy });
});
route('GET', '/v1/flows/:id/shadow', (m) => {
  const flow = findFlow(m[1]);
  if (!flow) return notFound();
  const shadows = Object.fromEntries(state.shadows.get(flow.flow_id) ?? new Map());
  const report: Record<string, unknown> = {};
  for (const [env, v] of Object.entries(shadows)) {
    // Replay the shadow version over this env's recorded decisions and compare its
    // disposition to what actually shipped — a real divergence count (so v1/v2/v3 give
    // different numbers), not a constant.
    const version = flow.versions.find((ver) => ver.version === Number(v));
    const recent = state.decisions
      .filter((d) => d.flow_id === flow.flow_id && d.environment === env && d.disposition)
      .slice(0, 25);
    let matched = 0;
    let diverged = 0;
    let errored = 0;
    const sampleDiverged: string[] = [];
    for (const d of recent) {
      const run = version
        ? runFlow(flow, version.graph, (d.data as Record<string, unknown>) ?? {})
        : undefined;
      if (!run || run.status !== 'completed' || !run.disposition) {
        errored += 1;
      } else if (run.disposition === d.disposition) {
        matched += 1;
      } else {
        diverged += 1;
        if (sampleDiverged.length < 3) sampleDiverged.push(d.decision_id);
      }
    }
    const rmap = new Map(Object.entries(report));
    rmap.set(env, {
      shadow_version: v,
      total: recent.length,
      matched,
      diverged,
      errored,
      sample_diverged: sampleDiverged
    });
    Object.assign(report, Object.fromEntries(rmap));
  }
  return ok({ shadows, report });
});
route('PUT', '/v1/flows/:id/shadow', (m, body) => {
  const flow = findFlow(m[1]);
  if (!flow) return notFound();
  const sh = state.shadows.get(flow.flow_id) ?? new Map<string, number>();
  const version = Number(body.version);
  if (version === 0) sh.delete(String(body.environment));
  else sh.set(String(body.environment), version);
  state.shadows.set(flow.flow_id, sh);
  return ok({});
});
route('POST', '/v1/flows/:id/deployment-requests', (m, body) => {
  const flow = findFlow(m[1]);
  if (!flow) return notFound();
  addRequest(
    flow,
    String(body.environment),
    Number(body.version),
    body.challenger_version as number | undefined,
    body.challenger_pct as number | undefined
  );
  return ok({ request_id: flow.deployment_requests?.at(-1)?.request_id });
});
route('POST', '/v1/flows/:id/deployment-requests/:rid/approve', (m, body) => {
  const flow = findFlow(m[1]);
  if (!flow) return notFound();
  const req = (flow.deployment_requests ?? []).find((r) => r.request_id === m[2]);
  if (!req) return notFound();
  // Four-eyes: an approval requires the approver role, and the proposer cannot
  // approve their own request — so the maker-checker story is real once you switch
  // user with the demo identity switcher.
  if (!roleAtLeast('approver'))
    return forbidden('approving a deployment requires the approver role');
  if (req.requested_by === state.identity.actor) {
    return badRequest('four-eyes: the requester cannot approve their own deployment');
  }
  if (req.status !== 'pending') return badRequest('request already decided');
  req.status = 'approved';
  req.decided_by = state.identity.actor;
  req.decided_at = new Date().toISOString();
  req.reason = String(body.reason ?? '');
  setDeployment(flow, req.environment, req.version, req.challenger_version, req.challenger_pct);
  pushAudit('decision.flow.deployment_approved', 'decision.flows', {
    flow_id: flow.flow_id,
    environment: req.environment,
    version: req.version
  });
  return ok({});
});
route('POST', '/v1/flows/:id/deployment-requests/:rid/reject', (m, body) => {
  const flow = findFlow(m[1]);
  if (!flow) return notFound();
  const req = (flow.deployment_requests ?? []).find((r) => r.request_id === m[2]);
  if (!req) return notFound();
  if (!roleAtLeast('approver'))
    return forbidden('rejecting a deployment requires the approver role');
  if (req.status !== 'pending') return badRequest('request already decided');
  req.status = 'rejected';
  req.decided_by = state.identity.actor;
  req.decided_at = new Date().toISOString();
  req.reason = String(body.reason ?? '');
  pushAudit('decision.flow.deployment_rejected', 'decision.flows', {
    flow_id: flow.flow_id,
    environment: req.environment,
    version: req.version
  });
  return ok({});
});
route('POST', '/v1/flows/:id/backtest', (m, body) => {
  const flow = findFlow(m[1]);
  if (!flow) return notFound();
  return ok(
    backtestFlowDataset(flow, {
      dataset: (body.dataset as Body[]) ?? [],
      compareVersion: body.compare_version as number | undefined
    })
  );
});
route('POST', '/v1/flows/:id/whatif', (m, body) => {
  const flow = findFlow(m[1]);
  if (!flow) return notFound();
  const base = (body.base as Body) ?? {};
  const field = String(body.field ?? '');
  const values = Array.isArray(body.values) ? body.values : [];
  const graph = flow.versions.find((v) => v.version === flow.latest)?.graph ??
    flow.versions.at(-1)?.graph ?? { nodes: [], edges: [] };
  let prev: string | undefined;
  let transitions = 0;
  const points = values.map((value) => {
    const fieldMap = new Map(Object.entries(base));
    fieldMap.set(field, value);
    const run = runFlow(flow, graph, Object.fromEntries(fieldMap));
    const sig = JSON.stringify(run.output);
    const changed = prev !== undefined && prev !== sig;
    if (changed) transitions += 1;
    prev = sig;
    return { value, status: run.status, output: run.output, changed };
  });
  return ok({ field, points, transitions });
});

// --- Decide ---------------------------------------------------------------------

// honorPreapproval returns a decision served straight from an active pre-approval when
// one matches the request's entity (and bound flow, if any), incrementing its honored
// count — else null, so the caller walks the flow normally.
function honorPreapproval(
  flow: Flow,
  env: Environment,
  body: Body,
  data: Record<string, unknown>
): DemoResponse | null {
  const entityType = body.entity_type ? String(body.entity_type) : '';
  const entityId = body.entity_id ? String(body.entity_id) : '';
  if (!entityType || !entityId) return null;
  const grant = state.preapprovals.find(
    (g) =>
      g.status === 'active' &&
      g.entity_type === entityType &&
      g.entity_id === entityId &&
      (!g.flow_slug || g.flow_slug === flow.slug) &&
      new Date(g.valid_until).getTime() > Date.now()
  );
  if (!grant) return null;
  grant.honored_count += 1;
  grant.updated_at = new Date().toISOString();
  const decisionId = nextId('dec');
  const now = new Date().toISOString();
  // The grant's terms (limit/apr/…) ARE the decision output on honor — the grant form
  // labels them exactly that. They used to be dropped (output = the input echo).
  const output =
    grant.terms && typeof grant.terms === 'object' ? { ...data, ...grant.terms } : data;
  const reasonCodes = [
    { code: 'PRE_APPROVED', description: `Served from pre-approval ${grant.preapproval_id}` }
  ];
  const decision: Decision = {
    decision_id: decisionId,
    flow_id: flow.flow_id,
    slug: flow.slug,
    version: pickVersion(flow, env).version,
    environment: env,
    variant: 'champion',
    status: 'completed',
    data,
    output,
    reason_codes: reasonCodes,
    disposition: grant.disposition,
    disposition_reason: 'pre-approval honored',
    preapproval_id: grant.preapproval_id,
    nodes: [],
    started_at: now,
    ended_at: now,
    duration_ms: 1
  };
  state.decisions.unshift(decision);
  auditDecisionRun(decision);
  return ok({
    decision_id: decisionId,
    status: 'completed',
    data: { ...output, reason_codes: reasonCodes },
    disposition: grant.disposition,
    disposition_reason: 'pre-approval honored',
    preapproval_id: grant.preapproval_id
  });
}
// inputTypeError validates a decide input against the flow version's input_schema, so a
// wrong-typed field (a string where a number is expected) is rejected at the boundary
// instead of silently scoring on garbage and recording a confident "completed" decision.
function inputTypeError(
  flow: Flow,
  env: Environment,
  data: Record<string, unknown>
): string | null {
  const ver =
    flow.versions.find((v) => v.version === pickVersion(flow, env).version) ?? flow.versions.at(-1);
  const props = (
    ver?.input_schema as { properties?: Record<string, { type?: string }> } | undefined
  )?.properties;
  if (!props) return null;
  const specs = new Map(Object.entries(props));
  for (const [k, v] of Object.entries(data)) {
    const want = specs.get(k)?.type;
    if (!want) continue;
    const got = Array.isArray(v) ? 'array' : v === null ? 'null' : typeof v;
    const ok =
      ((want === 'number' || want === 'integer') && got === 'number') ||
      (want === 'boolean' && got === 'boolean') ||
      (want === 'string' && got === 'string') ||
      (want !== 'number' && want !== 'integer' && want !== 'boolean' && want !== 'string');
    if (!ok) return `field "${k}" must be a ${want}, got ${got}`;
  }
  return null;
}
route('POST', '/v1/flows/:slug/:env/decide', (m, body) => {
  const flow = findFlow(m[1]);
  if (!flow) return notFound();
  if (!isEnvironment(m[2])) return badRequest(`unknown environment "${m[2]}"`);
  const data = (body.data as Body) ?? {};
  const typeErr = inputTypeError(flow, m[2] as Environment, data);
  if (typeErr) return badRequest(typeErr);
  // A preview run (the builder's test) computes the full result but records nothing —
  // no decision, no audit, no metrics — matching the real backend's Preview.
  if (body.preview === true) {
    const { result } = decideFlow(flow, m[2] as Environment, data, { record: false });
    return ok({ ...result, decision_id: '' });
  }
  // Pre-approval fast path: an active grant for this entity short-circuits the flow —
  // the decision is served instantly from the grant (the real backend's behavior),
  // incrementing its honored count, rather than walking the graph.
  const honored = honorPreapproval(flow, m[2] as Environment, body, data);
  if (honored) return honored;
  // decideFlow journals the run (started / node steps / terminal) into the audit log.
  const { result } = decideFlow(flow, m[2] as Environment, data);
  return ok(result);
});
route('POST', '/v1/flows/:slug/:env/decide/batch', (m, body) => {
  const flow = findFlow(m[1]);
  if (!flow) return notFound();
  if (!isEnvironment(m[2])) return badRequest(`unknown environment "${m[2]}"`);
  const dataset = (body.dataset as Body[]) ?? [];
  let completed = 0;
  let failed = 0;
  let rejected = 0;
  const results = dataset.map((row, index) => {
    // The same input-contract validation the single decide applies, per row: a
    // wrong-typed field rejects the row (no decision recorded), matching the real
    // batch's "rejected" status alongside completed/failed.
    const typeErr = inputTypeError(flow, m[2] as Environment, row);
    if (typeErr) {
      rejected += 1;
      return { index, status: 'rejected' as const, error: typeErr };
    }
    const { result } = decideFlow(flow, m[2] as Environment, row);
    if (result.status === 'completed') completed += 1;
    else failed += 1;
    return {
      index,
      decision_id: result.decision_id,
      status: result.status,
      data: result.data,
      disposition: result.disposition,
      disposition_reason: result.disposition_reason,
      error: result.error
    };
  });
  return ok({ total: dataset.length, completed, failed, rejected, results });
});
route('POST', '/v1/flows/:slug/:env/preapprove/batch', (m, body) => {
  const flow = findFlow(m[1]);
  if (!flow) return notFound();
  if (!isEnvironment(m[2])) return badRequest(`unknown environment "${m[2]}"`);
  const dataset = (body.dataset as Body[]) ?? [];
  const wantDisp = (body.disposition as Disposition) ?? 'approve';
  const entityType = String(body.entity_type ?? 'applicant');
  const entityKey = String(body.entity_key ?? 'id');
  const validDays = Number(body.valid_days ?? 30);
  let granted = 0;
  let skipped = 0;
  let failed = 0;
  let rejected = 0;
  const results = dataset.map((row, index) => {
    // Same per-row input-contract validation as decide/batch: a wrong-typed field
    // rejects the row without recording a decision or granting anything.
    const typeErr = inputTypeError(flow, m[2] as Environment, row);
    if (typeErr) {
      rejected += 1;
      return { index, status: 'rejected' as const, granted: false, error: typeErr };
    }
    const { result } = decideFlow(flow, m[2] as Environment, row);
    if (result.status !== 'completed') {
      failed += 1;
      return { index, status: result.status as 'failed', granted: false, error: result.error };
    }
    const entityId = String(
      Object.entries(row).find(([k]) => k === entityKey)?.[1] ?? `${entityType}-${index}`
    );
    if (result.disposition === wantDisp) {
      granted += 1;
      const preapprovalId = nextId('pa');
      state.preapprovals.unshift({
        preapproval_id: preapprovalId,
        entity_type: entityType,
        entity_id: entityId,
        disposition: wantDisp,
        flow_slug: flow.slug,
        valid_until: ahead(validDays),
        status: 'active',
        honored_count: 0,
        note: body.note ? String(body.note) : undefined,
        granted_at: new Date().toISOString(),
        granted_by: state.identity.actor,
        updated_at: new Date().toISOString()
      });
      return {
        index,
        entity_id: entityId,
        decision_id: result.decision_id,
        status: result.status as 'completed',
        disposition: result.disposition,
        disposition_reason: result.disposition_reason,
        granted: true,
        preapproval_id: preapprovalId
      };
    }
    skipped += 1;
    return {
      index,
      entity_id: entityId,
      decision_id: result.decision_id,
      status: result.status as 'completed',
      disposition: result.disposition,
      disposition_reason: result.disposition_reason,
      granted: false
    };
  });
  return ok({ total: dataset.length, granted, skipped, failed, rejected, results });
});

// --- Decisions ------------------------------------------------------------------
route('GET', '/v1/decisions', (_m, _b, q) => ok(filterDecisions(q)));
route('GET', '/v1/decisions/:id/export', (m, _b, q) => {
  const d = state.decisions.find((x) => x.decision_id === m[1]);
  if (!d) return notFound();
  return text(exportDecisionTrace(d, q.get('format') ?? 'mermaid'));
});
route('GET', '/v1/decisions/:id', (m) => {
  const d = state.decisions.find((x) => x.decision_id === m[1]);
  return d ? ok(d) : notFound();
});
// Resume a decision paused at a durable human task: re-run the flow from the recorded
// record with the reviewer's outcome injected, then complete the same decision and
// resolve its case. Mirrors POST /v1/decisions/{id}/resume on the real backend.
route('POST', '/v1/decisions/:id/resume', (m, body) => {
  const d = state.decisions.find((x) => x.decision_id === m[1]);
  if (!d) return notFound();
  if (d.status !== 'suspended') return badRequest('decision is not suspended');
  const flow = findFlow(d.flow_id);
  if (!flow) return notFound();
  const version = flow.versions.find((v) => v.version === d.version) ?? flow.versions.at(-1);
  const graph = version?.graph ?? { nodes: [], edges: [] };
  const outcome = ((body as Body).outcome as Record<string, unknown>) ?? {};
  // The trace recorded at suspension ends at the manual-review node; everything the
  // re-run walks beyond that point is the post-resume portion of the journal.
  const stepsBeforeResume = (d.nodes ?? []).length;
  const run = runFlow(flow, graph, (d.data as Record<string, unknown>) ?? {}, { outcome });
  // The reviewer's decision is AUTHORITATIVE — it becomes the disposition. The re-run
  // re-derives the same machine outcome, so without this the three Resume buttons
  // (approve/decline/refer) would all land on the same disposition.
  const choice = String(outcome.decision ?? '').toLowerCase();
  const reviewerDisp = (['approve', 'decline', 'refer'] as Disposition[]).find((x) => x === choice);
  d.status = run.status;
  d.data = run.data;
  d.output = run.output;
  d.disposition = reviewerDisp ?? run.disposition;
  d.disposition_reason = reviewerDisp ? `Resolved by reviewer: ${choice}` : d.disposition_reason;
  d.reason_codes = [
    ...(reviewerDisp
      ? [{ code: `REVIEW_${choice.toUpperCase()}`, description: `Reviewer decision: ${choice}` }]
      : []),
    ...run.reasonCodes.filter((rc) => rc.code !== 'MANUAL_REVIEW')
  ];
  d.nodes = run.nodes;
  d.ended_at = new Date().toISOString();
  // Resolve the case the suspension opened.
  const c = state.cases.find((x) => x.source_decision_id === d.decision_id);
  if (c) c.status = 'completed';
  // Journal the resumption like the real engine: resumed, then the node steps the
  // re-run walked past the suspension point, then the terminal event.
  pushAudit('decision.run.resumed', 'decision.runs', {
    decision_id: d.decision_id,
    flow_id: d.flow_id
  });
  auditRunSteps(d.decision_id, run.nodes.slice(stepsBeforeResume));
  auditRunEnd(d);
  return ok({ decision_id: d.decision_id, status: d.status, disposition: d.disposition });
});

// --- Cases ----------------------------------------------------------------------
route('GET', '/v1/cases/summary', (_m, _b, q) => ok(caseSummary(q)));
route('POST', '/v1/cases/sla-sweep', () => {
  // A real sweep: recompute every OPEN case's SLA state from its remaining days and flag
  // newly-breached ones (it used to only COUNT cases already seeded overdue and mutate
  // nothing, so the summary never moved). A re-run then breaches 0 — there's nothing new.
  let breached = 0;
  const now = new Date().toISOString();
  for (const c of state.cases) {
    if (c.status === 'completed') continue;
    const was = c.sla_state;
    c.sla_state = c.days_left <= 0 ? 'overdue' : c.days_left <= 1 ? 'due_soon' : 'on_track';
    if (c.sla_state === 'overdue' && was !== 'overdue') {
      breached += 1;
      c.updated_at = now;
      c.audit.push({ type: 'case.sla_breached', actor: 'system', at: now });
      pushAudit('cases.sla_breached', 'cases', { case_id: c.case_id });
    }
  }
  return ok({ count: breached });
});
route('GET', '/v1/cases', (_m, _b, q) => ok({ cases: filterCases(q) }));
route('POST', '/v1/cases', (_m, body) => {
  const caseId = nextId('case');
  const now = new Date().toISOString();
  const slaDays = Number(body.sla_days ?? 3);
  state.cases.unshift({
    case_id: caseId,
    company_name: String(body.company_name ?? 'New review'),
    case_type: String(body.case_type ?? 'review'),
    status: 'needs_review',
    sla_days: slaDays,
    days_left: slaDays,
    sla_state: 'on_track',
    notes: [],
    audit: [{ type: 'case.opened', actor: state.identity.actor, at: now }],
    created_at: now,
    updated_at: now
  });
  pushAudit('cases.review_requested', 'cases', { case_id: caseId, case_type: body.case_type });
  return ok({ case_id: caseId });
});
route('GET', '/v1/cases/:id', (m) => {
  const c = state.cases.find((x) => x.case_id === m[1]);
  return c ? ok(c) : notFound();
});
route('POST', '/v1/cases/:id/assign', (m, body) => {
  const c = state.cases.find((x) => x.case_id === m[1]);
  if (!c) return notFound();
  c.assignee = String(body.assignee ?? '');
  c.updated_at = new Date().toISOString();
  c.audit.push({
    type: 'case.assigned',
    actor: state.identity.actor,
    at: c.updated_at,
    detail: c.assignee
  });
  pushAudit('cases.assigned', 'cases', { case_id: c.case_id, assignee: c.assignee });
  return ok({});
});
const CASE_STATUSES = new Set<string>(['needs_review', 'in_progress', 'completed']);
route('POST', '/v1/cases/:id/status', (m, body) => {
  const c = state.cases.find((x) => x.case_id === m[1]);
  if (!c) return notFound();
  const next = String(body.status ?? '');
  if (!CASE_STATUSES.has(next)) return badRequest(`unknown case status "${next}"`);
  c.status = next as CaseStatus;
  c.updated_at = new Date().toISOString();
  c.audit.push({
    type: 'case.status',
    actor: state.identity.actor,
    at: c.updated_at,
    detail: c.status
  });
  pushAudit('cases.status_changed', 'cases', { case_id: c.case_id, status: c.status });
  return ok({});
});
route('POST', '/v1/cases/:id/notes', (m, body) => {
  const c = state.cases.find((x) => x.case_id === m[1]);
  if (!c) return notFound();
  const at = new Date().toISOString();
  c.notes.push({ author: state.identity.actor, text: String(body.text ?? ''), at });
  c.audit.push({ type: 'case.note', actor: state.identity.actor, at });
  c.updated_at = at;
  pushAudit('cases.note_added', 'cases', { case_id: c.case_id });
  return ok({});
});

// --- Agents ---------------------------------------------------------------------
route('GET', '/v1/agents', () => ok({ agents: state.agents }));
route('POST', '/v1/agents', (_m, body) => {
  const name = String(body.name ?? '').trim();
  if (!name) return badRequest('an agent name is required');
  const existing = state.agents.find((a) => a.name === name);
  // The Define-agent form is a CREATE: a blank resubmit of an existing name used to
  // Object.assign undefined over its provider/model/tools, silently wiping it.
  if (existing) return badRequest(`an agent named "${name}" already exists`);
  const agent: Agent = {
    name,
    provider: body.provider ? String(body.provider) : undefined,
    model: body.model ? String(body.model) : undefined,
    system: body.system ? String(body.system) : undefined,
    schema: body.schema,
    tools: Array.isArray(body.tools) ? (body.tools as string[]) : [],
    latest: 1,
    runs: 0,
    updated_at: new Date().toISOString()
  };
  state.agents.push(agent);
  return ok({});
});
route('GET', '/v1/agent-runs/summary', () => ok(runSummary()));
route('GET', '/v1/agents/:name', (m) => {
  const a = state.agents.find((x) => x.name === m[1]);
  return a ? ok(a) : notFound();
});
route('POST', '/v1/agents/:name/run', (m, body) => {
  const agent = state.agents.find((x) => x.name === m[1]);
  if (!agent) return notFound();
  const runId = nextId('run');
  const prompt = String(body.prompt ?? '');
  const reply = agentReply(prompt, agent.schema as { properties?: Record<string, unknown> });
  state.agentRuns.unshift({
    run_id: runId,
    agent: agent.name,
    model: agent.model,
    prompt,
    status: 'completed',
    text: reply.text,
    structured: reply.structured,
    at: new Date().toISOString()
  });
  agent.runs += 1;
  return ok({ run_id: runId, status: 'completed', text: reply.text, structured: reply.structured });
});
route('GET', '/v1/agents/:name/runs', (m) =>
  ok({ runs: state.agentRuns.filter((r) => r.agent === m[1]) })
);
route('GET', '/v1/agents/:name/versions', (m) =>
  ok({ versions: state.agentVersions.get(m[1]) ?? [] })
);
route('GET', '/v1/agents/:name/evals', (m) => ok({ cases: state.agentEvals.get(m[1]) ?? [] }));
route('PUT', '/v1/agents/:name/evals', (m, body) => {
  state.agentEvals.set(m[1], Array.isArray(body.cases) ? (body.cases as never[]) : []);
  return ok({});
});
route('POST', '/v1/agents/:name/evals/run', (m, body) => {
  const agent = state.agents.find((x) => x.name === m[1]);
  if (!agent) return notFound();
  const version = Number(body.version ?? agent.latest ?? 1);
  const results = scoreEvalCases(
    state.agentEvals.get(m[1]) ?? [],
    version,
    agent.schema as { properties?: Record<string, unknown> }
  );
  const passed = results.filter((r) => r.passed).length;
  return ok({ total: results.length, passed, failed: results.length - passed, version, results });
});
route('POST', '/v1/agents/:name/runs/:rid/escalate', (m, body) => {
  // Don't open a case for a run that doesn't exist: 404 a missing agent or run id
  // before creating anything, mirroring how POST /v1/agents/:name/run 404s.
  const agent = state.agents.find((x) => x.name === m[1]);
  if (!agent) return notFound();
  const run = state.agentRuns.find((r) => r.run_id === m[2] && r.agent === m[1]);
  if (!run) return notFound();
  const caseId = nextId('case');
  const now = new Date().toISOString();
  const slaDays = Number(body.sla_days ?? 3);
  // Carry the escalated run's prompt + a short form of its output into the case
  // context (the flat fact grid the case detail renders), so an agent_review case is
  // self-explanatory — the reviewer sees what was asked and what came back, not an
  // empty stub. Mirrors how manual-review cases carry their decision context.
  const runOutput = run?.error
    ? `error: ${run.error}`
    : (run?.text ?? (run?.structured != null ? JSON.stringify(run.structured) : ''));
  const context: Record<string, unknown> = { agent: m[1], run_id: m[2] };
  if (run?.prompt) context.prompt = run.prompt;
  if (runOutput)
    context.output = runOutput.length > 280 ? runOutput.slice(0, 280) + '…' : runOutput;
  state.cases.unshift({
    case_id: caseId,
    company_name: String(body.company_name ?? 'Agent escalation'),
    case_type: String(body.case_type ?? 'agent_review'),
    status: 'needs_review',
    sla_days: slaDays,
    days_left: slaDays,
    sla_state: 'on_track',
    context,
    notes: [],
    audit: [
      {
        type: 'case.opened',
        actor: state.identity.actor,
        at: now,
        detail: `escalated from run ${m[2]}`
      }
    ],
    created_at: now,
    updated_at: now
  });
  pushAudit('cases.review_requested', 'cases', {
    case_id: caseId,
    from_agent: m[1],
    run_id: m[2]
  });
  return ok({ case_id: caseId });
});

// --- Models ---------------------------------------------------------------------
route('GET', '/v1/models', () => ok({ models: state.models }));
const MODEL_KINDS = ['logistic', 'gbm', 'expression', 'external'];
route('POST', '/v1/models', (_m, body) => {
  const name = String(body.name ?? '').trim();
  if (!name) return badRequest('a model name is required');
  const spec = body.spec as { kind?: string };
  const kind = spec?.kind ?? 'expression';
  // Reject an unknown kind up front rather than creating a model a Predict node can't run.
  if (!MODEL_KINDS.includes(kind))
    return badRequest(`unknown model kind "${kind}" — expected one of ${MODEL_KINDS.join(', ')}`);
  const existing = state.models.find((x) => x.name === name);
  const model: Model = {
    name,
    kind: kind as Model['kind'],
    spec,
    owner: state.identity.actor,
    updated_at: new Date().toISOString()
  };
  if (existing) Object.assign(existing, model);
  else state.models.push(model);
  return ok({});
});
// modelPredictionSeries is a model's REAL prediction values (probability 0..1), oldest
// first, over the recorded decisions of every flow that scores with it — derived by
// re-running the model over each recorded input, so drift is computed from real data.
function modelPredictionSeries(modelName: string): number[] {
  const model = state.models.find((mo) => mo.name === modelName);
  if (!model) return [];
  const flowIds = new Set<string>();
  for (const f of state.flows) {
    const { graph } = pickVersion(f, 'production');
    const scores = graph.nodes.some(
      (n) =>
        n.type === 'predict' && (n.config as { model?: string } | undefined)?.model === modelName
    );
    if (scores) flowIds.add(f.flow_id);
  }
  return state.decisions
    .filter((d) => flowIds.has(d.flow_id) && d.status === 'completed' && d.data)
    .slice()
    .sort((a, b) => String(a.started_at).localeCompare(String(b.started_at)))
    .map((d) => evaluateModel(model, d.data as Record<string, unknown>))
    .map((p) =>
      Number.isFinite(p.probability)
        ? (p.probability as number)
        : Math.max(0, Math.min(1, (p.score ?? 0) / 100))
    )
    .filter((v) => Number.isFinite(v));
}

// predHist buckets prediction probabilities (0..1) into 5 bins (Map-built to dodge the
// object-injection lint on indexed writes).
function predHist(values: number[]): number[] {
  const m = new Map([0, 1, 2, 3, 4].map((k) => [k, 0] as [number, number]));
  for (const v of values) {
    const i = Math.min(4, Math.max(0, Math.floor(v * 5)));
    m.set(i, (m.get(i) ?? 0) + 1);
  }
  return [0, 1, 2, 3, 4].map((k) => m.get(k) ?? 0);
}

const BASELINE_SPLIT = 0.55;
// computeModelDrift splits the model's real predictions into an older "baseline" window
// and a recent "current" window and returns the genuine PSI between them (vs the captured
// baseline if one exists) plus the current histogram.
function computeModelDrift(
  name: string
): { psi: number; hist: number[]; count: number } | undefined {
  const series = modelPredictionSeries(name);
  if (series.length === 0) return undefined;
  // Period-over-period: the model's OWN earlier predictions are the baseline, its recent
  // ones the current window — both real, so the PSI is a believable distribution shift
  // rather than a fixed seed histogram vs concentrated live data (which read as severe).
  const split = Math.max(1, Math.floor(series.length * BASELINE_SPLIT));
  const baseline = predHist(series.slice(0, split));
  const current = series.slice(split);
  const hist = predHist(current.length ? current : series.slice(split - 1));
  // Laplace-smooth both histograms (+1 per bin) before PSI so the small, concentrated
  // demo samples don't blow the index up to a meaningless double-digit value.
  const smooth = (h: number[]) => h.map((x) => x + 1);
  return {
    psi: psi(smooth(baseline), smooth(hist)),
    hist,
    count: current.length || series.length
  };
}

route('GET', '/v1/models/:name/drift', (m, _b, q) => {
  const model = state.models.find((x) => x.name === m[1]);
  if (!model) return notFound();
  const hasBaseline = state.modelBaselines.has(model.name);
  const drift = computeModelDrift(model.name);
  const hist = drift?.hist ?? state.modelBaselines.get(model.name) ?? [4, 6, 9, 5, 3];
  const threshold = state.modelMonitors.get(model.name);
  const psiVal = hasBaseline ? drift?.psi : undefined;
  const windowDays = Number((q.get('window') ?? '0').replace('d', '')) || 30;
  return ok({
    model: model.name,
    count: drift?.count ?? hist.reduce((a, b) => a + b, 0),
    hist,
    window_days: windowDays,
    has_baseline: hasBaseline,
    psi: psiVal,
    threshold,
    firing: psiVal !== undefined && threshold !== undefined ? psiVal > threshold : false,
    alerting: threshold !== undefined
  });
});
route('POST', '/v1/models/:name/monitor', (m, body) => {
  state.modelMonitors.set(m[1], Number(body.threshold ?? 0.2));
  return ok({});
});
route('POST', '/v1/models/:name/baseline', (m) => {
  // Snapshot the OLDER window of the model's real predictions as the reference baseline,
  // so drift afterwards compares the recent window against genuine historical data.
  const series = modelPredictionSeries(m[1]);
  const baselineWindow = series.slice(0, Math.floor(series.length * BASELINE_SPLIT));
  state.modelBaselines.set(m[1], predHist(baselineWindow.length ? baselineWindow : series));
  return ok({});
});

// --- Context layer --------------------------------------------------------------
route('GET', '/v1/context/connectors/catalog', () => ok({ templates: state.connectorCatalog }));
route('GET', '/v1/context/connectors', () => ok({ connectors: state.connectors }));
route('POST', '/v1/context/connectors', (_m, body) => {
  const name = String(body.name ?? '');
  const existing = state.connectors.find((c) => c.name === name);
  const connector = {
    name,
    type: String(body.type ?? 'http'),
    config: body.config,
    updated_at: new Date().toISOString()
  };
  if (existing) Object.assign(existing, connector);
  else state.connectors.push(connector);
  return ok({});
});
route('GET', '/v1/context/features', () => ok({ features: state.features }));
route('POST', '/v1/context/features', (_m, body) => {
  state.features.push({
    name: String(body.name ?? ''),
    entity_type: String(body.entity_type ?? ''),
    event_name: String(body.event_name ?? ''),
    aggregation: (body.aggregation as Feature_['aggregation']) ?? 'count',
    field: body.field ? String(body.field) : undefined,
    window_hours: Number(body.window_hours ?? 24),
    updated_at: new Date().toISOString()
  });
  return ok({});
});
route('GET', '/v1/context/entities', (_m, _b, q) => {
  const type = q.get('type');
  const entities = type ? state.entities.filter((e) => e.entity_type === type) : state.entities;
  return ok({ entities });
});
route('GET', '/v1/context/entities/:type/:id/events', (m) =>
  ok({ events: state.entityEvents.get(`${m[1]}/${m[2]}`) ?? [] })
);
route('GET', '/v1/context/entities/:type/:id/features', (m) => {
  const events = state.entityEvents.get(`${m[1]}/${m[2]}`) ?? [];
  const features = state.features
    .filter((f) => f.entity_type === m[1])
    .map((f) => {
      // Honour the feature's window — only events within window_hours of now count
      // (a 7d feature must exclude a 2-week-old event; it used to sum all of them).
      const cutoff = Date.now() - (f.window_hours ?? 0) * 3600 * 1000;
      const matched = events.filter(
        (e) =>
          e.event_name === f.event_name &&
          (!f.window_hours || new Date(e.occurred_at).getTime() >= cutoff)
      );
      const value =
        f.aggregation === 'count'
          ? matched.length
          : matched.reduce(
              (sum, e) =>
                sum + Number(Object.entries(e.data ?? {}).find(([k]) => k === f.field)?.[1] ?? 0),
              0
            );
      return { name: f.name, value };
    });
  return ok({ features });
});
route('GET', '/v1/context/entities/:type/:id', (m) => {
  const e = state.entities.find((x) => x.entity_type === m[1] && x.entity_id === m[2]);
  return e ? ok(e) : notFound();
});

// --- Policies -------------------------------------------------------------------
route('GET', '/v1/policies', () => ok({ policies: state.policies }));
route('POST', '/v1/policies', (_m, body) => {
  const policyId = nextId('pol');
  state.policies.push({
    policy_id: policyId,
    name: String(body.name ?? ''),
    flow_slug: String(body.flow_slug ?? ''),
    latest: 0,
    versions: [],
    updated_at: new Date().toISOString()
  });
  return ok({ policy_id: policyId });
});
route('POST', '/v1/policies/:id/versions', (m, body) => {
  const policy = state.policies.find((p) => p.policy_id === m[1]);
  if (!policy) return notFound();
  const version = policy.latest + 1;
  policy.versions.push({
    version,
    etag: `pe${version}`,
    spec: (body.spec as Policy['versions'][number]['spec']) ?? { rules: [] },
    published_at: new Date().toISOString(),
    published_by: state.identity.actor
  });
  policy.latest = version;
  policy.updated_at = new Date().toISOString();
  return ok({ version, etag: `pe${version}` });
});
route('POST', '/v1/policies/:id/backtest', (m, body) => {
  const policy = state.policies.find((p) => p.policy_id === m[1]);
  if (!policy) return notFound();
  return ok(policyBacktest(policy, body));
});

// --- Pre-approvals --------------------------------------------------------------
route('GET', '/v1/preapprovals', () => ok({ preapprovals: state.preapprovals }));
route('POST', '/v1/preapprovals', (_m, body) => {
  const preapprovalId = nextId('pa');
  const now = new Date().toISOString();
  state.preapprovals.unshift({
    preapproval_id: preapprovalId,
    entity_type: String(body.entity_type ?? 'applicant'),
    entity_id: String(body.entity_id ?? ''),
    disposition: (body.disposition as Disposition) ?? 'approve',
    terms: body.terms as Record<string, unknown> | undefined,
    flow_slug: body.flow_slug ? String(body.flow_slug) : undefined,
    valid_until: ahead(Number(body.valid_days ?? 30)),
    status: 'active',
    honored_count: 0,
    note: body.note ? String(body.note) : undefined,
    granted_at: now,
    granted_by: state.identity.actor,
    updated_at: now
  });
  return ok({ preapproval_id: preapprovalId });
});
route('POST', '/v1/preapprovals/:type/:id/revoke', (m, body) => {
  const pa = state.preapprovals.find(
    (p) =>
      p.entity_type === decodeURIComponent(m[1]) &&
      p.entity_id === decodeURIComponent(m[2]) &&
      p.status === 'active'
  );
  if (pa) {
    pa.status = 'revoked';
    pa.revoked_reason = String(body.reason ?? '');
    pa.updated_at = new Date().toISOString();
  }
  return ok({});
});

// --- Webhooks -------------------------------------------------------------------
route('GET', '/v1/webhooks', () => ok({ webhooks: state.webhooks }));
route('POST', '/v1/webhooks', (_m, body) => {
  const webhookId = nextId('wh');
  state.webhooks.push({
    webhook_id: webhookId,
    url: String(body.url ?? ''),
    note: body.note ? String(body.note) : undefined,
    template: body.template ? String(body.template) : undefined,
    events: Array.isArray(body.events) ? (body.events as string[]) : undefined,
    active: true,
    delivery_count: 0,
    last_ok: true,
    created_at: new Date().toISOString()
  });
  return ok({ webhook_id: webhookId });
});
route('DELETE', '/v1/webhooks/:id', (m) => {
  state.webhooks = state.webhooks.filter((w) => w.webhook_id !== m[1]);
  return ok({});
});

// --- Notifications --------------------------------------------------------------
route('GET', '/v1/notifications', () => ok({ notifications: state.notifications }));
route('POST', '/v1/notifications/:id/read', (m) => {
  const n = state.notifications.find((x) => x.notification_id === decodeURIComponent(m[1]));
  if (n) n.read = true;
  return ok({});
});

// --- Privacy --------------------------------------------------------------------
route('GET', '/v1/privacy', () => ok(state.privacy));
route('PUT', '/v1/privacy', (_m, body) => {
  state.privacy = {
    fields: Array.isArray(body.fields) ? (body.fields as string[]) : [],
    updated_at: new Date().toISOString(),
    updated_by: state.identity.actor
  };
  return ok({});
});

// --- API keys -------------------------------------------------------------------
route('GET', '/v1/api-keys', () => {
  if (!roleAtLeast('admin')) return forbidden('managing API keys requires the admin role');
  return ok({ api_keys: state.apiKeys });
});
route('POST', '/v1/api-keys', (_m, body) => {
  if (!roleAtLeast('admin')) return forbidden('managing API keys requires the admin role');
  const key: ManagedApiKey = {
    id: nextId('key'),
    name: String(body.name ?? ''),
    identity: { org: 'demo', workspace: 'main', actor: String(body.actor ?? state.identity.actor) },
    scope: (body.scope as Scope) ?? 'sandbox',
    role: (body.role as Role) ?? 'viewer',
    created_at: new Date().toISOString(),
    expires_at: body.expires_at ? String(body.expires_at) : undefined
  };
  state.apiKeys.push(key);
  pushAudit('auth.managed_key.created', 'auth', {
    key_id: key.id,
    name: key.name,
    role: key.role,
    scope: key.scope
  });
  return ok({ api_key: key, secret: `sk-demo-${Math.random().toString(36).slice(2, 18)}` });
});
route('POST', '/v1/api-keys/:id/rotate', (m, body) => {
  if (!roleAtLeast('admin')) return forbidden('managing API keys requires the admin role');
  const key = state.apiKeys.find((k) => k.id === decodeURIComponent(m[1]));
  if (!key) return notFound();
  const now = Date.now();
  key.rotated_at = new Date(now).toISOString();
  // Honor the rotation grace window the client asks for: the previous secret keeps
  // authenticating until this instant (the UI renders it as the "keeps working until …"
  // note). It used to be dropped, so the grace note never appeared.
  const grace = Number((body as { grace_seconds?: unknown }).grace_seconds) || 3600;
  key.prev_hash_expires_at = new Date(now + grace * 1000).toISOString();
  pushAudit('auth.managed_key.rotated', 'auth', { key_id: key.id, name: key.name });
  return ok({ api_key: key, secret: `sk-demo-${Math.random().toString(36).slice(2, 18)}` });
});
route('DELETE', '/v1/api-keys/:id', (m) => {
  if (!roleAtLeast('admin')) return forbidden('managing API keys requires the admin role');
  const key = state.apiKeys.find((k) => k.id === decodeURIComponent(m[1]));
  if (!key) return notFound();
  key.revoked_at = new Date().toISOString();
  pushAudit('auth.managed_key.revoked', 'auth', { key_id: key.id, name: key.name });
  return ok({ api_key: key });
});

// --- Comments -------------------------------------------------------------------
route('GET', '/v1/comments/:type/:id', (m) =>
  ok({ comments: state.comments.get(`${m[1]}/${decodeURIComponent(m[2])}`) ?? [] })
);
route('POST', '/v1/comments/:type/:id', (m, body) => {
  const key = `${m[1]}/${decodeURIComponent(m[2])}`;
  const list = state.comments.get(key) ?? [];
  const commentId = nextId('cmt');
  list.push({
    comment_id: commentId,
    subject_type: m[1],
    subject_id: decodeURIComponent(m[2]),
    body: String(body.body ?? ''),
    parent_id: body.parent_id ? String(body.parent_id) : undefined,
    author: state.identity.actor,
    at: new Date().toISOString()
  });
  state.comments.set(key, list);
  return ok({ comment_id: commentId });
});

// --- Audit ----------------------------------------------------------------------
route('GET', '/v1/audit', (_m, _b, q) => {
  if (!roleAtLeast('admin')) return forbidden('the audit log requires the admin role');
  // An unparseable time bound is a bad request (the real backend 400s it), not a
  // filter that silently matches nothing.
  for (const key of ['since', 'until']) {
    const v = q.get(key);
    if (v && Number.isNaN(Date.parse(v))) return badRequest(`invalid ${key} (want RFC3339)`);
  }
  const filtered = filterAudit(q);
  if (q.get('format') === 'csv') return text(auditCsv(filtered));
  const limit = Number(q.get('limit') ?? 0);
  const offset = Number(q.get('offset') ?? 0);
  const entries = limit ? filtered.slice(offset, offset + limit) : filtered;
  return ok({ entries, total: filtered.length, limit, offset });
});

// --- MRM ------------------------------------------------------------------------
route('GET', '/v1/mrm/report', (_m, _b, q) => {
  if (!roleAtLeast('admin')) return forbidden('the model-risk report requires the admin role');
  const report = mrmReport();
  const fmt = q.get('format');
  if (fmt === 'csv') return text(mrmCsv(report));
  if (fmt === 'md') return text(mrmMarkdown(report));
  return ok(report);
});

// --- Copilot --------------------------------------------------------------------
route('POST', '/v1/copilot/explain', (_m, body) => {
  // Describe the flow from the nodes it ACTUALLY has (the prior canned text claimed a
  // risk-band split + manual review even for flows that had neither).
  const graph = (body.graph as { nodes?: { type?: string; name?: string }[] }) ?? {};
  const nodes = graph.nodes ?? [];
  const has = (t: string) => nodes.some((n) => n.type === t);
  const steps: string[] = [];
  if (has('input')) steps.push('reads the input');
  if (has('predict')) steps.push('scores it with a predictive model');
  if (has('assignment')) steps.push('derives intermediate values');
  if (has('ai')) steps.push('calls an AI agent');
  if (has('split')) steps.push('branches on a condition');
  if (has('manual_review')) steps.push('routes some cases to a human reviewer');
  if (has('output')) steps.push('emits a decision');
  const kinds = [...new Set(nodes.map((n) => n.type))].filter(Boolean).join(', ');
  const flow = steps.length ? `It ${steps.join(', then ')}.` : 'It has no executable steps yet.';
  return ok({
    text: `This flow has ${nodes.length} node(s) (${kinds || 'none'}). ${flow}`
  });
});
route('POST', '/v1/copilot/suggest', (_m, body) => {
  // A keyword-shaped suggestion so different requirements get different advice.
  const prompt = String(body.prompt ?? '');
  const p = prompt.toLowerCase();
  const lines: string[] = [];
  if (/income|salary|afford/.test(p))
    lines.push('Add an assignment: dti = debt / income, then split on dti.');
  if (/fraud|velocity|device/.test(p))
    lines.push('Add a Predict node referencing a fraud model and branch on its probability.');
  if (/sanction|aml|watchlist|pep/.test(p))
    lines.push('Add a split that routes a sanctions/watchlist hit straight to manual review.');
  if (/review|manual|human|escalat/.test(p))
    lines.push('Add a manual_review node on the high-risk branch to open a case.');
  if (lines.length === 0)
    lines.push('Add a Predict node to score the input, then a split to branch on the score.');
  lines.push('Bind a policy: approve below the low band, decline above the high band, else refer.');
  return ok({ text: `Suggested logic for "${prompt}":\n- ${lines.join('\n- ')}` });
});
route('POST', '/v1/copilot/generate', (_m, body) => {
  // Shape the generated flow from the prompt's keywords, so different requests produce
  // different flows (it used to return one fixed credit graph regardless of input).
  const prompt = String(body.prompt ?? '').toLowerCase();
  // Pull an explicit numeric threshold out of the prompt ("under $50k", "below 100") so a
  // value/amount-keyed flow actually uses it instead of a fixed default.
  const numMatch = prompt.match(
    /(?:under|below|less than|over|above|<|>|\$)\s*\$?([\d,.]+)\s*(k|m)?/
  );
  let promptThreshold = numMatch ? Number(numMatch[1].replace(/,/g, '')) : undefined;
  if (promptThreshold != null && numMatch?.[2] === 'k') promptThreshold *= 1000;
  if (promptThreshold != null && numMatch?.[2] === 'm') promptThreshold *= 1_000_000;
  let d: { input: string; score: string; expr: string; threshold: number };
  if (/fraud|velocity|device|chargeback/.test(prompt))
    d = {
      input: 'Transaction',
      score: 'fraud_score',
      expr: 'velocity_24h * 8 + (new_device ? 30 : 0)',
      threshold: 60
    };
  else if (/sanction|aml|watchlist|pep|launder/.test(prompt))
    d = {
      input: 'Party',
      score: 'aml_score',
      expr: 'watchlist_score + (pep ? 40 : 0)',
      threshold: 50
    };
  else if (/kyc|identity|onboard|document/.test(prompt))
    d = { input: 'Applicant', score: 'kyc_risk', expr: '100 - identity_confidence', threshold: 40 };
  else if (/refund|dispute|chargeback|return|reimburse/.test(prompt))
    // A value-keyed flow: approve below the (prompt-supplied) amount, else route to review.
    d = {
      input: 'Dispute',
      score: 'dispute_amount',
      expr: 'amount',
      threshold: promptThreshold ?? 500
    };
  else if (/loan|lending|mortgage|affordability|credit|income|dti|underwrit/.test(prompt))
    d = { input: 'Application', score: 'risk', expr: 'debt / income * 100', threshold: 50 };
  // Truly generic prompt: a value/threshold flow rather than a credit-specific one.
  else d = { input: 'Request', score: 'value', expr: 'amount', threshold: promptThreshold ?? 100 };
  return ok({
    graph: {
      nodes: [
        { id: 'in', type: 'input', name: d.input },
        {
          id: 'assign',
          type: 'assignment',
          name: 'Score',
          config: { assignments: [{ target: d.score, expr: d.expr }] }
        },
        { id: 'gate', type: 'split', name: 'Band' },
        {
          id: 'review',
          type: 'manual_review',
          name: 'Reviewer',
          config: { case_type: 'generated_review', sla_days: 3 }
        },
        {
          id: 'out',
          type: 'output',
          name: 'Decision',
          config: { assignments: [{ target: 'approved', expr: `${d.score} < ${d.threshold}` }] }
        }
      ],
      edges: [
        { from: 'in', to: 'assign' },
        { from: 'assign', to: 'gate' },
        { from: 'gate', to: 'out', branch: `${d.score} < ${d.threshold}` },
        // Default (unbranched) catch-all: anything not clearly below the threshold —
        // including a missing/partial input on the first test run — routes to review and
        // COMPLETES, rather than failing "no branch matched".
        { from: 'gate', to: 'review' },
        { from: 'review', to: 'out' }
      ]
    }
  });
});

// --- Decision intelligence: heatmap node-stats, counterfactual, coverage ---------
// The static demo computes these locally by re-running the pure demo engine, mirroring
// the real GET /v1/flows/{id}/node-stats, POST /v1/decisions/{id}/counterfactual, and
// POST /v1/flows/{id}/coverage endpoints.
route('GET', '/v1/flows/:id/node-stats', (m) => {
  const flow = findFlow(m[1]);
  if (!flow) return notFound();
  return ok(nodeStats(flow, state.decisions));
});
route('POST', '/v1/decisions/:id/counterfactual', (m) => {
  const d = state.decisions.find((x) => x.decision_id === m[1]);
  if (!d) return notFound();
  const flow = findFlow(d.flow_id);
  if (!flow) return notFound();
  return ok(counterfactual(flow, d));
});
route('POST', '/v1/flows/:id/coverage', (m, body) => {
  const flow = findFlow(m[1]);
  if (!flow) return notFound();
  const { graph } = pickVersion(flow, 'production');
  const runs = Number((body as { runs?: unknown }).runs) || 200;
  return ok(coverage(flow, graph, runs));
});

// Helper type alias for the Feature aggregation field (kept local to avoid an
// import clash; mirrors the api.ts Feature interface).
interface Feature_ {
  aggregation: 'count' | 'sum';
}

// --- handleDemo entry point -----------------------------------------------------

// handleDemo dispatches a parsed request to the first matching route. Returns a
// fallback default (never null) so the install layer always has a shape to serve.
export function handleDemo(method: string, path: string, query: Query, body: Body): DemoResponse {
  for (const r of routes) {
    if (r.method !== method) continue;
    const m = path.match(r.re);
    if (m) return r.fn(m, body, query);
  }
  // An unmatched /v1 route is a demo COVERAGE BUG, not a state to paper over: a
  // silent {} here previously made missing endpoints render as empty-but-working
  // pages. Fail exactly like the real mux so the gap is visible and testable.
  return { status: 404, body: { error: `demo backend has no route for ${method} ${path}` } };
}

// --- Computation helpers --------------------------------------------------------

function setDeployment(
  flow: Flow,
  env: Environment,
  version: number,
  challengerVersion?: number,
  challengerPct?: number
): void {
  const m = new Map(Object.entries(flow.deployments ?? {}));
  const current = m.get(env);
  // Remember what was live so a rollback restores the ACTUAL prior version, not a
  // naive latest-1 (which is wrong whenever live ≠ latest).
  const previous =
    current && current.version !== version ? current.version : current?.previous_version;
  m.set(env, {
    version,
    challenger_version: challengerVersion,
    challenger_pct: challengerPct,
    previous_version: previous
  });
  flow.deployments = Object.fromEntries(m);
}

function addRequest(
  flow: Flow,
  environment: string,
  version: number,
  challengerVersion?: number,
  challengerPct?: number
): void {
  const reqs = flow.deployment_requests ?? [];
  reqs.push({
    request_id: nextId('req'),
    environment: environment as Environment,
    version,
    challenger_version: challengerVersion,
    challenger_pct: challengerPct,
    status: 'pending',
    requested_by: state.identity.actor,
    requested_at: new Date().toISOString()
  });
  flow.deployment_requests = reqs;
  pushAudit('decision.flow.deployment_requested', 'decision.flows', {
    flow_id: flow.flow_id,
    environment,
    version
  });
}

function flowMetrics(flow: Flow) {
  const decisions = state.decisions.filter((d) => d.flow_id === flow.flow_id);
  const completed = decisions.filter((d) => d.status === 'completed').length;
  const failed = decisions.filter((d) => d.status === 'failed').length;
  const totalDuration = decisions.reduce((a, d) => a + (d.duration_ms ?? 0), 0);
  const byEnvironment: Record<string, number> = {};
  const byVersion: Record<string, number> = {};
  const byVariant: Record<string, { started: number; completed: number; failed: number }> = {};
  const byDisposition: Record<string, number> = {};
  for (const d of decisions) {
    const envMap = new Map(Object.entries(byEnvironment));
    envMap.set(d.environment, (envMap.get(d.environment) ?? 0) + 1);
    Object.assign(byEnvironment, Object.fromEntries(envMap));
    const verMap = new Map(Object.entries(byVersion));
    verMap.set(String(d.version), (verMap.get(String(d.version)) ?? 0) + 1);
    Object.assign(byVersion, Object.fromEntries(verMap));
    const variant = d.variant ?? 'champion';
    const vm = new Map(Object.entries(byVariant));
    const cur = vm.get(variant) ?? { started: 0, completed: 0, failed: 0 };
    cur.started += 1;
    // A suspended decision is neither a completion nor a failure — only a genuinely
    // failed run counts as failed (the real by_variant semantics).
    if (d.status === 'completed') cur.completed += 1;
    else if (d.status === 'failed') cur.failed += 1;
    vm.set(variant, cur);
    Object.assign(byVariant, Object.fromEntries(vm));
    if (d.disposition) {
      const dm = new Map(Object.entries(byDisposition));
      dm.set(d.disposition, (dm.get(d.disposition) ?? 0) + 1);
      Object.assign(byDisposition, Object.fromEntries(dm));
    }
  }
  return {
    flow_id: flow.flow_id,
    total: decisions.length,
    completed,
    failed,
    total_duration_ms: totalDuration,
    avg_duration_ms: decisions.length ? Math.round(totalDuration / decisions.length) : 0,
    by_environment: byEnvironment,
    by_version: byVersion,
    by_variant: byVariant,
    by_disposition: byDisposition
  };
}

function filterDecisions(q: Query) {
  let list: Decision[] = state.decisions;
  const flow = q.get('flow');
  const env = q.get('env');
  const status = q.get('status');
  const variant = q.get('variant');
  const search = q.get('q');
  if (flow) list = list.filter((d) => d.slug === flow || d.flow_id === flow);
  if (env) list = list.filter((d) => d.environment === env);
  if (status) list = list.filter((d) => d.status === status);
  if (variant) list = list.filter((d) => d.variant === variant);
  if (search) list = list.filter((d) => d.decision_id.includes(search));
  const total = list.length;
  const limit = Number(q.get('limit') ?? 0);
  const offset = Number(q.get('offset') ?? 0);
  const decisions = limit ? list.slice(offset, offset + limit) : list;
  return { decisions, total, limit, offset };
}

function filterCases(q: Query): Case[] {
  let list = state.cases;
  const status = q.get('status');
  const type = q.get('type');
  const assignee = q.get('assignee');
  if (status) list = list.filter((c) => c.status === status);
  if (type) list = list.filter((c) => c.case_type === type);
  if (assignee) list = list.filter((c) => c.assignee === assignee);
  return list;
}

function caseSummary(q: Query) {
  const list = filterCases(q);
  const byStatus: Record<string, number> = {};
  for (const c of list) {
    const m = new Map(Object.entries(byStatus));
    m.set(c.status, (m.get(c.status) ?? 0) + 1);
    Object.assign(byStatus, Object.fromEntries(m));
  }
  return {
    total: list.length,
    by_status: byStatus,
    unassigned: list.filter((c) => !c.assignee).length,
    due_soon: list.filter((c) => c.sla_state === 'due_soon').length,
    overdue: list.filter((c) => c.sla_state === 'overdue').length
  };
}

// The demo plays a deployment WITH INTRAKTIBLE_AI_PRICES configured: USD per million
// tokens (input/output), applied exactly like the real Pricing.Cost — a model without
// a price still reports usage but gets no cost row.
const AI_PRICES = new Map<string, { inputPerMTok: number; outputPerMTok: number }>([
  ['claude-sonnet', { inputPerMTok: 3, outputPerMTok: 15 }],
  ['claude-haiku', { inputPerMTok: 0.8, outputPerMTok: 4 }]
]);

function runSummary() {
  const runs = state.agentRuns;
  const byAgent: Record<string, number> = {};
  const byModel: Record<
    string,
    { runs: number; prompt_tokens: number; completion_tokens: number }
  > = {};
  let promptTokens = 0;
  let completionTokens = 0;
  for (const r of runs) {
    const pt = Math.max(8, r.prompt.length);
    const ct = Math.max(6, (r.text ?? '').length);
    promptTokens += pt;
    completionTokens += ct;
    const am = new Map(Object.entries(byAgent));
    am.set(r.agent, (am.get(r.agent) ?? 0) + 1);
    Object.assign(byAgent, Object.fromEntries(am));
    const model = r.model ?? 'unknown';
    const mm = new Map(Object.entries(byModel));
    const cur = mm.get(model) ?? { runs: 0, prompt_tokens: 0, completion_tokens: 0 };
    cur.runs += 1;
    cur.prompt_tokens += pt;
    cur.completion_tokens += ct;
    mm.set(model, cur);
    Object.assign(byModel, Object.fromEntries(mm));
  }
  const costByModel: Record<string, number> = {};
  let totalCost = 0;
  for (const [model, usage] of Object.entries(byModel)) {
    const price = AI_PRICES.get(model);
    if (!price) continue;
    const cost =
      (usage.prompt_tokens / 1e6) * price.inputPerMTok +
      (usage.completion_tokens / 1e6) * price.outputPerMTok;
    const cm = new Map(Object.entries(costByModel));
    cm.set(model, cost);
    Object.assign(costByModel, Object.fromEntries(cm));
    totalCost += cost;
  }
  return {
    total: runs.length,
    completed: runs.filter((r) => r.status === 'completed').length,
    failed: runs.filter((r) => r.status === 'failed').length,
    by_agent: byAgent,
    prompt_tokens: promptTokens,
    completion_tokens: completionTokens,
    by_model: byModel,
    priced: true,
    total_cost_usd: totalCost,
    cost_by_model: costByModel
  };
}

type PolicySpec = Policy['versions'][number]['spec'];
type Dist = { approve: number; decline: number; refer: number; failed: number };
function bumpDist(dist: Dist, d: string): void {
  if (d === 'approve') dist.approve += 1;
  else if (d === 'decline') dist.decline += 1;
  else if (d === 'refer') dist.refer += 1;
  else dist.failed += 1;
}
function policyBacktest(policy: Policy, body: Body) {
  const dataset = (body.dataset as Body[]) ?? [];
  const specOf = (v?: number) => policy.versions.find((x) => x.version === v)?.spec;
  const spec = (body.spec as PolicySpec) ??
    specOf(policy.latest) ??
    policy.versions.at(-1)?.spec ?? { rules: [] };
  const disposeWith = (s: PolicySpec, row: Body): string => {
    for (const rule of s.rules) {
      if (evalExpr(rule.when, row)) return rule.disposition;
    }
    return (s.default as string) ?? 'refer';
  };
  const evaluated: Dist = { approve: 0, decline: 0, refer: 0, failed: 0 };
  // When a compare_version is given, evaluate the same dataset through THAT published
  // version too and report the distribution shift + the rows whose disposition flipped.
  const compareSpec = specOf(body.compare_version as number | undefined);
  const compare: Dist | undefined = compareSpec
    ? { approve: 0, decline: 0, refer: 0, failed: 0 }
    : undefined;
  const flips: { index: number; evaluated: string; compare: string }[] = [];
  let flipped = 0;
  dataset.forEach((row, i) => {
    const d = disposeWith(spec, row);
    bumpDist(evaluated, d);
    if (compareSpec && compare) {
      const c = disposeWith(compareSpec, row);
      bumpDist(compare, c);
      if (c !== d) {
        flipped += 1;
        if (flips.length < 100) flips.push({ index: i, evaluated: d, compare: c });
      }
    }
  });
  return compareSpec
    ? { summary: { total: dataset.length, evaluated, compare, flipped }, flips }
    : { summary: { total: dataset.length, evaluated } };
}

// payloadReferences reports whether the payload references id as any JSON string
// value (a flow_id / case_id / decision_id / …) — the real backend's generic way to
// scope the trail to one resource without knowing each event type's schema.
function payloadReferences(v: unknown, id: string): boolean {
  if (typeof v === 'string') return v === id;
  if (Array.isArray(v)) return v.some((e) => payloadReferences(e, id));
  if (v && typeof v === 'object')
    return Object.values(v as Record<string, unknown>).some((e) => payloadReferences(e, id));
  return false;
}

function filterAudit(q: Query): AuditEntry[] {
  let list = state.audit;
  const stream = q.get('stream');
  const actor = q.get('actor');
  const type = q.get('type');
  const excludeType = q.get('exclude_type');
  const resource = q.get('resource');
  const since = q.get('since');
  const until = q.get('until');
  if (stream) list = list.filter((e) => e.stream === stream);
  if (actor) list = list.filter((e) => e.actor === actor);
  if (type) list = list.filter((e) => e.type === type);
  if (excludeType) list = list.filter((e) => e.type !== excludeType);
  // Streams are keyed by name and resource ids live in the payload (the real
  // backend's model), so the resource filter matches payload string values.
  if (resource) list = list.filter((e) => payloadReferences(e.payload, resource));
  // Inclusive RFC3339 time bounds, like the real backend's Since/Until (validated
  // at the route boundary, so Date.parse here is always finite).
  if (since) list = list.filter((e) => Date.parse(e.time) >= Date.parse(since));
  if (until) list = list.filter((e) => Date.parse(e.time) <= Date.parse(until));
  return list;
}

function csvCell(v: string): string {
  return /[",\n]/.test(v) ? `"${v.replace(/"/g, '""')}"` : v;
}
function auditCsv(entries: AuditEntry[]): string {
  // Include the per-row details/payload (the table shows it but the export used to drop
  // it), JSON-encoded into one properly-escaped column.
  const rows = entries.map((e) =>
    [e.seq, e.time, e.actor, e.stream, e.type, e.payload != null ? JSON.stringify(e.payload) : '']
      .map((c) => csvCell(String(c)))
      .join(',')
  );
  return ['seq,time,actor,stream,type,details', ...rows].join('\n');
}

function mrmReport() {
  const models = [
    ...state.flows.map((f) => mrmFlow(f)),
    ...state.models.map((m) => mrmModel(m)),
    ...state.agents.map((a) => mrmAgent(a))
  ];
  const byKind: Record<string, number> = {};
  for (const m of models) {
    const mm = new Map(Object.entries(byKind));
    mm.set(m.kind, (mm.get(m.kind) ?? 0) + 1);
    Object.assign(byKind, Object.fromEntries(mm));
  }
  return {
    generated_at: new Date().toISOString(),
    org: 'demo',
    workspace: 'main',
    summary: {
      total: models.length,
      by_kind: byKind,
      deployed: models.filter((m) => m.deployments && Object.keys(m.deployments).length > 0).length,
      unvalidated: models.filter((m) => m.validation.coverage === 'none').length,
      with_issues: models.filter((m) => (m.issues?.length ?? 0) > 0).length
    },
    models
  };
}

function mrmFlow(f: Flow): MrmModel {
  const metrics = flowMetrics(f);
  const assertions = state.assertions.get(f.flow_id) ?? [];
  const monitors = (state.monitors.get(f.flow_id) ?? [])
    .filter((m) => m.status.firing)
    .map((m) => m.metric);
  const deployments: Record<string, number> = {};
  for (const [env, dep] of Object.entries(f.deployments ?? {})) {
    const m = new Map(Object.entries(deployments));
    m.set(env, dep.version);
    Object.assign(deployments, Object.fromEntries(m));
  }
  // Reuse the same SLO attainment the /slo endpoint computes, so the MRM health column
  // can't disagree with the observability page (no objective set ⇒ nothing to breach).
  const slo = state.flowSlos.get(f.flow_id);
  const successRate = metrics.total ? metrics.completed / metrics.total : 1;
  const sloMet = slo
    ? successRate >= slo.success_target &&
      (slo.latency_target_ms === 0 || metrics.avg_duration_ms <= slo.latency_target_ms)
    : true;
  const issues: string[] = [];
  if (assertions.length === 0) issues.push('No assertions defined');
  if (monitors.length > 0) issues.push(`${monitors.length} monitor(s) firing`);
  if (slo && !sloMet) issues.push('SLO not met');
  return {
    kind: 'flow' as const,
    id: f.flow_id,
    name: f.name,
    version: f.latest,
    owner: 'risk@intraktible.dev',
    deployments,
    validation: {
      coverage: (assertions.length ? 'tested' : 'none') as 'tested' | 'failing' | 'none',
      has_assertions: assertions.length > 0,
      assertions_total: assertions.length,
      assertions_passed: assertions.length
    },
    monitoring: {
      decisions: metrics.total,
      success_rate: metrics.total
        ? Math.round((metrics.completed / metrics.total) * 1000) / 1000
        : 1,
      firing_monitors: monitors,
      slo_met: sloMet
    },
    issues: issues.length ? issues : undefined,
    updated_at: f.versions.at(-1)?.published_at ?? new Date().toISOString()
  };
}

function mrmModel(m: Model): MrmModel {
  const hasBaseline = state.modelBaselines.has(m.name);
  const drift = computeModelDrift(m.name);
  const threshold = state.modelMonitors.get(m.name);
  return {
    kind: 'predictive_model' as const,
    id: m.name,
    name: m.name,
    version: 1,
    owner: m.owner,
    validation: {
      coverage: (hasBaseline ? 'tested' : 'none') as 'tested' | 'failing' | 'none',
      has_baseline: hasBaseline
    },
    monitoring: {
      decisions: 0,
      success_rate: 1,
      drift_psi: drift?.psi,
      drift_firing: drift !== undefined && threshold !== undefined ? drift.psi > threshold : false
    },
    issues: hasBaseline ? undefined : ['No drift baseline captured'],
    updated_at: m.updated_at
  };
}

function mrmAgent(a: Agent): MrmModel {
  const evals = state.agentEvals.get(a.name) ?? [];
  return {
    kind: 'agent' as const,
    id: a.name,
    name: a.name,
    version: a.latest ?? 1,
    validation: {
      coverage: (evals.length ? 'tested' : 'none') as 'tested' | 'failing' | 'none',
      has_eval_cases: evals.length > 0,
      eval_cases: evals.length
    },
    monitoring: { decisions: a.runs, success_rate: 1 },
    issues: evals.length ? undefined : ['No eval cases defined'],
    updated_at: a.updated_at
  };
}

type MrmReportT = ReturnType<typeof mrmReport>;

function mrmCsv(report: MrmReportT): string {
  const rows = report.models.map(
    (m) =>
      `${m.kind},${m.id},${m.name},${m.version},${m.validation.coverage},${(m.issues ?? []).length}`
  );
  return ['kind,id,name,version,coverage,issues', ...rows].join('\n');
}

function mrmMarkdown(report: MrmReportT): string {
  const lines = [
    `# Model Risk Report — ${report.org}/${report.workspace}`,
    '',
    `Generated ${report.generated_at}`,
    '',
    `Total models: ${report.summary.total}`,
    '',
    '| Kind | Name | Version | Coverage | Issues |',
    '| --- | --- | --- | --- | --- |'
  ];
  for (const m of report.models) {
    lines.push(
      `| ${m.kind} | ${m.name} | v${m.version} | ${m.validation.coverage} | ${(m.issues ?? []).join('; ') || '—'} |`
    );
  }
  return lines.join('\n');
}

// --- Export renderers -----------------------------------------------------------

function exportGraph(flow: Flow, format: string): string {
  const graph = flow.versions.find((v) => v.version === flow.latest)?.graph ??
    flow.versions.at(-1)?.graph ?? { nodes: [], edges: [] };
  if (format === 'json')
    return JSON.stringify(
      { slug: flow.slug, graph, input_schema: flow.versions.at(-1)?.input_schema },
      null,
      2
    );
  if (format === 'dot') {
    const edges = graph.edges
      .map((e) => `  "${e.from}" -> "${e.to}"${e.branch ? ` [label="${e.branch}"]` : ''};`)
      .join('\n');
    return `digraph "${flow.slug}" {\n${edges}\n}`;
  }
  if (format === 'bpmn') {
    return `<?xml version="1.0"?>\n<definitions><process id="${flow.slug}">${graph.nodes.map((n) => `<task id="${n.id}" name="${n.name ?? n.id}"/>`).join('')}</process></definitions>`;
  }
  // mermaid / mermaid-state
  const header = format === 'mermaid-state' ? 'stateDiagram-v2' : 'flowchart TD';
  const edges = graph.edges
    .map((e) => `  ${e.from} -->${e.branch ? `|${e.branch}|` : ''} ${e.to}`)
    .join('\n');
  return `${header}\n${edges}`;
}

function exportDecisionTrace(d: Decision, format: string): string {
  if (format === 'json') return JSON.stringify(d, null, 2);
  if (format === 'dot') {
    const nodes = (d.nodes ?? []).map((n) => `  "${n.node_id}" [label="${n.type}"];`).join('\n');
    return `digraph "${d.decision_id}" {\n${nodes}\n}`;
  }
  const steps = (d.nodes ?? []).map((n) => `  Engine->>${n.node_id}: ${n.type}`).join('\n');
  return `sequenceDiagram\n  participant Engine\n${steps}`;
}
