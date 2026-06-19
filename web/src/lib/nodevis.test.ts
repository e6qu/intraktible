// SPDX-License-Identifier: AGPL-3.0-or-later
import { describe, it, expect } from 'vitest';
import { nodeSummary, nodeAccent, telemetrySummary, bpmnKind } from './nodevis';

describe('nodeSummary', () => {
  it('summarizes config per type', () => {
    expect(nodeSummary('input', '')).toBe('flow entry');
    expect(nodeSummary('output', '{"fields":["a","b"]}')).toBe('2 fields');
    expect(nodeSummary('output', '')).toBe('all fields');
    expect(nodeSummary('rule', '{"rules":[{}]}')).toBe('1 rule');
    expect(nodeSummary('scorecard', '{"factors":[{},{},{}]}')).toBe('3 factors');
    expect(nodeSummary('2d_matrix', '{"rows":[{},{}],"cols":[{}]}')).toBe('2×1 matrix');
    expect(nodeSummary('connect', '{"connector":"bureau"}')).toBe('bureau');
    expect(nodeSummary('ai', '{"agent":"screener"}')).toBe('screener'); // reads the configured agent
    expect(nodeSummary('ai', '')).toBe('AI');
    expect(nodeSummary('code', 'anything')).toBe('Starlark');
    expect(nodeSummary('manual_review', '')).toBe('human review');
  });

  it('tolerates partial / invalid JSON without throwing', () => {
    expect(nodeSummary('rule', '{bad json')).toBe('0 rules');
    expect(nodeSummary('output', '{"fields":')).toBe('all fields');
  });

  it('falls back to the type name for unknown types', () => {
    expect(nodeSummary('mystery', '')).toBe('mystery');
  });
});

describe('nodeAccent', () => {
  it('maps known types to a CSS var and unknown to the default accent', () => {
    expect(nodeAccent('scorecard')).toBe('var(--node-scorecard)');
    expect(nodeAccent('mystery')).toBe('var(--accent)');
  });
});

describe('bpmnKind', () => {
  it('maps node types to BPMN shapes', () => {
    expect(bpmnKind('input')).toBe('start');
    expect(bpmnKind('output')).toBe('end');
    expect(bpmnKind('split')).toBe('gateway');
    expect(bpmnKind('rule')).toBe('task');
    expect(bpmnKind('manual_review')).toBe('task');
  });
});

describe('telemetrySummary', () => {
  it('renders the last output compactly', () => {
    expect(telemetrySummary({ score: 72 })).toBe('score: 72');
    expect(telemetrySummary({ a: 1, b: 2 })).toBe('a: 1 +1');
    expect(telemetrySummary('APPROVE')).toBe('APPROVE');
    expect(telemetrySummary({})).toBe('∅');
    expect(telemetrySummary(undefined)).toBe('');
  });
});
