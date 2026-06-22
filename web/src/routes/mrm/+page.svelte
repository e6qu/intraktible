<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { onMount } from 'svelte';
  import EmptyState from '$lib/EmptyState.svelte';
  import Skeleton from '$lib/Skeleton.svelte';
  import { pct } from '$lib/dashboard';
  import { getMrmReport, type MrmReport } from '$lib/api';
  import { appHref } from '$lib/paths';

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

  const kindLabel: Record<string, string> = {
    flow: 'Flow',
    predictive_model: 'Model',
    agent: 'Agent'
  };

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
    <div class="summary" aria-label="inventory summary">
      <span class="stat">Models <b>{report.summary.total}</b></span>
      <span class="stat">Deployed <b>{report.summary.deployed}</b></span>
      <span class="stat" class:warn={report.summary.unvalidated > 0}
        >Unvalidated <b>{report.summary.unvalidated}</b></span
      >
      <span class="stat" class:warn={report.summary.with_issues > 0}
        >Open issues <b>{report.summary.with_issues}</b></span
      >
    </div>

    <div class="row">
      <a class="btn" href={appHref('/v1/mrm/report?format=csv')} download>Export CSV</a>
      <a class="btn" href={appHref('/v1/mrm/report?format=md')} download>Export Markdown</a>
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
              <th>Decisions</th><th>Success</th><th>Issues</th>
            </tr>
          </thead>
          <tbody>
            {#each report.models as m (m.kind + '/' + m.id)}
              <tr class:flagged={m.issues && m.issues.length > 0}>
                <td>{kindLabel[m.kind] ?? m.kind}</td>
                <td>{m.name || m.id}</td>
                <td>{m.version || '—'}</td>
                <td>{m.owner || '—'}</td>
                <td>
                  <span class="cov {m.validation.coverage}">{m.validation.coverage}</span>
                  {#if m.validation.has_assertions}<span class="muted"
                      >{m.validation.assertions_passed}/{m.validation.assertions_total} assert</span
                    >{/if}
                  {#if m.validation.has_eval_cases}<span class="muted"
                      >{m.validation.eval_cases} eval</span
                    >{/if}
                </td>
                <td>{m.monitoring.decisions}</td>
                <td>{m.monitoring.decisions > 0 ? pct(m.monitoring.success_rate) : '—'}</td>
                <td>
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
  .cov {
    text-transform: uppercase;
    font-size: 0.7rem;
    letter-spacing: 0.03em;
    padding: 0.05rem 0.4rem;
    border-radius: 999px;
    background: var(--surface-2);
  }
  .cov.tested {
    color: var(--ok, green);
  }
  .cov.failing,
  .cov.none {
    color: var(--danger);
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
