<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { onMount } from 'svelte';
  import { listAgents, defineAgent, getRunSummary, type Agent, type RunSummary } from '$lib/api';

  // API calls authenticate via the session cookie (empty key -> no X-Api-Key header).
  const key = '';
  let list = $state<Agent[]>([]);
  let summary = $state<RunSummary | null>(null);
  let error = $state('');

  // new-agent form
  let name = $state('');
  let model = $state('');
  let system = $state('');

  async function load() {
    error = '';
    try {
      [list, summary] = await Promise.all([listAgents(key), getRunSummary(key)]);
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    }
  }

  async function create() {
    error = '';
    try {
      await defineAgent(key, { name, model, system });
      name = '';
      await load();
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    }
  }

  onMount(load);
</script>

<main>
  <h1>Agent Manager</h1>
  <div class="row">
    <button onclick={load}>Reload</button>
  </div>

  <form
    class="row"
    onsubmit={(e) => {
      e.preventDefault();
      create();
    }}
  >
    <input bind:value={name} placeholder="agent name" aria-label="agent name" />
    <input bind:value={model} placeholder="model (optional)" aria-label="model" />
    <input bind:value={system} placeholder="system prompt (optional)" aria-label="system prompt" />
    <button type="submit">Define agent</button>
  </form>

  {#if error}<p class="err">{error}</p>{/if}

  {#if summary}
    <div class="summary" aria-label="run summary">
      <span class="stat">Runs <b>{summary.total}</b></span>
      <span class="stat">Completed <b>{summary.completed}</b></span>
      <span class="stat fail">Failed <b>{summary.failed}</b></span>
    </div>
  {/if}

  {#if list.length === 0}
    <p class="muted">No agents.</p>
  {:else}
    <div class="table-wrap">
      <table>
        <thead>
          <tr><th>Name</th><th>Model</th><th>Runs</th></tr>
        </thead>
        <tbody>
          {#each list as a (a.name)}
            <tr>
              <td><a href={`/agents/${a.name}`}>{a.name}</a></td>
              <td>{a.model || '—'}</td>
              <td>{a.runs}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</main>

<style>
  main {
    max-width: 52rem;
    margin: 2rem auto;
    padding: 0 1rem;
    font-family: system-ui, sans-serif;
  }
  .row {
    display: flex;
    gap: 0.5rem;
    flex-wrap: wrap;
    margin: 0.6rem 0;
    align-items: center;
  }
  input,
  button {
    font: inherit;
    padding: 0.4rem 0.6rem;
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
