<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { onMount } from 'svelte';
  import EmptyState from '$lib/EmptyState.svelte';
  import Skeleton from '$lib/Skeleton.svelte';
  import { listAgents, defineAgent, getRunSummary, type Agent, type RunSummary } from '$lib/api';
  import { appHref } from '$lib/paths';
  import { roleAtLeast } from '$lib/roles';
  import { user } from '$lib/session';

  // API calls authenticate via the session cookie (empty key -> no X-Api-Key header).
  const key = '';
  let list = $state<Agent[]>([]);
  let summary = $state<RunSummary | null>(null);
  let error = $state('');
  let loading = $state(true);

  // new-agent form
  let name = $state('');
  let provider = $state('');
  let model = $state('');
  let system = $state('');
  let schema = $state('');
  let tools = $state('');
  let busy = $state(false);
  // The creation form is secondary to the existing-agents table; keep it folded
  // away so the table leads (the form is tall and otherwise pushes it below the
  // fold). Auto-opens when there are no agents yet.
  let defineOpen = $state(false);

  const tokenFmt = new Intl.NumberFormat('en-US');
  const usdFmt = new Intl.NumberFormat('en-US', {
    style: 'currency',
    currency: 'USD',
    maximumFractionDigits: 4
  });

  async function load() {
    loading = true;
    error = '';
    try {
      [list, summary] = await Promise.all([listAgents(key), getRunSummary(key)]);
      if (list.length === 0) defineOpen = true; // no agents yet → surface the form
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
      const body: {
        name: string;
        provider?: string;
        model?: string;
        system?: string;
        schema?: unknown;
        tools?: string[];
      } = { name: name.trim() };
      if (provider.trim()) body.provider = provider.trim();
      if (model.trim()) body.model = model.trim();
      if (system.trim()) body.system = system.trim();
      // A structured-output schema, if given, must be valid JSON (fail loudly).
      if (schema.trim()) body.schema = JSON.parse(schema);
      const tl = tools
        .split(',')
        .map((t) => t.trim())
        .filter(Boolean);
      if (tl.length > 0) body.tools = tl;
      await defineAgent(key, body);
      name = '';
      provider = '';
      model = '';
      system = '';
      schema = '';
      tools = '';
      await load();
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      busy = false;
    }
  }

  onMount(load);
</script>

