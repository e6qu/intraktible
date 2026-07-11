<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { onMount } from 'svelte';
  import EmptyState from '$lib/EmptyState.svelte';
  import Skeleton from '$lib/Skeleton.svelte';
  import Badge from '$lib/Badge.svelte';
  import Hint from '$lib/Hint.svelte';
  import { coverageTone } from '$lib/badge';
  import { pct } from '$lib/dashboard';
  import { getMrmReport, mrmReportText, type MrmReport } from '$lib/api';
  import { appHref } from '$lib/paths';
  import { toast } from '$lib/toast';

  const key = '';
  let report = $state<MrmReport | null>(null);
  let error = $state('');
  let forbidden = $state(false);
  let loading = $state(true);

  async function load() {
    loading = true;
    error = '';
    forbidden = false;
    try {
      report = await getMrmReport(key);
    } catch (e) {
      const m = e instanceof Error ? e.message : String(e);
      // The model-risk report is admin-only; surface that clearly, not as a raw 403.
      if (m.includes('admin') || m.includes('403')) {
        forbidden = true;
      } else {
        error = m;
      }
    } finally {
      loading = false;
    }
  }

  // Fetch the report bytes through the app's fetch (so the demo's window.fetch mock can
  // serve them) and offer a Blob download — an <a href="/v1/..."> would be a browser
  // navigation that escapes the mock and 404s on the static demo host.
  async function downloadReport(format: 'csv' | 'md'): Promise<void> {
    try {
      const blob = new Blob([await mrmReportText(key, format)], {
        type: format === 'csv' ? 'text/csv' : 'text/markdown'
      });
      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = format === 'csv' ? 'mrm-report.csv' : 'mrm-report.md';
      a.click();
      setTimeout(() => URL.revokeObjectURL(url), 0);
      toast.success(`Downloaded ${a.download}`);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e));
    }
  }

  // Each inventory row drills through to its underlying entity so a gap is actionable.
  function entityHref(kind: string, id: string): string | null {
    if (kind === 'flow') return appHref(`/engine/${id}`);
    if (kind === 'agent') return appHref(`/agents/${encodeURIComponent(id)}`);
    if (kind === 'predictive_model') return appHref('/models');
    return null;
  }

  const kindLabel: Record<string, string> = {
    flow: 'Flow',
    predictive_model: 'Model',
    agent: 'Agent'
  };

  // Order the per-kind summary breakdown by the inventory's own kind sequence so
  // Flows/Models/Agents read consistently rather than in Object-key order, and
  // resolve the display label here (a Map keeps the object-injection lint happy).
  const KIND_ORDER = ['flow', 'predictive_model', 'agent'];
  const kindLabelMap = new Map(Object.entries(kindLabel));
  function byKindRows(by: Record<string, number>) {
    return Object.entries(by)
      .sort(([a], [b]) => (KIND_ORDER.indexOf(a) + 1 || 99) - (KIND_ORDER.indexOf(b) + 1 || 99))
      .map(([kind, n]) => ({ kind, n, label: kindLabelMap.get(kind) ?? kind }));
  }

  onMount(load);
</script>

