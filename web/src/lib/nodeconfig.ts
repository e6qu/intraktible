// SPDX-License-Identifier: AGPL-3.0-or-later

// Pure helpers backing the builder's structured per-node config panels: they read
// and normalise a node's config object without dynamic key access (so the inputs
// can use static property reads + object spread, and the published config stays
// clean of empty fields).

// asText renders a config value as an input string.
export function asText(v: unknown): string {
  if (typeof v === 'string') return v;
  if (v === null || v === undefined) return '';
  return String(v);
}

// asNum renders a numeric config value as an input string ('' when unset).
export function asNum(v: unknown): string {
  return typeof v === 'number' && !Number.isNaN(v) ? String(v) : '';
}

// asCsv renders a string-array config value as a comma-separated input string.
export function asCsv(v: unknown): string {
  return Array.isArray(v) ? v.join(', ') : '';
}

// asCellText renders a matrix cell's literal value as editable text: a string
// shows unquoted, other JSON values show as compact JSON, unset shows ''.
export function asCellText(v: unknown): string {
  if (v === undefined || v === null) return '';
  if (typeof v === 'string') return v;
  return JSON.stringify(v);
}

// parseCell turns cell input text into a literal: valid JSON parses (so `7`,
// `true`, `"x"` keep their type), anything else is kept as a plain string.
export function parseCell(s: string): unknown {
  const t = s.trim();
  if (t === '') return '';
  try {
    return JSON.parse(t);
  } catch {
    return s;
  }
}

// An edge in the builder model.
export interface BuilderEdge {
  from: string;
  to: string;
  branch?: string;
}

// addUniqueEdge appends from→to (drag-to-connect) unless an identical edge with
// the same branch already exists; it returns a new array (never mutates).
export function addUniqueEdge(edges: BuilderEdge[], from: string, to: string): BuilderEdge[] {
  if (!from || !to) return edges;
  if (edges.some((e) => e.from === from && e.to === to && !e.branch)) return edges;
  return [...edges, { from, to }];
}

// fromCsv parses a comma-separated input into a trimmed, non-empty string array.
export function fromCsv(s: string): string[] {
  return s
    .split(',')
    .map((x) => x.trim())
    .filter(Boolean);
}

// cleanConfig drops empty fields (empty string, null/undefined, empty array) so a
// structured panel never publishes `{"condition":""}` or stray keys.
export function cleanConfig(o: Record<string, unknown>): Record<string, unknown> {
  return Object.fromEntries(
    Object.entries(o).filter(
      ([, v]) => v !== '' && v !== null && v !== undefined && !(Array.isArray(v) && v.length === 0)
    )
  );
}
