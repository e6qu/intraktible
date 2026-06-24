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
import { state, nextId, driftReportFor, modelDrift, ahead } from './store';
import {
  decideFlow,
  runAssertionsFor,
  scoreEvalCases,
  backtestFlowDataset,
  runFlow,
  evalExpr,
  pickVersion
} from './engine';
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

function pushAudit(type: string, stream: string, payload?: unknown): void {
  state.seq += 1;
  state.audit.unshift({
    seq: state.seq,
    id: `aud_${state.seq}`,
    time: new Date().toISOString(),
    actor: state.identity.actor,
    stream,
    type,
    payload
  });
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
route('POST', '/v1/login', () => ok(state.identity));
route('GET', '/v1/me', () => ok(state.identity));
route('POST', '/v1/logout', () => ok({}));
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
  pushAudit('flow.created', flowId, { slug, name });
  return ok({ flow_id: flowId });
});
route('POST', '/v1/flows/import', (_m, body) => {
  const slug = String((body as Body).slug ?? '').trim();
  if (!slug) return badRequest('slug is required');
  if (state.flows.some((f) => f.slug === slug))
    return badRequest(`a flow with slug "${slug}" already exists`);
  const name = String((body as Body).name ?? slug);
  const graph = ((body as Body).graph as FlowGraph | undefined) ?? {
    nodes: [{ id: 'in', type: 'input', name: 'Input' }],
    edges: []
  };
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
        published_at: new Date().toISOString(),
        published_by: state.identity.actor
      }
    ],
    deployments: {}
  });
  pushAudit('flow.created', flowId, { slug, name, imported: true });
  return ok({ flow_id: flowId, slug, version: 1, etag: 'e1', created: true, published: true });
});
route('POST', '/v1/flows/import-bundle', (_m, body) => {
  // Import each flow in the bundle that doesn't already exist (real, not a no-op).
  const flows = Array.isArray((body as Body).flows) ? ((body as Body).flows as Body[]) : [];
  let published = 0;
  let unchanged = 0;
  for (const f of flows) {
    const slug = String(f.slug ?? '').trim();
    if (!slug || state.flows.some((x) => x.slug === slug)) {
      unchanged += 1;
      continue;
    }
    const flowId = nextId('flow');
    state.flows.push({
      flow_id: flowId,
      slug,
      name: String(f.name ?? slug),
      latest: 1,
      versions: [
        {
          version: 1,
          etag: 'e1',
          graph: (f.graph as FlowGraph | undefined) ?? { nodes: [], edges: [] },
          published_at: new Date().toISOString(),
          published_by: state.identity.actor
        }
      ],
      deployments: {}
    });
    pushAudit('flow.created', flowId, { slug, imported: true });
    published += 1;
  }
  return ok({ results: [], published, failed: 0, unchanged });
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
  const version = flow.latest + 1;
  const v: FlowVersion = {
    version,
    etag: `e${version}`,
    graph: (body.graph as FlowVersion['graph']) ?? { nodes: [], edges: [] },
    input_schema: body.input_schema,
    published_at: new Date().toISOString(),
    published_by: state.identity.actor
  };
  flow.versions.push(v);
  flow.latest = version;
  pushAudit('flow.published', flow.flow_id, { version });
  return ok({ version, etag: v.etag });
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
  state.flowSlos.set(flow.flow_id, {
    success_target: Number(body.success_target ?? 0.95),
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
  const dist: Record<string, number> = { approve: 0, decline: 0, refer: 0 };
  for (const d of state.decisions) {
    if (d.flow_id === flow.flow_id && d.disposition) {
      const cur = Object.entries(dist).find(([k]) => k === d.disposition)?.[1] ?? 0;
      const mp = new Map(Object.entries(dist));
      mp.set(d.disposition, cur + 1);
      Object.assign(dist, Object.fromEntries(mp));
    }
  }
  state.flowBaselines.set(flow.flow_id, dist);
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
  pushAudit('deployment.created', flow.flow_id, { environment: env, version: body.version });
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
  pushAudit('deployment.rolledback', flow.flow_id, { environment: env, version: prev });
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
  pushAudit('deployment.approved', flow.flow_id, { version: req.version });
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
  pushAudit('deployment.rejected', flow.flow_id, { version: req.version });
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
  const decision: Decision = {
    decision_id: decisionId,
    flow_id: flow.flow_id,
    slug: flow.slug,
    version: pickVersion(flow, env).version,
    environment: env,
    variant: 'champion',
    status: 'completed',
    data,
    output: data,
    reason_codes: [
      { code: 'PRE_APPROVED', description: `Served from pre-approval ${grant.preapproval_id}` }
    ],
    disposition: grant.disposition,
    preapproval_id: grant.preapproval_id,
    nodes: [],
    started_at: now,
    ended_at: now,
    duration_ms: 1
  };
  state.decisions.unshift(decision);
  pushAudit('decision.created', flow.flow_id, {
    environment: env,
    decision_id: decisionId,
    status: 'completed',
    disposition: grant.disposition,
    preapproval_id: grant.preapproval_id
  });
  return ok({ decision_id: decisionId, status: 'completed', data, disposition: grant.disposition });
}
route('POST', '/v1/flows/:slug/:env/decide', (m, body) => {
  const flow = findFlow(m[1]);
  if (!flow) return notFound();
  if (!isEnvironment(m[2])) return badRequest(`unknown environment "${m[2]}"`);
  const data = (body.data as Body) ?? {};
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
  const { result } = decideFlow(flow, m[2] as Environment, data);
  // Surface the run in the global audit trail so the build→decide→case→resolve
  // journey actually shows up on the Audit page.
  pushAudit('decision.created', flow.flow_id, {
    environment: m[2],
    decision_id: result.decision_id,
    status: result.status,
    disposition: result.disposition
  });
  return ok(result);
});
route('POST', '/v1/flows/:slug/:env/decide/batch', (m, body) => {
  const flow = findFlow(m[1]);
  if (!flow) return notFound();
  if (!isEnvironment(m[2])) return badRequest(`unknown environment "${m[2]}"`);
  const dataset = (body.dataset as Body[]) ?? [];
  let completed = 0;
  let failed = 0;
  const results = dataset.map((row, index) => {
    const { result } = decideFlow(flow, m[2] as Environment, row);
    if (result.status === 'completed') completed += 1;
    else failed += 1;
    return {
      index,
      decision_id: result.decision_id,
      status: result.status,
      data: result.data,
      disposition: result.disposition,
      error: result.error
    };
  });
  return ok({ total: dataset.length, completed, failed, rejected: 0, results });
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
  const results = dataset.map((row, index) => {
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
      granted: false
    };
  });
  return ok({ total: dataset.length, granted, skipped, failed, rejected: 0, results });
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

// --- Cases ----------------------------------------------------------------------
route('GET', '/v1/cases/summary', (_m, _b, q) => ok(caseSummary(q)));
route('POST', '/v1/cases/sla-sweep', () =>
  ok({
    count: state.cases.filter((c) => c.sla_state === 'overdue' && c.status !== 'completed').length
  })
);
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
  pushAudit('case.opened', caseId, { case_type: body.case_type });
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
  pushAudit('case.assigned', c.case_id, { assignee: c.assignee });
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
  pushAudit('case.status', c.case_id, { status: c.status });
  return ok({});
});
route('POST', '/v1/cases/:id/notes', (m, body) => {
  const c = state.cases.find((x) => x.case_id === m[1]);
  if (!c) return notFound();
  const at = new Date().toISOString();
  c.notes.push({ author: state.identity.actor, text: String(body.text ?? ''), at });
  c.audit.push({ type: 'case.note', actor: state.identity.actor, at });
  c.updated_at = at;
  pushAudit('case.note', c.case_id, {});
  return ok({});
});

// --- Agents ---------------------------------------------------------------------
route('GET', '/v1/agents', () => ok({ agents: state.agents }));
route('POST', '/v1/agents', (_m, body) => {
  const name = String(body.name ?? '');
  const existing = state.agents.find((a) => a.name === name);
  const agent: Agent = {
    name,
    provider: body.provider ? String(body.provider) : undefined,
    model: body.model ? String(body.model) : undefined,
    system: body.system ? String(body.system) : undefined,
    schema: body.schema,
    tools: Array.isArray(body.tools) ? (body.tools as string[]) : [],
    latest: existing?.latest ?? 1,
    runs: existing?.runs ?? 0,
    updated_at: new Date().toISOString()
  };
  if (existing) Object.assign(existing, agent);
  else state.agents.push(agent);
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
  pushAudit('case.opened', caseId, { from_agent: m[1], run: m[2] });
  return ok({ case_id: caseId });
});

// --- Models ---------------------------------------------------------------------
route('GET', '/v1/models', () => ok({ models: state.models }));
route('POST', '/v1/models', (_m, body) => {
  const name = String(body.name ?? '');
  const spec = body.spec as { kind?: string };
  const existing = state.models.find((x) => x.name === name);
  const model: Model = {
    name,
    kind: (spec?.kind as Model['kind']) ?? 'expression',
    spec,
    owner: state.identity.actor,
    updated_at: new Date().toISOString()
  };
  if (existing) Object.assign(existing, model);
  else state.models.push(model);
  return ok({});
});
route('GET', '/v1/models/:name/drift', (m, _b, q) => {
  const model = state.models.find((x) => x.name === m[1]);
  if (!model) return notFound();
  const hist = state.modelBaselines.get(model.name) ?? [4, 6, 9, 5, 3];
  const threshold = state.modelMonitors.get(model.name);
  const hasBaseline = state.modelBaselines.has(model.name);
  const psi = modelDrift(model.name)?.psi;
  const windowDays = Number((q.get('window') ?? '0').replace('d', '')) || 30;
  return ok({
    model: model.name,
    count: hist.reduce((a, b) => a + b, 0),
    hist,
    window_days: windowDays,
    has_baseline: hasBaseline,
    psi,
    threshold,
    firing: psi !== undefined && threshold !== undefined ? psi > threshold : false,
    alerting: threshold !== undefined
  });
});
route('POST', '/v1/models/:name/monitor', (m, body) => {
  state.modelMonitors.set(m[1], Number(body.threshold ?? 0.2));
  return ok({});
});
route('POST', '/v1/models/:name/baseline', (m) => {
  state.modelBaselines.set(m[1], [4, 7, 9, 6, 3]);
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
  pushAudit('apikey.created', key.id, { name: key.name });
  return ok({ api_key: key, secret: `sk-demo-${Math.random().toString(36).slice(2, 18)}` });
});
route('POST', '/v1/api-keys/:id/rotate', (m) => {
  if (!roleAtLeast('admin')) return forbidden('managing API keys requires the admin role');
  const key = state.apiKeys.find((k) => k.id === decodeURIComponent(m[1]));
  if (!key) return notFound();
  key.rotated_at = new Date().toISOString();
  return ok({ api_key: key, secret: `sk-demo-${Math.random().toString(36).slice(2, 18)}` });
});
route('DELETE', '/v1/api-keys/:id', (m) => {
  if (!roleAtLeast('admin')) return forbidden('managing API keys requires the admin role');
  const key = state.apiKeys.find((k) => k.id === decodeURIComponent(m[1]));
  if (!key) return notFound();
  key.revoked_at = new Date().toISOString();
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
  let d: { input: string; score: string; expr: string; threshold: number };
  if (/fraud|velocity|device|chargeback/.test(prompt))
    d = {
      input: 'Transaction',
      score: 'fraud_score',
      expr: 'velocity_24h * 8 + (new_device ? 30 : 0)',
      threshold: 60
    };
  else if (/sanction|aml|watchlist|pep/.test(prompt))
    d = {
      input: 'Party',
      score: 'aml_score',
      expr: 'watchlist_score + (pep ? 40 : 0)',
      threshold: 50
    };
  else if (/kyc|identity|onboard|document/.test(prompt))
    d = { input: 'Applicant', score: 'kyc_risk', expr: '100 - identity_confidence', threshold: 40 };
  else d = { input: 'Application', score: 'risk', expr: 'debt / income * 100', threshold: 50 };
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
        { from: 'gate', to: 'review', branch: `${d.score} >= ${d.threshold}` },
        { from: 'review', to: 'out' }
      ]
    }
  });
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
  return defaultFor(path);
}

