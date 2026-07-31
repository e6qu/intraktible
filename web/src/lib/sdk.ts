// SPDX-License-Identifier: AGPL-3.0-or-later

// A typed, framework-agnostic TypeScript client for the intraktible public
// data-plane API (the contract published at /openapi.json). It depends only on
// fetch, so it works in the browser, Node 18+, Deno, and edge runtimes — and is
// independent of this app's SvelteKit-specific api.ts. A Go counterpart lives in
// the repo's `client` package.

export interface ClientOptions {
  // apiKey is sent as the X-Api-Key header on every request.
  apiKey: string;
  // baseUrl defaults to '' (same-origin); set it to call a remote instance.
  baseUrl?: string;
  // fetch lets callers inject a fetch implementation (tests, SSR, custom agents).
  fetch?: typeof fetch;
}

// ApiError is thrown for any non-2xx response, carrying the server's status and
// its {error} message when present.
export class ApiError extends Error {
  readonly status: number;
  readonly body?: unknown;
  constructor(status: number, message: string, body?: unknown) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.body = body;
  }
}

export interface DecideRequest {
  data: Record<string, unknown>;
  entity_type?: string;
  entity_id?: string;
  business_reference?: string;
  correlation_id?: string;
  metadata?: Record<string, unknown>;
  control?: { timeout_ms?: number };
  // Sent as the Idempotency-Key header, not in the JSON request.
  idempotencyKey?: string;
}

export interface DecideResult {
  decision_id: string;
  status: 'completed' | 'failed' | 'suspended';
  data?: Record<string, unknown>;
  disposition?: string;
  error?: string;
}

export interface BatchResult {
  summary: Record<string, unknown>;
  results: Record<string, unknown>[];
}

export interface Decision {
  decision_id: string;
  slug: string;
  version: number;
  environment: string;
  status: string;
  disposition?: string;
  entity_type?: string;
  entity_id?: string;
  business_reference?: string;
  correlation_id?: string;
  metadata?: Record<string, unknown>;
}

export interface SdkFlow {
  flow_id: string;
  slug: string;
  name: string;
  latest: number;
}

export interface AuthoringDraft {
  draft_id: string;
  flow_id: string;
  base_version: number;
  revision: number;
  state: 'active' | 'archived';
  title: string;
  graph: unknown;
  input_schema?: unknown;
  created_by: string;
  created_at: string;
  updated_by: string;
  updated_at: string;
}

export interface AuthoringDraftRevision {
  draft_id: string;
  flow_id: string;
  base_version: number;
  revision: number;
  title: string;
  graph: unknown;
  input_schema?: unknown;
  actor: string;
  at: string;
  rebased?: boolean;
}

export interface AuthoringPresence {
  draft_id: string;
  actor: string;
  display_name?: string;
  revision: number;
  selected_id?: string;
  renewed_at: string;
  expires_at: string;
}

export interface AuthoringChangeSet {
  changeset_id: string;
  flow_id: string;
  base_version: number;
  draft_id: string;
  draft_revision: number;
  title: string;
  rationale?: string;
  state: 'draft' | 'in_review' | 'changes_requested' | 'approved' | 'publishing' | 'published';
  source_graph: unknown;
  graph: unknown;
  proposed_etag: string;
  dependencies?: Array<{ component_id: string; version: number; etag: string }>;
  required_checks?: string[];
  reviewers?: string[];
  checks?: Record<string, unknown>;
  created_by: string;
  submitted_by?: string;
  published_version?: number;
}

export interface ReusableComponent {
  component_id: string;
  slug: string;
  name: string;
  description?: string;
  latest: number;
  versions?: Array<Record<string, unknown>>;
  retired: boolean;
}

export interface FlowDoc {
  slug: string;
  name?: string;
  graph: unknown;
  input_schema?: unknown;
}

export interface ImportResult {
  flow_id: string;
  draft_id: string;
  revision: number;
  created: boolean;
  migration_report: {
    rewrites: Array<{
      path: string;
      from: string;
      to: string;
      reason: string;
    }>;
  };
}

export interface Identity {
  org: string;
  workspace: string;
  actor: string;
  scope: string;
  role: string;
}

export interface PromoteResult {
  promoted: boolean;
  pending?: boolean;
  request_id?: string;
  version: number;
}

export interface BundleFlowResult {
  flow_id: string;
  draft_id: string;
  revision: number;
  created: boolean;
  migration_report: ImportResult['migration_report'];
}

export interface BundleResult {
  imports: BundleFlowResult[];
  seq: number;
}

