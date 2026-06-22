<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { onMount } from 'svelte';
  import { goto } from '$app/navigation';
  import Icon from '$lib/Icon.svelte';
  import EmptyState from '$lib/EmptyState.svelte';
  import Skeleton from '$lib/Skeleton.svelte';
  import { toast } from '$lib/toast';
  import { listFlows, createFlow, importFlow, importFlowBundle, type Flow } from '$lib/api';
  import { appHref } from '$lib/paths';

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
    if (busy) return; // Enter fires onsubmit directly, bypassing the disabled button
    error = '';
    busy = true;
    try {
      await createFlow(key, slug, name);
      slug = '';
      name = '';
      await load();
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      busy = false;
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
        await goto(appHref(`/engine/${res.flow_id}`));
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

  onMount(load);
</script>

<main>
  <div class="head">
    <h1>Decision Engine — Flows</h1>
    <button onclick={load}><Icon name="reload" size={15} /> Reload</button>
  </div>

  <form
    class="row"
    onsubmit={(e) => {
      e.preventDefault();
      create();
    }}
  >
    <label>Slug <input bind:value={slug} placeholder="loan-origination" aria-label="slug" /></label>
    <label>Name <input bind:value={name} placeholder="Loan Origination" aria-label="name" /></label>
    <button type="submit" disabled={busy}>{busy ? 'Creating…' : 'Create flow'}</button>
  </form>

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
        disabled={importing || !importText.trim()}
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
                    >{:else}<span class="badge none">not deployed</span>{/if}
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
    max-width: 56rem;
    margin: 2rem auto;
    padding: 0 1.25rem;
  }
  .head {
    display: flex;
    align-items: center;
    justify-content: space-between;
  }
  .row {
    display: flex;
    gap: 0.5rem;
    flex-wrap: wrap;
    margin: 0.6rem 0;
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
    padding: 0.55rem 0.6rem;
    border-bottom: 1px solid var(--border);
    font-size: 0.92rem;
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