// defaultFor returns a safe, caller-shaped default for an unmatched route so no
// page hard-errors. Lists return their wrapped empty array; everything else {}.
function defaultFor(path: string): DemoResponse {
  const map = new Map<string, unknown>([
    ['/v1/flows', { flows: [] }],
    ['/v1/decisions', { decisions: [], total: 0, limit: 0, offset: 0 }],
    ['/v1/cases', { cases: [] }],
    ['/v1/agents', { agents: [] }],
    ['/v1/models', { models: [] }],
    ['/v1/policies', { policies: [] }],
    ['/v1/preapprovals', { preapprovals: [] }],
    ['/v1/webhooks', { webhooks: [] }],
    ['/v1/notifications', { notifications: [] }]
  ]);
  const hit = map.get(path);
  return ok(hit ?? {});
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
  pushAudit('deployment.requested', flow.flow_id, { environment, version });
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
    if (d.status === 'completed') cur.completed += 1;
    else cur.failed += 1;
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
  return {
    total: runs.length,
    completed: runs.filter((r) => r.status === 'completed').length,
    failed: runs.filter((r) => r.status === 'failed').length,
    by_agent: byAgent,
    prompt_tokens: promptTokens,
    completion_tokens: completionTokens,
    by_model: byModel,
    priced: false,
    total_cost_usd: 0
  };
}

