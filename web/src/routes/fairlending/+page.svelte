<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { onMount } from 'svelte';
  import EmptyState from '$lib/EmptyState.svelte';
  import Skeleton from '$lib/Skeleton.svelte';
  import Badge from '$lib/Badge.svelte';
  import Hint from '$lib/Hint.svelte';
  import { pct } from '$lib/dashboard';
  import {
    listFlows,
    getFairLendingReport,
    fairLendingReportText,
    ApiError,
    type Flow,
    type FairLendingReport,
    type FairLendingParams
  } from '$lib/api';
  import { toast } from '$lib/toast';

  const key = '';
  let flows = $state<Flow[]>([]);
  let flow = $state('');
  let attribute = $state('');
  let favorable = $state('approve');
  let env = $state('');

  let report = $state<FairLendingReport | null>(null);
  let loading = $state(true);
  let running = $state(false);
  let error = $state('');
  let forbidden = $state(false);

  // The report is admin-gated; probing the flow list surfaces a 403 up-front so the
  // page shows the restricted state instead of an empty form the user can't use.
  async function loadFlows() {
    loading = true;
    error = '';
    forbidden = false;
    try {
      flows = await listFlows(key);
      if (flows.length > 0) flow = flows[0].flow_id;
    } catch (e) {
      if (e instanceof ApiError && e.status === 403) forbidden = true;
      else error = e instanceof Error ? e.message : String(e);
    } finally {
      loading = false;
    }
  }

  function params(): FairLendingParams {
    return {
      flow,
      attribute: attribute.trim(),
      favorable,
      env: env.trim() || undefined
    };
  }

  async function run() {
    if (!flow || !attribute.trim()) {
      toast.error('Pick a flow and enter a protected-class attribute.');
      return;
    }
    running = true;
    error = '';
    try {
      report = await getFairLendingReport(key, params());
    } catch (e) {
      if (e instanceof ApiError && e.status === 403) forbidden = true;
      else error = e instanceof Error ? e.message : String(e);
    } finally {
      running = false;
    }
  }

  async function download(format: 'csv' | 'md'): Promise<void> {
    try {
      const text = await fairLendingReportText(key, params(), format);
      const blob = new Blob([text], {
        type: format === 'csv' ? 'text/csv' : 'text/markdown'
      });
      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = format === 'csv' ? 'disparate-impact.csv' : 'disparate-impact.md';
      a.click();
      setTimeout(() => URL.revokeObjectURL(url), 0);
      toast.success(`Downloaded ${a.download}`);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e));
    }
  }

  const verdict = $derived.by(() => {
    if (!report) return null;
    if (!report.two_groups) return { tone: 'neutral' as const, label: 'Insufficient groups' };
    return report.passes
      ? { tone: 'ok' as const, label: 'Passes (all groups ≥ 0.80)' }
      : { tone: 'danger' as const, label: 'Flagged — a group is below 0.80' };
  });

  function noteFor(g: FairLendingReport['groups'][number]): string {
    const parts: string[] = [];
    if (g.reference) parts.push('reference');
    if (g.flagged) parts.push('flagged');
    if (g.small_sample) parts.push('small sample');
    return parts.join(', ');
  }

  onMount(loadFlows);
</script>

