<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { goto, afterNavigate } from '$app/navigation';
  import { page } from '$app/stores';
  import { get } from 'svelte/store';
  import Icon from '$lib/Icon.svelte';
  import EmptyState from '$lib/EmptyState.svelte';
  import Skeleton from '$lib/Skeleton.svelte';
  import RelativeTime from '$lib/RelativeTime.svelte';
  import {
    listDecisionsPage,
    type Decision,
    type DecisionStatus,
    type Environment,
    type Variant
  } from '$lib/api';
  import { resolvePersona, personaConfig, personaLens, type DecisionColumn } from '$lib/persona';
  import { appHref } from '$lib/paths';
  import { withOffset } from '$lib/paging';
  import Badge from '$lib/Badge.svelte';
  import { statusTone, dispositionTone } from '$lib/badge';

  // API calls authenticate via the session cookie (empty key → no X-Api-Key).
  const key = '';
  const PAGE = 25;
  let list = $state<Decision[]>([]);
  let total = $state(0);
  let offset = $state(0);
  let error = $state('');
  let loading = $state(true);

  // filters (applied on Search / Enter, not keystroke). The URL query string is the
  // source of truth, so a filtered view is deep-linkable and back/forward replays it;
  // afterNavigate hydrates these from the query string. The persona's decisions lens
  // — a developer lands on failed traces, product on the challenger arm — seeds the
  // defaults when the URL carries no filter of its own.
  const lens = personaLens(resolvePersona()).decisions ?? {};
  // The page heading follows the persona's term for this surface (a developer's nav and
  // page both say "Traces", not just the nav item).
  const heading = personaConfig(resolvePersona()).terms?.decisions ?? 'Decisions';
  // The visible columns and their order come from the persona lens (a developer leads
  // with status/duration, product with the experiment variant); unset → the full set.
  const DEFAULT_COLUMNS: DecisionColumn[] = [
    'status',
    'disposition',
    'flow',
    'env',
    'version',
    'variant',
    'duration',
    'when'
  ];
  const columns: DecisionColumn[] = lens.columns ?? DEFAULT_COLUMNS;
  const COLUMN_LABELS = new Map<DecisionColumn, string>([
    ['status', 'Status'],
    ['disposition', 'Disposition'],
    ['flow', 'Flow'],
    ['env', 'Env'],
    ['version', 'Ver'],
    ['variant', 'Variant'],
    ['duration', 'Duration'],
    ['when', 'When']
  ]);
  const columnLabel = (c: DecisionColumn): string => COLUMN_LABELS.get(c) ?? c;
  const DEFAULT_EMPTY_HINT =
    'Run a flow from Flows and every determination shows up here — replayable, node by node.';
  const NO_MATCH_HINT = 'No decisions match these filters — clear or widen them to see more.';
  let fFlow = $state('');
  let fEnv = $state<Environment | ''>(lens.env ?? '');
  let fStatus = $state<DecisionStatus | ''>(lens.status ?? '');
  let fVariant = $state<Variant | ''>(lens.variant ?? '');
  let fQuery = $state('');

  function msg(e: unknown): string {
    return e instanceof Error ? e.message : String(e);
  }
  // The last-applied (URL-driven) filter. Reload and the pager fetch through this —
  // never the draft inputs — so edits that haven't been Applied can't leak into a
  // fetch (and the rows always match what the URL says).
  let applied = $state<{
    flow?: string;
    env?: Environment;
    status?: DecisionStatus;
    variant?: Variant;
    q?: string;
  }>({});
  // A generation token so overlapping loads (rapid Apply / pager) don't clobber: only
  // the latest request's response is allowed to write the list.
  let loadSeq = 0;
  async function load() {
    const seq = ++loadSeq;
    loading = true;
    error = '';
    try {
      const page = await listDecisionsPage(key, { ...applied, limit: PAGE, offset });
      if (seq !== loadSeq) return; // a newer load superseded this one
      list = page.decisions;
      total = page.total;
    } catch (e) {
      if (seq === loadSeq) error = msg(e);
    } finally {
      if (seq === loadSeq) loading = false;
    }
  }
  // Apply pushes the current inputs into the URL; afterNavigate below re-fetches.
  function applyFilters() {
    offset = 0;
    pushURL();
  }
  function pushURL() {
    const p = new URLSearchParams();
    if (fFlow.trim()) p.set('flow', fFlow.trim());
    if (fEnv) p.set('env', fEnv);
    if (fStatus) p.set('status', fStatus);
    if (fVariant) p.set('variant', fVariant);
    if (fQuery.trim()) p.set('q', fQuery.trim());
    if (offset) p.set('offset', String(offset));
    const qs = p.toString();
    goto(qs ? `?${qs}` : get(page).url.pathname, { keepFocus: true, noScroll: true });
  }
  function go(delta: number) {
    if (loading) return; // a double-click while a page is in flight would overshoot
    const next = offset + delta * PAGE;
    if (next < 0 || next >= total) return; // out of range (no empty page past the end)
    offset = next;
    // Page within the applied (URL) filter: rewrite only the offset param, so
    // draft filter edits stay un-applied until Apply is pressed.
    const qs = withOffset(get(page).url.searchParams, offset);
    goto(qs ? `?${qs}` : get(page).url.pathname, { keepFocus: true, noScroll: true });
  }
  function absTime(iso: string): string {
    const d = new Date(iso);
    return isNaN(d.getTime()) ? iso : d.toLocaleString();
  }
  const from = $derived(total === 0 ? 0 : offset + 1);
  const to = $derived(Math.min(offset + list.length, total));
  // Empty-state copy: a true "no decisions at all" keeps the onboarding message; an
  // empty result under the persona's lens (e.g. a developer's failed-only view with no
  // failures) shows the persona's own message when it provides one.
  const noFilters = $derived(!fFlow && !fEnv && !fStatus && !fVariant && !fQuery);
  const emptyTitle = $derived(
    total === 0 && noFilters
      ? 'No decisions yet'
      : (lens.empty?.title ?? 'No decisions match these filters')
  );
  const emptyHint = $derived(
    total === 0 && noFilters ? DEFAULT_EMPTY_HINT : (lens.empty?.hint ?? NO_MATCH_HINT)
  );
  // The URL drives the view: afterNavigate fires on mount, on Apply (goto), and on
  // back/forward — hydrate the inputs from the query string, falling back to the
  // persona lens defaults when a given filter is absent, then fetch.
  afterNavigate(() => {
    const sp = get(page).url.searchParams;
    // The persona lens only seeds a pristine URL (no filter params). Once any filter
    // is in the URL, an absent param means "explicitly cleared", not "use the default".
    const pristine = !['flow', 'env', 'status', 'variant', 'q'].some((k) => sp.has(k));
    fFlow = sp.get('flow') ?? '';
    fEnv = (sp.get('env') as Environment | '') || (pristine ? (lens.env ?? '') : '');
    fStatus = (sp.get('status') as DecisionStatus | '') || (pristine ? (lens.status ?? '') : '');
    fVariant = (sp.get('variant') as Variant | '') || (pristine ? (lens.variant ?? '') : '');
    fQuery = sp.get('q') ?? '';
    offset = Number(sp.get('offset') ?? '0') || 0;
    applied = {
      flow: fFlow.trim() || undefined,
      env: fEnv || undefined,
      status: fStatus || undefined,
      variant: fVariant || undefined,
      q: fQuery.trim() || undefined
    };
    void load();
  });
