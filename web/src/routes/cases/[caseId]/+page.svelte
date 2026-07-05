<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { page } from '$app/stores';
  import Icon from '$lib/Icon.svelte';
  import {
    getCase,
    assignCase,
    setCaseStatus,
    addCaseNote,
    type Case,
    type CaseStatus
  } from '$lib/api';
  import { displayEntries } from '$lib/kv';
  import Breadcrumb from '$lib/Breadcrumb.svelte';
  import CommentThread from '$lib/CommentThread.svelte';
  import RelativeTime from '$lib/RelativeTime.svelte';
  import Skeleton from '$lib/Skeleton.svelte';
  import EmptyState from '$lib/EmptyState.svelte';
  import Badge from '$lib/Badge.svelte';
  import { caseStatusTone, slaTone } from '$lib/badge';
  import { roleAtLeast } from '$lib/roles';
  import { user } from '$lib/session';
  import { appHref } from '$lib/paths';

  // API calls authenticate via the session cookie (empty key -> no X-Api-Key header).
  const key = '';
  let c = $state<Case | null>(null);
  let error = $state('');
  // A 404 (or "not found") is a distinct, expected state — a mistyped/stale id — and
  // gets a polished EmptyState rather than the raw red error string used for real
  // failures (network, 5xx).
  const notFound = $derived(/not found|404/i.test(error));

  let assignee = $state('');
  let newStatus = $state<CaseStatus>('in_progress');
  let noteText = $state('');
  // Seed the status <select> from the case's real status once, on first load, so it
  // doesn't default to in_progress (which invites an accidental backward
  // transition). Only the first load seeds it — later reloads must not clobber a
  // selection the user is mid-way through making.
  let statusSeeded = false;

  // Derive from the route param so navigating between sibling cases reloads.
  const caseID = $derived($page.params.caseId ?? '');

  // A closed case has no live SLA clock — its days_left is a frozen leftover, so the
  // urgency badge and the days-left figure are suppressed rather than shown as a stale
  // countdown. (completed is the only terminal status today; resolved/cancelled are
  // guarded for forward-compat.)
  const TERMINAL = new Set(['completed', 'resolved', 'cancelled']);
  const closed = $derived(c != null && TERMINAL.has(c.status));

  // The SLA state is a wire enum (on_track/due_soon/overdue) — render it as a
  // human label, not the raw underscored value.
  function slaLabel(s: string): string {
    return s.replace(/_/g, ' ');
  }

  async function load() {
    error = '';
    // Drop a stale response when sibling navigation changes caseID mid-flight.
    const reqID = caseID;
    try {
      // Only refresh the displayed case; the action inputs are user-controlled
      // (resetting them on every reload would race with the user's selection).
      const got = await getCase(key, caseID);
      if (caseID !== reqID) return;
      c = got;
      if (!statusSeeded) {
        newStatus = got.status;
        statusSeeded = true;
      }
    } catch (e) {
      if (caseID === reqID) error = e instanceof Error ? e.message : String(e);
    }
  }

  // Format a context value for the scannable fact grid: *_usd / *_amount keys get
  // currency formatting; everything else is shown verbatim (displayEntries has
  // already stringified nested objects to compact JSON).
  const usdFmt = new Intl.NumberFormat('en-US', {
    style: 'currency',
    currency: 'USD',
    maximumFractionDigits: 0
  });
  function factValue(key: string, value: string): string {
    const n = Number(value);
    if (/(_usd|_amount)$/.test(key) && Number.isFinite(n)) return usdFmt.format(n);
    return value;
  }
  // The risk figure is the headline number a reviewer scans for — emphasise it.
  function isRisk(key: string): boolean {
    return /risk|score/i.test(key);
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
    // Reset the rendered case and the status seed — otherwise a failed sibling
    // load keeps showing the previous case (and its status in the select).
    c = null;
    statusSeeded = false;
    void load();
  });
</script>