function policyBacktest(policy: Policy, body: Body) {
  const dataset = (body.dataset as Body[]) ?? [];
  const spec = (body.spec as Policy['versions'][number]['spec']) ??
    policy.versions.find((v) => v.version === policy.latest)?.spec ??
    policy.versions.at(-1)?.spec ?? { rules: [] };
  const evaluated = { approve: 0, decline: 0, refer: 0, failed: 0 };
  const dispose = (row: Body): string => {
    for (const rule of spec.rules) {
      if (evalExpr(rule.when, row)) return rule.disposition;
    }
    return (spec.default as string) ?? 'refer';
  };
  for (const row of dataset) {
    const d = dispose(row) as keyof typeof evaluated;
    const m = new Map(Object.entries(evaluated));
    m.set(d, (m.get(d) ?? 0) + 1);
    Object.assign(evaluated, Object.fromEntries(m));
  }
  return { summary: { total: dataset.length, evaluated } };
}

function filterAudit(q: Query): AuditEntry[] {
  let list = state.audit;
  const stream = q.get('stream');
  const actor = q.get('actor');
  const type = q.get('type');
  const excludeType = q.get('exclude_type');
  if (stream) list = list.filter((e) => e.stream === stream);
  if (actor) list = list.filter((e) => e.actor === actor);
  if (type) list = list.filter((e) => e.type === type);
  if (excludeType) list = list.filter((e) => e.type !== excludeType);
  return list;
}

function auditCsv(entries: AuditEntry[]): string {
  const rows = entries.map((e) => `${e.seq},${e.time},${e.actor},${e.stream},${e.type}`);
  return ['seq,time,actor,stream,type', ...rows].join('\n');
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
  const drift = modelDrift(m.name);
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