export interface ExperimentSpec {
  name: string;
  hypothesis: string;
  owner: string;
  flow_id: string;
  environment: string;
  subject_key_expression: string;
  eligibility_expression?: string;
  salt: string;
  arms: Array<{
    key: string;
    name: string;
    kind: 'champion' | 'challenger';
    version: number;
    allocation_bps: number;
  }>;
  primary_metric: {
    key: string;
    name: string;
    kind: 'binary' | 'continuous';
    direction: 'increase' | 'decrease';
  };
  guardrails?: Array<Record<string, unknown>>;
  minimum_sample_per_arm: number;
  minimum_effect: number;
  confidence: number;
  observation_window_days: number;
  start_at?: string;
  stop_at?: string;
}

export interface Experiment {
  experiment_id: string;
  cohort: number;
  state: 'draft' | 'pending_launch' | 'running' | 'paused' | 'completed' | 'cancelled';
  spec: ExperimentSpec;
  launch?: { request_id: string; status: string; requested_by: string };
}

export interface OutcomeRecord {
  decision_id: string;
  key: string;
  kind: 'binary' | 'continuous';
  value: number;
  event_time: string;
  observation_window_days?: number;
  source: { system: string; record_id: string; lineage?: string };
  label_version: string;
}

export interface PopulationJobCreate {
  kind: 'decision' | 'backtest';
  slug: string;
  environment: string;
  items: Array<{
    data: Record<string, unknown>;
    entity_type?: string;
    entity_id?: string;
    business_reference?: string;
    correlation_id?: string;
    metadata?: Record<string, unknown>;
  }>;
  max_attempts?: number;
  concurrency?: number;
  retention_days?: number;
}

export interface PopulationJob {
  job_id: string;
  state:
    | 'queued'
    | 'running'
    | 'paused'
    | 'cancelling'
    | 'cancelled'
    | 'completed'
    | 'completed_with_errors'
    | 'expired';
  manifest: Record<string, unknown>;
  manifest_hash: string;
  total: number;
  pending: number;
  running: number;
  succeeded: number;
  failed: number;
  items: Array<Record<string, unknown>>;
}

export interface AgentTemplate {
  template_id: string;
  slug: string;
  name: string;
  task: string;
  description?: string;
  high_impact: boolean;
  tags?: string[];
  latest_release?: number;
}

export interface AgentReleaseSpec {
  instructions: string;
  provider: string;
  model: string;
  input_schema: Record<string, unknown>;
  output_schema: Record<string, unknown>;
  tools: Array<{
    name: string;
    mode: 'automatic' | 'human_before_call' | 'forbidden';
    purpose: string;
    parameter_schema: Record<string, unknown>;
  }>;
  data_purposes: string[];
  dependencies: Array<{ kind: string; name: string; version: string; hash: string }>;
  budget: {
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
  };
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

export interface AgentRelease {
  template_id: string;
  release: number;
  status: 'draft' | 'evaluated' | 'review_requested' | 'approved' | 'rejected' | 'retired';
  spec: AgentReleaseSpec;
  spec_hash: string;
  campaign_ids: string[];
  review?: Record<string, unknown>;
  created_by: string;
  created_at: string;
}

export interface AgentDeployment {
  deployment_id: string;
  template_id: string;
  release: number;
  environment: 'sandbox' | 'staging' | 'production';
  status: 'scheduled' | 'active' | 'paused' | 'retired';
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

export interface AgentAssist {
  assist_id: string;
  case_id: string;
  template_id: string;
  release: number;
  environment: 'sandbox' | 'staging' | 'production';
  evidence_ids: string[];
  evidence_seq: number;
  current_evidence_seq: number;
  evidence_stale?: boolean;
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
  result?: Record<string, unknown>;
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
  failure?: string;
}

export interface AgentToolApproval {
  approval_id: string;
  assist_id: string;
  invocation_id: string;
  call_id: string;
  name: string;
  purpose: string;
  arguments_hash: string;
  status: 'pending' | 'approved' | 'rejected' | 'expired';
  requested_by: string;
  requested_at: string;
  expires_at: string;
  decided_by?: string;
  decided_at?: string;
  reason?: string;
}

export interface AgentSafetyIncident {
  incident_id: string;
  template_id: string;
  release: number;
  deployment_id?: string;
  kind: string;
  severity: 'info' | 'warning' | 'required' | 'critical';
  summary: string;
  status: 'open' | 'resolved';
  resolution?: string;
}

// Client calls an intraktible instance.
export class Client {
  private readonly apiKey: string;
  private readonly baseUrl: string;
  private readonly fetchImpl: typeof fetch;

  constructor(opts: ClientOptions) {
    this.apiKey = opts.apiKey;
    this.baseUrl = (opts.baseUrl ?? '').replace(/\/$/, '');
    this.fetchImpl = opts.fetch ?? fetch;
  }

