// SPDX-License-Identifier: AGPL-3.0-or-later

// API client for the intraktible backend. Functions take an injectable fetcher
// so they are unit-testable without a browser, and fail loudly on non-2xx
// responses rather than returning partial/empty data.

export interface HelloStats {
  org: string;
  workspace: string;
  count: number;
  last_name: string;
  last_at: string;
}

export interface SayHelloResult {
  event_id: string;
  seq: number;
}

function authHeaders(key: string): Record<string, string> {
  return { 'X-Api-Key': key };
}

export async function getStats(key: string, fetcher: typeof fetch = fetch): Promise<HelloStats> {
  const res = await fetcher('/v1/hello/stats', { headers: authHeaders(key) });
  if (!res.ok) {
    throw new Error(`GET /v1/hello/stats failed: ${res.status}`);
  }
  return (await res.json()) as HelloStats;
}

export async function sayHello(
  key: string,
  name: string,
  fetcher: typeof fetch = fetch
): Promise<SayHelloResult> {
  const res = await fetcher('/v1/hello', {
    method: 'POST',
    headers: { ...authHeaders(key), 'Content-Type': 'application/json' },
    body: JSON.stringify({ name })
  });
  if (!res.ok) {
    throw new Error(`POST /v1/hello failed: ${res.status}`);
  }
  return (await res.json()) as SayHelloResult;
}

// ---- Decision Engine ----

export interface GraphNode {
  id: string;
  type: string;
  name?: string;
  config?: unknown;
}

export interface GraphEdge {
  from: string;
  to: string;
  branch?: string;
}

export interface FlowGraph {
  nodes: GraphNode[];
  edges: GraphEdge[];
}

export interface FlowVersion {
  version: number;
  etag: string;
  graph: FlowGraph;
}

export interface Flow {
  flow_id: string;
  slug: string;
  name: string;
  latest: number;
  versions: FlowVersion[];
}

export interface DecideResult {
  decision_id: string;
  status: string;
  data?: Record<string, unknown>;
  error?: string;
}

function jsonHeaders(key: string): Record<string, string> {
  return { ...authHeaders(key), 'Content-Type': 'application/json' };
}

export async function listFlows(key: string, fetcher: typeof fetch = fetch): Promise<Flow[]> {
  const res = await fetcher('/v1/flows', { headers: authHeaders(key) });
  if (!res.ok) {
    throw new Error(`GET /v1/flows failed: ${res.status}`);
  }
  const body = (await res.json()) as { flows: Flow[] };
  return body.flows ?? [];
}

export async function createFlow(
  key: string,
  slug: string,
  name: string,
  fetcher: typeof fetch = fetch
): Promise<{ flow_id: string }> {
  const res = await fetcher('/v1/flows', {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify({ slug, name })
  });
  if (!res.ok) {
    throw new Error(`POST /v1/flows failed: ${res.status}`);
  }
  return (await res.json()) as { flow_id: string };
}

export async function getFlow(
  key: string,
  flowId: string,
  fetcher: typeof fetch = fetch
): Promise<Flow> {
  const res = await fetcher(`/v1/flows/${flowId}`, { headers: authHeaders(key) });
  if (!res.ok) {
    throw new Error(`GET /v1/flows/${flowId} failed: ${res.status}`);
  }
  return (await res.json()) as Flow;
}

export async function publishVersion(
  key: string,
  flowId: string,
  graph: FlowGraph,
  fetcher: typeof fetch = fetch
): Promise<{ version: number; etag: string }> {
  const res = await fetcher(`/v1/flows/${flowId}/versions`, {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify({ graph })
  });
  if (!res.ok) {
    // Surface the backend's validation message (fail loudly, visibly).
    const body = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(body.error ?? `publish version failed: ${res.status}`);
  }
  return (await res.json()) as { version: number; etag: string };
}

export async function decide(
  key: string,
  slug: string,
  env: string,
  data: Record<string, unknown>,
  fetcher: typeof fetch = fetch
): Promise<DecideResult> {
  const res = await fetcher(`/v1/flows/${slug}/${env}/decide`, {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify({ data })
  });
  if (!res.ok) {
    throw new Error(`POST decide failed: ${res.status}`);
  }
  return (await res.json()) as DecideResult;
}

// ---- Case Manager ----

export interface CaseNote {
  author: string;
  text: string;
  at: string;
}

export interface CaseAudit {
  type: string;
  actor: string;
  at: string;
  detail?: string;
}

export interface Case {
  case_id: string;
  company_name: string;
  case_type: string;
  status: string;
  assignee?: string;
  sla_days: number;
  source_decision_id?: string;
  context?: unknown;
  notes: CaseNote[];
  audit: CaseAudit[];
  created_at: string;
  updated_at: string;
}

export interface CaseFilter {
  status?: string;
  type?: string;
  assignee?: string;
}

// errorOrStatus throws the backend's error message (or a status fallback).
async function errorOrStatus(res: Response, label: string): Promise<never> {
  const body = (await res.json().catch(() => ({}))) as { error?: string };
  throw new Error(body.error ?? `${label}: ${res.status}`);
}

export async function listCases(
  key: string,
  filter: CaseFilter = {},
  fetcher: typeof fetch = fetch
): Promise<Case[]> {
  const q = new URLSearchParams();
  if (filter.status) q.set('status', filter.status);
  if (filter.type) q.set('type', filter.type);
  if (filter.assignee) q.set('assignee', filter.assignee);
  const qs = q.toString();
  const res = await fetcher(`/v1/cases${qs ? '?' + qs : ''}`, { headers: authHeaders(key) });
  if (!res.ok) {
    return errorOrStatus(res, 'GET /v1/cases');
  }
  return ((await res.json()) as { cases: Case[] }).cases ?? [];
}

export async function getCase(
  key: string,
  caseID: string,
  fetcher: typeof fetch = fetch
): Promise<Case> {
  const res = await fetcher(`/v1/cases/${caseID}`, { headers: authHeaders(key) });
  if (!res.ok) {
    return errorOrStatus(res, `GET /v1/cases/${caseID}`);
  }
  return (await res.json()) as Case;
}

export async function requestReview(
  key: string,
  body: { company_name: string; case_type: string; sla_days: number },
  fetcher: typeof fetch = fetch
): Promise<{ case_id: string }> {
  const res = await fetcher('/v1/cases', {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify(body)
  });
  if (!res.ok) {
    return errorOrStatus(res, 'POST /v1/cases');
  }
  return (await res.json()) as { case_id: string };
}

async function caseAction(
  key: string,
  caseID: string,
  action: string,
  body: Record<string, unknown>,
  fetcher: typeof fetch
): Promise<void> {
  const res = await fetcher(`/v1/cases/${caseID}/${action}`, {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify(body)
  });
  if (!res.ok) {
    await errorOrStatus(res, `POST /v1/cases/${caseID}/${action}`);
  }
}

export function assignCase(
  key: string,
  caseID: string,
  assignee: string,
  fetcher: typeof fetch = fetch
): Promise<void> {
  return caseAction(key, caseID, 'assign', { assignee }, fetcher);
}

export function setCaseStatus(
  key: string,
  caseID: string,
  status: string,
  fetcher: typeof fetch = fetch
): Promise<void> {
  return caseAction(key, caseID, 'status', { status }, fetcher);
}

export function addCaseNote(
  key: string,
  caseID: string,
  text: string,
  fetcher: typeof fetch = fetch
): Promise<void> {
  return caseAction(key, caseID, 'notes', { text }, fetcher);
}
