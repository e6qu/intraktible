// SPDX-License-Identifier: AGPL-3.0-or-later

// API client for the intraktible backend. Functions take an injectable fetcher
// so they are unit-testable without a browser, and fail loudly on non-2xx
// responses rather than returning partial/empty data. The default fetcher is
// recordingFetch below, which also logs {method, path, status} into the
// per-navigation recorder that feeds each page's "Export for AI" document.

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
  SLAState,
  AgentRunStatus,
  AgentAssistKind,
  AgentReleaseStatus,
  AgentDeploymentStatus,
  AgentToolApprovalMode,
  AgentToolApprovalStatus,
  AgentGraderKind,
  ModelKind,
  PreApprovalStatus,
  DeploymentRequestStatus,
  MonitorOp,
  MonitorMetric,
  ExperimentState,
  ExperimentArmKind,
  ExperimentMetricKind,
  ExperimentDirection,
  ExperimentAnalysisStatus,
  OutcomeKind,
  PopulationJobKind,
  PopulationJobState
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
  AgentAssistKind,
  AgentReleaseStatus,
  AgentDeploymentStatus,
  AgentToolApprovalMode,
  AgentToolApprovalStatus,
  AgentGraderKind,
  ModelKind,
  PreApprovalStatus,
  DeploymentRequestStatus,
  MonitorOp,
  MonitorMetric,
  ExperimentState,
  ExperimentArmKind,
  ExperimentMetricKind,
  ExperimentDirection,
  ExperimentAnalysisStatus,
  OutcomeKind,
  PopulationJobKind,
  PopulationJobState
} from './enums.generated';
export { ENVIRONMENTS, MONITOR_METRICS, AGGREGATIONS, ROLES, SCOPES } from './enums.generated';
import { record } from './recorder';
import { waitForApplied } from './poll';

// recordingFetch is the default fetcher for every function in this module: it
// forwards to the global fetch (resolved at call time, so the wasm demo's
// bridged fetch is included) and records the call for the page's "Export for
// AI" document. Tests that inject their own fetcher bypass it, by design.
export const recordingFetch: typeof fetch = async (input, init) => {
  const res = await fetch(input, init);
  const raw = input instanceof Request ? input.url : String(input);
  const method = (init?.method ?? (input instanceof Request ? input.method : 'GET')).toUpperCase();
  // The base is irrelevant — only the pathname is recorded (no host, no query).
  record({ method, path: new URL(raw, 'http://api').pathname, status: res.status });
  // Commands acknowledge their durable event before the projection consumer
  // necessarily exposes it. Most UI actions immediately reload the affected view;
  // when a successful command carries an event sequence, establish that shared
  // read-after-write boundary here so every page observes its own write. Clone the
  // response so the individual API function can still parse the original body.
  if (
    res.ok &&
    method !== 'GET' &&
    method !== 'HEAD' &&
    res.headers.get('content-type')?.includes('application/json')
  ) {
    const body = (await res.clone().json()) as unknown;
    if (
      body !== null &&
      typeof body === 'object' &&
      'seq' in body &&
      typeof (body as { seq?: unknown }).seq === 'number'
    ) {
      await waitForApplied((body as { seq: number }).seq);
    }
  }
  return res;
};

// Composite / UI-only unions that build on the generated ones (not a 1:1 Go enum):
// A recorded decision is running while its synchronous owner holds the execution
// lease, retrying after recovery claims it, or abandoned when an indeterminate
// at-least-once effect makes automatic replay unsafe.
export type DecisionStatus = RunStatus | 'running' | 'retrying' | 'abandoned';
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
export type EventAck = SayHelloResult;

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

export async function getStats(
  key: string,
  fetcher: typeof fetch = recordingFetch
): Promise<HelloStats> {
  const res = await fetcher('/v1/hello/stats', { headers: authHeaders(key) });
  if (!res.ok) {
    return errorOrStatus(res, `GET /v1/hello/stats failed`);
  }
  return (await res.json()) as HelloStats;
}

