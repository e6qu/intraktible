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
  NodeRecord,
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
  Environment,
  Role
} from '$lib/api';
import { agentReply } from './agent';

// DemoUser is one entry in the demo's cast: a named person with an RBAC role. The
// demo identity switcher (DemoBanner) lets a visitor view the app AS any of them, so
// role-gated surfaces (admin-only Model risk / Audit, maker-checker, etc.) change
// live. Seeded data (case assignees, audit actors, comment authors, approvers,
// model/agent owners) is woven from this roster so the app reads like a real team's.
export interface DemoUser {
  actor: string;
  name: string;
  role: Role;
  title: string;
}

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

// Anchor the seed to the REAL current time (floored to the hour for stable reads within
// a session), so every ago()/ahead() offset stays correctly relative. A fixed past date
// drifts: "expiring soon" pre-approvals and scheduled deploys render as already expired
// once the real clock passes it.
const now = (() => {
  const d = new Date();
  d.setMinutes(0, 0, 0);
  return d;
})();
function ago(hours: number): string {
  return new Date(now.getTime() - hours * 3600_000).toISOString();
}
function ahead(days: number): string {
  return new Date(now.getTime() + days * 86400_000).toISOString();
}

const ACTOR = 'ava.chen@intraktible.dev';

// The demo cast, ordered by descending privilege. The first (admin) is the default
// signed-in identity. Roles match the platform's RBAC ranks (viewer < operator <
// editor < approver < admin). USER_BY exposes them by actor for seed cross-refs.
export const USERS: DemoUser[] = [
  { actor: ACTOR, name: 'Ava Chen', role: 'admin', title: 'Head of Decisioning' },
  {
    actor: 'marcus.reed@intraktible.dev',
    name: 'Marcus Reed',
    role: 'approver',
    title: 'Risk Approver'
  },
  { actor: 'priya.nair@intraktible.dev', name: 'Priya Nair', role: 'editor', title: 'Flow Author' },
  {
    actor: 'diego.santos@intraktible.dev',
    name: 'Diego Santos',
    role: 'operator',
    title: 'Case Analyst'
  },
  {
    actor: 'lena.hoff@intraktible.dev',
    name: 'Lena Hoff',
    role: 'viewer',
    title: 'Audit & Compliance'
  }
];

function identityFor(u: DemoUser): Identity {
  return { org: 'demo', workspace: 'main', actor: u.actor, role: u.role, scope: 'production' };
}

// setDemoUser switches the signed-in identity the mocked /v1/me returns; the
// DemoBanner switcher calls this then triggers the app's refreshUser(). Unknown
// actors are ignored (the current identity stays).
export function setDemoUser(actor: string): Identity {
  const u = USERS.find((x) => x.actor === actor);
  if (u) {
    state.identity = identityFor(u);
    persist(); // the switched identity must survive a reload, like every other write
  }
  return state.identity;
}

// nextId/nextSeq are module-level counters the router uses to mint ids; the seed
// uses literal ids so cross-references (decision→flow, case→decision) stay stable.
let idCounter = 1000;
export function nextId(prefix: string): string {
  idCounter += 1;
  return `${prefix}_${idCounter.toString(36)}${Date.now().toString(36).slice(-4)}`;
}

// pushAudit appends one entry to the workspace event log (newest first), attributed
// to the signed-in actor — the demo's eventlog.AppendJSON. Streams and types follow
// the real taxonomy (decision.flows / decision.runs / cases / auth / …); resource
// ids live in the payload, matched by the audit page's resource filter.
export function pushAudit(type: string, stream: string, payload?: unknown): void {
  state.seq += 1;
  state.audit.unshift({
    seq: state.seq,
    id: `aud_${state.seq}`,
    time: new Date().toISOString(),
    actor: state.identity.actor,
    stream,
    type,
    payload
  });
}

// auditRunSteps journals one decision.run.node_evaluated per trace node, the way
// the real engine appends a step event for every node it walks.
export function auditRunSteps(decisionId: string, nodes: NodeRecord[]): void {
  for (const n of nodes) {
    pushAudit('decision.run.node_evaluated', 'decision.runs', {
      decision_id: decisionId,
      node_id: n.node_id,
      node_type: n.type
    });
  }
}

// auditRunEnd journals a run's terminal event from its recorded status.
export function auditRunEnd(d: Decision): void {
  if (d.status === 'completed') {
    pushAudit('decision.run.completed', 'decision.runs', {
      decision_id: d.decision_id,
      disposition: d.disposition
    });
  } else if (d.status === 'failed') {
    pushAudit('decision.run.failed', 'decision.runs', {
      decision_id: d.decision_id,
      error: d.error
    });
  } else {
    pushAudit('decision.run.suspended', 'decision.runs', { decision_id: d.decision_id });
  }
}

// auditDecisionRun mirrors a freshly recorded run into the event log exactly as the
// real engine journals it: started, node_evaluated per step, manual_review_requested
// when the run opened a case, then the terminal event.
export function auditDecisionRun(d: Decision): void {
  pushAudit('decision.run.started', 'decision.runs', {
    decision_id: d.decision_id,
    flow_id: d.flow_id,
    environment: d.environment
  });
  auditRunSteps(d.decision_id, d.nodes ?? []);
  if (d.case_id) {
    pushAudit('decision.manual_review_requested', 'decision.runs', {
      decision_id: d.decision_id,
      case_id: d.case_id
    });
  }
  auditRunEnd(d);
}

// --- Seed builders --------------------------------------------------------------

// Roster actor shortcuts so the seed reads like a real team (admin..viewer).
const AVA = USERS[0].actor; // admin — Head of Decisioning (=== ACTOR)
const MARCUS = USERS[1].actor; // approver — Risk Approver
const PRIYA = USERS[2].actor; // editor — Flow Author
const DIEGO = USERS[3].actor; // operator — Case Analyst
const LENA = USERS[4].actor; // viewer — Audit & Compliance

// strict promotion policy: gates tighten as you climb toward production. Reused by
// every flow so the maker-checker / promotion surfaces read consistently.
const STRICT_PROMOTION = {
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
};

