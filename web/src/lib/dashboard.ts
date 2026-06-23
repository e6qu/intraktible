// SPDX-License-Identifier: AGPL-3.0-or-later
// Shared landing-dashboard data. The three persona decks (Builder / Operator /
// Showcase) render the SAME underlying data, re-prioritised for the viewer — so
// the load + derivations live here, once, and each deck reads what it needs.

import {
  listFlows,
  listDecisions,
  getCaseSummary,
  getRunSummary,
  type Flow,
  type Decision,
  type CaseSummary,
  type RunSummary
} from '$lib/api';
import type { HomeStatId } from '$lib/persona';

export interface DashboardData {
  flows: Flow[];
  decisions: Decision[];
  cases: CaseSummary;
  runs: RunSummary;
}

// loadDashboard fetches every landing input in parallel. All four reads are
// viewer-role-safe (no admin-only audit), so any persona can render the deck.
export async function loadDashboard(
  key = '',
  fetcher: typeof fetch = fetch
): Promise<DashboardData> {
  const [flows, decisions, cases, runs] = await Promise.all([
    listFlows(key, fetcher),
    listDecisions(key, fetcher),
    getCaseSummary(key, {}, fetcher),
    getRunSummary(key, fetcher)
  ]);
  return { flows, decisions, cases, runs };
}

export interface DecisionStats {
  total: number;
  completed: number;
  failed: number;
  completionRate: number; // 0..1
  avgMs: number;
  p50Ms: number;
  p95Ms: number;
}

// percentile returns the p-th (0..1) percentile of a numeric series using the
// nearest-rank method; 0 for an empty series.
export function percentile(values: number[], p: number): number {
  if (values.length === 0) return 0;
  const sorted = [...values].sort((a, b) => a - b);
  const rank = Math.ceil(p * sorted.length);
  const idx = Math.min(sorted.length - 1, Math.max(0, rank - 1));
  return sorted.at(idx) ?? 0;
}

export function decisionStats(decisions: Decision[]): DecisionStats {
  const total = decisions.length;
  const completed = decisions.filter((d) => d.status === 'completed').length;
  const failed = decisions.filter((d) => d.status === 'failed').length;
  const durations = decisions.map((d) => d.duration_ms ?? 0).filter((ms) => ms > 0);
  const sum = durations.reduce((a, b) => a + b, 0);
  return {
    total,
    completed,
    failed,
    completionRate: total ? completed / total : 0,
    avgMs: durations.length ? Math.round(sum / durations.length) : 0,
    p50Ms: Math.round(percentile(durations, 0.5)),
    p95Ms: Math.round(percentile(durations, 0.95))
  };
}

// liveDeployments counts flows that have at least one environment pinned to a
// version, and pendingApprovals counts open maker-checker requests across flows.
export function deployStats(flows: Flow[]): { live: number; pending: number } {
  let live = 0;
  let pending = 0;
  for (const f of flows) {
    if (f.deployments && Object.keys(f.deployments).length > 0) live += 1;
    pending += (f.deployment_requests ?? []).filter((r) => r.status === 'pending').length;
  }
  return { live, pending };
}

// decisionsByDay buckets decisions by calendar day (the RFC3339 date prefix of
// started_at), returning the most recent maxDays active days in ascending order —
// the series behind the Executive volume trend. Clock-free (derived from the data),
// so it is deterministic and testable; days with no decisions are simply absent.
export function decisionsByDay(
  decisions: Decision[],
  maxDays = 14
): { day: string; count: number }[] {
  const counts = new Map<string, number>();
  for (const d of decisions) {
    if (!d.started_at) continue;
    const day = d.started_at.slice(0, 10);
    counts.set(day, (counts.get(day) ?? 0) + 1);
  }
  return [...counts.keys()]
    .sort()
    .slice(-maxDays)
    .map((day) => ({ day, count: counts.get(day) ?? 0 }));
}

export function pct(n: number): string {
  return `${Math.round(n * 100)}%`;
}

// HomeStat is one rendered PersonaHome tile: a value plus its short label.
export interface HomeStat {
  id: HomeStatId;
  value: string | number;
  label: string;
  href: string; // the surface this KPI drills into
  sub?: string; // a one-line qualifier (e.g. "93% success · 3 failed")
}

// DEFAULT_HOME_STATS is the generic deck for personas that declare no homeStats.
export const DEFAULT_HOME_STATS: HomeStatId[] = ['decisions', 'completed', 'flows'];

// personaHomeStats resolves a persona's chosen stat ids into rendered tiles, computed
// from the SAME dashboard data every persona loads — the role chooses which questions
// to foreground (a manager's pending/overdue vs a developer's failed/latency), not a
// different dataset. A switch (not an index) keeps the mapping exhaustive and clear of
// the object-injection lint.
export function personaHomeStats(ids: HomeStatId[], data: DashboardData): HomeStat[] {
  const stats = decisionStats(data.decisions);
  const deploy = deployStats(data.flows);
  const challenger = data.decisions.filter((d) => d.variant === 'challenger').length;
  const stat = (id: HomeStatId): HomeStat => {
    switch (id) {
      case 'decisions':
        return {
          id,
          value: stats.total,
          label: 'decisions',
          href: '/decisions',
          sub: `${pct(stats.completionRate)} success · ${stats.failed} failed`
        };
      case 'completed':
        return { id, value: stats.completed, label: 'completed', href: '/decisions' };
      case 'failed':
        return { id, value: stats.failed, label: 'failed', href: '/decisions' };
      case 'flows':
        return {
          id,
          value: data.flows.length,
          label: 'flows',
          href: '/engine',
          sub: `${deploy.live} live · ${deploy.pending} pending`
        };
      case 'p95':
        return { id, value: `${stats.p95Ms} ms`, label: 'p95 latency', href: '/observability' };
      case 'completion_rate':
        return {
          id,
          value: pct(stats.completionRate),
          label: 'completion',
          href: '/observability'
        };
      case 'pending_approvals':
        return {
          id,
          value: deploy.pending,
          label: 'pending approvals',
          href: '/preapprovals'
        };
      case 'needs_review':
        return {
          id,
          value: data.cases.by_status?.needs_review ?? 0,
          label: 'needs review',
          href: '/cases',
          sub: `${data.cases.overdue} overdue · ${data.cases.unassigned} unassigned`
        };
      case 'overdue':
        return { id, value: data.cases.overdue, label: 'overdue', href: '/cases' };
      case 'unassigned':
        return { id, value: data.cases.unassigned, label: 'unassigned', href: '/cases' };
      case 'challenger':
        return { id, value: challenger, label: 'challenger arm', href: '/decisions' };
    }
  };
  return ids.map(stat);
}

// compact formats a count with a k suffix past 1000 (e.g. 12_400 → "12.4k").
export function compact(n: number): string {
  if (n < 1000) return String(n);
  const k = n / 1000;
  return `${k >= 10 ? Math.round(k) : k.toFixed(1)}k`;
}
