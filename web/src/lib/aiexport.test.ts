// SPDX-License-Identifier: AGPL-3.0-or-later
// @vitest-environment happy-dom
import { describe, it, expect } from 'vitest';
import { buildPageExport, exportFilename } from './aiexport';
import type { PageHelp } from './help/types';

const help: PageHelp = {
  title: 'Decisions',
  summary: 'Every decision the engine has run.',
  capabilities: ['Filter by flow.', 'Open a decision to read its trace.'],
  journeys: [{ name: 'Find a run', steps: ['Set Flow.', 'Click Apply.', 'Open a row.'] }]
};

const calls = [
  { method: 'GET', path: '/v1/me', status: 200 },
  { method: 'GET', path: '/v1/decisions', status: 200 },
  { method: 'GET', path: '/v1/decisions', status: 200 }, // duplicate — deduped
  { method: 'POST', path: '/v1/login', status: 204 }
];

function fixture(): HTMLElement {
  const main = document.createElement('main');
  main.innerHTML = `
    <h1>Decisions</h1>
    <span class="stat">Total <b>12</b></span>
    <span class="stat">Failed <b>2</b></span>
    <h2>History</h2>
    <h3>Filters</h3>
    <table>
      <thead><tr><th>Flow</th><th>Env</th><th>Status</th></tr></thead>
      <tbody>
        <tr><td>loans</td><td>sandbox</td><td>completed</td></tr>
        <tr><td>loans</td><td>production</td><td>failed</td></tr>
        <tr><td>kyc</td><td>sandbox</td><td>completed</td></tr>
        <tr><td>kyc</td><td>sandbox</td><td>suspended</td></tr>
        <tr><td>fraud</td><td>production</td><td>completed</td></tr>
      </tbody>
    </table>`;
  return main;
}

describe('the "Export for AI" document', () => {
  const doc = buildPageExport({
    routeId: '/decisions',
    path: '/decisions',
    help,
    calls,
    main: fixture()
  });

  it('carries the stable section headings', () => {
    expect(doc).toContain('# Decisions — intraktible page export');
    expect(doc).toContain('Route: /decisions (route id `/decisions`)');
    expect(doc).toContain('## What this page is');
    expect(doc).toContain('## Flows, step by step');
    expect(doc).toContain('## Underlying API calls');
    expect(doc).toContain('## Current content');
  });

  it('renders the help entry: summary, capabilities, and numbered journey steps', () => {
    expect(doc).toContain('Every decision the engine has run.');
    expect(doc).toContain('- Filter by flow.');
    expect(doc).toContain('### Find a run');
    expect(doc).toContain('1. Set Flow.');
    expect(doc).toContain('3. Open a row.');
  });

  it('lists the recorded calls deduped in order, and cites the OpenAPI contract', () => {
    const lines = doc.split('\n').filter((l) => l.startsWith('- GET') || l.startsWith('- POST'));
    expect(lines).toEqual([
      '- GET /v1/me → 200',
      '- GET /v1/decisions → 200',
      '- POST /v1/login → 204'
    ]);
    expect(doc).toContain('`/openapi.json`');
    expect(doc).toContain('self-hosted');
  });

  it('summarizes the rendered content: outline, table sample, stat chips', () => {
    expect(doc).toContain('- Decisions');
    expect(doc).toContain('  - History');
    expect(doc).toContain('    - Filters');
    expect(doc).toContain('### Table 1 (5 rows)');
    expect(doc).toContain('| Flow | Env | Status |');
    expect(doc).toContain('| loans | sandbox | completed |');
    expect(doc).toContain('(first 3 of 5 rows)');
    expect(doc).not.toContain('| kyc | sandbox | suspended |'); // 4th row not sampled
    expect(doc).toContain('- Total 12');
    expect(doc).toContain('- Failed 2');
  });

  it('says so when nothing was recorded or rendered', () => {
    const empty = buildPageExport({
      routeId: '/x',
      path: '/x',
      help: { ...help, journeys: undefined },
      calls: [],
      main: null
    });
    expect(empty).toContain('No API calls were recorded on this visit.');
    expect(empty).toContain('(nothing rendered)');
    expect(empty).not.toContain('## Flows, step by step');
  });

  it('captures <dl> key/value pairs, .fact chips, and labelled list items', () => {
    const main = document.createElement('main');
    main.innerHTML = `
      <h1>loans-v3</h1>
      <dl class="fields">
        <dt>environment</dt><dd>production</dd>
        <dt>version</dt><dd>v3</dd>
      </dl>
      <div class="facts">
        <div class="fact"><span class="fact-key">risk_score</span><span class="fact-val">742</span></div>
      </div>
      <ul class="reasons">
        <li><span class="rcode">R01</span> thin file</li>
        <li><span class="rcode">R02</span> high utilization</li>
      </ul>`;
    const doc = buildPageExport({
      routeId: '/decisions/[decisionId]',
      path: '/decisions/abc',
      help,
      calls: [],
      main
    });
    expect(doc).toContain('### Details');
    expect(doc).toContain('- environment: production');
    expect(doc).toContain('- version: v3');
    expect(doc).toContain('- risk_score: 742');
    expect(doc).toContain('### List items');
    expect(doc).toContain('- R01 thin file');
    expect(doc).toContain('- R02 high utilization');
  });

  it('derives a stable download filename from the route id', () => {
    expect(exportFilename('/')).toBe('intraktible-home.ai.md');
    expect(exportFilename('/decisions/[decisionId]')).toBe(
      'intraktible-decisions-decisionid.ai.md'
    );
  });
});
