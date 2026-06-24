<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { onMount } from 'svelte';
  import EmptyState from '$lib/EmptyState.svelte';
  import Skeleton from '$lib/Skeleton.svelte';
  import SloCard from '$lib/SloCard.svelte';
  import { compact } from '$lib/dashboard';
  import {
    listFlows,
    getRunSummary,
    getFlowSLO,
    type Flow,
    type RunSummary,
    type SLOResponse
  } from '$lib/api';

  // API calls authenticate via the session cookie (empty key -> no X-Api-Key header).
  const key = '';
  let summary = $state<RunSummary | null>(null);
  let flows = $state<Flow[]>([]);
  // Map (not a plain object) so per-flow lookups don't trip the object-injection lint.
  let slos = $state<Map<string, SLOResponse>>(new Map());
  let error = $state('');
  let loading = $state(true);

  async function load() {
    loading = true;
    error = '';
    try {
      const [s, fl] = await Promise.all([getRunSummary(key), listFlows(key)]);
      summary = s;
      flows = fl;
      const entries = await Promise.all(
        fl.map(async (f) => [f.flow_id, await getFlowSLO(key, f.flow_id)] as const)
      );
      slos = new Map(entries);
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      loading = false;
    }
  }

  const usd = (n: number) => `$${n < 1 ? n.toFixed(4) : n.toFixed(2)}`;

  // Sort model usage rows by total tokens, descending — the costliest first. Cost is
  // looked up via a Map (computed object indexing trips the object-injection lint).
  const modelRows = $derived.by(() => {
    const costs = new Map(Object.entries(summary?.cost_by_model ?? {}));
    return Object.entries(summary?.by_model ?? {})
      .map(([model, u]) => ({
        model,
        ...u,
        total: u.prompt_tokens + u.completion_tokens,
        cost: costs.get(model)
      }))
      .sort((a, b) => b.total - a.total);
  });

  onMount(load);
</script>

<section>
  <h1>Observability</h1>
  <p class="lede">
    Service-level objectives, AI usage &amp; cost, and request tracing — the operational view across
    every flow.
  </p>

  {#if error}<p class="err">{error}</p>{/if}

  {#if loading}
    <Skeleton rows={6} />
  {:else}
    <h2>AI usage &amp; cost</h2>
    {#if summary}
      <div class="summary" aria-label="ai usage summary">
        <span class="stat">Runs <b>{compact(summary.total)}</b></span>
        <span class="stat">Prompt tokens <b>{compact(summary.prompt_tokens)}</b></span>
        <span class="stat">Completion tokens <b>{compact(summary.completion_tokens)}</b></span>
        {#if summary.priced}
          <span class="stat cost">Cost <b>{usd(summary.total_cost_usd)}</b></span>
        {/if}
      </div>
      {#if !summary.priced}
        <p class="muted note">
          Set <code>INTRAKTIBLE_AI_PRICES</code> (e.g. <code>claude-sonnet=3/15</code>, USD per
          million input/output tokens) to attribute cost. Token usage is shown regardless.
        </p>
      {/if}
      {#if modelRows.length > 0}
        <div class="table-wrap">
          <table>
            <thead>
              <tr>
                <th>Model</th><th>Runs</th><th>Prompt</th><th>Completion</th>
                {#if summary.priced}<th>Cost</th>{/if}
              </tr>
            </thead>
            <tbody>
              {#each modelRows as m (m.model)}
                <tr>
                  <td>{m.model}</td>
                  <td>{compact(m.runs)}</td>
                  <td>{compact(m.prompt_tokens)}</td>
                  <td>{compact(m.completion_tokens)}</td>
                  {#if summary.priced}<td>{m.cost === undefined ? '—' : usd(m.cost)}</td>{/if}
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {:else}
        <EmptyState
          icon="agents"
          title="No agent runs yet"
          hint="AI usage and cost appear here once a flow runs an Agent node or you invoke an agent."
        />
      {/if}
    {/if}

    <h2>Service-level objectives</h2>
    {#if flows.length === 0}
      <EmptyState
        icon="gauge"
        title="No flows yet"
        hint="Publish a flow, then set an availability and latency objective here to track attainment and error-budget burn."
      />
    {:else}
      <div class="slo-list">
        {#each flows as f (f.flow_id)}
          <SloCard
            flowId={f.flow_id}
            name={f.name}
            initial={slos.get(f.flow_id) ?? { slo: null }}
          />
        {/each}
      </div>
    {/if}

    <h2>Request tracing</h2>
    <p class="muted note">
      Distributed tracing is emitted via OpenTelemetry when <code>INTRAKTIBLE_OTEL_EXPORTER</code>
      is set (<code>stdout</code> or <code>otlp</code> to a collector). Each request carries an
      <code>X-Request-Id</code> response header that also tags its trace span, so logs and traces correlate.
      The decide path spans the flow, each connector/AI/model call, and every node.
    </p>
  {/if}
</section>

<style>
  section {
    max-width: 60rem;
    margin: 2rem auto;
    padding: 0 1.25rem;
  }
  h1 {
    margin-bottom: 0.25rem;
  }
  h2 {
    margin: 1.6rem 0 0.5rem;
    font-size: 1.05rem;
  }
  .lede {
    color: var(--fg-muted);
    margin-top: 0;
  }
  .summary {
    display: flex;
    gap: 1rem;
    flex-wrap: wrap;
    margin: 0.6rem 0 0.4rem;
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
  .stat.cost b {
    color: var(--accent, var(--fg));
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
  .slo-list {
    display: flex;
    flex-direction: column;
    gap: 0.6rem;
  }
  .note {
    font-size: 0.85rem;
  }
  .note code {
    background: var(--surface-2);
    padding: 0.05rem 0.3rem;
    border-radius: 4px;
  }
  .err {
    color: var(--danger);
  }
  .muted {
    color: var(--fg-subtle);
  }
</style>
