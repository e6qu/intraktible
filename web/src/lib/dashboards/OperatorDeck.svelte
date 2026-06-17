<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Operator persona: Mission Control. Calm KPI tiles for the risk/ops manager —
     throughput, queue health, SLA risk, maker-checker approvals, agent runs.
     Big readable numbers, status colour, comfortable spacing. -->
<script lang="ts">
  import Icon from '$lib/Icon.svelte';
  import { decisionStats, deployStats, pct, compact, type DashboardData } from '$lib/dashboard';

  let { data }: { data: DashboardData } = $props();

  const ds = $derived(decisionStats(data.decisions));
  const dep = $derived(deployStats(data.flows));
  const c = $derived(data.cases);
  const r = $derived(data.runs);
  const needsReview = $derived(c.by_status?.needs_review ?? 0);
  const runSuccess = $derived(r.total ? r.completed / r.total : 0);

  // Queue-health segments for the stacked bar (label, count, css class).
  const segments = $derived([
    { label: 'On track', n: Math.max(0, c.total - c.due_soon - c.overdue), cls: 'ok' },
    { label: 'Due soon', n: c.due_soon, cls: 'warn' },
    { label: 'Overdue', n: c.overdue, cls: 'danger' }
  ]);
</script>

<main class="ops">
  <header class="head">
    <div>
      <h1>Operations</h1>
      <p class="sub">Live health across decisioning, review, and agents.</p>
    </div>
  </header>

  {#if dep.pending > 0}
    <a class="callout" href="/engine">
      <Icon name="shield" size={18} />
      <span
        ><b>{dep.pending}</b> production deploy{dep.pending === 1 ? '' : 's'} awaiting four-eyes approval</span
      >
      <span class="go">Review →</span>
    </a>
  {/if}

  <section class="kpis">
    <a class="kpi" href="/decisions">
      <span class="kpi-label">Decisions</span>
      <span class="kpi-num">{compact(ds.total)}</span>
      <span class="kpi-foot {ds.failed ? 'warn' : 'ok'}"
        >{ds.total ? pct(ds.completionRate) + ' success' : 'no runs yet'} · {ds.failed} failed</span
      >
    </a>
    <a class="kpi" href="/cases">
      <span class="kpi-label">Cases in review</span>
      <span class="kpi-num">{needsReview}</span>
      <span class="kpi-foot {c.overdue ? 'danger' : 'muted'}"
        >{c.overdue} overdue · {c.unassigned} unassigned</span
      >
    </a>
    <a class="kpi" href="/agents">
      <span class="kpi-label">Agent runs</span>
      <span class="kpi-num">{compact(r.total)}</span>
      <span class="kpi-foot {r.failed ? 'warn' : 'ok'}"
        >{r.total ? pct(runSuccess) + ' success' : 'no runs yet'} · {r.failed} failed</span
      >
    </a>
    <a class="kpi" href="/engine">
      <span class="kpi-label">Live flows</span>
      <span class="kpi-num">{dep.live}</span>
      <span class="kpi-foot muted">of {data.flows.length} flows · p95 {ds.p95Ms}ms</span>
    </a>
  </section>

  <div class="grid">
    <section class="card">
      <h2>Case queue health</h2>
      {#if c.total === 0}
        <p class="empty">No open cases. The review queue is clear.</p>
      {:else}
        <div class="bar" role="img" aria-label="Case queue by SLA status">
          {#each segments as s (s.label)}
            {#if s.n > 0}
              <span class="seg {s.cls}" style="flex:{s.n}" title={`${s.label}: ${s.n}`}></span>
            {/if}
          {/each}
        </div>
        <ul class="legend">
          {#each segments as s (s.label)}
            <li><span class="key {s.cls}"></span>{s.label} <b>{s.n}</b></li>
          {/each}
        </ul>
        <p class="line">Total open <b>{c.total}</b> · unassigned <b>{c.unassigned}</b></p>
      {/if}
    </section>

    <section class="card">
      <h2>Governance &amp; throughput</h2>
      <dl class="stats">
        <div>
          <dt>Pending approvals</dt>
          <dd class={dep.pending ? 'warn' : 'ok'}>{dep.pending}</dd>
        </div>
        <div>
          <dt>Avg decision time</dt>
          <dd>{ds.avgMs} ms</dd>
        </div>
        <div>
          <dt>p95 latency</dt>
          <dd>{ds.p95Ms} ms</dd>
        </div>
        <div>
          <dt>Completed decisions</dt>
          <dd>{compact(ds.completed)}</dd>
        </div>
      </dl>
      <a class="audit-link" href="/audit"><Icon name="shield" size={14} /> Open the audit log</a>
    </section>
  </div>
</main>

<style>
  .ops {
    max-width: 72rem;
    margin: 2rem auto;
    padding: 0 1.25rem 3rem;
  }
  .head h1 {
    margin: 0;
  }
  .sub {
    color: var(--fg-muted);
    margin: 0.25rem 0 0;
  }
  .callout {
    display: flex;
    align-items: center;
    gap: 0.7rem;
    margin: 1.4rem 0 0;
    padding: 0.9rem 1.1rem;
    border-radius: var(--radius);
    border: 1px solid color-mix(in srgb, var(--warn) 45%, var(--border));
    background: color-mix(in srgb, var(--warn) 12%, var(--surface));
    color: var(--fg);
  }
  .callout :global(svg) {
    color: var(--warn);
  }
  .callout .go {
    margin-left: auto;
    color: var(--accent-ink);
    font-weight: 600;
  }
  .callout:hover {
    text-decoration: none;
    border-color: var(--warn);
  }

  .kpis {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(13rem, 1fr));
    gap: var(--gap);
    margin: 1.4rem 0;
  }
  .kpi {
    display: flex;
    flex-direction: column;
    gap: 0.35rem;
    padding: var(--pad-card);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--surface);
    box-shadow: var(--shadow);
    color: var(--fg);
    transition:
      border-color 0.15s ease,
      transform 0.15s ease;
  }
  .kpi:hover {
    text-decoration: none;
    border-color: var(--accent);
    transform: translateY(-2px);
  }
  .kpi-label {
    font-size: 0.8rem;
    color: var(--fg-muted);
    text-transform: uppercase;
    letter-spacing: 0.05em;
  }
  .kpi-num {
    font-size: 2.6rem;
    font-weight: 600;
    line-height: 1;
    letter-spacing: -0.02em;
    font-variant-numeric: tabular-nums;
  }
  .kpi-foot {
    font-size: 0.82rem;
    color: var(--fg-muted);
  }
  .kpi-foot.ok {
    color: var(--ok);
  }
  .kpi-foot.warn {
    color: var(--warn);
  }
  .kpi-foot.danger {
    color: var(--danger);
  }
  .kpi-foot.muted {
    color: var(--fg-subtle);
  }

  .grid {
    display: grid;
    grid-template-columns: 1.2fr 1fr;
    gap: var(--gap);
  }
  @media (max-width: 760px) {
    .grid {
      grid-template-columns: 1fr;
    }
  }
  .card {
    padding: var(--pad-card);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--surface);
    box-shadow: var(--shadow);
  }
  .card h2 {
    margin: 0 0 0.9rem;
    font-size: 0.95rem;
    text-transform: none;
    letter-spacing: 0;
    color: var(--fg);
    font-weight: 600;
  }
  .bar {
    display: flex;
    height: 0.85rem;
    border-radius: 999px;
    overflow: hidden;
    gap: 2px;
    background: var(--surface-2);
  }
  .seg.ok {
    background: var(--ok);
  }
  .seg.warn {
    background: var(--warn);
  }
  .seg.danger {
    background: var(--danger);
  }
  .legend {
    display: flex;
    flex-wrap: wrap;
    gap: 1rem;
    list-style: none;
    padding: 0;
    margin: 0.8rem 0 0;
    font-size: 0.85rem;
    color: var(--fg-muted);
  }
  .legend .key {
    display: inline-block;
    width: 0.7rem;
    height: 0.7rem;
    border-radius: 3px;
    margin-right: 0.35rem;
    vertical-align: -1px;
  }
  .key.ok {
    background: var(--ok);
  }
  .key.warn {
    background: var(--warn);
  }
  .key.danger {
    background: var(--danger);
  }
  .line {
    margin: 0.9rem 0 0;
    color: var(--fg-muted);
    font-size: 0.88rem;
  }
  .empty {
    color: var(--fg-subtle);
  }
  .stats {
    margin: 0;
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 0.9rem 1.2rem;
  }
  .stats div {
    display: flex;
    flex-direction: column;
    gap: 0.15rem;
  }
  .stats dt {
    font-size: 0.8rem;
    color: var(--fg-muted);
  }
  .stats dd {
    margin: 0;
    font-size: 1.5rem;
    font-weight: 600;
    font-variant-numeric: tabular-nums;
  }
  .stats dd.ok {
    color: var(--ok);
  }
  .stats dd.warn {
    color: var(--warn);
  }
  .audit-link {
    display: inline-flex;
    align-items: center;
    gap: 0.4rem;
    margin-top: 1.1rem;
    font-size: 0.88rem;
    color: var(--accent-ink);
  }
</style>
