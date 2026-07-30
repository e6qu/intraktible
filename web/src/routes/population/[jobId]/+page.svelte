<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { onMount } from 'svelte';
  import { page } from '$app/stores';
  import Badge from '$lib/Badge.svelte';
  import RelativeTime from '$lib/RelativeTime.svelte';
  import Skeleton from '$lib/Skeleton.svelte';
  import {
    downloadPopulationResults,
    getPopulationJob,
    retryPopulationJob,
    transitionPopulationJob,
    type PopulationJob
  } from '$lib/api';
  import { appHref } from '$lib/paths';
  import { roleAtLeast } from '$lib/roles';
  import { user } from '$lib/session';
  import { toast } from '$lib/toast';

  const key = '';
  const id = $derived($page.params.jobId ?? '');
  let job = $state<PopulationJob | null>(null);
  let loading = $state(true);
  let busy = $state(false);
  let error = $state('');
  let reason = $state('');

  const terminal = $derived(
    job?.state === 'completed' ||
      job?.state === 'completed_with_errors' ||
      job?.state === 'cancelled' ||
      job?.state === 'expired'
  );
  const finished = $derived((job?.succeeded ?? 0) + (job?.failed ?? 0));
  const progress = $derived(job && job.total > 0 ? (finished / job.total) * 100 : 0);

  function message(value: unknown): string {
    return value instanceof Error ? value.message : String(value);
  }

  function tone(): 'neutral' | 'ok' | 'warn' | 'danger' {
    if (job?.state === 'completed') return 'ok';
    if (job?.state === 'completed_with_errors' || job?.state === 'cancelled') return 'danger';
    if (job?.state === 'running' || job?.state === 'paused' || job?.state === 'cancelling')
      return 'warn';
    return 'neutral';
  }

  async function load(background = false): Promise<void> {
    if (!background) loading = true;
    try {
      job = await getPopulationJob(key, id);
      error = '';
    } catch (reason) {
      error = message(reason);
    } finally {
      loading = false;
    }
  }

  async function transition(action: 'pause' | 'resume' | 'cancel'): Promise<void> {
    if (busy) return;
    busy = true;
    try {
      await transitionPopulationJob(key, id, action, reason);
      toast.success(`${action[0]?.toUpperCase()}${action.slice(1)} recorded`);
      reason = '';
      await load();
    } catch (cause) {
      toast.error(message(cause));
    } finally {
      busy = false;
    }
  }

  async function retry(): Promise<void> {
    if (busy) return;
    busy = true;
    try {
      await retryPopulationJob(key, id);
      toast.success('Failed items requeued with fresh attempt budgets');
      await load();
    } catch (cause) {
      toast.error(message(cause));
    } finally {
      busy = false;
    }
  }

  async function download(): Promise<void> {
    if (busy) return;
    busy = true;
    try {
      const blob = await downloadPopulationResults(key, id);
      const url = URL.createObjectURL(blob);
      const anchor = document.createElement('a');
      anchor.href = url;
      anchor.download = `population-${id}.ndjson`;
      anchor.click();
      setTimeout(() => URL.revokeObjectURL(url), 0);
      toast.success('Result manifest downloaded');
    } catch (cause) {
      toast.error(message(cause));
    } finally {
      busy = false;
    }
  }

  onMount(() => {
    void load();
    const timer = window.setInterval(() => {
      if (!terminal) void load(true);
    }, 1000);
    return () => window.clearInterval(timer);
  });
</script>

