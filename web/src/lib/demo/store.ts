// SPDX-License-Identifier: AGPL-3.0-or-later
// In-memory, mutable state + a rich finance/risk seed for the client-side demo
// backend. Every collection is typed against the $lib/api interfaces so
// svelte-check enforces the same shapes the real server returns — the demo can
// never drift from the wire contract. Writes from the router mutate this state in
// place, so a created flow / added case note survives a list reload within the
// session (it resets on a hard page reload, which re-seeds).

import type {
  Flow,
  Decision,
  Case,
  Agent,
  AgentRun,
  AgentVersion,
  EvalCase,
  Model,
  Connector,
  ConnectorTemplate,
  Feature,
  Entity,
  EntityEvent,
  Policy,
  PreApproval,
  Monitor,
  Webhook,
  Notification,
  AuditEntry,
  AssertionCase,
  ManagedApiKey,
  PrivacyConfig,
  FlowGrant,
  ScheduledDeploy,
  Identity,
  DriftReport,
  Environment
} from '$lib/api';

// DriftBaseline is the captured disposition distribution a flow's drift report
// measures against (not a wire type — internal bookkeeping for the engine).
export interface DemoState {
  identity: Identity;
  flows: Flow[];
  decisions: Decision[];
  cases: Case[];
  agents: Agent[];
  agentRuns: AgentRun[];
  agentVersions: Map<string, AgentVersion[]>;
  agentEvals: Map<string, EvalCase[]>;
  models: Model[];
  modelBaselines: Map<string, number[]>;
  modelMonitors: Map<string, number>;
  connectors: Connector[];
  connectorCatalog: ConnectorTemplate[];
  features: Feature[];
  entities: Entity[];
  entityEvents: Map<string, EntityEvent[]>;
  policies: Policy[];
  preapprovals: PreApproval[];
  monitors: Map<string, Monitor[]>;
  assertions: Map<string, AssertionCase[]>;
  grants: Map<string, FlowGrant[]>;
  schedules: Map<string, ScheduledDeploy[]>;
  flowBaselines: Map<string, Record<string, number>>;
  flowSlos: Map<string, { success_target: number; latency_target_ms: number }>;
  shadows: Map<string, Map<string, number>>;
  webhooks: Webhook[];
  notifications: Notification[];
  audit: AuditEntry[];
  apiKeys: ManagedApiKey[];
  privacy: PrivacyConfig;
  comments: Map<
    string,
    {
      comment_id: string;
      subject_type: string;
      subject_id: string;
      body: string;
      parent_id?: string;
      author: string;
      at: string;
    }[]
  >;
  seq: number;
}

// Stable timestamps so the seed reads coherently (a few days of history).
const now = new Date('2026-06-22T10:00:00Z');
function ago(hours: number): string {
  return new Date(now.getTime() - hours * 3600_000).toISOString();
}
function ahead(days: number): string {
  return new Date(now.getTime() + days * 86400_000).toISOString();
}

const ACTOR = 'demo@intraktible.dev';

// nextId/nextSeq are module-level counters the router uses to mint ids; the seed
// uses literal ids so cross-references (decision→flow, case→decision) stay stable.
let idCounter = 1000;
export function nextId(prefix: string): string {
  idCounter += 1;
  return `${prefix}_${idCounter.toString(36)}${Date.now().toString(36).slice(-4)}`;
}

// --- Seed builders --------------------------------------------------------------

