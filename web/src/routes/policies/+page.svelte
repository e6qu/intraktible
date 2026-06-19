<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Policies: the operational disposition layer over a flow. Author auto-approve /
     decline / refer bands and publish a version; the decide path applies the
     policy bound to a flow and records the disposition. -->
<script lang="ts">
  import { onMount } from 'svelte';
  import Icon from '$lib/Icon.svelte';
  import CommentThread from '$lib/CommentThread.svelte';
  import EmptyState from '$lib/EmptyState.svelte';
  import Skeleton from '$lib/Skeleton.svelte';
  import { toast } from '$lib/toast';
  import {
    listPolicies,
    createPolicy,
    publishPolicy,
    policyBacktest,
    listFlows,
    type Policy,
    type PolicyRule,
    type PolicyBacktestReport,
    type Flow
  } from '$lib/api';

  const key = '';
  const DISPOSITIONS = ['approve', 'decline', 'refer'];

  let policies = $state<Policy[]>([]);
  let flows = $state<Flow[]>([]);
  let error = $state('');
  let loading = $state(true);

  // create form
  let cName = $state('');
  let cFlow = $state('');
  let creating = $state(false);

  // band editor (for the selected policy)
  let selectedId = $state('');
  let rules = $state<PolicyRule[]>([]);
  let dflt = $state('refer');
  let publishing = $state(false);

  // disposition backtest (preview the draft over a dataset)
  let btDataset = $state('[\n  {}\n]');
  let btReport = $state<PolicyBacktestReport | null>(null);
  let btRunning = $state(false);

  const selected = $derived(policies.find((p) => p.policy_id === selectedId) ?? null);

  function msg(e: unknown): string {
    return e instanceof Error ? e.message : String(e);
  }
  async function load() {
    loading = true;
    error = '';
    try {
      [policies, flows] = await Promise.all([listPolicies(key), listFlows(key)]);
    } catch (e) {
      error = msg(e);
    } finally {
      loading = false;
    }
  }
  async function create() {
    if (creating) return; // Enter fires onsubmit directly, bypassing the disabled button
    error = '';
    creating = true;
    try {
      const { policy_id } = await createPolicy(key, { name: cName.trim(), flow_slug: cFlow });
      cName = '';
      await load();
      edit(policy_id);
    } catch (e) {
      error = msg(e);
    } finally {
      creating = false;
    }
  }
  function edit(policyId: string) {
    selectedId = policyId;
    const p = policies.find((x) => x.policy_id === policyId);
    const spec = p?.versions?.at(-1)?.spec;
    rules = spec ? spec.rules.map((r) => ({ ...r })) : [];
    dflt = spec?.default || 'refer';
  }
  function addRule() {
    rules = [...rules, { when: '', disposition: 'approve', code: '', description: '' }];
  }
  function removeRule(i: number) {
    rules = rules.filter((_, j) => j !== i);
  }
  function setRule(i: number, patch: Partial<PolicyRule>) {
    rules = rules.map((r, j) => (j === i ? { ...r, ...patch } : r));
  }
  // draftSpec is the current band editor as a publishable / backtestable spec.
  function draftSpec() {
    const rs = rules
      .filter((r) => r.when.trim())
      .map((r) => ({
        when: r.when.trim(),
        disposition: r.disposition,
        code: r.code?.trim() || undefined,
        description: r.description?.trim() || undefined
      }));
    return { rules: rs, default: dflt };
  }
  async function publish() {
    error = '';
    publishing = true;
    try {
      const r = await publishPolicy(key, selectedId, draftSpec());
      toast.success(`Published policy v${r.version}`);
      await load();
    } catch (e) {
      error = msg(e);
    } finally {
      publishing = false;
    }
  }
  // preview replays a dataset through the bound flow + the draft bands, diffing
  // against the latest published version (when one exists) — safe tuning.
  async function preview() {
    error = '';
    btReport = null;
    btRunning = true;
    try {
      const dataset = JSON.parse(btDataset) as Record<string, unknown>[];
      const compare = selected && selected.latest > 0 ? selected.latest : undefined;
      btReport = await policyBacktest(key, selectedId, {
        spec: draftSpec(),
        compare_version: compare,
        dataset
      });
    } catch (e) {
      error = msg(e);
    } finally {
      btRunning = false;
    }
  }

  onMount(load);