function seedFlows(): Flow[] {
  // Consumer Credit: enrich → PD model → derive risk → narrative → 3-way band →
  // underwriter review → decision. Decidable: predict reads dti/utilization/
  // delinquencies off the record (set in `enrich`), split branches read `risk`.
  const creditGraph = {
    nodes: [
      { id: 'in', type: 'input' as const, name: 'Loan application', lane: 'Intake' },
      {
        id: 'enrich',
        type: 'assignment' as const,
        name: 'Enrich bureau features',
        lane: 'Intake',
        config: {
          assignments: [
            { target: 'dti', expr: '(debt / income)' },
            { target: 'utilization', expr: '(revolving_balance / credit_limit)' },
            { target: 'delinquencies', expr: 'delinquencies_24m' },
            { target: 'fico_score', expr: 'fico_score' }
          ]
        }
      },
      {
        id: 'propensity',
        type: 'predict' as const,
        name: 'Repayment propensity',
        lane: 'Score',
        config: { model: 'repayment_propensity', output: 'propensity' }
      },
      {
        id: 'score',
        type: 'predict' as const,
        name: 'Probability of default',
        lane: 'Score',
        config: { model: 'credit_pd', output: 'pd' }
      },
      {
        id: 'derive',
        type: 'assignment' as const,
        name: 'Derive risk + limit',
        lane: 'Score',
        config: {
          assignments: [
            { target: 'risk', expr: 'predict.pd.probability * 100' },
            {
              target: 'offered_limit',
              expr: 'risk >= 70 ? 0 : ((income - debt) / 12 * 4 < income * 0.1 ? (income - debt) / 12 * 4 : income * 0.1)'
            }
          ]
        }
      },
      {
        id: 'narrative',
        type: 'ai' as const,
        name: 'Adverse-action draft',
        lane: 'Score',
        config: {
          prompt: 'Draft an adverse-action rationale from the risk drivers',
          output: 'rationale'
        }
      },
      { id: 'band', type: 'split' as const, name: 'Risk band', lane: 'Decide', config: {} },
      {
        id: 'review',
        type: 'manual_review' as const,
        name: 'Underwriter review',
        lane: 'Decide',
        config: { case_type: 'credit_review', sla_days: 3 }
      },
      {
        id: 'approve',
        type: 'assignment' as const,
        name: 'Approve',
        lane: 'Decide',
        config: { assignments: [{ target: 'approved', expr: 'true' }] }
      },
      {
        id: 'decline',
        type: 'assignment' as const,
        name: 'Decline',
        lane: 'Decide',
        config: { assignments: [{ target: 'approved', expr: 'false' }] }
      },
      {
        id: 'out',
        type: 'output' as const,
        name: 'Credit decision',
        lane: 'Decide',
        config: { assignments: [{ target: 'flow', expr: '"credit-decision"' }] }
      }
    ],
    edges: [
      { from: 'in', to: 'enrich' },
      { from: 'enrich', to: 'propensity' },
      { from: 'propensity', to: 'score' },
      { from: 'score', to: 'derive' },
      { from: 'derive', to: 'narrative' },
      { from: 'narrative', to: 'band' },
      { from: 'band', to: 'approve', branch: 'risk < 35' },
      { from: 'band', to: 'decline', branch: 'risk >= 70' },
      { from: 'band', to: 'review', branch: 'risk >= 35' },
      { from: 'approve', to: 'out' },
      { from: 'decline', to: 'out' },
      { from: 'review', to: 'out' }
    ]
  };
  // Earlier, simpler credit graph (v1) — kept so the version history is real.
  const creditGraphV1 = {
    nodes: [
      { id: 'in', type: 'input' as const, name: 'Loan application', lane: 'Intake' },
      {
        id: 'enrich',
        type: 'assignment' as const,
        name: 'Compute DTI',
        lane: 'Score',
        config: { assignments: [{ target: 'dti', expr: '(debt / income)' }] }
      },
      {
        id: 'score',
        type: 'predict' as const,
        name: 'PD model',
        lane: 'Score',
        config: { model: 'credit_pd', output: 'pd' }
      },
      {
        id: 'derive',
        type: 'assignment' as const,
        name: 'Risk',
        lane: 'Decide',
        config: { assignments: [{ target: 'risk', expr: 'predict.pd.probability * 100' }] }
      },
      { id: 'band', type: 'split' as const, name: 'Risk band', lane: 'Decide', config: {} },
      {
        id: 'approve',
        type: 'assignment' as const,
        name: 'Approve',
        lane: 'Decide',
        config: { assignments: [{ target: 'approved', expr: 'true' }] }
      },
      {
        id: 'review',
        type: 'manual_review' as const,
        name: 'Refer',
        lane: 'Decide',
        config: { case_type: 'credit_review', sla_days: 3 }
      },
      { id: 'out', type: 'output' as const, name: 'Decision', lane: 'Decide', config: {} }
    ],
    edges: [
      { from: 'in', to: 'enrich' },
      { from: 'enrich', to: 'score' },
      { from: 'score', to: 'derive' },
      { from: 'derive', to: 'band' },
      { from: 'band', to: 'approve', branch: 'risk < 50' },
      { from: 'band', to: 'review', branch: 'risk >= 50' },
      { from: 'approve', to: 'out' },
      { from: 'review', to: 'out' }
    ]
  };

  // AML transaction screening: derive features → aml_risk model → narrative →
  // 3-way band → SAR analyst review → outcome. Decidable: aml_risk expression reads
  // amount/cross_border; split reads aml_score.
  const amlGraph = {
    nodes: [
      { id: 'in', type: 'input' as const, name: 'Wire / transfer', lane: 'Intake' },
      {
        id: 'feat',
        type: 'assignment' as const,
        name: 'Screening features',
        lane: 'Enrich',
        config: {
          assignments: [
            { target: 'cross_border', expr: 'origin_country != dest_country ? 1 : 0' },
            { target: 'high_value', expr: 'amount > 10000 ? 1 : 0' }
          ]
        }
      },
      {
        id: 'sanctions',
        type: 'assignment' as const,
        name: 'Sanctions hit',
        lane: 'Enrich',
        config: {
          assignments: [{ target: 'sanctions_hit', expr: 'watchlist_score >= 80 ? 1 : 0' }]
        }
      },
      {
        id: 'score',
        type: 'predict' as const,
        name: 'AML risk score',
        lane: 'Score',
        config: { model: 'aml_risk', output: 'aml' }
      },
      {
        id: 'derive',
        type: 'assignment' as const,
        name: 'Compose risk',
        lane: 'Score',
        config: {
          assignments: [{ target: 'aml_score', expr: 'predict.aml.score + sanctions_hit * 5' }]
        }
      },
      {
        id: 'sar',
        type: 'ai' as const,
        name: 'SAR narrative draft',
        lane: 'Score',
        config: {
          prompt: 'Draft a SAR narrative from the transaction context',
          output: 'narrative'
        }
      },
      { id: 'band', type: 'split' as const, name: 'Risk band', lane: 'Decide', config: {} },
      {
        id: 'review',
        type: 'manual_review' as const,
        name: 'AML analyst review',
        lane: 'Decide',
        config: { case_type: 'aml_alert', sla_days: 5 }
      },
      {
        id: 'clear',
        type: 'assignment' as const,
        name: 'Clear',
        lane: 'Decide',
        config: { assignments: [{ target: 'cleared', expr: 'true' }] }
      },
      {
        id: 'out',
        type: 'output' as const,
        name: 'Screening outcome',
        lane: 'Decide',
        config: {
          assignments: [{ target: 'cleared', expr: 'sanctions_hit == 1 ? false : aml_score < 6' }]
        }
      }
    ],
    edges: [
      { from: 'in', to: 'feat' },
      { from: 'feat', to: 'sanctions' },
      { from: 'sanctions', to: 'score' },
      { from: 'score', to: 'derive' },
      { from: 'derive', to: 'sar' },
      { from: 'sar', to: 'band' },
      { from: 'band', to: 'review', branch: 'sanctions_hit == 1' },
      { from: 'band', to: 'review', branch: 'aml_score >= 6' },
      { from: 'band', to: 'clear', branch: 'aml_score < 6' },
      { from: 'clear', to: 'out' },
      { from: 'review', to: 'out' }
    ]
  };
  const amlGraphV1 = {
    nodes: [
      { id: 'in', type: 'input' as const, name: 'Transaction', lane: 'Intake' },
      {
        id: 'rule',
        type: 'assignment' as const,
        name: 'Flag large',
        lane: 'Score',
        config: { assignments: [{ target: 'aml_score', expr: 'amount / 10000' }] }
      },
      { id: 'band', type: 'split' as const, name: 'Band', lane: 'Decide', config: {} },
      {
        id: 'review',
        type: 'manual_review' as const,
        name: 'Review',
        lane: 'Decide',
        config: { case_type: 'aml_alert', sla_days: 5 }
      },
      {
        id: 'clear',
        type: 'assignment' as const,
        name: 'Clear',
        lane: 'Decide',
        config: { assignments: [{ target: 'cleared', expr: 'true' }] }
      },
      { id: 'out', type: 'output' as const, name: 'Outcome', lane: 'Decide', config: {} }
    ],
    edges: [
      { from: 'in', to: 'rule' },
      { from: 'rule', to: 'band' },
      { from: 'band', to: 'review', branch: 'aml_score >= 2' },
      { from: 'band', to: 'clear', branch: 'aml_score < 2' },
      { from: 'clear', to: 'out' },
      { from: 'review', to: 'out' }
    ]
  };

  // KYC onboarding: extract document fields → kyc_score (external) → derive →
  // 2-way → compliance review → onboard. Decidable: external model stubs 0.5.
  const kycGraph = {
    nodes: [
      { id: 'in', type: 'input' as const, name: 'Onboarding packet', lane: 'Intake' },
      {
        id: 'extract',
        type: 'ai' as const,
        name: 'Document extract',
        lane: 'Enrich',
        config: { prompt: 'Extract KYC fields from the submitted documents', output: 'extracted' }
      },
      {
        id: 'pep',
        type: 'assignment' as const,
        name: 'PEP / adverse media',
        lane: 'Enrich',
        config: {
          assignments: [
            { target: 'pep_flag', expr: 'pep_match >= 1 ? 1 : 0' },
            { target: 'doc_quality', expr: 'doc_score' }
          ]
        }
      },
      {
        id: 'score',
        type: 'predict' as const,
        name: 'KYC vendor score',
        lane: 'Score',
        config: { model: 'kyc_score', output: 'kyc' }
      },
      {
        id: 'derive',
        type: 'assignment' as const,
        name: 'Identity confidence',
        lane: 'Score',
        config: { assignments: [{ target: 'identity_conf', expr: 'doc_quality - pep_flag * 40' }] }
      },
      { id: 'gate', type: 'split' as const, name: 'Verify gate', lane: 'Decide', config: {} },
      {
        id: 'review',
        type: 'manual_review' as const,
        name: 'EDD review',
        lane: 'Decide',
        config: { case_type: 'kyc_review', sla_days: 2 }
      },
      {
        id: 'pass',
        type: 'assignment' as const,
        name: 'Verified',
        lane: 'Decide',
        config: { assignments: [{ target: 'verified', expr: 'true' }] }
      },
      { id: 'out', type: 'output' as const, name: 'Onboarding result', lane: 'Decide', config: {} }
    ],
    edges: [
      { from: 'in', to: 'extract' },
      { from: 'extract', to: 'pep' },
      { from: 'pep', to: 'score' },
      { from: 'score', to: 'derive' },
      { from: 'derive', to: 'gate' },
      { from: 'gate', to: 'review', branch: 'identity_conf < 60' },
      { from: 'gate', to: 'pass', branch: 'identity_conf >= 60' },
      { from: 'pass', to: 'out' },
      { from: 'review', to: 'out' }
    ]
  };

  // Card fraud scoring: velocity/device features → fraud_score (gbm) → 3-way →
  // fraud analyst review → outcome. Decidable: gbm reads velocity, split reads score.
  const fraudGraph = {
    nodes: [
      { id: 'in', type: 'input' as const, name: 'Authorization', lane: 'Intake' },
      {
        id: 'feat',
        type: 'assignment' as const,
        name: 'Velocity + device',
        lane: 'Enrich',
        config: {
          assignments: [
            { target: 'velocity', expr: 'tx_count_1h' },
            { target: 'device_risk', expr: 'device_score' },
            { target: 'amount_ratio', expr: '(amount / avg_ticket)' }
          ]
        }
      },
      {
        id: 'score',
        type: 'predict' as const,
        name: 'Fraud model',
        lane: 'Score',
        config: { model: 'fraud_score', output: 'fraud' }
      },
      {
        id: 'derive',
        type: 'assignment' as const,
        name: 'Fraud probability',
        lane: 'Score',
        config: { assignments: [{ target: 'fraud_p', expr: 'predict.fraud.probability * 100' }] }
      },
      {
        id: 'explain',
        type: 'ai' as const,
        name: 'Explanation',
        lane: 'Score',
        config: { prompt: 'Explain the fraud score drivers for the analyst', output: 'explanation' }
      },
      { id: 'band', type: 'split' as const, name: 'Fraud band', lane: 'Decide', config: {} },
      {
        id: 'review',
        type: 'manual_review' as const,
        name: 'Fraud analyst review',
        lane: 'Decide',
        config: { case_type: 'fraud_review', sla_days: 1 }
      },
      {
        id: 'block',
        type: 'assignment' as const,
        name: 'Block',
        lane: 'Decide',
        config: { assignments: [{ target: 'blocked', expr: 'true' }] }
      },
      {
        id: 'allow',
        type: 'assignment' as const,
        name: 'Allow',
        lane: 'Decide',
        config: { assignments: [{ target: 'blocked', expr: 'false' }] }
      },
      { id: 'out', type: 'output' as const, name: 'Auth decision', lane: 'Decide', config: {} }
    ],
    edges: [
      { from: 'in', to: 'feat' },
      { from: 'feat', to: 'score' },
      { from: 'score', to: 'derive' },
      { from: 'derive', to: 'explain' },
      { from: 'explain', to: 'band' },
      { from: 'band', to: 'block', branch: 'fraud_p >= 80' },
      { from: 'band', to: 'review', branch: 'fraud_p >= 40' },
      { from: 'band', to: 'allow', branch: 'fraud_p < 40' },
      { from: 'block', to: 'out' },
      { from: 'allow', to: 'out' },
      { from: 'review', to: 'out' }
    ]
  };

  // Dispute / chargeback triage: classify → summarize → 3-way → ops review → route.
  const disputeGraph = {
    nodes: [
      { id: 'in', type: 'input' as const, name: 'Dispute intake', lane: 'Intake' },
      {
        id: 'classify',
        type: 'assignment' as const,
        name: 'Classify + liability',
        lane: 'Triage',
        config: {
          assignments: [
            { target: 'high_value', expr: 'amount > 500 ? 1 : 0' },
            { target: 'liability', expr: 'reason_code == "fraud" ? 1 : 0' }
          ]
        }
      },
      {
        id: 'summary',
        type: 'ai' as const,
        name: 'Dispute summary',
        lane: 'Triage',
        config: {
          prompt: 'Summarize the dispute and recommend representment vs refund',
          output: 'summary'
        }
      },
      {
        id: 'derive',
        type: 'assignment' as const,
        name: 'Triage score',
        lane: 'Triage',
        config: { assignments: [{ target: 'triage', expr: 'high_value * 50 + liability * 40' }] }
      },
      { id: 'band', type: 'split' as const, name: 'Triage band', lane: 'Decide', config: {} },
      {
        id: 'review',
        type: 'manual_review' as const,
        name: 'Disputes ops review',
        lane: 'Decide',
        config: { case_type: 'dispute', sla_days: 7 }
      },
      {
        id: 'refund',
        type: 'assignment' as const,
        name: 'Auto-refund',
        lane: 'Decide',
        config: { assignments: [{ target: 'outcome', expr: '"refund"' }] }
      },
      { id: 'out', type: 'output' as const, name: 'Disposition', lane: 'Decide', config: {} }
    ],
    edges: [
      { from: 'in', to: 'classify' },
      { from: 'classify', to: 'summary' },
      { from: 'summary', to: 'derive' },
      { from: 'derive', to: 'band' },
      { from: 'band', to: 'review', branch: 'triage >= 50' },
      { from: 'band', to: 'refund', branch: 'triage < 50' },
      { from: 'refund', to: 'out' },
      { from: 'review', to: 'out' }
    ]
  };

  // Merchant onboarding: risk features → aml_risk reuse → underwriting review.
  const merchantGraph = {
    nodes: [
      { id: 'in', type: 'input' as const, name: 'Merchant application', lane: 'Intake' },
      {
        id: 'feat',
        type: 'assignment' as const,
        name: 'MCC + volume risk',
        lane: 'Enrich',
        config: {
          assignments: [
            { target: 'high_risk_mcc', expr: 'mcc_risk >= 70 ? 1 : 0' },
            { target: 'amount', expr: 'monthly_volume' },
            { target: 'cross_border', expr: 'international ? 1 : 0' }
          ]
        }
      },
      {
        id: 'score',
        type: 'predict' as const,
        name: 'Merchant risk score',
        lane: 'Score',
        config: { model: 'aml_risk', output: 'mrisk' }
      },
      {
        id: 'derive',
        type: 'assignment' as const,
        name: 'Underwriting score',
        lane: 'Score',
        config: {
          assignments: [{ target: 'uw_score', expr: 'predict.mrisk.score + high_risk_mcc * 30' }]
        }
      },
      { id: 'gate', type: 'split' as const, name: 'Underwriting gate', lane: 'Decide', config: {} },
      {
        id: 'review',
        type: 'manual_review' as const,
        name: 'Underwriting review',
        lane: 'Decide',
        config: { case_type: 'merchant_review', sla_days: 4 }
      },
      {
        id: 'approve',
        type: 'assignment' as const,
        name: 'Board merchant',
        lane: 'Decide',
        config: { assignments: [{ target: 'boarded', expr: 'true' }] }
      },
      { id: 'out', type: 'output' as const, name: 'Boarding result', lane: 'Decide', config: {} }
    ],
    edges: [
      { from: 'in', to: 'feat' },
      { from: 'feat', to: 'score' },
      { from: 'score', to: 'derive' },
      { from: 'derive', to: 'gate' },
      { from: 'gate', to: 'review', branch: 'uw_score >= 25' },
      { from: 'gate', to: 'approve', branch: 'uw_score < 25' },
      { from: 'approve', to: 'out' },
      { from: 'review', to: 'out' }
    ]
  };

  // Examples land a sampled "Sample input" run in a real (mid) band so a test run
  // routes a branch and returns a disposition instead of failing "no branch matched".
  const creditSchema = {
    type: 'object',
    properties: {
      income: { type: 'number', example: 52000 },
      debt: { type: 'number', example: 14000 },
      revolving_balance: { type: 'number', example: 4200 },
      credit_limit: { type: 'number', example: 12000 },
      delinquencies_24m: { type: 'number', example: 0 },
      fico_score: { type: 'number', example: 668 },
      tenure_years: { type: 'number', example: 4 },
      employment_stability: { type: 'number', example: 0.8 }
    }
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
          graph: creditGraphV1,
          published_at: ago(540),
          published_by: AVA
        },
        {
          version: 2,
          etag: 'etag-c2',
          graph: creditGraph,
          input_schema: creditSchema,
          published_at: ago(180),
          published_by: PRIYA
        },
        {
          version: 3,
          etag: 'etag-c3',
          graph: creditGraph,
          input_schema: creditSchema,
          published_at: ago(36),
          published_by: PRIYA
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
          reason: 'Roll out tightened PD cutoff + adverse-action narrative',
          requested_by: PRIYA,
          requested_at: ago(12)
        }
      ],
      promotion_policy: STRICT_PROMOTION
    },
    {
      flow_id: 'flow_aml',
      slug: 'aml-screening',
      name: 'AML Transaction Screening',
      latest: 3,
      versions: [
        {
          version: 1,
          etag: 'etag-a1',
          graph: amlGraphV1,
          published_at: ago(480),
          published_by: AVA
        },
        {
          version: 2,
          etag: 'etag-a2',
          graph: amlGraph,
          published_at: ago(96),
          published_by: PRIYA
        },
        { version: 3, etag: 'etag-a3', graph: amlGraph, published_at: ago(20), published_by: PRIYA }
      ],
      deployments: {
        production: { version: 2 },
        staging: { version: 3, challenger_version: 2, challenger_pct: 30 },
        sandbox: { version: 3 }
      },
      deployment_requests: [
        {
          request_id: 'req_a1',
          environment: 'production',
          version: 3,
          status: 'pending',
          reason: 'Add sanctions composite + SAR narrative to prod',
          requested_by: DIEGO,
          requested_at: ago(8)
        }
      ],
      promotion_policy: STRICT_PROMOTION
    },
    {
      flow_id: 'flow_kyc',
      slug: 'kyc-onboarding',
      name: 'KYC Onboarding',
      latest: 2,
      versions: [
        {
          version: 1,
          etag: 'etag-k1',
          graph: kycGraph,
          published_at: ago(220),
          published_by: PRIYA
        },
        { version: 2, etag: 'etag-k2', graph: kycGraph, published_at: ago(60), published_by: PRIYA }
      ],
      deployments: {
        production: { version: 2 },
        staging: { version: 2 },
        sandbox: { version: 2 }
      },
      promotion_policy: STRICT_PROMOTION
    },
    {
      flow_id: 'flow_fraud',
      slug: 'card-fraud',
      name: 'Card Fraud Scoring',
      latest: 4,
      versions: [
        {
          version: 1,
          etag: 'etag-f1',
          graph: fraudGraph,
          published_at: ago(400),
          published_by: AVA
        },
        {
          version: 2,
          etag: 'etag-f2',
          graph: fraudGraph,
          published_at: ago(200),
          published_by: PRIYA
        },
        {
          version: 3,
          etag: 'etag-f3',
          graph: fraudGraph,
          published_at: ago(72),
          published_by: PRIYA
        },
        {
          version: 4,
          etag: 'etag-f4',
          graph: fraudGraph,
          published_at: ago(10),
          published_by: PRIYA
        }
      ],
      deployments: {
        production: { version: 3, challenger_version: 4, challenger_pct: 15 },
        staging: { version: 4 },
        sandbox: { version: 4 }
      },
      promotion_policy: STRICT_PROMOTION
    },
    {
      flow_id: 'flow_dispute',
      slug: 'dispute-triage',
      name: 'Dispute / Chargeback Triage',
      latest: 2,
      versions: [
        {
          version: 1,
          etag: 'etag-d1',
          graph: disputeGraph,
          published_at: ago(150),
          published_by: DIEGO
        },
        {
          version: 2,
          etag: 'etag-d2',
          graph: disputeGraph,
          published_at: ago(40),
          published_by: PRIYA
        }
      ],
      deployments: {
        production: { version: 1 },
        staging: { version: 2 },
        sandbox: { version: 2 }
      },
      deployment_requests: [
        {
          request_id: 'req_d1',
          environment: 'production',
          version: 2,
          status: 'pending',
          reason: 'Promote auto-refund threshold change',
          requested_by: DIEGO,
          requested_at: ago(30)
        }
      ],
      promotion_policy: STRICT_PROMOTION
    },
    {
      flow_id: 'flow_merchant',
      slug: 'merchant-onboarding',
      name: 'Merchant Onboarding',
      latest: 2,
      versions: [
        {
          version: 1,
          etag: 'etag-m1',
          graph: merchantGraph,
          published_at: ago(110),
          published_by: PRIYA
        },
        {
          version: 2,
          etag: 'etag-m2',
          graph: merchantGraph,
          published_at: ago(28),
          published_by: PRIYA
        }
      ],
      deployments: {
        staging: { version: 2 },
        sandbox: { version: 2 }
      },
      promotion_policy: STRICT_PROMOTION
    }
  ];
}

