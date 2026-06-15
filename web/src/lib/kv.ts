// SPDX-License-Identifier: AGPL-3.0-or-later

// displayEntries flattens a JSON object's top-level fields into [key, text] pairs
// for a readable key-value view (e.g. a case's context: the source decision's
// inputs, or an agent escalation's reference). Primitives render as text; nested
// objects/arrays render as compact JSON. A non-object yields no entries.
export function displayEntries(value: unknown): [string, string][] {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) return [];
  return Object.entries(value as Record<string, unknown>).map(([k, v]) => [
    k,
    v !== null && typeof v === 'object' ? JSON.stringify(v) : String(v)
  ]);
}
