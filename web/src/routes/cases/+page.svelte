<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { onMount } from 'svelte';
  import { listCases, requestReview, type Case } from '$lib/api';

  let key = $state('dev-sandbox-key');
  let statusFilter = $state('');
  let list = $state<Case[]>([]);
  let error = $state('');

  // new-case form
  let company = $state('');
  let caseType = $state('aml');
  let slaDays = $state(5);

  async function load() {
    error = '';
    try {
      list = await listCases(key, { status: statusFilter });
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    }
  }

  async function create() {
    error = '';
    try {
      await requestReview(key, { company_name: company, case_type: caseType, sla_days: slaDays });
      company = '';
      await load();
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    }
  }

  onMount(load);
</script>

<main>
  <h1>Case Manager — Queue</h1>
  <div class="row">
    <input bind:value={key} aria-label="API key" />
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
  </div>

  <form
    class="row"
    onsubmit={(e) => {
      e.preventDefault();
      create();
    }}
  >
    <input bind:value={company} placeholder="company name" aria-label="company name" />
    <input bind:value={caseType} placeholder="case type" aria-label="case type" />
    <input type="number" bind:value={slaDays} aria-label="sla days" min="0" style="width:5rem" />
    <button type="submit">Open case</button>
  </form>

  {#if error}<p class="err">{error}</p>{/if}

  {#if list.length === 0}
    <p class="muted">No cases.</p>
  {:else}
    <table>
      <thead>
        <tr><th>Company</th><th>Type</th><th>Status</th><th>Assignee</th><th>SLA</th></tr>
      </thead>
      <tbody>
        {#each list as c (c.case_id)}
          <tr>
            <td><a href={`/cases/${c.case_id}`}>{c.company_name}</a></td>
            <td>{c.case_type}</td>
            <td>{c.status}</td>
            <td>{c.assignee || '—'}</td>
            <td>{c.sla_days}d</td>
          </tr>
        {/each}
      </tbody>
    </table>
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
    border-bottom: 1px solid #eee;
  }
  .err {
    color: #b00;
  }
  .muted {
    color: #888;
  }
</style>