// Each flow profile shapes the recorded decisions for one flow: its node trace, the
// realistic input/output payloads, and which policy (if any) binds the disposition.
interface FlowProfile {
  flow_id: string;
  slug: string;
  versions: number[];
  policy_id?: string;
  build: (
    i: number,
    disp: 'approve' | 'decline' | 'refer' | undefined
  ) => {
    data: Record<string, unknown>;
    output: Record<string, unknown>;
    reason: { code: string; description: string }[];
    nodes: { node_id: string; type: NodeRecord['type']; output?: unknown }[];
  };
}

function seedDecisions(): Decision[] {
  const profiles: FlowProfile[] = [
    {
      flow_id: 'flow_credit',
      slug: 'credit-decision',
      versions: [2, 3],
      policy_id: 'pol_credit',
      build: (i, disp) => {
        const income = 42000 + (i % 9) * 9000;
        const debt = 8000 + (i % 7) * 4000;
        const risk =
          disp === 'approve' ? 18 + (i % 12) : disp === 'decline' ? 74 + (i % 18) : 48 + (i % 18);
        // FICO anti-correlates with the PD band: prime applicants clear, mid-600s
        // applicants refer, sub-prime decline. Mirrors the bureau score the credit
        // flow now consumes.
        const fico =
          disp === 'approve'
            ? 760 + (i % 5) * 10
            : disp === 'decline'
              ? 585 + (i % 6) * 5
              : 645 + (i % 4) * 8;
        const declined = disp === 'decline';
        const disposable = (income - debt) / 12;
        const offeredLimit = declined ? 0 : Math.round(Math.min(disposable * 4, income * 0.1));
        return {
          data: {
            income,
            debt,
            revolving_balance: 3000 + (i % 6) * 1500,
            credit_limit: 15000,
            delinquencies_24m: i % 4,
            fico_score: fico,
            tenure_years: 2 + (i % 8),
            employment_stability: 0.4 + (i % 5) * 0.12,
            risk
          },
          output: { approved: disp === 'approve', risk, offered_limit: offeredLimit },
          reason:
            disp === 'decline'
              ? [
                  { code: 'DELINQUENCY_HISTORY', description: 'Serious delinquency on file' },
                  { code: 'LOW_SCORE', description: 'Credit score below threshold' }
                ]
              : disp === 'refer'
                ? [
                    { code: 'DTI_TOO_HIGH', description: 'Debt-to-income ratio too high' },
                    { code: 'THIN_FILE', description: 'Insufficient credit history' }
                  ]
                : [{ code: 'BAND_PRIME', description: 'Prime risk band — auto-approve' }],
          nodes: [
            { node_id: 'in', type: 'input', output: { income, fico_score: fico } },
            {
              node_id: 'propensity',
              type: 'predict',
              output: {
                propensity: { score: 0.6 + (i % 4) * 0.08, probability: 0.6 + (i % 4) * 0.08 }
              }
            },
            {
              node_id: 'score',
              type: 'predict',
              output: { pd: { score: risk / 100, probability: risk / 100 } }
            },
            {
              node_id: 'band',
              type: 'split',
              output: {
                branch:
                  disp === 'approve'
                    ? 'risk < 35'
                    : disp === 'decline'
                      ? 'risk >= 70'
                      : 'risk >= 35'
              }
            },
            { node_id: 'out', type: 'output', output: { approved: disp === 'approve' } }
          ]
        };
      }
    },
    {
      flow_id: 'flow_aml',
      slug: 'aml-screening',
      versions: [2, 3],
      policy_id: 'pol_aml',
      build: (i, disp) => {
        const amount = 4000 + (i % 11) * 7000;
        const amlScore = disp === 'refer' ? 7 + (i % 5) : 2 + (i % 3);
        // Only referrals may carry a sanctions hit; a confirmed hit (watchlist >= 80)
        // hard-stops to review and can never clear. Cleared txns stay below the
        // watchlist threshold so the seed stays coherent with the gated split.
        const sanctionsHit = disp === 'refer' && i % 5 === 0;
        const watchlistScore = sanctionsHit ? 85 : 10;
        return {
          data: {
            amount,
            origin_country: 'US',
            dest_country: i % 3 === 0 ? 'KY' : 'US',
            watchlist_score: watchlistScore,
            sanctions_hit: sanctionsHit ? 1 : 0,
            aml_score: amlScore
          },
          output: { cleared: disp !== 'refer', aml_score: amlScore },
          reason:
            disp === 'refer'
              ? sanctionsHit
                ? [{ code: 'SANCTIONS_MATCH', description: 'Confirmed sanctions/watchlist match' }]
                : [{ code: 'AML_HIGH', description: 'AML risk above clearing band' }]
              : [{ code: 'AML_CLEAR', description: 'AML risk below clearing band' }],
          nodes: [
            { node_id: 'in', type: 'input', output: { amount } },
            { node_id: 'score', type: 'predict', output: { aml: { score: amlScore } } },
            {
              node_id: 'band',
              type: 'split',
              output: {
                branch: sanctionsHit
                  ? 'sanctions_hit == 1'
                  : amlScore >= 6
                    ? 'aml_score >= 6'
                    : 'aml_score < 6'
              }
            },
            { node_id: 'out', type: 'output', output: { cleared: disp !== 'refer' } }
          ]
        };
      }
    },
    {
      flow_id: 'flow_fraud',
      slug: 'card-fraud',
      versions: [3, 4],
      policy_id: 'pol_fraud',
      build: (i, disp) => {
        const amount = 60 + (i % 13) * 240;
        const fraudP =
          disp === 'decline' ? 82 + (i % 15) : disp === 'refer' ? 45 + (i % 25) : 8 + (i % 25);
        return {
          data: {
            amount,
            tx_count_1h: i % 9,
            device_score: (i % 10) * 10,
            avg_ticket: 120,
            fraud_p: fraudP
          },
          output: { blocked: disp === 'decline', fraud_p: fraudP },
          reason:
            disp === 'decline'
              ? [{ code: 'FRAUD_BLOCK', description: 'Fraud probability above block threshold' }]
              : disp === 'refer'
                ? [{ code: 'FRAUD_REVIEW', description: 'Routed to fraud analyst' }]
                : [{ code: 'FRAUD_LOW', description: 'Fraud probability below review band' }],
          nodes: [
            { node_id: 'in', type: 'input', output: { amount } },
            {
              node_id: 'score',
              type: 'predict',
              output: { fraud: { score: fraudP / 100, probability: fraudP / 100 } }
            },
            {
              node_id: 'band',
              type: 'split',
              output: {
                branch:
                  fraudP >= 80 ? 'fraud_p >= 80' : fraudP >= 40 ? 'fraud_p >= 40' : 'fraud_p < 40'
              }
            },
            { node_id: 'out', type: 'output', output: { blocked: disp === 'decline' } }
          ]
        };
      }
    },
    {
      flow_id: 'flow_kyc',
      slug: 'kyc-onboarding',
      versions: [2],
      policy_id: 'pol_kyc',
      build: (i, disp) => {
        const conf = disp === 'refer' ? 40 + (i % 18) : 70 + (i % 25);
        return {
          data: { doc_score: conf, pep_match: disp === 'refer' ? 1 : 0, identity_conf: conf },
          output: { verified: disp !== 'refer', identity_conf: conf },
          reason:
            disp === 'refer'
              ? [{ code: 'KYC_EDD', description: 'Enhanced due diligence required' }]
              : [{ code: 'KYC_PASS', description: 'Identity confidence above verify gate' }],
          nodes: [
            { node_id: 'in', type: 'input', output: {} },
            {
              node_id: 'score',
              type: 'predict',
              output: { kyc: { score: 0.5, probability: 0.5 } }
            },
            {
              node_id: 'gate',
              type: 'split',
              output: { branch: conf >= 60 ? 'identity_conf >= 60' : 'identity_conf < 60' }
            },
            { node_id: 'out', type: 'output', output: { verified: disp !== 'refer' } }
          ]
        };
      }
    },
    {
      flow_id: 'flow_dispute',
      slug: 'dispute-triage',
      versions: [1, 2],
      policy_id: 'pol_dispute',
      build: (i, disp) => {
        const amount = 80 + (i % 10) * 130;
        const triage = disp === 'refer' ? 50 + (i % 40) : i % 40;
        return {
          data: { amount, reason_code: i % 3 === 0 ? 'fraud' : 'product', triage },
          output: { outcome: disp === 'refer' ? 'review' : 'refund', triage },
          reason:
            disp === 'refer'
              ? [{ code: 'DISPUTE_REVIEW', description: 'Routed to disputes ops' }]
              : [{ code: 'DISPUTE_AUTO_REFUND', description: 'Below triage band — auto-refund' }],
          nodes: [
            { node_id: 'in', type: 'input', output: { amount } },
            {
              node_id: 'band',
              type: 'split',
              output: { branch: triage >= 50 ? 'triage >= 50' : 'triage < 50' }
            },
            {
              node_id: 'out',
              type: 'output',
              output: { outcome: disp === 'refer' ? 'review' : 'refund' }
            }
          ]
        };
      }
    },
    {
      flow_id: 'flow_merchant',
      slug: 'merchant-onboarding',
      versions: [2],
      policy_id: 'pol_merchant',
      build: (i, disp) => {
        const uw = disp === 'refer' ? 25 + (i % 30) : i % 24;
        return {
          data: {
            monthly_volume: 20000 + (i % 8) * 30000,
            mcc_risk: disp === 'refer' ? 80 : 30,
            international: i % 2,
            uw_score: uw
          },
          output: { boarded: disp !== 'refer', uw_score: uw },
          reason:
            disp === 'refer'
              ? [{ code: 'MCC_RISK', description: 'High-risk MCC underwriting review' }]
              : [{ code: 'UW_PASS', description: 'Underwriting score below review gate' }],
          nodes: [
            { node_id: 'in', type: 'input', output: {} },
            {
              node_id: 'gate',
              type: 'split',
              output: { branch: uw >= 25 ? 'uw_score >= 25' : 'uw_score < 25' }
            },
            { node_id: 'out', type: 'output', output: { boarded: disp !== 'refer' } }
          ]
        };
      }
    }
  ];

  const envCycle: Environment[] = ['production', 'production', 'production', 'staging', 'sandbox'];
  const dispCycle: ('approve' | 'decline' | 'refer')[] = [
    'approve',
    'approve',
    'refer',
    'approve',
    'decline',
    'approve',
    'refer'
  ];
  const out: Decision[] = [];
  // The flows that actually run a champion/challenger split (a challenger_version is
  // configured in their deployments) — only these get challenger decisions, so the
  // Experimentation lens shows a real spread instead of one flow.
  const challengerFlows = new Set(['credit-decision', 'aml-screening', 'card-fraud']);
  // Round-robin across flows so every flow has decisions across envs/time/variants.
  for (let i = 1; i <= 44; i++) {
    const profile = profiles[i % profiles.length];
    const failed = i % 13 === 0;
    const status: 'completed' | 'failed' = failed ? 'failed' : 'completed';
    const disp = failed ? undefined : dispCycle[i % dispCycle.length];
    const env = envCycle[i % envCycle.length];
    // Mark roughly a third of a challenger-flow's own decisions as challenger — keyed on
    // the per-flow occurrence (i/6), NOT i%6, which would pin it to a single flow.
    const variant: 'champion' | 'challenger' =
      challengerFlows.has(profile.slug) && Math.floor(i / 6) % 3 === 0 ? 'challenger' : 'champion';
    const version = profile.versions[i % profile.versions.length];
    const built = profile.build(i, disp);
    const hrs = 1 + i * 3;
    out.push({
      decision_id: `dec_${i}`,
      flow_id: profile.flow_id,
      slug: profile.slug,
      version,
      environment: env,
      variant,
      status,
      data: built.data,
      output: failed ? {} : built.output,
      reason_codes: failed ? [] : built.reason,
      disposition: disp,
      disposition_reason: disp === 'refer' ? 'Routed to manual review' : undefined,
      policy_id: profile.policy_id,
      policy_version: profile.policy_id ? 2 : undefined,
      error: failed ? 'connector timeout: bureau' : undefined,
      nodes: built.nodes,
      started_at: ago(hrs),
      ended_at: ago(hrs - 0.01),
      duration_ms: 24 + (i % 9) * 14
    });
  }

  // A decision PAUSED at a durable human task — the suspend/resume orchestration. It
  // holds no running process: it lives in the event log and resumes when a reviewer
  // records an outcome (the Resume control on the decision page re-runs it to completion).
  out.push({
    decision_id: 'dec_suspended_1',
    flow_id: 'flow_credit',
    slug: 'credit-decision',
    version: 3,
    environment: 'production',
    variant: 'champion',
    status: 'suspended',
    data: {
      income: 58000,
      debt: 26000,
      revolving_balance: 6000,
      credit_limit: 15000,
      delinquencies_24m: 1,
      fico_score: 662,
      tenure_years: 3,
      employment_stability: 0.6,
      risk: 52
    },
    output: {},
    reason_codes: [
      { code: 'DTI_TOO_HIGH', description: 'Debt-to-income ratio too high' },
      { code: 'MANUAL_REVIEW', description: 'Escalated to manual review' }
    ],
    policy_id: 'pol_credit',
    policy_version: 2,
    nodes: [],
    started_at: ago(2)
  });
  return out;
}