<main>
  <h1>Agents</h1>
  <div class="row">
    <button onclick={load}>Reload</button>
  </div>

  {#if error}<p class="err">{error}</p>{/if}

  {#if summary}
    <div class="summary" aria-label="run summary">
      <span class="stat">Runs <b>{summary.total}</b></span>
      <span class="stat">Completed <b>{summary.completed}</b></span>
      <span class="stat fail">Failed <b>{summary.failed}</b></span>
      <span class="stat"
        >Tokens <b>{tokenFmt.format(summary.prompt_tokens + summary.completion_tokens)}</b></span
      >
      {#if summary.priced}
        <span class="stat">Cost <b>{usdFmt.format(summary.total_cost_usd)}</b></span>
      {/if}
    </div>
  {/if}

  {#if loading}
    <Skeleton rows={4} />
  {:else if list.length === 0}
    <EmptyState
      icon="agents"
      title="No agents defined"
      hint="Define an agent below — a system prompt over the AI provider, optionally with tools and a structured-output schema — then run and monitor it here."
    />
  {:else}
    <div class="table-wrap">
      <table>
        <thead>
          <tr><th>Name</th><th>Model</th><th>Capabilities</th><th>Runs</th></tr>
        </thead>
        <tbody>
          {#each list as a (a.name)}
            <tr>
              <td><a href={appHref(`/agents/${a.name}`)}>{a.name}</a></td>
              <td>{a.model || '—'}</td>
              <td>
                {#if a.schema}<span class="badge">structured</span>{/if}
                {#if a.tools && a.tools.length > 0}<span class="badge"
                    >{a.tools.length} tool{a.tools.length === 1 ? '' : 's'}</span
                  >{/if}
                {#if !a.schema && !(a.tools && a.tools.length > 0)}<span class="muted">—</span>{/if}
              </td>
              <td>{a.runs}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}

  <details class="define-disclosure" bind:open={defineOpen}>
    <summary>+ Define agent</summary>
    <form
      class="define"
      onsubmit={(e) => {
        e.preventDefault();
        create();
      }}
    >
      <p class="grouphdr">Identity</p>
      <div class="row">
        <label
          >Name (required) <input
            bind:value={name}
            placeholder="sanctions-screener"
            aria-label="agent name"
            required
          /></label
        >
        <label
          >Provider <input
            bind:value={provider}
            placeholder="optional"
            aria-label="provider"
          /></label
        >
        <label>Model <input bind:value={model} placeholder="optional" aria-label="model" /></label>
      </div>
      <p class="grouphdr">Behavior</p>
      <label class="field"
        >System prompt
        <input bind:value={system} placeholder="optional" aria-label="system prompt" /></label
      >
      <label class="field"
        >Tools <input
          bind:value={tools}
          placeholder="comma-separated (optional)"
          aria-label="tools"
        /></label
      >
      <label class="field"
        >Output schema (optional)
        <textarea
          bind:value={schema}
          placeholder={'JSON Schema, e.g. {"type":"object","required":["risk"]}'}
          aria-label="output schema"
          rows="3"
        ></textarea></label
      >
      <div class="row">
        <button
          type="submit"
          disabled={busy || !roleAtLeast($user?.role, 'editor')}
          title={!roleAtLeast($user?.role, 'editor') ? 'Requires the editor role' : undefined}
          >{busy ? 'Saving…' : 'Define agent'}</button
        >
      </div>
    </form>
  </details>
</main>

<style>
  main {
    max-width: 52rem;
    margin: 2rem auto;
    padding: 0 1.25rem;
  }
  .row {
    display: flex;
    gap: 0.5rem;
    flex-wrap: wrap;
    margin: 0.6rem 0;
    align-items: center;
  }
  .define-disclosure {
    margin: 1.25rem 0 0.6rem;
    border: 1px solid var(--border);
    border-radius: 0.5rem;
    background: var(--surface-2);
  }
  .define-disclosure > summary {
    cursor: pointer;
    padding: 0.6rem 0.8rem;
    font-weight: 600;
    color: var(--accent-ink, var(--accent));
    list-style: none;
    user-select: none;
  }
  .define-disclosure > summary::-webkit-details-marker {
    display: none;
  }
  .define-disclosure[open] > summary {
    border-bottom: 1px solid var(--border);
  }
  .define {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
    margin: 0;
    padding: 0.6rem 0.8rem 0.8rem;
  }
  .define .row {
    margin: 0;
  }
  input,
  button,
  textarea {
    font: inherit;
    padding: 0.4rem 0.6rem;
  }
  textarea {
    width: 100%;
    box-sizing: border-box;
    resize: vertical;
  }
  .grouphdr {
    margin: 0.5rem 0 0.1rem;
    font-size: 0.72rem;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--fg-subtle);
  }
  label.field {
    display: block;
    margin: 0;
    font-size: 0.78rem;
    color: var(--fg-subtle);
  }
  label.field input,
  label.field textarea {
    display: block;
    width: 100%;
    box-sizing: border-box;
    margin-top: 0.15rem;
    color: var(--fg);
  }
  .define .row label {
    display: inline-flex;
    flex-direction: column;
    gap: 0.15rem;
    font-size: 0.78rem;
    color: var(--fg-subtle);
  }
  .badge {
    display: inline-block;
    padding: 0.05rem 0.45rem;
    margin-right: 0.25rem;
    border-radius: 999px;
    font-size: 0.75rem;
    background: var(--surface-2);
    color: var(--fg-muted);
  }
  table {
    border-collapse: collapse;
    width: 100%;
  }
  th,
  td {
    text-align: left;
    padding: 0.4rem 0.6rem;
    border-bottom: 1px solid var(--border);
  }
  .err {
    color: var(--danger);
  }
  .muted {
    color: var(--fg-subtle);
  }
  .summary {
    display: flex;
    gap: 1rem;
    flex-wrap: wrap;
    margin: 0.6rem 0 1rem;
    padding: 0.6rem 0.8rem;
    background: var(--surface-2);
    border-radius: 6px;
  }
  .stat {
    color: var(--fg-muted);
    font-size: 0.9rem;
  }
  .stat b {
    color: var(--fg);
    font-size: 1.05rem;
  }
  .stat.fail b {
    color: var(--danger);
  }
</style>
