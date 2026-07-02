// SPDX-License-Identifier: AGPL-3.0-or-later
import { describe, it, expect } from 'vitest';
import { decisionsByDay, decisionStats, percentile, personaHomeStats } from './dashboard';
import type { DashboardData } from './dashboard';
import type { Decision } from './api';

function dec(started_at: string, status = 'completed', duration_ms = 10): Decision {
  return {
    decision_id: Math.random().toString(36).slice(2),
    flow_id: 'f',
    slug: 'f',
    version: 1,
    environment: 'production',
    status,
    started_at,
    duration_ms
  } as Decision;
}

describe('decisionsByDay', () => {
  it('buckets by calendar day into a dense zero-filled window ending at the last active day', () => {
    const series = decisionsByDay(
      [dec('2026-06-15T10:00:00Z'), dec('2026-06-15T23:59:00Z'), dec('2026-06-17T01:00:00Z')],
      4
    );
    expect(series).toEqual([
      { day: '2026-06-14', count: 0 },
      { day: '2026-06-15', count: 2 },
      { day: '2026-06-16', count: 0 },
      { day: '2026-06-17', count: 1 }
    ]);
  });

  it('always renders the full window — one busy day is one bar among maxDays, not a slab', () => {
    const series = decisionsByDay([dec('2026-06-15T10:00:00Z')], 14);
    expect(series).toHaveLength(14);
    expect(series.at(-1)).toEqual({ day: '2026-06-15', count: 1 });
    expect(series.slice(0, -1).every((s) => s.count === 0)).toBe(true);
  });

  it('keeps only the most recent maxDays days', () => {
    const days = ['2026-06-10', '2026-06-11', '2026-06-12', '2026-06-13'];
    const series = decisionsByDay(
      days.map((d) => dec(`${d}T00:00:00Z`)),
      2
    );
    expect(series.map((s) => s.day)).toEqual(['2026-06-12', '2026-06-13']);
  });

  it('is empty for no decisions', () => {
    expect(decisionsByDay([])).toEqual([]);
  });
});

describe('decisionStats', () => {
  it('computes counts, completion rate, and latency percentiles', () => {
    const s = decisionStats([
      dec('2026-06-15T00:00:00Z', 'completed', 10),
      dec('2026-06-15T00:00:00Z', 'completed', 30),
      dec('2026-06-15T00:00:00Z', 'failed', 20)
    ]);
    expect(s.total).toBe(3);
    expect(s.completed).toBe(2);
    expect(s.failed).toBe(1);
    expect(Math.round(s.completionRate * 100)).toBe(67);
    expect(s.p50Ms).toBeGreaterThan(0);
  });
});

describe('percentile', () => {
  it('uses nearest-rank and returns 0 for empty', () => {
    expect(percentile([], 0.5)).toBe(0);
    expect(percentile([10, 20, 30, 40], 0.5)).toBe(20);
    expect(percentile([10, 20, 30, 40], 0.95)).toBe(40);
  });
});

describe('personaHomeStats', () => {
  const data: DashboardData = {
    flows: [{}, {}] as unknown as DashboardData['flows'],
    decisions: [
      dec('2026-06-15T00:00:00Z', 'completed', 10),
      dec('2026-06-15T00:00:00Z', 'failed', 20),
      { ...dec('2026-06-15T00:00:00Z', 'completed', 30), variant: 'challenger' } as Decision
    ],
    cases: {
      total: 5,
      by_status: { needs_review: 3 },
      unassigned: 2,
      overdue: 1,
      due_soon: 0
    } as DashboardData['cases'],
    runs: {} as DashboardData['runs']
  };

  it('selects and computes the requested tiles, in order, over shared data', () => {
    const tiles = personaHomeStats(['failed', 'challenger', 'needs_review', 'overdue'], data);
    expect(tiles.map((t) => t.id)).toEqual(['failed', 'challenger', 'needs_review', 'overdue']);
    expect(tiles[0].value).toBe(1); // one failed decision
    expect(tiles[1].value).toBe(1); // one challenger-variant decision
    expect(tiles[2].value).toBe(3); // needs_review from the case summary
    expect(tiles[3].value).toBe(1); // overdue from the case summary
  });

  it('formats latency and rate tiles', () => {
    const [p95, rate] = personaHomeStats(['p95', 'completion_rate'], data);
    expect(String(p95.value)).toMatch(/ms$/);
    expect(String(rate.value)).toMatch(/%$/);
  });
});
