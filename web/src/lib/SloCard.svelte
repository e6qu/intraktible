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
  import { getFlowSLO, putFlowSLO, type SLOResponse } from '$lib/api';

  let { flowId, name, initial }: { flowId: string; name: string; initial: SLOResponse } = $props();

  const key = '';
  // Show the parent-provided objective until a local save/clear replaces it (deriving
  // avoids seeding $state directly from a prop, which Svelte flags).
  let override = $state<SLOResponse | null>(null);
  const r = $derived(override ?? initial);
  let targetPct = $state(99);
  let latencyMs = $state(0);
  let busy = $state(false);
  let error = $state('');

  async function save() {
    if (busy) return;
    busy = true;
    error = '';
    try {
      await putFlowSLO(key, flowId, {
        success_target: targetPct / 100,
        latency_target_ms: latencyMs
      });
      override = await getFlowSLO(key, flowId);
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      busy = false;
    }
  }

  async function clear() {
    if (busy) return;
    busy = true;
    error = '';
    try {
      await putFlowSLO(key, flowId, { success_target: 0, latency_target_ms: 0 });
      override = await getFlowSLO(key, flowId);
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      busy = false;
    }
  }
</script>

<div class="slo-card">
  <div class="slo-head">
    <a href={`/engine/${flowId}`}>{name}</a>
    {#if r.attainment}
      <span class="badge {r.attainment.success_met ? 'ok' : 'bad'}">
        {r.attainment.success_met ? 'meeting' : 'breaching'}
      </span>
    {/if}
  </div>
  {#if error}<p class="err">{error}</p>{/if}
  {#if r.slo && r.attainment}
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
    <button class="link" onclick={clear} disabled={busy}>Clear objective</button>
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
      <button onclick={save} disabled={busy}>{busy ? 'Saving…' : 'Set objective'}</button>
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
  .err {
    color: var(--danger);
  }
</style>
