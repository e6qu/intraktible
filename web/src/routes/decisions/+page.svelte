<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { onMount } from 'svelte';
  import Icon from '$lib/Icon.svelte';
  import EmptyState from '$lib/EmptyState.svelte';
  import Skeleton from '$lib/Skeleton.svelte';
  import RelativeTime from '$lib/RelativeTime.svelte';
  import { listDecisionsPage, type Decision } from '$lib/api';
  import { resolvePersona, personaLens } from '$lib/persona';

  // API calls authenticate via the session cookie (empty key → no X-Api-Key).
  const key = '';
  const PAGE = 50;
  let list = $state<Decision[]>([]);
  let total = $state(0);
  let offset = $state(0);
  let error = $state('');
  let loading = $state(true);

  // filters (applied on Search / Enter, not keystroke). They default to the persona's
  // decisions lens — a developer lands on failed traces, product on the challenger arm
  // — and are freely changeable/clearable.
  const lens = personaLens(resolvePersona()).decisions ?? {};
  let fFlow = $state('');
  let fEnv = $state<string>(lens.env ?? '');
  let fStatus = $state<string>(lens.status ?? '');
  let fVariant = $state<string>(lens.variant ?? '');
  let fQuery = $state('');

  function msg(e: unknown): string {
    return e instanceof Error ? e.message : String(e);
  }
  async function load() {
    loading = true;
    error = '';
    try {
      const page = await listDecisionsPage(key, {
        flow: fFlow.trim() || undefined,
        env: fEnv || undefined,
        status: fStatus || undefined,
        variant: fVariant || undefined,
        q: fQuery.trim() || undefined,
        limit: PAGE,
        offset
      });
      list = page.decisions;
      total = page.total;
    } catch (e) {
      error = msg(e);
    } finally {
      loading = false;
    }
  }
  function applyFilters() {
    offset = 0;
    void load();
  }
  function go(delta: number) {
    if (loading) return; // a double-click while a page is in flight would overshoot
    const next = offset + delta * PAGE;
    if (next < 0 || next >= total) return; // out of range (no empty page past the end)
    offset = next;
    void load();
  }
  function absTime(iso: string): string {
    const d = new Date(iso);
    return isNaN(d.getTime()) ? iso : d.toLocaleString();
  }
  const from = $derived(total === 0 ? 0 : offset + 1);
  const to = $derived(Math.min(offset + list.length, total));
  onMount(load);
</script>

<main>
  <div class="head">
    <h1>Decisions</h1>
    <button onclick={load}><Icon name="reload" size={15} /> Reload</button>
  </div>

  <form
    class="filters"
    onsubmit={(e) => {
      e.preventDefault();
      applyFilters();
    }}
  >
    <label
      >Flow <input bind:value={fFlow} placeholder="slug" aria-label="filter by flow slug" /></label
    >
    <label
      >Env
      <select bind:value={fEnv} aria-label="filter by environment">
        <option value="">any</option>
        <option value="sandbox">sandbox</option>
        <option value="staging">staging</option>
        <option value="production">production</option>
      </select></label
    >
    <label
      >Status
      <select bind:value={fStatus} aria-label="filter by status">
        <option value="">any</option>
        <option value="completed">completed</option>
        <option value="failed">failed</option>
        <option value="started">started</option>
      </select></label
    >
    <label
      >Variant
      <select bind:value={fVariant} aria-label="filter by variant">
        <option value="">any</option>
        <option value="champion">champion</option>
        <option value="challenger">challenger</option>
      </select></label
    >
    <label
      >Decision ID <input
        bind:value={fQuery}
        placeholder="search id"
        aria-label="search by decision id"
      /></label
    >
    <button type="submit"><Icon name="search" size={14} /> Apply</button>
  </form>

  {#if error}<p class="err">{error}</p>{/if}

  {#if loading}
    <Skeleton rows={6} />
  {:else if list.length === 0}
    <EmptyState
      icon="diagram"
      title={total === 0 && !fFlow && !fEnv && !fStatus && !fVariant && !fQuery
        ? 'No decisions yet'
        : 'No decisions match these filters'}
      hint="Run a flow from the Decision Engine and every determination shows up here — replayable, node by node."
    >
      {#snippet action()}
        <a href="/engine">Open the Decision Engine →</a>
      {/snippet}
    </EmptyState>
  {:else}
    <div class="table-wrap">
      <table>
        <thead>
          <tr
            ><th>Status</th><th>Flow</th><th>Env</th><th>Ver</th><th>Variant</th><th>Duration</th
            ><th>When</th></tr
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
              <td class="muted" title={absTime(d.started_at)}
                ><RelativeTime value={d.started_at} /></td
              >
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
    <div class="pager">
      <span class="muted">{from}–{to} of {total}</span>
      <span class="spacer"></span>
      <button onclick={() => go(-1)} disabled={offset === 0}>← Prev</button>
      <button onclick={() => go(1)} disabled={to >= total}>Next →</button>
    </div>
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
  .filters {
    display: flex;
    flex-wrap: wrap;
    gap: 0.6rem 0.9rem;
    align-items: flex-end;
    margin: 0.75rem 0 0.25rem;
  }
  .filters label {
    display: flex;
    flex-direction: column;
    gap: 0.2rem;
    font-size: 0.72rem;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--fg-subtle);
  }
  .filters input,
  .filters select,
  .filters button {
    font: inherit;
    padding: 0.35rem 0.5rem;
  }
  .pager {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    margin-top: 0.75rem;
  }
  .pager .spacer {
    flex: 1;
  }
  .pager button {
    font: inherit;
    padding: 0.35rem 0.7rem;
  }
</style>
