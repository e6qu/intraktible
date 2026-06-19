<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { page } from '$app/stores';
  import { getCase, assignCase, setCaseStatus, addCaseNote, type Case } from '$lib/api';
  import { displayEntries } from '$lib/kv';
  import Breadcrumb from '$lib/Breadcrumb.svelte';
  import RelativeTime from '$lib/RelativeTime.svelte';

  // API calls authenticate via the session cookie (empty key -> no X-Api-Key header).
  const key = '';
  let c = $state<Case | null>(null);
  let error = $state('');

  let assignee = $state('');
  let newStatus = $state('in_progress');
  let noteText = $state('');

  // Derive from the route param so navigating between sibling cases reloads.
  const caseID = $derived($page.params.caseId ?? '');

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

  let busy = $state(false);
  async function run(action: () => Promise<void>) {
    error = '';
    busy = true;
    try {
      await action();
      await load();
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      busy = false;
    }
  }

  $effect(() => {
    void caseID; // reload on initial mount and sibling navigation
    void load();
  });
</script>

<main>
  <Breadcrumb sectionHref="/cases" sectionLabel="Cases" current={caseID} />
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

  <h2>Actions</h2>
  <div class="actions">
    <div class="row">
      <input bind:value={assignee} placeholder="assignee" aria-label="assignee" />
      <button onclick={() => run(() => assignCase(key, caseID, assignee))} disabled={busy}
        >Assign</button
      >
    </div>
    <div class="row">
      <select bind:value={newStatus} aria-label="set status">
        <option value="needs_review">needs_review</option>
        <option value="in_progress">in_progress</option>
        <option value="completed">completed</option>
      </select>
      <button onclick={() => run(() => setCaseStatus(key, caseID, newStatus))} disabled={busy}
        >Set status</button
      >
    </div>
    <div class="row">
      <input bind:value={noteText} placeholder="note" aria-label="note" />
      <button
        onclick={() =>
          run(async () => {
            await addCaseNote(key, caseID, noteText);
            noteText = ''; // only clear after a successful save (run() swallows errors)
          })}
        disabled={busy}
      >
        Add note
      </button>
    </div>
  </div>

  {#if c}
    <h2>Notes</h2>
    {#if c.notes.length === 0}<p class="muted">No notes.</p>{/if}
    <ul>
      {#each c.notes as n (n.at)}<li>
          <b>{n.author}</b>: {n.text} <span class="muted"><RelativeTime value={n.at} /></span>
        </li>{/each}
    </ul>

    <h2>Activity</h2>
    <ol class="timeline" data-testid="audit">
      {#each c.audit as a (a.at + a.type)}
        <li>
          <span class="when muted" title={new Date(a.at).toLocaleString()}>
            <RelativeTime value={a.at} />
          </span>
          <span class="what"><code>{a.type}</code> {a.detail}</span>
          <span class="who muted">{a.actor}</span>
        </li>
      {/each}
    </ol>
  {/if}
</main>

<style>
  main {
    max-width: 52rem;
    margin: 2rem auto;
    padding: 0 1rem;
    font-family: var(--font-ui);
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
  ol.timeline {
    list-style: none;
    padding: 0;
    margin: 0.5rem 0;
    border-left: 2px solid var(--border);
  }
  ol.timeline li {
    position: relative;
    padding: 0.4rem 0 0.4rem 1rem;
    display: flex;
    flex-wrap: wrap;
    gap: 0.5rem;
    align-items: baseline;
  }
  ol.timeline li::before {
    content: '';
    position: absolute;
    left: -5px;
    top: 0.75rem;
    width: 8px;
    height: 8px;
    border-radius: 999px;
    background: var(--accent);
  }
  ol.timeline .when {
    min-width: 7rem;
    font-size: 0.82rem;
  }
  ol.timeline .who {
    font-size: 0.82rem;
  }
</style>