function seedFlows(): Flow[] {
  const creditGraph = {
    nodes: [
      { id: 'in', type: 'input' as const, name: 'Application', lane: 'Intake' },
      {
        id: 'score',
        type: 'predict' as const,
        name: 'PD model',
        lane: 'Score',
        config: { model: 'credit_pd', output: 'pd' }
      },
      {
        id: 'assign',
        type: 'assignment' as const,
        name: 'Derive',
        lane: 'Score',
        config: { assignments: [{ target: 'risk', expr: 'predict.pd.probability * 100' }] }
      },
      {
        id: 'gate',
        type: 'split' as const,
        name: 'Risk band',
        lane: 'Decide',
        config: {}
      },
      {
        id: 'review',
        type: 'manual_review' as const,
        name: 'Underwriter',
        lane: 'Decide',
        config: { case_type: 'credit_review', sla_days: 3 }
      },
      {
        id: 'out',
        type: 'output' as const,
        name: 'Decision',
        lane: 'Decide',
        config: { assignments: [{ target: 'approved', expr: 'true' }] }
      }
    ],
    edges: [
      { from: 'in', to: 'score' },
      { from: 'score', to: 'assign' },
      { from: 'assign', to: 'gate' },
      { from: 'gate', to: 'out', branch: 'risk < 50' },
      { from: 'gate', to: 'review', branch: 'risk >= 50' },
      { from: 'review', to: 'out' }
    ]
  };
  const amlGraph = {
    nodes: [
      { id: 'in', type: 'input' as const, name: 'Transaction' },
      {
        id: 'rule',
        type: 'assignment' as const,
        name: 'Flag large',
        config: { assignments: [{ target: 'high_value', expr: 'amount > 10000' }] }
      },
      { id: 'out', type: 'output' as const, name: 'Outcome' }
    ],
    edges: [
      { from: 'in', to: 'rule' },
      { from: 'rule', to: 'out' }
    ]
  };

  return [
    {
      flow_id: 'flow_credit',
      slug: 'credit-decision',
      name: 'Consumer Credit Decision',
      latest: 3,
      versions: [
        {
          version: 1,
          etag: 'etag-c1',
          graph: amlGraph,
          published_at: ago(240),
          published_by: ACTOR
        },
        {
          version: 2,
          etag: 'etag-c2',
          graph: creditGraph,
          input_schema: {
            type: 'object',
            properties: {
              income: { type: 'number' },
              debt: { type: 'number' },
              age: { type: 'number' }
            }
          },
          published_at: ago(120),
          published_by: ACTOR
        },
        {
          version: 3,
          etag: 'etag-c3',
          graph: creditGraph,
          input_schema: {
            type: 'object',
            properties: {
              income: { type: 'number' },
              debt: { type: 'number' },
              age: { type: 'number' }
            }
          },
          published_at: ago(36),
          published_by: ACTOR
        }
      ],
      deployments: {
        production: { version: 2 },
        staging: { version: 3 },
        sandbox: { version: 3, challenger_version: 2, challenger_pct: 20 }
      },
      deployment_requests: [
        {
          request_id: 'req_c1',
          environment: 'production',
          version: 3,
          status: 'pending',
          reason: 'Roll out tightened PD cutoff',
          requested_by: 'maker@intraktible.dev',
          requested_at: ago(12)
        }
      ],
      promotion_policy: {
        sandbox: {
          require_assertions: false,
          require_no_firing_monitors: false,
          allow_force: true,
          require_review: false
        },
        staging: {
          require_assertions: true,
          require_no_firing_monitors: true,
          allow_force: true,
          require_review: false
        },
        production: {
          require_assertions: true,
          require_no_firing_monitors: true,
          allow_force: false,
          require_review: true
        }
      }
    },
    {
      flow_id: 'flow_aml',
      slug: 'aml-screening',
      name: 'AML Transaction Screening',
      latest: 2,
      versions: [
        {
          version: 1,
          etag: 'etag-a1',
          graph: amlGraph,
          published_at: ago(300),
          published_by: ACTOR
        },
        { version: 2, etag: 'etag-a2', graph: amlGraph, published_at: ago(48), published_by: ACTOR }
      ],
      deployments: { production: { version: 2 }, sandbox: { version: 2 } }
    },
    {
      flow_id: 'flow_kyc',
      slug: 'kyc-onboarding',
      name: 'KYC Onboarding',
      latest: 1,
      versions: [
        { version: 1, etag: 'etag-k1', graph: amlGraph, published_at: ago(72), published_by: ACTOR }
      ],
      deployments: { sandbox: { version: 1 } }
    }
  ];
}

