<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { onMount } from 'svelte';
  import { goto } from '$app/navigation';
  import Icon from '$lib/Icon.svelte';
  import EmptyState from '$lib/EmptyState.svelte';
  import Skeleton from '$lib/Skeleton.svelte';
  import { toast } from '$lib/toast';
  import {
    listFlows,
    createFlow,
    importFlow,
    importFlowBundle,
    copilotGenerate,
    type Flow
  } from '$lib/api';
  import { TEMPLATES, type FlowTemplate } from '$lib/templates';
  import { nodeTypeLabel, isNodeType } from '$lib/nodevis';
  import { appHref } from '$lib/paths';
  import { roleAtLeast } from '$lib/roles';
  import { user } from '$lib/session';

  // API calls authenticate via the session cookie (empty key -> no X-Api-Key header).
  const key = '';
  let flows = $state<Flow[]>([]);
  let slug = $state('');
  let name = $state('');
  let error = $state('');
  let busy = $state(false);
  let loading = $state(true);

  // Search + a render cap so a tenant with hundreds of flows gets a usable page
  // (a finite, filterable list) instead of one giant unbounded table.
  let query = $state('');
  const RENDER_CAP = 100;
  const filtered = $derived(
    query.trim()
      ? flows.filter((f) => {
          const q = query.trim().toLowerCase();
          return (
            f.name.toLowerCase().includes(q) ||
            f.slug.toLowerCase().includes(q) ||
            f.flow_id.toLowerCase().includes(q)
          );
        })
      : flows
  );
  const visible = $derived(filtered.slice(0, RENDER_CAP));

  async function load() {
    loading = true;
    error = '';
    try {
      flows = await listFlows(key);
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      loading = false;
    }
  }

  async function create() {
    // Enter fires onsubmit directly, bypassing the disabled button — re-check the
    // same guards (busy and a non-empty slug) so a blank-slug flow can't slip through.
    if (busy || !slug.trim()) return;
    error = '';
    busy = true;
    try {
      const created = await createFlow(key, slug, name);
      const label = name.trim() || slug.trim();
      slug = '';
      name = '';
      toast.success(`Created ${label}`);
      // Fire the navigation without awaiting: awaiting it can race the page's own reload
      // in this static SPA and leave the button stuck on "Creating…" if the goto promise
      // doesn't settle. The button clears immediately; the route advances when it can.
      void goto(appHref(`/engine/${created.flow_id}`));
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      busy = false;
    }
  }

  // --- Start from a template: import the curated flow-as-code, then open it ---
  let templating = $state('');
  // Avoid a slug collision with an existing flow (e.g. importing the same starter twice)
  // by suffixing, so "Use template" always lands on a fresh, editable flow.
  function uniqueSlug(base: string): string {
    const taken = new Set(flows.map((f) => f.slug));
    if (!taken.has(base)) return base;
    let n = 2;
    while (taken.has(`${base}-${n}`)) n += 1;
    return `${base}-${n}`;
  }
  async function pickTemplate(t: FlowTemplate) {
    if (templating || !roleAtLeast($user?.role, 'editor')) return;
    templating = t.id;
    error = '';
    try {
      const res = await importFlow(key, { ...t.doc, slug: uniqueSlug(t.doc.slug) });
      toast.success(`Created ${res.slug} from "${t.name}"`);
      void goto(appHref(`/engine/${res.flow_id}`));
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      templating = '';
    }
  }

  // --- Draft a flow with AI: a natural-language requirement -> a real, editable graph ---
  // (POST /v1/copilot/generate; the backend asks the configured LLM for a graph and runs
  // it through ValidateGraph, so what comes back always imports.)
  type DraftNode = { id: string; type: string; name?: string };
  const COPILOT_EXAMPLES = [
    'Approve loans under $50k when DTI is below 40% and there are no recent delinquencies; refer the rest to a human.',
    'Screen a payment for fraud using transaction velocity and a new-device signal; block the high-risk ones.',
    'Onboard a business customer: run sanctions and PEP screening, send any hit to manual review.'
  ];
  let copilotPrompt = $state('');
  let drafting = $state(false);
  let draftGraph = $state<{ nodes?: DraftNode[]; edges?: unknown[] } | null>(null);
  const draftNodes = $derived(draftGraph?.nodes ?? []);
  async function draft() {
    if (drafting || !copilotPrompt.trim() || !roleAtLeast($user?.role, 'editor')) return;
    drafting = true;
    error = '';
    draftGraph = null;
    try {
      const graph = (await copilotGenerate(key, copilotPrompt.trim())) as {
        nodes?: DraftNode[];
        edges?: unknown[];
      };
      draftGraph = graph;
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      drafting = false;
    }
  }
  async function openDraft() {
    if (!draftGraph) return;
    drafting = true;
    try {
      const res = await importFlow(key, {
        slug: uniqueSlug('ai-draft'),
        name: 'AI draft',
        graph: draftGraph
      });
      toast.success(`Drafted ${res.slug} — opening in the builder`);
      void goto(appHref(`/engine/${res.flow_id}`));
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      drafting = false;
    }
  }

  // --- Flow-as-code import (paste or upload one flow, or a { flows: [...] } bundle) ---
  let importText = $state('');
  let importing = $state(false);
  const isBundle = (d: unknown): d is { flows: unknown[] } =>
    typeof d === 'object' && d !== null && Array.isArray((d as { flows?: unknown }).flows);
  async function runImport() {
    error = '';
    let doc: unknown;
    try {
      doc = JSON.parse(importText);
    } catch {
      error = 'Import document is not valid JSON';
      return;
    }
    importing = true;
    try {
      if (isBundle(doc)) {
        const res = await importFlowBundle(key, doc);
        const parts = [`${res.published} published`, `${res.unchanged} unchanged`];
        if (res.failed) parts.push(`${res.failed} failed`);
        toast.success(`Bundle: ${parts.join(', ')}`);
        importText = '';
        await load();
      } else {
        const res = await importFlow(key, doc);
        toast.success(
          res.created
            ? `Created ${res.slug} (v${res.version})`
            : res.published
              ? `Updated ${res.slug} → v${res.version}`
              : `${res.slug} already at v${res.version} — no change`
        );
        importText = '';
        await load();
        void goto(appHref(`/engine/${res.flow_id}`));
      }
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      importing = false;
    }
  }
  async function onImportFile(e: Event) {
    const file = (e.currentTarget as HTMLInputElement).files?.[0];
    if (file) {
      importText = await file.text();
    }
  }

  // liveVersion reads a flow's deployed version for an environment (via entries,
  // not a computed index, to stay clear of detect-object-injection).
  function liveVersion(flow: Flow, env: string): number | undefined {
    const found = Object.entries(flow.deployments ?? {}).find(([e]) => e === env);
    return found?.[1]?.version;
  }

  // Header KPIs over the loaded flows: how many ship to production vs. sit
  // undeployed there — the at-a-glance health of the whole fleet.
  const deployedToProd = $derived(flows.filter((f) => liveVersion(f, 'production') != null).length);
  const notDeployedToProd = $derived(flows.length - deployedToProd);

  // A flow's production deploy lags its latest published version → an editor has
  // shipped work that production hasn't picked up yet (worth a nudge in the row).
  function prodLagsLatest(flow: Flow): boolean {
    const prod = liveVersion(flow, 'production');
    return prod != null && prod < flow.latest;
  }

  onMount(load);
