// SPDX-License-Identifier: AGPL-3.0-or-later

// API client for the intraktible backend. Functions take an injectable fetcher
// so they are unit-testable without a browser, and fail loudly on non-2xx
// responses rather than returning partial/empty data.

// --- Domain enums (string-literal unions) ---------------------------------------
// The closed enums that mirror Go one-to-one are GENERATED from the Go consts
// (enums.generated.ts, via `make tsenums`) — the Go enum is the single source of
// truth, so the TS values cannot drift (a drift-check test fails CI otherwise). We
// re-export them here so callers keep importing from `$lib/api`. A typo'd or
// unhandled value fails type-check rather than silently rendering, and exhaustive
// switches use assertNever to flag a missing case at compile time.
import type {
  Disposition,
  RunStatus,
  Variant,
  NodeType,
  Aggregation,
  Role,
  Scope,
  Environment,
  CaseStatus,
  SLAState,
  AgentRunStatus,
  ModelKind,
  PreApprovalStatus,
  DeploymentRequestStatus,
  MonitorOp,
  MonitorMetric
} from './enums.generated';
export type {
  Disposition,
  RunStatus,
  Variant,
  NodeType,
  Aggregation,
  Role,
  Scope,
  Environment,
  CaseStatus,
  SLAState,
  AgentRunStatus,
  ModelKind,
  PreApprovalStatus,
  DeploymentRequestStatus,
  MonitorOp,
  MonitorMetric
} from './enums.generated';
export { ENVIRONMENTS, MONITOR_METRICS, AGGREGATIONS, ROLES, SCOPES } from './enums.generated';

// Composite / UI-only unions that build on the generated ones (not a 1:1 Go enum):
// a recorded decision is 'started' until its terminal event projects (the history
// read model writes 'started' on DecisionStarted), unlike the synchronous
// DecideResult which only ever returns a terminal status; batch/preapprove adds a
// client-rejected row state.
export type DecisionStatus = RunStatus | 'started';
export type BatchStatus = RunStatus | 'rejected';

// assertNever flags a missing case in an exhaustive switch at compile time: passing
// a value whose type isn't `never` (i.e. a case the switch failed to narrow away) is
// a type error. At runtime it throws, so an unexpected wire value still fails loudly.
export function assertNever(x: never, context = 'value'): never {
  throw new Error(`unexpected ${context}: ${JSON.stringify(x)}`);
}

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
  // X-Requested-With satisfies the backend's CSRF check for cookie-authenticated
  // requests — a cross-site form/navigation can't set a custom header. Harmless on
  // API-key requests (which the check exempts) and on the demo's fetch mock.
  const h: Record<string, string> = { 'X-Requested-With': 'intraktible' };
  if (key) h['X-Api-Key'] = key;
  return h;
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
  type: NodeType;
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
  previous_version?: number; // the version live before this one (for rollback)
}