function seedDecisions(): Decision[] {
  const mk = (
    i: number,
    flowId: string,
    slug: string,
    env: Environment,
    status: 'completed' | 'failed',
    disposition: 'approve' | 'decline' | 'refer' | undefined,
    ms: number,
    hrs: number
  ): Decision => ({
    decision_id: `dec_${i}`,
    flow_id: flowId,
    slug,
    version: 2,
    environment: env,
    variant: i % 5 === 0 ? 'challenger' : 'champion',
    status,
    data: { income: 60000 + i * 1000, debt: 12000, age: 30 + (i % 30) },
    output: { approved: disposition === 'approve', risk: 40 + (i % 50) },
    reason_codes:
      disposition === 'refer'
        ? [{ code: 'HIGH_RISK', description: 'Risk score above auto-approve band' }]
        : disposition === 'decline'
          ? [{ code: 'DTI', description: 'Debt-to-income ratio too high' }]
          : [],
    disposition,
    disposition_reason: disposition === 'refer' ? 'manual review required' : undefined,
    nodes: [
      { node_id: 'in', type: 'input', output: { income: 60000 + i * 1000 } },
      { node_id: 'score', type: 'predict', output: { pd: { score: 0.4, probability: 0.4 } } },
      {
        node_id: 'gate',
        type: 'split',
        output: { branch: disposition === 'refer' ? 'risk >= 50' : 'risk < 50' }
      },
      { node_id: 'out', type: 'output', output: { approved: disposition === 'approve' } }
    ],
    started_at: ago(hrs),
    ended_at: ago(hrs),
    duration_ms: ms
  });

  const out: Decision[] = [];
  const dispositions: ('approve' | 'decline' | 'refer')[] = [
    'approve',
    'approve',
    'refer',
    'decline',
    'approve'
  ];
  for (let i = 1; i <= 24; i++) {
    const flow = i % 3 === 0 ? ['flow_aml', 'aml-screening'] : ['flow_credit', 'credit-decision'];
    const status: 'completed' | 'failed' = i % 11 === 0 ? 'failed' : 'completed';
    const disp = status === 'failed' ? undefined : dispositions[i % dispositions.length];
    const env: Environment = i % 4 === 0 ? 'sandbox' : i % 3 === 0 ? 'staging' : 'production';
    out.push(mk(i, flow[0], flow[1], env, status, disp, 30 + (i % 8) * 12, i));
  }
  return out;
}

function seedCases(): Case[] {
  const base = (
    id: string,
    name: string,
    type: string,
    status: Case['status'],
    assignee: string | undefined,
    slaDays: number,
    daysLeft: number,
    slaState: Case['sla_state'],
    srcDec: string | undefined
  ): Case => ({
    case_id: id,
    company_name: name,
    case_type: type,
    status,
    assignee,
    sla_days: slaDays,
    days_left: daysLeft,
    sla_state: slaState,
    source_decision_id: srcDec,
    context: { risk: 62, segment: 'SMB', exposure_usd: 45000 },
    notes: [
      { author: 'analyst@intraktible.dev', text: 'Requested additional income docs.', at: ago(20) }
    ],
    audit: [
      { type: 'case.opened', actor: 'system', at: ago(48), detail: 'Opened from decision' },
      { type: 'case.note', actor: 'analyst@intraktible.dev', at: ago(20) }
    ],
    created_at: ago(48),
    updated_at: ago(20)
  });
  return [
    base(
      'case_1',
      'Northwind Capital',
      'credit_review',
      'needs_review',
      undefined,
      3,
      2,
      'on_track',
      'dec_3'
    ),
    base('case_2', 'Acme Imports', 'aml_alert', 'in_progress', ACTOR, 5, 1, 'due_soon', 'dec_6'),
    base(
      'case_3',
      'Globex Lending',
      'kyc_review',
      'in_progress',
      'analyst@intraktible.dev',
      2,
      -1,
      'overdue',
      undefined
    ),
    base(
      'case_4',
      'Initech Finance',
      'credit_review',
      'completed',
      ACTOR,
      3,
      1,
      'on_track',
      'dec_9'
    ),
    base(
      'case_5',
      'Umbrella Bank',
      'fraud_review',
      'needs_review',
      undefined,
      4,
      3,
      'on_track',
      'dec_12'
    )
  ];
}

function seedAgents(): Agent[] {
  return [
    {
      name: 'aml-narrative',
      provider: 'anthropic',
      model: 'claude-sonnet',
      system: 'You write concise SAR narratives from transaction context.',
      schema: { type: 'object', properties: { narrative: { type: 'string' } } },
      tools: ['lookup_entity'],
      latest: 2,
      runs: 0, // derived from seeded runs in createState
      updated_at: ago(30)
    },
    {
      name: 'kyc-extract',
      provider: 'anthropic',
      model: 'claude-haiku',
      system: 'Extract structured KYC fields from a document.',
      tools: [],
      latest: 1,
      runs: 0, // derived from seeded runs in createState
      updated_at: ago(72)
    }
  ];
}

