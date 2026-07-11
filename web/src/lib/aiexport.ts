// SPDX-License-Identifier: AGPL-3.0-or-later
// Builds the per-page "Export for AI" document: a semi-structured markdown file
// (stable headings) that lets an AI agent understand the page — what it IS (from
// the help registry), the API calls behind it (from the recorder), and a generic
// summary of what it currently shows (headings, tables, stat chips — extracted
// from the rendered main, no per-page code). Offered on every page from the
// guide panel and the header's copy-for-AI button.
import { get } from 'svelte/store';
import type { PageHelp } from './help/types';
import { helpFor } from './help/registry';
import { recordedCalls, type RecordedCall } from './recorder';

// How many leading table rows the "Current content" section samples.
const SAMPLE_ROWS = 3;

export interface PageExportInput {
  routeId: string;
  path: string; // the concrete pathname, e.g. /decisions
  help: PageHelp;
  calls: RecordedCall[];
  main: ParentNode | null; // the rendered page container (null = nothing rendered)
}

// buildPageExport is the pure builder — everything it reads comes in as input,
// so it is unit-testable against a fake help entry, calls, and a DOM fixture.
export function buildPageExport(input: PageExportInput): string {
  const { routeId, path, help, calls, main } = input;
  const out: string[] = [
    `# ${help.title} — intraktible page export`,
    '',
    `Route: ${path} (route id \`${routeId}\`)`,
    '',
    '## What this page is',
    '',
    help.summary,
    '',
    'You can:',
    ...help.capabilities.map((c) => `- ${c}`)
  ];

  if (help.journeys && help.journeys.length > 0) {
    out.push('', '## Flows, step by step');
    for (const j of help.journeys) {
      out.push('', `### ${j.name}`, '', ...j.steps.map((s, i) => `${i + 1}. ${s}`));
    }
  }

  out.push('', '## Underlying API calls', '');
  const deduped = dedupe(calls);
  if (deduped.length === 0) {
    out.push('No API calls were recorded on this visit.');
  } else {
    out.push(
      'Observed on this visit (in order, deduped):',
      '',
      ...deduped.map((c) => `- ${c.method} ${c.path} → ${c.status}`)
    );
  }
  out.push(
    '',
    'The same REST API serves self-hosted deployments; the OpenAPI 3.1 contract is served at `/openapi.json` (reference UI at `/docs`).'
  );

  out.push('', '## Current content', '', ...extractContent(main));
  return out.join('\n') + '\n';
}

// buildCurrentPageExport assembles the export for the live page. It throws when
// the route has no help entry — impossible in practice (coverage-tested), so a
// miss is a bug that must surface, not a silently empty document.
export function buildCurrentPageExport(routeId: string | null | undefined, path: string): string {
  const help = helpFor(routeId);
  if (!routeId || !help) throw new Error(`Export for AI: no help entry for route "${routeId}"`);
  return buildPageExport({
    routeId,
    path,
    help,
    calls: get(recordedCalls),
    main: document.getElementById('main')
  });
}

// exportFilename derives a stable download name from the route id, e.g.
// "/decisions/[decisionId]" → "intraktible-decisions-decisionid.ai.md".
export function exportFilename(routeId: string): string {
  const slug = routeId.replace(/[[\]]/g, '').split('/').filter(Boolean).join('-').toLowerCase();
  return `intraktible-${slug || 'home'}.ai.md`;
}

// dedupe keeps the first occurrence of each method+path, preserving call order.
function dedupe(calls: RecordedCall[]): RecordedCall[] {
  const seen = new Set<string>();
  const out: RecordedCall[] = [];
  for (const c of calls) {
    const key = `${c.method} ${c.path}`;
    if (seen.has(key)) continue;
    seen.add(key);
    out.push(c);
  }
  return out;
}

// extractContent summarizes the rendered page generically: the h1/h2/h3 outline,
// each table as column headers + row count + the first rows, and stat-chip text.
function extractContent(main: ParentNode | null): string[] {
  if (!main) return ['(nothing rendered)'];
  const out: string[] = [];

  const headings = [...main.querySelectorAll('h1, h2, h3')];
  if (headings.length > 0) {
    out.push('### Outline', '');
    for (const h of headings) {
      const level = Number(h.tagName[1]);
      out.push(`${'  '.repeat(level - 1)}- ${text(h)}`);
    }
  }

  const tables = [...main.querySelectorAll('table')];
  tables.forEach((table, i) => {
    const headers = [...table.querySelectorAll('thead th, thead td')].map(text);
    const rows = [...table.querySelectorAll('tbody tr')];
    out.push('', `### Table ${i + 1} (${rows.length} rows)`, '');
    if (headers.length > 0) {
      out.push(`| ${headers.map(cell).join(' | ')} |`, `|${' --- |'.repeat(headers.length)}`);
    }
    for (const row of rows.slice(0, SAMPLE_ROWS)) {
      const cells = [...row.querySelectorAll('th, td')].map(text);
      out.push(`| ${cells.map(cell).join(' | ')} |`);
    }
    if (rows.length > SAMPLE_ROWS) out.push(`(first ${SAMPLE_ROWS} of ${rows.length} rows)`);
  });

  const stats = [...main.querySelectorAll('.stat')].map(text).filter(Boolean);
  if (stats.length > 0) {
    out.push('', '### Stats', '', ...stats.map((s) => `- ${s}`));
  }

  // Detail pages render their substance as <dl> key/value grids (a decision's fields,
  // an entity's attributes, a case's meta) and labelled fact chips (.fact-key/.fact-val)
  // — neither a heading, a table, nor a .stat, so they were previously dropped.
  const details: string[] = [];
  for (const dl of main.querySelectorAll('dl')) {
    let dt: string | null = null;
    for (const el of [...dl.children]) {
      if (el.tagName === 'DT') dt = text(el);
      else if (el.tagName === 'DD' && dt !== null) {
        details.push(`- ${dt}: ${text(el)}`);
        dt = null;
      }
    }
  }
  for (const f of main.querySelectorAll('.fact')) {
    const k = f.querySelector('.fact-key');
    const v = f.querySelector('.fact-val');
    if (k && v) details.push(`- ${text(k)}: ${text(v)}`);
  }
  if (details.length > 0) out.push('', '### Details', '', ...details);

  // Labelled list items outside tables carry the rest (reason codes, notes, timelines,
  // counterfactual flips), so a decision's disposition rationale is captured too.
  const listItems: string[] = [];
  for (const list of main.querySelectorAll('ul, ol')) {
    if (list.closest('table')) continue;
    for (const li of [...list.children].filter((c) => c.tagName === 'LI').slice(0, SAMPLE_ROWS)) {
      const t = text(li);
      if (t) listItems.push(`- ${t}`);
    }
  }
  if (listItems.length > 0) out.push('', '### List items', '', ...listItems);

  if (out.length === 0) return ['(no headings, tables, stats, or details rendered)'];
  return out;
}

function text(el: Element): string {
  return (el.textContent ?? '').replace(/\s+/g, ' ').trim();
}

function cell(s: string): string {
  return s.replace(/\|/g, '\\|');
}