interface CaseSeed {
  id: string;
  name: string;
  type: string;
  status: Case['status'];
  assignee?: string;
  slaDays: number;
  daysLeft: number;
  slaState: Case['sla_state'];
  src?: string;
  context: Record<string, unknown>;
  notes: { author: string; text: string; at: string }[];
  audit: { type: string; actor: string; at: string; detail?: string }[];
  createdHrs: number;
  updatedHrs: number;
}

function seedCases(): Case[] {
  const seeds: CaseSeed[] = [
    {
      id: 'case_1',
      name: 'Northwind Capital',
      type: 'credit_review',
      status: 'needs_review',
      slaDays: 3,
      daysLeft: 2,
      slaState: 'on_track',
      src: 'dec_6',
      context: { risk: 58, segment: 'SMB', exposure_usd: 45000, dti: 0.41, fico_score: 662 },
      notes: [
        { author: DIEGO, text: 'Requested two recent pay stubs and bank statements.', at: ago(20) }
      ],
      audit: [
        { type: 'case.opened', actor: 'system', at: ago(48), detail: 'from decision dec_6' },
        { type: 'case.note', actor: DIEGO, at: ago(20) }
      ],
      createdHrs: 48,
      updatedHrs: 20
    },
    {
      id: 'case_2',
      name: 'Acme Imports LLC',
      type: 'aml_alert',
      status: 'in_progress',
      assignee: DIEGO,
      slaDays: 5,
      daysLeft: 1,
      slaState: 'due_soon',
      src: 'dec_37',
      context: { aml_score: 9, amount_usd: 52000, corridor: 'US→KY' },
      notes: [
        {
          author: DIEGO,
          text: 'Cross-border wire to a high-risk jurisdiction; pulling counterparty KYC.',
          at: ago(30)
        },
        { author: MARCUS, text: 'Escalate to SAR drafting if counterparty unverified.', at: ago(6) }
      ],
      audit: [
        { type: 'case.opened', actor: 'system', at: ago(70), detail: 'from decision dec_37' },
        { type: 'case.assigned', actor: AVA, at: ago(64), detail: `to ${DIEGO}` },
        { type: 'case.note', actor: MARCUS, at: ago(6) }
      ],
      createdHrs: 70,
      updatedHrs: 6
    },
    {
      id: 'case_3',
      name: 'Globex Lending',
      type: 'kyc_review',
      status: 'in_progress',
      assignee: DIEGO,
      slaDays: 2,
      daysLeft: -1,
      slaState: 'overdue',
      src: 'dec_9',
      context: { identity_conf: 44, pep_flag: 1 },
      notes: [
        {
          author: DIEGO,
          text: 'PEP match on a beneficial owner; awaiting adverse-media disposition.',
          at: ago(54)
        }
      ],
      audit: [
        { type: 'case.opened', actor: 'system', at: ago(96), detail: 'from decision dec_9' },
        { type: 'case.breached', actor: 'system', at: ago(4), detail: 'SLA exceeded' }
      ],
      createdHrs: 96,
      updatedHrs: 4
    },
    {
      id: 'case_4',
      name: 'Initech Finance',
      type: 'credit_review',
      status: 'completed',
      assignee: DIEGO,
      slaDays: 3,
      daysLeft: 1,
      slaState: 'on_track',
      src: 'dec_30',
      context: { risk: 52, fico_score: 671, dti: 0.38, decision: 'approved with reduced limit' },
      notes: [
        { author: DIEGO, text: 'Approved at $18k limit after income verification.', at: ago(12) }
      ],
      audit: [
        { type: 'case.opened', actor: 'system', at: ago(60), detail: 'from decision dec_30' },
        { type: 'case.resolved', actor: DIEGO, at: ago(12), detail: 'approved' }
      ],
      createdHrs: 60,
      updatedHrs: 12
    },
    {
      id: 'case_5',
      name: 'Umbrella Card 4821',
      type: 'fraud_review',
      status: 'needs_review',
      slaDays: 1,
      daysLeft: 1,
      slaState: 'on_track',
      src: 'dec_2',
      context: { fraud_p: 64, device_risk: 80, amount_usd: 1290 },
      notes: [],
      audit: [{ type: 'case.opened', actor: 'system', at: ago(8), detail: 'from decision dec_2' }],
      createdHrs: 8,
      updatedHrs: 8
    },
    {
      id: 'case_6',
      name: 'Soylent Merchant Co',
      type: 'merchant_review',
      status: 'in_progress',
      assignee: MARCUS,
      slaDays: 4,
      daysLeft: 2,
      slaState: 'on_track',
      src: 'dec_23',
      context: { uw_score: 38, mcc: '7995 (gambling)' },
      notes: [
        {
          author: MARCUS,
          text: 'High-risk MCC; requesting processing history and chargeback ratios.',
          at: ago(18)
        }
      ],
      audit: [
        { type: 'case.opened', actor: 'system', at: ago(40), detail: 'from decision dec_23' },
        { type: 'case.assigned', actor: AVA, at: ago(38), detail: `to ${MARCUS}` }
      ],
      createdHrs: 40,
      updatedHrs: 18
    },
    {
      id: 'case_7',
      name: 'Wayne Disputes #5512',
      type: 'dispute',
      status: 'in_progress',
      assignee: DIEGO,
      slaDays: 7,
      daysLeft: 4,
      slaState: 'on_track',
      src: 'dec_16',
      context: { amount_usd: 740, reason: 'fraud', recommendation: 'representment' },
      notes: [
        {
          author: DIEGO,
          text: 'Compelling evidence on file; preparing representment package.',
          at: ago(26)
        }
      ],
      audit: [
        { type: 'case.opened', actor: 'system', at: ago(36), detail: 'from decision dec_16' }
      ],
      createdHrs: 36,
      updatedHrs: 26
    },
    {
      id: 'case_8',
      name: 'Stark Industries',
      type: 'credit_review',
      status: 'in_progress',
      assignee: DIEGO,
      slaDays: 3,
      daysLeft: 0,
      slaState: 'due_soon',
      src: 'dec_6',
      context: { risk: 61, segment: 'corporate', fico_score: 648, dti: 0.44 },
      notes: [{ author: DIEGO, text: 'Awaiting guarantor financials.', at: ago(14) }],
      audit: [{ type: 'case.opened', actor: 'system', at: ago(50), detail: 'from decision dec_6' }],
      createdHrs: 50,
      updatedHrs: 14
    },
    {
      id: 'case_9',
      name: 'Hooli Payments',
      type: 'aml_alert',
      status: 'completed',
      assignee: DIEGO,
      slaDays: 5,
      daysLeft: 2,
      slaState: 'on_track',
      src: 'dec_37',
      context: { aml_score: 7, outcome: 'no SAR — false positive' },
      notes: [
        {
          author: DIEGO,
          text: 'Structuring pattern explained by payroll batch; cleared.',
          at: ago(90)
        }
      ],
      audit: [
        { type: 'case.opened', actor: 'system', at: ago(140), detail: 'from decision dec_37' },
        { type: 'case.resolved', actor: DIEGO, at: ago(90), detail: 'cleared' }
      ],
      createdHrs: 140,
      updatedHrs: 90
    },
    {
      id: 'case_10',
      name: 'Pied Piper Card 9913',
      type: 'fraud_review',
      status: 'completed',
      assignee: DIEGO,
      slaDays: 1,
      daysLeft: 0,
      slaState: 'on_track',
      src: 'dec_20',
      context: { fraud_p: 88, outcome: 'confirmed fraud — card blocked' },
      notes: [{ author: DIEGO, text: 'Account takeover confirmed; card reissued.', at: ago(110) }],
      audit: [
        { type: 'case.opened', actor: 'system', at: ago(118), detail: 'from decision dec_20' },
        { type: 'case.resolved', actor: DIEGO, at: ago(110), detail: 'blocked' }
      ],
      createdHrs: 118,
      updatedHrs: 110
    },
    {
      id: 'case_11',
      name: 'Cyberdyne Onboarding',
      type: 'kyc_review',
      status: 'needs_review',
      slaDays: 2,
      daysLeft: 2,
      slaState: 'on_track',
      src: 'dec_27',
      context: { identity_conf: 55, doc_quality: 'low' },
      notes: [],
      audit: [
        { type: 'case.opened', actor: 'system', at: ago(10), detail: 'from decision dec_27' }
      ],
      createdHrs: 10,
      updatedHrs: 10
    },
    {
      id: 'case_12',
      name: 'Tyrell Merchant',
      type: 'merchant_review',
      status: 'needs_review',
      slaDays: 4,
      daysLeft: -2,
      slaState: 'overdue',
      src: 'dec_41',
      context: { uw_score: 42, mcc: '6051 (crypto)' },
      notes: [
        {
          author: MARCUS,
          text: 'Crypto MCC requires enhanced underwriting; chasing licensing docs.',
          at: ago(100)
        }
      ],
      audit: [
        { type: 'case.opened', actor: 'system', at: ago(150), detail: 'from decision dec_41' },
        { type: 'case.breached', actor: 'system', at: ago(6), detail: 'SLA exceeded' }
      ],
      createdHrs: 150,
      updatedHrs: 6
    },
    {
      id: 'case_13',
      name: 'Oscorp Disputes #7740',
      type: 'dispute',
      status: 'completed',
      assignee: DIEGO,
      slaDays: 7,
      daysLeft: 3,
      slaState: 'on_track',
      src: 'dec_34',
      context: { amount_usd: 210, outcome: 'refunded' },
      notes: [
        { author: DIEGO, text: 'Low value, product-not-received; auto-refunded.', at: ago(160) }
      ],
      audit: [
        { type: 'case.opened', actor: 'system', at: ago(180), detail: 'from decision dec_34' },
        { type: 'case.resolved', actor: DIEGO, at: ago(160), detail: 'refund' }
      ],
      createdHrs: 180,
      updatedHrs: 160
    },
    {
      id: 'case_14',
      name: 'Aperture Capital',
      type: 'credit_review',
      status: 'needs_review',
      slaDays: 3,
      daysLeft: 3,
      slaState: 'on_track',
      src: 'dec_30',
      context: { risk: 49, segment: 'SMB', fico_score: 668, dti: 0.36 },
      notes: [],
      audit: [{ type: 'case.opened', actor: 'system', at: ago(5), detail: 'from decision dec_30' }],
      createdHrs: 5,
      updatedHrs: 5
    }
  ];
  return seeds.map((s) => ({
    case_id: s.id,
    company_name: s.name,
    case_type: s.type,
    status: s.status,
    assignee: s.assignee,
    sla_days: s.slaDays,
    days_left: s.daysLeft,
    sla_state: s.slaState,
    source_decision_id: s.src,
    context: s.context,
    notes: s.notes,
    audit: s.audit,
    created_at: ago(s.createdHrs),
    updated_at: ago(s.updatedHrs)
  }));
}