function seedModels(): Model[] {
  return [
    {
      name: 'credit_pd',
      kind: 'logistic',
      spec: {
        kind: 'logistic',
        intercept: -3.2,
        coefficients: { dti: 4.1, utilization: 2.3, delinquencies: 1.8 }
      },
      owner: 'risk@intraktible.dev',
      updated_at: ago(96)
    },
    {
      name: 'fraud_score',
      kind: 'gbm',
      spec: {
        kind: 'gbm',
        base: 0.1,
        link: 'logit',
        trees: [
          {
            feature: 'velocity',
            threshold: 5,
            left: { leaf: true, value: -0.5 },
            right: { leaf: true, value: 1.2 }
          }
        ]
      },
      owner: 'fraud@intraktible.dev',
      updated_at: ago(50)
    },
    {
      name: 'aml_risk',
      kind: 'expression',
      spec: { kind: 'expression', expr: 'amount / 10000 + cross_border * 2' },
      owner: 'aml@intraktible.dev',
      updated_at: ago(120)
    }
  ];
}

function seedPolicies(): Policy[] {
  return [
    {
      policy_id: 'pol_credit',
      name: 'Credit Disposition',
      flow_slug: 'credit-decision',
      latest: 1,
      updated_at: ago(40),
      versions: [
        {
          version: 1,
          etag: 'petag-1',
          published_at: ago(40),
          published_by: ACTOR,
          spec: {
            rules: [
              {
                when: 'risk < 30',
                disposition: 'approve',
                code: 'LOW_RISK',
                description: 'Auto-approve low risk'
              },
              {
                when: 'risk >= 70',
                disposition: 'decline',
                code: 'HIGH_RISK',
                description: 'Auto-decline high risk'
              },
              {
                when: 'risk >= 30',
                disposition: 'refer',
                code: 'MID_RISK',
                description: 'Refer mid band'
              }
            ],
            default: 'refer'
          }
        }
      ]
    }
  ];
}

function seedPreApprovals(): PreApproval[] {
  return [
    {
      preapproval_id: 'pa_1',
      entity_type: 'applicant',
      entity_id: 'APP-1001',
      disposition: 'approve',
      terms: { limit_usd: 25000, apr: 12.5 },
      policy_id: 'pol_credit',
      policy_version: 1,
      flow_slug: 'credit-decision',
      valid_until: ahead(20),
      status: 'active',
      honored_count: 3,
      note: 'Pre-approved gold-tier applicant',
      granted_at: ago(120),
      granted_by: ACTOR,
      updated_at: ago(120)
    },
    {
      preapproval_id: 'pa_2',
      entity_type: 'applicant',
      entity_id: 'APP-1002',
      disposition: 'decline',
      flow_slug: 'credit-decision',
      valid_until: ahead(-1),
      status: 'revoked',
      revoked_reason: 'Adverse media match',
      honored_count: 0,
      granted_at: ago(200),
      granted_by: ACTOR,
      updated_at: ago(10)
    }
  ];
}

function seedConnectors(): Connector[] {
  return [
    {
      name: 'experian',
      type: 'http',
      config: { base_url: 'https://api.experian.demo' },
      updated_at: ago(80)
    },
    { name: 'core-banking', type: 'postgres', config: { dsn: 'redacted' }, updated_at: ago(160) }
  ];
}

function seedCatalog(): ConnectorTemplate[] {
  return [
    {
      id: 'experian',
      name: 'Experian Bureau',
      category: 'Credit Bureau',
      type: 'http',
      description: 'Pull a consumer credit report and FICO score.',
      config: { base_url: 'https://api.experian.com' }
    },
    {
      id: 'sanctions',
      name: 'OFAC Sanctions',
      category: 'Compliance',
      type: 'http',
      description: 'Screen an entity against sanctions and watchlists.',
      config: { base_url: 'https://api.sanctions.demo' }
    }
  ];
}

function seedFeatures(): Feature[] {
  return [
    {
      name: 'tx_count_30d',
      entity_type: 'applicant',
      event_name: 'transaction',
      aggregation: 'count',
      window_hours: 720,
      updated_at: ago(60)
    },
    {
      name: 'tx_sum_7d',
      entity_type: 'applicant',
      event_name: 'transaction',
      aggregation: 'sum',
      field: 'amount',
      window_hours: 168,
      updated_at: ago(60)
    }
  ];
}