</script>

<main>
  <div class="head">
    <div class="head-titles">
      <h1>Flows</h1>
      <p class="subtitle">
        Author decision flows on the canvas, publish versions, and deploy them across environments.
      </p>
    </div>
    <button onclick={load}><Icon name="reload" size={15} /> Reload</button>
  </div>

  {#if !loading && flows.length > 0}
    <div class="kpis" data-testid="flow-kpis">
      <span class="kpi"><b>{flows.length}</b> flow{flows.length === 1 ? '' : 's'}</span>
      <span class="kpi ok"><b>{deployedToProd}</b> in production</span>
      <span class="kpi muted"><b>{notDeployedToProd}</b> not in production</span>
    </div>
  {/if}

  <form
    class="row"
    onsubmit={(e) => {
      e.preventDefault();
      create();
    }}
  >
    <label>Slug <input bind:value={slug} placeholder="loan-origination" aria-label="slug" /></label>
    <label>Name <input bind:value={name} placeholder="Loan Origination" aria-label="name" /></label>
    <button
      type="submit"
      disabled={busy || !slug.trim() || !roleAtLeast($user?.role, 'editor')}
      title={!roleAtLeast($user?.role, 'editor')
        ? 'Requires the editor role'
        : !slug.trim()
          ? 'A slug is required'
          : undefined}>{busy ? 'Creating…' : 'Create flow'}</button
    >
  </form>

  <details class="copilot" data-testid="copilot-panel" open>
    <summary><Icon name="ai" size={14} /> Draft a flow with AI</summary>
    <p class="muted">
      Describe the decision in plain English — the copilot drafts a real, editable flow you can
      refine, publish, and deploy.
    </p>
    <div class="copilot-examples">
      {#each COPILOT_EXAMPLES as ex (ex)}
        <button type="button" class="ex" onclick={() => (copilotPrompt = ex)}>{ex}</button>
      {/each}
    </div>
    <textarea
      bind:value={copilotPrompt}
      aria-label="describe the flow"
      placeholder="Approve loans under $50k when DTI is below 40% and there are no recent delinquencies; refer the rest to a human."
      rows="3"
    ></textarea>
    <div class="copilot-actions">
      <button
        class="primary"
        onclick={draft}
        disabled={drafting || !copilotPrompt.trim() || !roleAtLeast($user?.role, 'editor')}
        title={!roleAtLeast($user?.role, 'editor') ? 'Requires the editor role' : undefined}
      >
        {drafting ? 'Drafting…' : 'Generate flow'}
      </button>
    </div>
    {#if draftGraph}
      <div class="draft" data-testid="copilot-draft">
        <b>Drafted flow — {draftNodes.length} node{draftNodes.length === 1 ? '' : 's'}</b>
        <ol class="draft-nodes">
          {#each draftNodes as n (n.id)}
            <li>
              <span class="chip">{isNodeType(n.type) ? nodeTypeLabel(n.type) : n.type}</span>
              {n.name ?? ''}
            </li>
          {/each}
        </ol>
        <div class="copilot-actions">
          <button class="primary" onclick={openDraft} data-testid="open-draft"
            >Open in builder →</button
          >
          <button onclick={() => (draftGraph = null)}>Discard</button>
        </div>
      </div>
    {/if}
  </details>

  <details class="templates" data-testid="template-gallery">
    <summary><Icon name="plus" size={14} /> New from template</summary>
    <p class="muted">
      Start from a curated flow that already wires the right node types for a use case — it's
      imported as a new, fully-editable flow and opens in the builder.
    </p>
    <div class="tmpl-grid">
      {#each TEMPLATES as t (t.id)}
        <div class="tmpl-card">
          <h2>{t.name}</h2>
          <p class="tmpl-purpose">{t.purpose}</p>
          <div class="tmpl-chips">
            {#each t.nodeTypes as nt (nt)}<span class="chip">{nt}</span>{/each}
          </div>
          <button
            class="tmpl-use"
            onclick={() => pickTemplate(t)}
            disabled={templating !== '' || !roleAtLeast($user?.role, 'editor')}
            title={!roleAtLeast($user?.role, 'editor') ? 'Requires the editor role' : undefined}
            data-testid={`use-template-${t.id}`}
          >
            {templating === t.id ? 'Creating…' : 'Use template'}
          </button>
        </div>
      {/each}
    </div>
  </details>

  <details class="import" data-testid="import-flow">
    <summary><Icon name="upload" size={14} /> Import flow (as code)</summary>
    <p class="muted">
      Paste or upload a flow exported as JSON (the builder's Export → JSON), or a bundle
      <code>{'{ "flows": [ … ] }'}</code> of several. Each flow is created if its slug is new, otherwise
      a new version is published; re-importing identical content is a no-op.
    </p>
    <textarea
      bind:value={importText}
      aria-label="flow document"
      placeholder={'{ "slug": "…", "name": "…", "graph": { … } }'}
      rows="6"
    ></textarea>
    <div class="import-actions">
      <input
        type="file"
        accept="application/json,.json"
        aria-label="import file"
        onchange={onImportFile}
      />
      <button
        onclick={runImport}
        disabled={importing || !importText.trim() || !roleAtLeast($user?.role, 'editor')}
        title={!roleAtLeast($user?.role, 'editor') ? 'Requires the editor role' : undefined}
        data-testid="import-submit"
      >
        {importing ? 'Importing…' : 'Import'}
      </button>
    </div>
  </details>

  {#if error}<p class="err">{error}</p>{/if}

  {#if loading}
    <Skeleton rows={4} />
  {:else if flows.length === 0}
    <EmptyState
      icon="engine"
      title="No flows yet"
      hint="Create your first decision flow above, then open it to build the graph on the canvas, publish a version, and deploy."
    />
  {:else}
    <div class="listhead">
      <input
        bind:value={query}
        placeholder="Search flows by name or slug…"
        aria-label="search flows"
        class="search"
      />
      <span class="muted count">
        {#if filtered.length > visible.length}
          showing {visible.length} of {filtered.length} — refine your search
        {:else}
          {filtered.length} of {flows.length} flow{flows.length === 1 ? '' : 's'}
        {/if}
      </span>
    </div>
    {#if filtered.length === 0}
      <p class="muted">No flows match “{query}”.</p>
    {:else}
      <div class="table-wrap">
        <table>
          <thead>
            <tr><th>Name</th><th>Slug</th><th>Latest</th><th>Sandbox</th><th>Production</th></tr>
          </thead>
          <tbody>
            {#each visible as f (f.flow_id)}
              <tr>
                <td><a href={appHref(`/engine/${f.flow_id}`)}>{f.name}</a></td>
                <td><code>{f.slug}</code></td>
                <td>v{f.latest}</td>
                <td>
                  {#if liveVersion(f, 'sandbox')}<span class="badge live"
                      >v{liveVersion(f, 'sandbox')}</span
                    >{:else}<span class="badge none">not deployed</span>{/if}
                </td>
                <td>
                  {#if liveVersion(f, 'production')}<span class="badge live"
                      >v{liveVersion(f, 'production')}</span
                    >{#if prodLagsLatest(f)}<span
                        class="lag"
                        title={`Production runs v${liveVersion(f, 'production')} but v${f.latest} is published — deploy to catch up`}
                        >↑ v{f.latest} ready</span
                      >{/if}{:else}<span class="badge none">not deployed</span>{/if}
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  {/if}
</main>

<style>
  main {
    max-width: 72rem;
    margin: 2rem auto;
    padding: 0 1.25rem;
  }
  .head {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 1rem;
  }
  .head-titles h1 {
    margin: 0;
  }
  .subtitle {
    margin: 0.15rem 0 0;
    color: var(--fg-muted);
    font-size: 0.9rem;
  }
  .kpis {
    display: flex;
    flex-wrap: wrap;
    gap: 0.6rem;
    margin: 0.85rem 0 0.25rem;
  }
  .kpi {
    padding: 0.4rem 0.8rem;
    border: 1px solid var(--border);
    border-radius: 10px;
    background: var(--surface);
    font-size: 0.9rem;
    color: var(--fg-muted);
  }
  .kpi b {
    color: var(--fg);
    font-size: 1.05rem;
    margin-right: 0.2rem;
  }
  .kpi.ok b {
    color: var(--ok);
  }
  .row {
    display: flex;
    gap: 0.5rem;
    flex-wrap: wrap;
    margin: 0.6rem 0;
  }
  .copilot {
    border: 1px solid color-mix(in srgb, var(--accent) 35%, var(--border));
    border-radius: 10px;
    padding: 0.6rem 0.9rem;
    margin: 0.6rem 0;
    background: color-mix(in srgb, var(--accent) 5%, transparent);
  }
  .copilot > summary {
    cursor: pointer;
    font-weight: 600;
    display: flex;
    align-items: center;
    gap: 0.4rem;
    list-style: none;
  }
  .copilot > summary::-webkit-details-marker {
    display: none;
  }
  .copilot-examples {
    display: flex;
    flex-wrap: wrap;
    gap: 0.4rem;
    margin: 0.5rem 0;
  }
  .copilot-examples .ex {
    text-align: left;
    font-size: 0.78rem;
    padding: 0.3rem 0.55rem;
    border-radius: 8px;
    background: var(--surface-2);
    border: 1px solid var(--border);
    color: var(--fg-muted);
    cursor: pointer;
    max-width: 22rem;
  }
  .copilot-examples .ex:hover {
    color: var(--fg);
    border-color: var(--accent);
  }
  .copilot textarea {
    width: 100%;
    box-sizing: border-box;
    font: inherit;
    font-size: 0.88rem;
    padding: 0.5rem;
    border: 1px solid var(--border);
    border-radius: 8px;
    background: var(--surface);
    color: inherit;
    resize: vertical;
  }
  .copilot-actions {
    display: flex;
    gap: 0.5rem;
    margin-top: 0.5rem;
    align-items: center;
  }
  .draft {
    margin-top: 0.7rem;
    padding: 0.6rem 0.7rem;
    border: 1px solid var(--border);
    border-radius: 8px;
    background: var(--surface-2);
  }
  .draft-nodes {
    margin: 0.4rem 0 0;
    padding-left: 1.1rem;
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
    font-size: 0.84rem;
  }
  .draft-nodes .chip {
    margin-right: 0.3rem;
  }
  .templates {
    border: 1px solid var(--border);
    border-radius: 10px;
    padding: 0.6rem 0.9rem;
    margin: 0.6rem 0;
  }
  .templates > summary {
    cursor: pointer;
    font-weight: 600;
    display: flex;
    align-items: center;
    gap: 0.4rem;
    list-style: none;
  }
  .templates > summary::-webkit-details-marker {
    display: none;
  }
  .tmpl-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(15rem, 1fr));
    gap: 0.7rem;
    margin-top: 0.7rem;
  }
  .tmpl-card {
    display: flex;
    flex-direction: column;
    gap: 0.4rem;
    border: 1px solid var(--border);
    border-radius: 8px;
    padding: 0.7rem 0.8rem;
    background: var(--surface-2);
  }
  .tmpl-card h2 {
    margin: 0;
    font-size: 0.95rem;
  }
  .tmpl-purpose {
    margin: 0;
    font-size: 0.82rem;
    color: var(--fg-muted);
    flex: 1;
  }
  .tmpl-chips {
    display: flex;
    flex-wrap: wrap;
    gap: 0.3rem;
  }
  .chip {
    font-size: 0.7rem;
    padding: 0.1rem 0.4rem;
    border-radius: 999px;
    background: var(--surface);
    border: 1px solid var(--border);
    color: var(--fg-muted);
  }
  .tmpl-use {
    align-self: flex-start;
    margin-top: 0.2rem;
  }
  .import {
    border: 1px solid var(--border);
    border-radius: 10px;
    padding: 0.6rem 0.9rem;
    margin: 0.6rem 0;
  }
  .import summary {
    cursor: pointer;
    font-weight: 600;
    display: flex;
    align-items: center;
    gap: 0.4rem;
  }
  .import textarea {
    width: 100%;
    box-sizing: border-box;
    font-family: var(--mono, monospace);
    font-size: 0.82rem;
    padding: 0.5rem;
    border: 1px solid var(--border);
    border-radius: 8px;
    background: var(--surface-2);
    color: inherit;
    resize: vertical;
  }
  .import-actions {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.5rem;
    margin-top: 0.5rem;
  }
  input,
  button {
    font: inherit;
    padding: 0.4rem 0.6rem;
  }
  table {
    width: 100%;
    border-collapse: collapse;
    margin-top: 0.5rem;
  }
  th {
    text-align: left;
    font-size: 0.78rem;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--fg-subtle);
    padding: 0.5rem 0.6rem;
    border-bottom: 1px solid var(--border);
  }
  td {
    padding: 0.7rem 0.6rem;
    border-bottom: 1px solid var(--border);
    font-size: 0.92rem;
  }
  tbody tr {
    transition: background 0.12s ease;
  }
  tbody tr:hover {
    background: var(--surface-2);
  }
  tbody tr:hover td:first-child a {
    text-decoration: underline;
  }
  .lag {
    margin-left: 0.4rem;
    padding: 0.05rem 0.45rem;
    border-radius: 999px;
    font-size: 0.72rem;
    font-weight: 600;
    background: color-mix(in srgb, var(--warn) 18%, transparent);
    color: var(--warn);
    cursor: help;
  }
  code {
    background: var(--surface-2);
    padding: 0 0.3rem;
    border-radius: 0.3rem;
  }
  .badge {
    display: inline-block;
    padding: 0.1rem 0.5rem;
    border-radius: 999px;
    font-size: 0.78rem;
    background: var(--surface-2);
    color: var(--fg-muted);
  }
  .badge.live {
    background: color-mix(in srgb, var(--ok) 18%, transparent);
    color: var(--ok);
  }
  .badge.none {
    color: var(--fg-subtle);
    font-style: italic;
  }
  .row label {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    margin: 0;
    color: var(--fg-subtle);
    font-size: 0.85rem;
  }
  .err {
    color: var(--danger);
  }
  .muted {
    color: var(--fg-subtle);
  }
  .listhead {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.75rem;
    margin-top: 0.75rem;
    flex-wrap: wrap;
  }
  .search {
    flex: 1 1 18rem;
    min-width: 0;
  }
  .count {
    font-size: 0.82rem;
    white-space: nowrap;
  }
</style>