export async function sayHello(
  key: string,
  name: string,
  fetcher: typeof fetch = recordingFetch
): Promise<SayHelloResult> {
  const res = await fetcher('/v1/hello', {
    method: 'POST',
    headers: { ...authHeaders(key), 'Content-Type': 'application/json' },
    body: JSON.stringify({ name })
  });
  if (!res.ok) {
    return errorOrStatus(res, `POST /v1/hello failed`);
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
  source_graph?: FlowGraph;
  dependencies?: FlowDependency[];
  changeset_id?: string;
  draft_id?: string;
  draft_revision?: number;
  input_schema?: unknown;
  published_at?: string;
  published_by?: string;
}

export interface FlowDependency {
  component_id: string;
  version: number;
  etag: string;
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
  schedule_id?: string;
  at?: string;
  until?: string;
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
  description?: string;
  latest: number;
  versions: FlowVersion[];
  deployments?: Record<string, DeploymentView>;
  deployment_requests?: DeploymentRequest[];
  promotion_policy?: Record<string, PromotionStagePolicy>;
}

export interface DecideResult {
  decision_id: string;
  status: RunStatus;
  seq?: number; // absent for record-free Preview
  data?: Record<string, unknown>;
  disposition?: Disposition; // when a policy is bound
  disposition_reason?: string; // the matched band, or "pre-approval honored"
  preapproval_id?: string; // set when served instantly from a pre-approval (flow skipped)
  experiment_id?: string;
  experiment_cohort?: number;
  experiment_arm?: string;
  error?: string;
}

function jsonHeaders(key: string): Record<string, string> {
  return { ...authHeaders(key), 'Content-Type': 'application/json' };
}

function idempotentJSONHeaders(key: string, idempotencyKey: string): Record<string, string> {
  return { ...jsonHeaders(key), 'Idempotency-Key': idempotencyKey };
}

// normalizeFlow makes the wire shape honest against the Flow type: the Go API
// omits empty collections (omitempty), so an unpublished flow arrives WITHOUT
// `versions` — normalize once here so no consumer needs a null guard to iterate.
function normalizeFlow(f: Flow): Flow {
  return { ...f, versions: f.versions ?? [] };
}

export async function listFlows(
  key: string,
  fetcher: typeof fetch = recordingFetch
): Promise<Flow[]> {
  const res = await fetcher('/v1/flows', { headers: authHeaders(key) });
  if (!res.ok) {
    return errorOrStatus(res, `GET /v1/flows failed`);
  }
  const body = (await res.json()) as { flows: Flow[] };
  return (body.flows ?? []).map(normalizeFlow);
}

export async function createFlow(
  key: string,
  slug: string,
  name: string,
  description = '',
  fetcher: typeof fetch = recordingFetch
): Promise<{ flow_id: string }> {
  // description is optional on the wire — omit it entirely when blank rather
  // than sending an empty string.
  const body = description.trim()
    ? { slug, name, description: description.trim() }
    : { slug, name };
  const res = await fetcher('/v1/flows', {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify(body)
  });
  if (!res.ok) {
    return errorOrStatus(res, `POST /v1/flows failed`);
  }
  return (await res.json()) as { flow_id: string };
}

// updateFlow patches a flow's mutable metadata (today: the description).
export async function updateFlow(
  key: string,
  flowId: string,
  patch: { description: string },
  fetcher: typeof fetch = recordingFetch
): Promise<void> {
  const res = await fetcher(`/v1/flows/${flowId}`, {
    method: 'PATCH',
    headers: jsonHeaders(key),
    body: JSON.stringify(patch)
  });
  if (!res.ok) {
    return errorOrStatus(res, `PATCH /v1/flows/${flowId} failed`);
  }
}

export async function getFlow(
  key: string,
  flowId: string,
  fetcher: typeof fetch = recordingFetch
): Promise<Flow> {
  const res = await fetcher(`/v1/flows/${flowId}`, { headers: authHeaders(key) });
  if (!res.ok) {
    return errorOrStatus(res, `GET /v1/flows/${flowId} failed`);
  }
  return normalizeFlow((await res.json()) as Flow);
}

// ExportFormat is a flow export the builder offers (diagrams + portable data).
export type ExportFormat = 'mermaid' | 'mermaid-state' | 'bpmn' | 'dot' | 'json';

// exportFlow fetches a flow version rendered as a diagram (text), failing loudly.
export async function exportFlow(
  key: string,
  flowId: string,
  format: ExportFormat,
  fetcher: typeof fetch = recordingFetch
): Promise<string> {
  const res = await fetcher(`/v1/flows/${flowId}/export?format=${format}`, {
    headers: authHeaders(key)
  });
  if (!res.ok) {
    return errorOrStatus(res, `export (${format}) failed`);
  }
  return res.text();
}

// exportAuthoringDraft returns the current canvas as byte-stable canonical v1
// source, without target-workspace version/deployment metadata or layout noise.
export async function exportAuthoringDraft(
  key: string,
  draftId: string,
  fetcher: typeof fetch = recordingFetch
): Promise<string> {
  const res = await fetcher(`/v1/authoring/drafts/${encodeURIComponent(draftId)}/export`, {
    headers: authHeaders(key)
  });
  if (!res.ok) {
    return errorOrStatus(res, 'export canonical authoring draft');
  }
  return res.text();
}

export interface FlowImportResult {
  flow_id: string;
  draft_id: string;
  revision: number;
  created: boolean;
  migration_report: CanonicalMigrationReport;
  event_id: string;
  seq: number;
}

export interface CanonicalMigrationReport {
  rewrites: Array<{
    path: string;
    from: string;
    to: string;
    reason: string;
  }>;
}

function canonicalFlowDocument(doc: unknown): Record<string, unknown> {
  if (doc === null || typeof doc !== 'object' || Array.isArray(doc)) {
    throw new Error('canonical flow import must be a JSON object');
  }
  const source = doc as Record<string, unknown>;
  // The older flow JSON export carries target-workspace publication metadata
  // (`version`, `etag`) that is deliberately not part of repository source.
  // Recognize that unversioned shape explicitly and project only its documented
  // source fields. Once a caller supplies a canonical marker, preserve every
  // field so the strict server decoder still catches typos and future semantics.
  if (source.format_version === undefined && source.kind === undefined) {
    return {
      format_version: 'intraktible.authoring/v1',
      kind: 'flow',
      slug: source.slug,
      name: source.name,
      description: source.description,
      graph: source.graph,
      input_schema: source.input_schema
    };
  }
  return {
    ...source,
    format_version: source.format_version ?? 'intraktible.authoring/v1',
    kind: source.kind ?? 'flow'
  };
}

// importFlow creates a durable draft from a versioned canonical source. Recognized
// legacy JSON flow exports are projected to source fields and wrapped in the v1
// envelope; canonical documents remain strict. Neither form publishes or deploys
// without the normal changeset review.
export async function importFlow(
  key: string,
  doc: unknown,
  fetcher: typeof fetch = recordingFetch,
  idempotencyKey: string = crypto.randomUUID()
): Promise<FlowImportResult> {
  const res = await fetcher('/v1/authoring/import', {
    method: 'POST',
    headers: { ...jsonHeaders(key), 'Idempotency-Key': idempotencyKey },
    body: JSON.stringify(canonicalFlowDocument(doc))
  });
  if (!res.ok) {
    return errorOrStatus(res, 'POST /v1/authoring/import');
  }
  return (await res.json()) as FlowImportResult;
}

export interface FlowBundleResult {
  flow_id: string;
  draft_id: string;
  revision: number;
  created: boolean;
  migration_report: CanonicalMigrationReport;
}

export interface FlowBundleImport {
  imports: FlowBundleResult[];
  seq: number;
}

// importFlowBundle validates the complete versioned document before creating
// one independently reviewable durable draft per flow.
export async function importFlowBundle(
  key: string,
  bundle: unknown,
  fetcher: typeof fetch = recordingFetch,
  idempotencyKey: string = crypto.randomUUID()
): Promise<FlowBundleImport> {
  if (bundle === null || typeof bundle !== 'object' || Array.isArray(bundle)) {
    throw new Error('canonical bundle import must be a JSON object');
  }
  const flows = (bundle as { flows?: unknown }).flows;
  if (!Array.isArray(flows)) {
    throw new Error('canonical bundle import requires a flows array');
  }
  const document = {
    format_version:
      (bundle as Record<string, unknown>).format_version ?? 'intraktible.authoring/v1',
    kind: (bundle as Record<string, unknown>).kind ?? 'bundle',
    flows: flows.map(canonicalFlowDocument)
  };
  const res = await fetcher('/v1/authoring/import-bundle', {
    method: 'POST',
    headers: { ...jsonHeaders(key), 'Idempotency-Key': idempotencyKey },
    body: JSON.stringify(document)
  });
  if (!res.ok) {
    return errorOrStatus(res, 'POST /v1/authoring/import-bundle');
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
  fetcher: typeof fetch = recordingFetch
): Promise<string> {
  const res = await fetcher(`/v1/decisions/${decisionId}/export?format=${format}`, {
    headers: authHeaders(key)
  });
  if (!res.ok) {
    return errorOrStatus(res, `export decision (${format}) failed`);
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

export interface DecisionEffect {
  effect_id: string;
  scope: 'live' | 'shadow';
  node_id: string;
  kind: 'connect' | 'ai' | 'predict';
  reference: string;
  provider_version?: number;
  output_key: string;
  input_hash: string;
  attempt: number;
  delivery: 'replay_safe' | 'provider_idempotent' | 'at_least_once';
  status: 'requested' | 'succeeded' | 'failed';
  output?: unknown;
  error?: string;
  requested_at: string;
  ended_at?: string;
  duration_ms?: number;
}

export interface Decision {
  decision_id: string;
  flow_id: string;
  slug: string;
  version: number;
  environment: Environment;
  variant?: Variant;
  status: DecisionStatus;
  generation?: number;
  entity_type?: string;
  entity_id?: string;
  business_reference?: string;
  correlation_id?: string;
  metadata?: Record<string, unknown>;
  control?: { timeout_ms?: number };
  data?: unknown;
  output?: unknown;
  reason_codes?: ReasonCode[];
  disposition?: Disposition;
  disposition_reason?: string;
  policy_id?: string;
  policy_version?: number;
  preapproval_id?: string; // set when served instantly from a pre-approval
  experiment_id?: string;
  experiment_cohort?: number;
  experiment_arm?: string;
  experiment_arm_name?: string;
  experiment_subject_hash?: string;
  case_id?: string; // set when the decision routed to manual_review and opened a case
  human_reviewed?: boolean; // set when a suspended decision was resumed by a person
  error?: string;
  recovery_attempt?: number;
  recovery_owner?: string;
  recovery_lease_until?: string;
  recovery_after?: string;
  last_recovery_error?: string;
  nodes?: NodeRecord[];
  effects?: DecisionEffect[];
  started_at: string;
  ended_at?: string;
  duration_ms?: number;
}

export async function listDecisions(
  key: string,
  fetcher: typeof fetch = recordingFetch
): Promise<Decision[]> {
  const res = await fetcher('/v1/decisions', { headers: authHeaders(key) });
  if (!res.ok) {
    return errorOrStatus(res, `GET /v1/decisions failed`);
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
  entity_type?: string;
  entity_id?: string;
  business_reference?: string;
  correlation_id?: string;
  metadata?: Record<string, string | number | boolean>;
  q?: string; // decision id/reference/correlation/entity search (substring)
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

export interface DecisionExecutionAttention {
  decision_id: string;
  slug: string;
  status: 'running' | 'retrying' | 'abandoned';
  recovery_attempt?: number;
  recovery_owner?: string;
  last_recovery_error?: string;
}

export interface DecisionExecutionSummary {
  total: number;
  running: number;
  retrying: number;
  suspended: number;
  completed: number;
  failed: number;
  abandoned: number;
  recovery_attempts: number;
  attention: DecisionExecutionAttention[];
}

export async function getDecisionExecutionSummary(
  key: string,
  fetcher: typeof fetch = recordingFetch
): Promise<DecisionExecutionSummary> {
  const res = await fetcher('/v1/decisions/summary', { headers: authHeaders(key) });
  if (!res.ok) return errorOrStatus(res, 'GET /v1/decisions/summary failed');
  return (await res.json()) as DecisionExecutionSummary;
}

// listDecisionsPage is the filtered/paginated decisions query backing the list UI.
export async function listDecisionsPage(
  key: string,
  filter: DecisionFilter = {},
  fetcher: typeof fetch = recordingFetch
): Promise<DecisionPage> {
  const qs = new URLSearchParams();
  for (const [k, v] of Object.entries(filter)) {
    if (k === 'metadata' && v && typeof v === 'object') {
      for (const [field, value] of Object.entries(v)) {
        qs.set(`metadata[${field}]`, String(value));
      }
    } else if (v !== undefined && v !== '' && v !== null) {
      qs.set(k, String(v));
    }
  }
  const url = `/v1/decisions${qs.toString() ? `?${qs}` : ''}`;
  const res = await fetcher(url, { headers: authHeaders(key) });
  if (!res.ok) {
    return errorOrStatus(res, `GET /v1/decisions failed`);
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
  fetcher: typeof fetch = recordingFetch
): Promise<Decision> {
  const res = await fetcher(`/v1/decisions/${id}`, { headers: authHeaders(key) });
  if (!res.ok) {
    return errorOrStatus(res, `GET /v1/decisions/${id} failed`);
  }
  return (await res.json()) as Decision;
}

// resumeDecision un-pauses a decision suspended at a durable human task, injecting the
// reviewer's outcome so the flow runs on to completion.
export async function resumeDecision(
  key: string,
  id: string,
  outcome: Record<string, unknown>,
  fetcher: typeof fetch = recordingFetch
): Promise<{ decision_id: string; status: RunStatus; disposition?: Disposition; seq: number }> {
  const res = await fetcher(`/v1/decisions/${id}/resume`, {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify({ outcome })
  });
  if (!res.ok) {
    return errorOrStatus(res, `POST /v1/decisions/${id}/resume failed`);
  }
  return (await res.json()) as {
    decision_id: string;
    status: RunStatus;
    disposition?: Disposition;
    seq: number;
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
  fetcher: typeof fetch = recordingFetch
): Promise<FlowMetrics> {
  const res = await fetcher(`/v1/flows/${flowId}/metrics`, { headers: authHeaders(key) });
  if (!res.ok) {
    return errorOrStatus(res, `GET /v1/flows/${flowId}/metrics failed`);
  }
  return (await res.json()) as FlowMetrics;
}

export interface SLOConfig {
  success_target: number; // fraction in [0,1]
  latency_target_ms: number; // 0 = no latency objective
  window_days?: number; // rolling window; 0/undefined = all-time
}

export interface SLOAttainment {
  window_days: number; // rolling window measured over (0 = all-time)
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
  fetcher: typeof fetch = recordingFetch
): Promise<SLOResponse> {
  const res = await fetcher(`/v1/flows/${flowId}/slo`, { headers: authHeaders(key) });
  if (!res.ok) {
    return errorOrStatus(res, `GET /v1/flows/${flowId}/slo failed`);
  }
  return (await res.json()) as SLOResponse;
}

export async function putFlowSLO(
  key: string,
  flowId: string,
  slo: SLOConfig,
  fetcher: typeof fetch = recordingFetch
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
  fetcher: typeof fetch = recordingFetch
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
  fetcher: typeof fetch = recordingFetch
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
  fetcher: typeof fetch = recordingFetch
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
  fetcher: typeof fetch = recordingFetch
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
  fetcher: typeof fetch = recordingFetch
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
  fetcher: typeof fetch = recordingFetch
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
  fetcher: typeof fetch = recordingFetch
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
  fetcher: typeof fetch = recordingFetch
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
  fetcher: typeof fetch = recordingFetch
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

export async function listWebhooks(
  key: string,
  fetcher: typeof fetch = recordingFetch
): Promise<Webhook[]> {
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
  fetcher: typeof fetch = recordingFetch
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
  fetcher: typeof fetch = recordingFetch
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
  approved_version: number;
  approved_by?: string;
  approved_at?: string;
  pending?: {
    request_id: string;
    version: number;
    requested_by: string;
    requested_at: string;
  };
  updated_at?: string;
}

export async function listPolicies(
  key: string,
  fetcher: typeof fetch = recordingFetch
): Promise<Policy[]> {
  const res = await fetcher('/v1/policies', { headers: authHeaders(key) });
  if (!res.ok) {
    return errorOrStatus(res, 'GET /v1/policies');
  }
  return ((await res.json()) as { policies: Policy[] }).policies ?? [];
}

export async function createPolicy(
  key: string,
  body: { name: string; flow_slug: string },
  fetcher: typeof fetch = recordingFetch
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
  fetcher: typeof fetch = recordingFetch
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

export async function requestPolicyApproval(
  key: string,
  policyId: string,
  fetcher: typeof fetch = recordingFetch
): Promise<{ request_id: string }> {
  const res = await fetcher(`/v1/policies/${encodeURIComponent(policyId)}/approval-request`, {
    method: 'POST',
    headers: jsonHeaders(key)
  });
  if (!res.ok) {
    return errorOrStatus(res, 'request policy approval');
  }
  return (await res.json()) as { request_id: string };
}

export async function decidePolicyApproval(
  key: string,
  policyId: string,
  requestId: string,
  approve: boolean,
  reason: string,
  fetcher: typeof fetch = recordingFetch
): Promise<void> {
  const action = approve ? 'approve' : 'reject';
  const res = await fetcher(`/v1/policies/${encodeURIComponent(policyId)}/${action}`, {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify({ request_id: requestId, reason })
  });
  if (!res.ok) {
    return errorOrStatus(res, `${action} policy`);
  }
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
  fetcher: typeof fetch = recordingFetch
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
  fetcher: typeof fetch = recordingFetch
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
  fetcher: typeof fetch = recordingFetch
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
  fetcher: typeof fetch = recordingFetch
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
  fetcher: typeof fetch = recordingFetch
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
  fetcher: typeof fetch = recordingFetch
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
  fetcher: typeof fetch = recordingFetch
): Promise<{ version: number; etag: string; published?: boolean }> {
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
  return (await res.json()) as { version: number; etag: string; published?: boolean };
}

export type DraftState = 'active' | 'archived';
export type ChangeSetState =
  | 'draft'
  | 'in_review'
  | 'changes_requested'
  | 'approved'
  | 'publishing'
  | 'published';

export interface AuthoringDraft {
  draft_id: string;
  flow_id: string;
  base_version: number;
  revision: number;
  state: DraftState;
  title: string;
  graph: FlowGraph;
  input_schema?: unknown;
  created_by: string;
  created_at: string;
  updated_by: string;
  updated_at: string;
  archived_by?: string;
  archived_at?: string;
}

export interface DraftRevision {
  draft_id: string;
  flow_id: string;
  base_version: number;
  revision: number;
  title: string;
  graph: FlowGraph;
  input_schema?: unknown;
  actor: string;
  at: string;
  rebased?: boolean;
}

export interface DraftPresence {
  draft_id: string;
  actor: string;
  display_name?: string;
  revision: number;
  selected_id?: string;
  renewed_at: string;
  expires_at: string;
}

export interface ChangeSetCheck {
  name: string;
  status: 'passed' | 'failed';
  evidence?: string;
  recorded_by: string;
  recorded_at: string;
}

export interface ChangeSetReview {
  decision: 'approve' | 'request_changes';
  reason?: string;
  actor: string;
  at: string;
}

export interface ChangeSet {
  changeset_id: string;
  flow_id: string;
  base_version: number;
  draft_id: string;
  draft_revision: number;
  title: string;
  rationale?: string;
  state: ChangeSetState;
  source_graph: FlowGraph;
  graph: FlowGraph;
  input_schema?: unknown;
  dependencies?: FlowDependency[];
  proposed_etag: string;
  required_checks?: string[];
  reviewers?: string[];
  checks?: Record<string, ChangeSetCheck>;
  review?: ChangeSetReview;
  created_by: string;
  created_at: string;
  submitted_by?: string;
  submitted_at?: string;
  updated_at: string;
  published_by?: string;
  published_at?: string;
  published_version?: number;
  published_etag?: string;
}

export interface SemanticDifference {
  kind:
    | 'node_added'
    | 'node_removed'
    | 'node_changed'
    | 'edge_added'
    | 'edge_removed'
    | 'input_schema_changed'
    | 'dependency_added'
    | 'dependency_removed'
    | 'dependency_changed';
  object: string;
  before?: unknown;
  after?: unknown;
}

export interface ReusableComponentVersion {
  version: number;
  etag: string;
  source_graph: FlowGraph;
  graph: FlowGraph;
  input_schema?: unknown;
  output_schema?: unknown;
  dependencies?: FlowDependency[];
  compatibility: ComponentCompatibilityReport;
  breaking_change_reason?: string;
  published_by: string;
  published_at: string;
}

export interface ComponentCompatibilityIssue {
  path: string;
  code: string;
  message: string;
}

export interface ComponentCompatibilityReport {
  from_version?: number;
  to_version: number;
  status: 'initial' | 'compatible' | 'incompatible';
  issues?: ComponentCompatibilityIssue[];
}

export interface ComponentCompatibilityAssessment {
  component_id: string;
  report: ComponentCompatibilityReport;
  consumers: ComponentConsumer[];
  upgradeable: boolean;
}

export interface ComponentUpgradeDraft {
  flow_id: string;
  draft_id: string;
  base_version: number;
  revision: number;
}

export interface ReusableComponent {
  component_id: string;
  slug: string;
  name: string;
  description?: string;
  latest: number;
  versions?: ReusableComponentVersion[];
  retired: boolean;
  created_by: string;
  created_at: string;
  updated_at: string;
}

export interface ComponentConsumer {
  component_id: string;
  component_version: number;
  component_etag: string;
  consumer_kind: 'flow' | 'component';
  consumer_id: string;
  consumer_version: number;
}

export async function createDraft(
  key: string,
  input: {
    flow_id: string;
    base_version: number;
    title: string;
    graph: FlowGraph;
    input_schema?: unknown;
  },
  fetcher: typeof fetch = recordingFetch,
  idempotencyKey: string = crypto.randomUUID()
): Promise<{ draft_id: string; revision: number; event_id: string; seq: number }> {
  const res = await fetcher('/v1/authoring/drafts', {
    method: 'POST',
    headers: idempotentJSONHeaders(key, idempotencyKey),
    body: JSON.stringify(input)
  });
  if (!res.ok) return errorOrStatus(res, 'POST /v1/authoring/drafts');
  return (await res.json()) as {
    draft_id: string;
    revision: number;
    event_id: string;
    seq: number;
  };
}

export async function listDrafts(
  key: string,
  flowId = '',
  fetcher: typeof fetch = recordingFetch
): Promise<AuthoringDraft[]> {
  const query = flowId ? `?flow_id=${encodeURIComponent(flowId)}` : '';
  const res = await fetcher(`/v1/authoring/drafts${query}`, { headers: authHeaders(key) });
  if (!res.ok) return errorOrStatus(res, 'GET /v1/authoring/drafts');
  return ((await res.json()) as { drafts?: AuthoringDraft[] }).drafts ?? [];
}

export async function getDraft(
  key: string,
  draftId: string,
  fetcher: typeof fetch = recordingFetch
): Promise<AuthoringDraft> {
  const res = await fetcher(`/v1/authoring/drafts/${encodeURIComponent(draftId)}`, {
    headers: authHeaders(key)
  });
  if (!res.ok) return errorOrStatus(res, 'GET /v1/authoring/drafts/{draft_id}');
  return (await res.json()) as AuthoringDraft;
}

export async function saveDraft(
  key: string,
  draftId: string,
  input: {
    expected_revision: number;
    title: string;
    graph: FlowGraph;
    input_schema?: unknown;
  },
  fetcher: typeof fetch = recordingFetch
): Promise<{ draft_id: string; revision: number; event_id: string; seq: number }> {
  const res = await fetcher(`/v1/authoring/drafts/${encodeURIComponent(draftId)}`, {
    method: 'PUT',
    headers: jsonHeaders(key),
    body: JSON.stringify(input)
  });
  if (res.status === 409) {
    const body = (await res.json()) as { error: string; current: AuthoringDraft };
    throw new DraftConflictError(body.error, body.current);
  }
  if (!res.ok) return errorOrStatus(res, 'PUT /v1/authoring/drafts/{draft_id}');
  return (await res.json()) as {
    draft_id: string;
    revision: number;
    event_id: string;
    seq: number;
  };
}

export async function rebaseDraft(
  key: string,
  draftId: string,
  input: {
    expected_revision: number;
    base_version: number;
    title: string;
    graph: FlowGraph;
    input_schema?: unknown;
  },
  fetcher: typeof fetch = recordingFetch
): Promise<{ draft_id: string; revision: number; event_id: string; seq: number }> {
  const res = await fetcher(`/v1/authoring/drafts/${encodeURIComponent(draftId)}/rebase`, {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify(input)
  });
  if (res.status === 409) {
    const body = (await res.json()) as { error: string; current: AuthoringDraft };
    throw new DraftConflictError(body.error, body.current);
  }
  if (!res.ok) return errorOrStatus(res, 'POST /v1/authoring/drafts/{draft_id}/rebase');
  return (await res.json()) as {
    draft_id: string;
    revision: number;
    event_id: string;
    seq: number;
  };
}

export async function listDraftRevisions(
  key: string,
  draftId: string,
  fetcher: typeof fetch = recordingFetch
): Promise<DraftRevision[]> {
  const res = await fetcher(`/v1/authoring/drafts/${encodeURIComponent(draftId)}/revisions`, {
    headers: authHeaders(key)
  });
  if (!res.ok) return errorOrStatus(res, 'GET /v1/authoring/drafts/{draft_id}/revisions');
  return ((await res.json()) as { revisions?: DraftRevision[] }).revisions ?? [];
}

export async function archiveDraft(
  key: string,
  draftId: string,
  fetcher: typeof fetch = recordingFetch
): Promise<void> {
  const res = await fetcher(`/v1/authoring/drafts/${encodeURIComponent(draftId)}`, {
    method: 'DELETE',
    headers: authHeaders(key)
  });
  if (!res.ok) return errorOrStatus(res, 'DELETE /v1/authoring/drafts/{draft_id}');
}

export async function renewDraftPresence(
  key: string,
  draftId: string,
  input: { display_name?: string; revision: number; selected_id?: string; ttl_seconds?: number },
  fetcher: typeof fetch = recordingFetch
): Promise<DraftPresence> {
  const res = await fetcher(`/v1/authoring/drafts/${encodeURIComponent(draftId)}/presence`, {
    method: 'PUT',
    headers: jsonHeaders(key),
    body: JSON.stringify(input)
  });
  if (!res.ok) return errorOrStatus(res, 'PUT /v1/authoring/drafts/{draft_id}/presence');
  return (await res.json()) as DraftPresence;
}

export async function listDraftPresence(
  key: string,
  draftId: string,
  fetcher: typeof fetch = recordingFetch
): Promise<DraftPresence[]> {
  const res = await fetcher(`/v1/authoring/drafts/${encodeURIComponent(draftId)}/presence`, {
    headers: authHeaders(key)
  });
  if (!res.ok) return errorOrStatus(res, 'GET /v1/authoring/drafts/{draft_id}/presence');
  return ((await res.json()) as { presence?: DraftPresence[] }).presence ?? [];
}

export async function leaveDraftPresence(
  key: string,
  draftId: string,
  fetcher: typeof fetch = recordingFetch
): Promise<void> {
  const res = await fetcher(`/v1/authoring/drafts/${encodeURIComponent(draftId)}/presence`, {
    method: 'DELETE',
    headers: authHeaders(key)
  });
  if (!res.ok) return errorOrStatus(res, 'DELETE /v1/authoring/drafts/{draft_id}/presence');
}

export async function createChangeSet(
  key: string,
  input: {
    draft_id: string;
    draft_revision: number;
    title: string;
    rationale?: string;
    required_checks?: string[];
    reviewers?: string[];
  },
  fetcher: typeof fetch = recordingFetch,
  idempotencyKey: string = crypto.randomUUID()
): Promise<{ changeset_id: string; event_id: string; seq: number }> {
  const res = await fetcher('/v1/authoring/changesets', {
    method: 'POST',
    headers: idempotentJSONHeaders(key, idempotencyKey),
    body: JSON.stringify(input)
  });
  if (!res.ok) return errorOrStatus(res, 'POST /v1/authoring/changesets');
  return (await res.json()) as { changeset_id: string; event_id: string; seq: number };
}

export async function listChangeSets(
  key: string,
  flowId = '',
  fetcher: typeof fetch = recordingFetch
): Promise<ChangeSet[]> {
  const query = flowId ? `?flow_id=${encodeURIComponent(flowId)}` : '';
  const res = await fetcher(`/v1/authoring/changesets${query}`, {
    headers: authHeaders(key)
  });
  if (!res.ok) return errorOrStatus(res, 'GET /v1/authoring/changesets');
  return ((await res.json()) as { changesets?: ChangeSet[] }).changesets ?? [];
}

export async function getChangeSetDiff(
  key: string,
  changeSetId: string,
  fetcher: typeof fetch = recordingFetch
): Promise<SemanticDifference[]> {
  const res = await fetcher(`/v1/authoring/changesets/${encodeURIComponent(changeSetId)}/diff`, {
    headers: authHeaders(key)
  });
  if (!res.ok) return errorOrStatus(res, 'GET /v1/authoring/changesets/{changeset_id}/diff');
  return ((await res.json()) as { differences?: SemanticDifference[] }).differences ?? [];
}

export async function checkChangeSet(
  key: string,
  changeSetId: string,
  name = 'flow-validation',
  status: 'passed' | 'failed' = 'passed',
  evidence = '',
  fetcher: typeof fetch = recordingFetch
): Promise<EventAck> {
  const res = await fetcher(`/v1/authoring/changesets/${encodeURIComponent(changeSetId)}/checks`, {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify({ name, status, evidence: evidence || undefined })
  });
  if (!res.ok) return errorOrStatus(res, 'POST /v1/authoring/changesets/{id}/checks');
  return (await res.json()) as EventAck;
}

async function changeSetAction(
  key: string,
  changeSetId: string,
  action: 'submit' | 'publish',
  fetcher: typeof fetch
): Promise<Record<string, unknown>> {
  const res = await fetcher(
    `/v1/authoring/changesets/${encodeURIComponent(changeSetId)}/${action}`,
    { method: 'POST', headers: jsonHeaders(key), body: '{}' }
  );
  if (!res.ok) return errorOrStatus(res, `POST changeset ${action}`);
  return (await res.json()) as Record<string, unknown>;
}

export async function submitChangeSet(
  key: string,
  changeSetId: string,
  fetcher: typeof fetch = recordingFetch
): Promise<EventAck> {
  return (await changeSetAction(key, changeSetId, 'submit', fetcher)) as unknown as EventAck;
}

export async function reviewChangeSet(
  key: string,
  changeSetId: string,
  decision: 'approve' | 'request_changes',
  reason = '',
  fetcher: typeof fetch = recordingFetch
): Promise<EventAck> {
  const res = await fetcher(`/v1/authoring/changesets/${encodeURIComponent(changeSetId)}/review`, {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify({ decision, reason })
  });
  if (!res.ok) return errorOrStatus(res, 'POST /v1/authoring/changesets/{id}/review');
  return (await res.json()) as EventAck;
}

export async function publishChangeSet(
  key: string,
  changeSetId: string,
  fetcher: typeof fetch = recordingFetch
): Promise<{ version: number; etag: string; event_id?: string; seq?: number }> {
  return (await changeSetAction(key, changeSetId, 'publish', fetcher)) as {
    version: number;
    etag: string;
    event_id?: string;
    seq?: number;
  };
}

export async function listReusableComponents(
  key: string,
  fetcher: typeof fetch = recordingFetch
): Promise<ReusableComponent[]> {
  const res = await fetcher('/v1/authoring/components', { headers: authHeaders(key) });
  if (!res.ok) return errorOrStatus(res, 'GET /v1/authoring/components');
  return ((await res.json()) as { components?: ReusableComponent[] }).components ?? [];
}

export async function createReusableComponent(
  key: string,
  input: { slug: string; name: string; description?: string },
  fetcher: typeof fetch = recordingFetch,
  idempotencyKey: string = crypto.randomUUID()
): Promise<{ component_id: string; event_id: string; seq: number }> {
  const res = await fetcher('/v1/authoring/components', {
    method: 'POST',
    headers: idempotentJSONHeaders(key, idempotencyKey),
    body: JSON.stringify(input)
  });
  if (!res.ok) return errorOrStatus(res, 'POST /v1/authoring/components');
  return (await res.json()) as { component_id: string; event_id: string; seq: number };
}

export async function getReusableComponent(
  key: string,
  componentId: string,
  fetcher: typeof fetch = recordingFetch
): Promise<ReusableComponent> {
  const res = await fetcher(`/v1/authoring/components/${encodeURIComponent(componentId)}`, {
    headers: authHeaders(key)
  });
  if (!res.ok) return errorOrStatus(res, 'GET /v1/authoring/components/{id}');
  return (await res.json()) as ReusableComponent;
}

export async function publishReusableComponent(
  key: string,
  componentId: string,
  input: {
    graph: FlowGraph;
    input_schema?: unknown;
    output_schema?: unknown;
    allow_breaking?: boolean;
    breaking_change_reason?: string;
  },
  fetcher: typeof fetch = recordingFetch,
  idempotencyKey: string = crypto.randomUUID()
): Promise<{ version: number; etag: string; event_id: string; seq: number }> {
  const res = await fetcher(
    `/v1/authoring/components/${encodeURIComponent(componentId)}/versions`,
    {
      method: 'POST',
      headers: idempotentJSONHeaders(key, idempotencyKey),
      body: JSON.stringify(input)
    }
  );
  if (!res.ok) return errorOrStatus(res, 'POST /v1/authoring/components/{id}/versions');
  return (await res.json()) as {
    version: number;
    etag: string;
    event_id: string;
    seq: number;
  };
}

export async function assessComponentCompatibility(
  key: string,
  componentId: string,
  fromVersion: number,
  toVersion: number,
  fetcher: typeof fetch = recordingFetch
): Promise<ComponentCompatibilityAssessment> {
  const query = new URLSearchParams({
    from_version: String(fromVersion),
    to_version: String(toVersion)
  });
  const res = await fetcher(
    `/v1/authoring/components/${encodeURIComponent(componentId)}/compatibility?${query}`,
    { headers: authHeaders(key) }
  );
  if (!res.ok) return errorOrStatus(res, 'GET reusable component compatibility');
  return (await res.json()) as ComponentCompatibilityAssessment;
}

export async function createComponentUpgradeDrafts(
  key: string,
  componentId: string,
  input: { from_version: number; to_version: number; flow_ids: string[]; title?: string },
  fetcher: typeof fetch = recordingFetch,
  idempotencyKey: string = crypto.randomUUID()
): Promise<{ drafts: ComponentUpgradeDraft[]; seq: number }> {
  const res = await fetcher(
    `/v1/authoring/components/${encodeURIComponent(componentId)}/upgrade-drafts`,
    {
      method: 'POST',
      headers: idempotentJSONHeaders(key, idempotencyKey),
      body: JSON.stringify(input)
    }
  );
  if (!res.ok) return errorOrStatus(res, 'POST reusable component upgrade drafts');
  return (await res.json()) as { drafts: ComponentUpgradeDraft[]; seq: number };
}

export async function listComponentConsumers(
  key: string,
  componentId: string,
  version: number,
  fetcher: typeof fetch = recordingFetch
): Promise<ComponentConsumer[]> {
  const res = await fetcher(
    `/v1/authoring/components/${encodeURIComponent(componentId)}/versions/${version}/consumers`,
    { headers: authHeaders(key) }
  );
  if (!res.ok) return errorOrStatus(res, 'GET reusable component consumers');
  return ((await res.json()) as { consumers?: ComponentConsumer[] }).consumers ?? [];
}

export async function retireReusableComponent(
  key: string,
  componentId: string,
  fetcher: typeof fetch = recordingFetch
): Promise<EventAck> {
  const res = await fetcher(`/v1/authoring/components/${encodeURIComponent(componentId)}`, {
    method: 'DELETE',
    headers: authHeaders(key)
  });
  if (!res.ok) return errorOrStatus(res, 'DELETE /v1/authoring/components/{id}');
  return (await res.json()) as EventAck;
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
  fetcher: typeof fetch = recordingFetch
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
  fetcher: typeof fetch = recordingFetch
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
  fetcher: typeof fetch = recordingFetch
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
  fetcher: typeof fetch = recordingFetch
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
  fetcher: typeof fetch = recordingFetch
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
  fetcher: typeof fetch = recordingFetch
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
  fetcher: typeof fetch = recordingFetch
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
  fetcher: typeof fetch = recordingFetch
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
  fetcher: typeof fetch = recordingFetch
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
  fetcher: typeof fetch = recordingFetch
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

export interface ShadowComparisonSample {
  decision_id: string;
  experiment_id?: string;
  experiment_cohort?: number;
  experiment_arm?: string;
  live_status: string;
  shadow_status?: string;
  live_disposition?: string;
  shadow_disposition?: string;
  live_code?: string;
  shadow_code?: string;
  live_reason?: string;
  shadow_reason?: string;
  changed_fields?: string[];
  error?: string;
}

export interface EnvShadow {
  environment: string;
  experiment_id?: string;
  experiment_cohort?: number;
  experiment_arm?: string;
  live_version: number;
  shadow_version: number;
  match_basis: 'output' | 'policy';
  policy_id?: string;
  policy_version?: number;
  total: number;
  matched: number;
  diverged: number;
  errored: number;
  sample_diverged?: string[];
  samples?: ShadowComparisonSample[];
}

export interface ShadowState {
  shadows: Record<string, number>;
  report: Record<string, EnvShadow>;
  cohorts: Record<string, EnvShadow>;
}

// getShadow returns the per-environment shadow assignments and the divergence
// report (how often a shadow version's outcome differs from the live decision).
export async function getShadow(
  key: string,
  flowId: string,
  fetcher: typeof fetch = recordingFetch
): Promise<ShadowState> {
  const res = await fetcher(`/v1/flows/${flowId}/shadow`, { headers: authHeaders(key) });
  if (!res.ok) {
    return errorOrStatus(res, 'GET shadow');
  }
  const body = (await res.json()) as Partial<ShadowState>;
  return { shadows: body.shadows ?? {}, report: body.report ?? {}, cohorts: body.cohorts ?? {} };
}

// setShadow assigns (version 0 clears) the shadow version for an environment.
export async function setShadow(
  key: string,
  flowId: string,
  environment: string,
  version: number,
  fetcher: typeof fetch = recordingFetch
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
  body: DeployInput & { at?: string; until?: string },
  fetcher: typeof fetch = recordingFetch
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
  fetcher: typeof fetch = recordingFetch
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
  fetcher: typeof fetch = recordingFetch
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

export interface DecisionInvocationOptions {
  idempotencyKey?: string;
  businessReference?: string;
  correlationId?: string;
  metadata?: Record<string, unknown>;
  control?: { timeout_ms?: number };
  // Preview-only authoritative values for reached dependency namespaces.
  mockData?: Record<string, unknown>;
}

export async function decide(
  key: string,
  slug: string,
  env: string,
  data: Record<string, unknown>,
  entity?: EntityRef,
  fetcher: typeof fetch = recordingFetch,
  // preview runs the flow WITHOUT recording a decision (no history/metrics/audit) —
  // used by the builder's test run. The result then carries no decision_id.
  preview = false,
  options: DecisionInvocationOptions = {}
): Promise<DecideResult> {
  const body: Record<string, unknown> = { data };
  if (preview) body.preview = true;
  if (options.businessReference) body.business_reference = options.businessReference;
  if (options.correlationId) body.correlation_id = options.correlationId;
  if (options.metadata) body.metadata = options.metadata;
  if (options.control) body.control = options.control;
  if (options.mockData) body.mock_data = options.mockData;
  if (entity?.type && entity?.id) {
    body.entity_type = entity.type;
    body.entity_id = entity.id;
  }
  const headers = jsonHeaders(key);
  if (options.idempotencyKey) headers['Idempotency-Key'] = options.idempotencyKey;
  const res = await fetcher(`/v1/flows/${slug}/${env}/decide`, {
    method: 'POST',
    headers,
    body: JSON.stringify(body)
  });
  if (!res.ok) {
    return errorOrStatus(res, `POST decide failed`);
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
  seq?: number;
}

export interface BatchReport {
  total: number;
  completed: number;
  failed: number;
  rejected: number;
  results: BatchResult[];
  seq?: number;
}

// batchDecide runs a dataset of inputs through a published flow — each row a real
// recorded decision (appears in history, metrics, audit), unlike a backtest.
export async function batchDecide(
  key: string,
  slug: string,
  env: string,
  dataset: Record<string, unknown>[],
  entity?: EntityRef,
  fetcher: typeof fetch = recordingFetch
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
  seq?: number;
}

export interface PreApproveBatchReport {
  total: number;
  granted: number;
  skipped: number;
  failed: number;
  rejected: number;
  results: PreApproveResult[];
  seq?: number;
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
  fetcher: typeof fetch = recordingFetch
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
  case_type_version: number;
  status: string;
  priority: 'low' | 'normal' | 'high' | 'critical';
  queue?: string;
  routing_explanation?: string;
  assignee?: string;
  jurisdiction?: string;
  subject?: string;
  subject_governance?: {
    subject: string;
    retained: boolean;
    retain_until?: string;
    legal_hold?: boolean;
    erased?: boolean;
  };
  sla_days: number;
  deadline?: string;
  days_left: number;
  sla_state?: SLAState;
  sla_breached?: boolean;
  sla_escalation_status?: 'pending' | 'delivered' | 'no_channel' | 'permanent_failure';
  sla_escalation_round?: number;
  sla_delivery_attempts: Array<{
    round: number;
    attempt: number;
    outcome: 'delivered' | 'retryable' | 'no_channel' | 'permanent_failure';
    at: string;
    actor: string;
  }>;
  source_decision_id?: string;
  context?: unknown;
  disposition?: string;
  reason_code?: string;
  disposition_note?: string;
  disposition_override?: boolean;
  evidence: CaseEvidence[];
  attachments: CaseAttachment[];
  qa?: CaseQA;
  notes: CaseNote[];
  audit: CaseAudit[];
  created_at: string;
  first_action_at?: string;
  resolved_at?: string;
  updated_at: string;
}

export interface CaseEvidence {
  evidence_id: string;
  requirement?: string;
  kind: string;
  subject_type: string;
  subject_id: string;
  label: string;
  content_hash?: string;
  linked_seq: number;
}

export interface CaseAttachment {
  attachment_id: string;
  name: string;
  media_type: string;
  size: number;
  sha256: string;
  storage_ref?: string;
  requirement?: string;
  subject?: string;
  lawful_basis?: string;
  retain_until?: string;
  legal_hold?: boolean;
  erased?: boolean;
  registered_seq: number;
  access_count: number;
}

export interface CaseQA {
  sample_id: string;
  primary_actor: string;
  reviewer: string;
  status: string;
  disposition?: string;
  reason_code?: string;
  agreement?: boolean;
  override?: boolean;
  validated?: boolean;
  disputed?: boolean;
  effective_disposition?: string;
  effective_reason_code?: string;
  note?: string;
  feedback?: string;
}

export interface CaseFieldDefinition {
  key: string;
  label: string;
  kind: 'string' | 'number' | 'boolean' | 'object' | 'array';
  required?: boolean;
  pii?: boolean;
  read_by?: string[];
}

export interface CaseTypeDefinition {
  key: string;
  name: string;
  description?: string;
  initial_state: string;
  fields: CaseFieldDefinition[];
  transitions: Array<{ from: string; to: string; roles?: string[] }>;
  dispositions: Array<{
    key: string;
    label: string;
    reason_codes: string[];
    terminal_state: string;
    requires_second_review?: boolean;
  }>;
  priorities: Array<'low' | 'normal' | 'high' | 'critical'>;
  service_calendar: {
    timezone: string;
    weekdays: number[];
    start_hour: number;
    end_hour: number;
    holidays?: string[];
    sla_hours: number;
    escalation_hours?: number;
  };
  evidence_requirements: Array<{
    key: string;
    label: string;
    kinds: string[];
    required?: boolean;
  }>;
  layouts: Array<{ role: string; sections: string[]; editable?: string[] }>;
  assist_automations?: CaseAssistAutomation[];
}

export interface CaseAssistAutomation {
  key: string;
  kind: AgentAssistKind;
  template_id: string;
  environment: Environment;
  evidence_requirements: string[];
}

export interface CaseTypeView {
  key: string;
  version: number;
  definition: CaseTypeDefinition;
  published_by: string;
  published_at: string;
}

export interface CaseQueueDefinition {
  key: string;
  name: string;
  order?: number;
  case_types?: string[];
  priorities?: Array<'low' | 'normal' | 'high' | 'critical'>;
  jurisdictions?: string[];
  required_skills?: string[];
  capacity: number;
  escalation_queue?: string;
  min_age_hours?: number;
  max_age_hours?: number;
  conflict_context_keys?: string[];
  context_equals?: Record<string, string>;
  assist_automations?: CaseAssistAutomation[];
}

export interface CaseReviewerProfile {
  actor: string;
  skills: string[];
  jurisdictions: string[];
  capacity: number;
  active: boolean;
  conflicts?: string[];
}

export interface CaseAnalytics {
  total: number;
  open: number;
  completed: number;
  unassigned: number;
  unrouted: number;
  due_soon: number;
  overdue: number;
  sla_breached: number;
  average_first_action_seconds: number;
  average_resolution_seconds: number;
  ageing: Record<string, number>;
  by_status: Record<string, number>;
  by_priority: Record<string, number>;
  workloads: Array<{ actor: string; open: number; capacity: number; utilization: number }>;
  queues: Array<{
    queue: string;
    open: number;
    capacity: number;
    utilization: number;
    oldest_age_hours: number;
  }>;
  qa_selected: number;
  qa_completed: number;
  qa_disagreements: number;
  qa_overrides: number;
}

export interface CaseBulkResult {
  batch_id: string;
  operation: 'assign' | 'status' | 'priority' | 'disposition';
  status: string;
  succeeded: number;
  failed: number;
  items: Array<{ case_id: string; success: boolean; error?: string }>;
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
  queue?: string;
  priority?: string;
  jurisdiction?: string;
  subject?: string;
  q?: string;
  sla_state?: string;
}

export interface CaseSavedView {
  view_id: string;
  name: string;
  owner: string;
  query: CaseFilter;
  updated_at: string;
}

export interface CaseDuplicateGroup {
  key: string;
  reason: string;
  case_ids: string[];
}

function caseQuery(filter: CaseFilter): string {
  const q = new URLSearchParams();
  if (filter.status) q.set('status', filter.status);
  if (filter.type) q.set('type', filter.type);
  if (filter.assignee) q.set('assignee', filter.assignee);
  if (filter.queue) q.set('queue', filter.queue);
  if (filter.priority) q.set('priority', filter.priority);
  if (filter.jurisdiction) q.set('jurisdiction', filter.jurisdiction);
  if (filter.subject) q.set('subject', filter.subject);
  if (filter.q) q.set('q', filter.q);
  if (filter.sla_state) q.set('sla_state', filter.sla_state);
  const qs = q.toString();
  return qs ? '?' + qs : '';
}

// ApiError carries the HTTP status alongside the backend's message so a caller can
// branch on it (e.g. show a "not found" copy only for a real 404, not any failure).
export class ApiError extends Error {
  constructor(
    message: string,
    readonly status: number
  ) {
    super(message);
    this.name = 'ApiError';
  }
}

// DraftConflictError carries the authoritative remote snapshot returned by a
// failed optimistic save. The builder can compare base/local/remote and requires
// an explicit user choice; it never silently retries as last-write-wins.
export class DraftConflictError extends ApiError {
  constructor(
    message: string,
    readonly current: AuthoringDraft
  ) {
    super(message, 409);
    this.name = 'DraftConflictError';
  }
}

// whenPermitted resolves an admin-gated read to `absent` when the caller is not
// allowed to make it, and lets every other failure through.
//
// A 403 is a fact about the viewer's role, not a failure: a page that composes
// several sections of differing sensitivity should render the ones this viewer may
// see rather than collapsing entirely. Nothing else qualifies. Catching everything
// — which these call sites used to do with `.catch(() => [])` — renders a 500, a
// dropped connection, or a bad gateway as an empty section, telling an operator
// "there is nothing here" when the truth is "we could not find out". On a
// compliance surface (pending adverse actions, legal holds, consent records) those
// two readings are not close to equivalent.
export async function whenPermitted<T>(read: Promise<T>, absent: T): Promise<T> {
  try {
    return await read;
  } catch (e) {
    if (e instanceof ApiError && e.status === 403) return absent;
    throw e;
  }
}

// errorOrStatus throws the backend's error message (or a status fallback).
async function errorOrStatus(res: Response, label: string): Promise<never> {
  const body = (await res.json().catch(() => ({}))) as { error?: string };
  throw new ApiError(body.error ?? `${label}: ${res.status}`, res.status);
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
  fetcher: typeof fetch = recordingFetch
): Promise<AuditEntry[]> {
  return (await listAuditPage(key, filter, fetcher)).entries;
}

// listAuditPage is the filtered/paginated audit read backing the Audit UI.
export async function listAuditPage(
  key: string,
  filter: AuditFilter = {},
  fetcher: typeof fetch = recordingFetch
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
  fetcher: typeof fetch = recordingFetch
): Promise<string> {
  const res = await fetcher(auditExportUrl(filter), { headers: authHeaders(key) });
  if (!res.ok) {
    return errorOrStatus(res, `export audit csv failed`);
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
  resolved: boolean;
  resolved_by?: string;
  resolved_at?: string;
}

export async function listComments(
  key: string,
  subjectType: string,
  subjectId: string,
  fetcher: typeof fetch = recordingFetch
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
  fetcher: typeof fetch = recordingFetch
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

export async function setCommentResolved(
  key: string,
  subjectType: string,
  subjectId: string,
  commentId: string,
  resolved: boolean,
  fetcher: typeof fetch = recordingFetch
): Promise<void> {
  const action = resolved ? 'resolve' : 'reopen';
  const res = await fetcher(
    `/v1/comments/${subjectType}/${encodeURIComponent(subjectId)}/${encodeURIComponent(commentId)}/${action}`,
    {
      method: 'POST',
      headers: jsonHeaders(key),
      body: '{}'
    }
  );
  if (!res.ok) {
    return errorOrStatus(res, `POST comment ${action}`);
  }
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
  fetcher: typeof fetch = recordingFetch
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
  fetcher: typeof fetch = recordingFetch
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
  fetcher: typeof fetch = recordingFetch
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
  fetcher: typeof fetch = recordingFetch
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
  fetcher: typeof fetch = recordingFetch
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
  fetcher: typeof fetch = recordingFetch
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
  fetcher: typeof fetch = recordingFetch
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
  fetcher: typeof fetch = recordingFetch
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
  fetcher: typeof fetch = recordingFetch
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
  version: number;
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

export async function recordEntity(
  key: string,
  body: { entity_type: string; entity_id: string; attributes?: Record<string, unknown> },
  fetcher: typeof fetch = recordingFetch
): Promise<EventAck> {
  const res = await fetcher('/v1/context/entities', {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify(body)
  });
  if (!res.ok) {
    return errorOrStatus(res, 'POST /v1/context/entities');
  }
  return (await res.json()) as EventAck;
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

export async function recordEntityEvent(
  key: string,
  body: {
    entity_type: string;
    entity_id: string;
    event_name: string;
    data?: Record<string, unknown>;
    occurred_at?: string;
  },
  fetcher: typeof fetch = recordingFetch
): Promise<EventAck> {
  const res = await fetcher('/v1/context/events', {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify(body)
  });
  if (!res.ok) {
    return errorOrStatus(res, 'POST /v1/context/events');
  }
  return (await res.json()) as EventAck;
}

export interface FeatureValue {
  name: string;
  value: number;
  version: number;
  event_count: number; // events that fed the value (lineage)
  cached?: boolean; // served from the materialized cache
}

export async function listConnectors(
  key: string,
  fetcher: typeof fetch = recordingFetch
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
  fetcher: typeof fetch = recordingFetch
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

export interface ConnectorFetch {
  fetch_id: string;
  connector: string;
  params?: unknown;
  response: unknown;
  seq: number;
  at: string;
}

// fetchConnector exercises the real configured source and records its response,
// exactly as a Connect node does before deterministic flow execution.
export async function fetchConnector(
  key: string,
  name: string,
  params: unknown,
  fetcher: typeof fetch = recordingFetch
): Promise<{ fetch_id: string; response: unknown; event_id: string; seq: number }> {
  const res = await fetcher(`/v1/context/connectors/${encodeURIComponent(name)}/fetch`, {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify({ params })
  });
  if (!res.ok) {
    return errorOrStatus(res, `POST connector ${name} fetch`);
  }
  return (await res.json()) as {
    fetch_id: string;
    response: unknown;
    event_id: string;
    seq: number;
  };
}

export async function listConnectorFetches(
  key: string,
  name: string,
  fetcher: typeof fetch = recordingFetch
): Promise<ConnectorFetch[]> {
  const res = await fetcher(`/v1/context/connectors/${encodeURIComponent(name)}/fetches`, {
    headers: authHeaders(key)
  });
  if (!res.ok) {
    return errorOrStatus(res, `GET connector ${name} fetches`);
  }
  return ((await res.json()) as { fetches: ConnectorFetch[] }).fetches ?? [];
}

export interface ModelValidation {
  version: number;
  dataset?: string;
  metrics?: Record<string, number>;
  validator?: string;
  notes?: string;
  passed: boolean;
  recorded_by?: string;
  recorded_at?: string;
}
export interface ModelPendingApproval {
  request_id: string;
  version: number;
  requested_by: string;
  requested_at: string;
}
export interface Model {
  name: string;
  kind: ModelKind;
  spec: unknown;
  owner?: string;
  updated_at: string;
  version?: number;
  approved_version?: number;
  approved_by?: string;
  approved_at?: string;
  pending?: ModelPendingApproval | null;
  validations?: ModelValidation[];
}
// modelApproved mirrors the backend's Approved(): the current version is the one a
// checker signed off on.
export function modelApproved(m: Model): boolean {
  return (m.version ?? 0) > 0 && m.approved_version === m.version;
}

// requestModelApproval proposes a model's current version for four-eyes review (maker).
export async function requestModelApproval(
  key: string,
  name: string,
  fetcher: typeof fetch = recordingFetch
): Promise<{ request_id: string }> {
  const res = await fetcher(`/v1/models/${encodeURIComponent(name)}/approval-request`, {
    method: 'POST',
    headers: authHeaders(key)
  });
  if (!res.ok) {
    return errorOrStatus(res, 'request model approval');
  }
  return (await res.json()) as { request_id: string };
}
// approveModel / rejectModel are the checker side (a different actor than the maker).
export async function approveModel(
  key: string,
  name: string,
  requestId: string,
  reason: string,
  fetcher: typeof fetch = recordingFetch
): Promise<void> {
  const res = await fetcher(`/v1/models/${encodeURIComponent(name)}/approve`, {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify({ request_id: requestId, reason })
  });
  if (!res.ok) {
    return errorOrStatus(res, 'approve model');
  }
}
export async function rejectModel(
  key: string,
  name: string,
  requestId: string,
  reason: string,
  fetcher: typeof fetch = recordingFetch
): Promise<void> {
  const res = await fetcher(`/v1/models/${encodeURIComponent(name)}/reject`, {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify({ request_id: requestId, reason })
  });
  if (!res.ok) {
    return errorOrStatus(res, 'reject model');
  }
}
// recordModelValidation attaches validation evidence to the model's current version.
export async function recordModelValidation(
  key: string,
  name: string,
  body: {
    dataset: string;
    metrics: Record<string, number>;
    notes: string;
    passed: boolean;
  },
  fetcher: typeof fetch = recordingFetch
): Promise<void> {
  const res = await fetcher(`/v1/models/${encodeURIComponent(name)}/validation`, {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify(body)
  });
  if (!res.ok) {
    return errorOrStatus(res, 'record model validation');
  }
}

export async function listModels(
  key: string,
  fetcher: typeof fetch = recordingFetch
): Promise<Model[]> {
  const res = await fetcher('/v1/models', { headers: authHeaders(key) });
  if (!res.ok) {
    return errorOrStatus(res, 'GET /v1/models');
  }
  return ((await res.json()) as { models: Model[] }).models ?? [];
}

export async function defineModel(
  key: string,
  body: { name: string; spec: unknown },
  fetcher: typeof fetch = recordingFetch
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

export interface FeatureImportance {
  feature: string;
  coefficient: number;
  importance: number;
}

export interface TrainReport {
  rows: number;
  positives: number;
  features: string[];
  iterations: number;
  train_log_loss: number;
  folds: number;
  cv_auc: number;
  cv_log_loss: number;
  cv_accuracy: number;
  importance: FeatureImportance[];
}

export interface TrainRow {
  features: Record<string, number>;
  label: number; // 0 or 1
}

// trainModel fits a logistic model from a labelled dataset, defines it under name, and
// returns the training report (cross-validated metrics + feature importance).
export async function trainModel(
  key: string,
  body: {
    name: string;
    dataset: TrainRow[];
    options?: { iterations?: number; learning_rate?: number; l2?: number; folds?: number };
  },
  fetcher: typeof fetch = recordingFetch
): Promise<TrainReport> {
  const res = await fetcher('/v1/models/train', {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify(body)
  });
  if (!res.ok) {
    return errorOrStatus(res, 'POST /v1/models/train');
  }
  return ((await res.json()) as { report: TrainReport }).report;
}

// copilotExplain returns a plain-language explanation of a flow graph.
export async function copilotExplain(
  key: string,
  graph: unknown,
  fetcher: typeof fetch = recordingFetch
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
  fetcher: typeof fetch = recordingFetch
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
  fetcher: typeof fetch = recordingFetch
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

// --- Decision intelligence: node-stats (heatmap), counterfactual, coverage ------------

export interface NodeStat {
  node_id: string;
  type: string;
  count: number;
  pct: number;
}
export interface FlowNodeStats {
  total: number;
  dispositions: { approve: number; decline: number; refer: number };
  nodes: NodeStat[];
}
// flowNodeStats returns per-node traversal counts over the flow's recorded decisions —
// the data behind the builder's heatmap (which nodes are hot, which are never hit).
export async function flowNodeStats(
  key: string,
  flowId: string,
  environment?: string,
  fetcher: typeof fetch = recordingFetch
): Promise<FlowNodeStats> {
  const q = environment ? `?environment=${encodeURIComponent(environment)}` : '';
  const res = await fetcher(`/v1/flows/${flowId}/node-stats${q}`, { headers: authHeaders(key) });
  if (!res.ok) {
    return errorOrStatus(res, `GET /v1/flows/${flowId}/node-stats failed`);
  }
  return (await res.json()) as FlowNodeStats;
}

export interface CounterfactualFlip {
  field: string;
  from: number;
  to: number;
  direction: 'increase' | 'decrease';
  disposition: string;
}
export interface Counterfactual {
  disposition: string;
  flips: CounterfactualFlip[];
  searched: number;
}
// decisionCounterfactual searches the minimal single-field input changes that would flip
// a non-favorable decision to a better disposition ("what would change the outcome?").
export async function decisionCounterfactual(
  key: string,
  decisionId: string,
  fetcher: typeof fetch = recordingFetch
): Promise<Counterfactual> {
  const res = await fetcher(`/v1/decisions/${decisionId}/counterfactual`, {
    method: 'POST',
    headers: jsonHeaders(key),
    body: '{}'
  });
  if (!res.ok) {
    return errorOrStatus(res, `POST /v1/decisions/${decisionId}/counterfactual failed`);
  }
  return (await res.json()) as Counterfactual;
}

export interface CoverageBranch {
  from: string;
  to: string;
  branch: string;
}
export interface Coverage {
  runs: number;
  failed_runs: number;
  fields: string[];
  nodes: { node_id: string; type: string; hits: number }[];
  branches: { from: string; to: string; branch: string; hits: number }[];
  dispositions: { approve: number; decline: number; refer: number };
  dead_nodes: string[];
  dead_branches: CoverageBranch[];
}
// flowCoverage fuzzes synthetic inputs through a flow and reports node/branch hit
// coverage — surfacing dead branches and the disposition spread (a policy red-team).
export async function flowCoverage(
  key: string,
  flowId: string,
  opts: { version?: number; runs?: number; mock_data?: Record<string, unknown> } = {},
  fetcher: typeof fetch = recordingFetch
): Promise<Coverage> {
  const res = await fetcher(`/v1/flows/${flowId}/coverage`, {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify(opts)
  });
  if (!res.ok) {
    return errorOrStatus(res, `POST /v1/flows/${flowId}/coverage failed`);
  }
  return (await res.json()) as Coverage;
}

export interface FeatureDrift {
  feature: string;
  count: number;
  mean: number;
  std: number;
  baseline_mean: number;
  baseline_std: number;
  mean_shift: number;
  var_ratio: number;
  drifting: boolean;
}

export interface ModelDrift {
  model: string;
  model_version?: number;
  count: number;
  hist: number[];
  window_days: number;
  has_baseline: boolean;
  psi?: number;
  threshold?: number;
  firing: boolean;
  alerting: boolean;
  features?: FeatureDrift[]; // covariate (input) drift vs the baseline
}

export interface Calibration {
  bucket: number;
  predicted: number;
  actual: number;
  count: number;
}

export interface ModelPerformance {
  model: string;
  model_version?: number;
  outcome_key?: string;
  node_id?: string;
  environment?: Environment;
  experiment_id?: string;
  cohort?: number;
  arm?: string;
  count: number;
  positives: number;
  excluded_actuals?: number;
  accuracy: number;
  brier: number;
  auc: number;
  calibration: Calibration[];
}

export interface ModelPerformanceFilter {
  outcome_key?: string;
  node_id?: string;
  environment?: Environment;
  experiment_id?: string;
  cohort?: number;
  arm?: string;
}

export async function getModelPerformance(
  key: string,
  name: string,
  filter: ModelPerformanceFilter = {},
  fetcher: typeof fetch = recordingFetch
): Promise<ModelPerformance> {
  const query = new URLSearchParams();
  if (filter.outcome_key) query.set('outcome_key', filter.outcome_key);
  if (filter.node_id) query.set('node_id', filter.node_id);
  if (filter.environment) query.set('env', filter.environment);
  if (filter.experiment_id) query.set('experiment', filter.experiment_id);
  if (filter.cohort) query.set('cohort', String(filter.cohort));
  if (filter.arm) query.set('arm', filter.arm);
  const suffix = query.size ? `?${query}` : '';
  const res = await fetcher(`/v1/models/${encodeURIComponent(name)}/performance${suffix}`, {
    headers: authHeaders(key)
  });
  if (!res.ok) {
    return errorOrStatus(res, `GET /v1/models/${name}/performance`);
  }
  return (await res.json()) as ModelPerformance;
}

// recordModelOutcome reconciles a realized label with an immutable prediction.
// Probability and model version are recovered by the backend from the decision trace.
export async function recordModelOutcome(
  key: string,
  name: string,
  body: { label: number; decision_id: string; node_id?: string },
  fetcher: typeof fetch = recordingFetch
): Promise<void> {
  const res = await fetcher(`/v1/models/${encodeURIComponent(name)}/outcomes`, {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify(body)
  });
  if (!res.ok) {
    return errorOrStatus(res, `POST /v1/models/${name}/outcomes`);
  }
}

export async function modelDrift(
  key: string,
  name: string,
  windowDays = 0,
  fetcher: typeof fetch = recordingFetch
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
  fetcher: typeof fetch = recordingFetch
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
  fetcher: typeof fetch = recordingFetch
): Promise<void> {
  const res = await fetcher(`/v1/models/${encodeURIComponent(name)}/baseline`, {
    method: 'POST',
    headers: jsonHeaders(key)
  });
  if (!res.ok) {
    return errorOrStatus(res, 'POST model baseline');
  }
}

export async function listFeatures(
  key: string,
  fetcher: typeof fetch = recordingFetch
): Promise<Feature[]> {
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
  fetcher: typeof fetch = recordingFetch
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
  fetcher: typeof fetch = recordingFetch
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
  fetcher: typeof fetch = recordingFetch
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
  fetcher: typeof fetch = recordingFetch
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
  fetcher: typeof fetch = recordingFetch
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
  fetcher: typeof fetch = recordingFetch
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
  fetcher: typeof fetch = recordingFetch
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
  fetcher: typeof fetch = recordingFetch
): Promise<{ count: number; seq?: number }> {
  const res = await fetcher('/v1/cases/sla-sweep', { method: 'POST', headers: authHeaders(key) });
  if (!res.ok) {
    return errorOrStatus(res, 'POST /v1/cases/sla-sweep');
  }
  return (await res.json()) as { count: number; seq?: number };
}

export async function getCase(
  key: string,
  caseID: string,
  fetcher: typeof fetch = recordingFetch
): Promise<Case> {
  const res = await fetcher(`/v1/cases/${caseID}`, { headers: authHeaders(key) });
  if (!res.ok) {
    return errorOrStatus(res, `GET /v1/cases/${caseID}`);
  }
  return (await res.json()) as Case;
}

export async function requestReview(
  key: string,
  body: {
    company_name: string;
    case_type: string;
    priority?: string;
    jurisdiction?: string;
    subject?: string;
    sla_days?: number;
    context?: Record<string, unknown>;
    source_decision_id?: string;
  },
  fetcher: typeof fetch = recordingFetch
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
  fetcher: typeof fetch,
  method: 'POST' | 'PATCH' = 'POST'
): Promise<void> {
  const res = await fetcher(`/v1/cases/${caseID}/${action}`, {
    method,
    headers: jsonHeaders(key),
    body: JSON.stringify(body)
  });
  if (!res.ok) {
    return errorOrStatus(res, `${method} /v1/cases/${caseID}/${action}`);
  }
}

// assignCase claims a case. The backend refuses to overwrite another reviewer's
// claim unless reassign says to, so two reviewers cannot both believe they own it.
export function assignCase(
  key: string,
  caseID: string,
  assignee: string,
  reassign = false,
  fetcher: typeof fetch = recordingFetch
): Promise<void> {
  return caseAction(key, caseID, 'assign', { assignee, reassign }, fetcher);
}

export function setCaseStatus(
  key: string,
  caseID: string,
  status: string,
  fetcher: typeof fetch = recordingFetch
): Promise<void> {
  return caseAction(key, caseID, 'status', { status }, fetcher);
}

export async function listCaseTypes(
  key: string,
  fetcher: typeof fetch = recordingFetch
): Promise<CaseTypeView[]> {
  const res = await fetcher('/v1/case-types', { headers: authHeaders(key) });
  if (!res.ok) return errorOrStatus(res, 'GET /v1/case-types');
  return ((await res.json()) as { case_types: CaseTypeView[] }).case_types ?? [];
}

export async function publishCaseType(
  key: string,
  definition: CaseTypeDefinition,
  fetcher: typeof fetch = recordingFetch
): Promise<{ key: string; version: number }> {
  const res = await fetcher('/v1/case-types', {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify(definition)
  });
  if (!res.ok) return errorOrStatus(res, 'POST /v1/case-types');
  return (await res.json()) as { key: string; version: number };
}

export async function getCaseTypeVersion(
  key: string,
  typeKey: string,
  version: number,
  fetcher: typeof fetch = recordingFetch
): Promise<CaseTypeView> {
  const res = await fetcher(`/v1/case-types/${encodeURIComponent(typeKey)}/versions/${version}`, {
    headers: authHeaders(key)
  });
  if (!res.ok) return errorOrStatus(res, 'GET /v1/case-types/{key}/versions/{version}');
  return (await res.json()) as CaseTypeView;
}

export async function listCaseQueues(
  key: string,
  fetcher: typeof fetch = recordingFetch
): Promise<Array<{ definition: CaseQueueDefinition }>> {
  const res = await fetcher('/v1/case-queues', { headers: authHeaders(key) });
  if (!res.ok) return errorOrStatus(res, 'GET /v1/case-queues');
  return (
    ((await res.json()) as { queues: Array<{ definition: CaseQueueDefinition }> }).queues ?? []
  );
}

export async function configureCaseQueue(
  key: string,
  definition: CaseQueueDefinition,
  fetcher: typeof fetch = recordingFetch
): Promise<void> {
  const res = await fetcher(`/v1/case-queues/${encodeURIComponent(definition.key)}`, {
    method: 'PUT',
    headers: jsonHeaders(key),
    body: JSON.stringify(definition)
  });
  if (!res.ok) return errorOrStatus(res, 'PUT /v1/case-queues/{key}');
}

export async function listCaseReviewers(
  key: string,
  fetcher: typeof fetch = recordingFetch
): Promise<Array<{ profile: CaseReviewerProfile }>> {
  const res = await fetcher('/v1/case-reviewers', { headers: authHeaders(key) });
  if (!res.ok) return errorOrStatus(res, 'GET /v1/case-reviewers');
  return (
    ((await res.json()) as { reviewers: Array<{ profile: CaseReviewerProfile }> }).reviewers ?? []
  );
}

export async function configureCaseReviewer(
  key: string,
  profile: CaseReviewerProfile,
  fetcher: typeof fetch = recordingFetch
): Promise<void> {
  const res = await fetcher(`/v1/case-reviewers/${encodeURIComponent(profile.actor)}`, {
    method: 'PUT',
    headers: jsonHeaders(key),
    body: JSON.stringify(profile)
  });
  if (!res.ok) return errorOrStatus(res, 'PUT /v1/case-reviewers/{actor}');
}

export async function bulkCases(
  key: string,
  request: {
    operation: CaseBulkResult['operation'];
    case_ids: string[];
    target: string;
    reason_code?: string;
    note?: string;
    reassign?: boolean;
    override?: boolean;
  },
  idempotencyKey: string,
  fetcher: typeof fetch = recordingFetch
): Promise<CaseBulkResult> {
  const res = await fetcher('/v1/cases/bulk', {
    method: 'POST',
    headers: { ...jsonHeaders(key), 'Idempotency-Key': idempotencyKey },
    body: JSON.stringify(request)
  });
  if (!res.ok) return errorOrStatus(res, 'POST /v1/cases/bulk');
  return (await res.json()) as CaseBulkResult;
}

export async function listCaseSavedViews(
  key: string,
  fetcher: typeof fetch = recordingFetch
): Promise<CaseSavedView[]> {
  const res = await fetcher('/v1/case-views', { headers: authHeaders(key) });
  if (!res.ok) return errorOrStatus(res, 'GET /v1/case-views');
  const body = (await res.json()) as { views: CaseSavedView[] };
  return body.views;
}

export async function saveCaseView(
  key: string,
  name: string,
  query: CaseFilter,
  viewID = '',
  fetcher: typeof fetch = recordingFetch
): Promise<{ view_id: string }> {
  const res = await fetcher('/v1/case-views', {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify({ view_id: viewID, name, query })
  });
  if (!res.ok) return errorOrStatus(res, 'POST /v1/case-views');
  return (await res.json()) as { view_id: string };
}

export async function deleteCaseView(
  key: string,
  viewID: string,
  fetcher: typeof fetch = recordingFetch
): Promise<void> {
  const res = await fetcher(`/v1/case-views/${encodeURIComponent(viewID)}`, {
    method: 'DELETE',
    headers: authHeaders(key)
  });
  if (!res.ok) return errorOrStatus(res, 'DELETE /v1/case-views/{view_id}');
}

export async function listCaseDuplicates(
  key: string,
  fetcher: typeof fetch = recordingFetch
): Promise<CaseDuplicateGroup[]> {
  const res = await fetcher('/v1/cases/duplicates', { headers: authHeaders(key) });
  if (!res.ok) return errorOrStatus(res, 'GET /v1/cases/duplicates');
  const body = (await res.json()) as { duplicate_groups: CaseDuplicateGroup[] };
  return body.duplicate_groups;
}

export async function getCaseAnalytics(
  key: string,
  fetcher: typeof fetch = recordingFetch
): Promise<CaseAnalytics> {
  const res = await fetcher('/v1/cases/analytics', { headers: authHeaders(key) });
  if (!res.ok) return errorOrStatus(res, 'GET /v1/cases/analytics');
  return (await res.json()) as CaseAnalytics;
}

export async function exportCaseAudit(
  key: string,
  format: 'csv' | 'json' | 'markdown',
  filter: CaseFilter = {},
  fetcher: typeof fetch = recordingFetch
): Promise<Blob> {
  const separator = caseQuery(filter) ? '&' : '?';
  const res = await fetcher(`/v1/cases/export${caseQuery(filter)}${separator}format=${format}`, {
    headers: authHeaders(key)
  });
  if (!res.ok) return errorOrStatus(res, 'GET /v1/cases/export');
  return res.blob();
}

export async function routeCase(
  key: string,
  caseID: string,
  fetcher: typeof fetch = recordingFetch
): Promise<void> {
  return caseAction(key, caseID, 'route', {}, fetcher);
}

export async function rebalanceCases(
  key: string,
  fetcher: typeof fetch = recordingFetch
): Promise<{ moved: string[]; failures: Record<string, string> }> {
  const res = await fetcher('/v1/cases/rebalance', {
    method: 'POST',
    headers: authHeaders(key)
  });
  if (!res.ok) return errorOrStatus(res, 'POST /v1/cases/rebalance');
  return (await res.json()) as { moved: string[]; failures: Record<string, string> };
}

export function setCasePriority(
  key: string,
  caseID: string,
  priority: string,
  fetcher: typeof fetch = recordingFetch
): Promise<void> {
  return caseAction(key, caseID, 'priority', { priority }, fetcher);
}

export function updateCaseFields(
  key: string,
  caseID: string,
  fields: Record<string, unknown>,
  fetcher: typeof fetch = recordingFetch
): Promise<void> {
  return caseAction(key, caseID, 'fields', { fields }, fetcher, 'PATCH');
}

export function dispositionCase(
  key: string,
  caseID: string,
  disposition: string,
  reasonCode: string,
  note = '',
  override = false,
  fetcher: typeof fetch = recordingFetch
): Promise<void> {
  return caseAction(
    key,
    caseID,
    'disposition',
    { disposition, reason_code: reasonCode, note, override },
    fetcher
  );
}

export function linkCaseEvidence(
  key: string,
  caseID: string,
  evidence: Omit<CaseEvidence, 'evidence_id' | 'linked_seq'> & { evidence_id?: string },
  fetcher: typeof fetch = recordingFetch
): Promise<void> {
  return caseAction(key, caseID, 'evidence', evidence, fetcher);
}

export function registerCaseAttachment(
  key: string,
  caseID: string,
  attachment: Omit<CaseAttachment, 'access_count' | 'registered_seq' | 'storage_ref'> & {
    storage_ref: string;
  },
  fetcher: typeof fetch = recordingFetch
): Promise<void> {
  return caseAction(key, caseID, 'attachments', attachment, fetcher);
}

export async function accessCaseAttachment(
  key: string,
  caseID: string,
  attachmentID: string,
  purpose: string,
  fetcher: typeof fetch = recordingFetch
): Promise<string> {
  const res = await fetcher(
    `/v1/cases/${encodeURIComponent(caseID)}/attachments/${encodeURIComponent(attachmentID)}/access`,
    {
      method: 'POST',
      headers: jsonHeaders(key),
      body: JSON.stringify({ purpose })
    }
  );
  if (!res.ok)
    return errorOrStatus(res, 'POST /v1/cases/{case_id}/attachments/{attachment_id}/access');
  const body = (await res.json()) as { storage_ref: string };
  return body.storage_ref;
}

export function selectCaseQA(
  key: string,
  caseID: string,
  sampleID: string,
  reviewer: string,
  rateBPS: number,
  fetcher: typeof fetch = recordingFetch
): Promise<void> {
  return caseAction(
    key,
    caseID,
    'qa/select',
    { sample_id: sampleID, reviewer, rate_bps: rateBPS },
    fetcher
  );
}

export function reviewCaseQA(
  key: string,
  caseID: string,
  sampleID: string,
  disposition: string,
  reasonCode: string,
  note: string,
  override: boolean,
  fetcher: typeof fetch = recordingFetch
): Promise<void> {
  return caseAction(
    key,
    caseID,
    'qa/review',
    { sample_id: sampleID, disposition, reason_code: reasonCode, note, override },
    fetcher
  );
}

export function addCaseQAFeedback(
  key: string,
  caseID: string,
  sampleID: string,
  text: string,
  fetcher: typeof fetch = recordingFetch
): Promise<void> {
  return caseAction(key, caseID, 'qa/feedback', { sample_id: sampleID, text }, fetcher);
}

export function retryCaseWebhook(
  key: string,
  caseID: string,
  reason: string,
  fetcher: typeof fetch = recordingFetch
): Promise<void> {
  return caseAction(key, caseID, 'webhook/retry', { reason }, fetcher);
}

export function addCaseNote(
  key: string,
  caseID: string,
  text: string,
  fetcher: typeof fetch = recordingFetch
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
  version?: number;
  attempt?: number;
  max_attempts?: number;
  timeout_ms?: number;
  worker_owner?: string;
  lease_until?: string;
  cancel_requested?: boolean;
  business_reference?: string;
  correlation_id?: string;
  prompt_tokens?: number;
  completion_tokens?: number;
  case_id?: string;
  seq: number;
  at: string;
}

export interface RunResult {
  run_id: string;
  status: AgentRunStatus;
  text?: string;
  structured?: unknown;
  error?: string;
  seq: number;
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
  running: number;
  retrying: number;
  cancelled: number;
  timed_out: number;
  dead_letter: number;
  by_agent: Record<string, number>;
  prompt_tokens: number;
  completion_tokens: number;
  by_model: Record<string, ModelUsage>;
  // Cost is present only when a price table is configured (INTRAKTIBLE_AI_PRICES).
  priced: boolean;
  total_cost_usd: number;
  cost_by_model?: Record<string, number>;
}

export async function listAgents(
  key: string,
  fetcher: typeof fetch = recordingFetch
): Promise<Agent[]> {
  const res = await fetcher('/v1/agents', { headers: authHeaders(key) });
  if (!res.ok) {
    return errorOrStatus(res, 'GET /v1/agents');
  }
  return ((await res.json()) as { agents: Agent[] }).agents ?? [];
}

export async function getAgent(
  key: string,
  name: string,
  fetcher: typeof fetch = recordingFetch
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
  fetcher: typeof fetch = recordingFetch
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

// Governed agent operations -----------------------------------------------------

export interface AgentTemplate {
  org?: string;
  workspace?: string;
  template_id: string;
  slug: string;
  name: string;
  description?: string;
  task: string;
  tags?: string[];
  high_impact: boolean;
  latest_release?: number;
  registered_by?: string;
  registered_at?: string;
  seq?: number;
}

export interface AgentBudget {
  max_prompt_tokens: number;
  max_completion_tokens: number;
  max_tool_calls: number;
  max_cost_usd: number;
  input_cost_per_mtok: number;
  output_cost_per_mtok: number;
  pricing_source: string;
  pricing_version: string;
  period?: 'day' | 'month';
  period_cost_usd?: number;
}

export interface AgentToolPolicy {
  name: string;
  mode: AgentToolApprovalMode;
  purpose: string;
  parameter_schema?: unknown;
}

export interface AgentReleaseSpec {
  instructions: string;
  provider: string;
  model: string;
  input_schema: unknown;
  output_schema: unknown;
  tools: AgentToolPolicy[];
  data_purposes: string[];
  dependencies: Array<{ kind: string; name: string; version: string; hash?: string }>;
  budget: AgentBudget;
  timeout_ms: number;
  max_attempts: number;
  circuit_breaker?: {
    window_minutes: number;
    min_samples: number;
    failure_rate: number;
  };
  require_citations: boolean;
  require_human_gate: boolean;
  allow_remote_agent: boolean;
  remote_protocol_url?: string;
  remote_protocol_version?: string;
  remote_credential_env?: string;
}

export interface AgentReview {
  request_id: string;
  campaign_ids: string[];
  evidence_ids: string[];
  reviewers?: string[];
  requested_by: string;
  requested_at: string;
  expires_at: string;
  decision?: 'approve' | 'reject';
  reason?: string;
  reviewed_by?: string;
  reviewed_at?: string;
  expired_at?: string;
}

export interface AgentRelease {
  template_id: string;
  release: number;
  status: AgentReleaseStatus;
  spec: AgentReleaseSpec;
  spec_hash: string;
  campaign_ids: string[];
  review?: AgentReview;
  created_by: string;
  created_at: string;
  retired_at?: string;
  seq: number;
}

export interface AgentEvalSuite {
  suite_id: string;
  version: number;
  name: string;
  description?: string;
  adversarial: boolean;
  required: boolean;
  trials: number;
  min_pass_rate: number;
  max_variance: number;
  cases: Array<Record<string, unknown>>;
  semantic_grader?: {
    provider: string;
    model: string;
    instructions: string;
    version: string;
    budget: AgentBudget;
    timeout_ms: number;
    max_attempts: number;
  };
  dataset_hash: string;
  published_by?: string;
  published_at?: string;
  seq?: number;
}

export interface AgentCampaign {
  campaign_id: string;
  template_id: string;
  release: number;
  suite_id: string;
  suite_version: number;
  total: number;
  passed: number;
  pass_rate: number;
  variance: number;
  confidence_low: number;
  confidence_high: number;
  blocking: boolean;
  trials: AgentTrialResult[];
  assessment?: AgentCampaignAssessment;
  adjudications?: AgentTrialAdjudication[];
  recorded_by?: string;
  recorded_at?: string;
  seq?: number;
}

export interface AgentTrialResult {
  case_id: string;
  trial: number;
  passed: boolean;
  status: string;
  detail?: string;
  output_hash?: string;
  citations?: string[];
  tool_calls?: string[];
  invocation_id?: string;
  provider?: string;
  model?: string;
  attempts?: number;
  latency_ms: number;
  prompt_tokens?: number;
  output_tokens?: number;
  cost_usd?: number;
  grade?: AgentGradeEvidence;
}

export interface AgentGradeEvidence {
  kind: AgentGraderKind;
  score?: number;
  passed: boolean;
  detail?: string;
  grader_hash: string;
  rubric_hash?: string;
  invocation_id?: string;
  provider?: string;
  model?: string;
  attempts?: number;
  latency_ms?: number;
  prompt_tokens?: number;
  output_tokens?: number;
  cost_usd?: number;
  output_hash?: string;
}

export interface AgentTrialAdjudication {
  campaign_id: string;
  case_id: string;
  trial: number;
  passed: boolean;
  reason: string;
  adjudicated_by: string;
  adjudicated_at: string;
}

export interface AgentCampaignAssessment {
  total: number;
  passed: number;
  pass_rate: number;
  variance: number;
  confidence_low: number;
  confidence_high: number;
  blocking: boolean;
}

export interface AgentCampaignComparison {
  suite_id: string;
  suite_version: number;
  baseline_campaign_id: string;
  baseline_template_id: string;
  baseline_release: number;
  challenger_campaign_id: string;
  challenger_template_id: string;
  challenger_release: number;
  baseline: AgentCampaignAssessment;
  challenger: AgentCampaignAssessment;
  delta_pass_rate: number;
  delta_confidence_low: number;
  delta_confidence_high: number;
  baseline_cost_usd: number;
  challenger_cost_usd: number;
  baseline_latency_ms: number;
  challenger_latency_ms: number;
  regressions: number;
  improvements: number;
  rows: Array<{
    case_id: string;
    segment?: string;
    trials: number;
    baseline_pass_rate: number;
    challenger_pass_rate: number;
    delta_pass_rate: number;
    regression: boolean;
  }>;
}

export interface AgentDeployment {
  deployment_id: string;
  template_id: string;
  release: number;
  environment: Environment;
  status: AgentDeploymentStatus;
  reason: string;
  requested_by: string;
  requested_at: string;
  activate_at?: string;
  expires_at?: string;
  activated_by?: string;
  activated_at?: string;
  paused_by?: string;
  paused_at?: string;
  resumed_by?: string;
  resumed_at?: string;
  previous_release?: number;
  seq: number;
}

export interface AgentSafetyIncident {
  incident_id: string;
  template_id: string;
  release: number;
  deployment_id?: string;
  run_id?: string;
  kind: string;
  severity: 'info' | 'warning' | 'required' | 'critical';
  summary: string;
  evidence?: Record<string, unknown>;
  status: 'open' | 'resolved';
  resolution?: string;
  opened_at: string;
  resolved_at?: string;
  seq: number;
}

export interface AgentCitation {
  evidence_id: string;
  claim: string;
  quote_hash?: string;
}

export interface AgentToolExecution {
  call_id: string;
  name: string;
  mode: 'automatic' | 'human_before_call' | 'forbidden';
  purpose: string;
  arguments_hash: string;
  result_hash: string;
  approved_by?: string;
}

export interface AgentToolApproval {
  approval_id: string;
  assist_id: string;
  invocation_id: string;
  call_id: string;
  name: string;
  purpose: string;
  arguments_hash: string;
  status: AgentToolApprovalStatus;
  requested_by: string;
  requested_at: string;
  expires_at: string;
  decided_by?: string;
  decided_at?: string;
  reason?: string;
  seq: number;
}

export interface AgentQualityMetrics {
  assists: number;
  completed: number;
  failed: number;
  awaiting_tool_approval: number;
  awaiting_result: number;
  actioned: number;
  accepted: number;
  edited: number;
  rejected: number;
  escalated: number;
  adopted: number;
  adoption_rate: number;
  adoption_ci_low: number;
  adoption_ci_high: number;
  qa_assessed: number;
  qa_agreed: number;
  qa_agreement_rate: number;
  qa_agreement_ci_low: number;
  qa_agreement_ci_high: number;
  validated_outcomes: number;
  missing_outcomes: number;
  prompt_tokens: number;
  output_tokens: number;
  cost_usd: number;
  average_latency_ms: number;
  p95_latency_ms: number;
  tool_executions: number;
  human_tool_executions: number;
  time_saved_ms: number;
  average_time_saved_ms: number;
}

export interface AgentAnalyticsReport {
  totals: AgentQualityMetrics;
  groups: Array<{
    template_id: string;
    template_name: string;
    task: string;
    release: number;
    provider: string;
    model: string;
    environment: Environment;
    deployment_id?: string;
    deployment_status?: AgentDeploymentStatus;
    campaigns: number;
    blocking_campaigns: number;
    open_incidents: number;
    metrics: AgentQualityMetrics;
  }>;
  segments: Array<{
    case_type: string;
    jurisdiction?: string;
    metrics: AgentQualityMetrics;
  }>;
}

export interface AgentAssist {
  assist_id: string;
  case_id: string;
  kind: string;
  template_id: string;
  release: number;
  environment: Environment;
  evidence_ids: string[];
  evidence_seq: number;
  current_evidence_seq: number;
  evidence_stale?: boolean;
  invocation_id: string;
  policy_source?: {
    kind: 'case_type' | 'queue';
    key: string;
    configuration_seq: number;
    policy_key: string;
    evidence_fingerprint: string;
  };
  status:
    | 'requested'
    | 'running'
    | 'awaiting_tool_approval'
    | 'completed'
    | 'failed'
    | 'dead_letter'
    | 'cancelled';
  content_erased?: boolean;
  result?: {
    suggestion?: Record<string, unknown>;
    citations?: AgentCitation[];
    unsupported?: string[];
    confidence: number;
    limitations?: string[];
    invocation_id: string;
    provider: string;
    model: string;
    attempts: number;
    latency_ms: number;
    prompt_tokens: number;
    output_tokens: number;
    cost_usd: number;
    tool_calls?: AgentToolExecution[];
  };
  failure?: string;
  action?: {
    assist_id: string;
    action: 'accepted' | 'edited' | 'rejected' | 'escalated';
    final?: Record<string, unknown>;
    reason?: string;
    time_saved_ms?: number;
    evidence_head_seq: number;
    evidence_stale?: boolean;
  };
  suggestion_hash?: string;
  final_hash?: string;
  differences?: Array<{ path: string; kind: 'added' | 'removed' | 'changed' }>;
  action_final_erased?: boolean;
  requested_by: string;
  requested_at: string;
  completed_at?: string;
  attempt?: number;
  lease_until?: string;
  seq: number;
}

async function governedJSON<T>(
  path: string,
  key: string,
  init: RequestInit = {},
  fetcher: typeof fetch = recordingFetch
): Promise<T> {
  const headers = new Headers(init.body ? jsonHeaders(key) : authHeaders(key));
  new Headers(init.headers).forEach((value, name) => headers.set(name, value));
  const res = await fetcher(path, {
    ...init,
    headers
  });
  if (!res.ok) return errorOrStatus(res, `${init.method ?? 'GET'} ${path}`);
  return (await res.json()) as T;
}

export async function listAgentTemplates(
  key: string,
  fetcher: typeof fetch = recordingFetch
): Promise<AgentTemplate[]> {
  const body = await governedJSON<{ templates: AgentTemplate[] }>(
    '/v1/agent-templates',
    key,
    {},
    fetcher
  );
  return body.templates ?? [];
}

export async function createAgentTemplate(
  key: string,
  template: Omit<AgentTemplate, 'org' | 'workspace' | 'latest_release' | 'seq'>,
  fetcher: typeof fetch = recordingFetch
): Promise<AgentTemplate> {
  const body = await governedJSON<{ template: AgentTemplate }>(
    '/v1/agent-templates',
    key,
    { method: 'POST', body: JSON.stringify(template) },
    fetcher
  );
  return body.template;
}

export function getAgentTemplate(
  key: string,
  templateID: string,
  fetcher: typeof fetch = recordingFetch
): Promise<AgentTemplate> {
  return governedJSON(`/v1/agent-templates/${encodeURIComponent(templateID)}`, key, {}, fetcher);
}

export async function listAgentReleases(
  key: string,
  templateID: string,
  fetcher: typeof fetch = recordingFetch
): Promise<AgentRelease[]> {
  const body = await governedJSON<{ releases: AgentRelease[] }>(
    `/v1/agent-templates/${encodeURIComponent(templateID)}/releases`,
    key,
    {},
    fetcher
  );
  return body.releases ?? [];
}

export async function createAgentRelease(
  key: string,
  templateID: string,
  spec: AgentReleaseSpec,
  fetcher: typeof fetch = recordingFetch
): Promise<{ release: number; spec_hash: string; seq: number }> {
  return governedJSON(
    `/v1/agent-templates/${encodeURIComponent(templateID)}/releases`,
    key,
    { method: 'POST', body: JSON.stringify({ spec }) },
    fetcher
  );
}

export async function listAgentEvalSuites(
  key: string,
  fetcher: typeof fetch = recordingFetch
): Promise<AgentEvalSuite[]> {
  const body = await governedJSON<{ suites: AgentEvalSuite[] }>(
    '/v1/agent-eval-suites',
    key,
    {},
    fetcher
  );
  return body.suites ?? [];
}

export async function publishAgentEvalSuite(
  key: string,
  suite: Omit<AgentEvalSuite, 'version' | 'dataset_hash'> & {
    version?: number;
    dataset_hash?: string;
  },
  fetcher: typeof fetch = recordingFetch
): Promise<AgentEvalSuite> {
  const body = await governedJSON<{ suite: AgentEvalSuite }>(
    '/v1/agent-eval-suites',
    key,
    { method: 'POST', body: JSON.stringify(suite) },
    fetcher
  );
  return body.suite;
}

export async function runAgentCampaign(
  key: string,
  templateID: string,
  release: number,
  suiteID: string,
  suiteVersion: number,
  fetcher: typeof fetch = recordingFetch
): Promise<AgentCampaign> {
  const body = await governedJSON<{ campaign: AgentCampaign }>(
    `/v1/agent-templates/${encodeURIComponent(templateID)}/releases/${release}/campaigns`,
    key,
    {
      method: 'POST',
      body: JSON.stringify({ suite_id: suiteID, suite_version: suiteVersion })
    },
    fetcher
  );
  return body.campaign;
}

export async function listAgentCampaigns(
  key: string,
  templateID: string,
  release: number,
  fetcher: typeof fetch = recordingFetch
): Promise<AgentCampaign[]> {
  const body = await governedJSON<{ campaigns: AgentCampaign[] }>(
    `/v1/agent-templates/${encodeURIComponent(templateID)}/releases/${release}/campaigns`,
    key,
    {},
    fetcher
  );
  return body.campaigns ?? [];
}

export async function adjudicateAgentCampaignTrial(
  key: string,
  campaignID: string,
  caseID: string,
  trial: number,
  passed: boolean,
  reason: string,
  fetcher: typeof fetch = recordingFetch
): Promise<{ event_id: string; seq: number }> {
  return governedJSON(
    `/v1/agent-eval-campaigns/${encodeURIComponent(campaignID)}/trials/${encodeURIComponent(caseID)}/${trial}/adjudication`,
    key,
    { method: 'POST', body: JSON.stringify({ passed, reason }) },
    fetcher
  );
}

export async function compareAgentCampaigns(
  key: string,
  baselineCampaignID: string,
  challengerCampaignID: string,
  fetcher: typeof fetch = recordingFetch
): Promise<AgentCampaignComparison> {
  const query = new URLSearchParams({
    baseline_campaign_id: baselineCampaignID,
    challenger_campaign_id: challengerCampaignID
  });
  return governedJSON(`/v1/agent-eval-comparisons?${query}`, key, {}, fetcher);
}

export async function downloadAgentCampaign(
  key: string,
  campaignID: string,
  format: 'json' | 'csv',
  fetcher: typeof fetch = recordingFetch
): Promise<Blob> {
  const path = `/v1/agent-eval-campaigns/${encodeURIComponent(campaignID)}/export?format=${format}`;
  const response = await fetcher(path, { headers: authHeaders(key) });
  if (!response.ok) return errorOrStatus(response, `GET ${path}`);
  return response.blob();
}

export async function requestAgentReleaseReview(
  key: string,
  templateID: string,
  release: number,
  campaignIDs: string[],
  evidenceIDs: string[],
  reviewers: string[],
  expiresAt: string,
  fetcher: typeof fetch = recordingFetch
): Promise<{ request_id: string; seq: number }> {
  return governedJSON(
    `/v1/agent-templates/${encodeURIComponent(templateID)}/releases/${release}/review-request`,
    key,
    {
      method: 'POST',
      body: JSON.stringify({
        campaign_ids: campaignIDs,
        evidence_ids: evidenceIDs,
        reviewers,
        expires_at: expiresAt
      })
    },
    fetcher
  );
}

export function reviewAgentRelease(
  key: string,
  templateID: string,
  release: number,
  requestID: string,
  decision: 'approve' | 'reject',
  reason: string,
  fetcher: typeof fetch = recordingFetch
): Promise<{ seq: number }> {
  return governedJSON(
    `/v1/agent-templates/${encodeURIComponent(templateID)}/releases/${release}/review`,
    key,
    { method: 'POST', body: JSON.stringify({ request_id: requestID, decision, reason }) },
    fetcher
  );
}

export async function listAgentDeployments(
  key: string,
  fetcher: typeof fetch = recordingFetch
): Promise<AgentDeployment[]> {
  const body = await governedJSON<{ deployments: AgentDeployment[] }>(
    '/v1/agent-deployments',
    key,
    {},
    fetcher
  );
  return body.deployments ?? [];
}

export function deployAgentRelease(
  key: string,
  request: {
    template_id: string;
    release: number;
    environment: Environment;
    at?: string;
    expires_at?: string;
    reason: string;
  },
  fetcher: typeof fetch = recordingFetch
): Promise<{ deployment_id: string; seq: number }> {
  return governedJSON(
    '/v1/agent-deployments',
    key,
    { method: 'POST', body: JSON.stringify(request) },
    fetcher
  );
}

export function pauseAgentDeployment(
  key: string,
  deploymentID: string,
  reason: string,
  fetcher: typeof fetch = recordingFetch
): Promise<{ seq: number }> {
  return governedJSON(
    `/v1/agent-deployments/${encodeURIComponent(deploymentID)}/pause`,
    key,
    { method: 'POST', body: JSON.stringify({ reason }) },
    fetcher
  );
}

export function resumeAgentDeployment(
  key: string,
  deploymentID: string,
  reason: string,
  fetcher: typeof fetch = recordingFetch
): Promise<{ seq: number }> {
  return governedJSON(
    `/v1/agent-deployments/${encodeURIComponent(deploymentID)}/resume`,
    key,
    { method: 'POST', body: JSON.stringify({ reason }) },
    fetcher
  );
}

export function activateAgentDeployment(
  key: string,
  deploymentID: string,
  fetcher: typeof fetch = recordingFetch
): Promise<{ seq: number }> {
  return governedJSON(
    `/v1/agent-deployments/${encodeURIComponent(deploymentID)}/activate`,
    key,
    { method: 'POST', body: '{}' },
    fetcher
  );
}

export function rollbackAgentDeployment(
  key: string,
  deploymentID: string,
  toRelease: number,
  reason: string,
  fetcher: typeof fetch = recordingFetch
): Promise<{ seq: number }> {
  return governedJSON(
    `/v1/agent-deployments/${encodeURIComponent(deploymentID)}/rollback`,
    key,
    { method: 'POST', body: JSON.stringify({ to_release: toRelease, reason }) },
    fetcher
  );
}

export function retireAgentRelease(
  key: string,
  templateID: string,
  release: number,
  reason: string,
  fetcher: typeof fetch = recordingFetch
): Promise<{ seq: number }> {
  return governedJSON(
    `/v1/agent-templates/${encodeURIComponent(templateID)}/releases/${release}/retire`,
    key,
    { method: 'POST', body: JSON.stringify({ reason }) },
    fetcher
  );
}

export async function listAgentSafetyIncidents(
  key: string,
  fetcher: typeof fetch = recordingFetch
): Promise<AgentSafetyIncident[]> {
  const body = await governedJSON<{ incidents: AgentSafetyIncident[] }>(
    '/v1/agent-safety-incidents',
    key,
    {},
    fetcher
  );
  return body.incidents ?? [];
}

export function openAgentSafetyIncident(
  key: string,
  incident: Omit<AgentSafetyIncident, 'incident_id' | 'status' | 'opened_at' | 'seq'> & {
    incident_id?: string;
  },
  fetcher: typeof fetch = recordingFetch
): Promise<{ incident_id: string; seq: number }> {
  return governedJSON(
    '/v1/agent-safety-incidents',
    key,
    { method: 'POST', body: JSON.stringify(incident) },
    fetcher
  );
}

export function resolveAgentSafetyIncident(
  key: string,
  incidentID: string,
  resolution: string,
  fetcher: typeof fetch = recordingFetch
): Promise<{ seq: number }> {
  return governedJSON(
    `/v1/agent-safety-incidents/${encodeURIComponent(incidentID)}/resolve`,
    key,
    { method: 'POST', body: JSON.stringify({ resolution }) },
    fetcher
  );
}

export async function listAgentToolApprovals(
  key: string,
  fetcher: typeof fetch = recordingFetch
): Promise<AgentToolApproval[]> {
  const body = await governedJSON<{ approvals: AgentToolApproval[] }>(
    '/v1/agent-tool-approvals',
    key,
    {},
    fetcher
  );
  return body.approvals ?? [];
}

export function decideAgentToolApproval(
  key: string,
  approvalID: string,
  decision: 'approved' | 'rejected',
  reason: string,
  fetcher: typeof fetch = recordingFetch
): Promise<{
  approval_id: string;
  assist_id?: string;
  status: 'requested' | 'rejected';
  seq: number;
}> {
  return governedJSON(
    `/v1/agent-tool-approvals/${encodeURIComponent(approvalID)}/decision`,
    key,
    { method: 'POST', body: JSON.stringify({ decision, reason }) },
    fetcher
  );
}

export function getAgentGovernanceAnalytics(
  key: string,
  fetcher: typeof fetch = recordingFetch
): Promise<AgentAnalyticsReport> {
  return governedJSON('/v1/agent-governance/analytics', key, {}, fetcher);
}

export async function listCaseAgentAssists(
  key: string,
  caseID: string,
  fetcher: typeof fetch = recordingFetch
): Promise<AgentAssist[]> {
  const body = await governedJSON<{ assists: AgentAssist[] }>(
    `/v1/cases/${encodeURIComponent(caseID)}/agent-assists`,
    key,
    {},
    fetcher
  );
  return body.assists ?? [];
}

export function requestCaseAgentAssist(
  key: string,
  caseID: string,
  request: {
    kind: string;
    template_id: string;
    release: number;
    environment: Environment;
    evidence_ids: string[];
  },
  idempotencyKey: string,
  fetcher: typeof fetch = recordingFetch
): Promise<{
  assist_id: string;
  status: AgentAssist['status'];
  approval_id?: string;
  result?: AgentAssist['result'];
  seq?: number;
}> {
  return governedJSON(
    `/v1/cases/${encodeURIComponent(caseID)}/agent-assists`,
    key,
    {
      method: 'POST',
      headers: { ...jsonHeaders(key), 'Idempotency-Key': idempotencyKey },
      body: JSON.stringify(request)
    },
    fetcher
  );
}

export function recordAgentAssistAction(
  key: string,
  assistID: string,
  action: {
    action: 'accepted' | 'edited' | 'rejected' | 'escalated';
    final?: Record<string, unknown>;
    reason?: string;
    time_saved_ms?: number;
  },
  fetcher: typeof fetch = recordingFetch
): Promise<{ seq: number }> {
  return governedJSON(
    `/v1/agent-assists/${encodeURIComponent(assistID)}/reviewer-action`,
    key,
    { method: 'POST', body: JSON.stringify({ assist_id: assistID, ...action }) },
    fetcher
  );
}

export function retryAgentAssist(
  key: string,
  assistID: string,
  reason: string,
  acknowledgeAtLeastOnce: boolean,
  fetcher: typeof fetch = recordingFetch
): Promise<{ event_id: string; seq: number }> {
  return governedJSON(
    `/v1/agent-assists/${encodeURIComponent(assistID)}/retry`,
    key,
    {
      method: 'POST',
      body: JSON.stringify({
        reason,
        acknowledge_at_least_once: acknowledgeAtLeastOnce
      })
    },
    fetcher
  );
}

export function cancelAgentAssist(
  key: string,
  assistID: string,
  reason: string,
  fetcher: typeof fetch = recordingFetch
): Promise<{ event_id: string; seq: number }> {
  return governedJSON(
    `/v1/agent-assists/${encodeURIComponent(assistID)}/cancel`,
    key,
    { method: 'POST', body: JSON.stringify({ reason }) },
    fetcher
  );
}

export async function runAgent(
  key: string,
  name: string,
  prompt: string,
  version = 0, // 0 = latest; pin a published version
  fetcher: typeof fetch = recordingFetch
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

export interface AsyncAgentRunOptions {
  version?: number;
  timeoutMs?: number;
  maxAttempts?: number;
  idempotencyKey?: string;
  businessReference?: string;
  correlationId?: string;
}

export async function startAgentRun(
  key: string,
  name: string,
  prompt: string,
  options: AsyncAgentRunOptions = {},
  fetcher: typeof fetch = recordingFetch
): Promise<RunResult> {
  const headers = jsonHeaders(key);
  if (options.idempotencyKey) headers['Idempotency-Key'] = options.idempotencyKey;
  if (options.correlationId) headers['X-Correlation-ID'] = options.correlationId;
  const res = await fetcher(`/v1/agents/${name}/run`, {
    method: 'POST',
    headers,
    body: JSON.stringify({
      prompt,
      async: true,
      version: options.version || undefined,
      timeout_ms: options.timeoutMs || undefined,
      max_attempts: options.maxAttempts || undefined,
      business_reference: options.businessReference || undefined,
      correlation_id: options.correlationId || undefined
    })
  });
  if (!res.ok) {
    return errorOrStatus(res, `POST /v1/agents/${name}/run`);
  }
  return (await res.json()) as RunResult;
}

export async function cancelAgentRun(
  key: string,
  runID: string,
  fetcher: typeof fetch = recordingFetch
): Promise<{ status: 'cancelling'; seq: number }> {
  const res = await fetcher(`/v1/agent-runs/${runID}/cancel`, {
    method: 'POST',
    headers: jsonHeaders(key),
    body: '{}'
  });
  if (!res.ok) return errorOrStatus(res, `POST /v1/agent-runs/${runID}/cancel`);
  return (await res.json()) as { status: 'cancelling'; seq: number };
}

export async function retryAgentRun(
  key: string,
  runID: string,
  reason: string,
  acknowledgeAtLeastOnce = false,
  fetcher: typeof fetch = recordingFetch
): Promise<{ status: 'retrying'; seq: number }> {
  const res = await fetcher(`/v1/agent-runs/${runID}/retry`, {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify({
      reason,
      acknowledge_at_least_once: acknowledgeAtLeastOnce || undefined
    })
  });
  if (!res.ok) return errorOrStatus(res, `POST /v1/agent-runs/${runID}/retry`);
  return (await res.json()) as { status: 'retrying'; seq: number };
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
  fetcher: typeof fetch = recordingFetch
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
  fetcher: typeof fetch = recordingFetch
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
  fetcher: typeof fetch = recordingFetch
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
  fetcher: typeof fetch = recordingFetch
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
  prompt_tokens?: number;
  output_tokens?: number;
  cost_usd?: number;
  assist_accepted?: number;
  assist_edited?: number;
  assist_rejected?: number;
  assist_escalated?: number;
  open_incidents?: number;
}

export interface MrmModel {
  kind: MrmModelKind;
  id: string;
  name: string;
  governed_agent?: boolean;
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
export async function getMrmReport(
  key: string,
  fetcher: typeof fetch = recordingFetch
): Promise<MrmReport> {
  const res = await fetcher('/v1/mrm/report', { headers: authHeaders(key) });
  if (!res.ok) {
    return errorOrStatus(res, 'GET /v1/mrm/report');
  }
  const report = (await res.json()) as MrmReport;
  // Go encodes an empty slice as null, and the page reads report.models.length —
  // which threw and blanked the whole model-risk surface for any workspace with no
  // models yet. Normalize once here, as normalizeFlow does for a flow's versions,
  // rather than leaving every consumer to remember a null guard.
  return { ...report, models: report.models ?? [] };
}
// The MRM report as CSV/Markdown text — the page wraps it in a Blob download (an
// <a href> would escape the demo's fetch mock and 404 on the static host).
export async function mrmReportText(
  key: string,
  format: 'csv' | 'md',
  fetcher: typeof fetch = recordingFetch
): Promise<string> {
  const res = await fetcher(`/v1/mrm/report?format=${format}`, { headers: authHeaders(key) });
  if (!res.ok) {
    return errorOrStatus(res, `export mrm report (${format}) failed`);
  }
  return res.text();
}

export interface FairLendingGroup {
  value: string;
  total: number;
  favorable: number;
  adverse: number;
  rate: number;
  air: number;
  reference?: boolean;
  flagged?: boolean;
  small_sample?: boolean;
}
export interface FairLendingReport {
  generated_at: string;
  org: string;
  workspace: string;
  flow_id: string;
  attribute: string;
  favorable: string;
  environment?: string;
  experiment_id?: string;
  cohort?: number;
  arm?: string;
  groups: FairLendingGroup[];
  reference: string;
  min_air: number;
  passes: boolean;
  decisions: number;
  excluded: number;
  two_groups: boolean;
}
export interface FairLendingParams {
  flow: string;
  attribute: string;
  favorable?: string;
  env?: string;
  experiment?: string;
  cohort?: number;
  arm?: string;
}

// fairLendingQuery serializes the report parameters, dropping the blank optionals
// so the request carries only what was set.
function fairLendingQuery(p: FairLendingParams): string {
  const q = new URLSearchParams({ flow: p.flow, attribute: p.attribute });
  if (p.favorable) q.set('favorable', p.favorable);
  if (p.env) q.set('env', p.env);
  if (p.experiment) q.set('experiment', p.experiment);
  if (p.cohort) q.set('cohort', String(p.cohort));
  if (p.arm) q.set('arm', p.arm);
  return q.toString();
}

// getFairLendingReport fetches the disparate-impact report for a flow (admin-gated).
export async function getFairLendingReport(
  key: string,
  params: FairLendingParams,
  fetcher: typeof fetch = recordingFetch
): Promise<FairLendingReport> {
  const res = await fetcher(`/v1/fairlending/report?${fairLendingQuery(params)}`, {
    headers: authHeaders(key)
  });
  if (!res.ok) {
    return errorOrStatus(res, 'GET /v1/fairlending/report');
  }
  return (await res.json()) as FairLendingReport;
}
// The report as CSV/Markdown text — the page wraps it in a Blob download (an
// <a href> would escape the demo's fetch mock and 404 on the static host).
export async function fairLendingReportText(
  key: string,
  params: FairLendingParams,
  format: 'csv' | 'md',
  fetcher: typeof fetch = recordingFetch
): Promise<string> {
  const res = await fetcher(`/v1/fairlending/report?${fairLendingQuery(params)}&format=${format}`, {
    headers: authHeaders(key)
  });
  if (!res.ok) {
    return errorOrStatus(res, `export fair-lending report (${format}) failed`);
  }
  return res.text();
}

export interface FairLendingConfig {
  flow_id: string;
  attribute: string;
  favorable: string;
  threshold: number;
  updated_at?: string;
  updated_by?: string;
}
// getFairLendingConfig fetches a flow's stored fair-lending config (empty when unset).
export async function getFairLendingConfig(
  key: string,
  flowId: string,
  fetcher: typeof fetch = recordingFetch
): Promise<FairLendingConfig> {
  const res = await fetcher(`/v1/flows/${encodeURIComponent(flowId)}/fairlending`, {
    headers: authHeaders(key)
  });
  if (!res.ok) {
    return errorOrStatus(res, 'GET fair-lending config');
  }
  return (await res.json()) as FairLendingConfig;
}
// setFairLendingConfig stores a flow's protected-attribute / favorable / threshold (admin).
export async function setFairLendingConfig(
  key: string,
  flowId: string,
  body: { attribute: string; favorable?: string; threshold?: number },
  fetcher: typeof fetch = recordingFetch
): Promise<void> {
  const res = await fetcher(`/v1/flows/${encodeURIComponent(flowId)}/fairlending`, {
    method: 'PUT',
    headers: { ...authHeaders(key), 'Content-Type': 'application/json' },
    body: JSON.stringify(body)
  });
  if (!res.ok) {
    return errorOrStatus(res, 'PUT fair-lending config');
  }
}

export interface AdverseActionSettings {
  creditor_name: string;
  creditor_address?: string;
  creditor_phone?: string;
  enforcement_agency?: string;
  // Consumer reporting agency, required by FCRA §615(a) for a report-based notice.
  cra_name?: string;
  cra_address?: string;
  cra_phone?: string;
  updated_at?: string;
  updated_by?: string;
}
// AdverseActionIssuance is the durable record that a declined applicant was served
// their notice — proof ECOA/Reg B expects a creditor to keep (who, when, how, citing
// what, the immutable artifact's media type, and its integrity hash).
export interface AdverseActionIssuance {
  decision_id: string;
  subject?: string;
  method: string;
  based_on_consumer_report: boolean;
  principal_reasons: string[];
  content_hash: string;
  hash_algo: string;
  content_type: string;
  issued_at: string;
  issued_by: string;
}
// AdverseActionItem is one row of the pending/issued work queue.
export interface AdverseActionItem {
  decision_id: string;
  flow_id: string;
  slug: string;
  subject?: string;
  decided_at: string;
  age_days: number;
  issued: boolean;
  issuance?: AdverseActionIssuance;
}
// getAdverseActionSettings fetches the workspace creditor identification for notices.
export async function getAdverseActionSettings(
  key: string,
  fetcher: typeof fetch = recordingFetch
): Promise<AdverseActionSettings> {
  const res = await fetcher('/v1/fairlending/settings', { headers: authHeaders(key) });
  if (!res.ok) {
    return errorOrStatus(res, 'GET adverse-action settings');
  }
  return (await res.json()) as AdverseActionSettings;
}
// setAdverseActionSettings stores the workspace creditor identification (admin).
export async function setAdverseActionSettings(
  key: string,
  body: AdverseActionSettings,
  fetcher: typeof fetch = recordingFetch
): Promise<void> {
  const res = await fetcher('/v1/fairlending/settings', {
    method: 'PUT',
    headers: { ...authHeaders(key), 'Content-Type': 'application/json' },
    body: JSON.stringify(body)
  });
  if (!res.ok) {
    return errorOrStatus(res, 'PUT adverse-action settings');
  }
}
// adverseActionNotice fetches the ECOA / Reg B notice for a declined decision as text
// (wrapped in a Blob download by the caller, so it survives the demo's fetch mock).
export async function adverseActionNotice(
  key: string,
  decisionId: string,
  fetcher: typeof fetch = recordingFetch
): Promise<string> {
  const res = await fetcher(`/v1/decisions/${encodeURIComponent(decisionId)}/adverse-action`, {
    headers: authHeaders(key)
  });
  if (!res.ok) {
    return errorOrStatus(res, 'generate adverse-action notice');
  }
  return res.text();
}

// issuedAdverseActionNotice fetches the exact immutable artifact retained when the
// notice was recorded as served; it never re-renders from current settings.
export async function issuedAdverseActionNotice(
  key: string,
  decisionId: string,
  fetcher: typeof fetch = recordingFetch
): Promise<string> {
  const res = await fetcher(
    `/v1/decisions/${encodeURIComponent(decisionId)}/adverse-action/issued`,
    { headers: authHeaders(key) }
  );
  if (!res.ok) {
    return errorOrStatus(res, 'download issued adverse-action artifact');
  }
  return res.text();
}
// getAdverseAction fetches the rendered notice plus any recorded issuance as JSON. The
// consumerReport flag adds the FCRA §615(a) disclosures to the rendered preview.
export async function getAdverseAction(
  key: string,
  decisionId: string,
  consumerReport = false,
  fetcher: typeof fetch = recordingFetch
): Promise<{ notice: string; issuance?: AdverseActionIssuance }> {
  const q = consumerReport ? '&consumer_report=true' : '';
  const res = await fetcher(
    `/v1/decisions/${encodeURIComponent(decisionId)}/adverse-action?format=json${q}`,
    { headers: authHeaders(key) }
  );
  if (!res.ok) {
    return errorOrStatus(res, 'get adverse-action');
  }
  return (await res.json()) as { notice: string; issuance?: AdverseActionIssuance };
}
// issueAdverseAction records that the notice for a declined decision was served, by a
// delivery method, optionally marking it as based on a consumer report (FCRA).
export async function issueAdverseAction(
  key: string,
  decisionId: string,
  body: { method: string; based_on_consumer_report: boolean },
  fetcher: typeof fetch = recordingFetch
): Promise<EventAck> {
  const res = await fetcher(
    `/v1/decisions/${encodeURIComponent(decisionId)}/adverse-action/issue`,
    { method: 'POST', headers: jsonHeaders(key), body: JSON.stringify(body) }
  );
  if (!res.ok) {
    return errorOrStatus(res, 'issue adverse-action notice');
  }
  return (await res.json()) as EventAck;
}
// listAdverseActions returns the declined decisions and their notice status — the work
// queue. status 'pending' returns declines not yet served; 'issued' those served.
export async function listAdverseActions(
  key: string,
  status: 'pending' | 'issued' | '' = '',
  fetcher: typeof fetch = recordingFetch
): Promise<AdverseActionItem[]> {
  const q = status ? `?status=${status}` : '';
  const res = await fetcher(`/v1/adverse-actions${q}`, { headers: authHeaders(key) });
  if (!res.ok) {
    return errorOrStatus(res, 'list adverse-actions');
  }
  return ((await res.json()) as { adverse_actions: AdverseActionItem[] }).adverse_actions ?? [];
}

// Reconsideration is the human review of a solely-automated adverse decision — the
// Art. 22 / ECOA record that a person, not the model, upheld or overturned the decline.
export interface Reconsideration {
  decision_id: string;
  subject?: string;
  basis: string;
  outcome: string;
  rationale: string;
  reviewed_at: string;
  reviewed_by: string;
}
// decisionExplanation fetches the GDPR Art. 22 subject-facing explanation of a decision
// (how it was made, the factors, the subject's rights) as Markdown text, for the caller
// to download as a Blob.
export async function decisionExplanation(
  key: string,
  decisionId: string,
  fetcher: typeof fetch = recordingFetch
): Promise<string> {
  const res = await fetcher(`/v1/decisions/${encodeURIComponent(decisionId)}/explanation`, {
    headers: authHeaders(key)
  });
  if (!res.ok) {
    return errorOrStatus(res, 'get decision explanation');
  }
  return res.text();
}
// getReconsideration returns the recorded human review for a decision, if any.
export async function getReconsideration(
  key: string,
  decisionId: string,
  fetcher: typeof fetch = recordingFetch
): Promise<Reconsideration | null> {
  const res = await fetcher(`/v1/decisions/${encodeURIComponent(decisionId)}/reconsideration`, {
    headers: authHeaders(key)
  });
  if (!res.ok) {
    return errorOrStatus(res, 'get reconsideration');
  }
  const body = (await res.json()) as { reviewed: boolean; review?: Reconsideration };
  return body.reviewed ? (body.review ?? null) : null;
}
// recordReconsideration logs a human review (upheld/overturned + rationale) of an
// automated decline. Rejected unless the decision is a completed, solely-automated
// decline (the Art. 22 predicate).
export async function recordReconsideration(
  key: string,
  decisionId: string,
  body: { basis: string; outcome: string; rationale: string },
  fetcher: typeof fetch = recordingFetch
): Promise<EventAck> {
  const res = await fetcher(`/v1/decisions/${encodeURIComponent(decisionId)}/reconsideration`, {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify(body)
  });
  if (!res.ok) {
    return errorOrStatus(res, 'record reconsideration');
  }
  return (await res.json()) as EventAck;
}
// Contest is a subject's dispute of an automated decision — opened when logged, and
// resolved once a human review is recorded for the same decision.
export interface Contest {
  decision_id: string;
  subject?: string;
  channel: string;
  note?: string;
  received_at: string;
  received_by: string;
  resolved: boolean;
  resolved_at?: string;
}
// getContest returns the contest for a decision, if one has been logged.
export async function getContest(
  key: string,
  decisionId: string,
  fetcher: typeof fetch = recordingFetch
): Promise<Contest | null> {
  const res = await fetcher(`/v1/decisions/${encodeURIComponent(decisionId)}/contest`, {
    headers: authHeaders(key)
  });
  if (!res.ok) {
    return errorOrStatus(res, 'get contest');
  }
  const body = (await res.json()) as { contested: boolean; contest?: Contest };
  return body.contested ? (body.contest ?? null) : null;
}
// recordContest logs that a subject contested an automated decline (their right to
// contest), by the channel they used. A later human review resolves it.
export async function recordContest(
  key: string,
  decisionId: string,
  body: { channel: string; note?: string },
  fetcher: typeof fetch = recordingFetch
): Promise<EventAck> {
  const res = await fetcher(`/v1/decisions/${encodeURIComponent(decisionId)}/contest`, {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify(body)
  });
  if (!res.ok) {
    return errorOrStatus(res, 'record contest');
  }
  return (await res.json()) as EventAck;
}
// listContests returns the tenant's contests. status 'open' returns those awaiting a
// review; 'resolved' those a review has closed.
export async function listContests(
  key: string,
  status: 'open' | 'resolved' | '' = '',
  fetcher: typeof fetch = recordingFetch
): Promise<Contest[]> {
  const q = status ? `?status=${status}` : '';
  const res = await fetcher(`/v1/contests${q}`, { headers: authHeaders(key) });
  if (!res.ok) {
    return errorOrStatus(res, 'list contests');
  }
  return ((await res.json()) as { contests: Contest[] }).contests ?? [];
}
// listReconsiderations returns every recorded human review for the tenant (the audit
// trail behind the compliance surface).
export async function listReconsiderations(
  key: string,
  fetcher: typeof fetch = recordingFetch
): Promise<Reconsideration[]> {
  const res = await fetcher('/v1/reconsiderations', { headers: authHeaders(key) });
  if (!res.ok) {
    return errorOrStatus(res, 'list reconsiderations');
  }
  return ((await res.json()) as { reconsiderations: Reconsideration[] }).reconsiderations ?? [];
}

// LegalHold suspends erasure/retention for a subject under an investigation or dispute.
export interface LegalHold {
  subject: string;
  reason?: string;
  since: string;
}
// RetentionPolicy is the workspace's retention window (0 = keep indefinitely).
export interface RetentionPolicy {
  retention_days: number;
}
// ErasureStatus reports the independent crypto-shred and legal-hold states for
// one subject. Statutory retention is served by getRetentionStatus below because
// it is derived from regulatory records rather than the key vault.
export interface ErasureStatus {
  subject: string;
  erased: boolean;
  held: boolean;
}
export interface RetentionSweepResult {
  erased: number;
  held: number;
  statutory_retained: number;
  max_age_days: number;
}
// exportComplianceRegister fetches a compliance register (adverse-actions,
// reconsiderations, or consent) as CSV or Markdown text, for the caller to download
// as a Blob — the examiner-ready artifact behind the compliance dashboard.
export async function exportComplianceRegister(
  key: string,
  register: 'adverse-actions' | 'reconsiderations' | 'consent',
  format: 'csv' | 'md' = 'csv',
  fetcher: typeof fetch = recordingFetch
): Promise<string> {
  const res = await fetcher(`/v1/compliance/registers/${register}?format=${format}`, {
    headers: authHeaders(key)
  });
  if (!res.ok) {
    return errorOrStatus(res, `export ${register} register`);
  }
  return res.text();
}
// listLegalHolds returns the tenant's active legal holds (admin).
export async function listLegalHolds(
  key: string,
  fetcher: typeof fetch = recordingFetch
): Promise<LegalHold[]> {
  const res = await fetcher('/v1/erasure/holds', { headers: authHeaders(key) });
  if (!res.ok) {
    return errorOrStatus(res, 'list legal holds');
  }
  return ((await res.json()) as { held: LegalHold[] }).held ?? [];
}
// getRetentionPolicy returns the workspace retention window (admin).
export async function getRetentionPolicy(
  key: string,
  fetcher: typeof fetch = recordingFetch
): Promise<RetentionPolicy> {
  const res = await fetcher('/v1/erasure/retention-policy', { headers: authHeaders(key) });
  if (!res.ok) {
    return errorOrStatus(res, 'get retention policy');
  }
  return (await res.json()) as RetentionPolicy;
}
// listErasedSubjects returns the subject keys that have been crypto-shredded (admin).
export async function listErasedSubjects(
  key: string,
  fetcher: typeof fetch = recordingFetch
): Promise<string[]> {
  const res = await fetcher('/v1/erasure/subjects', { headers: authHeaders(key) });
  if (!res.ok) {
    return errorOrStatus(res, 'list erased subjects');
  }
  return ((await res.json()) as { erased: string[] }).erased ?? [];
}
// getErasureStatus returns whether one subject has been crypto-shredded or is
// protected by a manual legal hold (admin).
export async function getErasureStatus(
  key: string,
  subject: string,
  fetcher: typeof fetch = recordingFetch
): Promise<ErasureStatus> {
  const res = await fetcher(`/v1/erasure/subjects/${encodeURIComponent(subject)}`, {
    headers: authHeaders(key)
  });
  if (!res.ok) {
    return errorOrStatus(res, 'get erasure status');
  }
  return (await res.json()) as ErasureStatus;
}
// holdErasureSubject places a legal hold on an existing subject key (admin).
export async function holdErasureSubject(
  key: string,
  subject: string,
  reason: string,
  fetcher: typeof fetch = recordingFetch
): Promise<void> {
  const res = await fetcher(`/v1/erasure/subjects/${encodeURIComponent(subject)}/hold`, {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify({ reason })
  });
  if (!res.ok) {
    return errorOrStatus(res, 'place legal hold');
  }
}
// releaseErasureSubject deliberately lifts a legal hold (admin).
export async function releaseErasureSubject(
  key: string,
  subject: string,
  fetcher: typeof fetch = recordingFetch
): Promise<void> {
  const res = await fetcher(`/v1/erasure/subjects/${encodeURIComponent(subject)}/release`, {
    method: 'POST',
    headers: jsonHeaders(key)
  });
  if (!res.ok) {
    return errorOrStatus(res, 'release legal hold');
  }
}
// eraseSubject permanently destroys a subject's encryption key (admin).
export async function eraseSubject(
  key: string,
  subject: string,
  fetcher: typeof fetch = recordingFetch
): Promise<void> {
  const res = await fetcher(`/v1/erasure/subjects/${encodeURIComponent(subject)}`, {
    method: 'POST',
    headers: jsonHeaders(key)
  });
  if (!res.ok) {
    return errorOrStatus(res, 'erase subject');
  }
}
// setRetentionPolicy stores the scheduled retention window. Zero disables it;
// this call never performs an erasure itself.
export async function setRetentionPolicy(
  key: string,
  retentionDays: number,
  fetcher: typeof fetch = recordingFetch
): Promise<RetentionPolicy> {
  const res = await fetcher('/v1/erasure/retention-policy', {
    method: 'PUT',
    headers: jsonHeaders(key),
    body: JSON.stringify({ retention_days: retentionDays })
  });
  if (!res.ok) {
    return errorOrStatus(res, 'set retention policy');
  }
  return (await res.json()) as RetentionPolicy;
}
// runRetentionSweep immediately crypto-shreds every eligible subject older than
// the requested window, while preserving manual holds and statutory retention.
export async function runRetentionSweep(
  key: string,
  maxAgeDays: number,
  fetcher: typeof fetch = recordingFetch
): Promise<RetentionSweepResult> {
  const res = await fetcher(`/v1/erasure/retention?max_age_days=${maxAgeDays}`, {
    method: 'POST',
    headers: jsonHeaders(key)
  });
  if (!res.ok) {
    return errorOrStatus(res, 'run retention sweep');
  }
  return (await res.json()) as RetentionSweepResult;
}

// ConsentEvidence is the proof backing a grant — the document/audit-trail a regulator
// asks the controller to produce. The signed artifact stays in the controller's own
// system of record; we hold a tamper-evident reference (content hash) and the capture
// metadata, so the bytes never leave the tenant (data residency).
export interface ConsentEvidence {
  method?: string;
  reference?: string;
  content_hash?: string;
  hash_algo?: string;
  notice_version?: string;
}
export interface ConsentRecord {
  subject: string;
  purpose: string;
  granted: boolean;
  active: boolean;
  basis?: string;
  granted_at?: string;
  withdrawn_at?: string;
  expires_at?: string;
  evidence?: ConsentEvidence;
  updated_by: string;
}
// getConsents lists a data subject's consent records across purposes (the subject is
// opaque — the decide integration keys it as "type/id").
export async function getConsents(
  key: string,
  subject: string,
  fetcher: typeof fetch = recordingFetch
): Promise<ConsentRecord[]> {
  const res = await fetcher(`/v1/consent?subject=${encodeURIComponent(subject)}`, {
    headers: authHeaders(key)
  });
  if (!res.ok) {
    return errorOrStatus(res, 'GET /v1/consent');
  }
  return ((await res.json()) as { consents: ConsentRecord[] }).consents ?? [];
}
// listConsentRecords returns every consent/lawful-basis record in the tenant — the
// cross-subject view for the compliance surface (per-subject getConsents answers a DSAR).
export async function listConsentRecords(
  key: string,
  fetcher: typeof fetch = recordingFetch
): Promise<ConsentRecord[]> {
  const res = await fetcher('/v1/consent/records', { headers: authHeaders(key) });
  if (!res.ok) {
    return errorOrStatus(res, 'list consent records');
  }
  return ((await res.json()) as { consents: ConsentRecord[] }).consents ?? [];
}
// grantConsent records a subject's consent for a purpose. Provided by the operating
// business's staff (a bank/insurer/fintech employee), not the end customer.
export async function grantConsent(
  key: string,
  body: {
    subject: string;
    purpose: string;
    basis?: string;
    expires_at?: string;
    evidence?: ConsentEvidence;
  },
  fetcher: typeof fetch = recordingFetch
): Promise<EventAck> {
  const res = await fetcher('/v1/consent/grant', {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify(body)
  });
  if (!res.ok) {
    return errorOrStatus(res, 'POST /v1/consent/grant');
  }
  return (await res.json()) as EventAck;
}
export async function withdrawConsent(
  key: string,
  body: { subject: string; purpose: string; reason?: string },
  fetcher: typeof fetch = recordingFetch
): Promise<EventAck> {
  const res = await fetcher('/v1/consent/withdraw', {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify(body)
  });
  if (!res.ok) {
    return errorOrStatus(res, 'POST /v1/consent/withdraw');
  }
  return (await res.json()) as EventAck;
}

// SharingRecord is a subject's GLBA election to stop (or resume) NPI sharing with
// nonaffiliated third parties — the opt-out mirror of a consent record.
export interface SharingRecord {
  subject: string;
  opted_out: boolean;
  reason?: string;
  opted_out_at?: string;
  updated_at: string;
  updated_by: string;
}
export interface SharingStatus {
  opted_out: boolean;
  record?: SharingRecord;
}
// getSharingStatus returns a subject's sharing opt-out state.
export async function getSharingStatus(
  key: string,
  subject: string,
  fetcher: typeof fetch = recordingFetch
): Promise<SharingStatus> {
  const res = await fetcher(`/v1/sharing?subject=${encodeURIComponent(subject)}`, {
    headers: authHeaders(key)
  });
  if (!res.ok) {
    return errorOrStatus(res, 'GET /v1/sharing');
  }
  return (await res.json()) as SharingStatus;
}
// optOutSharing records a subject's election to stop NPI sharing (GLBA §6802).
export async function optOutSharing(
  key: string,
  body: { subject: string; reason?: string },
  fetcher: typeof fetch = recordingFetch
): Promise<void> {
  const res = await fetcher('/v1/sharing/opt-out', {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify(body)
  });
  if (!res.ok) {
    return errorOrStatus(res, 'POST /v1/sharing/opt-out');
  }
}
// optInSharing rescinds a subject's prior opt-out (opting back in to sharing).
export async function optInSharing(
  key: string,
  subject: string,
  fetcher: typeof fetch = recordingFetch
): Promise<void> {
  const res = await fetcher('/v1/sharing/opt-in', {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify({ subject })
  });
  if (!res.ok) {
    return errorOrStatus(res, 'POST /v1/sharing/opt-in');
  }
}
// JurisdictionSetting is the data-protection / fair-lending regimes a workspace operates
// under — which law the automated-decision explanation cites.
export interface JurisdictionSetting {
  regimes: string[]; // any of 'eu' | 'uk' | 'us'
  configured: boolean;
  updated_at?: string;
  updated_by?: string;
}
// getJurisdiction returns the workspace's applicable regimes (defaults to all when unset).
export async function getJurisdiction(
  key: string,
  fetcher: typeof fetch = recordingFetch
): Promise<JurisdictionSetting> {
  const res = await fetcher('/v1/compliance/jurisdiction', { headers: authHeaders(key) });
  if (!res.ok) {
    return errorOrStatus(res, 'get jurisdiction');
  }
  return (await res.json()) as JurisdictionSetting;
}
// setJurisdiction replaces the workspace's applicable regimes (admin).
export async function setJurisdiction(
  key: string,
  regimes: string[],
  fetcher: typeof fetch = recordingFetch
): Promise<void> {
  const res = await fetcher('/v1/compliance/jurisdiction', {
    method: 'PUT',
    headers: jsonHeaders(key),
    body: JSON.stringify({ regimes })
  });
  if (!res.ok) {
    return errorOrStatus(res, 'set jurisdiction');
  }
}
// RetentionStatus is how long a subject's compliance records must be kept, and thus
// whether the subject may be erased (GDPR Art. 17(3)(b) exempts required retention).
export interface RetentionStatus {
  subject: string;
  retained: boolean;
  retain_until?: string;
  items: { kind: string; record_id: string; basis: string; retain_until: string }[];
}
// getRetentionStatus returns a subject's statutory record-retention status.
export async function getRetentionStatus(
  key: string,
  subject: string,
  fetcher: typeof fetch = recordingFetch
): Promise<RetentionStatus> {
  const res = await fetcher(`/v1/retention?subject=${encodeURIComponent(subject)}`, {
    headers: authHeaders(key)
  });
  if (!res.ok) {
    return errorOrStatus(res, 'GET /v1/retention');
  }
  return (await res.json()) as RetentionStatus;
}
// listSharingRecords returns every sharing record in the tenant (the compliance view).
export async function listSharingRecords(
  key: string,
  fetcher: typeof fetch = recordingFetch
): Promise<SharingRecord[]> {
  const res = await fetcher('/v1/sharing/records', { headers: authHeaders(key) });
  if (!res.ok) {
    return errorOrStatus(res, 'list sharing records');
  }
  return ((await res.json()) as { records: SharingRecord[] }).records ?? [];
}

export async function listAgentRuns(
  key: string,
  name: string,
  fetcher: typeof fetch = recordingFetch
): Promise<AgentRun[]> {
  const res = await fetcher(`/v1/agents/${name}/runs`, { headers: authHeaders(key) });
  if (!res.ok) {
    return errorOrStatus(res, `GET /v1/agents/${name}/runs`);
  }
  return ((await res.json()) as { runs: AgentRun[] }).runs ?? [];
}

export async function getRunSummary(
  key: string,
  fetcher: typeof fetch = recordingFetch
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
  fetcher: typeof fetch = recordingFetch
): Promise<{ case_id: string; event_id: string; seq: number }> {
  const res = await fetcher(`/v1/agents/${name}/runs/${runID}/escalate`, {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify(body)
  });
  if (!res.ok) {
    return errorOrStatus(res, `POST /v1/agents/${name}/runs/${runID}/escalate`);
  }
  return (await res.json()) as { case_id: string; event_id: string; seq: number };
}

export interface Identity {
  org: string;
  workspace: string;
  actor: string;
  username?: string;
  email?: string;
  role?: string; // viewer|operator|editor|approver|admin — present from /v1/me
  scope?: string;
}

// login exchanges an API key for a session cookie (set by the server).
export async function login(
  apiKey: string,
  fetcher: typeof fetch = recordingFetch
): Promise<Identity> {
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

// logout revokes the current application session and returns the server-built
// RP-Initiated Logout URL for an SSO session, if the provider advertises one.
export async function logout(fetcher: typeof fetch = recordingFetch): Promise<string> {
  const res = await fetcher('/v1/logout', {
    method: 'POST',
    headers: { 'X-Requested-With': 'intraktible' }
  });
  if (!res.ok) {
    return errorOrStatus(res, 'POST /v1/logout');
  }
  const body = (await res.json()) as { logout_url?: unknown };
  if (typeof body.logout_url !== 'string') {
    throw new Error('POST /v1/logout returned an invalid logout_url');
  }
  return body.logout_url;
}

// listSsoProviders returns the configured OIDC providers (e.g. ["google","aws"])
// so the login page can offer a "Sign in with …" button for each. A successful
// empty list means OIDC is not configured; an unavailable or malformed discovery
// response is an error, because treating it as empty would expose a different
// sign-in method during an identity-service outage.
export async function listSsoProviders(fetcher: typeof fetch = recordingFetch): Promise<string[]> {
  const res = await fetcher('/v1/auth/oidc/providers');
  if (!res.ok) {
    return errorOrStatus(res, 'GET /v1/auth/oidc/providers');
  }
  return providerNames(await res.json(), 'GET /v1/auth/oidc/providers');
}

// listSamlProviders returns the configured SAML providers, mirroring
// listSsoProviders. A successful empty list means SAML is not configured.
export async function listSamlProviders(fetcher: typeof fetch = recordingFetch): Promise<string[]> {
  const res = await fetcher('/v1/auth/saml/providers');
  if (!res.ok) {
    return errorOrStatus(res, 'GET /v1/auth/saml/providers');
  }
  return providerNames(await res.json(), 'GET /v1/auth/saml/providers');
}

function providerNames(body: unknown, endpoint: string): string[] {
  if (
    typeof body !== 'object' ||
    body === null ||
    !('providers' in body) ||
    !Array.isArray(body.providers) ||
    !body.providers.every((provider) => typeof provider === 'string' && provider.length > 0)
  ) {
    throw new Error(`${endpoint} returned an invalid provider list`);
  }
  return body.providers;
}

// currentUser returns the signed-in identity from the session cookie, or null
// when there is no valid session.
export async function currentUser(
  fetcher: typeof fetch = recordingFetch
): Promise<Identity | null> {
  const res = await fetcher('/v1/me');
  if (res.status === 401) {
    return null;
  }
  if (!res.ok) {
    return errorOrStatus(res, 'GET /v1/me');
  }
  return (await res.json()) as Identity;
}

// --- Experiments, business outcomes, and durable population jobs ----------------

export interface ExperimentArm {
  key: string;
  name: string;
  kind: ExperimentArmKind;
  version: number;
  allocation_bps: number;
}

export interface ExperimentMetric {
  key: string;
  name: string;
  kind: ExperimentMetricKind;
  direction: ExperimentDirection;
  max_regression?: number;
}

export interface ExperimentSpec {
  name: string;
  hypothesis: string;
  owner: string;
  flow_id: string;
  environment: Environment;
  subject_key_expression: string;
  eligibility_expression?: string;
  salt: string;
  arms: ExperimentArm[];
  primary_metric: ExperimentMetric;
  guardrails?: ExperimentMetric[];
  minimum_sample_per_arm: number;
  minimum_effect: number;
  confidence: number;
  observation_window_days: number;
  start_at?: string;
  stop_at?: string;
}

export interface ExperimentLaunchRequest {
  request_id: string;
  cohort: number;
  status: 'pending' | 'approved' | 'rejected';
  requested_by: string;
  requested_at: string;
  decided_by?: string;
  decided_at?: string;
  reason?: string;
}

export interface Experiment {
  experiment_id: string;
  cohort: number;
  state: ExperimentState;
  spec: ExperimentSpec;
  created_by: string;
  created_at: string;
  updated_by: string;
  updated_at: string;
  started_at?: string;
  ended_at?: string;
  launch?: ExperimentLaunchRequest;
}

export interface ExperimentExposure {
  experiment_id: string;
  cohort: number;
  decision_id: string;
  flow_id: string;
  environment: Environment;
  arm_key: string;
  arm_name: string;
  arm_kind: ExperimentArmKind;
  version: number;
  subject_hash: string;
  reached_at: string;
}

export interface ExperimentArmMetric {
  arm_key: string;
  count: number;
  mean: number;
  std_dev: number;
  interval: { low: number; high: number };
}

export interface ExperimentComparison {
  arm_key: string;
  effect: number;
  effect_size: number;
  interval: { low: number; high: number };
  favorable: boolean;
}

export interface ExperimentMetricAnalysis {
  metric: ExperimentMetric;
  arms: ExperimentArmMetric[];
  comparisons: ExperimentComparison[];
  excluded: number;
  label_version?: string;
}

export interface ExperimentAnalysis {
  experiment_id: string;
  cohort: number;
  state: ExperimentState;
  status: ExperimentAnalysisStatus;
  reason: string;
  winner_arm_key?: string;
  exposure_counts: Record<string, number>;
  srm_p_value: number;
  primary: ExperimentMetricAnalysis;
  guardrails: ExperimentMetricAnalysis[];
  assumptions: string[];
}

export async function listExperiments(
  key: string,
  fetcher: typeof fetch = recordingFetch
): Promise<Experiment[]> {
  const res = await fetcher('/v1/experiments', { headers: authHeaders(key) });
  if (!res.ok) return errorOrStatus(res, 'GET /v1/experiments');
  return ((await res.json()) as { experiments?: Experiment[] }).experiments ?? [];
}

export async function getExperiment(
  key: string,
  experimentId: string,
  fetcher: typeof fetch = recordingFetch
): Promise<Experiment> {
  const res = await fetcher(`/v1/experiments/${encodeURIComponent(experimentId)}`, {
    headers: authHeaders(key)
  });
  if (!res.ok) return errorOrStatus(res, `GET /v1/experiments/${experimentId}`);
  return (await res.json()) as Experiment;
}

export async function createExperiment(
  key: string,
  spec: ExperimentSpec,
  fetcher: typeof fetch = recordingFetch
): Promise<{ experiment_id: string; seq: number }> {
  const res = await fetcher('/v1/experiments', {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify(spec)
  });
  if (!res.ok) return errorOrStatus(res, 'POST /v1/experiments');
  return (await res.json()) as { experiment_id: string; seq: number };
}

export async function updateExperiment(
  key: string,
  experimentId: string,
  spec: ExperimentSpec,
  fetcher: typeof fetch = recordingFetch
): Promise<EventAck> {
  const res = await fetcher(`/v1/experiments/${encodeURIComponent(experimentId)}`, {
    method: 'PUT',
    headers: jsonHeaders(key),
    body: JSON.stringify(spec)
  });
  if (!res.ok) return errorOrStatus(res, `PUT /v1/experiments/${experimentId}`);
  return (await res.json()) as EventAck;
}

export async function transitionExperiment(
  key: string,
  experimentId: string,
  action: 'start' | 'pause' | 'resume' | 'complete' | 'cancel',
  reason = '',
  fetcher: typeof fetch = recordingFetch
): Promise<EventAck & { status?: ExperimentState; request_id?: string }> {
  const res = await fetcher(`/v1/experiments/${encodeURIComponent(experimentId)}/${action}`, {
    method: 'POST',
    headers: jsonHeaders(key),
    body: action === 'start' ? undefined : JSON.stringify({ reason })
  });
  if (!res.ok) return errorOrStatus(res, `POST experiment ${action}`);
  return (await res.json()) as EventAck & { status?: ExperimentState; request_id?: string };
}

export async function decideExperimentLaunch(
  key: string,
  experimentId: string,
  requestId: string,
  decision: 'approve' | 'reject',
  reason: string,
  fetcher: typeof fetch = recordingFetch
): Promise<EventAck> {
  const res = await fetcher(
    `/v1/experiments/${encodeURIComponent(experimentId)}/launch-requests/${encodeURIComponent(requestId)}/${decision}`,
    { method: 'POST', headers: jsonHeaders(key), body: JSON.stringify({ reason }) }
  );
  if (!res.ok) return errorOrStatus(res, `POST experiment launch ${decision}`);
  return (await res.json()) as EventAck;
}

export async function getExperimentAnalysis(
  key: string,
  experimentId: string,
  fetcher: typeof fetch = recordingFetch
): Promise<ExperimentAnalysis> {
  const res = await fetcher(`/v1/experiments/${encodeURIComponent(experimentId)}/analysis`, {
    headers: authHeaders(key)
  });
  if (!res.ok) return errorOrStatus(res, `GET experiment analysis`);
  return (await res.json()) as ExperimentAnalysis;
}

export async function listExperimentExposures(
  key: string,
  experimentId: string,
  fetcher: typeof fetch = recordingFetch
): Promise<ExperimentExposure[]> {
  const res = await fetcher(`/v1/experiments/${encodeURIComponent(experimentId)}/exposures`, {
    headers: authHeaders(key)
  });
  if (!res.ok) return errorOrStatus(res, `GET experiment exposures`);
  return ((await res.json()) as { exposures?: ExperimentExposure[] }).exposures ?? [];
}

export async function promoteExperimentWinner(
  key: string,
  experimentId: string,
  fetcher: typeof fetch = recordingFetch
): Promise<{ request_id: string; winner_arm_key: string; version: number; seq: number }> {
  const res = await fetcher(`/v1/experiments/${encodeURIComponent(experimentId)}/promote`, {
    method: 'POST',
    headers: jsonHeaders(key)
  });
  if (!res.ok) return errorOrStatus(res, `POST experiment promote`);
  return (await res.json()) as {
    request_id: string;
    winner_arm_key: string;
    version: number;
    seq: number;
  };
}

export interface OutcomeSource {
  system: string;
  record_id: string;
  lineage?: string;
}

export interface OutcomeRevision {
  revision: number;
  value: number;
  event_time: string;
  observation_window_days?: number;
  source: OutcomeSource;
  label_version: string;
  reason?: string;
  recorded_by: string;
  recorded_at: string;
}

export interface BusinessOutcome {
  outcome_id: string;
  decision_id: string;
  key: string;
  kind: OutcomeKind;
  flow_id: string;
  flow_version: number;
  environment: Environment;
  treatment?: {
    experiment_id: string;
    cohort: number;
    arm_key: string;
    arm_name: string;
    arm_kind: ExperimentArmKind;
    version: number;
  };
  predictions?: Array<{
    model: string;
    model_version: number;
    node_id: string;
    probability: number;
  }>;
  current: OutcomeRevision;
  history: OutcomeRevision[];
}

export interface RecordOutcomeRequest {
  decision_id: string;
  key: string;
  kind: OutcomeKind;
  value: number;
  event_time: string;
  observation_window_days?: number;
  source: OutcomeSource;
  label_version: string;
}

export async function listOutcomes(
  key: string,
  filter: { decision_id?: string; key?: string } = {},
  fetcher: typeof fetch = recordingFetch
): Promise<BusinessOutcome[]> {
  const query = new URLSearchParams();
  if (filter.decision_id) query.set('decision_id', filter.decision_id);
  if (filter.key) query.set('key', filter.key);
  const res = await fetcher(`/v1/outcomes${query.size ? `?${query}` : ''}`, {
    headers: authHeaders(key)
  });
  if (!res.ok) return errorOrStatus(res, 'GET /v1/outcomes');
  return ((await res.json()) as { outcomes?: BusinessOutcome[] }).outcomes ?? [];
}

export async function recordOutcome(
  key: string,
  request: RecordOutcomeRequest,
  idempotencyKey: string,
  fetcher: typeof fetch = recordingFetch
): Promise<{ outcome_id: string; revision: number; seq: number }> {
  const res = await fetcher('/v1/outcomes', {
    method: 'POST',
    headers: { ...jsonHeaders(key), 'Idempotency-Key': idempotencyKey },
    body: JSON.stringify(request)
  });
  if (!res.ok) return errorOrStatus(res, 'POST /v1/outcomes');
  return (await res.json()) as { outcome_id: string; revision: number; seq: number };
}

export async function correctOutcome(
  key: string,
  outcomeId: string,
  request: Omit<RecordOutcomeRequest, 'decision_id' | 'key' | 'kind'> & { reason: string },
  idempotencyKey: string,
  fetcher: typeof fetch = recordingFetch
): Promise<{ outcome_id: string; revision: number; seq: number }> {
  const res = await fetcher(`/v1/outcomes/${encodeURIComponent(outcomeId)}/corrections`, {
    method: 'POST',
    headers: { ...jsonHeaders(key), 'Idempotency-Key': idempotencyKey },
    body: JSON.stringify(request)
  });
  if (!res.ok) return errorOrStatus(res, `POST outcome correction`);
  return (await res.json()) as { outcome_id: string; revision: number; seq: number };
}

export interface PopulationItemInput {
  data: Record<string, unknown>;
  entity_type?: string;
  entity_id?: string;
  business_reference?: string;
  correlation_id?: string;
  metadata?: Record<string, unknown>;
}

export interface PopulationJobSummary {
  job_id: string;
  kind: PopulationJobKind;
  slug: string;
  environment: Environment;
  state: PopulationJobState;
  total: number;
  pending: number;
  running: number;
  succeeded: number;
  failed: number;
  manifest_hash: string;
  created_at: string;
  updated_at: string;
  expires_at: string;
}

export interface PopulationJob extends PopulationJobSummary {
  manifest: {
    kind: PopulationJobKind;
    flow_id: string;
    slug: string;
    environment: Environment;
    items: Array<{
      index: number;
      input: PopulationItemInput;
      version: number;
      assignment?: {
        experiment_id: string;
        cohort: number;
        arm_key: string;
        arm_name: string;
        arm_kind: ExperimentArmKind;
        version: number;
        subject_hash: string;
      };
    }>;
  };
  max_attempts: number;
  concurrency: number;
  items: Array<{
    index: number;
    state: 'pending' | 'claimed' | 'succeeded' | 'failed';
    attempt: number;
    worker?: string;
    lease_until?: string;
    decision_id?: string;
    status?: string;
    output?: Record<string, unknown>;
    disposition?: string;
    error?: string;
    retryable?: boolean;
  }>;
  created_by: string;
}

export async function listPopulationJobs(
  key: string,
  fetcher: typeof fetch = recordingFetch
): Promise<PopulationJobSummary[]> {
  const res = await fetcher('/v1/population-jobs', { headers: authHeaders(key) });
  if (!res.ok) return errorOrStatus(res, 'GET /v1/population-jobs');
  return ((await res.json()) as { jobs?: PopulationJobSummary[] }).jobs ?? [];
}

export async function getPopulationJob(
  key: string,
  jobId: string,
  fetcher: typeof fetch = recordingFetch
): Promise<PopulationJob> {
  const res = await fetcher(`/v1/population-jobs/${encodeURIComponent(jobId)}`, {
    headers: authHeaders(key)
  });
  if (!res.ok) return errorOrStatus(res, `GET /v1/population-jobs/${jobId}`);
  const view = (await res.json()) as PopulationJob;
  return {
    ...view,
    kind: view.manifest.kind,
    slug: view.manifest.slug,
    environment: view.manifest.environment
  };
}

export async function createPopulationJob(
  key: string,
  request: {
    kind: PopulationJobKind;
    slug: string;
    environment: Environment;
    items: PopulationItemInput[];
    max_attempts?: number;
    concurrency?: number;
    retention_days?: number;
  },
  idempotencyKey: string,
  fetcher: typeof fetch = recordingFetch
): Promise<{
  job_id: string;
  state: PopulationJobState;
  manifest_hash: string;
  total: number;
  seq: number;
}> {
  const res = await fetcher('/v1/population-jobs', {
    method: 'POST',
    headers: { ...jsonHeaders(key), 'Idempotency-Key': idempotencyKey },
    body: JSON.stringify(request)
  });
  if (!res.ok) return errorOrStatus(res, 'POST /v1/population-jobs');
  return (await res.json()) as {
    job_id: string;
    state: PopulationJobState;
    manifest_hash: string;
    total: number;
    seq: number;
  };
}

export async function transitionPopulationJob(
  key: string,
  jobId: string,
  action: 'pause' | 'resume' | 'cancel',
  reason = '',
  fetcher: typeof fetch = recordingFetch
): Promise<EventAck> {
  const res = await fetcher(`/v1/population-jobs/${encodeURIComponent(jobId)}/${action}`, {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify({ reason })
  });
  if (!res.ok) return errorOrStatus(res, `POST population job ${action}`);
  return (await res.json()) as EventAck;
}

export async function retryPopulationJob(
  key: string,
  jobId: string,
  indices: number[] = [],
  fetcher: typeof fetch = recordingFetch
): Promise<EventAck> {
  const res = await fetcher(`/v1/population-jobs/${encodeURIComponent(jobId)}/retry`, {
    method: 'POST',
    headers: jsonHeaders(key),
    body: JSON.stringify({ indices })
  });
  if (!res.ok) return errorOrStatus(res, 'POST population job retry');
  return (await res.json()) as EventAck;
}

export async function downloadPopulationResults(
  key: string,
  jobId: string,
  fetcher: typeof fetch = recordingFetch
): Promise<Blob> {
  const res = await fetcher(`/v1/population-jobs/${encodeURIComponent(jobId)}/results`, {
    headers: authHeaders(key)
  });
  if (!res.ok) return errorOrStatus(res, 'GET population job results');
  return res.blob();
}
