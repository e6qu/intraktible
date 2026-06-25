// SPDX-License-Identifier: AGPL-3.0-or-later

// Presentation helpers for the flow-builder node cards: a one-line summary of a
// node's config and an accent colour per type. Pure and config-tolerant (a node
// being edited may hold partial/invalid JSON), so the card never throws.

import { assertNever } from '$lib/api';
import type { NodeType } from './enums.generated';

// NodeType is the closed set of flow-builder node kinds, GENERATED from the Go
// events.NodeType consts (single source of truth). NODE_TYPES (the palette order),
// the accent map, and the card summary all derive from it, and the exhaustive switch
// in nodeSummary fails the build if a new kind is added without being handled — so
// "forgot to handle the new node type" is unrepresentable, not a silent fallback.
export type { NodeType };

// NODE_TYPES is the builder palette order. There is a matching `--node-<type>` CSS
// custom property in app.css for each (the accent rail).
export const NODE_TYPES: NodeType[] = [
  'input',
  'assignment',
  'rule',
  'split',
  'scorecard',
  'decision_table',
  '2d_matrix',
  'code',
  'connect',
  'ai',
  'predict',
  'reason',
  'manual_review',
  'output'
];

// nodeTypeLabel is a human-readable label for the node-type pickers — the raw ids
// (2d_matrix, manual_review, …) are opaque to a first-time author. Exhaustive: a new
// node type fails the build here until it's labelled, like nodeSummary below.
export function nodeTypeLabel(type: NodeType): string {
  switch (type) {
    case 'input':
      return 'Input';
    case 'assignment':
      return 'Assignment — set fields';
    case 'rule':
      return 'Rule — if/then';
    case 'split':
      return 'Split — branch on a condition';
    case 'scorecard':
      return 'Scorecard — weighted score';
    case 'decision_table':
      return 'Decision table — DMN rules';
    case '2d_matrix':
      return '2D matrix — grid lookup';
    case 'code':
      return 'Code — Starlark';
    case 'connect':
      return 'Connect — external data';
    case 'ai':
      return 'AI — LLM agent';
    case 'predict':
      return 'Predict — ML model';
    case 'reason':
      return 'Reason — adverse-action codes';
    case 'manual_review':
      return 'Manual review — human task';
    case 'output':
      return 'Output — final result';
    default:
      return assertNever(type);
  }
}

const NODE_TYPE_SET: ReadonlySet<string> = new Set<string>(NODE_TYPES);

// isNodeType narrows an arbitrary string (a node being edited may carry a partial or
// not-yet-recognized type) to the closed NodeType union.
export function isNodeType(type: string): type is NodeType {
  return NODE_TYPE_SET.has(type);
}

// accent maps a node type to a CSS custom property (defined in app.css) for the
// card's left rail / icon tint. Unknown types fall back to the neutral accent.
export function nodeAccent(type: string): string {
  return isNodeType(type) ? `var(--node-${type})` : 'var(--accent)';
}

function parse(config: string): Record<string, unknown> {
  if (!config.trim()) return {};
  try {
    const v = JSON.parse(config);
    return v && typeof v === 'object' ? (v as Record<string, unknown>) : {};
  } catch {
    return {};
  }
}

function len(v: unknown): number {
  return Array.isArray(v) ? v.length : 0;
}

function plural(n: number, one: string): string {
  return `${n} ${one}${n === 1 ? '' : 's'}`;
}

// nodeSummary returns a short, human description of what a node does, derived from
// its config — the second line of the card.
export function nodeSummary(type: string, config: string): string {
  if (!isNodeType(type)) return type; // tolerant: a partial/unknown type shows itself
  const c = parse(config);
  switch (type) {
    case 'input':
      return 'flow entry';
    case 'output':
      return len(c.fields) ? plural(len(c.fields), 'field') : 'all fields';
    case 'rule':
      return plural(len(c.rules), 'rule');
    case 'split':
      return len(c.branches) ? plural(len(c.branches), 'branch') : 'branch';
    case 'scorecard':
      return plural(len(c.factors), 'factor');
    case 'decision_table':
      return plural(len(c.rows), 'row');
    case '2d_matrix': {
      const rows = len(c.rows);
      const cols = len(c.cols);
      return rows && cols ? `${rows}×${cols} matrix` : 'matrix';
    }
    case 'assignment':
      return plural(len(c.assignments), 'assignment');
    case 'code':
      return 'Starlark';
    case 'connect':
      return typeof c.connector === 'string' && c.connector ? String(c.connector) : 'connector';
    case 'ai':
      return typeof c.agent === 'string' && c.agent ? String(c.agent) : 'AI';
    case 'predict':
      return typeof c.model === 'string' && c.model ? String(c.model) : 'prediction';
    case 'reason':
      return plural(len(c.reasons), 'reason code');
    case 'manual_review':
      return 'human review';
    default:
      // Exhaustiveness guard: if a NodeType is added without a case above, `type` is
      // no longer `never` here and this fails to compile.
      return assertNever(type, 'node type');
  }
}

// BpmnKind is the BPMN shape a node maps to in the process view.
export type BpmnKind = 'start' | 'end' | 'gateway' | 'task';

// bpmnKind maps a flow node type to its BPMN notation: the input is a start
// event, the output an end event, a split a gateway, everything else a task.
export function bpmnKind(type: string): BpmnKind {
  switch (type) {
    case 'input':
      return 'start';
    case 'output':
      return 'end';
    case 'split':
      return 'gateway';
    default:
      return 'task';
  }
}

// telemetrySummary renders a node's last test-run output compactly for the card's
// status badge (e.g. {"score":72} → "score: 72"; a scalar → its value).
export function telemetrySummary(output: unknown): string {
  if (output === null || output === undefined) return '';
  if (typeof output !== 'object') return String(output);
  const entries = Object.entries(output as Record<string, unknown>);
  if (entries.length === 0) return '∅';
  const [k, v] = entries[0];
  const rest = entries.length > 1 ? ` +${entries.length - 1}` : '';
  return `${k}: ${typeof v === 'object' ? '…' : String(v)}${rest}`;
}
