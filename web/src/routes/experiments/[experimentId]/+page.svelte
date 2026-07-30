<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { onMount } from 'svelte';
  import { page } from '$app/stores';
  import Badge from '$lib/Badge.svelte';
  import RelativeTime from '$lib/RelativeTime.svelte';
  import Skeleton from '$lib/Skeleton.svelte';
  import {
    correctOutcome,
    decideExperimentLaunch,
    getExperiment,
    getExperimentAnalysis,
    listExperimentExposures,
    listOutcomes,
    promoteExperimentWinner,
    recordOutcome,
    transitionExperiment,
    updateExperiment,
    type BusinessOutcome,
    type Experiment,
    type ExperimentAnalysis,
    type ExperimentExposure,
    type OutcomeKind
  } from '$lib/api';
  import { appHref } from '$lib/paths';
  import { roleAtLeast } from '$lib/roles';
  import { user } from '$lib/session';
  import { toast } from '$lib/toast';

  const key = '';
  const id = $derived($page.params.experimentId ?? '');
  let experiment = $state<Experiment | null>(null);
  let analysis = $state<ExperimentAnalysis | null>(null);
  let exposures = $state<ExperimentExposure[]>([]);
  let outcomes = $state<BusinessOutcome[]>([]);
  let loading = $state(true);
  let busy = $state(false);
  let error = $state('');
  let reason = $state('');
  let editJSON = $state('');
  let editing = $state(false);

  let outcomeDecision = $state('');
  let outcomeKey = $state('');
  let outcomeKind = $state<OutcomeKind>('binary');
  let outcomeValue = $state(1);
  let outcomeTime = $state('');
  let outcomeWindow = $state(30);
  let sourceSystem = $state('');
  let sourceRecord = $state('');
  let sourceLineage = $state('');
  let labelVersion = $state('v1');

  let correction = $state<BusinessOutcome | null>(null);
  let correctionValue = $state(0);
  let correctionReason = $state('');

  function message(value: unknown): string {
    return value instanceof Error ? value.message : String(value);
  }

  function analysisTone(
    status: ExperimentAnalysis['status']
  ): 'neutral' | 'ok' | 'warn' | 'danger' {
    if (status === 'winner') return 'ok';
    if (status === 'invalid' || status === 'guardrail_failed') return 'danger';
    if (status === 'underpowered' || status === 'inconclusive') return 'warn';
    return 'neutral';
  }

  function percent(value: number): string {
    return `${(value * 100).toFixed(2)}%`;
  }

  function localNow(): string {
    const date = new Date();
    return new Date(date.getTime() - date.getTimezoneOffset() * 60_000).toISOString().slice(0, 16);
  }

  async function load(background = false): Promise<void> {
    if (!background) loading = true;
    error = '';
    try {
      experiment = await getExperiment(key, id);
      [analysis, exposures, outcomes] = await Promise.all([
        getExperimentAnalysis(key, id),
        listExperimentExposures(key, id),
        listOutcomes(key)
      ]);
      outcomes = outcomes.filter((outcome) => outcome.treatment?.experiment_id === id);
      editJSON = JSON.stringify(experiment.spec, null, 2);
      outcomeKey ||= experiment.spec.primary_metric.key;
      outcomeKind = experiment.spec.primary_metric.kind;
      outcomeWindow = experiment.spec.observation_window_days;
      outcomeDecision ||= exposures[0]?.decision_id ?? '';
      outcomeTime ||= localNow();
    } catch (reason) {
      error = message(reason);
    } finally {
      loading = false;
    }
  }

  async function lifecycle(
    action: 'start' | 'pause' | 'resume' | 'complete' | 'cancel'
  ): Promise<void> {
    if (busy) return;
    busy = true;
    try {
      const result = await transitionExperiment(key, id, action, reason);
      toast.success(
        result.request_id
          ? 'Production launch sent for independent review'
          : `Experiment ${action}d`
      );
      reason = '';
      await load();
    } catch (cause) {
      toast.error(message(cause));
    } finally {
      busy = false;
    }
  }

  async function launchDecision(decision: 'approve' | 'reject'): Promise<void> {
    if (!experiment?.launch || busy) return;
    if (!reason.trim()) {
      toast.error('A review reason is required.');
      return;
    }
    busy = true;
    try {
      await decideExperimentLaunch(key, id, experiment.launch.request_id, decision, reason);
      toast.success(decision === 'approve' ? 'Experiment launched' : 'Launch rejected');
      reason = '';
      await load();
    } catch (cause) {
      toast.error(message(cause));
    } finally {
      busy = false;
    }
  }

  async function saveDraft(): Promise<void> {
    if (!experiment || busy) return;
    busy = true;
    try {
      const parsed = JSON.parse(editJSON) as Experiment['spec'];
      await updateExperiment(key, id, parsed);
      toast.success('Draft updated; a new cohort namespace was created');
      editing = false;
      await load();
    } catch (cause) {
      toast.error(message(cause));
    } finally {
      busy = false;
    }
  }

  async function promote(): Promise<void> {
    if (analysis?.status !== 'winner' || busy) return;
    busy = true;
    try {
      const result = await promoteExperimentWinner(key, id);
      toast.success(`Deployment request opened for v${result.version}`);
    } catch (cause) {
      toast.error(message(cause));
    } finally {
      busy = false;
    }
  }

  async function record(): Promise<void> {
    if (busy) return;
    busy = true;
    try {
      await recordOutcome(
        key,
        {
          decision_id: outcomeDecision,
          key: outcomeKey.trim(),
          kind: outcomeKind,
          value: outcomeValue,
          event_time: new Date(outcomeTime).toISOString(),
          observation_window_days: outcomeWindow,
          source: {
            system: sourceSystem.trim(),
            record_id: sourceRecord.trim(),
            lineage: sourceLineage.trim() || undefined
          },
          label_version: labelVersion.trim()
        },
        globalThis.crypto.randomUUID()
      );
      toast.success('Business outcome attributed from immutable decision evidence');
      sourceRecord = '';
      await load();
    } catch (cause) {
      toast.error(message(cause));
    } finally {
      busy = false;
    }
  }

  function beginCorrection(outcome: BusinessOutcome): void {
    correction = outcome;
    correctionValue = outcome.current.value;
    correctionReason = '';
    outcomeTime = localNow();
    sourceSystem = outcome.current.source.system;
    sourceRecord = outcome.current.source.record_id;
    sourceLineage = outcome.current.source.lineage ?? '';
    labelVersion = outcome.current.label_version;
    outcomeWindow = outcome.current.observation_window_days ?? 0;
  }

  async function correct(): Promise<void> {
    if (!correction || busy) return;
    busy = true;
    try {
      await correctOutcome(
        key,
        correction.outcome_id,
        {
          value: correctionValue,
          event_time: new Date(outcomeTime).toISOString(),
          observation_window_days: outcomeWindow,
          source: {
            system: sourceSystem.trim(),
            record_id: sourceRecord.trim(),
            lineage: sourceLineage.trim() || undefined
          },
          label_version: labelVersion.trim(),
          reason: correctionReason.trim()
        },
        globalThis.crypto.randomUUID()
      );
      toast.success('Correction appended; prior revisions remain in the audit chain');
      correction = null;
      await load();
    } catch (cause) {
      toast.error(message(cause));
    } finally {
      busy = false;
    }
  }

  onMount(() => {
    void load();
    const timer = window.setInterval(() => {
      if (!busy && (experiment?.state === 'running' || experiment?.state === 'pending_launch')) {
        void load(true);
      }
    }, 5000);
    return () => window.clearInterval(timer);
  });
