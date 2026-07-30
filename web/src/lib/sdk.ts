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
  constructor(status: number, message: string) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
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

export interface FlowDoc {
  slug: string;
  name?: string;
  graph: unknown;
  input_schema?: unknown;
}

export interface ImportResult {
  flow_id: string;
  slug: string;
  version: number;
  created: boolean;
  published: boolean;
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
  slug: string;
  flow_id?: string;
  version?: number;
  created: boolean;
  published: boolean;
  error?: string;
}

export interface BundleResult {
  results: BundleFlowResult[];
  published: number;
  failed: number;
  unchanged: number;
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

  // importFlow upserts a flow from a flow-as-code document.
  importFlow(doc: FlowDoc): Promise<ImportResult> {
    return this.request<ImportResult>('POST', '/v1/flows/import', doc);
  }

  // importBundle imports many flows in one request (best-effort per flow).
  importBundle(docs: FlowDoc[]): Promise<BundleResult> {
    return this.request<BundleResult>('POST', '/v1/flows/import-bundle', { flows: docs });
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

  me(): Promise<Identity> {
    return this.request<Identity>('GET', '/v1/me');
  }

  private flowPath(slug: string, env: string): string {
    return `/v1/flows/${encodeURIComponent(slug)}/${encodeURIComponent(env)}`;
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
      try {
        const e = (await res.json()) as { error?: string };
        if (e && typeof e.error === 'string' && e.error) {
          message = e.error;
        }
      } catch (_nonJSONErrorBody) {
        /* a non-JSON error body leaves the status as the message */
      }
      throw new ApiError(res.status, message);
    }
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
