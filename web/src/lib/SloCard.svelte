<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  One flow's SLO card: shows attainment against the objective, or an inline editor
  to set one. Self-contained — it owns its objective state and persists via the API
  — so the Observability page just drops one per flow without per-row keyed state
  (which keeps the object-injection lint happy and the page simple).
-->
<script lang="ts">
  import Icon from '$lib/Icon.svelte';
  import { pct } from '$lib/dashboard';
  import { toast } from '$lib/toast';
  import { getFlowSLO, putFlowSLO, type SLOResponse } from '$lib/api';
  import { appHref } from '$lib/paths';

  let { flowId, name, initial }: { flowId: string; name: string; initial: SLOResponse } = $props();

  const key = '';
  // Show the parent-provided objective until a local save/clear replaces it (deriving
  // avoids seeding $state directly from a prop, which Svelte flags).
  let override = $state<SLOResponse | null>(null);
  const r = $derived(override ?? initial);
  let editing = $state(false);
  let targetPct = $state(99);
  let latencyMs = $state(0);
  let busy = $state(false);

  // Guard the inputs: success target is a percentage in [0,100], latency >= 0.
  const valid = $derived(
    Number.isFinite(targetPct) &&
      targetPct >= 0 &&
      targetPct <= 100 &&
      Number.isFinite(latencyMs) &&
      latencyMs >= 0
  );

  // Edit seeds the form from the current objective (non-destructive — unlike the old
  // clear-then-re-enter flow).
  function edit() {
    if (r.slo) {
      targetPct = Math.round(r.slo.success_target * 1000) / 10;
      latencyMs = r.slo.latency_target_ms;
    }
    editing = true;
  }

  async function save() {
    if (busy || !valid) return;
    busy = true;
    try {
      await putFlowSLO(key, flowId, {
        success_target: targetPct / 100,
        latency_target_ms: latencyMs
      });
      override = await getFlowSLO(key, flowId);
      editing = false;
      toast.success(`Objective set for ${name}`);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e));
    } finally {
      busy = false;
    }
  }

  async function clear() {
    if (busy) return;
    if (!confirm(`Clear the SLO objective for ${name}?`)) return;
    busy = true;
    try {
      await putFlowSLO(key, flowId, { success_target: 0, latency_target_ms: 0 });
      override = await getFlowSLO(key, flowId);
      editing = false;
      toast.success(`Objective cleared for ${name}`);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e));
    } finally {
      busy = false;
    }
  }
</script>