  // decide runs the live version of a flow in an environment against the input.
  decide(slug: string, env: string, req: DecideRequest): Promise<DecideResult> {
    const { idempotencyKey, ...body } = req;
    return this.request<DecideResult>(
      'POST',
      `${this.flowPath(slug, env)}/decide`,
      body,
      idempotencyKey ? { 'Idempotency-Key': idempotencyKey } : undefined
    );
  }

  // decideBatch runs each row of a dataset through the recorded decide path.
  decideBatch(slug: string, env: string, dataset: Record<string, unknown>[]): Promise<BatchResult> {
    return this.request<BatchResult>('POST', `${this.flowPath(slug, env)}/decide/batch`, {
      dataset
    });
  }

  async listDecisions(): Promise<Decision[]> {
    const out = await this.request<{ decisions?: Decision[] }>('GET', '/v1/decisions');
    return out.decisions ?? [];
  }

  getDecision(decisionId: string): Promise<Decision> {
    return this.request<Decision>('GET', `/v1/decisions/${encodeURIComponent(decisionId)}`);
  }

  async listFlows(): Promise<SdkFlow[]> {
    const out = await this.request<{ flows?: SdkFlow[] }>('GET', '/v1/flows');
    return out.flows ?? [];
  }

  // createFlow creates an empty flow and returns its id.
  async createFlow(slug: string, name: string): Promise<string> {
    const out = await this.request<{ flow_id: string }>('POST', '/v1/flows', { slug, name });
    return out.flow_id;
  }

  getFlow(flowId: string): Promise<SdkFlow> {
    return this.request<SdkFlow>('GET', `/v1/flows/${encodeURIComponent(flowId)}`);
  }

  async createDraft(
    input: {
      flow_id: string;
      base_version: number;
      title: string;
      graph: unknown;
      input_schema?: unknown;
    },
    idempotencyKey = crypto.randomUUID()
  ): Promise<string> {
    const out = await this.request<{ draft_id: string }>('POST', '/v1/authoring/drafts', input, {
      'Idempotency-Key': idempotencyKey
    });
    return out.draft_id;
  }

  async listDrafts(flowId = ''): Promise<AuthoringDraft[]> {
    const query = flowId ? `?flow_id=${encodeURIComponent(flowId)}` : '';
    const out = await this.request<{ drafts?: AuthoringDraft[] }>(
      'GET',
      `/v1/authoring/drafts${query}`
    );
    return out.drafts ?? [];
  }

  saveDraft(
    draftId: string,
    input: { expected_revision: number; title: string; graph: unknown; input_schema?: unknown }
  ): Promise<{ revision: number }> {
    return this.request('PUT', `/v1/authoring/drafts/${encodeURIComponent(draftId)}`, input);
  }

  getDraft(draftId: string): Promise<AuthoringDraft> {
    return this.request('GET', `/v1/authoring/drafts/${encodeURIComponent(draftId)}`);
  }

  rebaseDraft(
    draftId: string,
    input: {
      expected_revision: number;
      base_version: number;
      title: string;
      graph: unknown;
      input_schema?: unknown;
    }
  ): Promise<{ revision: number }> {
    return this.request(
      'POST',
      `/v1/authoring/drafts/${encodeURIComponent(draftId)}/rebase`,
      input
    );
  }

  async listDraftRevisions(draftId: string): Promise<AuthoringDraftRevision[]> {
    const out = await this.request<{ revisions?: AuthoringDraftRevision[] }>(
      'GET',
      `/v1/authoring/drafts/${encodeURIComponent(draftId)}/revisions`
    );
    return out.revisions ?? [];
  }

  async archiveDraft(draftId: string): Promise<void> {
    await this.request('DELETE', `/v1/authoring/drafts/${encodeURIComponent(draftId)}`);
  }

  renewDraftPresence(
    draftId: string,
    input: {
      display_name?: string;
      revision: number;
      selected_id?: string;
      ttl_seconds?: number;
    }
  ): Promise<AuthoringPresence> {
    return this.request(
      'PUT',
      `/v1/authoring/drafts/${encodeURIComponent(draftId)}/presence`,
      input
    );
  }

  async listDraftPresence(draftId: string): Promise<AuthoringPresence[]> {
    const out = await this.request<{ presence?: AuthoringPresence[] }>(
      'GET',
      `/v1/authoring/drafts/${encodeURIComponent(draftId)}/presence`
    );
    return out.presence ?? [];
  }