<main>
  <Breadcrumb sectionHref="/cases" sectionLabel="Cases" current={caseID} />
  {#if !notFound}
    <div class="row reload-row">
      <button onclick={load} title="Re-fetch this case and its activity"
        ><Icon name="reload" size={15} /> Reload</button
      >
    </div>
  {/if}
  {#if c}
    <div class="head">
      <h1>{c.company_name}</h1>
      <Badge tone={caseStatusTone(c.status)}
        ><span data-testid="case-status">{c.status}</span></Badge
      >
      {#if !closed && c.sla_state && c.sla_state !== 'on_track'}
        <Badge tone={slaTone(c.sla_state)} title="SLA urgency">
          {c.sla_state === 'overdue' ? '⚠ overdue' : 'due soon'} · {c.days_left}d left
        </Badge>
      {/if}
    </div>
    <dl>
      <dt>type</dt>
      <dd><span class="chip">{c.case_type}</span></dd>
      <dt>assignee</dt>
      <dd>{c.assignee || '—'}</dd>
      <dt>SLA</dt>
      <dd>{c.sla_days} day{c.sla_days === 1 ? '' : 's'}</dd>
      <dt>days left</dt>
      <dd class={closed ? '' : `sla-${c.sla_state ?? ''}`} data-testid="days-left">
        {#if closed}<span class="muted">—</span>{:else}{c.days_left}{#if c.sla_state}<span
              class="muted">{' ('}{slaLabel(c.sla_state)})</span
            >{/if}{/if}
      </dd>
      {#if c.source_decision_id}<dt>source decision</dt>
        <dd>
          <a href={appHref(`/decisions/${c.source_decision_id}`)}>{c.source_decision_id} →</a>
        </dd>
      {/if}
    </dl>

    {#if displayEntries(c.context).length > 0}
      <h2>Context</h2>
      <div class="facts" data-testid="context">
        {#each displayEntries(c.context) as [k, v] (k)}
          <div class="fact" class:risk={isRisk(k)}>
            <span class="fact-key">{k}</span>
            <span class="fact-val">{factValue(k, v)}</span>
          </div>
        {/each}
      </div>
    {/if}
  {:else if !error}
    <h1>{caseID}</h1>
    <Skeleton rows={5} />
  {:else if notFound}
    <EmptyState
      icon="cases"
      title="Case not found"
      hint="No case matches this id. It may have been deleted, or the id may be mistyped."
    >
      {#snippet action()}
        <a href={appHref('/cases')}>← Back to the queue</a>
      {/snippet}
    </EmptyState>
  {:else}
    <h1>{caseID}</h1>
  {/if}
  {#if error && !notFound}<p class="err">{error}</p>{/if}

  {#if c && c.status !== 'completed'}
    <div class="resolve-bar">
      <button
        class="resolve"
        onclick={() => run(() => setCaseStatus(key, caseID, 'completed'))}
        disabled={busy || !roleAtLeast($user?.role, 'operator')}
        title={!roleAtLeast($user?.role, 'operator') ? 'Requires the operator role' : undefined}
      >
        {busy ? 'Working…' : '✓ Resolve case'}
      </button>
      <span class="muted">Mark this case completed.</span>
    </div>
  {:else if c}
    <p class="resolved muted">✓ This case is resolved.</p>
  {/if}

  {#if c}
    <h2>Actions</h2>
    <div class="actions">
      <div class="row">
        <input bind:value={assignee} placeholder="assignee" aria-label="assignee" />
        <button
          onclick={() => run(() => assignCase(key, caseID, assignee))}
          disabled={busy || !roleAtLeast($user?.role, 'operator')}
          title={!roleAtLeast($user?.role, 'operator') ? 'Requires the operator role' : undefined}
          >Assign</button
        >
        <button
          onclick={() => run(() => assignCase(key, caseID, $user?.actor ?? ''))}
          disabled={busy || !$user?.actor || !roleAtLeast($user?.role, 'operator')}
          title={!roleAtLeast($user?.role, 'operator') ? 'Requires the operator role' : undefined}
          >Assign to me</button
        >
      </div>
      <div class="row">
        <select bind:value={newStatus} aria-label="set status">
          <option value="needs_review">needs_review</option>
          <option value="in_progress">in_progress</option>
          <option value="completed">completed</option>
        </select>
        <button
          onclick={() => run(() => setCaseStatus(key, caseID, newStatus))}
          disabled={busy || !roleAtLeast($user?.role, 'operator')}
          title={!roleAtLeast($user?.role, 'operator') ? 'Requires the operator role' : undefined}
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
          disabled={busy || !roleAtLeast($user?.role, 'operator')}
          title={!roleAtLeast($user?.role, 'operator') ? 'Requires the operator role' : undefined}
        >
          Add note
        </button>
      </div>
    </div>
  {/if}

  {#if c}
    <h2>Notes</h2>
    {#if c.notes.length === 0}<p class="muted">No notes.</p>{/if}
    <ul>
      {#each c.notes as n, i (i)}<li>
          <b>{n.author}</b>: {n.text} <span class="muted"><RelativeTime value={n.at} /></span>
        </li>{/each}
    </ul>

    <h2>Activity</h2>
    <ol class="timeline" data-testid="audit">
      {#each c.audit as a, i (i)}
        <li>
          <span class="when muted" title={new Date(a.at).toLocaleString()}>
            <RelativeTime value={a.at} />
          </span>
          <span class="what"><code>{a.type}</code> {a.detail}</span>
          <span class="who muted">{a.actor}</span>
        </li>
      {/each}
    </ol>

    <h2>Discussion</h2>
    <p class="muted disc-hint">
      Talk the case through with the team — @mention a colleague to notify them. Notes above stay
      the immutable work record; this thread is for collaboration.
    </p>
    <CommentThread subjectType="case" subjectId={caseID} title="Case discussion" />
  {/if}
</main>

<style>
  .reload-row {
    justify-content: flex-end;
    margin-top: -2.4rem;
  }
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
  .head {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.6rem;
    margin-bottom: 0.4rem;
  }
  .head h1 {
    margin: 0;
  }
  dl {
    display: grid;
    grid-template-columns: 8rem 1fr;
    gap: 0.2rem 1rem;
  }
  dt {
    color: var(--fg-subtle);
  }
  .chip {
    display: inline-block;
    padding: 0.1rem 0.5rem;
    border-radius: 999px;
    font-size: 0.78rem;
    background: var(--surface-2);
    color: var(--fg-muted);
    border: 1px solid var(--border);
  }
  .facts {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(11rem, 1fr));
    gap: 0.5rem;
    margin: 0.5rem 0 1rem;
  }
  .fact {
    display: flex;
    flex-direction: column;
    gap: 0.1rem;
    padding: 0.5rem 0.65rem;
    background: var(--surface-2);
    border: 1px solid var(--border);
    border-radius: 0.5rem;
  }
  .fact-key {
    font-size: 0.72rem;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--fg-subtle);
  }
  .fact-val {
    font-size: 0.95rem;
    color: var(--fg);
    word-break: break-word;
  }
  .fact.risk {
    border-color: color-mix(in srgb, var(--accent) 40%, transparent);
    background: color-mix(in srgb, var(--accent) 8%, var(--surface-2));
  }
  .fact.risk .fact-val {
    font-size: 1.35rem;
    font-weight: 700;
    color: var(--accent-ink, var(--accent));
  }
  .resolve-bar {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.6rem;
    margin: 1rem 0;
  }
  .resolve {
    font: inherit;
    font-weight: 600;
    padding: 0.5rem 1rem;
    border: 1px solid color-mix(in srgb, var(--ok, #16a34a) 40%, transparent);
    border-radius: 0.5rem;
    background: color-mix(in srgb, var(--ok, #16a34a) 16%, transparent);
    color: var(--ok, #16a34a);
    cursor: pointer;
  }
  .resolve:disabled {
    opacity: 0.6;
    cursor: default;
  }
  .resolved {
    margin: 1rem 0;
    font-weight: 600;
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
  .disc-hint {
    margin: 0.2rem 0 0;
    font-size: 0.85rem;
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
