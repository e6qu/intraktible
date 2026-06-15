<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { onMount } from 'svelte';
  import Icon from '$lib/Icon.svelte';
  import { listDecisions, type Decision } from '$lib/api';

  // API calls authenticate via the session cookie (empty key → no X-Api-Key).
  const key = '';
  let list = $state<Decision[]>([]);
  let error = $state('');

  function msg(e: unknown): string {
    return e instanceof Error ? e.message : String(e);
  }
  async function load() {
    error = '';
    try {
      list = await listDecisions(key);
    } catch (e) {
      error = msg(e);
    }
  }
  function when(iso: string): string {
    return iso ? new Date(iso).toLocaleString() : '';
  }
  onMount(load);
</script>

<main>
  <div class="head">
    <h1>Decisions</h1>
    <button onclick={load}><Icon name="reload" size={15} /> Reload</button>
  </div>
  {#if error}<p class="err">{error}</p>{/if}

  {#if list.length === 0}
    <p class="muted">
      No decisions yet. Run a flow from the <a href="/engine">Decision Engine</a>.
    </p>
  {:else}
    <table>
      <thead>
        <tr
          ><th>Status</th><th>Flow</th><th>Env</th><th>Ver</th><th>Variant</th><th>Duration</th><th
            >When</th
          ></tr
        >
      </thead>
      <tbody>
        {#each list as d (d.decision_id)}
          <tr>
            <td><span class="badge {d.status}">{d.status}</span></td>
            <td><a href={`/decisions/${d.decision_id}`}>{d.slug}</a></td>
            <td>{d.environment}</td>
            <td>v{d.version}</td>
            <td class="muted">{d.variant ?? '—'}</td>
            <td>{d.duration_ms ?? 0} ms</td>
            <td class="muted">{when(d.started_at)}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  {/if}
</main>

<style>
  main {
    max-width: 60rem;
    margin: 2rem auto;
    padding: 0 1.25rem;
  }
  .head {
    display: flex;
    align-items: center;
    justify-content: space-between;
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
  .badge {
    display: inline-block;
    padding: 0.1rem 0.5rem;
    border-radius: 999px;
    font-size: 0.78rem;
    font-weight: 550;
    background: var(--surface-2);
    color: var(--fg-muted);
  }
  .badge.completed {
    background: color-mix(in srgb, var(--ok) 18%, transparent);
    color: var(--ok);
  }
  .badge.failed {
    background: color-mix(in srgb, var(--danger) 16%, transparent);
    color: var(--danger);
  }
  .muted {
    color: var(--fg-subtle);
  }
  .err {
    color: var(--danger);
  }
</style>