  async leaveDraftPresence(draftId: string): Promise<void> {
    await this.request('DELETE', `/v1/authoring/drafts/${encodeURIComponent(draftId)}/presence`);
  }

  async createChangeSet(
    input: {
      draft_id: string;
      draft_revision: number;
      title: string;
      rationale?: string;
      required_checks?: string[];
      reviewers?: string[];
    },
    idempotencyKey = crypto.randomUUID()
  ): Promise<string> {
    const out = await this.request<{ changeset_id: string }>(
      'POST',
      '/v1/authoring/changesets',
      input,
      { 'Idempotency-Key': idempotencyKey }
    );
    return out.changeset_id;
  }

  async listChangeSets(flowId = ''): Promise<AuthoringChangeSet[]> {
    const query = flowId ? `?flow_id=${encodeURIComponent(flowId)}` : '';
    const out = await this.request<{ changesets?: AuthoringChangeSet[] }>(
      'GET',
      `/v1/authoring/changesets${query}`
    );
    return out.changesets ?? [];
  }

  checkChangeSet(
    changeSetId: string,
    name = 'flow-validation',
    status: 'passed' | 'failed' = 'passed',
    evidence = ''
  ): Promise<Record<string, unknown>> {
    return this.request(
      'POST',
      `/v1/authoring/changesets/${encodeURIComponent(changeSetId)}/checks`,
      { name, status, evidence: evidence || undefined }
    );
  }

  submitChangeSet(changeSetId: string): Promise<Record<string, unknown>> {
    return this.request(
      'POST',
      `/v1/authoring/changesets/${encodeURIComponent(changeSetId)}/submit`,
      {}
    );
  }

  reviewChangeSet(
    changeSetId: string,
    decision: 'approve' | 'request_changes',
    reason = ''
  ): Promise<Record<string, unknown>> {
    return this.request(
      'POST',
      `/v1/authoring/changesets/${encodeURIComponent(changeSetId)}/review`,
      { decision, reason }
    );
  }

  publishChangeSet(changeSetId: string): Promise<{ version: number; etag: string }> {
    return this.request(
      'POST',
      `/v1/authoring/changesets/${encodeURIComponent(changeSetId)}/publish`,
      {}
    );
  }

  async listReusableComponents(): Promise<ReusableComponent[]> {
    const out = await this.request<{ components?: ReusableComponent[] }>(
      'GET',
      '/v1/authoring/components'
    );
    return out.components ?? [];
  }

  async createReusableComponent(
    input: { slug: string; name: string; description?: string },
    idempotencyKey = crypto.randomUUID()
  ): Promise<string> {
    const out = await this.request<{ component_id: string }>(
      'POST',
      '/v1/authoring/components',
      input,
      { 'Idempotency-Key': idempotencyKey }
    );
    return out.component_id;
  }

  publishReusableComponent(
    componentId: string,
    input: {
      graph: unknown;
      input_schema?: unknown;
      output_schema?: unknown;
      allow_breaking?: boolean;
      breaking_change_reason?: string;
    },
    idempotencyKey = crypto.randomUUID()
  ): Promise<{ version: number; etag: string }> {
    return this.request(
      'POST',
      `/v1/authoring/components/${encodeURIComponent(componentId)}/versions`,
      input,
      { 'Idempotency-Key': idempotencyKey }
    );
  }

  assessComponentCompatibility(
    componentId: string,
    fromVersion: number,
    toVersion: number
  ): Promise<{
    report: {
      from_version: number;
      to_version: number;
      status: 'compatible' | 'incompatible';
      issues?: Array<{ path: string; code: string; message: string }>;
    };
    consumers: Array<Record<string, unknown>>;
    upgradeable: boolean;
  }> {
    const query = new URLSearchParams({
      from_version: String(fromVersion),
      to_version: String(toVersion)
    });
    return this.request(
      'GET',
      `/v1/authoring/components/${encodeURIComponent(componentId)}/compatibility?${query}`
    );
  }

  createComponentUpgradeDrafts(
    componentId: string,
    input: { from_version: number; to_version: number; flow_ids: string[]; title?: string },
    idempotencyKey: string = crypto.randomUUID()
  ): Promise<{
    drafts: Array<{ flow_id: string; draft_id: string; base_version: number; revision: number }>;
    seq: number;
  }> {
    return this.request(
      'POST',
      `/v1/authoring/components/${encodeURIComponent(componentId)}/upgrade-drafts`,
      input,
      { 'Idempotency-Key': idempotencyKey }
    );
  }

  // importFlow creates a durable draft; it never bypasses changeset review.
  importFlow(doc: FlowDoc, idempotencyKey: string): Promise<ImportResult> {
    return this.request<ImportResult>(
      'POST',
      '/v1/authoring/import',
      { ...doc, format_version: 'intraktible.authoring/v1', kind: 'flow' },
      { 'Idempotency-Key': idempotencyKey }
    );
  }