function seedEntities(): Entity[] {
  return [
    {
      entity_type: 'applicant',
      entity_id: 'APP-1001',
      attributes: { name: 'Jane Doe', segment: 'gold', country: 'US' },
      event_count: 3,
      first_seen: ago(400),
      updated_at: ago(12)
    },
    {
      entity_type: 'applicant',
      entity_id: 'APP-1002',
      attributes: { name: 'John Roe', segment: 'standard', country: 'GB' },
      event_count: 1,
      first_seen: ago(200),
      updated_at: ago(48)
    }
  ];
}

function seedEntityEvents(): Map<string, EntityEvent[]> {
  const m = new Map<string, EntityEvent[]>();
  m.set('applicant/APP-1001', [
    {
      entity_type: 'applicant',
      entity_id: 'APP-1001',
      event_name: 'transaction',
      data: { amount: 1200 },
      seq: 1,
      occurred_at: ago(300),
      recorded_at: ago(300)
    },
    {
      entity_type: 'applicant',
      entity_id: 'APP-1001',
      event_name: 'transaction',
      data: { amount: 4500 },
      seq: 2,
      occurred_at: ago(100),
      recorded_at: ago(100)
    },
    {
      entity_type: 'applicant',
      entity_id: 'APP-1001',
      event_name: 'login',
      data: {},
      seq: 3,
      occurred_at: ago(12),
      recorded_at: ago(12)
    }
  ]);
  m.set('applicant/APP-1002', [
    {
      entity_type: 'applicant',
      entity_id: 'APP-1002',
      event_name: 'transaction',
      data: { amount: 50000 },
      seq: 1,
      occurred_at: ago(200),
      recorded_at: ago(200)
    }
  ]);
  return m;
}

function seedMonitors(): Map<string, Monitor[]> {
  const m = new Map<string, Monitor[]>();
  m.set('flow_credit', [
    {
      monitor_id: 'mon_1',
      flow_id: 'flow_credit',
      metric: 'failure_rate',
      op: 'gt',
      threshold: 0.05,
      description: 'Alert on failures',
      status: { actual: 0.09, computable: true, firing: true }
    },
    {
      monitor_id: 'mon_2',
      flow_id: 'flow_credit',
      metric: 'refer_rate',
      op: 'gt',
      threshold: 0.4,
      description: 'Too many referrals',
      status: { actual: 0.2, computable: true, firing: false }
    }
  ]);
  m.set('flow_aml', [
    {
      monitor_id: 'mon_3',
      flow_id: 'flow_aml',
      metric: 'volume',
      op: 'lt',
      threshold: 10,
      status: { actual: 8, computable: true, firing: true }
    }
  ]);
  return m;
}

function seedAssertions(): Map<string, AssertionCase[]> {
  const m = new Map<string, AssertionCase[]>();
  m.set('flow_credit', [
    {
      name: 'low risk approves',
      input: { income: 90000, debt: 5000, age: 40 },
      expect: { approved: true }
    },
    {
      name: 'high debt refers',
      input: { income: 30000, debt: 40000, age: 25 },
      expect: { approved: false }
    }
  ]);
  return m;
}

function seedWebhooks(): Webhook[] {
  return [
    {
      webhook_id: 'wh_1',
      url: 'https://hooks.slack.demo/risk-alerts',
      note: 'Risk team Slack',
      events: ['monitor.fired'],
      active: true,
      delivery_count: 42,
      last_status: 200,
      last_ok: true,
      last_delivery_at: ago(6),
      created_at: ago(400)
    }
  ];
}

function seedNotifications(): Notification[] {
  return [
    {
      notification_id: 'ntf_1',
      recipient: ACTOR,
      kind: 'mention',
      subject_type: 'case',
      subject_id: 'case_2',
      snippet: '@you please review the AML alert',
      author: 'analyst@intraktible.dev',
      read: false,
      created_at: ago(4)
    },
    {
      notification_id: 'ntf_2',
      recipient: ACTOR,
      kind: 'deployment',
      subject_type: 'flow',
      subject_id: 'flow_credit',
      snippet: 'Deployment request pending your approval',
      author: 'maker@intraktible.dev',
      read: false,
      created_at: ago(12)
    }
  ];
}