<main>
  <a class="back" href={appHref('/population')}>← Population jobs</a>
  {#if loading}
    <Skeleton rows={8} />
  {:else if error}
    <p class="err" role="alert">{error}</p>
  {:else if job}
    <header class="head">
      <div>
        <div class="eyebrow">{job.kind} · {job.environment}</div>
        <h1>{job.slug}</h1>
        <p class="lede">Immutable manifest <code>{job.manifest_hash}</code></p>
      </div>
      <Badge tone={tone()}>{job.state.replaceAll('_', ' ')}</Badge>
    </header>

    <section class="progress">
      <div class="bar"><i style={`width:${progress}%`}></i></div>
      <div class="metrics">
        <article><strong>{job.total}</strong><span>total</span></article>
        <article><strong>{job.succeeded}</strong><span>succeeded</span></article>
        <article><strong>{job.failed}</strong><span>failed</span></article>
        <article><strong>{job.running}</strong><span>running</span></article>
        <article><strong>{job.pending}</strong><span>pending</span></article>
      </div>
      <p>
        Concurrency {job.concurrency} · up to {job.max_attempts} attempts · created by {job.created_by}
        <RelativeTime value={job.created_at} /> · results expire <RelativeTime
          value={job.expires_at}
        />
      </p>
    </section>

    {#if roleAtLeast($user?.role, 'operator')}
      <section class="controls">
        <label
          ><span>Action reason</span><input
            bind:value={reason}
            placeholder="Operational reason"
          /></label
        >
        <div class="buttons">
          {#if job.state === 'queued' || job.state === 'running'}
            <button disabled={busy} onclick={() => transition('pause')}>Pause</button>
            <button disabled={busy} onclick={() => transition('cancel')}>Cancel</button>
          {:else if job.state === 'paused'}
            <button class="primary" disabled={busy} onclick={() => transition('resume')}
              >Resume</button
            >
            <button disabled={busy} onclick={() => transition('cancel')}>Cancel</button>
          {:else if job.state === 'completed_with_errors'}
            <button class="primary" disabled={busy} onclick={retry}>Retry failed items</button>
          {/if}
          {#if terminal && job.state !== 'expired'}
            <button disabled={busy} onclick={download}>Download NDJSON results</button>
          {/if}
        </div>
      </section>
    {/if}

    <section>
      <div class="section-head">
        <div>
          <h2>Item manifest and progress</h2>
          <p>
            Every row retains its exact version, stable experiment assignment, attempts, and
            terminal output.
          </p>
        </div>
      </div>
      <div class="table-wrap">
        <table>
          <thead
            ><tr
              ><th>#</th><th>State</th><th>Version / cohort</th><th>Attempt</th><th>Decision</th><th
                >Disposition / output</th
              ><th>Error</th></tr
            ></thead
          >
          <tbody>
            {#each job.items as item (item.index)}
              {@const manifest = job.manifest.items[item.index]}
              <tr>
                <td>{item.index + 1}</td>
                <td
                  ><Badge
                    tone={item.state === 'succeeded'
                      ? 'ok'
                      : item.state === 'failed'
                        ? 'danger'
                        : item.state === 'claimed'
                          ? 'warn'
                          : 'neutral'}>{item.state}</Badge
                  ></td
                >
                <td>
                  v{manifest?.version}
                  {#if manifest?.assignment}
                    <small
                      >{manifest.assignment.arm_name} · cohort #{manifest.assignment.cohort}</small
                    >
                  {/if}
                </td>
                <td
                  >{item.attempt}{#if item.worker}<small>{item.worker}</small>{/if}</td
                >
                <td>
                  {#if item.decision_id}<a href={appHref(`/decisions/${item.decision_id}`)}
                      >{item.decision_id.slice(0, 12)}</a
                    >{:else}—{/if}
                </td>
                <td
                  >{item.disposition ?? item.status ?? '—'}{#if item.output}<small
                      ><code>{JSON.stringify(item.output)}</code></small
                    >{/if}</td
                >
                <td class="failure">{item.error ?? '—'}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    </section>
  {/if}
</main>

<style>
  main {
    max-width: 1180px;
    margin: 0 auto;
    padding: 2rem;
  }
  .back {
    display: inline-block;
    margin-bottom: 1rem;
  }
  .head,
  .section-head,
  .buttons {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 1rem;
  }
  .eyebrow,
  .lede,
  small,
  section p,
  label span {
    color: var(--muted);
  }
  .eyebrow {
    text-transform: uppercase;
    letter-spacing: 0.08em;
    font-size: 0.72rem;
  }
  .lede code {
    overflow-wrap: anywhere;
  }
  section {
    border: 1px solid var(--border);
    border-radius: 12px;
    background: var(--surface);
    padding: 1.25rem;
    margin-top: 1rem;
  }
  .bar {
    height: 9px;
    overflow: hidden;
    border-radius: 999px;
    background: var(--surface-2);
  }
  .bar i {
    display: block;
    height: 100%;
    background: var(--accent);
    transition: width 0.2s ease;
  }
  .metrics {
    display: grid;
    grid-template-columns: repeat(5, 1fr);
    gap: 0.75rem;
    margin-top: 1rem;
  }
  .metrics article {
    display: grid;
    gap: 0.1rem;
  }
  .metrics strong {
    font-size: 1.5rem;
  }
  .metrics span {
    color: var(--muted);
    font-size: 0.78rem;
  }
  .controls {
    display: flex;
    align-items: end;
    justify-content: space-between;
    gap: 1rem;
  }
  label {
    display: grid;
    gap: 0.3rem;
    flex: 1;
  }
  input {
    width: 100%;
    box-sizing: border-box;
  }
  .table-wrap {
    overflow-x: auto;
  }
  table {
    width: 100%;
    border-collapse: collapse;
  }
  th,
  td {
    padding: 0.7rem;
    text-align: left;
    border-bottom: 1px solid var(--border);
    vertical-align: top;
  }
  td small {
    display: block;
    margin-top: 0.2rem;
    max-width: 28rem;
    overflow-wrap: anywhere;
  }
  .failure {
    max-width: 20rem;
    overflow-wrap: anywhere;
  }
  @media (max-width: 760px) {
    main {
      padding: 1rem;
    }
    .head,
    .controls {
      align-items: flex-start;
      display: grid;
    }
    .metrics {
      grid-template-columns: repeat(2, 1fr);
    }
    .buttons {
      justify-content: flex-start;
      flex-wrap: wrap;
    }
  }
</style>