  // importBundle validates the full bundle before creating governed drafts.
  importBundle(docs: FlowDoc[], idempotencyKey: string): Promise<BundleResult> {
    return this.request<BundleResult>(
      'POST',
      '/v1/authoring/import-bundle',
      {
        format_version: 'intraktible.authoring/v1',
        kind: 'bundle',
        flows: docs.map((doc) => ({
          ...doc,
          format_version: 'intraktible.authoring/v1',
          kind: 'flow'
        }))
      },
      { 'Idempotency-Key': idempotencyKey }
    );
  }

  // deploy makes a version live in an environment (a direct deploy).
  async deploy(flowId: string, environment: string, version: number): Promise<void> {
    await this.request<unknown>('POST', `/v1/flows/${encodeURIComponent(flowId)}/deployments`, {
      environment,
      version
    });
  }

  // promote ships the live version of `from` up to `to`. A non-production target
  // deploys directly; production opens a maker-checker request (pending).
  promote(flowId: string, from: string, to: string, force = false): Promise<PromoteResult> {
    return this.request<PromoteResult>('POST', `/v1/flows/${encodeURIComponent(flowId)}/promote`, {
      from,
      to,
      force
    });
  }

  async listExperiments(): Promise<Experiment[]> {
    const out = await this.request<{ experiments?: Experiment[] }>('GET', '/v1/experiments');
    return out.experiments ?? [];
  }

  getExperiment(experimentId: string): Promise<Experiment> {
    return this.request<Experiment>('GET', `/v1/experiments/${encodeURIComponent(experimentId)}`);
  }

  async createExperiment(spec: ExperimentSpec): Promise<string> {
    const out = await this.request<{ experiment_id: string }>('POST', '/v1/experiments', spec);
    return out.experiment_id;
  }

  updateExperiment(experimentId: string, spec: ExperimentSpec): Promise<Record<string, unknown>> {
    return this.request('PUT', `/v1/experiments/${encodeURIComponent(experimentId)}`, spec);
  }

  transitionExperiment(
    experimentId: string,
    action: 'start' | 'pause' | 'resume' | 'complete' | 'cancel',
    reason = ''
  ): Promise<Record<string, unknown>> {
    return this.request(
      'POST',
      `/v1/experiments/${encodeURIComponent(experimentId)}/${action}`,
      action === 'start' ? undefined : { reason }
    );
  }

  decideExperimentLaunch(
    experimentId: string,
    requestId: string,
    decision: 'approve' | 'reject',
    reason: string
  ): Promise<Record<string, unknown>> {
    return this.request(
      'POST',
      `/v1/experiments/${encodeURIComponent(experimentId)}/launch-requests/${encodeURIComponent(requestId)}/${decision}`,
      { reason }
    );
  }

  promoteExperimentWinner(experimentId: string): Promise<Record<string, unknown>> {
    return this.request('POST', `/v1/experiments/${encodeURIComponent(experimentId)}/promote`);
  }

  experimentAnalysis(experimentId: string): Promise<Record<string, unknown>> {
    return this.request('GET', `/v1/experiments/${encodeURIComponent(experimentId)}/analysis`);
  }

  async listOutcomes(decisionId = '', key = ''): Promise<Array<Record<string, unknown>>> {
    const query = new URLSearchParams();
    if (decisionId) query.set('decision_id', decisionId);
    if (key) query.set('key', key);
    const out = await this.request<{ outcomes?: Array<Record<string, unknown>> }>(
      'GET',
      `/v1/outcomes${query.size ? `?${query}` : ''}`
    );
    return out.outcomes ?? [];
  }

  recordOutcome(
    outcome: OutcomeRecord,
    idempotencyKey: string
  ): Promise<{ outcome_id: string; revision: number }> {
    return this.request('POST', '/v1/outcomes', outcome, {
      'Idempotency-Key': idempotencyKey
    });
  }

  correctOutcome(
    outcomeId: string,
    correction: Omit<OutcomeRecord, 'decision_id' | 'key' | 'kind'> & { reason: string },
    idempotencyKey: string
  ): Promise<{ outcome_id: string; revision: number }> {
    return this.request(
      'POST',
      `/v1/outcomes/${encodeURIComponent(outcomeId)}/corrections`,
      correction,
      { 'Idempotency-Key': idempotencyKey }
    );
  }

  async listPopulationJobs(): Promise<PopulationJob[]> {
    const out = await this.request<{ jobs?: PopulationJob[] }>('GET', '/v1/population-jobs');
    return out.jobs ?? [];
  }

