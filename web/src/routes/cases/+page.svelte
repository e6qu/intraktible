<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { onMount } from 'svelte';
  import { toast } from '$lib/toast';
  import EmptyState from '$lib/EmptyState.svelte';
  import Skeleton from '$lib/Skeleton.svelte';
  import {
    listCases,
    getCaseSummary,
    requestReview,
    sweepSLA,
    assignCase,
    setCaseStatus,
    type Case,
    type CaseSummary
  } from '$lib/api';

  function msg(e: unknown): string {
    return e instanceof Error ? e.message : String(e);
  }

  // API calls authenticate via the session cookie (empty key -> no X-Api-Key header).
  const key = '';
  let statusFilter = $state('');
  let list = $state<Case[]>([]);
  let summary = $state<CaseSummary | null>(null);
  let error = $state('');
  let loading = $state(true);

  // new-case form
  let company = $state('');
  let caseType = $state('aml');
  let slaDays = $state(5);

  // Bulk selection on the queue (multi-select → assign / mark completed).
  let selectedIds = $state<string[]>([]);
  let bulkAssignee = $state('');
  let bulkBusy = $state(false);
  const allSelected = $derived(list.length > 0 && selectedIds.length === list.length);
  function toggle(id: string) {
    selectedIds = selectedIds.includes(id)
      ? selectedIds.filter((x) => x !== id)
      : [...selectedIds, id];
  }
  function toggleAll() {
    selectedIds = allSelected ? [] : list.map((c) => c.case_id);
  }

  async function load() {
    loading = true;
    error = '';
    selectedIds = []; // the list is changing; drop a stale selection
    try {
      [list, summary] = await Promise.all([
        listCases(key, { status: statusFilter }),
        getCaseSummary(key)
      ]);
    } catch (e) {
      error = msg(e);
    } finally {
      loading = false;
    }
  }

  // runBulk applies op to every id concurrently and reports partial success — one
  // failing case must not abort the rest or hide which ones did change.
  async function runBulk(verb: string, op: (id: string) => Promise<unknown>) {
    const ids = selectedIds;
    const results = await Promise.allSettled(ids.map((id) => op(id)));
    const failed = results.filter((r) => r.status === 'rejected').length;
    const ok = ids.length - failed;
    if (failed === 0) {
      toast.success(`${verb} ${ok} case(s)`);
    } else {
      const first = results.find((r) => r.status === 'rejected') as
        | PromiseRejectedResult
        | undefined;
      error = `${verb} ${ok} of ${ids.length} case(s); ${failed} failed${first ? `: ${msg(first.reason)}` : ''}`;
    }
    await load();
  }

  async function bulkAssign() {
    if (bulkBusy || !bulkAssignee.trim() || selectedIds.length === 0) return;
    bulkBusy = true;
    error = '';
    const who = bulkAssignee.trim();
    try {
      await runBulk(`Assigned to ${who}:`, (id) => assignCase(key, id, who));
      bulkAssignee = '';
    } finally {
      bulkBusy = false;
    }
  }
  async function bulkComplete() {
    if (bulkBusy || selectedIds.length === 0) return;
    if (!confirm(`Mark ${selectedIds.length} case(s) completed?`)) return;
    bulkBusy = true;
    error = '';
    try {
      await runBulk('Completed', (id) => setCaseStatus(key, id, 'completed'));
    } finally {
      bulkBusy = false;
    }
  }

  let creating = $state(false);
  async function create() {
    if (creating) return; // guard against double-submit (Enter + click) → duplicate cases
    error = '';
    creating = true;
    try {
      await requestReview(key, { company_name: company, case_type: caseType, sla_days: slaDays });
      company = '';
      await load();
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      creating = false;
    }
  }

  let sweeping = $state(false);
  async function runSweep() {
    error = '';
    sweeping = true;
    try {
      const { count } = await sweepSLA(key);
      toast.success(count > 0 ? `${count} case(s) breached SLA` : 'No SLA breaches');
      await load();
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      sweeping = false;
    }
  }

  onMount(load);
</script>

