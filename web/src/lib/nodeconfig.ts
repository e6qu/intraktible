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

// asCsv renders a string-array config value as a comma-separated input string.
export function asCsv(v: unknown): string {
  return Array.isArray(v) ? v.join(', ') : '';
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
