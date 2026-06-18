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

export interface GraphNode {
  id: string;
  type: string;
  name?: string;
  config?: unknown;
  position?: { x: number; y: number }; // builder canvas coordinate (presentation only)
  lane?: string; // swimlane the node belongs to (presentation/organizational)
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

export interface PromotionStagePolicy {
  require_assertions: boolean;
  require_no_firing_monitors: boolean;
  allow_force: boolean;
  require_review: boolean;
}

export interface Flow {
  flow_id: string;
  slug: string;
  name: string;
  latest: number;
  versions: FlowVersion[];
  deployments?: Record<string, DeploymentView>;
  deployment_requests?: DeploymentRequest[];
  promotion_policy?: Record<string, PromotionStagePolicy>;
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

export interface FlowImportResult {
  flow_id: string;
  slug: string;
  version: number;
  etag: string;
  created: boolean;
  published: boolean;
}

// importFlow upserts a flow from an exported document (the JSON `exportFlow`
// produces): it creates the flow if its slug is new, then publishes the graph as
// a new version. Re-importing identical content is a no-op (`published: false`),
// so it is safe to run from CI / GitOps on every push.
export async function importFlow(
  key: string,
  doc: unknown,
  fetcher: typeof fetch = fetch
): Promise<FlowImportResult> {
  const res = await fetcher('/v1/flows/import', {
    method: 'POST',
    headers: jsonHeaders(key),
    body: typeof doc === 'string' ? doc : JSON.stringify(doc)
  });
  if (!res.ok) {
    return errorOrStatus(res, 'POST /v1/flows/import');
  }
  return (await res.json()) as FlowImportResult;
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
  preapproval_id?: string; // set when served instantly from a pre-approval
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

export const MONITOR_METRICS = [
  'failure_rate',
  'refer_rate',
  'automation_rate',
  'approve_rate',
  'decline_rate',
  'avg_latency_ms',
  'volume',
  'distribution_drift'
] as const;
export type MonitorMetric = (typeof MONITOR_METRICS)[number];

export interface MonitorStatus {
  actual: number;
  computable: boolean;
  firing: boolean;
}

export interface Monitor {
  monitor_id: string;
  flow_id: string;
  metric: string;
  op: string; // gt | lt
  threshold: number;
  description?: string;
  status: MonitorStatus;
}

export async function listMonitors(
  key: string,
  flowId: string,
  fetcher: typeof fetch = fetch
): Promise<Monitor[]> {
  const res = await fetcher(`/v1/flows/${flowId}/monitors`, { headers: authHeaders(key) });
  if (!res.ok) {
    return errorOrStatus(res, `GET /v1/flows/${flowId}/monitors`);
  }
  return ((await res.json()) as { monitors: Monitor[] }).monitors ?? [];
}

export async function defineMonitor(
  key: string,
  flowId: string,
  body: { metric: string; op: string; threshold: number; description?: string },
  fetcher: typeof fetch = fetch
): Promise<{ monitor_id: string }> {
  const res = await fetcher(`/v1/flows/${flowId}/monitors`, {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify(body)
  });
  if (!res.ok) {
    return errorOrStatus(res, 'POST monitor');
  }
  return (await res.json()) as { monitor_id: string };
}

export async function deleteMonitor(
  key: string,
  flowId: string,
  monitorId: string,
  fetcher: typeof fetch = fetch
): Promise<void> {
  const res = await fetcher(`/v1/flows/${flowId}/monitors/${monitorId}`, {
    method: 'DELETE',
    headers: authHeaders(key)
  });
  if (!res.ok) {
    return errorOrStatus(res, 'DELETE monitor');
  }
}

export interface FiredMonitor {
  monitor_id: string;
  metric: string;
  op: string;
  threshold: number;
  actual: number;
  description?: string;
}

export interface DeliveryResult {
  webhook_id: string;
  url: string;
  ok: boolean;
  status?: number;
  error?: string;
}

export interface MonitorCheck {
  flow_id: string;
  checked: number;
  fired: FiredMonitor[];
  deliveries?: DeliveryResult[];
}

// checkMonitors evaluates a flow's monitors and pushes the firing ones to every
// active webhook (the pull-based alerting trigger).
export async function checkMonitors(
  key: string,
  flowId: string,
  fetcher: typeof fetch = fetch
): Promise<MonitorCheck> {
  const res = await fetcher(`/v1/flows/${flowId}/monitors/check`, {
    method: 'POST',
    headers: jsonHeaders(key)
  });
  if (!res.ok) {
    return errorOrStatus(res, 'POST monitor check');
  }
  return (await res.json()) as MonitorCheck;
}

export interface AssertionCase {
  name: string;
  input: Record<string, unknown>;
  expect: Record<string, unknown>;
}

export interface AssertionResult {
  name: string;
  passed: boolean;
  status: string;
  got?: Record<string, unknown>;
  mismatch?: string[];
  error?: string;
}

export interface AssertionReport {
  total: number;
  passed: number;
  failed: number;
  results: AssertionResult[];
}

export async function getAssertions(
  key: string,
  flowId: string,
  fetcher: typeof fetch = fetch
): Promise<AssertionCase[]> {
  const res = await fetcher(`/v1/flows/${flowId}/assertions`, { headers: authHeaders(key) });
  if (!res.ok) {
    return errorOrStatus(res, 'GET assertions');
  }
  return ((await res.json()) as { cases: AssertionCase[] }).cases ?? [];
}

export async function setAssertions(
  key: string,
  flowId: string,
  cases: AssertionCase[],
  fetcher: typeof fetch = fetch
): Promise<void> {
  const res = await fetcher(`/v1/flows/${flowId}/assertions`, {
    method: 'PUT',
    headers: jsonHeaders(key),
    body: JSON.stringify({ cases })
  });
  if (!res.ok) {
    return errorOrStatus(res, 'PUT assertions');
  }
}

export async function runAssertions(
  key: string,
  flowId: string,
  fetcher: typeof fetch = fetch
): Promise<AssertionReport> {
  const res = await fetcher(`/v1/flows/${flowId}/assertions/run`, {
    method: 'POST',
    headers: jsonHeaders(key)
  });
  if (!res.ok) {
    return errorOrStatus(res, 'POST assertions run');
  }
  return (await res.json()) as AssertionReport;
}

export interface DriftBucket {
  disposition: string;
  baseline: number;
  current: number;
  delta: number;
}

export interface DriftReport {
  has_baseline: boolean;
  has_current: boolean;
  max_drift: number;
  baseline_total?: number;
  current_total: number;
  buckets?: DriftBucket[];
}

// captureBaseline snapshots the flow's current disposition distribution as the
// reference that distribution_drift monitors measure against.
export async function captureBaseline(
  key: string,
  flowId: string,
  fetcher: typeof fetch = fetch
): Promise<void> {
  const res = await fetcher(`/v1/flows/${flowId}/baseline`, {
    method: 'POST',
    headers: jsonHeaders(key)
  });
  if (!res.ok) {
    return errorOrStatus(res, 'POST baseline');
  }
}

export async function getDrift(
  key: string,
  flowId: string,
  fetcher: typeof fetch = fetch
): Promise<DriftReport> {
  const res = await fetcher(`/v1/flows/${flowId}/drift`, { headers: authHeaders(key) });
  if (!res.ok) {
    return errorOrStatus(res, 'GET drift');
  }
  return (await res.json()) as DriftReport;
}

export interface Webhook {
  webhook_id: string;
  url: string;
  note?: string;
  active: boolean;
  delivery_count: number;
  last_status?: number;
  last_ok: boolean;
  last_error?: string;
  last_delivery_at?: string;
  created_at: string;
}

export async function listWebhooks(key: string, fetcher: typeof fetch = fetch): Promise<Webhook[]> {
  const res = await fetcher('/v1/webhooks', { headers: authHeaders(key) });
  if (!res.ok) {
    return errorOrStatus(res, 'GET /v1/webhooks');
  }
  return ((await res.json()) as { webhooks: Webhook[] }).webhooks ?? [];
}

export async function subscribeWebhook(
  key: string,
  url: string,
  note: string,
  fetcher: typeof fetch = fetch
): Promise<{ webhook_id: string }> {
  const res = await fetcher('/v1/webhooks', {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify({ url, note: note || undefined })
  });
  if (!res.ok) {
    return errorOrStatus(res, 'POST /v1/webhooks');
  }
  return (await res.json()) as { webhook_id: string };
}

export async function deleteWebhook(
  key: string,
  webhookId: string,
  fetcher: typeof fetch = fetch
): Promise<void> {
  const res = await fetcher(`/v1/webhooks/${webhookId}`, {
    method: 'DELETE',
    headers: authHeaders(key)
  });
  if (!res.ok) {
    return errorOrStatus(res, 'DELETE /v1/webhooks');
  }
}

export interface PolicyRule {
  when: string;
  disposition: string; // approve | decline | refer
  code?: string;
  description?: string;
}

export interface PolicySpec {
  rules: PolicyRule[];
  default?: string;
}

export interface PolicyVersion {
  version: number;
  etag: string;
  spec: PolicySpec;
  published_at?: string;
  published_by?: string;
}

export interface Policy {
  policy_id: string;
  name: string;
  flow_slug: string;
  latest: number;
  versions: PolicyVersion[];
  updated_at?: string;
}

export async function listPolicies(key: string, fetcher: typeof fetch = fetch): Promise<Policy[]> {
  const res = await fetcher('/v1/policies', { headers: authHeaders(key) });
  if (!res.ok) {
    return errorOrStatus(res, 'GET /v1/policies');
  }
  return ((await res.json()) as { policies: Policy[] }).policies ?? [];
}

export async function createPolicy(
  key: string,
  body: { name: string; flow_slug: string },
  fetcher: typeof fetch = fetch
): Promise<{ policy_id: string }> {
  const res = await fetcher('/v1/policies', {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify(body)
  });
  if (!res.ok) {
    return errorOrStatus(res, 'POST /v1/policies');
  }
  return (await res.json()) as { policy_id: string };
}

export async function publishPolicy(
  key: string,
  policyId: string,
  spec: PolicySpec,
  fetcher: typeof fetch = fetch
): Promise<{ version: number; etag: string }> {
  const res = await fetcher(`/v1/policies/${policyId}/versions`, {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify({ spec })
  });
  if (!res.ok) {
    return errorOrStatus(res, 'POST policy version');
  }
  return (await res.json()) as { version: number; etag: string };
}

export interface PolicyDistribution {
  approve: number;
  decline: number;
  refer: number;
  failed: number;
}

export interface PolicyBacktestReport {
  summary: {
    total: number;
    evaluated: PolicyDistribution;
    compare?: PolicyDistribution;
    flipped?: number;
  };
  flips?: { index: number; evaluated: string; compare: string }[];
}

// policyBacktest previews how a policy disposes a dataset (and how it shifts vs a
// compare version) without recording anything. `spec` is the unpublished draft.
export async function policyBacktest(
  key: string,
  policyId: string,
  body: {
    spec?: PolicySpec;
    compare_version?: number;
    flow_version?: number;
    dataset: Record<string, unknown>[];
  },
  fetcher: typeof fetch = fetch
): Promise<PolicyBacktestReport> {
  const res = await fetcher(`/v1/policies/${policyId}/backtest`, {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify(body)
  });
  if (!res.ok) {
    return errorOrStatus(res, 'POST policy backtest');
  }
  return (await res.json()) as PolicyBacktestReport;
}

export interface PreApproval {
  preapproval_id: string;
  entity_type: string;
  entity_id: string;
  disposition: string; // approve | decline
  terms?: Record<string, unknown>;
  policy_id?: string;
  policy_version?: number;
  flow_slug?: string;
  valid_until: string;
  status: string; // active | revoked
  revoked_reason?: string;
  honored_count: number;
  note?: string;
  granted_at: string;
  granted_by: string;
  updated_at: string;
}

export interface GrantPreApproval {
  entity_type: string;
  entity_id: string;
  disposition: string;
  terms?: Record<string, unknown>;
  flow_slug?: string;
  valid_days: number;
  note?: string;
}

export async function listPreApprovals(
  key: string,
  fetcher: typeof fetch = fetch
): Promise<PreApproval[]> {
  const res = await fetcher('/v1/preapprovals', { headers: authHeaders(key) });
  if (!res.ok) {
    return errorOrStatus(res, 'GET /v1/preapprovals');
  }
  return ((await res.json()) as { preapprovals: PreApproval[] }).preapprovals ?? [];
}

export async function grantPreApproval(
  key: string,
  body: GrantPreApproval,
  fetcher: typeof fetch = fetch
): Promise<{ preapproval_id: string }> {
  const res = await fetcher('/v1/preapprovals', {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify(body)
  });
  if (!res.ok) {
    return errorOrStatus(res, 'POST /v1/preapprovals');
  }
  return (await res.json()) as { preapproval_id: string };
}

export async function revokePreApproval(
  key: string,
  entityType: string,
  entityId: string,
  reason: string,
  fetcher: typeof fetch = fetch
): Promise<void> {
  const res = await fetcher(
    `/v1/preapprovals/${encodeURIComponent(entityType)}/${encodeURIComponent(entityId)}/revoke`,
    {
      method: 'POST',
      headers: jsonHeaders(key),
      body: JSON.stringify({ reason })
    }
  );
  if (!res.ok) {
    return errorOrStatus(res, 'POST revoke pre-approval');
  }
}

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

// promoteFlow ships the live version of `from` up to `to`. A non-production
// target deploys directly; production opens a maker-checker request (pending).
export async function promoteFlow(
  key: string,
  flowId: string,
  from: string,
  to: string,
  force = false,
  fetcher: typeof fetch = fetch
): Promise<{ promoted: boolean; pending?: boolean; request_id?: string; version: number }> {
  const res = await fetcher(`/v1/flows/${flowId}/promote`, {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify({ from, to, force })
  });
  if (!res.ok) {
    return errorOrStatus(res, 'POST promote');
  }
  return (await res.json()) as {
    promoted: boolean;
    pending?: boolean;
    request_id?: string;
    version: number;
  };
}

export async function setPromotionPolicy(
  key: string,
  flowId: string,
  policy: Record<string, Partial<PromotionStagePolicy>>,
  fetcher: typeof fetch = fetch
): Promise<Record<string, PromotionStagePolicy>> {
  const res = await fetcher(`/v1/flows/${flowId}/promotion-policy`, {
    method: 'PUT',
    headers: jsonHeaders(key),
    body: JSON.stringify({ policy })
  });
  if (!res.ok) {
    return errorOrStatus(res, 'PUT promotion policy');
  }
  return ((await res.json()) as { policy: Record<string, PromotionStagePolicy> }).policy;
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
  reason = '',
  fetcher: typeof fetch = fetch
): Promise<void> {
  const res = await fetcher(`/v1/flows/${flowId}/deployment-requests/${reqId}/approve`, {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify({ reason })
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

export interface PreApproveResult {
  index: number;
  entity_id?: string;
  decision_id?: string;
  status: string; // completed | failed | rejected
  disposition?: string;
  granted: boolean;
  preapproval_id?: string;
  reason?: string;
  error?: string;
}

export interface PreApproveBatchReport {
  total: number;
  granted: number;
  skipped: number;
  failed: number;
  rejected: number;
  results: PreApproveResult[];
}

// preapproveBatch runs a population through the flow + its bound policy and grants
// a time-boxed pre-approval for every row the policy disposes to `disposition`
// (default approve), keyed by each row's `entityKey` field.
export async function preapproveBatch(
  key: string,
  slug: string,
  env: string,
  body: {
    dataset: Record<string, unknown>[];
    entity_type: string;
    entity_key: string;
    disposition?: string;
    valid_days: number;
    note?: string;
  },
  fetcher: typeof fetch = fetch
): Promise<PreApproveBatchReport> {
  const res = await fetcher(`/v1/flows/${slug}/${env}/preapprove/batch`, {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify(body)
  });
  if (!res.ok) {
    const b = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(b.error ?? `POST preapprove batch failed: ${res.status}`);
  }
  return (await res.json()) as PreApproveBatchReport;
}

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

export interface Comment {
  comment_id: string;
  subject_type: string;
  subject_id: string;
  body: string;
  parent_id?: string;
  author: string;
  at: string;
}

export async function listComments(
  key: string,
  subjectType: string,
  subjectId: string,
  fetcher: typeof fetch = fetch
): Promise<Comment[]> {
  const res = await fetcher(`/v1/comments/${subjectType}/${encodeURIComponent(subjectId)}`, {
    headers: authHeaders(key)
  });
  if (!res.ok) {
    return errorOrStatus(res, 'GET comments');
  }
  return ((await res.json()) as { comments: Comment[] }).comments ?? [];
}

export async function postComment(
  key: string,
  subjectType: string,
  subjectId: string,
  body: string,
  parentId = '',
  fetcher: typeof fetch = fetch
): Promise<{ comment_id: string }> {
  const res = await fetcher(`/v1/comments/${subjectType}/${encodeURIComponent(subjectId)}`, {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify({ body, parent_id: parentId || undefined })
  });
  if (!res.ok) {
    return errorOrStatus(res, 'POST comment');
  }
  return (await res.json()) as { comment_id: string };
}

export interface Notification {
  notification_id: string;
  recipient: string;
  kind: string;
  subject_type: string;
  subject_id: string;
  snippet: string;
  author: string;
  read: boolean;
  created_at: string;
}

export async function listNotifications(
  key: string,
  fetcher: typeof fetch = fetch
): Promise<Notification[]> {
  const res = await fetcher('/v1/notifications', { headers: authHeaders(key) });
  if (!res.ok) {
    return errorOrStatus(res, 'GET /v1/notifications');
  }
  return ((await res.json()) as { notifications: Notification[] }).notifications ?? [];
}

export async function markNotificationRead(
  key: string,
  notificationId: string,
  fetcher: typeof fetch = fetch
): Promise<void> {
  const res = await fetcher(`/v1/notifications/${encodeURIComponent(notificationId)}/read`, {
    method: 'POST',
    headers: jsonHeaders(key)
  });
  if (!res.ok) {
    return errorOrStatus(res, 'POST notification read');
  }
}

export interface PrivacyConfig {
  fields: string[];
  updated_at?: string;
  updated_by?: string;
}

export async function getPrivacy(
  key: string,
  fetcher: typeof fetch = fetch
): Promise<PrivacyConfig> {
  const res = await fetcher('/v1/privacy', { headers: authHeaders(key) });
  if (!res.ok) {
    return errorOrStatus(res, 'GET /v1/privacy');
  }
  return (await res.json()) as PrivacyConfig;
}

export async function setPrivacy(
  key: string,
  fields: string[],
  fetcher: typeof fetch = fetch
): Promise<void> {
  const res = await fetcher('/v1/privacy', {
    method: 'PUT',
    headers: jsonHeaders(key),
    body: JSON.stringify({ fields })
  });
  if (!res.ok) {
    return errorOrStatus(res, 'PUT /v1/privacy');
  }
}

export interface ManagedApiKey {
  id: string;
  name: string;
  identity: { org: string; workspace: string; actor: string };
  scope: string;
  role: string;
  created_at: string;
  expires_at?: string;
  revoked_at?: string;
  rotated_at?: string;
  prev_hash_expires_at?: string;
}

export interface CreateApiKeyRequest {
  name: string;
  actor: string;
  role: string;
  scope?: string;
  expires_at?: string;
}

export async function listApiKeys(
  key: string,
  fetcher: typeof fetch = fetch
): Promise<ManagedApiKey[]> {
  const res = await fetcher('/v1/api-keys', { headers: authHeaders(key) });
  if (!res.ok) {
    return errorOrStatus(res, 'GET /v1/api-keys');
  }
  return ((await res.json()) as { api_keys: ManagedApiKey[] }).api_keys ?? [];
}

// createApiKey returns the new token's metadata plus the generated secret, which
// the server reveals only once — the caller must surface it immediately.
export async function createApiKey(
  key: string,
  req: CreateApiKeyRequest,
  fetcher: typeof fetch = fetch
): Promise<{ api_key: ManagedApiKey; secret: string }> {
  const res = await fetcher('/v1/api-keys', {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify(req)
  });
  if (!res.ok) {
    return errorOrStatus(res, 'POST /v1/api-keys');
  }
  return (await res.json()) as { api_key: ManagedApiKey; secret: string };
}

// rotateApiKey mints a fresh secret for a token, returning it once. The prior
// secret keeps working for graceSeconds (0 = immediate) so it can be rolled out
// without downtime.
export async function rotateApiKey(
  key: string,
  id: string,
  graceSeconds = 0,
  fetcher: typeof fetch = fetch
): Promise<{ api_key: ManagedApiKey; secret: string }> {
  const res = await fetcher(`/v1/api-keys/${encodeURIComponent(id)}/rotate`, {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify({ grace_seconds: graceSeconds })
  });
  if (!res.ok) {
    return errorOrStatus(res, 'POST rotate api key');
  }
  return (await res.json()) as { api_key: ManagedApiKey; secret: string };
}

export async function revokeApiKey(
  key: string,
  id: string,
  fetcher: typeof fetch = fetch
): Promise<ManagedApiKey> {
  const res = await fetcher(`/v1/api-keys/${encodeURIComponent(id)}`, {
    method: 'DELETE',
    headers: jsonHeaders(key)
  });
  if (!res.ok) {
    return errorOrStatus(res, 'DELETE /v1/api-keys');
  }
  return ((await res.json()) as { api_key: ManagedApiKey }).api_key;
}

export interface Connector {
  name: string;
  type: string;
  config?: unknown;
  updated_at: string;
}

export interface ConnectorTemplate {
  id: string;
  name: string;
  category: string;
  type: string;
  description: string;
  config: unknown;
}

export async function listConnectorCatalog(
  key: string,
  fetcher: typeof fetch = fetch
): Promise<ConnectorTemplate[]> {
  const res = await fetcher('/v1/context/connectors/catalog', { headers: authHeaders(key) });
  if (!res.ok) {
    return errorOrStatus(res, 'GET connector catalog');
  }
  return ((await res.json()) as { templates: ConnectorTemplate[] }).templates ?? [];
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