</script>

<main>
  <h1>Policies</h1>
  <p class="muted">
    A policy maps a flow's output to a disposition — <b>approve</b> / <b>decline</b>
    (straight-through) or <b>refer</b> (to a human). The decide path applies the policy bound to a flow
    and records the disposition. Rules are tried in order; the first match wins.
  </p>

  <form
    class="row"
    onsubmit={(e) => {
      e.preventDefault();
      create();
    }}
  >
    <input bind:value={cName} placeholder="policy name" aria-label="policy name" required />
    <select bind:value={cFlow} aria-label="flow" required>
      <option value="" disabled>flow…</option>
      {#each flows as f (f.flow_id)}
        <option value={f.slug}>{f.name} ({f.slug})</option>
      {/each}
    </select>
    <button type="submit" disabled={creating || !cFlow}
      >{creating ? 'Creating…' : 'Create policy'}</button
    >
  </form>

  {#if error}<p class="err">{error}</p>{/if}

  {#if loading}
    <Skeleton rows={4} />
  {:else if policies.length === 0}
    <EmptyState
      icon="rule"
      title="No policies yet"
      hint="Create one above, bound to a flow, then add auto-approve / decline / refer bands and publish."
    />
  {:else}
    <div class="table-wrap">
      <table>
        <thead>
          <tr><th>Name</th><th>Flow</th><th>Version</th><th>Bands</th><th></th></tr>
        </thead>
        <tbody>
          {#each policies as p (p.policy_id)}
            <tr class:sel={p.policy_id === selectedId}>
              <td>{p.name}</td>
              <td class="mono">{p.flow_slug}</td>
              <td>{p.latest > 0 ? `v${p.latest}` : '—'}</td>
              <td>{p.versions?.at(-1)?.spec.rules.length ?? 0}</td>
              <td><button class="link" onclick={() => edit(p.policy_id)}>Edit bands</button></td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}

  {#if selected}
    <section class="editor" data-testid="band-editor">
      <h2>Bands — {selected.name} <span class="mono muted">→ {selected.flow_slug}</span></h2>
      <p class="muted">
        Each rule's condition is an expression over the flow's output (e.g. <code
          >score &gt;= 0.85</code
        >).
      </p>
      {#each rules as r, i (i)}
        <div class="band">
          <input
            class="when"
            value={r.when}
            oninput={(e) => setRule(i, { when: e.currentTarget.value })}
            placeholder="when (expr over output)"
            aria-label={`band ${i} when`}
          />
          <select
            value={r.disposition}
            onchange={(e) => setRule(i, { disposition: e.currentTarget.value })}
            aria-label={`band ${i} disposition`}
          >
            {#each DISPOSITIONS as d (d)}<option value={d}>{d}</option>{/each}
          </select>
          <input
            class="code"
            value={r.code ?? ''}
            oninput={(e) => setRule(i, { code: e.currentTarget.value })}
            placeholder="code"
            aria-label={`band ${i} code`}
          />
          <input
            class="desc"
            value={r.description ?? ''}
            oninput={(e) => setRule(i, { description: e.currentTarget.value })}
            placeholder="description"
            aria-label={`band ${i} description`}
          />
          <button class="icon" aria-label="remove band" onclick={() => removeRule(i)}>
            <Icon name="trash" size={14} />
          </button>
        </div>
      {/each}
      <div class="row">
        <button onclick={addRule}><Icon name="plus" size={14} /> Add band</button>
        <label class="dflt">
          default
          <select bind:value={dflt} aria-label="default disposition">
            {#each DISPOSITIONS as d (d)}<option value={d}>{d}</option>{/each}
          </select>
        </label>
        <button
          class="primary"
          onclick={publish}
          disabled={publishing}
          data-testid="publish-policy"
        >
          <Icon name="check" size={14} />
          {publishing ? 'Publishing…' : 'Publish version'}
        </button>
      </div>

      <h2 class="preview-h">Preview impact</h2>
      <p class="muted">
        Replay a dataset through the flow + these draft bands — no decisions are recorded. Diffs
        against the latest published version when one exists, so you can see how the distribution
        shifts before publishing.
      </p>
      <div class="row">
        <button onclick={preview} disabled={btRunning} data-testid="backtest-policy">
          {btRunning ? 'Running…' : 'Preview impact'}
        </button>
      </div>
      <textarea
        class="when"
        bind:value={btDataset}
        aria-label="backtest dataset"
        rows="4"
        placeholder={'[\n  {"score": 0.9},\n  {"score": 0.4}\n]'}
      ></textarea>
      {#if btReport}
        <table class="bt" data-testid="backtest-result">
          <thead>
            <tr><th></th><th>Approve</th><th>Decline</th><th>Refer</th><th>Failed</th></tr>
          </thead>
          <tbody>
            <tr>
              <td>draft</td>
              <td class="ok">{btReport.summary.evaluated.approve}</td>
              <td>{btReport.summary.evaluated.decline}</td>
              <td>{btReport.summary.evaluated.refer}</td>
              <td class="err">{btReport.summary.evaluated.failed}</td>
            </tr>
            {#if btReport.summary.compare}
              <tr class="muted">
                <td>published</td>
                <td>{btReport.summary.compare.approve}</td>
                <td>{btReport.summary.compare.decline}</td>
                <td>{btReport.summary.compare.refer}</td>
                <td>{btReport.summary.compare.failed}</td>
              </tr>
            {/if}
          </tbody>
        </table>
        <p class="muted">
          {btReport.summary.total} records{#if btReport.summary.compare}
            · <b class="changed">{btReport.summary.flipped ?? 0}</b> would change disposition{/if}
        </p>
      {/if}

      <!-- Key on the policy id: switching the selected policy remounts the thread
           so it reloads that policy's comments (it loads once, on mount). -->
      {#key selected.policy_id}
        <CommentThread
          subjectType="policy"
          subjectId={selected.policy_id}
          title="Policy discussion"
        />
      {/key}
    </section>
  {/if}
</main>

<style>
  main {
    max-width: 60rem;
    margin: 2rem auto;
    padding: 0 1.25rem;
  }
  .row {
    display: flex;
    gap: 0.5rem;
    flex-wrap: wrap;
    align-items: center;
    margin: 0.7rem 0;
  }
  .muted {
    color: var(--fg-subtle);
  }
  .err {
    color: var(--danger);
  }
  .mono {
    font-family: var(--font-mono);
    font-size: 0.9em;
  }
  table {
    width: 100%;
    border-collapse: collapse;
    margin-top: 0.5rem;
  }
  th,
  td {
    text-align: left;
    padding: 0.45rem 0.6rem;
    border-bottom: 1px solid var(--border);
    font-size: 0.92rem;
  }
  tr.sel {
    background: color-mix(in srgb, var(--accent) 10%, transparent);
  }
  .link {
    border: none;
    background: none;
    color: var(--accent-ink);
    padding: 0;
    cursor: pointer;
  }
  .editor {
    margin-top: 1.5rem;
    padding: 1rem 1.1rem;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--surface);
  }
  .band {
    display: flex;
    gap: 0.4rem;
    margin: 0.35rem 0;
    align-items: center;
  }
  .band .when {
    flex: 2;
    min-width: 10rem;
    font-family: var(--font-mono);
  }
  .band .code {
    width: 6rem;
  }
  .band .desc {
    flex: 1.5;
    min-width: 8rem;
  }
  .band .icon {
    padding: 0.4rem 0.5rem;
  }
  .dflt {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    color: var(--fg-muted);
    font-size: 0.9rem;
  }
  .preview-h {
    margin-top: 1.4rem;
    border-top: 1px solid var(--border);
    padding-top: 1rem;
  }
  table.bt {
    width: auto;
    margin-top: 0.5rem;
  }
  table.bt td,
  table.bt th {
    text-align: right;
    padding: 0.3rem 0.8rem;
  }
  table.bt td:first-child,
  table.bt th:first-child {
    text-align: left;
    color: var(--fg-muted);
  }
  .ok {
    color: var(--ok);
  }
  .err {
    color: var(--danger);
  }
  .changed {
    color: var(--warn);
  }
</style>
