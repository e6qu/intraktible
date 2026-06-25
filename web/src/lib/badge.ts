// SPDX-License-Identifier: AGPL-3.0-or-later
// Tone mapping for the shared <Badge> — one source of truth so a status reads with
// the same colour everywhere (decision/case status, disposition, MRM coverage, key
// lifecycle). Maps are used (not object indexing) to keep the object-injection lint
// happy; an unknown value falls back to a neutral tone.
export type Tone = 'ok' | 'warn' | 'danger' | 'info' | 'neutral';
export type BadgeTone = Tone; // alias used by callers that import the tone type

function lookup(m: Map<string, Tone>, v: string | undefined): Tone {
  return (v && m.get(v)) || 'neutral';
}

const STATUS = new Map<string, Tone>([
  ['completed', 'ok'],
  ['failed', 'danger'],
  ['started', 'info'],
  ['suspended', 'warn']
]);
export const statusTone = (s: string | undefined): Tone => lookup(STATUS, s);

const DISPOSITION = new Map<string, Tone>([
  ['approve', 'ok'],
  ['decline', 'danger'],
  ['refer', 'warn'],
  ['review', 'warn']
]);
export const dispositionTone = (d: string | undefined): Tone => lookup(DISPOSITION, d);

const COVERAGE = new Map<string, Tone>([
  ['tested', 'ok'],
  ['failing', 'danger'],
  ['none', 'neutral']
]);
export const coverageTone = (c: string | undefined): Tone => lookup(COVERAGE, c);

const CASE_STATUS = new Map<string, Tone>([
  ['needs_review', 'warn'],
  ['in_progress', 'info'],
  ['completed', 'ok']
]);
export const caseStatusTone = (s: string | undefined): Tone => lookup(CASE_STATUS, s);

const LIFECYCLE = new Map<string, Tone>([
  ['active', 'ok'],
  ['revoked', 'neutral'],
  ['expired', 'warn'],
  ['pending', 'warn'],
  ['approved', 'ok'],
  ['rejected', 'danger']
]);
export const lifecycleTone = (s: string | undefined): Tone => lookup(LIFECYCLE, s);

const SLA = new Map<string, Tone>([
  ['on_track', 'ok'],
  ['due_soon', 'warn'],
  ['overdue', 'danger']
]);
export const slaTone = (s: string | undefined): Tone => lookup(SLA, s);