function seedAudit(): AuditEntry[] {
  const types = [
    'flow.published',
    'decision.recorded',
    'deployment.requested',
    'case.opened',
    'policy.published',
    'apikey.created'
  ];
  const out: AuditEntry[] = [];
  for (let i = 0; i < 30; i++) {
    out.push({
      seq: 30 - i,
      id: `aud_${30 - i}`,
      time: ago(i * 3),
      actor: i % 4 === 0 ? 'system' : ACTOR,
      stream: i % 2 === 0 ? 'flow_credit' : 'global',
      type: types[i % types.length],
      payload: { detail: `event ${30 - i}` }
    });
  }
  return out;
}

function seedApiKeys(): ManagedApiKey[] {
  return [
    {
      id: 'key_1',
      name: 'Production server',
      identity: { org: 'demo', workspace: 'main', actor: 'svc-prod@intraktible.dev' },
      scope: 'production',
      role: 'editor',
      created_at: ago(2000)
    },
    {
      id: 'key_2',
      name: 'CI sandbox',
      identity: { org: 'demo', workspace: 'main', actor: 'svc-ci@intraktible.dev' },
      scope: 'sandbox',
      role: 'operator',
      created_at: ago(1000),
      expires_at: ahead(90)
    }
  ];
}

function seedAgentVersions(): Map<string, AgentVersion[]> {
  const m = new Map<string, AgentVersion[]>();
  m.set('aml-narrative', [
    {
      version: 2,
      etag: 'av2',
      provider: 'anthropic',
      model: 'claude-sonnet',
      system: 'You write concise SAR narratives from transaction context.',
      published_at: ago(30),
      published_by: ACTOR
    },
    {
      version: 1,
      etag: 'av1',
      provider: 'anthropic',
      model: 'claude-haiku',
      system: 'Draft a SAR narrative.',
      published_at: ago(200),
      published_by: ACTOR
    }
  ]);
  m.set('kyc-extract', [
    {
      version: 1,
      etag: 'kv1',
      provider: 'anthropic',
      model: 'claude-haiku',
      system: 'Extract structured KYC fields from a document.',
      published_at: ago(72),
      published_by: ACTOR
    }
  ]);
  return m;
}

function seedAgentEvals(): Map<string, EvalCase[]> {
  const m = new Map<string, EvalCase[]>();
  m.set('aml-narrative', [
    {
      name: 'mentions amount',
      prompt: 'Wire of $50,000 to a sanctioned region',
      mode: 'contains',
      expect: 'stub'
    },
    { name: 'structured shape', prompt: 'Summarize', mode: 'contains', expect: 'stub' }
  ]);
  return m;
}

function seedAgentRuns(): AgentRun[] {
  const run = (
    id: string,
    agent: string,
    model: string,
    prompt: string,
    hrs: number,
    status: AgentRun['status'] = 'completed'
  ): AgentRun => ({
    run_id: id,
    agent,
    model,
    prompt,
    status,
    text: status === 'failed' ? '' : `stub: ${prompt}`,
    error: status === 'failed' ? 'provider timeout' : undefined,
    at: ago(hrs)
  });
  return [
    run(
      'run_1',
      'aml-narrative',
      'claude-sonnet',
      'Wire of $50,000 to a high-risk jurisdiction',
      8
    ),
    run('run_2', 'aml-narrative', 'claude-sonnet', 'Structuring pattern across 6 deposits', 30),
    run(
      'run_3',
      'aml-narrative',
      'claude-sonnet',
      'Rapid pass-through funding from a shell entity',
      54
    ),
    run(
      'run_4',
      'aml-narrative',
      'claude-sonnet',
      'Cash deposits just under the reporting threshold',
      76
    ),
    run(
      'run_5',
      'aml-narrative',
      'claude-sonnet',
      'Cross-border transfer to a PEP-linked account',
      120,
      'failed'
    ),
    run('run_6', 'kyc-extract', 'claude-haiku', 'Passport, DOB 1990-01-01', 50),
    run('run_7', 'kyc-extract', 'claude-haiku', 'Utility bill, address verification', 96),
    run('run_8', 'kyc-extract', 'claude-haiku', 'Company registration extract', 150)
  ];
}

