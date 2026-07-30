<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { onMount } from 'svelte';
  import { goto } from '$app/navigation';
  import Badge from '$lib/Badge.svelte';
  import EmptyState from '$lib/EmptyState.svelte';
  import RelativeTime from '$lib/RelativeTime.svelte';
  import Skeleton from '$lib/Skeleton.svelte';
  import {
    createExperiment,
    listExperiments,
    listFlows,
    type Environment,
    type Experiment,
    type ExperimentMetricKind,
    type ExperimentDirection,
    type Flow
  } from '$lib/api';
  import { appHref } from '$lib/paths';
  import { roleAtLeast } from '$lib/roles';
  import { user } from '$lib/session';
  import { toast } from '$lib/toast';

  const key = '';
  let experiments = $state<Experiment[]>([]);
  let flows = $state<Flow[]>([]);
  let loading = $state(true);
  let error = $state('');
  let creating = $state(false);

  let name = $state('');
  let hypothesis = $state('');
  let owner = $state('');
  let flowID = $state('');
  let environment = $state<Environment>('sandbox');
  let subjectExpression = $state('customer_id');
  let eligibilityExpression = $state('');
  let salt = $state('');
  let championVersion = $state(1);
  let challengerVersion = $state(1);
  let championAllocation = $state(5000);
  let metricKey = $state('converted');
  let metricName = $state('Conversion');
  let metricKind = $state<ExperimentMetricKind>('binary');
  let direction = $state<ExperimentDirection>('increase');
  let minimumSample = $state(100);
  let minimumEffect = $state(0);
  let confidence = $state(0.95);
  let observationDays = $state(30);
  let startAt = $state('');
  let stopAt = $state('');
  let guardrailKey = $state('');
  let guardrailName = $state('');
  let guardrailKind = $state<ExperimentMetricKind>('binary');
  let guardrailDirection = $state<ExperimentDirection>('decrease');
  let guardrailMaxRegression = $state(0);
  let showCreate = $state(false);

  const selectedFlow = $derived(flows.find((flow) => flow.flow_id === flowID));
  const versions = $derived(selectedFlow?.versions ?? []);

  function message(value: unknown): string {
    return value instanceof Error ? value.message : String(value);
  }

  function stateTone(state: Experiment['state']): 'neutral' | 'ok' | 'warn' | 'danger' {
    if (state === 'running') return 'ok';
    if (state === 'pending_launch' || state === 'paused') return 'warn';
    if (state === 'cancelled') return 'danger';
    return 'neutral';
  }

  function newSalt(): void {
    salt = globalThis.crypto.randomUUID();
  }

  function selectFlow(): void {
    const published = selectedFlow?.versions ?? [];
    const latest = published.at(-1)?.version ?? 1;
    const deployments = new Map(Object.entries(selectedFlow?.deployments ?? {}));
    championVersion = deployments.get(environment)?.version ?? latest;
    challengerVersion = latest;
  }

  async function load(): Promise<void> {
    loading = true;
    error = '';
    try {
      [experiments, flows] = await Promise.all([listExperiments(key), listFlows(key)]);
      if (!flowID && flows[0]) {
        flowID = flows[0].flow_id;
        selectFlow();
      }
      if (!owner && $user) owner = $user.actor;
      if (!salt) newSalt();
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
      const result = await createExperiment(key, {
        name: name.trim(),
        hypothesis: hypothesis.trim(),
        owner: owner.trim(),
        flow_id: flowID,
        environment,
        subject_key_expression: subjectExpression.trim(),
        eligibility_expression: eligibilityExpression.trim() || undefined,
        salt: salt.trim(),
        arms: [
          {
            key: 'champion',
            name: 'Champion',
            kind: 'champion',
            version: championVersion,
            allocation_bps: championAllocation
          },
          {
            key: 'challenger',
            name: 'Challenger',
            kind: 'challenger',
            version: challengerVersion,
            allocation_bps: 10000 - championAllocation
          }
        ],
        primary_metric: {
          key: metricKey.trim(),
          name: metricName.trim(),
          kind: metricKind,
          direction
        },
        guardrails: guardrailKey.trim()
          ? [
              {
                key: guardrailKey.trim(),
                name: guardrailName.trim(),
                kind: guardrailKind,
                direction: guardrailDirection,
                max_regression: guardrailMaxRegression
              }
            ]
          : undefined,
        minimum_sample_per_arm: minimumSample,
        minimum_effect: minimumEffect,
        confidence,
        observation_window_days: observationDays,
        start_at: startAt ? new Date(startAt).toISOString() : undefined,
        stop_at: stopAt ? new Date(stopAt).toISOString() : undefined
      });
      toast.success('Experiment draft created');
      await goto(appHref(`/experiments/${result.experiment_id}`));
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
      <h1>Experiments</h1>
      <p class="lede">
        Govern stable subject cohorts, reached-treatment exposure, business outcomes, and
        statistically valid promotion decisions.
      </p>
    </div>
    {#if roleAtLeast($user?.role, 'editor')}
      <button class="primary" onclick={() => (showCreate = !showCreate)}>
        {showCreate ? 'Close setup' : 'New experiment'}
      </button>
    {/if}
  </header>

  {#if error}<p class="err" role="alert">{error}</p>{/if}

  {#if showCreate}
    <form
      class="setup"
      onsubmit={(event) => {
        event.preventDefault();
        void create();
      }}
    >
      <div class="setup-head">
        <div>
          <h2>Experiment setup</h2>
          <p>
            A material draft edit starts a new cohort. Production launch requires another approver.
          </p>
        </div>
        <Badge tone="neutral">10,000 bps total</Badge>
      </div>
      <div class="form-grid">
        <label
          ><span>Name</span><input
            bind:value={name}
            required
            placeholder="Offer strategy lift"
          /></label
        >
        <label><span>Owner</span><input bind:value={owner} required /></label>
        <label class="wide"
          ><span>Hypothesis</span><textarea
            bind:value={hypothesis}
            required
            rows="2"
            placeholder="The challenger increases conversion without increasing loss rate."
          ></textarea></label
        >
        <label
          ><span>Flow</span><select
            aria-label="Flow"
            bind:value={flowID}
            onchange={selectFlow}
            required
          >
            {#each flows as flow (flow.flow_id)}
              <option value={flow.flow_id}>{flow.name} · {flow.slug}</option>
            {/each}
          </select></label
        >
        <label
          ><span>Environment</span><select bind:value={environment} onchange={selectFlow}>
            <option value="sandbox">sandbox</option>
            <option value="staging">staging</option>
            <option value="production">production</option>
          </select></label
        >
        <label
          ><span>Champion version</span><select bind:value={championVersion}>
            {#each versions as version (version.version)}
              <option value={version.version}>v{version.version}</option>
            {/each}
          </select></label
        >
        <label
          ><span>Challenger version</span><select bind:value={challengerVersion}>
            {#each versions as version (version.version)}
              <option value={version.version}>v{version.version}</option>
            {/each}
          </select></label
        >
        <label
          ><span>Champion allocation (bps)</span><input
            type="number"
            min="1"
            max="9999"
            bind:value={championAllocation}
          /></label
        >
        <label
          ><span>Challenger allocation</span><output>{10000 - championAllocation} bps</output
          ></label
        >
        <label
          ><span>Stable subject expression</span><input
            bind:value={subjectExpression}
            required
            placeholder="customer_id"
          /></label
        >
        <label
          ><span>Eligibility expression <em>(optional)</em></span><input
            bind:value={eligibilityExpression}
            placeholder="country == 'US'"
          /></label
        >
        <label class="wide"
          ><span>Cohort salt</span>
          <div class="inline">
            <input bind:value={salt} required /><button type="button" onclick={newSalt}
              >Regenerate</button
            >
          </div></label
        >
        <label><span>Primary KPI key</span><input bind:value={metricKey} required /></label>
        <label><span>Primary KPI name</span><input bind:value={metricName} required /></label>
        <label
          ><span>KPI kind</span><select bind:value={metricKind}
            ><option value="binary">binary</option><option value="continuous">continuous</option
            ></select
          ></label
        >
        <label
          ><span>Preferred direction</span><select bind:value={direction}
            ><option value="increase">increase</option><option value="decrease">decrease</option
            ></select
          ></label
        >
        <label
          ><span>Minimum observations / arm</span><input
            type="number"
            min="2"
            bind:value={minimumSample}
          /></label
        >
        <label
          ><span>Minimum absolute effect</span><input
            type="number"
            min="0"
            step="0.001"
            bind:value={minimumEffect}
          /></label
        >
        <label
          ><span>Confidence</span><input
            type="number"
            min="0.8"
            max="0.999"
            step="0.01"
            bind:value={confidence}
          /></label
        >
        <label
          ><span>Observation window (days)</span><input
            type="number"
            min="0"
            max="3650"
            bind:value={observationDays}
          /></label
        >
        <label
          ><span>Assignment starts <em>(optional)</em></span><input
            type="datetime-local"
            bind:value={startAt}
          /></label
        >
        <label
          ><span>Assignment stops <em>(optional)</em></span><input
            type="datetime-local"
            bind:value={stopAt}
          /></label
        >
        <fieldset class="wide">
          <legend>Safety guardrail <em>(optional)</em></legend>
          <div class="guardrail-grid">
            <label
              ><span>Metric key</span><input
                bind:value={guardrailKey}
                placeholder="loss_rate"
              /></label
            >
            <label
              ><span>Metric name</span><input
                bind:value={guardrailName}
                required={Boolean(guardrailKey.trim())}
                placeholder="Loss rate"
              /></label
            >
            <label
              ><span>Kind</span><select bind:value={guardrailKind}
                ><option value="binary">binary</option><option value="continuous">continuous</option
                ></select
              ></label
            >
            <label
              ><span>Safe direction</span><select bind:value={guardrailDirection}
                ><option value="decrease">decrease</option><option value="increase">increase</option
                ></select
              ></label
            >
            <label
              ><span>Maximum regression</span><input
                type="number"
                min="0"
                step="0.001"
                bind:value={guardrailMaxRegression}
              /></label
            >
          </div>
        </fieldset>
      </div>
      <button class="primary" type="submit" disabled={creating || versions.length < 1}>
        {creating ? 'Creating…' : 'Create draft'}
      </button>
    </form>
  {/if}

  {#if loading}
    <Skeleton rows={5} />
  {:else if experiments.length === 0}
    <EmptyState
      title="No experiments yet"
      hint="Create a governed cohort to compare immutable flow versions with stable subjects and attributable outcomes."
    />
  {:else}
    <div class="table-wrap">
      <table>
        <thead
          ><tr
            ><th>Experiment</th><th>State</th><th>Cohort</th><th>Flow / env</th><th>Arms</th><th
              >Updated</th
            ></tr
          ></thead
        >
        <tbody>
          {#each experiments as experiment (experiment.experiment_id)}
            <tr>
              <td>
                <a href={appHref(`/experiments/${experiment.experiment_id}`)}
                  >{experiment.spec.name}</a
                >
                <small>{experiment.spec.hypothesis}</small>
              </td>
              <td
                ><Badge tone={stateTone(experiment.state)}
                  >{experiment.state.replace('_', ' ')}</Badge
                ></td
              >
              <td>#{experiment.cohort}</td>
              <td
                ><code>{experiment.spec.flow_id.slice(0, 8)}</code><small
                  >{experiment.spec.environment}</small
                ></td
              >
              <td>{experiment.spec.arms.length}</td>
              <td><RelativeTime value={experiment.updated_at} /></td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</main>

<style>
  main {
    max-width: 1180px;
    margin: 0 auto;
    padding: 2rem;
  }
  .head,
  .setup-head,
  .inline {
    display: flex;
    gap: 1rem;
    align-items: center;
    justify-content: space-between;
  }
  .lede,
  .setup p,
  small {
    color: var(--muted);
  }
  .setup {
    border: 1px solid var(--border);
    background: var(--surface);
    border-radius: 12px;
    padding: 1.25rem;
    margin: 1rem 0 2rem;
  }
  .form-grid {
    display: grid;
    grid-template-columns: repeat(4, minmax(0, 1fr));
    gap: 1rem;
    margin: 1rem 0;
  }
  label {
    display: grid;
    gap: 0.35rem;
    align-content: start;
  }
  label span {
    font-size: 0.78rem;
    color: var(--muted);
  }
  .wide {
    grid-column: span 2;
  }
  fieldset {
    border: 1px solid var(--border);
    border-radius: 8px;
    margin: 0;
    padding: 0.75rem;
  }
  fieldset legend {
    color: var(--muted);
    font-size: 0.78rem;
  }
  .guardrail-grid {
    display: grid;
    grid-template-columns: repeat(5, minmax(0, 1fr));
    gap: 0.75rem;
  }
  input,
  select,
  textarea,
  output {
    width: 100%;
    box-sizing: border-box;
  }
  output {
    min-height: 2.35rem;
    display: flex;
    align-items: center;
  }
  .inline input {
    flex: 1;
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
    text-align: left;
    padding: 0.8rem;
    border-bottom: 1px solid var(--border);
    vertical-align: top;
  }
  td small {
    display: block;
    margin-top: 0.25rem;
    max-width: 34rem;
  }
  @media (max-width: 900px) {
    .form-grid {
      grid-template-columns: repeat(2, minmax(0, 1fr));
    }
    .guardrail-grid {
      grid-template-columns: repeat(2, minmax(0, 1fr));
    }
  }
  @media (max-width: 620px) {
    main {
      padding: 1rem;
    }
    .form-grid {
      grid-template-columns: 1fr;
    }
    .wide {
      grid-column: auto;
    }
    .guardrail-grid {
      grid-template-columns: 1fr;
    }
    .head {
      align-items: flex-start;
    }
  }
</style>
