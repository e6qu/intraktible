// SPDX-License-Identifier: AGPL-3.0-or-later
// In-memory, mutable state + a rich finance/risk seed for the client-side demo
// backend. Every collection is typed against the $lib/api interfaces so
// svelte-check enforces the same shapes the real server returns — the demo can
// never drift from the wire contract. The seed itself lives in ./seeds/* (one
// module per concern); decision histories are synthesized by WALKING the seeded
// graphs through the pure engine core (./walk), so traces, dispositions and model
// outputs are internally consistent by construction. Writes from the router mutate
// this state in place, so a created flow / added case note survives a list reload
// within the session (and localStorage persistence carries it across reloads).

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
  DriftReport
} from '$lib/api';
import { USERS, ACTOR, AVA, ago, ahead, type DemoUser } from './seeds/base';
import { seedFlows } from './seeds/flows';
import { seedDecisions } from './seeds/decisions';
import { seedCases } from './seeds/cases';
import { seedAgents, seedAgentVersions, seedAgentEvals, seedAgentRuns } from './seeds/agents';
import {
  seedModels,
  seedConnectors,
  seedCatalog,
  seedFeatures,
  seedEntities,
  seedEntityEvents
} from './seeds/context';
import {
  seedPolicies,
  seedPreApprovals,
  seedMonitors,
  seedAssertions,
  seedWebhooks,
  seedNotifications,
  seedApiKeys,
  seedGrants,
  seedSchedules,
  seedComments,
  seedAudit,
  seedFlowSlos,
  seedFlowBaselines,
  type CommentRec
} from './seeds/governance';

export { USERS, ACTOR, ago, ahead };
export type { DemoUser };

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
  comments: Map<string, CommentRec[]>;
  seq: number;
}

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

// createState assembles a fresh seeded state (called once per page load). Seed
// order matters: decisions walk the flows' graphs with the models and policies,
// and cases link decisions — so those seed first, as arguments.
export function createState(): DemoState {
  const models = seedModels();
  const policies = seedPolicies();
  const flows = seedFlows();
  const decisions = seedDecisions(flows, models, policies);
  const cases = seedCases(decisions);
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
  const agentRuns = seedAgentRuns();
  // Derive each agent's run counter from the actual run records, so the agents-page
  // summary, the per-agent count, and the observability/MRM rollups can never drift.
  const agents = seedAgents().map((a) => ({
    ...a,
    runs: agentRuns.filter((r) => r.agent === a.name).length
  }));
  const audit = seedAudit(decisions);
  return {
    identity: identityFor(USERS[0]),
    flows,
    decisions,
    cases,
    agents,
    agentRuns,
    agentVersions: seedAgentVersions(),
    agentEvals: seedAgentEvals(),
    models,
    modelBaselines: new Map([
      ['credit_pd', [3, 5, 8, 6, 2]],
      ['fraud_score', [10, 6, 3, 2, 1]],
      ['aml_risk', [8, 5, 4, 2, 1]],
      ['claim_fraud', [9, 3, 1, 1, 1]]
    ]),
    modelMonitors: new Map([
      ['credit_pd', 0.2],
      ['fraud_score', 0.25],
      ['aml_risk', 0.3],
      ['claim_fraud', 0.1]
    ]),
    connectors: seedConnectors(),
    connectorCatalog: seedCatalog(),
    features: seedFeatures(),
    entities: seedEntities(),
    entityEvents: seedEntityEvents(),
    policies,
    preapprovals: seedPreApprovals(),
    monitors: seedMonitors(),
    assertions: seedAssertions(),
    grants: seedGrants(),
    schedules: seedSchedules(),
    flowBaselines: seedFlowBaselines(),
    flowSlos: seedFlowSlos(),
    shadows: new Map([
      ['flow_credit', new Map([['production', 3]])],
      ['flow_payout', new Map([['staging', 1]])]
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
const SCHEMA_VERSION = 3;
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