export interface DeploymentRequest {
  request_id: string;
  environment: Environment;
  version: number;
  challenger_version?: number;
  challenger_pct?: number;
  status: DeploymentRequestStatus;
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
  status: RunStatus;
  data?: Record<string, unknown>;
  disposition?: Disposition; // when a policy is bound
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

export interface FlowBundleResult {
  slug: string;
  flow_id?: string;
  version?: number;
  created: boolean;
  published: boolean;
  error?: string;
}

export interface FlowBundleImport {
  results: FlowBundleResult[];
  published: number;
  failed: number;
  unchanged: number;
}

// importFlowBundle imports many flows in one document (`{ flows: [...] }`). It is
// best-effort: each flow's outcome (including any error) is in its result, so a
// bad flow does not abort the rest.
export async function importFlowBundle(
  key: string,
  bundle: unknown,
  fetcher: typeof fetch = fetch
): Promise<FlowBundleImport> {
  const res = await fetcher('/v1/flows/import-bundle', {
    method: 'POST',
    headers: jsonHeaders(key),
    body: typeof bundle === 'string' ? bundle : JSON.stringify(bundle)
  });
  if (!res.ok) {
    return errorOrStatus(res, 'POST /v1/flows/import-bundle');
  }
  return (await res.json()) as FlowBundleImport;
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
  type: NodeType;
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
  environment: Environment;
  variant?: Variant;
  status: DecisionStatus;
  data?: unknown;
  output?: unknown;
  reason_codes?: ReasonCode[];
  disposition?: Disposition;
  disposition_reason?: string;
  policy_id?: string;
  policy_version?: number;
  preapproval_id?: string; // set when served instantly from a pre-approval
  case_id?: string; // set when the decision routed to manual_review and opened a case
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

// DecisionFilter narrows the decisions list; empty fields are "any". A positive
// limit paginates (offset into the matched set); omit it for the full list.
export interface DecisionFilter {
  flow?: string;
  env?: Environment;
  status?: DecisionStatus;
  variant?: Variant;
  q?: string; // decision-id search (substring)
  since?: string; // RFC3339
  until?: string; // RFC3339
  limit?: number;
  offset?: number;
}

export interface DecisionPage {
  decisions: Decision[];
  total: number;
  limit: number;
  offset: number;
}

// listDecisionsPage is the filtered/paginated decisions query backing the list UI.
export async function listDecisionsPage(
  key: string,
  filter: DecisionFilter = {},
  fetcher: typeof fetch = fetch
): Promise<DecisionPage> {
  const qs = new URLSearchParams();
  for (const [k, v] of Object.entries(filter)) {
    if (v !== undefined && v !== '' && v !== null) qs.set(k, String(v));
  }
  const url = `/v1/decisions${qs.toString() ? `?${qs}` : ''}`;
  const res = await fetcher(url, { headers: authHeaders(key) });
  if (!res.ok) {
    throw new Error(`GET /v1/decisions failed: ${res.status}`);
  }
  const d = (await res.json()) as Partial<DecisionPage>;
  return {
    decisions: d.decisions ?? [],
    total: d.total ?? d.decisions?.length ?? 0,
    limit: d.limit ?? 0,
    offset: d.offset ?? 0
  };
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

// resumeDecision un-pauses a decision suspended at a durable human task, injecting the
// reviewer's outcome so the flow runs on to completion.
export async function resumeDecision(
  key: string,
  id: string,
  outcome: Record<string, unknown>,
  fetcher: typeof fetch = fetch
): Promise<{ decision_id: string; status: RunStatus; disposition?: Disposition }> {
  const res = await fetcher(`/v1/decisions/${id}/resume`, {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify({ outcome })
  });
  if (!res.ok) {
    throw new Error(`POST /v1/decisions/${id}/resume failed: ${res.status}`);
  }
  return (await res.json()) as {
    decision_id: string;
    status: RunStatus;
    disposition?: Disposition;
  };
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

export interface SLOConfig {
  success_target: number; // fraction in [0,1]
  latency_target_ms: number; // 0 = no latency objective
}

export interface SLOAttainment {
  decisions: number;
  success_rate: number;
  success_target: number;
  success_met: boolean;
  error_budget: number;
  budget_remaining: number; // 1 = full budget, <0 = over budget
  avg_latency_ms: number;
  latency_target_ms: number;
  latency_met: boolean;
}

export interface SLOResponse {
  slo: SLOConfig | null;
  attainment?: SLOAttainment;
}

export async function getFlowSLO(
  key: string,
  flowId: string,
  fetcher: typeof fetch = fetch
): Promise<SLOResponse> {
  const res = await fetcher(`/v1/flows/${flowId}/slo`, { headers: authHeaders(key) });
  if (!res.ok) {
    throw new Error(`GET /v1/flows/${flowId}/slo failed: ${res.status}`);
  }
  return (await res.json()) as SLOResponse;
}

export async function putFlowSLO(
  key: string,
  flowId: string,
  slo: SLOConfig,
  fetcher: typeof fetch = fetch
): Promise<void> {
  const res = await fetcher(`/v1/flows/${flowId}/slo`, {
    method: 'PUT',
    headers: jsonHeaders(key),
    body: JSON.stringify(slo)
  });
  if (!res.ok) {
    return errorOrStatus(res, `PUT /v1/flows/${flowId}/slo`);
  }
}

export interface MonitorStatus {
  actual: number;
  computable: boolean;
  firing: boolean;
}

export interface Monitor {
  monitor_id: string;
  flow_id: string;
  metric: MonitorMetric;
  op: MonitorOp;
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
  body: { metric: MonitorMetric; op: MonitorOp; threshold: number; description?: string },
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
  metric: MonitorMetric;
  op: MonitorOp;
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
  status: RunStatus;
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
  disposition: Disposition;
  baseline: number;
  current: number;
  delta: number;
}

export interface DriftReport {
  has_baseline: boolean;
  has_current: boolean;
  max_drift: number;
  psi: number; // population stability index over the disposition buckets
  kl: number; // Kullback–Leibler divergence over the disposition buckets
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
  template?: string; // Go text/template rendered against the alert payload (empty = raw JSON)
  events?: string[]; // routing filter on the delivery reason (empty = all)
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
  opts: { template?: string; events?: string[] } = {},
  fetcher: typeof fetch = fetch
): Promise<{ webhook_id: string }> {
  const res = await fetcher('/v1/webhooks', {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify({
      url,
      note: note || undefined,
      template: opts.template || undefined,
      events: opts.events && opts.events.length > 0 ? opts.events : undefined
    })
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
  disposition: Disposition;
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
  disposition: Disposition; // server narrows to approve|decline for a pre-approval
  terms?: Record<string, unknown>;
  policy_id?: string;
  policy_version?: number;
  flow_slug?: string;
  valid_until: string;
  status: PreApprovalStatus;
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
  disposition: Disposition;
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
  status: RunStatus;
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

export interface SweepPoint {
  value: unknown;
  status: RunStatus;
  output?: Record<string, unknown>;
  error?: string;
  changed: boolean;
}

export interface SweepReport {
  field: string;
  points: SweepPoint[];
  transitions: number;
}

// whatif runs a sensitivity analysis: sweep one input field across values and
// see how the flow's outcome shifts (record-nothing, pure engine).
export async function whatif(
  key: string,
  flowId: string,
  body: { base: Record<string, unknown>; field: string; values: unknown[]; version?: number },
  fetcher: typeof fetch = fetch
): Promise<SweepReport> {
  const res = await fetcher(`/v1/flows/${flowId}/whatif`, {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify(body)
  });
  if (!res.ok) {
    return errorOrStatus(res, 'POST whatif');
  }
  return (await res.json()) as SweepReport;
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
  environment: Environment;
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
    return errorOrStatus(res, 'POST deployment');
  }
}

// rollbackDeploy reverts an environment to its previous live version (instant
// rollback — allowed for any environment, audited).
export async function rollbackDeploy(
  key: string,
  flowId: string,
  environment: string,
  fetcher: typeof fetch = fetch
): Promise<void> {
  const res = await fetcher(`/v1/flows/${flowId}/deployments/rollback`, {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify({ environment })
  });
  if (!res.ok) {
    return errorOrStatus(res, 'POST rollback');
  }
}

export interface ScheduledDeploy {
  schedule_id: string;
  flow_id: string;
  environment: string;
  version: number;
  at: string;
  until?: string;
  status: 'pending' | 'active' | 'reverted' | 'canceled';
  prior_version?: number;
  created_at: string;
}

// scheduleDeploy queues a deploy for `at` (RFC3339); `until` makes it time-boxed
// (auto-revert after the window).
export async function scheduleDeploy(
  key: string,
  flowId: string,
  body: { environment: string; version: number; at: string; until?: string },
  fetcher: typeof fetch = fetch
): Promise<{ schedule_id: string }> {
  const res = await fetcher(`/v1/flows/${flowId}/deployments/schedule`, {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify(body)
  });
  if (!res.ok) {
    return errorOrStatus(res, 'POST schedule');
  }
  return (await res.json()) as { schedule_id: string };
}

export async function listSchedules(
  key: string,
  flowId: string,
  fetcher: typeof fetch = fetch
): Promise<ScheduledDeploy[]> {
  const res = await fetcher(`/v1/flows/${flowId}/deployments/schedules`, {
    headers: authHeaders(key)
  });
  if (!res.ok) {
    return errorOrStatus(res, 'GET schedules');
  }
  return ((await res.json()) as { schedules: ScheduledDeploy[] }).schedules ?? [];
}

export async function cancelSchedule(
  key: string,
  flowId: string,
  scheduleId: string,
  fetcher: typeof fetch = fetch
): Promise<void> {
  const res = await fetcher(`/v1/flows/${flowId}/deployments/schedules/${scheduleId}`, {
    method: 'DELETE',
    headers: authHeaders(key)
  });
  if (!res.ok) {
    return errorOrStatus(res, 'DELETE schedule');
  }
}

export interface FlowGrant {
  grant_id: string;
  flow_id: string;
  actor: string;
  environment: string;
  created_by: string;
  created_at: string;
}

export async function listGrants(
  key: string,
  flowId: string,
  fetcher: typeof fetch = fetch
): Promise<FlowGrant[]> {
  const res = await fetcher(`/v1/flows/${flowId}/grants`, { headers: authHeaders(key) });
  if (!res.ok) {
    return errorOrStatus(res, 'GET grants');
  }
  return ((await res.json()) as { grants: FlowGrant[] }).grants ?? [];
}

export async function addGrant(
  key: string,
  flowId: string,
  actor: string,
  environment: string,
  fetcher: typeof fetch = fetch
): Promise<{ grant_id: string }> {
  const res = await fetcher(`/v1/flows/${flowId}/grants`, {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify({ actor, environment })
  });
  if (!res.ok) {
    return errorOrStatus(res, 'POST grant');
  }
  return (await res.json()) as { grant_id: string };
}

export async function revokeGrant(
  key: string,
  flowId: string,
  grantId: string,
  fetcher: typeof fetch = fetch
): Promise<void> {
  const res = await fetcher(`/v1/flows/${flowId}/grants/${grantId}`, {
    method: 'DELETE',
    headers: authHeaders(key)
  });
  if (!res.ok) {
    return errorOrStatus(res, 'DELETE grant');
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

export interface EnvShadow {
  shadow_version: number;
  total: number;
  matched: number;
  diverged: number;
  errored: number;
  sample_diverged?: string[];
}

export interface ShadowState {
  shadows: Record<string, number>;
  report: Record<string, EnvShadow>;
}

// getShadow returns the per-environment shadow assignments and the divergence
// report (how often a shadow version's outcome differs from the live decision).
export async function getShadow(
  key: string,
  flowId: string,
  fetcher: typeof fetch = fetch
): Promise<ShadowState> {
  const res = await fetcher(`/v1/flows/${flowId}/shadow`, { headers: authHeaders(key) });
  if (!res.ok) {
    return errorOrStatus(res, 'GET shadow');
  }
  const body = (await res.json()) as Partial<ShadowState>;
  return { shadows: body.shadows ?? {}, report: body.report ?? {} };
}

// setShadow assigns (version 0 clears) the shadow version for an environment.
export async function setShadow(
  key: string,
  flowId: string,
  environment: string,
  version: number,
  fetcher: typeof fetch = fetch
): Promise<void> {
  const res = await fetcher(`/v1/flows/${flowId}/shadow`, {
    method: 'PUT',
    headers: jsonHeaders(key),
    body: JSON.stringify({ environment, version })
  });
  if (!res.ok) {
    return errorOrStatus(res, 'PUT shadow');
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
  reason = '',
  fetcher: typeof fetch = fetch
): Promise<void> {
  const res = await fetcher(`/v1/flows/${flowId}/deployment-requests/${reqId}/approve`, {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify({ reason })
  });
  if (!res.ok) {
    return errorOrStatus(res, 'approve deployment');
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
    return errorOrStatus(res, 'reject deployment');
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
  fetcher: typeof fetch = fetch,
  // preview runs the flow WITHOUT recording a decision (no history/metrics/audit) —
  // used by the builder's test run. The result then carries no decision_id.
  preview = false
): Promise<DecideResult> {
  const body: Record<string, unknown> = { data };
  if (preview) body.preview = true;
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
  status: BatchStatus;
  data?: Record<string, unknown>;
  disposition?: Disposition;
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
  status: BatchStatus;
  disposition?: Disposition;
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
    disposition?: Disposition;
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
  status: CaseStatus;
  assignee?: string;
  sla_days: number;
  days_left: number;
  sla_state?: SLAState;
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
  exclude_type?: string; // drop one event type (e.g. the node-evaluated noise)
  limit?: number;
  offset?: number;
}

export interface AuditPage {
  entries: AuditEntry[];
  total: number;
  limit: number;
  offset: number;
}

export function auditQuery(filter: AuditFilter): string {
  const q = new URLSearchParams();
  if (filter.stream) q.set('stream', filter.stream);
  if (filter.actor) q.set('actor', filter.actor);
  if (filter.type) q.set('type', filter.type);
  if (filter.resource) q.set('resource', filter.resource);
  if (filter.since) q.set('since', filter.since);
  if (filter.until) q.set('until', filter.until);
  if (filter.exclude_type) q.set('exclude_type', filter.exclude_type);
  if (filter.limit) q.set('limit', String(filter.limit));
  if (filter.offset) q.set('offset', String(filter.offset));
  const qs = q.toString();
  return qs ? '?' + qs : '';
}

export async function listAudit(
  key: string,
  filter: AuditFilter = {},
  fetcher: typeof fetch = fetch
): Promise<AuditEntry[]> {
  return (await listAuditPage(key, filter, fetcher)).entries;
}

// listAuditPage is the filtered/paginated audit read backing the Audit UI.
export async function listAuditPage(
  key: string,
  filter: AuditFilter = {},
  fetcher: typeof fetch = fetch
): Promise<AuditPage> {
  const res = await fetcher(`/v1/audit${auditQuery(filter)}`, { headers: authHeaders(key) });
  if (!res.ok) {
    return errorOrStatus(res, 'GET /v1/audit');
  }
  const d = (await res.json()) as Partial<AuditPage>;
  return {
    entries: d.entries ?? [],
    total: d.total ?? d.entries?.length ?? 0,
    limit: d.limit ?? 0,
    offset: d.offset ?? 0
  };
}

// auditExportUrl is the CSV download URL for the current filter (the browser
// follows it with the session cookie, so no key is embedded).
export function auditExportUrl(filter: AuditFilter = {}): string {
  const q = auditQuery({ ...filter });
  return `/v1/audit${q ? q + '&' : '?'}format=csv`;
}
// The filtered audit log as CSV text — wrapped in a Blob download by the page.
export async function auditCsvText(
  key: string,
  filter: AuditFilter = {},
  fetcher: typeof fetch = fetch
): Promise<string> {
  const res = await fetcher(auditExportUrl(filter), { headers: authHeaders(key) });
  if (!res.ok) {
    throw new Error(`export audit csv failed: ${res.status}`);
  }
  return res.text();
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
  scope: Scope;
  role: Role;
  created_at: string;
  expires_at?: string;
  revoked_at?: string;
  rotated_at?: string;
  prev_hash_expires_at?: string;
}

export interface CreateApiKeyRequest {
  name: string;
  actor: string;
  role: Role;
  scope?: Scope;
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
  aggregation: Aggregation;
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
    return errorOrStatus(res, 'POST /v1/context/connectors');
  }
}

export interface Model {
  name: string;
  kind: ModelKind;
  spec: unknown;
  owner?: string;
  updated_at: string;
}

export async function listModels(key: string, fetcher: typeof fetch = fetch): Promise<Model[]> {
  const res = await fetcher('/v1/models', { headers: authHeaders(key) });
  if (!res.ok) {
    return errorOrStatus(res, 'GET /v1/models');
  }
  return ((await res.json()) as { models: Model[] }).models ?? [];
}

export async function defineModel(
  key: string,
  body: { name: string; spec: unknown },
  fetcher: typeof fetch = fetch
): Promise<void> {
  const res = await fetcher('/v1/models', {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify(body)
  });
  if (!res.ok) {
    return errorOrStatus(res, 'POST /v1/models');
  }
}

// copilotExplain returns a plain-language explanation of a flow graph.
export async function copilotExplain(
  key: string,
  graph: unknown,
  fetcher: typeof fetch = fetch
): Promise<string> {
  const res = await fetcher('/v1/copilot/explain', {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify({ graph })
  });
  if (!res.ok) {
    return errorOrStatus(res, 'POST /v1/copilot/explain');
  }
  return ((await res.json()) as { text: string }).text ?? '';
}

// copilotSuggest turns a natural-language requirement into suggested decision logic.
export async function copilotSuggest(
  key: string,
  prompt: string,
  fetcher: typeof fetch = fetch
): Promise<string> {
  const res = await fetcher('/v1/copilot/suggest', {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify({ prompt })
  });
  if (!res.ok) {
    return errorOrStatus(res, 'POST /v1/copilot/suggest');
  }
  return ((await res.json()) as { text: string }).text ?? '';
}

// copilotGenerate returns a server-validated flow graph for a requirement (throws
// with the server message on 422 when the model can't produce a valid flow).
export async function copilotGenerate(
  key: string,
  prompt: string,
  fetcher: typeof fetch = fetch
): Promise<unknown> {
  const res = await fetcher('/v1/copilot/generate', {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify({ prompt })
  });
  if (!res.ok) {
    return errorOrStatus(res, 'POST /v1/copilot/generate');
  }
  return ((await res.json()) as { graph: unknown }).graph;
}

export interface ModelDrift {
  model: string;
  count: number;
  hist: number[];
  window_days: number;
  has_baseline: boolean;
  psi?: number;
  threshold?: number;
  firing: boolean;
  alerting: boolean;
}

export async function modelDrift(
  key: string,
  name: string,
  windowDays = 0,
  fetcher: typeof fetch = fetch
): Promise<ModelDrift> {
  const q = windowDays > 0 ? `?window=${windowDays}d` : '';
  const res = await fetcher(`/v1/models/${encodeURIComponent(name)}/drift${q}`, {
    headers: authHeaders(key)
  });
  if (!res.ok) {
    return errorOrStatus(res, 'GET model drift');
  }
  return (await res.json()) as ModelDrift;
}

export async function setModelMonitor(
  key: string,
  name: string,
  threshold: number,
  fetcher: typeof fetch = fetch
): Promise<void> {
  const res = await fetcher(`/v1/models/${encodeURIComponent(name)}/monitor`, {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify({ threshold })
  });
  if (!res.ok) {
    return errorOrStatus(res, 'POST model monitor');
  }
}

export async function captureModelBaseline(
  key: string,
  name: string,
  fetcher: typeof fetch = fetch
): Promise<void> {
  const res = await fetcher(`/v1/models/${encodeURIComponent(name)}/baseline`, {
    method: 'POST',
    headers: jsonHeaders(key)
  });
  if (!res.ok) {
    return errorOrStatus(res, 'POST model baseline');
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
    return errorOrStatus(res, 'POST /v1/context/features');
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
    return errorOrStatus(res, `POST /v1/cases/${caseID}/${action}`);
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
  status: CaseStatus,
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
  latest?: number; // current version number (registry)
  runs: number;
  updated_at: string;
}

export interface AgentRun {
  run_id: string;
  agent: string;
  model?: string;
  prompt: string;
  status: AgentRunStatus;
  text?: string;
  structured?: unknown;
  error?: string;
  at: string;
}

export interface RunResult {
  run_id: string;
  status: AgentRunStatus;
  text?: string;
  structured?: unknown;
  error?: string;
}

export interface ModelUsage {
  runs: number;
  prompt_tokens: number;
  completion_tokens: number;
}

export interface RunSummary {
  total: number;
  completed: number;
  failed: number;
  by_agent: Record<string, number>;
  prompt_tokens: number;
  completion_tokens: number;
  by_model: Record<string, ModelUsage>;
  // Cost is present only when a price table is configured (INTRAKTIBLE_AI_PRICES).
  priced: boolean;
  total_cost_usd: number;
  cost_by_model?: Record<string, number>;
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
    return errorOrStatus(res, 'POST /v1/agents');
  }
}

export async function runAgent(
  key: string,
  name: string,
  prompt: string,
  version = 0, // 0 = latest; pin a published version
  fetcher: typeof fetch = fetch
): Promise<RunResult> {
  const res = await fetcher(`/v1/agents/${name}/run`, {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify({ prompt, version: version || undefined })
  });
  if (!res.ok) {
    return errorOrStatus(res, `POST /v1/agents/${name}/run`);
  }
  return (await res.json()) as RunResult;
}

export interface AgentVersion {
  version: number;
  etag: string;
  provider?: string;
  model?: string;
  system?: string;
  schema?: unknown;
  tools?: string[];
  published_at: string;
  published_by: string;
}

// listAgentVersions returns an agent's immutable config history (newest first).
export async function listAgentVersions(
  key: string,
  name: string,
  fetcher: typeof fetch = fetch
): Promise<AgentVersion[]> {
  const res = await fetcher(`/v1/agents/${name}/versions`, { headers: authHeaders(key) });
  if (!res.ok) {
    return errorOrStatus(res, `GET /v1/agents/${name}/versions`);
  }
  return ((await res.json()) as { versions: AgentVersion[] }).versions ?? [];
}

export type EvalMode = 'contains' | 'equals' | 'json_subset';

export interface EvalCase {
  name: string;
  prompt: string;
  mode?: EvalMode;
  expect?: string;
  expect_json?: unknown;
}

export interface EvalResult {
  name: string;
  passed: boolean;
  status: string;
  output?: string;
  detail?: string;
}

export interface EvalReport {
  total: number;
  passed: number;
  failed: number;
  version: number;
  results: EvalResult[];
}

export async function getAgentEvals(
  key: string,
  name: string,
  fetcher: typeof fetch = fetch
): Promise<EvalCase[]> {
  const res = await fetcher(`/v1/agents/${name}/evals`, { headers: authHeaders(key) });
  if (!res.ok) {
    return errorOrStatus(res, `GET /v1/agents/${name}/evals`);
  }
  return ((await res.json()) as { cases: EvalCase[] }).cases ?? [];
}

export async function setAgentEvals(
  key: string,
  name: string,
  cases: EvalCase[],
  fetcher: typeof fetch = fetch
): Promise<void> {
  const res = await fetcher(`/v1/agents/${name}/evals`, {
    method: 'PUT',
    headers: jsonHeaders(key),
    body: JSON.stringify({ cases })
  });
  if (!res.ok) {
    return errorOrStatus(res, `PUT /v1/agents/${name}/evals`);
  }
}

export async function runAgentEval(
  key: string,
  name: string,
  version = 0,
  fetcher: typeof fetch = fetch
): Promise<EvalReport> {
  const res = await fetcher(`/v1/agents/${name}/evals/run`, {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify({ version: version || undefined })
  });
  if (!res.ok) {
    return errorOrStatus(res, `POST /v1/agents/${name}/evals/run`);
  }
  return (await res.json()) as EvalReport;
}

// --- Model risk management (SR 11-7) report ---

export type MrmModelKind = 'flow' | 'predictive_model' | 'agent';
export type MrmCoverage = 'tested' | 'failing' | 'none';

export interface MrmValidation {
  coverage: MrmCoverage;
  has_assertions?: boolean;
  assertions_total?: number;
  assertions_passed?: number;
  has_eval_cases?: boolean;
  eval_cases?: number;
  has_baseline?: boolean;
  shadow_diverged?: number;
}

export interface MrmMonitoring {
  decisions: number;
  success_rate: number;
  firing_monitors?: string[];
  drift_psi?: number;
  drift_firing?: boolean;
  slo_met?: boolean;
}

export interface MrmModel {
  kind: MrmModelKind;
  id: string;
  name: string;
  version: number;
  owner?: string;
  deployments?: Record<string, number>;
  validation: MrmValidation;
  monitoring: MrmMonitoring;
  issues?: string[];
  updated_at: string;
}

export interface MrmReport {
  generated_at: string;
  org: string;
  workspace: string;
  summary: {
    total: number;
    by_kind: Record<string, number>;
    deployed: number;
    unvalidated: number;
    with_issues: number;
  };
  models: MrmModel[];
}

// getMrmReport fetches the model-risk report (admin-gated).
export async function getMrmReport(key: string, fetcher: typeof fetch = fetch): Promise<MrmReport> {
  const res = await fetcher('/v1/mrm/report', { headers: authHeaders(key) });
  if (!res.ok) {
    return errorOrStatus(res, 'GET /v1/mrm/report');
  }
  return (await res.json()) as MrmReport;
}
// The MRM report as CSV/Markdown text — the page wraps it in a Blob download (an
// <a href> would escape the demo's fetch mock and 404 on the static host).
export async function mrmReportText(
  key: string,
  format: 'csv' | 'md',
  fetcher: typeof fetch = fetch
): Promise<string> {
  const res = await fetcher(`/v1/mrm/report?format=${format}`, { headers: authHeaders(key) });
  if (!res.ok) {
    throw new Error(`export mrm report (${format}) failed: ${res.status}`);
  }
  return res.text();
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
  role?: string; // viewer|operator|editor|approver|admin — present from /v1/me
  scope?: string;
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
    return errorOrStatus(res, 'POST /v1/logout');
  }
}

// listSsoProviders returns the configured OIDC providers (e.g. ["google","aws"])
// so the login page can offer a "Sign in with …" button for each. Returns an
// empty list when SSO is not configured or the endpoint is unavailable.
export async function listSsoProviders(fetcher: typeof fetch = fetch): Promise<string[]> {
  try {
    const res = await fetcher('/v1/auth/oidc/providers');
    if (!res.ok) {
      return [];
    }
    return ((await res.json()) as { providers?: string[] }).providers ?? [];
  } catch {
    return [];
  }
}

// listSamlProviders returns the configured SAML providers, mirroring
// listSsoProviders. Empty when SAML is not configured.
export async function listSamlProviders(fetcher: typeof fetch = fetch): Promise<string[]> {
  try {
    const res = await fetcher('/v1/auth/saml/providers');
    if (!res.ok) {
      return [];
    }
    return ((await res.json()) as { providers?: string[] }).providers ?? [];
  } catch {
    return [];
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