</script>

<main>
  <div class="head">
    <div class="head-titles">
      <h1>{heading}</h1>
      <p class="subtitle">
        Every determination the engine recorded — replayable, node by node.
        {#if !loading && total > 0}<span class="count-pill"
            >{total} match{total === 1 ? '' : 'es'}</span
          >{/if}
      </p>
    </div>
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
      >Filter by ID <input
        bind:value={fQuery}
        placeholder="id substring"
        aria-label="filter by decision id substring"
      /></label
    >
    <button type="submit" disabled={loading}><Icon name="search" size={14} /> Apply</button>
  </form>

  {#if error}<p class="err">{error}</p>{/if}

  {#if loading}
    <Skeleton rows={6} />
  {:else if list.length === 0}
    <EmptyState icon="diagram" title={emptyTitle} hint={emptyHint}>
      {#snippet action()}
        <a href={appHref('/engine')}>Open the Decision Engine →</a>
      {/snippet}
    </EmptyState>
  {:else}
    <div class="table-wrap">
      <table>
        <thead>
          <tr>
            {#each columns as col (col)}<th class:num={col === 'version' || col === 'duration'}
                >{columnLabel(col)}</th
              >{/each}
          </tr>
        </thead>
        <tbody>
          {#each list as d (d.decision_id)}
            <tr>
              {#each columns as col (col)}
                {#if col === 'status'}
                  <td><Badge tone={statusTone(d.status)}>{d.status}</Badge></td>
                {:else if col === 'disposition'}
                  <td>
                    {#if d.disposition}<Badge tone={dispositionTone(d.disposition)}
                        >{d.disposition}</Badge
                      >{:else}<span class="muted">—</span>{/if}
                  </td>
                {:else if col === 'flow'}
                  <td><a href={appHref(`/decisions/${d.decision_id}`)}>{d.slug}</a></td>
                {:else if col === 'env'}
                  <td>{d.environment}</td>
                {:else if col === 'version'}
                  <td class="num">v{d.version}</td>
                {:else if col === 'variant'}
                  <td class="muted">{d.variant ?? '—'}</td>
                {:else if col === 'duration'}
                  <td class="num">{d.duration_ms != null ? `${d.duration_ms} ms` : '—'}</td>
                {:else if col === 'when'}
                  <td class="muted" title={absTime(d.started_at)}
                    ><RelativeTime value={d.started_at} /></td
                  >
                {/if}
              {/each}
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
    max-width: 72rem;
    margin: 2rem auto;
    padding: 0 1.25rem;
  }
  .head {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 1rem;
  }
  .head-titles h1 {
    margin: 0;
  }
  .subtitle {
    margin: 0.15rem 0 0;
    color: var(--fg-muted);
    font-size: 0.9rem;
    display: flex;
    align-items: center;
    gap: 0.5rem;
    flex-wrap: wrap;
  }
  .count-pill {
    padding: 0.05rem 0.5rem;
    border-radius: 999px;
    font-size: 0.78rem;
    font-weight: 600;
    background: var(--surface-2);
    color: var(--fg-muted);
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
  tbody tr:hover {
    background: var(--surface-2);
  }
  th.num,
  td.num {
    text-align: right;
    font-variant-numeric: tabular-nums;
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
