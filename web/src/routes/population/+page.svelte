<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { onMount } from 'svelte';
  import { goto } from '$app/navigation';
  import Badge from '$lib/Badge.svelte';
  import EmptyState from '$lib/EmptyState.svelte';
  import RelativeTime from '$lib/RelativeTime.svelte';
  import Skeleton from '$lib/Skeleton.svelte';
  import {
    createPopulationJob,
    listFlows,
    listPopulationJobs,
    type Environment,
    type Flow,
    type PopulationItemInput,
    type PopulationJobKind,
    type PopulationJobState,
    type PopulationJobSummary
  } from '$lib/api';
  import { appHref } from '$lib/paths';
  import { roleAtLeast } from '$lib/roles';
  import { user } from '$lib/session';
  import { toast } from '$lib/toast';

  const key = '';
  let jobs = $state<PopulationJobSummary[]>([]);
  let flows = $state<Flow[]>([]);
  let loading = $state(true);
  let error = $state('');
  let creating = $state(false);
  let showCreate = $state(false);
  let kind = $state<PopulationJobKind>('backtest');
  let slug = $state('');
  let environment = $state<Environment>('sandbox');
  let concurrency = $state(4);
  let maxAttempts = $state(3);
  let retentionDays = $state(30);
  let dataset = $state(
    '[\n  { "customer_id": "customer-001" },\n  { "customer_id": "customer-002" }\n]'
  );

  function message(value: unknown): string {
    return value instanceof Error ? value.message : String(value);
  }

  function tone(state: PopulationJobState): 'neutral' | 'ok' | 'warn' | 'danger' {
    if (state === 'completed') return 'ok';
    if (state === 'completed_with_errors' || state === 'cancelled') return 'danger';
    if (state === 'running' || state === 'cancelling' || state === 'paused') return 'warn';
    return 'neutral';
  }

  function progress(job: PopulationJobSummary): number {
    return job.total === 0 ? 0 : ((job.succeeded + job.failed) / job.total) * 100;
  }

  function parseDataset(): PopulationItemInput[] {
    const raw = JSON.parse(dataset) as unknown;
    if (!Array.isArray(raw) || raw.length === 0)
      throw new Error('Dataset must be a non-empty JSON array.');
    return raw.map((row, index) => {
      if (typeof row !== 'object' || row === null || Array.isArray(row)) {
        throw new Error(`Dataset row ${index + 1} must be a JSON object.`);
      }
      if ('data' in row) {
        const item = row as unknown as PopulationItemInput;
        if (typeof item.data !== 'object' || item.data === null || Array.isArray(item.data)) {
          throw new Error(`Dataset row ${index + 1} data must be an object.`);
        }
        return item;
      }
      return { data: row as Record<string, unknown> };
    });
  }

  async function load(): Promise<void> {
    loading = true;
    error = '';
    try {
      [jobs, flows] = await Promise.all([listPopulationJobs(key), listFlows(key)]);
      slug ||= flows[0]?.slug ?? '';
    } catch (reason) {
      error = message(reason);
    } finally {
      loading = false;
    }
  }

  async function create(): Promise<void> {
    if (creating) return;
    creating = true;
    error = '';
    try {
      const items = parseDataset();
      const result = await createPopulationJob(
        key,
        {
          kind,
          slug,
          environment,
          items,
          concurrency,
          max_attempts: maxAttempts,
          retention_days: retentionDays
        },
        globalThis.crypto.randomUUID()
      );
      toast.success(`Queued ${result.total} immutable population items`);
      await goto(appHref(`/population/${result.job_id}`));
    } catch (reason) {
      error = message(reason);
    } finally {
      creating = false;
    }
  }

  onMount(load);
</script>