function seedAgents(): Agent[] {
  return [
    {
      name: 'aml-narrative',
      provider: 'anthropic',
      model: 'claude-sonnet',
      system: 'You write concise SAR narratives from transaction context.',
      schema: { type: 'object', properties: { narrative: { type: 'string' } } },
      tools: ['lookup_entity', 'sanctions_check'],
      latest: 3,
      runs: 0, // derived from seeded runs in createState
      updated_at: ago(20)
    },
    {
      name: 'kyc-extract',
      provider: 'anthropic',
      model: 'claude-haiku',
      system: 'Extract structured KYC fields from a submitted identity document.',
      schema: {
        type: 'object',
        properties: {
          name: { type: 'string' },
          dob: { type: 'string' },
          doc_number: { type: 'string' }
        }
      },
      tools: [],
      latest: 2,
      runs: 0,
      updated_at: ago(60)
    },
    {
      name: 'dispute-summarizer',
      provider: 'anthropic',
      model: 'claude-haiku',
      system: 'Summarize a cardholder dispute and recommend representment or refund.',
      schema: {
        type: 'object',
        properties: { summary: { type: 'string' }, recommendation: { type: 'string' } }
      },
      tools: ['lookup_transaction'],
      latest: 1,
      runs: 0,
      updated_at: ago(44)
    },
    {
      name: 'fraud-explainer',
      provider: 'anthropic',
      model: 'claude-sonnet',
      system: 'Explain a fraud model score in plain language for an analyst.',
      tools: [],
      latest: 2,
      runs: 0,
      updated_at: ago(15)
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
        // FICO carries the intercept: 700 is the reference score, so the +5.3 base
        // is ~5.3 - 0.012*fico. A negative fico weight (higher score -> lower PD)
        // plus DTI/utilization/delinquency drivers; tuned so a mid-tier applicant
        // (DTI ~0.35, utilization ~0.5, 1 delinquency, mid-600s FICO) lands ~0.44
        // probability and routes to manual review rather than auto-approving/declining.
        intercept: 5.3,
        coefficients: { dti: 3.0, utilization: 1.2, delinquencies: 0.7, fico_score: -0.012 }
      },
      owner: AVA,
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
            left: { leaf: true, value: -0.6 },
            right: {
              feature: 'device_risk',
              threshold: 60,
              left: { leaf: true, value: 0.8 },
              right: { leaf: true, value: 1.9 }
            }
          },
          {
            feature: 'amount_ratio',
            threshold: 3,
            left: { leaf: true, value: -0.2 },
            right: { leaf: true, value: 1.1 }
          }
        ]
      },
      owner: MARCUS,
      updated_at: ago(50)
    },
    {
      name: 'aml_risk',
      kind: 'expression',
      spec: { kind: 'expression', expr: 'amount / 10000 + cross_border * 2 + high_value' },
      owner: MARCUS,
      updated_at: ago(120)
    },
    {
      name: 'kyc_score',
      kind: 'external',
      spec: { kind: 'external', endpoint: 'kyc-vendor:/score', timeout_ms: 800 },
      owner: PRIYA,
      updated_at: ago(70)
    },
    {
      name: 'repayment_propensity',
      kind: 'logistic',
      spec: {
        kind: 'logistic',
        // Logistic classifier of on-time repayment propensity from employment
        // signals; an auxiliary score the credit flow enriches alongside PD.
        // Renamed from the mis-named "income_estimator" so the name matches a
        // logistic classifier. No baseline captured (MRM monitoring gap, intact).
        intercept: -1.1,
        coefficients: { tenure_years: 0.4, employment_stability: 0.9 }
      },
      owner: AVA,
      updated_at: ago(310)
    }
  ];
}