<section>
  <h1>Fair lending</h1>
  <p class="lede">
    Disparate-impact analysis over a flow's recorded decisions: the
    <Hint label="adverse-impact ratio">
      Each protected-class group's favorable-outcome rate divided by the highest group's rate. The
      <b>four-fifths rule</b> (ECOA / Reg B) treats a ratio below 0.80 as the conventional trigger for
      a disparate-impact review — it is a screen, not a legal conclusion.</Hint
    >
    across a protected-class attribute. You choose which input field encodes the protected class; the
    system does not infer one. Only completed decisions with a favorable or decline outcome are scored.
  </p>

  {#if loading}
    <Skeleton rows={4} />
  {:else if forbidden}
    <EmptyState
      icon="shield"
      title="Restricted to the admin role"
      hint="The disparate-impact report breaks a flow's whole decision population down by a protected-class attribute, so it is available only to admins."
    />
  {:else}
    <div class="controls">
      <label>
        <span>Flow</span>
        <select bind:value={flow}>
          {#each flows as f (f.flow_id)}
            <option value={f.flow_id}>{f.name}</option>
          {/each}
        </select>
      </label>
      <label>
        <span>Protected attribute</span>
        <input bind:value={attribute} placeholder="applicant.gender" spellcheck="false" />
      </label>
      <label>
        <span>Favorable outcome</span>
        <select bind:value={favorable}>
          <option value="approve">approve</option>
          <option value="decline">decline</option>
          <option value="refer">refer</option>
        </select>
      </label>
      <label>
        <span>Environment <em>(optional)</em></span>
        <input bind:value={env} placeholder="all" spellcheck="false" />
      </label>
      <button class="btn primary" onclick={run} disabled={running || !flow}>
        {running ? 'Analysing…' : 'Run analysis'}
      </button>
    </div>

    {#if error}
      <p class="err">{error}</p>
    {/if}

    {#if report}
      {#if verdict}
        <div class="verdict">
          <Badge tone={verdict.tone}>{verdict.label}</Badge>
          <span class="stat">Scored <b>{report.decisions}</b></span>
          <span class="stat" class:warn={report.excluded > 0}
            >Excluded <b>{report.excluded}</b></span
          >
          {#if report.reference}
            <span class="stat">Reference <b>{report.reference}</b></span>
            <span class="stat">Lowest AIR <b>{report.min_air.toFixed(2)}</b></span>
          {/if}
        </div>
      {/if}

      {#if report.excluded > 0}
        <p class="note">
          {report.excluded} completed decision{report.excluded === 1 ? '' : 's'} were excluded — referred
          to a human, no disposition, or missing <code>{report.attribute}</code> in the input.
        </p>
      {/if}

      {#if report.groups.length === 0}
        <EmptyState
          icon="gauge"
          title="No decisions to score"
          hint="No completed decisions for this flow had a favorable/decline outcome with the attribute present. Check the attribute path, or run more decisions."
        />
      {:else}
        <div class="row">
          <button class="btn" onclick={() => download('csv')}>Export CSV</button>
          <button class="btn" onclick={() => download('md')}>Export Markdown</button>
        </div>
        <div class="table-wrap">
          <table>
            <thead>
              <tr>
                <th>Group</th><th>Decisions</th><th>Favorable</th><th>Rate</th><th>AIR</th><th
                  >Note</th
                >
              </tr>
            </thead>
            <tbody>
              {#each report.groups as g (g.value)}
                <tr class:flagged={g.flagged} class:reference={g.reference}>
                  <td>{g.value}</td>
                  <td>{g.total}</td>
                  <td>{g.favorable}</td>
                  <td>{pct(g.rate)}</td>
                  <td class="air">{g.air.toFixed(2)}</td>
                  <td>
                    {#if g.reference}<Badge tone="neutral">reference</Badge>{/if}
                    {#if g.flagged}<Badge tone="danger">flagged</Badge>{/if}
                    {#if g.small_sample}<span class="muted" title="fewer than 30 scored decisions"
                        >small sample</span
                      >{/if}
                    {#if !noteFor(g)}<span class="muted">—</span>{/if}
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {/if}
    {/if}
  {/if}
</section>

<style>
  section {
    max-width: 64rem;
    margin: 2rem auto;
    padding: 0 1.25rem;
  }
  h1 {
    margin-bottom: 0.25rem;
  }
  .lede {
    color: var(--fg-muted);
    margin-top: 0;
  }
  .controls {
    display: flex;
    flex-wrap: wrap;
    gap: 0.75rem;
    align-items: flex-end;
    margin: 1rem 0;
    padding: 0.8rem;
    background: var(--surface-2);
    border-radius: 6px;
  }
  .controls label {
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
    font-size: 0.82rem;
    color: var(--fg-muted);
  }
  .controls label em {
    color: var(--fg-subtle);
    font-style: normal;
  }
  .controls input,
  .controls select {
    font: inherit;
    padding: 0.35rem 0.5rem;
    border: 1px solid var(--border);
    border-radius: 6px;
    background: var(--surface);
    color: var(--fg);
  }
  .btn {
    font: inherit;
    padding: 0.4rem 0.8rem;
    border: 1px solid var(--border);
    border-radius: 6px;
    background: var(--surface);
    color: var(--fg);
    cursor: pointer;
  }
  .btn.primary {
    background: var(--accent, #2563eb);
    border-color: var(--accent, #2563eb);
    color: #fff;
  }
  .btn:disabled {
    opacity: 0.5;
    cursor: default;
  }
  .verdict {
    display: flex;
    gap: 1rem;
    flex-wrap: wrap;
    align-items: center;
    margin: 0.8rem 0;
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
  .stat.warn b {
    color: var(--danger);
  }
  .note {
    color: var(--fg-subtle);
    font-size: 0.85rem;
    margin: 0.3rem 0 0.6rem;
  }
  .row {
    display: flex;
    gap: 0.5rem;
    margin: 0.6rem 0;
  }
  table {
    border-collapse: collapse;
    width: 100%;
    font-size: 0.9rem;
  }
  th,
  td {
    text-align: left;
    padding: 0.4rem 0.6rem;
    border-bottom: 1px solid var(--border);
  }
  tr.flagged {
    background: var(--danger-bg, transparent);
  }
  tr.reference td {
    font-weight: 500;
  }
  .air {
    font-variant-numeric: tabular-nums;
  }
  .muted {
    color: var(--fg-subtle);
  }
  .err {
    color: var(--danger);
  }
</style>