</script>

<main>
  <a class="back" href={appHref('/experiments')}>← Experiments</a>
  {#if loading}
    <Skeleton rows={8} />
  {:else if error}
    <p class="err" role="alert">{error}</p>
  {:else if experiment && analysis}
    <header class="head">
      <div>
        <div class="eyebrow">Cohort #{experiment.cohort} · {experiment.spec.environment}</div>
        <h1>{experiment.spec.name}</h1>
        <p class="lede">{experiment.spec.hypothesis}</p>
      </div>
      <Badge
        tone={experiment.state === 'running'
          ? 'ok'
          : experiment.state === 'cancelled'
            ? 'danger'
            : 'neutral'}>{experiment.state.replace('_', ' ')}</Badge
      >
    </header>

    <section class="control">
      <div>
        <strong>Lifecycle controls</strong>
        <p>
          Owner {experiment.spec.owner} · updated <RelativeTime value={experiment.updated_at} />
        </p>
        {#if experiment.spec.start_at || experiment.spec.stop_at}
          <p>
            Assignment window:
            {experiment.spec.start_at
              ? new Date(experiment.spec.start_at).toLocaleString()
              : 'immediately'}
            →
            {experiment.spec.stop_at
              ? new Date(experiment.spec.stop_at).toLocaleString()
              : 'no scheduled stop'}
          </p>
        {/if}
      </div>
      <input bind:value={reason} aria-label="action reason" placeholder="Reason for this action" />
      <div class="buttons">
        {#if experiment.state === 'draft' && roleAtLeast($user?.role, 'editor')}
          <button onclick={() => (editing = !editing)}>Edit configuration</button>
          <button class="primary" disabled={busy} onclick={() => lifecycle('start')}>Start</button>
          <button disabled={busy} onclick={() => lifecycle('cancel')}>Cancel</button>
        {:else if experiment.state === 'pending_launch' && experiment.launch}
          {#if roleAtLeast($user?.role, 'approver') && $user?.actor !== experiment.launch.requested_by && $user?.actor !== experiment.created_by}
            <button
              class="primary"
              disabled={busy || !reason.trim()}
              onclick={() => launchDecision('approve')}>Approve launch</button
            >
            <button disabled={busy || !reason.trim()} onclick={() => launchDecision('reject')}
              >Reject</button
            >
          {:else}
            <span class="muted">Waiting for an independent approver.</span>
          {/if}
          {#if roleAtLeast($user?.role, 'operator')}
            <button disabled={busy} onclick={() => lifecycle('cancel')}>Cancel request</button>
          {/if}
        {:else if experiment.state === 'running' && roleAtLeast($user?.role, 'operator')}
          <button disabled={busy} onclick={() => lifecycle('pause')}>Pause assignment</button>
          <button disabled={busy} onclick={() => lifecycle('complete')}>Stop & analyse</button>
          <button disabled={busy} onclick={() => lifecycle('cancel')}>Cancel</button>
        {:else if experiment.state === 'paused' && roleAtLeast($user?.role, 'operator')}
          <button class="primary" disabled={busy} onclick={() => lifecycle('resume')}>Resume</button
          >
          <button disabled={busy} onclick={() => lifecycle('complete')}>Complete</button>
          <button disabled={busy} onclick={() => lifecycle('cancel')}>Cancel</button>
        {/if}
      </div>
    </section>

    {#if editing}
      <section class="editor">
        <h2>Edit draft configuration</h2>
        <p>
          Saving validates every version, expression, allocation, KPI, guardrail, and time window.
        </p>
        <textarea bind:value={editJSON} rows="24" aria-label="experiment configuration JSON"
        ></textarea>
        <div class="buttons">
          <button class="primary" disabled={busy} onclick={saveDraft}>Save as new cohort</button>
        </div>
      </section>
    {/if}

    <section>
      <div class="section-head">
        <div>
          <h2>Evidence and analysis</h2>
          <p>
            Only reached treatments with coherent, current outcome lineage enter these calculations.
          </p>
        </div>
        <Badge tone={analysisTone(analysis.status)}>{analysis.status.replace('_', ' ')}</Badge>
      </div>
      <div class="verdict" class:winner={analysis.status === 'winner'}>
        {#if analysis.status === 'winner'}
          <strong>Winner: {analysis.winner_arm_key}</strong>
        {:else}
          <strong>No winner can be called</strong>
        {/if}
        <span>{analysis.reason}</span>
      </div>
      <div class="metrics">
        {#each experiment.spec.arms as arm (arm.key)}
          {@const result = analysis.primary.arms.find((item) => item.arm_key === arm.key)}
          <article>
            <span>{arm.name} · v{arm.version}</span>
            <strong
              >{result
                ? analysis.primary.metric.kind === 'binary'
                  ? percent(result.mean)
                  : result.mean.toFixed(4)
                : '—'}</strong
            >
            <small
              >{result?.count ?? 0} outcomes · {analysis.exposure_counts[arm.key] ?? 0} exposures</small
            >
          </article>
        {/each}
        <article>
          <span>SRM p-value</span><strong>{analysis.srm_p_value.toPrecision(3)}</strong><small
            >invalid below 0.01</small
          >
        </article>
      </div>
      {#if analysis.primary.comparisons.length}
        <table>
          <thead
            ><tr
              ><th>Arm vs champion</th><th>Effect</th><th>Confidence interval</th><th
                >Effect size</th
              ></tr
            ></thead
          >
          <tbody>
            {#each analysis.primary.comparisons as comparison (comparison.arm_key)}
              <tr>
                <td>{comparison.arm_key}</td>
                <td>{comparison.effect.toFixed(5)}</td>
                <td
                  >{comparison.interval.low.toFixed(5)} to {comparison.interval.high.toFixed(5)}</td
                >
                <td>{comparison.effect_size.toFixed(3)}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      {/if}
      {#if analysis.guardrails.length}
        <div class="guardrails">
          <h3>Safety guardrails</h3>
          {#each analysis.guardrails as guardrail (guardrail.metric.key)}
            <article>
              <div>
                <strong>{guardrail.metric.name}</strong>
                <small>
                  {guardrail.metric.key} · safe direction {guardrail.metric.direction} · maximum regression
                  {guardrail.metric.max_regression ?? 0}
                </small>
              </div>
              {#each guardrail.arms as arm (arm.arm_key)}
                <span>{arm.arm_key}: {arm.mean.toFixed(5)} ({arm.count})</span>
              {/each}
            </article>
          {/each}
        </div>
      {/if}
      <details>
        <summary>Statistical assumptions</summary>
        <ul>
          {#each analysis.assumptions as assumption (assumption)}<li>{assumption}</li>{/each}
        </ul>
      </details>
      {#if experiment.state === 'completed' && analysis.status === 'winner' && roleAtLeast($user?.role, 'approver')}
        <button class="primary promote" disabled={busy} onclick={promote}
          >Open governed winner deployment</button
        >
      {/if}
    </section>

    <section>
      <div class="section-head">
        <div>
          <h2>Outcome attribution</h2>
          <p>
            The backend derives treatment and model facts; you provide only the observed business
            fact.
          </p>
        </div>
      </div>
      {#if roleAtLeast($user?.role, 'operator')}
        <form
          class="outcome-form"
          onsubmit={(event) => {
            event.preventDefault();
            void record();
          }}
        >
          <label
            ><span>Reached decision</span><select bind:value={outcomeDecision} required>
              <option value="" disabled>Select an exposure</option>
              {#each exposures as exposure (exposure.decision_id)}
                <option value={exposure.decision_id}
                  >{exposure.arm_name} · {exposure.decision_id.slice(0, 12)}</option
                >
              {/each}
            </select></label
          >
          <label><span>KPI key</span><input bind:value={outcomeKey} required /></label>
          <label
            ><span>Kind</span><select bind:value={outcomeKind}
              ><option value="binary">binary</option><option value="continuous">continuous</option
              ></select
            ></label
          >
          <label
            ><span>Observed value</span><input
              type="number"
              step="any"
              bind:value={outcomeValue}
            /></label
          >
          <label
            ><span>Event time</span><input
              type="datetime-local"
              bind:value={outcomeTime}
              required
            /></label
          >
          <label
            ><span>Window days</span><input
              type="number"
              min="0"
              max="3650"
              bind:value={outcomeWindow}
            /></label
          >
          <label
            ><span>Source system</span><input
              bind:value={sourceSystem}
              required
              placeholder="loan-core"
            /></label
          >
          <label><span>Source record ID</span><input bind:value={sourceRecord} required /></label>
          <label
            ><span>Source lineage</span><input
              bind:value={sourceLineage}
              placeholder="settlement-v2"
            /></label
          >
          <label><span>Label version</span><input bind:value={labelVersion} required /></label>
          <button class="primary" disabled={busy || exposures.length === 0}>Record outcome</button>
        </form>
      {/if}
      {#if outcomes.length}
        <table>
          <thead
            ><tr
              ><th>Decision / arm</th><th>Metric</th><th>Current value</th><th>Source</th><th
                >Revision</th
              ><th></th></tr
            ></thead
          >
          <tbody>
            {#each outcomes as outcome (outcome.outcome_id)}
              <tr>
                <td
                  ><a href={appHref(`/decisions/${outcome.decision_id}`)}
                    >{outcome.decision_id.slice(0, 12)}</a
                  ><small>{outcome.treatment?.arm_name}</small></td
                >
                <td>{outcome.key}<small>{outcome.kind} · {outcome.current.label_version}</small></td
                >
                <td>{outcome.current.value}</td>
                <td
                  >{outcome.current.source.system}<small>{outcome.current.source.record_id}</small
                  ></td
                >
                <td>{outcome.current.revision} / {outcome.history.length}</td>
                <td
                  >{#if roleAtLeast($user?.role, 'operator')}<button
                      onclick={() => beginCorrection(outcome)}>Correct</button
                    >{/if}</td
                >
              </tr>
            {/each}
          </tbody>
        </table>
      {/if}
      {#if correction}
        <form
          class="correction"
          onsubmit={(event) => {
            event.preventDefault();
            void correct();
          }}
        >
          <strong>Correct {correction.key} for {correction.decision_id.slice(0, 12)}</strong>
          <label
            ><span>Corrected value</span><input
              type="number"
              step="any"
              bind:value={correctionValue}
            /></label
          >
          <label
            ><span>Event time</span><input
              type="datetime-local"
              bind:value={outcomeTime}
              required
            /></label
          >
          <label><span>Source system</span><input bind:value={sourceSystem} required /></label>
          <label><span>Source record ID</span><input bind:value={sourceRecord} required /></label>
          <label><span>Label version</span><input bind:value={labelVersion} required /></label>
          <label><span>Reason</span><input bind:value={correctionReason} required /></label>
          <div class="buttons">
            <button class="primary" disabled={busy}>Append correction</button><button
              type="button"
              onclick={() => (correction = null)}>Close</button
            >
          </div>
        </form>
      {/if}
    </section>

    <section>
      <div class="section-head">
        <div>
          <h2>Reached treatments</h2>
          <p>
            Assignment alone is not counted. Each row proves execution reached its experimental
            treatment.
          </p>
        </div>
        <Badge tone="neutral">{exposures.length}</Badge>
      </div>
      {#if exposures.length}
        <table>
          <thead
            ><tr
              ><th>Decision</th><th>Arm</th><th>Version</th><th>Subject digest</th><th>Reached</th
              ></tr
            ></thead
          >
          <tbody>
            {#each exposures as exposure (exposure.decision_id)}
              <tr>
                <td
                  ><a href={appHref(`/decisions/${exposure.decision_id}`)}>{exposure.decision_id}</a
                  ></td
                >
                <td>{exposure.arm_name}</td><td>v{exposure.version}</td>
                <td><code>{exposure.subject_hash.slice(0, 16)}…</code></td>
                <td><RelativeTime value={exposure.reached_at} /></td>
              </tr>
            {/each}
          </tbody>
        </table>
      {:else}<p class="muted">No treatment has been reached in this cohort.</p>{/if}
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
  .control,
  .buttons {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 1rem;
  }
  .eyebrow,
  .muted,
  small,
  section p,
  .control p {
    color: var(--muted);
  }
  .eyebrow {
    text-transform: uppercase;
    letter-spacing: 0.08em;
    font-size: 0.72rem;
  }
  section,
  .control,
  .editor {
    border: 1px solid var(--border);
    border-radius: 12px;
    background: var(--surface);
    padding: 1.25rem;
    margin-top: 1rem;
  }
  .control input {
    min-width: 15rem;
    flex: 1;
  }
  .verdict {
    display: flex;
    justify-content: space-between;
    gap: 1rem;
    border-left: 3px solid var(--warn);
    padding: 0.8rem 1rem;
    background: var(--surface-2);
  }
  .verdict.winner {
    border-left-color: var(--ok);
  }
  .metrics {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
    gap: 0.75rem;
    margin: 1rem 0;
  }
  .metrics article {
    border: 1px solid var(--border);
    border-radius: 9px;
    padding: 1rem;
    display: grid;
    gap: 0.3rem;
  }
  .metrics article > span,
  label span {
    color: var(--muted);
    font-size: 0.78rem;
  }
  .metrics strong {
    font-size: 1.5rem;
  }
  .guardrails {
    display: grid;
    gap: 0.6rem;
    margin: 1rem 0;
  }
  .guardrails article {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 0.8rem 1.2rem;
    border: 1px solid var(--border);
    border-radius: 8px;
    padding: 0.75rem;
  }
  .guardrails article > div {
    display: grid;
    margin-right: auto;
  }
  table {
    width: 100%;
    border-collapse: collapse;
    margin-top: 0.75rem;
  }
  th,
  td {
    text-align: left;
    padding: 0.7rem;
    border-bottom: 1px solid var(--border);
    vertical-align: top;
  }
  td small {
    display: block;
    margin-top: 0.2rem;
  }
  textarea {
    width: 100%;
    box-sizing: border-box;
    font-family: var(--font-mono);
  }
  .outcome-form {
    display: grid;
    grid-template-columns: repeat(5, minmax(0, 1fr));
    gap: 0.75rem;
    margin: 1rem 0;
  }
  label {
    display: grid;
    gap: 0.3rem;
  }
  input,
  select {
    width: 100%;
    box-sizing: border-box;
  }
  .outcome-form button {
    align-self: end;
  }
  .correction {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(10rem, 1fr));
    align-items: end;
    gap: 0.75rem;
    margin-top: 1rem;
    padding: 1rem;
    background: var(--surface-2);
  }
  .promote {
    margin-top: 1rem;
  }
  @media (max-width: 900px) {
    .outcome-form {
      grid-template-columns: repeat(2, minmax(0, 1fr));
    }
    .control,
    .correction {
      display: grid;
      grid-template-columns: 1fr;
    }
  }
  @media (max-width: 620px) {
    main {
      padding: 1rem;
    }
    .head,
    .section-head,
    .buttons {
      align-items: flex-start;
      flex-wrap: wrap;
    }
    .outcome-form {
      grid-template-columns: 1fr;
    }
    section {
      overflow-x: auto;
    }
  }
</style>
