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