<main>
  <header class="head">
    <div>
      <h1>Population jobs</h1>
      <p class="lede">
        Durable, version-pinned bulk decisions and record-free backtests with bounded multi-worker
        execution.
      </p>
    </div>
    {#if roleAtLeast($user?.role, 'operator')}
      <button class="primary" onclick={() => (showCreate = !showCreate)}>
        {showCreate ? 'Close setup' : 'New population job'}
      </button>
    {/if}
  </header>
  {#if error}<p class="err" role="alert">{error}</p>{/if}

  {#if showCreate}
    <form
      class="create"
      onsubmit={(event) => {
        event.preventDefault();
        void create();
      }}
    >
      <div class="form-grid">
        <label
          ><span>Job kind</span><select bind:value={kind}
            ><option value="backtest">Backtest — records no decisions</option><option
              value="decision">Decision — durable production-like records</option
            ></select
          ></label
        >
        <label
          ><span>Flow</span><select aria-label="Flow" bind:value={slug} required
            >{#each flows as flow (flow.flow_id)}<option value={flow.slug}
                >{flow.name} · {flow.slug}</option
              >{/each}</select
          ></label
        >
        <label
          ><span>Environment</span><select bind:value={environment}
            ><option value="sandbox">sandbox</option><option value="staging">staging</option><option
              value="production">production</option
            ></select
          ></label
        >
        <label
          ><span>Concurrency</span><input
            type="number"
            min="1"
            max="32"
            bind:value={concurrency}
          /></label
        >
        <label
          ><span>Maximum attempts</span><input
            type="number"
            min="1"
            max="10"
            bind:value={maxAttempts}
          /></label
        >
        <label
          ><span>Result retention days</span><input
            type="number"
            min="1"
            max="3650"
            bind:value={retentionDays}
          /></label
        >
      </div>
      <label class="dataset">
        <span>Dataset</span>
        <textarea bind:value={dataset} rows="12" aria-label="population dataset"></textarea>
        <small>
          Paste raw data rows, or item objects with data, entity_type/entity_id, business_reference,
          correlation_id, and metadata. The server snapshots the exact flow version and experiment
          arm for every row before work starts.
        </small>
      </label>
      <button class="primary" disabled={creating || !slug}
        >{creating ? 'Queueing…' : 'Create durable job'}</button
      >
    </form>
  {/if}

  {#if loading}
    <Skeleton rows={5} />
  {:else if jobs.length === 0}
    <EmptyState
      title="No population jobs"
      hint="Run a durable backtest or bulk decision job. Inputs and exact deployed versions remain replayable."
    />
  {:else}
    <div class="jobs">
      {#each jobs as job (job.job_id)}
        <a class="job" href={appHref(`/population/${job.job_id}`)}>
          <div class="job-head">
            <div><strong>{job.slug}</strong><span>{job.kind} · {job.environment}</span></div>
            <Badge tone={tone(job.state)}>{job.state.replaceAll('_', ' ')}</Badge>
          </div>
          <div class="bar"><i style={`width:${progress(job)}%`}></i></div>
          <div class="counts">
            <span>{job.succeeded} succeeded</span><span>{job.failed} failed</span><span
              >{job.running} running</span
            ><span>{job.pending} pending</span>
          </div>
          <small>{job.total} inputs · updated <RelativeTime value={job.updated_at} /></small>
        </a>
      {/each}
    </div>
  {/if}
</main>

<style>
  main {
    max-width: 1120px;
    margin: 0 auto;
    padding: 2rem;
  }
  .head,
  .job-head,
  .counts {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 1rem;
  }
  .lede,
  small,
  label span,
  .job-head span {
    color: var(--muted);
  }
  .create {
    border: 1px solid var(--border);
    border-radius: 12px;
    background: var(--surface);
    padding: 1.25rem;
    margin: 1rem 0 2rem;
  }
  .form-grid {
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    gap: 0.8rem;
  }
  label {
    display: grid;
    gap: 0.35rem;
  }
  input,
  select,
  textarea {
    width: 100%;
    box-sizing: border-box;
  }
  textarea {
    font-family: var(--font-mono);
  }
  .dataset {
    margin: 1rem 0;
  }
  .jobs {
    display: grid;
    gap: 0.8rem;
  }
  .job {
    display: grid;
    gap: 0.7rem;
    color: inherit;
    text-decoration: none;
    border: 1px solid var(--border);
    border-radius: 12px;
    background: var(--surface);
    padding: 1rem;
  }
  .job:hover {
    border-color: var(--accent);
  }
  .job-head > div {
    display: grid;
    gap: 0.2rem;
  }
  .counts {
    justify-content: flex-start;
    flex-wrap: wrap;
    color: var(--muted);
    font-size: 0.82rem;
  }
  .bar {
    height: 7px;
    overflow: hidden;
    border-radius: 999px;
    background: var(--surface-2);
  }
  .bar i {
    display: block;
    height: 100%;
    background: var(--accent);
  }
  @media (max-width: 720px) {
    main {
      padding: 1rem;
    }
    .form-grid {
      grid-template-columns: 1fr;
    }
    .head {
      align-items: flex-start;
    }
  }
</style>