function seedPolicies(): Policy[] {
  return [
    {
      policy_id: 'pol_credit',
      name: 'Credit Disposition',
      flow_slug: 'credit-decision',
      latest: 2,
      updated_at: ago(38),
      versions: [
        {
          version: 1,
          etag: 'petag-c1',
          published_at: ago(160),
          published_by: AVA,
          spec: {
            rules: [
              {
                when: 'risk < 30',
                disposition: 'approve',
                code: 'APPROVED',
                description: 'Meets approval criteria'
              },
              {
                when: 'fico_score < 620',
                disposition: 'decline',
                code: 'LOW_SCORE',
                description: 'Credit score below threshold'
              },
              {
                when: 'risk >= 70',
                disposition: 'decline',
                code: 'DELINQUENCY_HISTORY',
                description: 'Serious delinquency on file'
              },
              {
                when: 'risk >= 30',
                disposition: 'refer',
                code: 'DTI_TOO_HIGH',
                description: 'Debt-to-income ratio too high'
              }
            ],
            default: 'refer'
          }
        },
        {
          version: 2,
          etag: 'petag-c2',
          published_at: ago(38),
          published_by: MARCUS,
          spec: {
            // FCRA / Reg B permissible adverse-action reason codes (no generic
            // risk-band labels): the matched rule's code becomes the decision's
            // adverse-action reason and feeds the adverse-action narrative node.
            rules: [
              {
                when: 'risk < 35',
                disposition: 'approve',
                code: 'APPROVED',
                description: 'Meets approval criteria'
              },
              {
                when: 'fico_score < 620',
                disposition: 'decline',
                code: 'LOW_SCORE',
                description: 'Credit score below threshold'
              },
              {
                when: 'risk >= 70',
                disposition: 'decline',
                code: 'DELINQUENCY_HISTORY',
                description: 'Serious delinquency on file'
              },
              {
                when: 'risk >= 35',
                disposition: 'refer',
                code: 'DTI_TOO_HIGH',
                description: 'Debt-to-income ratio too high'
              }
            ],
            default: 'refer'
          }
        }
      ]
    },
    {
      policy_id: 'pol_aml',
      name: 'AML Clearing Policy',
      flow_slug: 'aml-screening',
      latest: 1,
      updated_at: ago(90),
      versions: [
        {
          version: 1,
          etag: 'petag-a1',
          published_at: ago(90),
          published_by: MARCUS,
          spec: {
            rules: [
              {
                when: 'aml_score >= 6',
                disposition: 'refer',
                code: 'AML_HIGH',
                description: 'Refer high AML risk to analyst'
              },
              {
                when: 'aml_score < 6',
                disposition: 'approve',
                code: 'AML_CLEAR',
                description: 'Clear low AML risk'
              }
            ],
            default: 'refer'
          }
        }
      ]
    },
    {
      policy_id: 'pol_fraud',
      name: 'Card Fraud Policy',
      flow_slug: 'card-fraud',
      latest: 1,
      updated_at: ago(65),
      versions: [
        {
          version: 1,
          etag: 'petag-f1',
          published_at: ago(65),
          published_by: MARCUS,
          spec: {
            rules: [
              {
                when: 'fraud_p >= 80',
                disposition: 'decline',
                code: 'FRAUD_BLOCK',
                description: 'Block high fraud probability'
              },
              {
                when: 'fraud_p >= 40',
                disposition: 'refer',
                code: 'FRAUD_REVIEW',
                description: 'Refer to fraud analyst'
              },
              {
                when: 'fraud_p < 40',
                disposition: 'approve',
                code: 'FRAUD_PASS',
                description: 'Allow low fraud probability'
              }
            ],
            default: 'refer'
          }
        }
      ]
    },
    {
      policy_id: 'pol_kyc',
      name: 'KYC Onboarding Policy',
      flow_slug: 'kyc-onboarding',
      latest: 1,
      updated_at: ago(80 * 24),
      versions: [
        {
          version: 1,
          etag: 'petag-k1',
          published_at: ago(80 * 24),
          published_by: MARCUS,
          spec: {
            rules: [
              {
                when: 'identity_conf < 60',
                disposition: 'refer',
                code: 'KYC_LOW_CONF',
                description: 'Identity confidence below threshold'
              },
              {
                when: 'identity_conf >= 60',
                disposition: 'approve',
                code: 'KYC_CLEAR',
                description: 'Identity verified'
              }
            ],
            default: 'refer'
          }
        }
      ]
    },
    {
      policy_id: 'pol_dispute',
      name: 'Chargeback Triage Policy',
      flow_slug: 'dispute-triage',
      latest: 1,
      updated_at: ago(60 * 24),
      versions: [
        {
          version: 1,
          etag: 'petag-d1',
          published_at: ago(60 * 24),
          published_by: MARCUS,
          spec: {
            rules: [
              {
                when: 'triage >= 50',
                disposition: 'refer',
                code: 'DISPUTE_REVIEW',
                description: 'High triage score — route to disputes ops'
              },
              {
                when: 'triage < 50',
                disposition: 'approve',
                code: 'DISPUTE_REFUND',
                description: 'Below triage band — auto-refund'
              }
            ],
            default: 'refer'
          }
        }
      ]
    },
    {
      policy_id: 'pol_merchant',
      name: 'Merchant Onboarding Policy',
      flow_slug: 'merchant-onboarding',
      latest: 1,
      updated_at: ago(70 * 24),
      versions: [
        {
          version: 1,
          etag: 'petag-m1',
          published_at: ago(70 * 24),
          published_by: MARCUS,
          spec: {
            rules: [
              {
                when: 'uw_score >= 25',
                disposition: 'refer',
                code: 'UW_HIGH',
                description: 'Underwriting score above the review gate'
              },
              {
                when: 'uw_score < 25',
                disposition: 'approve',
                code: 'UW_PASS',
                description: 'Underwriting score below the review gate'
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
      policy_version: 2,
      flow_slug: 'credit-decision',
      valid_until: ahead(20),
      status: 'active',
      honored_count: 3,
      note: 'Pre-approved gold-tier applicant',
      granted_at: ago(120),
      granted_by: AVA,
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
      granted_by: MARCUS,
      updated_at: ago(10)
    },
    {
      preapproval_id: 'pa_3',
      entity_type: 'applicant',
      entity_id: 'APP-1007',
      disposition: 'approve',
      terms: { limit_usd: 40000, apr: 9.9 },
      policy_id: 'pol_credit',
      policy_version: 2,
      flow_slug: 'credit-decision',
      valid_until: ahead(2),
      status: 'active',
      honored_count: 6,
      note: 'Platinum relationship — expiring soon, renewal queued',
      granted_at: ago(330),
      granted_by: MARCUS,
      updated_at: ago(48)
    },
    {
      preapproval_id: 'pa_4',
      entity_type: 'merchant',
      entity_id: 'MER-4400',
      disposition: 'approve',
      terms: { mdr_bps: 240, monthly_cap_usd: 500000 },
      flow_slug: 'merchant-onboarding',
      valid_until: ahead(60),
      status: 'active',
      honored_count: 1,
      note: 'Established low-risk retail merchant',
      granted_at: ago(80),
      granted_by: MARCUS,
      updated_at: ago(80)
    },
    {
      preapproval_id: 'pa_5',
      entity_type: 'applicant',
      entity_id: 'APP-1011',
      disposition: 'approve',
      terms: { limit_usd: 12000, apr: 15.0 },
      policy_id: 'pol_credit',
      policy_version: 2,
      flow_slug: 'credit-decision',
      valid_until: ahead(1),
      status: 'active',
      honored_count: 0,
      note: 'Promo offer — expires within 24h',
      granted_at: ago(160),
      granted_by: AVA,
      updated_at: ago(160)
    },
    {
      preapproval_id: 'pa_6',
      entity_type: 'transaction',
      entity_id: 'TXN-9920',
      disposition: 'approve',
      flow_slug: 'aml-screening',
      valid_until: ahead(30),
      status: 'active',
      honored_count: 12,
      note: 'Whitelisted recurring payroll corridor',
      granted_at: ago(240),
      granted_by: MARCUS,
      updated_at: ago(72)
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
    { name: 'core-banking', type: 'postgres', config: { dsn: 'redacted' }, updated_at: ago(160) },
    {
      name: 'ofac-sanctions',
      type: 'http',
      config: { base_url: 'https://api.sanctions.demo' },
      updated_at: ago(50)
    },
    {
      name: 'device-intel',
      type: 'http',
      config: { base_url: 'https://api.deviceintel.demo' },
      updated_at: ago(36)
    },
    {
      name: 'jumio-kyc',
      type: 'http',
      config: { base_url: 'https://api.jumio.demo' },
      updated_at: ago(72)
    }
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
    },
    {
      id: 'device-intel',
      name: 'Device Intelligence',
      category: 'Fraud',
      type: 'http',
      description: 'Device fingerprint and risk score for an authorization.',
      config: { base_url: 'https://api.deviceintel.demo' }
    },
    {
      id: 'jumio-kyc',
      name: 'Jumio Identity',
      category: 'Identity',
      type: 'http',
      description: 'Document verification and liveness for KYC onboarding.',
      config: { base_url: 'https://api.jumio.demo' }
    },
    {
      id: 'core-banking',
      name: 'Core Banking (Postgres)',
      category: 'Data',
      type: 'postgres',
      description: 'Read account balances and transaction history.',
      config: { dsn: 'postgres://core-banking' }
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
    },
    {
      name: 'tx_count_1h',
      entity_type: 'customer',
      event_name: 'authorization',
      aggregation: 'count',
      window_hours: 1,
      updated_at: ago(36)
    },
    {
      name: 'auth_sum_24h',
      entity_type: 'customer',
      event_name: 'authorization',
      aggregation: 'sum',
      field: 'amount',
      window_hours: 24,
      updated_at: ago(36)
    },
    {
      name: 'wire_count_7d',
      entity_type: 'transaction',
      event_name: 'wire',
      aggregation: 'count',
      window_hours: 168,
      updated_at: ago(48)
    },
    {
      name: 'wire_sum_30d',
      entity_type: 'transaction',
      event_name: 'wire',
      aggregation: 'sum',
      field: 'amount',
      window_hours: 720,
      updated_at: ago(48)
    },
    {
      name: 'chargeback_count_90d',
      entity_type: 'merchant',
      event_name: 'chargeback',
      aggregation: 'count',
      window_hours: 2160,
      updated_at: ago(90)
    },
    {
      name: 'settlement_sum_30d',
      entity_type: 'merchant',
      event_name: 'settlement',
      aggregation: 'sum',
      field: 'amount',
      window_hours: 720,
      updated_at: ago(90)
    },
    {
      name: 'login_count_24h',
      entity_type: 'customer',
      event_name: 'login',
      aggregation: 'count',
      window_hours: 24,
      updated_at: ago(20)
    },
    {
      name: 'dispute_count_180d',
      entity_type: 'customer',
      event_name: 'dispute',
      aggregation: 'count',
      window_hours: 4320,
      updated_at: ago(70)
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
      event_count: 2,
      first_seen: ago(200),
      updated_at: ago(48)
    },
    {
      entity_type: 'applicant',
      entity_id: 'APP-1007',
      attributes: { name: 'Mei Lin', segment: 'platinum', country: 'SG' },
      event_count: 4,
      first_seen: ago(330),
      updated_at: ago(48)
    },
    {
      entity_type: 'applicant',
      entity_id: 'APP-1011',
      attributes: { name: 'Carlos Reyes', segment: 'standard', country: 'MX' },
      event_count: 1,
      first_seen: ago(160),
      updated_at: ago(160)
    },
    {
      entity_type: 'transaction',
      entity_id: 'TXN-9920',
      attributes: { corridor: 'US→US', type: 'payroll', recurring: true },
      event_count: 3,
      first_seen: ago(240),
      updated_at: ago(72)
    },
    {
      entity_type: 'transaction',
      entity_id: 'TXN-9931',
      attributes: { corridor: 'US→KY', type: 'wire', recurring: false },
      event_count: 2,
      first_seen: ago(120),
      updated_at: ago(30)
    },
    {
      entity_type: 'merchant',
      entity_id: 'MER-4400',
      attributes: { name: 'Soylent Retail', mcc: '5411', risk: 'low' },
      event_count: 3,
      first_seen: ago(300),
      updated_at: ago(48)
    },
    {
      entity_type: 'merchant',
      entity_id: 'MER-4471',
      attributes: { name: 'Tyrell Digital', mcc: '6051', risk: 'high' },
      event_count: 2,
      first_seen: ago(150),
      updated_at: ago(40)
    },
    {
      entity_type: 'customer',
      entity_id: 'CUST-7781',
      attributes: { name: 'Ada Stark', tenure_years: 6, card_present: true },
      event_count: 4,
      first_seen: ago(500),
      updated_at: ago(8)
    },
    {
      entity_type: 'customer',
      entity_id: 'CUST-7790',
      attributes: { name: 'Bruce Pied', tenure_years: 1, card_present: false },
      event_count: 3,
      first_seen: ago(118),
      updated_at: ago(110)
    }
  ];
}

function seedEntityEvents(): Map<string, EntityEvent[]> {
  const m = new Map<string, EntityEvent[]>();
  const ev = (
    type: string,
    id: string,
    name: string,
    data: Record<string, unknown>,
    seq: number,
    hrs: number
  ): EntityEvent => ({
    entity_type: type,
    entity_id: id,
    event_name: name,
    data,
    seq,
    occurred_at: ago(hrs),
    recorded_at: ago(hrs)
  });
  m.set('applicant/APP-1001', [
    ev('applicant', 'APP-1001', 'transaction', { amount: 1200 }, 1, 300),
    ev('applicant', 'APP-1001', 'transaction', { amount: 4500 }, 2, 100),
    ev('applicant', 'APP-1001', 'login', {}, 3, 12)
  ]);
  m.set('applicant/APP-1002', [
    ev('applicant', 'APP-1002', 'transaction', { amount: 50000 }, 1, 200),
    ev('applicant', 'APP-1002', 'login', {}, 2, 48)
  ]);
  m.set('applicant/APP-1007', [
    ev('applicant', 'APP-1007', 'transaction', { amount: 9800 }, 1, 330),
    ev('applicant', 'APP-1007', 'transaction', { amount: 15000 }, 2, 150),
    ev('applicant', 'APP-1007', 'transaction', { amount: 6200 }, 3, 90),
    ev('applicant', 'APP-1007', 'login', {}, 4, 48)
  ]);
  m.set('applicant/APP-1011', [
    ev('applicant', 'APP-1011', 'transaction', { amount: 2300 }, 1, 160)
  ]);
  m.set('transaction/TXN-9920', [
    ev('transaction', 'TXN-9920', 'wire', { amount: 32000, dest: 'US' }, 1, 240),
    ev('transaction', 'TXN-9920', 'wire', { amount: 32000, dest: 'US' }, 2, 168),
    ev('transaction', 'TXN-9920', 'wire', { amount: 32000, dest: 'US' }, 3, 72)
  ]);
  m.set('transaction/TXN-9931', [
    ev('transaction', 'TXN-9931', 'wire', { amount: 48000, dest: 'KY' }, 1, 120),
    ev('transaction', 'TXN-9931', 'wire', { amount: 51000, dest: 'KY' }, 2, 30)
  ]);
  m.set('merchant/MER-4400', [
    ev('merchant', 'MER-4400', 'settlement', { amount: 220000 }, 1, 300),
    ev('merchant', 'MER-4400', 'settlement', { amount: 245000 }, 2, 120),
    ev('merchant', 'MER-4400', 'chargeback', { amount: 740 }, 3, 48)
  ]);
  m.set('merchant/MER-4471', [
    ev('merchant', 'MER-4471', 'settlement', { amount: 90000 }, 1, 150),
    ev('merchant', 'MER-4471', 'chargeback', { amount: 1200 }, 2, 40)
  ]);
  m.set('customer/CUST-7781', [
    ev('customer', 'CUST-7781', 'authorization', { amount: 120 }, 1, 500),
    ev('customer', 'CUST-7781', 'authorization', { amount: 95 }, 2, 200),
    ev('customer', 'CUST-7781', 'authorization', { amount: 1290 }, 3, 8),
    ev('customer', 'CUST-7781', 'login', {}, 4, 8)
  ]);
  m.set('customer/CUST-7790', [
    ev('customer', 'CUST-7790', 'authorization', { amount: 60 }, 1, 118),
    ev('customer', 'CUST-7790', 'authorization', { amount: 2400 }, 2, 112),
    ev('customer', 'CUST-7790', 'dispute', { amount: 2400 }, 3, 110)
  ]);
  return m;
}

function seedMonitors(): Map<string, Monitor[]> {
  const m = new Map<string, Monitor[]>();
  m.set('flow_credit', [
    {
      monitor_id: 'mon_c1',
      flow_id: 'flow_credit',
      metric: 'failure_rate',
      op: 'gt',
      threshold: 0.05,
      description: 'Decision failure rate',
      status: { actual: 0.09, computable: true, firing: true }
    },
    {
      monitor_id: 'mon_c2',
      flow_id: 'flow_credit',
      metric: 'refer_rate',
      op: 'gt',
      threshold: 0.4,
      description: 'Manual-review referral rate',
      status: { actual: 0.21, computable: true, firing: false }
    },
    {
      monitor_id: 'mon_c3',
      flow_id: 'flow_credit',
      metric: 'distribution_drift_psi',
      op: 'gt',
      threshold: 0.2,
      description: 'Disposition drift (PSI)',
      status: { actual: 0.12, computable: true, firing: false }
    }
  ]);
  m.set('flow_aml', [
    {
      monitor_id: 'mon_a1',
      flow_id: 'flow_aml',
      metric: 'volume',
      op: 'lt',
      threshold: 5,
      description: 'Screening throughput floor',
      status: { actual: 8, computable: true, firing: false }
    },
    {
      monitor_id: 'mon_a2',
      flow_id: 'flow_aml',
      metric: 'refer_rate',
      op: 'gt',
      threshold: 0.3,
      description: 'SAR referral rate',
      status: { actual: 0.38, computable: true, firing: true }
    }
  ]);
  m.set('flow_fraud', [
    {
      monitor_id: 'mon_f1',
      flow_id: 'flow_fraud',
      metric: 'decline_rate',
      op: 'gt',
      threshold: 0.15,
      description: 'Block rate',
      status: { actual: 0.11, computable: true, firing: false }
    },
    {
      monitor_id: 'mon_f2',
      flow_id: 'flow_fraud',
      metric: 'avg_latency_ms',
      op: 'gt',
      threshold: 120,
      description: 'p50 scoring latency',
      status: { actual: 86, computable: true, firing: false }
    }
  ]);
  m.set('flow_dispute', [
    {
      monitor_id: 'mon_d1',
      flow_id: 'flow_dispute',
      metric: 'automation_rate',
      op: 'lt',
      threshold: 0.5,
      description: 'Auto-refund automation rate',
      status: { actual: 0.43, computable: true, firing: true }
    }
  ]);
  return m;
}

function seedAssertions(): Map<string, AssertionCase[]> {
  const m = new Map<string, AssertionCase[]>();
  m.set('flow_credit', [
    {
      name: 'prime applicant approves',
      input: {
        income: 120000,
        debt: 4000,
        revolving_balance: 1000,
        credit_limit: 20000,
        delinquencies_24m: 0,
        fico_score: 800
      },
      expect: { approved: true }
    },
    {
      name: 'sub-prime high dti declines',
      input: {
        income: 30000,
        debt: 26000,
        revolving_balance: 14000,
        credit_limit: 15000,
        delinquencies_24m: 3,
        fico_score: 600
      },
      expect: { approved: false }
    },
    {
      // Mid-tier applicant (DTI ~0.47, mid-600s FICO, 1 delinquency) lands in the
      // review band (risk ~53), opens a case, and gets a conservative affordability
      // -aware line (min(4 mo disposable income, 10% of annual) = $6,000) — never
      // auto-approved or auto-declined.
      name: 'mid band refers to underwriter',
      input: {
        income: 60000,
        debt: 28000,
        revolving_balance: 8000,
        credit_limit: 15000,
        delinquencies_24m: 1,
        fico_score: 660
      },
      expect: { offered_limit: 6000 }
    }
  ]);
  m.set('flow_aml', [
    {
      name: 'small domestic clears',
      input: { amount: 2000, origin_country: 'US', dest_country: 'US', watchlist_score: 5 },
      expect: { cleared: true }
    },
    {
      name: 'large cross-border refers',
      input: { amount: 60000, origin_country: 'US', dest_country: 'KY', watchlist_score: 10 },
      expect: { cleared: false }
    },
    {
      // A confirmed sanctions/watchlist hit hard-stops: even a small domestic wire
      // that would otherwise clear can never auto-clear once watchlist_score >= 80.
      name: 'sanctions hit cannot clear',
      input: { amount: 1000, origin_country: 'US', dest_country: 'US', watchlist_score: 90 },
      expect: { cleared: false }
    }
  ]);
  m.set('flow_fraud', [
    {
      name: 'low velocity allows',
      input: { amount: 80, tx_count_1h: 1, device_score: 10, avg_ticket: 120 },
      expect: { blocked: false }
    },
    {
      name: 'high velocity blocks',
      input: { amount: 1500, tx_count_1h: 9, device_score: 95, avg_ticket: 120 },
      expect: { blocked: true }
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
    },
    {
      webhook_id: 'wh_2',
      url: 'https://pager.demo/aml-oncall',
      note: 'AML on-call pager',
      events: ['monitor.fired', 'case.breached'],
      active: true,
      delivery_count: 7,
      last_status: 500,
      last_ok: false,
      last_error: 'upstream 500',
      last_delivery_at: ago(3),
      created_at: ago(200)
    }
  ];
}

function seedNotifications(): Notification[] {
  return [
    // Human-task reminders: a flow escalates to a case, and the assignee is pulled to it
    // (assigned -> due-soon -> overdue) without anything auto-resolving the human step.
    {
      notification_id: 'ntf_task_assigned',
      recipient: ACTOR,
      kind: 'task',
      subject_type: 'case',
      subject_id: 'case_1',
      snippet: 'Review task assigned to you: credit_review',
      author: 'system',
      read: false,
      created_at: ago(2)
    },
    {
      notification_id: 'ntf_task_due',
      recipient: ACTOR,
      kind: 'task',
      subject_type: 'case',
      subject_id: 'case_2',
      snippet: 'Review task due soon: aml_alert',
      author: 'sla-sweeper',
      read: false,
      created_at: ago(3)
    },
    {
      notification_id: 'ntf_task_overdue',
      recipient: ACTOR,
      kind: 'task',
      subject_type: 'case',
      subject_id: 'case_3',
      snippet: 'Review task OVERDUE: kyc_review',
      author: 'sla-sweeper',
      read: false,
      created_at: ago(4)
    },
    {
      notification_id: 'ntf_1',
      recipient: ACTOR,
      kind: 'mention',
      subject_type: 'case',
      subject_id: 'case_2',
      snippet: '@Ava escalate to SAR drafting if counterparty unverified',
      author: MARCUS,
      read: false,
      created_at: ago(6)
    },
    {
      notification_id: 'ntf_2',
      recipient: ACTOR,
      kind: 'deployment',
      subject_type: 'flow',
      subject_id: 'flow_credit',
      snippet: 'Deployment request pending your approval (credit v3 → production)',
      author: PRIYA,
      read: false,
      created_at: ago(12)
    },
    {
      notification_id: 'ntf_3',
      recipient: ACTOR,
      kind: 'deployment',
      subject_type: 'flow',
      subject_id: 'flow_aml',
      snippet: 'AML v3 → production awaiting four-eyes approval',
      author: DIEGO,
      read: false,
      created_at: ago(8)
    },
    {
      notification_id: 'ntf_4',
      recipient: ACTOR,
      kind: 'monitor',
      subject_type: 'flow',
      subject_id: 'flow_aml',
      snippet: 'Monitor firing: SAR referral rate above 30%',
      author: 'system',
      read: false,
      created_at: ago(3)
    },
    {
      notification_id: 'ntf_5',
      recipient: ACTOR,
      kind: 'monitor',
      subject_type: 'flow',
      subject_id: 'flow_credit',
      snippet: 'Monitor firing: decision failure rate above 5%',
      author: 'system',
      read: true,
      created_at: ago(5)
    },
    {
      notification_id: 'ntf_6',
      recipient: ACTOR,
      kind: 'sla',
      subject_type: 'case',
      subject_id: 'case_3',
      snippet: 'KYC review breached its SLA',
      author: 'system',
      read: false,
      created_at: ago(4)
    },
    {
      notification_id: 'ntf_7',
      recipient: ACTOR,
      kind: 'comment',
      subject_type: 'decision',
      subject_id: 'dec_2',
      snippet: 'Lena left a compliance note on this decision',
      author: LENA,
      read: true,
      created_at: ago(18)
    }
  ];
}

function seedAudit(decisions: Decision[]): AuditEntry[] {
  // A believable workspace timeline across the roster and a few weeks, on the real
  // event taxonomy (stream → type) with resource ids in the payload — exactly how a
  // real deployment's log reads, so the Audit UI's filters work the same way.
  const tmpl: { actor: string; stream: string; type: string; payload: Record<string, unknown> }[] =
    [
      {
        actor: AVA,
        stream: 'auth',
        type: 'auth.managed_key.created',
        payload: { key_id: 'key_1', name: 'Production server' }
      },
      {
        actor: PRIYA,
        stream: 'decision.flows',
        type: 'decision.flow.version_published',
        payload: { flow_id: 'flow_credit', version: 3 }
      },
      {
        actor: PRIYA,
        stream: 'decision.flows',
        type: 'decision.flow.deployment_requested',
        payload: { flow_id: 'flow_credit', environment: 'production', version: 3 }
      },
      {
        actor: MARCUS,
        stream: 'decision.flows',
        type: 'decision.flow.deployment_approved',
        payload: { flow_id: 'flow_credit', environment: 'production', version: 3 }
      },
      {
        actor: AVA,
        stream: 'decision.flows',
        type: 'decision.flow.version_deployed',
        payload: { flow_id: 'flow_credit', environment: 'staging', version: 3 }
      },
      {
        actor: MARCUS,
        stream: 'decision.policies',
        type: 'decision.policy.version_published',
        payload: { policy_id: 'pol_credit', version: 2 }
      },
      {
        actor: PRIYA,
        stream: 'decision.flows',
        type: 'decision.flow.version_published',
        payload: { flow_id: 'flow_aml', version: 3 }
      },
      {
        actor: DIEGO,
        stream: 'decision.flows',
        type: 'decision.flow.deployment_requested',
        payload: { flow_id: 'flow_aml', environment: 'production', version: 3 }
      },
      {
        actor: 'system',
        stream: 'decision.monitors',
        type: 'decision.monitor_alerted',
        payload: { monitor_id: 'mon_a2', flow_id: 'flow_aml', metric: 'refer_rate' }
      },
      { actor: DIEGO, stream: 'cases', type: 'cases.note_added', payload: { case_id: 'case_2' } },
      {
        actor: AVA,
        stream: 'cases',
        type: 'cases.assigned',
        payload: { case_id: 'case_2', assignee: DIEGO }
      },
      {
        actor: 'system',
        stream: 'cases',
        type: 'cases.sla_breached',
        payload: { case_id: 'case_3' }
      },
      {
        actor: PRIYA,
        stream: 'decision.flows',
        type: 'decision.flow.version_published',
        payload: { flow_id: 'flow_fraud', version: 4 }
      },
      {
        actor: AVA,
        stream: 'decision.flows',
        type: 'decision.flow.shadow_set',
        payload: { flow_id: 'flow_fraud', environment: 'staging', version: 4 }
      },
      {
        actor: MARCUS,
        stream: 'decision.models',
        type: 'decision.model.baseline_captured',
        payload: { model: 'credit_pd' }
      },
      {
        actor: AVA,
        stream: 'decision.models',
        type: 'decision.model.monitor_set',
        payload: { model: 'fraud_score', threshold: 0.25 }
      },
      {
        actor: 'system',
        stream: 'decision.monitors',
        type: 'decision.monitor_alerted',
        payload: { monitor_id: 'mon_c1', flow_id: 'flow_credit', metric: 'failure_rate' }
      },
      {
        actor: DIEGO,
        stream: 'decision.flows',
        type: 'decision.flow.version_published',
        payload: { flow_id: 'flow_dispute', version: 2 }
      },
      {
        actor: AVA,
        stream: 'auth',
        type: 'auth.managed_key.rotated',
        payload: { key_id: 'key_3', name: 'Analytics read-only' }
      },
      {
        actor: AVA,
        stream: 'auth',
        type: 'auth.managed_key.revoked',
        payload: { key_id: 'key_4', name: 'Decommissioned partner' }
      }
    ];
  const events: {
    time: string;
    actor: string;
    stream: string;
    type: string;
    payload: unknown;
  }[] = [];
  const total = 60;
  for (let i = 0; i < total; i++) {
    events.push({ time: ago(i * 4 + (i % 3)), ...tmpl[i % tmpl.length] });
  }
  // Mirror the run journal (started / node_evaluated per step / terminal) for the most
  // recent seeded decisions, so the raw log opens dominated by node steps — the real
  // event-log noise the audit page's "Hide node steps" toggle exists for.
  const recent = [...decisions]
    .sort((a, b) => b.started_at.localeCompare(a.started_at))
    .slice(0, 5);
  for (const d of recent) {
    const actor =
      d.environment === 'production' ? 'svc-prod@intraktible.dev' : 'svc-ci@intraktible.dev';
    events.push({
      time: d.started_at,
      actor,
      stream: 'decision.runs',
      type: 'decision.run.started',
      payload: { decision_id: d.decision_id, flow_id: d.flow_id, environment: d.environment }
    });
    for (const n of d.nodes ?? []) {
      events.push({
        time: d.started_at,
        actor,
        stream: 'decision.runs',
        type: 'decision.run.node_evaluated',
        payload: { decision_id: d.decision_id, node_id: n.node_id, node_type: n.type }
      });
    }
    if (d.case_id) {
      events.push({
        time: d.started_at,
        actor: 'system',
        stream: 'decision.runs',
        type: 'decision.manual_review_requested',
        payload: { decision_id: d.decision_id, case_id: d.case_id }
      });
    }
    const ended = d.ended_at ?? d.started_at;
    if (d.status === 'completed') {
      events.push({
        time: ended,
        actor,
        stream: 'decision.runs',
        type: 'decision.run.completed',
        payload: { decision_id: d.decision_id, disposition: d.disposition }
      });
    } else if (d.status === 'failed') {
      events.push({
        time: ended,
        actor,
        stream: 'decision.runs',
        type: 'decision.run.failed',
        payload: { decision_id: d.decision_id, error: d.error }
      });
    } else {
      events.push({
        time: ended,
        actor,
        stream: 'decision.runs',
        type: 'decision.run.suspended',
        payload: { decision_id: d.decision_id }
      });
    }
  }
  // Chronological seq numbering, stored newest-first like the live log.
  events.sort((a, b) => a.time.localeCompare(b.time));
  return events.map((e, i) => ({ seq: i + 1, id: `aud_${i + 1}`, ...e })).reverse();
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
    },
    {
      id: 'key_3',
      name: 'Analytics read-only',
      identity: { org: 'demo', workspace: 'main', actor: 'svc-bi@intraktible.dev' },
      scope: '*',
      role: 'viewer',
      created_at: ago(700),
      rotated_at: ago(120)
    },
    {
      id: 'key_4',
      name: 'Decommissioned partner',
      identity: { org: 'demo', workspace: 'main', actor: 'svc-partner@intraktible.dev' },
      scope: 'production',
      role: 'operator',
      created_at: ago(3000),
      revoked_at: ago(300)
    }
  ];
}

function seedAgentVersions(): Map<string, AgentVersion[]> {
  const m = new Map<string, AgentVersion[]>();
  m.set('aml-narrative', [
    {
      version: 3,
      etag: 'av3',
      provider: 'anthropic',
      model: 'claude-sonnet',
      system:
        'You write concise SAR narratives from transaction context, citing the triggering typology.',
      published_at: ago(20),
      published_by: PRIYA
    },
    {
      version: 2,
      etag: 'av2',
      provider: 'anthropic',
      model: 'claude-sonnet',
      system: 'You write concise SAR narratives from transaction context.',
      published_at: ago(120),
      published_by: PRIYA
    },
    {
      version: 1,
      etag: 'av1',
      provider: 'anthropic',
      model: 'claude-haiku',
      system: 'Draft a SAR narrative.',
      published_at: ago(300),
      published_by: AVA
    }
  ]);
  m.set('kyc-extract', [
    {
      version: 2,
      etag: 'kv2',
      provider: 'anthropic',
      model: 'claude-haiku',
      system: 'Extract structured KYC fields from a submitted identity document.',
      published_at: ago(60),
      published_by: PRIYA
    },
    {
      version: 1,
      etag: 'kv1',
      provider: 'anthropic',
      model: 'claude-haiku',
      system: 'Extract KYC fields.',
      published_at: ago(220),
      published_by: PRIYA
    }
  ]);
  m.set('dispute-summarizer', [
    {
      version: 1,
      etag: 'dv1',
      provider: 'anthropic',
      model: 'claude-haiku',
      system: 'Summarize a cardholder dispute and recommend representment or refund.',
      published_at: ago(44),
      published_by: DIEGO
    }
  ]);
  m.set('fraud-explainer', [
    {
      version: 2,
      etag: 'fv2',
      provider: 'anthropic',
      model: 'claude-sonnet',
      system: 'Explain a fraud model score in plain language for an analyst.',
      published_at: ago(15),
      published_by: PRIYA
    },
    {
      version: 1,
      etag: 'fv1',
      provider: 'anthropic',
      model: 'claude-haiku',
      system: 'Explain a fraud score.',
      published_at: ago(130),
      published_by: PRIYA
    }
  ]);
  return m;
}

function seedAgentEvals(): Map<string, EvalCase[]> {
  const m = new Map<string, EvalCase[]>();
  m.set('aml-narrative', [
    {
      name: 'produces narrative',
      prompt: 'Wire of $50,000 to a sanctioned region',
      mode: 'contains',
      expect: 'narrative'
    },
    {
      name: 'handles structuring',
      prompt: 'Structuring across 6 deposits under threshold',
      mode: 'contains',
      expect: 'narrative'
    }
  ]);
  m.set('kyc-extract', [
    {
      name: 'extracts passport',
      prompt: 'Passport, DOB 1990-01-01',
      mode: 'contains',
      expect: 'doc_number'
    }
  ]);
  m.set('dispute-summarizer', [
    {
      name: 'recommends action',
      prompt: 'Chargeback for non-receipt, $210',
      mode: 'contains',
      expect: 'recommendation'
    }
  ]);
  m.set('fraud-explainer', [
    {
      name: 'explains drivers',
      prompt: 'Score 88: high velocity, new device',
      mode: 'contains',
      expect: 'velocity'
    }
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
    text: status === 'completed' ? agentReply(prompt).text : '',
    structured: undefined,
    error: status === 'failed' ? 'provider timeout' : undefined,
    at: ago(hrs)
  });
  const amlPrompts = [
    'Wire of $50,000 to a high-risk jurisdiction',
    'Structuring pattern across 6 deposits',
    'Rapid pass-through funding from a shell entity',
    'Cash deposits just under the reporting threshold',
    'Cross-border transfer to a PEP-linked account',
    'Round-tripping between affiliated accounts',
    'Unusual surge in inbound remittances',
    'Trade-based laundering via over-invoicing'
  ];
  const kycPrompts = [
    'Passport, DOB 1990-01-01',
    'Utility bill, address verification',
    'Company registration extract',
    'Driver license, expired',
    'Proof of funds statement'
  ];
  const disputePrompts = [
    'Chargeback for non-receipt, $210',
    'Duplicate charge dispute, $89',
    'Fraudulent transaction claim, $740',
    'Subscription not canceled, $29',
    'Quality dispute, $1,200'
  ];
  const fraudPrompts = [
    'Score 88: high velocity, new device',
    'Score 41: mid risk, mismatched geo',
    'Score 12: low risk, trusted device',
    'Score 92: account takeover signals',
    'Score 33: card-present, recurring merchant'
  ];
  const out: AgentRun[] = [];
  let n = 0;
  amlPrompts.forEach((p, i) => {
    n += 1;
    out.push(
      run(
        `run_${n}`,
        'aml-narrative',
        'claude-sonnet',
        p,
        6 + i * 14,
        i === 4 ? 'failed' : 'completed'
      )
    );
  });
  kycPrompts.forEach((p, i) => {
    n += 1;
    out.push(
      run(
        `run_${n}`,
        'kyc-extract',
        'claude-haiku',
        p,
        18 + i * 26,
        i === 3 ? 'failed' : 'completed'
      )
    );
  });
  disputePrompts.forEach((p, i) => {
    n += 1;
    out.push(run(`run_${n}`, 'dispute-summarizer', 'claude-haiku', p, 12 + i * 20, 'completed'));
  });
  fraudPrompts.forEach((p, i) => {
    n += 1;
    out.push(
      run(
        `run_${n}`,
        'fraud-explainer',
        'claude-sonnet',
        p,
        4 + i * 9,
        i === 3 ? 'failed' : 'completed'
      )
    );
  });
  return out;
}

function seedGrants(): Map<string, FlowGrant[]> {
  return new Map([
    [
      'flow_credit',
      [
        {
          grant_id: 'g_c1',
          flow_id: 'flow_credit',
          actor: DIEGO,
          environment: 'sandbox',
          created_by: AVA,
          created_at: ago(100)
        },
        {
          grant_id: 'g_c2',
          flow_id: 'flow_credit',
          actor: PRIYA,
          environment: '*',
          created_by: AVA,
          created_at: ago(180)
        }
      ]
    ],
    [
      'flow_aml',
      [
        {
          grant_id: 'g_a1',
          flow_id: 'flow_aml',
          actor: DIEGO,
          environment: 'staging',
          created_by: AVA,
          created_at: ago(60)
        }
      ]
    ],
    [
      'flow_fraud',
      [
        {
          grant_id: 'g_f1',
          flow_id: 'flow_fraud',
          actor: PRIYA,
          environment: '*',
          created_by: AVA,
          created_at: ago(40)
        }
      ]
    ]
  ]);
}

function seedSchedules(): Map<string, ScheduledDeploy[]> {
  return new Map([
    [
      'flow_credit',
      [
        {
          schedule_id: 'sch_c1',
          flow_id: 'flow_credit',
          environment: 'staging',
          version: 3,
          at: ahead(2),
          status: 'pending',
          prior_version: 2,
          created_at: ago(10)
        }
      ]
    ],
    [
      'flow_fraud',
      [
        {
          schedule_id: 'sch_f1',
          flow_id: 'flow_fraud',
          environment: 'sandbox',
          version: 4,
          at: ahead(1),
          until: ahead(5),
          status: 'pending',
          prior_version: 3,
          created_at: ago(6)
        }
      ]
    ]
  ]);
}

type CommentRec = DemoState['comments'] extends Map<string, infer V> ? V : never;

function seedComments(): DemoState['comments'] {
  const m: DemoState['comments'] = new Map();
  const c = (
    id: string,
    type: string,
    subject: string,
    body: string,
    author: string,
    hrs: number,
    parent?: string
  ): CommentRec[number] => ({
    comment_id: id,
    subject_type: type,
    subject_id: subject,
    body,
    parent_id: parent,
    author,
    at: ago(hrs)
  });
  m.set('decision/dec_2', [
    c(
      'cmt_1',
      'decision',
      'dec_2',
      'Counterparty KYC is stale — flagging for compliance.',
      LENA,
      18
    ),
    c('cmt_2', 'decision', 'dec_2', 'Agreed, holding the wire pending refresh.', DIEGO, 16, 'cmt_1')
  ]);
  m.set('case/case_2', [
    c(
      'cmt_3',
      'case',
      'case_2',
      'SAR draft started; will attach the narrative agent output.',
      DIEGO,
      5
    ),
    c('cmt_4', 'case', 'case_2', 'Loop me in before filing.', MARCUS, 4, 'cmt_3')
  ]);
  m.set('flow/flow_credit', [
    c(
      'cmt_5',
      'flow',
      'flow_credit',
      'v3 tightens the approve band to risk<35 — please review before prod.',
      PRIYA,
      14
    )
  ]);
  return m;
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
  const decisions = seedDecisions();
  const cases = seedCases();
  // Backfill the reverse decision→case link: a seeded case carries its source
  // decision id, but the decision needs `case_id` set for the trace page to surface
  // the "opened case" link. One-time at state creation; first case wins when several
  // share a source decision (the link only needs to land on a real, related case).
  const decisionById = new Map(decisions.map((d) => [d.decision_id, d]));
  for (const c of cases) {
    if (!c.source_decision_id) continue;
    const dec = decisionById.get(c.source_decision_id);
    if (dec && !dec.case_id) dec.case_id = c.case_id;
  }
  const audit = seedAudit(decisions);
  return {
    identity: identityFor(USERS[0]),
    flows: seedFlows(),
    decisions,
    cases,
    agents,
    agentRuns,
    agentVersions: seedAgentVersions(),
    agentEvals: seedAgentEvals(),
    models: seedModels(),
    modelBaselines: new Map([
      ['credit_pd', [3, 5, 8, 6, 2]],
      ['fraud_score', [10, 6, 3, 2, 1]],
      ['aml_risk', [8, 5, 4, 2, 1]]
    ]),
    modelMonitors: new Map([
      ['credit_pd', 0.2],
      ['fraud_score', 0.25],
      ['aml_risk', 0.3]
    ]),
    connectors: seedConnectors(),
    connectorCatalog: seedCatalog(),
    features: seedFeatures(),
    entities: seedEntities(),
    entityEvents: seedEntityEvents(),
    policies: seedPolicies(),
    preapprovals: seedPreApprovals(),
    monitors: seedMonitors(),
    assertions: seedAssertions(),
    grants: seedGrants(),
    schedules: seedSchedules(),
    flowBaselines: new Map([
      ['flow_credit', { approve: 12, decline: 4, refer: 6 }],
      ['flow_aml', { approve: 14, decline: 0, refer: 5 }],
      ['flow_fraud', { approve: 18, decline: 3, refer: 7 }]
    ]),
    flowSlos: new Map([
      ['flow_credit', { success_target: 0.85, latency_target_ms: 200 }],
      ['flow_aml', { success_target: 0.98, latency_target_ms: 300 }],
      ['flow_fraud', { success_target: 0.99, latency_target_ms: 120 }]
    ]),
    shadows: new Map([
      ['flow_credit', new Map([['production', 3]])],
      ['flow_fraud', new Map([['staging', 4]])]
    ]),
    webhooks: seedWebhooks(),
    notifications: seedNotifications(),
    audit,
    apiKeys: seedApiKeys(),
    privacy: { fields: ['ssn', 'dob', 'pan'], updated_at: ago(500), updated_by: AVA },
    comments: seedComments(),
    seq: audit.length + 1
  };
}

// --- Persistence ----------------------------------------------------------------
// The demo state is persisted to localStorage so a visitor can ADVANCE flows across
// reloads (build → publish → deploy → decide → triage → resolve), not just within a
// single page view. Bump SCHEMA_VERSION whenever the seed/state shape changes so an
// older persisted blob is discarded (re-seeded) instead of hydrating a stale shape.
const SCHEMA_VERSION = 2;
const PERSIST_KEY = 'intraktible-demo-state';

// Map values can't survive JSON, so tag them on write and rebuild on read. The
// reviver runs inner-first, so the nested `shadows` Map<string,Map<…>> round-trips
// without special-casing. Tagging (vs enumerating fields) avoids object-injection.
function mapReplacer(_k: string, v: unknown): unknown {
  return v instanceof Map ? { __map: Array.from(v.entries()) } : v;
}
function mapReviver(_k: string, v: unknown): unknown {
  if (v && typeof v === 'object' && '__map' in v) {
    return new Map((v as { __map: [unknown, unknown][] }).__map);
  }
  return v;
}

function canPersist(): boolean {
  try {
    return typeof localStorage !== 'undefined';
  } catch {
    return false;
  }
}

// loadPersisted hydrates the saved state when present and schema-compatible; any
// version mismatch or parse error discards it (returns null) so we fall back to a
// fresh seed — never hydrate a shape the code no longer understands.
function loadPersisted(): DemoState | null {
  if (!canPersist()) return null;
  try {
    const raw = localStorage.getItem(PERSIST_KEY);
    if (!raw) return null;
    const blob = JSON.parse(raw, mapReviver) as { v: number; idCounter: number; state: DemoState };
    if (blob.v !== SCHEMA_VERSION || !blob.state || !isValidState(blob.state)) return null;
    if (typeof blob.idCounter === 'number') idCounter = blob.idCounter;
    return blob.state;
  } catch {
    return null;
  }
}

// isValidState is a shallow shape check so a stale/partial blob (e.g. a Map field
// that hydrated as a plain object because SCHEMA_VERSION wasn't bumped, or a missing
// collection) is discarded and re-seeded rather than crashing every page on the
// first `.get`/`.filter`. Spot-checks one representative field of each kind.
function isValidState(s: DemoState): boolean {
  const arrays = [s.flows, s.decisions, s.cases, s.agents, s.audit];
  const maps = [s.monitors, s.grants, s.flowSlos, s.shadows, s.comments];
  return (
    !!s.identity &&
    typeof s.seq === 'number' &&
    arrays.every(Array.isArray) &&
    maps.every((m) => m instanceof Map)
  );
}

// persist saves the current state (called after each mutating request). Best-effort:
// a serialization/quota failure must never crash the demo.
export function persist(): void {
  if (!canPersist()) return;
  try {
    localStorage.setItem(
      PERSIST_KEY,
      JSON.stringify({ v: SCHEMA_VERSION, idCounter, state }, mapReplacer)
    );
  } catch {
    // ignore (quota / serialization) — the in-memory state is still authoritative
  }
}

// resetDemo clears the persisted state so the next load re-seeds. The Reset control
// in DemoBanner calls this then reloads the page.
export function resetDemo(): void {
  if (!canPersist()) return;
  try {
    localStorage.removeItem(PERSIST_KEY);
  } catch {
    // ignore
  }
}

// The single shared, mutable state instance for the session: the persisted blob if
// one exists and matches the schema, otherwise a fresh seed.
export const state: DemoState = loadPersisted() ?? createState();

// psi computes the Population Stability Index between two binned distributions —
// the real formula (Σ (a−e)·ln(a/e) over normalized bins), not a hardcoded constant.
export function psi(baseline: number[], current: number[]): number {
  const sb = baseline.reduce((a, b) => a + b, 0) || 1;
  const sc = current.reduce((a, b) => a + b, 0) || 1;
  const total = baseline.reduce((acc, b, i) => {
    const e = Math.max(b / sb, 1e-4);
    const a = Math.max((current.at(i) ?? 0) / sc, 1e-4);
    return acc + (a - e) * Math.log(a / e);
  }, 0);
  return Math.round(total * 1000) / 1000;
}

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