  getPopulationJob(jobId: string): Promise<PopulationJob> {
    return this.request('GET', `/v1/population-jobs/${encodeURIComponent(jobId)}`);
  }

  async createPopulationJob(job: PopulationJobCreate, idempotencyKey: string): Promise<string> {
    const out = await this.request<{ job_id: string }>('POST', '/v1/population-jobs', job, {
      'Idempotency-Key': idempotencyKey
    });
    return out.job_id;
  }

  transitionPopulationJob(
    jobId: string,
    action: 'pause' | 'resume' | 'cancel',
    reason = ''
  ): Promise<Record<string, unknown>> {
    return this.request('POST', `/v1/population-jobs/${encodeURIComponent(jobId)}/${action}`, {
      reason
    });
  }

  retryPopulationJob(jobId: string, indices: number[] = []): Promise<Record<string, unknown>> {
    return this.request('POST', `/v1/population-jobs/${encodeURIComponent(jobId)}/retry`, {
      indices
    });
  }

  populationResults(jobId: string): Promise<string> {
    return this.requestText(`/v1/population-jobs/${encodeURIComponent(jobId)}/results`);
  }

  async listAgentTemplates(): Promise<AgentTemplate[]> {
    const out = await this.request<{ templates?: AgentTemplate[] }>('GET', '/v1/agent-templates');
    return out.templates ?? [];
  }

  async createAgentTemplate(template: AgentTemplate): Promise<AgentTemplate> {
    const out = await this.request<{ template: AgentTemplate }>(
      'POST',
      '/v1/agent-templates',
      template
    );
    return out.template;
  }

  getAgentTemplate(templateId: string): Promise<AgentTemplate> {
    return this.request('GET', `/v1/agent-templates/${encodeURIComponent(templateId)}`);
  }

  async listAgentReleases(templateId: string): Promise<AgentRelease[]> {
    const out = await this.request<{ releases?: AgentRelease[] }>(
      'GET',
      `${this.agentTemplatePath(templateId)}/releases`
    );
    return out.releases ?? [];
  }

  createAgentRelease(
    templateId: string,
    spec: AgentReleaseSpec
  ): Promise<{ release: number; spec_hash: string; seq: number }> {
    return this.request('POST', `${this.agentTemplatePath(templateId)}/releases`, { spec });
  }

  getAgentRelease(templateId: string, release: number): Promise<AgentRelease> {
    return this.request('GET', this.agentReleasePath(templateId, release));
  }

  retireAgentRelease(
    templateId: string,
    release: number,
    reason: string
  ): Promise<Record<string, unknown>> {
    return this.request('POST', `${this.agentReleasePath(templateId, release)}/retire`, { reason });
  }

  async listAgentEvalSuites(): Promise<Array<Record<string, unknown>>> {
    const out = await this.request<{ suites?: Array<Record<string, unknown>> }>(
      'GET',
      '/v1/agent-eval-suites'
    );
    return out.suites ?? [];
  }

  async publishAgentEvalSuite(suite: Record<string, unknown>): Promise<Record<string, unknown>> {
    const out = await this.request<{ suite: Record<string, unknown> }>(
      'POST',
      '/v1/agent-eval-suites',
      suite
    );
    return out.suite;
  }

  getAgentEvalSuite(suiteId: string, version: number): Promise<Record<string, unknown>> {
    return this.request(
      'GET',
      `/v1/agent-eval-suites/${encodeURIComponent(suiteId)}/versions/${version}`
    );
  }

  async runAgentCampaign(
    templateId: string,
    release: number,
    suiteId: string,
    suiteVersion: number
  ): Promise<Record<string, unknown>> {
    const out = await this.request<{ campaign: Record<string, unknown> }>(
      'POST',
      `${this.agentReleasePath(templateId, release)}/campaigns`,
      { suite_id: suiteId, suite_version: suiteVersion }
    );
    return out.campaign;
  }

  async listAgentCampaigns(
    templateId: string,
    release: number
  ): Promise<Array<Record<string, unknown>>> {
    const out = await this.request<{ campaigns?: Array<Record<string, unknown>> }>(
      'GET',
      `${this.agentReleasePath(templateId, release)}/campaigns`
    );
    return out.campaigns ?? [];
  }

  adjudicateAgentCampaignTrial(
    campaignId: string,
    caseId: string,
    trial: number,
    passed: boolean,
    reason: string
  ): Promise<Record<string, unknown>> {
    return this.request(
      'POST',
      `/v1/agent-eval-campaigns/${encodeURIComponent(campaignId)}/trials/${encodeURIComponent(caseId)}/${trial}/adjudication`,
      { passed, reason }
    );
  }