<main>
  <h1>Case Manager — Queue</h1>
  <div class="row">
    <label
      >status
      <select bind:value={statusFilter} onchange={load} aria-label="status filter">
        <option value="">all</option>
        <option value="needs_review">needs_review</option>
        <option value="in_progress">in_progress</option>
        <option value="completed">completed</option>
      </select>
    </label>
    <button onclick={load}>Reload</button>
    <button onclick={runSweep} disabled={sweeping} title="Flag overdue open cases as SLA-breached">
      {sweeping ? 'Sweeping…' : 'Run SLA sweep'}
    </button>
  </div>

  <form
    class="row"
    onsubmit={(e) => {
      e.preventDefault();
      create();
    }}
  >
    <label
      >Company <input
        bind:value={company}
        placeholder="Globex Corp"
        aria-label="company name"
      /></label
    >
    <label>Type <input bind:value={caseType} placeholder="aml" aria-label="case type" /></label>
    <label
      >SLA days
      <input
        type="number"
        bind:value={slaDays}
        aria-label="sla days"
        min="0"
        style="width:5rem"
      /></label
    >
    <button type="submit" disabled={creating}>{creating ? 'Opening…' : 'Open case'}</button>
  </form>

  {#if error}<p class="err">{error}</p>{/if}

  {#if summary}
    <div class="summary" aria-label="queue summary">
      <span class="stat">Total <b>{summary.total}</b></span>
      <span class="stat">Needs review <b>{summary.by_status?.needs_review ?? 0}</b></span>
      <span class="stat">In progress <b>{summary.by_status?.in_progress ?? 0}</b></span>
      <span class="stat">Unassigned <b>{summary.unassigned}</b></span>
      <span class="stat due">Due soon <b>{summary.due_soon}</b></span>
      <span class="stat over">Overdue <b>{summary.overdue}</b></span>
    </div>
  {/if}

  {#if selectedIds.length > 0}
    <div class="row bulk" data-testid="bulk-bar">
      <span class="muted">{selectedIds.length} selected</span>
      <input bind:value={bulkAssignee} placeholder="assignee" aria-label="bulk assignee" />
      <button onclick={bulkAssign} disabled={bulkBusy || !bulkAssignee.trim()}>Assign</button>
      <button onclick={bulkComplete} disabled={bulkBusy}>Mark completed</button>
      <button class="link" onclick={() => (selectedIds = [])}>clear</button>
    </div>
  {/if}

  {#if loading}
    <Skeleton rows={5} />
  {:else if list.length === 0}
    <EmptyState
      icon="cases"
      title={statusFilter ? `No ${statusFilter} cases` : 'The review queue is clear'}
      hint="Cases open here when a flow's manual-review node escalates, an agent run is escalated, or you open one above."
    />
  {:else}
    <div class="table-wrap">
      <table>
        <thead>
          <tr
            ><th
              ><input
                type="checkbox"
                checked={allSelected}
                onchange={toggleAll}
                aria-label="select all cases"
              /></th
            ><th>Company</th><th>Type</th><th>Status</th><th>Assignee</th><th>SLA</th><th
              >Days left</th
            ></tr
          >
        </thead>
        <tbody>
          {#each list as c (c.case_id)}
            <tr class:sel={selectedIds.includes(c.case_id)}>
              <td
                ><input
                  type="checkbox"
                  checked={selectedIds.includes(c.case_id)}
                  onchange={() => toggle(c.case_id)}
                  aria-label={`select ${c.company_name}`}
                /></td
              >
              <td><a href={`/cases/${c.case_id}`}>{c.company_name}</a></td>
              <td>{c.case_type}</td>
              <td>{c.status}</td>
              <td>{c.assignee || '—'}</td>
              <td>{c.sla_days}d</td>
              <td class={`sla-${c.sla_state ?? ''}`}>{c.days_left}d</td>
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
  }
  .row {
    display: flex;
    gap: 0.5rem;
    flex-wrap: wrap;
    margin: 0.6rem 0;
    align-items: center;
  }
  input,
  button,
  select {
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
  .stat.due b {
    color: var(--warn);
  }
  .stat.over b {
    color: var(--danger);
  }
  .sla-due_soon {
    color: var(--warn);
  }
  .sla-overdue {
    color: var(--danger);
    font-weight: 600;
  }
  .row label {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    margin: 0;
    color: var(--fg-subtle);
    font-size: 0.85rem;
  }
  .bulk {
    padding: 0.5rem 0.7rem;
    background: var(--surface-2);
    border-radius: 6px;
  }
  tr.sel {
    background: color-mix(in srgb, var(--accent) 8%, transparent);
  }
  button.link {
    background: none;
    border: none;
    color: var(--accent);
    cursor: pointer;
    padding: 0.2rem;
  }
</style>