<div class="slo-card">
  <div class="slo-head">
    <a href={appHref(`/engine/${flowId}`)}>{name}</a>
    {#if r.attainment}
      <span class="badge {r.attainment.success_met ? 'ok' : 'bad'}">
        {r.attainment.success_met ? 'meeting' : 'breaching'}
      </span>
    {/if}
  </div>
  {#if r.slo && r.attainment && !editing}
    <div class="attain">
      <span class="stat"
        >Success <b>{pct(r.attainment.success_rate)}</b>
        <small>/ target {pct(r.attainment.success_target)}</small></span
      >
      <span class="stat"
        >Error budget
        <b class={r.attainment.budget_remaining < 0 ? 'over' : ''}
          >{pct(Math.max(0, r.attainment.budget_remaining))}</b
        >
        <small>left</small></span
      >
      {#if r.slo.latency_target_ms > 0}
        <span class="stat"
          >Latency
          <b class={r.attainment.latency_met ? '' : 'over'}>{r.attainment.avg_latency_ms} ms</b>
          <small>/ ≤ {r.slo.latency_target_ms} ms</small></span
        >
      {/if}
      <span class="stat muted"><Icon name="diagram" /> {r.attainment.decisions} decisions</span>
    </div>
    <div
      class="budget-bar"
      role="img"
      aria-label={`error budget ${pct(Math.max(0, r.attainment.budget_remaining))} remaining`}
      title={`${pct(Math.max(0, r.attainment.budget_remaining))} of the error budget remaining`}
    >
      <span
        class="fill {r.attainment.budget_remaining < 0 ? 'over' : ''}"
        style:width={`${Math.min(100, Math.max(0, r.attainment.budget_remaining * 100))}%`}
      ></span>
    </div>
    <button class="link" onclick={edit} disabled={busy}>Edit objective</button>
    <button class="link" onclick={clear} disabled={busy}>Clear objective</button>
  {:else if !r.slo && !editing}
    <div class="no-obj">
      <p class="hint">
        No objective set — this flow's availability and latency aren't being tracked yet.
      </p>
      <button onclick={edit} disabled={busy}>Set objective</button>
    </div>
  {:else}
    <div class="slo-form">
      <label
        >Success target %
        <input type="number" min="0" max="100" step="0.1" bind:value={targetPct} /></label
      >
      <label
        >Latency target ms
        <input type="number" min="0" bind:value={latencyMs} placeholder="0 = none" /></label
      >
      <button onclick={save} disabled={busy || !valid}>{busy ? 'Saving…' : 'Set objective'}</button>
      {#if editing}<button class="link" onclick={() => (editing = false)} disabled={busy}
          >Cancel</button
        >{/if}
    </div>
  {/if}
</div>

<style>
  .slo-card {
    border: 1px solid var(--border);
    border-radius: 6px;
    padding: 0.7rem 0.9rem;
  }
  .slo-head {
    display: flex;
    align-items: center;
    gap: 0.6rem;
    font-weight: 600;
  }
  .attain {
    display: flex;
    gap: 1.2rem;
    flex-wrap: wrap;
    margin-top: 0.5rem;
  }
  .attain :global(svg) {
    width: 0.9em;
    height: 0.9em;
    vertical-align: -0.1em;
  }
  .budget-bar {
    height: 6px;
    border-radius: 999px;
    background: var(--surface-2);
    overflow: hidden;
    margin-top: 0.5rem;
  }
  .budget-bar .fill {
    display: block;
    height: 100%;
    background: var(--ok, #16a34a);
    border-radius: 999px;
  }
  .budget-bar .fill.over {
    background: var(--danger);
  }
  .no-obj {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.8rem;
    flex-wrap: wrap;
    margin-top: 0.5rem;
  }
  .no-obj .hint {
    margin: 0;
    font-size: 0.85rem;
    color: var(--fg-subtle);
  }
  .slo-form {
    display: flex;
    gap: 0.8rem;
    flex-wrap: wrap;
    align-items: flex-end;
    margin-top: 0.5rem;
  }
  .slo-form label {
    display: inline-flex;
    flex-direction: column;
    gap: 0.15rem;
    font-size: 0.78rem;
    color: var(--fg-subtle);
  }
  input,
  button {
    font: inherit;
    padding: 0.4rem 0.6rem;
  }
  .stat {
    color: var(--fg-muted);
    font-size: 0.9rem;
  }
  .stat b {
    color: var(--fg);
    font-size: 1.05rem;
  }
  .stat b.over {
    color: var(--danger);
  }
  .stat small {
    color: var(--fg-subtle);
  }
  .badge {
    display: inline-block;
    padding: 0.05rem 0.5rem;
    border-radius: 999px;
    font-size: 0.72rem;
    text-transform: uppercase;
    letter-spacing: 0.03em;
  }
  .badge.ok {
    background: var(--ok-bg, var(--surface-2));
    color: var(--ok, var(--fg-muted));
  }
  .badge.bad {
    background: var(--danger-bg, var(--surface-2));
    color: var(--danger);
  }
  button.link {
    background: none;
    border: none;
    color: var(--fg-subtle);
    cursor: pointer;
    padding: 0.3rem 0;
    margin-top: 0.4rem;
    text-decoration: underline;
  }
  .muted {
    color: var(--fg-subtle);
  }
</style>