  compareAgentCampaigns(
    baselineCampaignId: string,
    challengerCampaignId: string
  ): Promise<Record<string, unknown>> {
    const query = new URLSearchParams({
      baseline_campaign_id: baselineCampaignId,
      challenger_campaign_id: challengerCampaignId
    });
    return this.request('GET', `/v1/agent-eval-comparisons?${query}`);
  }

  exportAgentCampaign(campaignId: string, format: 'json' | 'csv'): Promise<string> {
    return this.requestText(
      `/v1/agent-eval-campaigns/${encodeURIComponent(campaignId)}/export?format=${format}`
    );
  }

  async requestAgentReleaseReview(
    templateId: string,
    release: number,
    input: {
      campaign_ids: string[];
      evidence_ids: string[];
      reviewers: string[];
      expires_at: string;
    }
  ): Promise<string> {
    const out = await this.request<{ request_id: string }>(
      'POST',
      `${this.agentReleasePath(templateId, release)}/review-request`,
      input
    );
    return out.request_id;
  }

  reviewAgentRelease(
    templateId: string,
    release: number,
    requestId: string,
    decision: 'approve' | 'reject',
    reason: string
  ): Promise<Record<string, unknown>> {
    return this.request('POST', `${this.agentReleasePath(templateId, release)}/review`, {
      request_id: requestId,
      decision,
      reason
    });
  }

  async listAgentDeployments(): Promise<AgentDeployment[]> {
    const out = await this.request<{ deployments?: AgentDeployment[] }>(
      'GET',
      '/v1/agent-deployments'
    );
    return out.deployments ?? [];
  }

  async requestAgentDeployment(input: {
    template_id: string;
    release: number;
    environment: 'sandbox' | 'staging' | 'production';
    at?: string;
    expires_at?: string;
    reason: string;
  }): Promise<string> {
    const out = await this.request<{ deployment_id: string }>(
      'POST',
      '/v1/agent-deployments',
      input
    );
    return out.deployment_id;
  }

  getAgentDeployment(deploymentId: string): Promise<AgentDeployment> {
    return this.request('GET', `/v1/agent-deployments/${encodeURIComponent(deploymentId)}`);
  }

  activateAgentDeployment(deploymentId: string): Promise<Record<string, unknown>> {
    return this.request(
      'POST',
      `/v1/agent-deployments/${encodeURIComponent(deploymentId)}/activate`,
      {}
    );
  }

  pauseAgentDeployment(deploymentId: string, reason: string): Promise<Record<string, unknown>> {
    return this.request('POST', `/v1/agent-deployments/${encodeURIComponent(deploymentId)}/pause`, {
      reason
    });
  }

  resumeAgentDeployment(deploymentId: string, reason: string): Promise<Record<string, unknown>> {
    return this.request(
      'POST',
      `/v1/agent-deployments/${encodeURIComponent(deploymentId)}/resume`,
      {
        reason
      }
    );
  }

  rollbackAgentDeployment(
    deploymentId: string,
    toRelease: number,
    reason: string
  ): Promise<Record<string, unknown>> {
    return this.request(
      'POST',
      `/v1/agent-deployments/${encodeURIComponent(deploymentId)}/rollback`,
      { to_release: toRelease, reason }
    );
  }

  async requestCaseAgentAssist(
    caseId: string,
    input: {
      kind:
        | 'summary'
        | 'evidence_extraction'
        | 'prioritization'
        | 'next_best_action'
        | 'draft_disposition';
      template_id: string;
      release: number;
      environment: 'sandbox' | 'staging' | 'production';
      evidence_ids: string[];
    },
    idempotencyKey: string
  ): Promise<{ assist_id: string; status: AgentAssist['status']; approval_id?: string }> {
    return this.request('POST', `/v1/cases/${encodeURIComponent(caseId)}/agent-assists`, input, {
      'Idempotency-Key': idempotencyKey
    });
  }

  async listCaseAgentAssists(caseId: string): Promise<AgentAssist[]> {
    const out = await this.request<{ assists?: AgentAssist[] }>(
      'GET',
      `/v1/cases/${encodeURIComponent(caseId)}/agent-assists`
    );
    return out.assists ?? [];
  }

  getAgentAssist(assistId: string): Promise<AgentAssist> {
    return this.request('GET', `/v1/agent-assists/${encodeURIComponent(assistId)}`);
  }