// createState assembles a fresh seeded state (called once per page load).
export function createState(): DemoState {
  const agentRuns = seedAgentRuns();
  // Derive each agent's run counter from the actual run records, so the agents-page
  // summary, the per-agent count, and the observability/MRM rollups can never drift.
  const agents = seedAgents().map((a) => ({
    ...a,
    runs: agentRuns.filter((r) => r.agent === a.name).length
  }));
  return {
    identity: { org: 'demo', workspace: 'main', actor: ACTOR, role: 'admin', scope: 'production' },
    flows: seedFlows(),
    decisions: seedDecisions(),
    cases: seedCases(),
    agents,
    agentRuns,
    agentVersions: seedAgentVersions(),
    agentEvals: seedAgentEvals(),
    models: seedModels(),
    modelBaselines: new Map([['credit_pd', [3, 5, 8, 6, 2]]]),
    modelMonitors: new Map([['credit_pd', 0.2]]),
    connectors: seedConnectors(),
    connectorCatalog: seedCatalog(),
    features: seedFeatures(),
    entities: seedEntities(),
    entityEvents: seedEntityEvents(),
    policies: seedPolicies(),
    preapprovals: seedPreApprovals(),
    monitors: seedMonitors(),
    assertions: seedAssertions(),
    grants: new Map([
      [
        'flow_credit',
        [
          {
            grant_id: 'g_1',
            flow_id: 'flow_credit',
            actor: 'analyst@intraktible.dev',
            environment: '*',
            created_by: ACTOR,
            created_at: ago(100)
          }
        ]
      ]
    ]),
    schedules: new Map(),
    flowBaselines: new Map([['flow_credit', { approve: 12, decline: 4, refer: 6 }]]),
    flowSlos: new Map([['flow_credit', { success_target: 0.95, latency_target_ms: 200 }]]),
    shadows: new Map([['flow_credit', new Map([['production', 3]])]]),
    webhooks: seedWebhooks(),
    notifications: seedNotifications(),
    audit: seedAudit(),
    apiKeys: seedApiKeys(),
    privacy: { fields: ['ssn', 'dob'], updated_at: ago(500), updated_by: ACTOR },
    comments: new Map(),
    seq: 31
  };
}

// The single shared, mutable state instance for the session.
export const state: DemoState = createState();

// driftReportFor computes a DriftReport from a flow's captured baseline vs the
// current disposition distribution over its recorded decisions.
export function driftReportFor(flowId: string): DriftReport {
  const baseline = state.flowBaselines.get(flowId);
  // Counts keyed via a Map (not a plain object) so the variable-key writes don't
  // trip eslint-plugin-security's object-injection rule.
  const counts = new Map<string, number>([
    ['approve', 0],
    ['decline', 0],
    ['refer', 0]
  ]);
  let current = 0;
  for (const d of state.decisions) {
    if (d.flow_id === flowId && d.disposition) {
      counts.set(d.disposition, (counts.get(d.disposition) ?? 0) + 1);
      current += 1;
    }
  }
  if (!baseline) {
    return {
      has_baseline: false,
      has_current: current > 0,
      max_drift: 0,
      psi: 0,
      kl: 0,
      current_total: current
    };
  }
  const baseMap = new Map(Object.entries(baseline));
  const baseTotal = [...baseMap.values()].reduce((a, b) => a + b, 0) || 1;
  const curTotal = current || 1;
  const dispoKeys: ('approve' | 'decline' | 'refer')[] = ['approve', 'decline', 'refer'];
  let psi = 0;
  let kl = 0;
  let maxDrift = 0;
  const buckets = dispoKeys.map((k) => {
    const baseCount = baseMap.get(k) ?? 0;
    const curCount = counts.get(k) ?? 0;
    const b = baseCount / baseTotal || 0.0001;
    const c = curCount / curTotal || 0.0001;
    psi += (c - b) * Math.log(c / b);
    kl += c * Math.log(c / b);
    maxDrift = Math.max(maxDrift, Math.abs(c - b));
    return {
      disposition: k,
      baseline: baseCount,
      current: curCount,
      delta: Math.round((c - b) * 1000) / 1000
    };
  });
  return {
    has_baseline: true,
    has_current: current > 0,
    max_drift: Math.round(maxDrift * 1000) / 1000,
    psi: Math.round(psi * 1000) / 1000,
    kl: Math.round(kl * 1000) / 1000,
    baseline_total: baseTotal,
    current_total: current,
    buckets
  };
}

export { ACTOR, ago, ahead };
