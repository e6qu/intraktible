<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { onMount } from 'svelte';
  import { page } from '$app/stores';
  import { getCase, assignCase, setCaseStatus, addCaseNote, type Case } from '$lib/api';
  import { displayEntries } from '$lib/kv';

  // API calls authenticate via the session cookie (empty key -> no X-Api-Key header).
  const key = '';
  let c = $state<Case | null>(null);
  let error = $state('');

  let assignee = $state('');
  let newStatus = $state('in_progress');
  let noteText = $state('');

  const caseID = $page.params.caseId ?? '';

  async function load() {
    error = '';
    try {
      // Only refresh the displayed case; the action inputs are user-controlled
      // (resetting them on every reload would race with the user's selection).
      c = await getCase(key, caseID);
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    }
  }

  async function run(action: () => Promise<void>) {
    error = '';
    try {
      await action();
      await load();
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    }
  }

  onMount(load);
</script>

<main>
  <p><a href="/cases">← queue</a></p>
  {#if c}
    <h1>{c.company_name}</h1>
    <dl>
      <dt>type</dt>
      <dd>{c.case_type}</dd>
      <dt>status</dt>
      <dd data-testid="case-status">{c.status}</dd>
      <dt>assignee</dt>
      <dd>{c.assignee || '—'}</dd>
      <dt>SLA</dt>
      <dd>{c.sla_days} days</dd>
      <dt>days left</dt>
      <dd class={`sla-${c.sla_state ?? ''}`} data-testid="days-left">
        {c.days_left}{#if c.sla_state}<span class="muted"> ({c.sla_state})</span>{/if}
      </dd>
      {#if c.source_decision_id}<dt>source decision</dt>
        <dd><code>{c.source_decision_id}</code></dd>{/if}
    </dl>

    {#if displayEntries(c.context).length > 0}
      <h2>Context</h2>
      <dl class="ctx" data-testid="context">
        {#each displayEntries(c.context) as [k, v] (k)}
          <dt>{k}</dt>
          <dd>{v}</dd>
        {/each}
      </dl>
    {/if}
  {:else}
    <h1>{caseID}</h1>
  {/if}
  {#if error}<p class="err">{error}</p>{/if}

  <div class="row">
    <button onclick={load}>Reload</button>
  </div>

  <div class="actions">
    <div class="row">
      <input bind:value={assignee} placeholder="assignee" aria-label="assignee" />
      <button onclick={() => run(() => assignCase(key, caseID, assignee))}>Assign</button>
    </div>
    <div class="row">
      <select bind:value={newStatus} aria-label="set status">
        <option value="needs_review">needs_review</option>
        <option value="in_progress">in_progress</option>
        <option value="completed">completed</option>
      </select>
      <button onclick={() => run(() => setCaseStatus(key, caseID, newStatus))}>Set status</button>
    </div>
    <div class="row">
      <input bind:value={noteText} placeholder="note" aria-label="note" />
      <button
        onclick={() => run(() => addCaseNote(key, caseID, noteText)).then(() => (noteText = ''))}
      >
        Add note
      </button>
    </div>
  </div>

  {#if c}
    <h2>Notes</h2>
    {#if c.notes.length === 0}<p class="muted">No notes.</p>{/if}
    <ul>
      {#each c.notes as n (n.at)}<li><b>{n.author}</b>: {n.text}</li>{/each}
    </ul>

    <h2>Audit</h2>
    <ul data-testid="audit">
      {#each c.audit as a (a.at + a.type)}<li>
          <code>{a.type}</code> — {a.detail} <span class="muted">({a.actor})</span>
        </li>{/each}
    </ul>
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
    margin: 0.4rem 0;
    align-items: center;
  }
  input,
  button,
  select {
    font: inherit;
    padding: 0.4rem 0.6rem;
  }
  dl {
    display: grid;
    grid-template-columns: 8rem 1fr;
    gap: 0.2rem 1rem;
  }
  dt {
    color: var(--fg-subtle);
  }
  .actions {
    margin: 1rem 0;
    padding: 0.6rem;
    background: #8881;
    border-radius: 0.5rem;
  }
  ul {
    padding-left: 1rem;
  }
  code {
    background: #8881;
    padding: 0 0.3rem;
    border-radius: 0.3rem;
  }
  .err {
    color: var(--danger);
  }
  .muted {
    color: var(--fg-subtle);
  }
  .sla-due_soon {
    color: var(--warn);
  }
  .sla-overdue {
    color: var(--danger);
    font-weight: 600;
  }
</style>