  recordAgentAssistAction(
    assistId: string,
    input: {
      action: 'accepted' | 'edited' | 'rejected' | 'escalated';
      final?: Record<string, unknown>;
      reason?: string;
      time_saved_ms?: number;
    }
  ): Promise<Record<string, unknown>> {
    return this.request(
      'POST',
      `/v1/agent-assists/${encodeURIComponent(assistId)}/reviewer-action`,
      { assist_id: assistId, ...input }
    );
  }

  retryAgentAssist(
    assistId: string,
    reason: string,
    acknowledgeAtLeastOnce = false
  ): Promise<Record<string, unknown>> {
    return this.request('POST', `/v1/agent-assists/${encodeURIComponent(assistId)}/retry`, {
      reason,
      acknowledge_at_least_once: acknowledgeAtLeastOnce
    });
  }

  cancelAgentAssist(assistId: string, reason: string): Promise<Record<string, unknown>> {
    return this.request('POST', `/v1/agent-assists/${encodeURIComponent(assistId)}/cancel`, {
      reason
    });
  }

  async listAgentToolApprovals(): Promise<AgentToolApproval[]> {
    const out = await this.request<{ approvals?: AgentToolApproval[] }>(
      'GET',
      '/v1/agent-tool-approvals'
    );
    return out.approvals ?? [];
  }

  getAgentToolApproval(approvalId: string): Promise<AgentToolApproval> {
    return this.request('GET', `/v1/agent-tool-approvals/${encodeURIComponent(approvalId)}`);
  }

  decideAgentToolApproval(
    approvalId: string,
    decision: 'approved' | 'rejected',
    reason: string
  ): Promise<{ assist_id?: string; status: 'requested' | 'rejected' }> {
    return this.request(
      'POST',
      `/v1/agent-tool-approvals/${encodeURIComponent(approvalId)}/decision`,
      { decision, reason }
    );
  }

  async listAgentSafetyIncidents(): Promise<AgentSafetyIncident[]> {
    const out = await this.request<{ incidents?: AgentSafetyIncident[] }>(
      'GET',
      '/v1/agent-safety-incidents'
    );
    return out.incidents ?? [];
  }

  async openAgentSafetyIncident(
    incident: Omit<AgentSafetyIncident, 'incident_id' | 'status' | 'resolution'>
  ): Promise<string> {
    const out = await this.request<{ incident_id: string }>(
      'POST',
      '/v1/agent-safety-incidents',
      incident
    );
    return out.incident_id;
  }

  resolveAgentSafetyIncident(
    incidentId: string,
    resolution: string
  ): Promise<Record<string, unknown>> {
    return this.request(
      'POST',
      `/v1/agent-safety-incidents/${encodeURIComponent(incidentId)}/resolve`,
      { resolution }
    );
  }

  agentGovernanceAnalytics(): Promise<Record<string, unknown>> {
    return this.request('GET', '/v1/agent-governance/analytics');
  }

  me(): Promise<Identity> {
    return this.request<Identity>('GET', '/v1/me');
  }

  private flowPath(slug: string, env: string): string {
    return `/v1/flows/${encodeURIComponent(slug)}/${encodeURIComponent(env)}`;
  }

  private agentTemplatePath(templateId: string): string {
    return `/v1/agent-templates/${encodeURIComponent(templateId)}`;
  }

  private agentReleasePath(templateId: string, release: number): string {
    return `${this.agentTemplatePath(templateId)}/releases/${release}`;
  }

  private async request<T>(
    method: string,
    path: string,
    body?: unknown,
    extraHeaders: Record<string, string> = {}
  ): Promise<T> {
    const headers: Record<string, string> = {
      'X-Api-Key': this.apiKey,
      Accept: 'application/json',
      ...extraHeaders
    };
    if (body !== undefined) {
      headers['Content-Type'] = 'application/json';
    }
    const res = await this.fetchImpl(`${this.baseUrl}${path}`, {
      method,
      headers,
      body: body !== undefined ? JSON.stringify(body) : undefined
    });
    if (!res.ok) {
      let message = String(res.status);
      let body: unknown;
      try {
        body = await res.json();
        const e = body as { error?: string };
        if (e && typeof e.error === 'string' && e.error) {
          message = e.error;
        }
      } catch (_nonJSONErrorBody) {
        /* a non-JSON error body leaves the status as the message */
      }
      throw new ApiError(res.status, message, body);
    }
    if (res.status === 204) return undefined as T;
    return (await res.json()) as T;
  }

  private async requestText(path: string): Promise<string> {
    const res = await this.fetchImpl(`${this.baseUrl}${path}`, {
      headers: { 'X-Api-Key': this.apiKey, Accept: 'application/x-ndjson' }
    });
    if (!res.ok) throw new ApiError(res.status, await res.text());
    return res.text();
  }
}