<section>
  <h1>Model risk</h1>
  <p class="lede">
    SR 11-7 / SS1/23 model inventory — every decision flow, predictive model, and agent, with its
    validation evidence, live monitoring, and open governance gaps.
  </p>

  {#if error}<p class="err">{error}</p>{/if}

  {#if loading}
    <Skeleton rows={6} />
  {:else if forbidden}
    <EmptyState
      icon="shield"
      title="Restricted to the admin role"
      hint="The model-risk report aggregates every model across the workspace, so it is available only to admins. Ask an admin to share the exported report."
    />
  {:else if report}
    <p class="asof">
      As of <time datetime={report.generated_at}
        >{new Date(report.generated_at).toLocaleString()}</time
      >
    </p>
    <div class="summary" aria-label="inventory summary">
      <span class="stat">Models <b>{report.summary.total}</b></span>
      {#each byKindRows(report.summary.by_kind) as k (k.kind)}
        <span class="stat sub">{k.label} <b>{k.n}</b></span>
      {/each}
      <span class="stat">Deployed <b>{report.summary.deployed}</b></span>
      <span class="stat" class:warn={report.summary.unvalidated > 0}
        >Unvalidated <b>{report.summary.unvalidated}</b></span
      >
      <span class="stat" class:warn={report.summary.with_issues > 0}
        >Open issues <b>{report.summary.with_issues}</b></span
      >
    </div>

    <div class="row">
      <button class="btn" onclick={() => downloadReport('csv')}>Export CSV</button>
      <button class="btn" onclick={() => downloadReport('md')}>Export Markdown</button>
    </div>

    {#if report.models.length === 0}
      <EmptyState
        icon="shield"
        title="No models yet"
        hint="Publish a flow, register a predictive model, or define an agent — each becomes an inventoried model here with its validation and monitoring evidence."
      />
    {:else}
      <div class="table-wrap">
        <table>
          <thead>
            <tr>
              <th>Kind</th><th>Model</th><th>Ver</th><th>Owner</th><th>Validation</th>
              <th>Decisions</th><th>Success</th><th
                ><Hint label="Monitoring"
                  >Live health signals per model: <b>PSI</b> (Population Stability Index — how far
                  the recent score distribution has drifted from its baseline; &gt;0.2 fires), any
                  <b>firing</b>
                  custom monitors, and whether the <b>SLO</b>
                  (success-rate / latency target) is met.</Hint
                ></th
              ><th>Issues</th>
            </tr>
          </thead>
          <tbody>
            {#each report.models as m (m.kind + '/' + m.id)}
              <tr class:flagged={m.issues && m.issues.length > 0}>
                <td>{kindLabel[m.kind] ?? m.kind}</td>
                <td>
                  {#if entityHref(m.kind, m.id)}
                    <a href={entityHref(m.kind, m.id)}>{m.name || m.id}</a>
                  {:else}{m.name || m.id}{/if}
                </td>
                <td>{m.version || '—'}</td>
                <td>{m.owner || '—'}</td>
                <td>
                  <Badge tone={coverageTone(m.validation.coverage)}>{m.validation.coverage}</Badge>
                  {#if m.validation.has_assertions}<span class="muted"
                      >{m.validation.assertions_passed}/{m.validation.assertions_total} assert</span
                    >{/if}
                  {#if m.validation.has_eval_cases}<span class="muted"
                      >{m.validation.eval_cases} eval</span
                    >{/if}
                  {#if m.validation.shadow_diverged}<span
                      class="muted"
                      title="shadow runs that diverged from production"
                      >{m.validation.shadow_diverged} shadow Δ</span
                    >{/if}
                </td>
                <td>{m.monitoring.decisions}</td>
                <td>{m.monitoring.decisions > 0 ? pct(m.monitoring.success_rate) : '—'}</td>
                <td>
                  <div class="mon">
                    {#if m.monitoring.drift_psi !== undefined}
                      <span class="metric" title="population stability index"
                        >PSI {m.monitoring.drift_psi.toFixed(2)}</span
                      >
                      {#if m.monitoring.drift_firing}<Badge tone="danger">drift</Badge>{/if}
                    {/if}
                    {#if m.monitoring.firing_monitors && m.monitoring.firing_monitors.length > 0}
                      <Badge tone="danger" title={m.monitoring.firing_monitors.join(', ')}
                        >{m.monitoring.firing_monitors.length} firing</Badge
                      >
                    {/if}
                    {#if m.monitoring.slo_met !== undefined}
                      <Badge tone={m.monitoring.slo_met ? 'ok' : 'danger'}
                        >SLO {m.monitoring.slo_met ? 'met' : 'breach'}</Badge
                      >
                    {/if}
                    {#if m.monitoring.drift_psi === undefined && !(m.monitoring.firing_monitors && m.monitoring.firing_monitors.length) && m.monitoring.slo_met === undefined}
                      <span class="muted">—</span>
                    {/if}
                  </div>
                </td>
                <td class="issues">
                  {#if m.issues && m.issues.length > 0}
                    <span class="err">{m.issues.join('; ')}</span>
                  {:else}<span class="ok" role="img" aria-label="no open issues">✓</span>{/if}
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  {:else}
    <EmptyState
      icon="shield"
      title="No report available"
      hint="The model-risk report could not be loaded. Reload the page, or check that the workspace has any inventoried models."
    />
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
  .asof {
    color: var(--fg-subtle);
    font-size: 0.82rem;
    margin: 0 0 0.6rem;
  }
  .summary {
    display: flex;
    gap: 1rem;
    flex-wrap: wrap;
    margin: 0.6rem 0;
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
  .stat.sub {
    color: var(--fg-subtle);
    font-size: 0.82rem;
  }
  .stat.sub b {
    font-size: 0.95rem;
    color: var(--fg-muted);
  }
  .stat.warn b {
    color: var(--danger);
  }
  .row {
    display: flex;
    gap: 0.5rem;
    margin: 0.6rem 0;
  }
  .btn {
    font: inherit;
    padding: 0.35rem 0.7rem;
    border: 1px solid var(--border);
    border-radius: 6px;
    text-decoration: none;
    color: var(--fg);
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
    vertical-align: top;
  }
  tr.flagged {
    background: var(--danger-bg, transparent);
  }
  .mon {
    display: flex;
    flex-wrap: wrap;
    gap: 0.3rem;
    align-items: center;
    white-space: nowrap;
  }
  .issues {
    min-width: 12rem;
  }
  .metric {
    font-size: 0.78rem;
    color: var(--fg-muted);
    font-variant-numeric: tabular-nums;
  }
  .muted {
    color: var(--fg-subtle);
    margin-left: 0.3rem;
  }
  .err {
    color: var(--danger);
  }
  .ok {
    color: var(--ok, green);
  }
</style>
