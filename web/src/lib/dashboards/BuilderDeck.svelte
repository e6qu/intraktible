<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Builder persona: the Workbench. Dense, instrument-panel layout for the
     developer/maintainer — versions, latency percentiles, pending deploys, and a
     live decision tape. Monospace numerals, hairline grid, signal accent. -->
<script lang="ts">
  import Icon from '$lib/Icon.svelte';
  import { decisionStats, deployStats, pct, type DashboardData } from '$lib/dashboard';

  let { data }: { data: DashboardData } = $props();

  const ds = $derived(decisionStats(data.decisions));
  const dep = $derived(deployStats(data.flows));
  const recent = $derived(
    // Newest first; localeCompare returns 0 for equal timestamps so the order is a
    // consistent total order (a comparator that never returns 0 sorts unstably).
    [...data.decisions].sort((a, b) => b.started_at.localeCompare(a.started_at)).slice(0, 9)
  );
  const attention = $derived(
    data.flows.filter((f) => (f.deployment_requests ?? []).some((r) => r.status === 'pending'))
  );

  function clock(iso: string): string {
    if (!iso) return '—';
    const d = new Date(iso);
    return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' });
  }
</script>

<main class="workbench tabular">
  <header class="bar">
    <div class="title">
      <span class="prompt">▸</span> command deck
    </div>
    <nav class="quick">
      <a href="/engine">flows</a>
      <a href="/decisions">decisions</a>
      <a href="/data">context</a>
      <a href="/agents">agents</a>
    </nav>
  </header>

  <section class="readouts">
    <div class="ro"><span class="k">flows</span><span class="v">{data.flows.length}</span></div>
    <div class="ro"><span class="k">live envs</span><span class="v">{dep.live}</span></div>
    <div class="ro" class:alert={dep.pending > 0}>
      <span class="k">pending deploy</span><span class="v">{dep.pending}</span>
    </div>
    <div class="ro"><span class="k">decisions</span><span class="v">{ds.total}</span></div>
    <div class="ro">
      <span class="k">p50</span><span class="v">{ds.p50Ms}<small>ms</small></span>
    </div>
    <div class="ro">
      <span class="k">p95</span><span class="v">{ds.p95Ms}<small>ms</small></span>
    </div>
    <div class="ro" class:alert={ds.failed > 0}>
      <span class="k">failed</span><span class="v">{ds.failed}</span>
    </div>
    <div class="ro">
      <span class="k">success</span><span class="v">{ds.total ? pct(ds.completionRate) : '—'}</span>
    </div>
  </section>

  <div class="grid">
    <section class="panel tape">
      <h2>decision tape</h2>
      {#if recent.length === 0}
        <p class="empty">no decisions yet — run a flow from the <a href="/engine">engine</a>.</p>
      {:else}
        <table>
          <tbody>
            {#each recent as d (d.decision_id)}
              <tr>
                <td class="st"><span class="dot {d.status}"></span></td>
                <td class="slug"><a href={`/decisions/${d.decision_id}`}>{d.slug}</a></td>
                <td class="dim">{d.environment}</td>
                <td class="dim">v{d.version}</td>
                <td class="num">{d.duration_ms ?? 0}<small>ms</small></td>
                <td class="dim time">{clock(d.started_at)}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      {/if}
    </section>

    <section class="panel">
      <h2>needs review</h2>
      {#if attention.length === 0}
        <p class="empty ok"><Icon name="check" size={14} /> no pending approvals.</p>
      {:else}
        <ul class="attn">
          {#each attention as f (f.flow_id)}
            <li>
              <a href={`/engine/${f.flow_id}`}>{f.slug}</a>
              <span class="badge"
                >{(f.deployment_requests ?? []).filter((r) => r.status === 'pending').length} pending</span
              >
            </li>
          {/each}
        </ul>
      {/if}
      <h2 class="mt">shortcuts</h2>
      <div class="shortcuts">
        <a class="sc" href="/engine"><Icon name="plus" size={14} /> new flow</a>
        <a class="sc" href="/decisions"><Icon name="diagram" size={14} /> trace runs</a>
        <a class="sc" href="/data"><Icon name="database" size={14} /> context data</a>
      </div>
    </section>
  </div>
</main>

<style>
  .workbench {
    max-width: 76rem;
    margin: 1.25rem auto;
    padding: 0 1.25rem 2.5rem;
    font-family: var(--font-mono);
  }
  .bar {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 1rem;
    flex-wrap: wrap;
    padding-bottom: 0.75rem;
    border-bottom: 1px solid var(--border);
  }
  .title {
    font-family: var(--font-mono);
    font-size: 1.05rem;
    font-weight: 600;
    letter-spacing: 0.02em;
  }
  .prompt {
    color: var(--accent-ink);
  }
  .quick {
    display: flex;
    gap: 1rem;
    font-size: 0.85rem;
  }
  .quick a {
    color: var(--fg-muted);
  }
  .quick a:hover {
    color: var(--accent-ink);
  }

  .readouts {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(7rem, 1fr));
    gap: 1px;
    margin: 1rem 0 1.25rem;
    background: var(--border);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    overflow: hidden;
  }
  .ro {
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
    padding: 0.7rem 0.85rem;
    background: var(--surface);
    /* subtle instrument grid */
    background-image: linear-gradient(var(--hairline) 1px, transparent 1px);
    background-size: 100% 9px;
  }
  .ro .k {
    font-size: 0.66rem;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    color: var(--fg-subtle);
  }
  .ro .v {
    font-size: 1.5rem;
    font-weight: 600;
    line-height: 1;
  }
  .ro .v small {
    font-size: 0.7rem;
    color: var(--fg-subtle);
    margin-left: 1px;
  }
  .ro.alert .v {
    color: var(--accent-ink);
  }

  .grid {
    display: grid;
    grid-template-columns: 1.7fr 1fr;
    gap: 1rem;
  }
  @media (max-width: 760px) {
    .grid {
      grid-template-columns: 1fr;
    }
  }
  /* Phone: trim the decision tape to status · slug · duration so it never forces
     horizontal page scroll (env, version, and time drop out). */
  @media (max-width: 560px) {
    .tape td.dim {
      display: none;
    }
  }
  .panel {
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--surface);
    padding: 0.9rem 1rem 1.1rem;
  }
  .panel h2 {
    margin: 0 0 0.5rem;
    font-family: var(--font-mono);
    font-size: 0.72rem;
    letter-spacing: 0.08em;
  }
  .panel h2.mt {
    margin-top: 1.25rem;
  }
  table {
    width: 100%;
    border-collapse: collapse;
  }
  td {
    padding: 0.4rem 0.4rem;
    border-bottom: 1px solid var(--border);
    font-size: 0.84rem;
    white-space: nowrap;
  }
  tr:last-child td {
    border-bottom: none;
  }
  .st {
    width: 1.2rem;
  }
  .dot {
    display: inline-block;
    width: 0.55rem;
    height: 0.55rem;
    border-radius: 999px;
    background: var(--fg-subtle);
  }
  .dot.completed {
    background: var(--ok);
  }
  .dot.failed {
    background: var(--danger);
  }
  .slug {
    width: 99%;
  }
  .slug a {
    color: var(--fg);
    font-weight: 500;
  }
  .slug a:hover {
    color: var(--accent-ink);
  }
  .dim {
    color: var(--fg-muted);
  }
  .num {
    text-align: right;
  }
  .num small {
    color: var(--fg-subtle);
    margin-left: 1px;
  }
  .time {
    text-align: right;
  }
  .empty {
    color: var(--fg-subtle);
    font-size: 0.85rem;
  }
  .empty.ok {
    color: var(--ok);
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
  }
  .attn {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .attn li {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.5rem;
    padding: 0.4rem 0;
    border-bottom: 1px solid var(--border);
    font-size: 0.85rem;
  }
  .badge {
    font-size: 0.7rem;
    padding: 0.1rem 0.45rem;
    border-radius: 999px;
    background: color-mix(in srgb, var(--accent) 16%, transparent);
    color: var(--accent-ink);
    font-weight: 600;
  }
  .shortcuts {
    display: flex;
    flex-direction: column;
    gap: 0.4rem;
  }
  .sc {
    display: inline-flex;
    align-items: center;
    gap: 0.45rem;
    padding: 0.45rem 0.6rem;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    color: var(--fg);
    font-size: 0.84rem;
  }
  .sc:hover {
    border-color: var(--accent);
    color: var(--accent-ink);
    text-decoration: none;
  }
</style>
