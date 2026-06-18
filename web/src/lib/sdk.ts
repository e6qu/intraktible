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
}

export interface DecideResult {
  decision_id: string;
  status: 'completed' | 'failed';
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
    return this.request<DecideResult>('POST', `${this.flowPath(slug, env)}/decide`, req);
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

  me(): Promise<Identity> {
    return this.request<Identity>('GET', '/v1/me');
  }

  private flowPath(slug: string, env: string): string {
    return `/v1/flows/${encodeURIComponent(slug)}/${encodeURIComponent(env)}`;
  }

  private async request<T>(method: string, path: string, body?: unknown): Promise<T> {
    const headers: Record<string, string> = {
      'X-Api-Key': this.apiKey,
      Accept: 'application/json'
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
      } catch {
        /* a non-JSON error body leaves the status as the message */
      }
      throw new ApiError(res.status, message);
    }
    return (await res.json()) as T;
  }
}
