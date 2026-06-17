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

// authHeaders adds the API-key header only when a key is given; with an empty key
// the request authenticates via the session cookie (sent automatically same-origin).
function authHeaders(key: string): Record<string, string> {
  return key ? { 'X-Api-Key': key } : {};
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
  input_schema?: unknown;
  published_at?: string;
  published_by?: string;
}

export interface DeploymentView {
  version: number;
  challenger_version?: number;
  challenger_pct?: number;
}

export interface DeploymentRequest {
  request_id: string;
  environment: string;
  version: number;
  challenger_version?: number;
  challenger_pct?: number;
  status: string; // pending | approved | rejected
  reason?: string;
  requested_by: string;
  requested_at: string;
  decided_by?: string;
  decided_at?: string;
}

export interface Flow {
  flow_id: string;
  slug: string;
  name: string;
  latest: number;
  versions: FlowVersion[];
  deployments?: Record<string, DeploymentView>;
  deployment_requests?: DeploymentRequest[];
}

export interface DecideResult {
  decision_id: string;
  status: string;
  data?: Record<string, unknown>;
  disposition?: string; // approve | decline | refer (when a policy is bound)
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

// ExportFormat is a flow export the builder offers (diagrams + portable data).
export type ExportFormat = 'mermaid' | 'mermaid-state' | 'bpmn' | 'dot' | 'json';

// exportFlow fetches a flow version rendered as a diagram (text), failing loudly.
export async function exportFlow(
  key: string,
  flowId: string,
  format: ExportFormat,
  fetcher: typeof fetch = fetch
): Promise<string> {
  const res = await fetcher(`/v1/flows/${flowId}/export?format=${format}`, {
    headers: authHeaders(key)
  });
  if (!res.ok) {
    throw new Error(`export (${format}) failed: ${res.status}`);
  }
  return res.text();
}

// exportDecision fetches a decision run rendered as a Mermaid sequence diagram.
// RunExportFormat is a decision-run export the UI offers.
export type RunExportFormat = 'mermaid' | 'dot' | 'json';

export async function exportDecision(
  key: string,
  decisionId: string,
  format: RunExportFormat = 'mermaid',
  fetcher: typeof fetch = fetch
): Promise<string> {
  const res = await fetcher(`/v1/decisions/${decisionId}/export?format=${format}`, {
    headers: authHeaders(key)
  });
  if (!res.ok) {
    throw new Error(`export decision (${format}) failed: ${res.status}`);
  }
  return res.text();
}

// ---- Decision history + analytics ----

export interface NodeRecord {
  node_id: string;
  type: string;
  output?: unknown;
}

export interface ReasonCode {
  code: string;
  description: string;
}

export interface Decision {
  decision_id: string;
  flow_id: string;
  slug: string;
  version: number;
  environment: string;
  variant?: string;
  status: string;
  data?: unknown;
  output?: unknown;
  reason_codes?: ReasonCode[];
  disposition?: string; // approve | decline | refer
  disposition_reason?: string;
  policy_id?: string;
  policy_version?: number;
  error?: string;
  nodes?: NodeRecord[];
  started_at: string;
  ended_at?: string;
  duration_ms?: number;
}

export async function listDecisions(
  key: string,
  fetcher: typeof fetch = fetch
): Promise<Decision[]> {
  const res = await fetcher('/v1/decisions', { headers: authHeaders(key) });
  if (!res.ok) {
    throw new Error(`GET /v1/decisions failed: ${res.status}`);
  }
  return ((await res.json()) as { decisions: Decision[] }).decisions ?? [];
}

export async function getDecision(
  key: string,
  id: string,
  fetcher: typeof fetch = fetch
): Promise<Decision> {
  const res = await fetcher(`/v1/decisions/${id}`, { headers: authHeaders(key) });
  if (!res.ok) {
    throw new Error(`GET /v1/decisions/${id} failed: ${res.status}`);
  }
  return (await res.json()) as Decision;
}

export interface VariantStats {
  started: number;
  completed: number;
  failed: number;
}

export interface FlowMetrics {
  flow_id: string;
  total: number;
  completed: number;
  failed: number;
  total_duration_ms: number;
  avg_duration_ms: number;
  by_environment: Record<string, number>;
  by_version: Record<string, number>;
  by_variant: Record<string, VariantStats>;
  by_disposition?: Record<string, number>;
}

export async function getFlowMetrics(
  key: string,
  flowId: string,
  fetcher: typeof fetch = fetch
): Promise<FlowMetrics> {
  const res = await fetcher(`/v1/flows/${flowId}/metrics`, { headers: authHeaders(key) });
  if (!res.ok) {
    throw new Error(`GET /v1/flows/${flowId}/metrics failed: ${res.status}`);
  }
  return (await res.json()) as FlowMetrics;
}

// ---- Backtesting ----

export interface BacktestOutcome {
  status: string;
  output?: Record<string, unknown>;
  error?: string;
}

export interface BacktestRecord {
  index: number;
  baseline: BacktestOutcome;
  candidate?: BacktestOutcome;
  changed?: boolean;
}

export interface BacktestReport {
  summary: {
    total: number;
    compare: boolean;
    baseline_completed: number;
    baseline_failed: number;
    candidate_completed?: number;
    candidate_failed?: number;
    changed: number;
  };
  records: BacktestRecord[];
}

export async function backtestFlow(
  key: string,
  flowId: string,
  body: { version?: number; compare_version?: number; dataset: Record<string, unknown>[] },
  fetcher: typeof fetch = fetch
): Promise<BacktestReport> {
  const res = await fetcher(`/v1/flows/${flowId}/backtest`, {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify(body)
  });
  if (!res.ok) {
    const err = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(err.error ?? `backtest failed: ${res.status}`);
  }
  return (await res.json()) as BacktestReport;
}

export async function publishVersion(
  key: string,
  flowId: string,
  graph: FlowGraph,
  inputSchema?: unknown,
  fetcher: typeof fetch = fetch
): Promise<{ version: number; etag: string }> {
  const body = inputSchema === undefined ? { graph } : { graph, input_schema: inputSchema };
  const res = await fetcher(`/v1/flows/${flowId}/versions`, {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify(body)
  });
  if (!res.ok) {
    // Surface the backend's validation message (fail loudly, visibly).
    const body = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(body.error ?? `publish version failed: ${res.status}`);
  }
  return (await res.json()) as { version: number; etag: string };
}

// ---- Deployment & maker-checker (four-eyes) ----

export interface DeployInput {
  environment: string;
  version: number;
  challenger_version?: number;
  challenger_pct?: number;
}

// deployVersion pins a version live in an environment. The backend refuses a
// direct production deploy (use requestDeployment + approveDeployment for that).
export async function deployVersion(
  key: string,
  flowId: string,
  body: DeployInput,
  fetcher: typeof fetch = fetch
): Promise<void> {
  const res = await fetcher(`/v1/flows/${flowId}/deployments`, {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify(body)
  });
  if (!res.ok) {
    await errorOrStatus(res, 'POST deployment');
  }
}

// requestDeployment proposes a deployment for review (maker side).
export async function requestDeployment(
  key: string,
  flowId: string,
  body: DeployInput,
  fetcher: typeof fetch = fetch
): Promise<{ request_id: string }> {
  const res = await fetcher(`/v1/flows/${flowId}/deployment-requests`, {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify(body)
  });
  if (!res.ok) {
    return errorOrStatus(res, 'POST deployment-request');
  }
  return (await res.json()) as { request_id: string };
}

// approveDeployment is the checker side: approving deploys it (four-eyes — the
// backend rejects a self-approval by the proposer).
export async function approveDeployment(
  key: string,
  flowId: string,
  reqId: string,
  fetcher: typeof fetch = fetch
): Promise<void> {
  const res = await fetcher(`/v1/flows/${flowId}/deployment-requests/${reqId}/approve`, {
    method: 'POST',
    headers: jsonHeaders(key)
  });
  if (!res.ok) {
    await errorOrStatus(res, 'approve deployment');
  }
}

// rejectDeployment rejects a pending request, with an optional reason.
export async function rejectDeployment(
  key: string,
  flowId: string,
  reqId: string,
  reason: string,
  fetcher: typeof fetch = fetch
): Promise<void> {
  const res = await fetcher(`/v1/flows/${flowId}/deployment-requests/${reqId}/reject`, {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify({ reason })
  });
  if (!res.ok) {
    await errorOrStatus(res, 'reject deployment');
  }
}

// EntityRef optionally points a decision at a Context Layer entity so its
// features are folded into the input (referenced in expressions as features.*).
export interface EntityRef {
  type: string;
  id: string;
}

export async function decide(
  key: string,
  slug: string,
  env: string,
  data: Record<string, unknown>,
  entity?: EntityRef,
  fetcher: typeof fetch = fetch
): Promise<DecideResult> {
  const body: Record<string, unknown> = { data };
  if (entity?.type && entity?.id) {
    body.entity_type = entity.type;
    body.entity_id = entity.id;
  }
  const res = await fetcher(`/v1/flows/${slug}/${env}/decide`, {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify(body)
  });
  if (!res.ok) {
    throw new Error(`POST decide failed: ${res.status}`);
  }
  return (await res.json()) as DecideResult;
}

export interface BatchResult {
  index: number;
  decision_id?: string;
  status: string; // completed | failed | rejected
  data?: Record<string, unknown>;
  disposition?: string; // approve | decline | refer
  error?: string;
}

export interface BatchReport {
  total: number;
  completed: number;
  failed: number;
  rejected: number;
  results: BatchResult[];
}

// batchDecide runs a dataset of inputs through a published flow — each row a real
// recorded decision (appears in history, metrics, audit), unlike a backtest.
export async function batchDecide(
  key: string,
  slug: string,
  env: string,
  dataset: Record<string, unknown>[],
  entity?: EntityRef,
  fetcher: typeof fetch = fetch
): Promise<BatchReport> {
  const body: Record<string, unknown> = { dataset };
  if (entity?.type && entity?.id) {
    body.entity_type = entity.type;
    body.entity_id = entity.id;
  }
  const res = await fetcher(`/v1/flows/${slug}/${env}/decide/batch`, {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify(body)
  });
  if (!res.ok) {
    const b = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(b.error ?? `POST batch decide failed: ${res.status}`);
  }
  return (await res.json()) as BatchReport;
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
  days_left: number;
  sla_state?: string;
  source_decision_id?: string;
  context?: unknown;
  notes: CaseNote[];
  audit: CaseAudit[];
  created_at: string;
  updated_at: string;
}

export interface CaseSummary {
  total: number;
  by_status: Record<string, number>;
  unassigned: number;
  due_soon: number;
  overdue: number;
}

export interface CaseFilter {
  status?: string;
  type?: string;
  assignee?: string;
}

function caseQuery(filter: CaseFilter): string {
  const q = new URLSearchParams();
  if (filter.status) q.set('status', filter.status);
  if (filter.type) q.set('type', filter.type);
  if (filter.assignee) q.set('assignee', filter.assignee);
  const qs = q.toString();
  return qs ? '?' + qs : '';
}

// errorOrStatus throws the backend's error message (or a status fallback).
async function errorOrStatus(res: Response, label: string): Promise<never> {
  const body = (await res.json().catch(() => ({}))) as { error?: string };
  throw new Error(body.error ?? `${label}: ${res.status}`);
}

// ---- Audit surface ----

export interface AuditEntry {
  seq: number;
  id: string;
  time: string;
  actor: string;
  stream: string;
  type: string;
  payload?: unknown;
}

export interface AuditFilter {
  stream?: string;
  actor?: string;
  type?: string;
  resource?: string;
  since?: string;
  until?: string;
  limit?: number;
}

export function auditQuery(filter: AuditFilter): string {
  const q = new URLSearchParams();
  if (filter.stream) q.set('stream', filter.stream);
  if (filter.actor) q.set('actor', filter.actor);
  if (filter.type) q.set('type', filter.type);
  if (filter.resource) q.set('resource', filter.resource);
  if (filter.since) q.set('since', filter.since);
  if (filter.until) q.set('until', filter.until);
  if (filter.limit) q.set('limit', String(filter.limit));
  const qs = q.toString();
  return qs ? '?' + qs : '';
}

export async function listAudit(
  key: string,
  filter: AuditFilter = {},
  fetcher: typeof fetch = fetch
): Promise<AuditEntry[]> {
  const res = await fetcher(`/v1/audit${auditQuery(filter)}`, { headers: authHeaders(key) });
  if (!res.ok) {
    return errorOrStatus(res, 'GET /v1/audit');
  }
  return ((await res.json()) as { entries: AuditEntry[] }).entries ?? [];
}

// auditExportUrl is the CSV download URL for the current filter (the browser
// follows it with the session cookie, so no key is embedded).
export function auditExportUrl(filter: AuditFilter = {}): string {
  const q = auditQuery({ ...filter });
  return `/v1/audit${q ? q + '&' : '?'}format=csv`;
}

// ---- Context Layer (connectors, features, entities) ----

export interface Connector {
  name: string;
  type: string;
  config?: unknown;
  updated_at: string;
}

export interface Feature {
  name: string;
  entity_type: string;
  event_name: string;
  aggregation: string;
  field?: string;
  window_hours: number;
  updated_at: string;
}

export interface Entity {
  entity_type: string;
  entity_id: string;
  attributes: Record<string, unknown>;
  event_count: number;
  first_seen: string;
  updated_at: string;
}

export interface EntityEvent {
  entity_type: string;
  entity_id: string;
  event_name: string;
  data?: Record<string, unknown>;
  seq: number;
  occurred_at: string;
  recorded_at: string;
}

export interface FeatureValue {
  name: string;
  value: number;
}

export async function listConnectors(
  key: string,
  fetcher: typeof fetch = fetch
): Promise<Connector[]> {
  const res = await fetcher('/v1/context/connectors', { headers: authHeaders(key) });
  if (!res.ok) {
    return errorOrStatus(res, 'GET /v1/context/connectors');
  }
  return ((await res.json()) as { connectors: Connector[] }).connectors ?? [];
}

export async function defineConnector(
  key: string,
  body: { name: string; type: string; config?: unknown },
  fetcher: typeof fetch = fetch
): Promise<void> {
  const res = await fetcher('/v1/context/connectors', {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify(body)
  });
  if (!res.ok) {
    await errorOrStatus(res, 'POST /v1/context/connectors');
  }
}

export async function listFeatures(key: string, fetcher: typeof fetch = fetch): Promise<Feature[]> {
  const res = await fetcher('/v1/context/features', { headers: authHeaders(key) });
  if (!res.ok) {
    return errorOrStatus(res, 'GET /v1/context/features');
  }
  return ((await res.json()) as { features: Feature[] }).features ?? [];
}

export async function defineFeature(
  key: string,
  body: {
    name: string;
    entity_type: string;
    event_name: string;
    aggregation: string;
    field?: string;
    window_hours: number;
  },
  fetcher: typeof fetch = fetch
): Promise<void> {
  const res = await fetcher('/v1/context/features', {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify(body)
  });
  if (!res.ok) {
    await errorOrStatus(res, 'POST /v1/context/features');
  }
}

export async function listEntities(
  key: string,
  type = '',
  fetcher: typeof fetch = fetch
): Promise<Entity[]> {
  const qs = type ? `?type=${encodeURIComponent(type)}` : '';
  const res = await fetcher(`/v1/context/entities${qs}`, { headers: authHeaders(key) });
  if (!res.ok) {
    return errorOrStatus(res, 'GET /v1/context/entities');
  }
  return ((await res.json()) as { entities: Entity[] }).entities ?? [];
}

export async function getEntity(
  key: string,
  type: string,
  id: string,
  fetcher: typeof fetch = fetch
): Promise<Entity> {
  const res = await fetcher(
    `/v1/context/entities/${encodeURIComponent(type)}/${encodeURIComponent(id)}`,
    { headers: authHeaders(key) }
  );
  if (!res.ok) {
    return errorOrStatus(res, 'GET entity');
  }
  return (await res.json()) as Entity;
}

export async function listEntityEvents(
  key: string,
  type: string,
  id: string,
  fetcher: typeof fetch = fetch
): Promise<EntityEvent[]> {
  const res = await fetcher(
    `/v1/context/entities/${encodeURIComponent(type)}/${encodeURIComponent(id)}/events`,
    { headers: authHeaders(key) }
  );
  if (!res.ok) {
    return errorOrStatus(res, 'GET entity events');
  }
  return ((await res.json()) as { events: EntityEvent[] }).events ?? [];
}

export async function getEntityFeatures(
  key: string,
  type: string,
  id: string,
  fetcher: typeof fetch = fetch
): Promise<FeatureValue[]> {
  const res = await fetcher(
    `/v1/context/entities/${encodeURIComponent(type)}/${encodeURIComponent(id)}/features`,
    { headers: authHeaders(key) }
  );
  if (!res.ok) {
    return errorOrStatus(res, 'GET entity features');
  }
  return ((await res.json()) as { features: FeatureValue[] }).features ?? [];
}

export async function listCases(
  key: string,
  filter: CaseFilter = {},
  fetcher: typeof fetch = fetch
): Promise<Case[]> {
  const res = await fetcher(`/v1/cases${caseQuery(filter)}`, { headers: authHeaders(key) });
  if (!res.ok) {
    return errorOrStatus(res, 'GET /v1/cases');
  }
  return ((await res.json()) as { cases: Case[] }).cases ?? [];
}

export async function getCaseSummary(
  key: string,
  filter: CaseFilter = {},
  fetcher: typeof fetch = fetch
): Promise<CaseSummary> {
  const res = await fetcher(`/v1/cases/summary${caseQuery(filter)}`, { headers: authHeaders(key) });
  if (!res.ok) {
    return errorOrStatus(res, 'GET /v1/cases/summary');
  }
  return (await res.json()) as CaseSummary;
}

// sweepSLA folds the case stream and emits a breach event for every overdue open
// case (idempotent), returning how many were breached this sweep.
export async function sweepSLA(
  key: string,
  fetcher: typeof fetch = fetch
): Promise<{ count: number }> {
  const res = await fetcher('/v1/cases/sla-sweep', { method: 'POST', headers: authHeaders(key) });
  if (!res.ok) {
    return errorOrStatus(res, 'POST /v1/cases/sla-sweep');
  }
  return (await res.json()) as { count: number };
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

// ---- Agent Manager ----

export interface Agent {
  name: string;
  provider?: string;
  model?: string;
  system?: string;
  schema?: unknown;
  tools?: string[];
  runs: number;
  updated_at: string;
}

export interface AgentRun {
  run_id: string;
  agent: string;
  model?: string;
  prompt: string;
  status: string;
  text?: string;
  structured?: unknown;
  error?: string;
  at: string;
}

export interface RunResult {
  run_id: string;
  status: string;
  text?: string;
  structured?: unknown;
  error?: string;
}

export interface RunSummary {
  total: number;
  completed: number;
  failed: number;
  by_agent: Record<string, number>;
}

export async function listAgents(key: string, fetcher: typeof fetch = fetch): Promise<Agent[]> {
  const res = await fetcher('/v1/agents', { headers: authHeaders(key) });
  if (!res.ok) {
    return errorOrStatus(res, 'GET /v1/agents');
  }
  return ((await res.json()) as { agents: Agent[] }).agents ?? [];
}

export async function getAgent(
  key: string,
  name: string,
  fetcher: typeof fetch = fetch
): Promise<Agent> {
  const res = await fetcher(`/v1/agents/${name}`, { headers: authHeaders(key) });
  if (!res.ok) {
    return errorOrStatus(res, `GET /v1/agents/${name}`);
  }
  return (await res.json()) as Agent;
}

export async function defineAgent(
  key: string,
  body: {
    name: string;
    provider?: string;
    model?: string;
    system?: string;
    schema?: unknown;
    tools?: string[];
  },
  fetcher: typeof fetch = fetch
): Promise<void> {
  const res = await fetcher('/v1/agents', {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify(body)
  });
  if (!res.ok) {
    await errorOrStatus(res, 'POST /v1/agents');
  }
}

export async function runAgent(
  key: string,
  name: string,
  prompt: string,
  fetcher: typeof fetch = fetch
): Promise<RunResult> {
  const res = await fetcher(`/v1/agents/${name}/run`, {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify({ prompt })
  });
  if (!res.ok) {
    return errorOrStatus(res, `POST /v1/agents/${name}/run`);
  }
  return (await res.json()) as RunResult;
}

export async function listAgentRuns(
  key: string,
  name: string,
  fetcher: typeof fetch = fetch
): Promise<AgentRun[]> {
  const res = await fetcher(`/v1/agents/${name}/runs`, { headers: authHeaders(key) });
  if (!res.ok) {
    return errorOrStatus(res, `GET /v1/agents/${name}/runs`);
  }
  return ((await res.json()) as { runs: AgentRun[] }).runs ?? [];
}

export async function getRunSummary(
  key: string,
  fetcher: typeof fetch = fetch
): Promise<RunSummary> {
  const res = await fetcher('/v1/agent-runs/summary', { headers: authHeaders(key) });
  if (!res.ok) {
    return errorOrStatus(res, 'GET /v1/agent-runs/summary');
  }
  return (await res.json()) as RunSummary;
}

export async function escalateRun(
  key: string,
  name: string,
  runID: string,
  body: { company_name: string; case_type: string; sla_days: number },
  fetcher: typeof fetch = fetch
): Promise<{ case_id: string }> {
  const res = await fetcher(`/v1/agents/${name}/runs/${runID}/escalate`, {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify(body)
  });
  if (!res.ok) {
    return errorOrStatus(res, `POST /v1/agents/${name}/runs/${runID}/escalate`);
  }
  return (await res.json()) as { case_id: string };
}

// ---- Session auth (login/logout) ----

export interface Identity {
  org: string;
  workspace: string;
  actor: string;
}

// login exchanges an API key for a session cookie (set by the server).
export async function login(apiKey: string, fetcher: typeof fetch = fetch): Promise<Identity> {
  const res = await fetcher('/v1/login', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ api_key: apiKey })
  });
  if (!res.ok) {
    return errorOrStatus(res, 'POST /v1/login');
  }
  return (await res.json()) as Identity;
}

// logout revokes the current session and clears the cookie.
export async function logout(fetcher: typeof fetch = fetch): Promise<void> {
  const res = await fetcher('/v1/logout', { method: 'POST' });
  if (!res.ok && res.status !== 204) {
    await errorOrStatus(res, 'POST /v1/logout');
  }
}

// currentUser returns the signed-in identity from the session cookie, or null
// when there is no valid session.
export async function currentUser(fetcher: typeof fetch = fetch): Promise<Identity | null> {
  const res = await fetcher('/v1/me');
  if (res.status === 401) {
    return null;
  }
  if (!res.ok) {
    return errorOrStatus(res, 'GET /v1/me');
  }
  return (await res.json()) as Identity;
}
