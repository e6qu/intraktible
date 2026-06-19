<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Predictive model registry: define models as data (logistic | gbm | expression |
     external), evaluated by the engine and referenced from a Predict node.
     Everything goes through the documented /v1/models API. -->
<script lang="ts">
  import { onMount } from 'svelte';
  import Icon from '$lib/Icon.svelte';
  import EmptyState from '$lib/EmptyState.svelte';
  import Skeleton from '$lib/Skeleton.svelte';
  import RelativeTime from '$lib/RelativeTime.svelte';
  import { listModels, defineModel, type Model } from '$lib/api';

  // Authenticates via the session cookie (empty key → no X-Api-Key header).
  const key = '';
  // Starter specs (a Map, so the keyed lookup stays clean under the object-injection lint).
  const STARTERS = new Map<string, string>([
    [
      'logistic',
      '{\n  "kind": "logistic",\n  "intercept": -3,\n  "coefficients": { "fico": 0.005 }\n}'
    ],
    [
      'gbm',
      '{\n  "kind": "gbm",\n  "link": "logit",\n  "trees": [\n    { "feature": "fico", "threshold": 650,\n      "left": { "leaf": true, "value": -0.4 },\n      "right": { "leaf": true, "value": 0.3 } }\n  ]\n}'
    ],
    ['expression', '{\n  "kind": "expression",\n  "expr": "fico * 0.001 + income * 0.00001"\n}'],
    [
      'external',
      '{\n  "kind": "external",\n  "endpoint": "https://models.internal/score",\n  "timeout_ms": 5000\n}'
    ]
  ]);

  let models = $state<Model[]>([]);
  let error = $state('');
  let loading = $state(true);

  let name = $state('');
  let spec = $state(STARTERS.get('logistic') ?? '');
  let busy = $state(false);

  function msg(e: unknown): string {
    return e instanceof Error ? e.message : String(e);
  }
  function starter(kind: string) {
    spec = STARTERS.get(kind) ?? spec;
  }
  async function load() {
    loading = true;
    error = '';
    try {
      models = await listModels(key);
    } catch (e) {
      error = msg(e);
    } finally {
      loading = false;
    }
  }
  async function create() {
    if (busy) return; // Enter fires onsubmit directly, bypassing the disabled button
    error = '';
    busy = true;
    try {
      const parsed: unknown = JSON.parse(spec); // fail loudly on bad JSON before POST
      await defineModel(key, { name: name.trim(), spec: parsed });
      name = '';
      await load();
    } catch (e) {
      error = msg(e);
    } finally {
      busy = false;
    }
  }

  onMount(load);
</script>

<main>
  <h1><Icon name="scorecard" size={20} /> Models</h1>
  <p class="muted">
    Predictive models hosted as data. Reference one from a <b>Predict</b> node (it injects
    <code>predict.&lt;output&gt;</code>). Three kinds evaluate in-process and deterministically —
    <b>logistic</b> regression, a <b>gbm</b> tree ensemble, an <b>expression</b> score — and an
    <b>external</b> kind serves a bring-your-own model over an egress-guarded HTTP endpoint (returns
    <code>{'{'}score, probability{'}'}</code>).
  </p>

  <form
    class="define"
    onsubmit={(e) => {
      e.preventDefault();
      create();
    }}
  >
    <div class="row">
      <label
        >Name (required) <input
          bind:value={name}
          placeholder="risk"
          aria-label="model name"
          required
        /></label
      >
      <span class="starters">
        starter:
        <button type="button" onclick={() => starter('logistic')}>logistic</button>
        <button type="button" onclick={() => starter('gbm')}>gbm</button>
        <button type="button" onclick={() => starter('expression')}>expression</button>
        <button type="button" onclick={() => starter('external')}>external</button>
      </span>
      <button type="submit" disabled={busy}>{busy ? 'Saving…' : 'Define model'}</button>
    </div>
    <label class="field"
      >Spec (JSON)
      <textarea bind:value={spec} aria-label="model spec" rows="9" spellcheck="false"
      ></textarea></label
    >
  </form>

  {#if error}<p class="err">{error}</p>{/if}

  {#if loading}
    <Skeleton rows={3} />
  {:else if models.length === 0}
    <EmptyState
      icon="scorecard"
      title="No models yet"
      hint="Define one above, then add a Predict node to a flow that references it by name."
    />
  {:else}
    <div class="table-wrap">
      <table>
        <thead>
          <tr><th>Name</th><th>Kind</th><th>Updated</th></tr>
        </thead>
        <tbody>
          {#each models as m (m.name)}
            <tr>
              <td>{m.name}</td>
              <td><span class="badge">{m.kind || '—'}</span></td>
              <td class="muted"><RelativeTime value={m.updated_at} /></td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</main>

<style>
  main {
    max-width: 52rem;
    margin: 2rem auto;
    padding: 0 1.25rem;
  }
  h1 {
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }
  .define {
    display: flex;
    flex-direction: column;
    gap: 0.6rem;
    margin: 0.8rem 0;
  }
  .row {
    display: flex;
    flex-wrap: wrap;
    gap: 0.6rem;
    align-items: center;
  }
  label {
    display: inline-flex;
    flex-direction: column;
    gap: 0.15rem;
    font-size: 0.74rem;
    color: var(--fg-subtle);
  }
  label.field {
    display: block;
  }
  input,
  button,
  textarea {
    font: inherit;
    padding: 0.4rem 0.6rem;
  }
  textarea {
    width: 100%;
    box-sizing: border-box;
    margin-top: 0.15rem;
    font-family: var(--font-mono);
    font-size: 0.82rem;
    resize: vertical;
  }
  .starters {
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
    font-size: 0.78rem;
    color: var(--fg-subtle);
  }
  .starters button {
    padding: 0.2rem 0.5rem;
    border: 1px solid var(--border);
    border-radius: 999px;
    background: var(--surface-2);
    color: var(--fg);
    font-size: 0.78rem;
    cursor: pointer;
  }
  .table-wrap {
    overflow-x: auto;
  }
  table {
    width: 100%;
    border-collapse: collapse;
    margin-top: 0.5rem;
  }
  th {
    text-align: left;
    font-size: 0.74rem;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--fg-subtle);
    padding: 0.45rem 0.6rem;
    border-bottom: 1px solid var(--border);
  }
  td {
    padding: 0.5rem 0.6rem;
    border-bottom: 1px solid var(--border);
    font-size: 0.9rem;
  }
  .badge {
    display: inline-block;
    padding: 0.1rem 0.5rem;
    border-radius: 999px;
    font-size: 0.78rem;
    background: var(--surface-2);
    color: var(--fg-muted);
  }
  code {
    background: var(--surface-2);
    padding: 0 0.3rem;
    border-radius: 0.3rem;
  }
  .muted {
    color: var(--fg-subtle);
  }
  .err {
    color: var(--danger);
  }
</style>
