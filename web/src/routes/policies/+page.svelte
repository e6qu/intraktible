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
  import Badge from '$lib/Badge.svelte';
  import RelativeTime from '$lib/RelativeTime.svelte';
  import { toast } from '$lib/toast';
  import {
    listPolicies,
    createPolicy,
    publishPolicy,
    policyBacktest,
    listFlows,
    type Policy,
    type PolicyRule,
    type PolicySpec,
    type PolicyBacktestReport,
    type Flow,
    type Disposition
  } from '$lib/api';
  import type { BadgeTone } from '$lib/badge';
  import { roleAtLeast } from '$lib/roles';
  import { user } from '$lib/session';

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

  // Plain-language read of the bands in order (first match wins) + the default.
  const bandPreview = $derived([
    ...rules.filter((r) => r.when.trim()).map((r) => `if ${r.when.trim()} → ${r.disposition}`),
    `otherwise → ${dflt}`
  ]);
  let publishing = $state(false);

  // Field names the selected policy's bands actually key on, read from the `when`
  // expressions (e.g. `risk == "low"` / `fico_score >= 700` → risk, fico_score).
  // Bands compare these against the FLOW's output, so they tell a tester which keys
  // the backtest dataset must carry — `score` is not universal across policies.
  const RESERVED = new Set(['and', 'or', 'not', 'in', 'true', 'false', 'null']);
  // Rows that exercise every band: for each field, values just below / at /
  // above every numeric threshold its rules compare against (plus quoted string
  // literals verbatim) — so a preview shows the full disposition mix.
  function sampleImpactDataset(): string {
    const perField = new Map<string, Set<number | string>>();
    for (const r of rules) {
      for (const m of r.when.matchAll(
        /([A-Za-z_][A-Za-z0-9_]*)\s*(?:[<>]=?|[!=]=)\s*(-?\d+(?:\.\d+)?|"[^"]*"|'[^']*')/g
      )) {
        const set = perField.get(m[1]) ?? new Set();
        const lit = m[2];
        if (lit.startsWith('"') || lit.startsWith("'")) set.add(lit.slice(1, -1));
        else {
          const n = Number(lit);
          set.add(Math.round((n - Math.max(1, Math.abs(n) * 0.15)) * 100) / 100);
          set.add(n);
          set.add(Math.round((n + Math.max(1, Math.abs(n) * 0.15)) * 100) / 100);
        }
        perField.set(m[1], set);
      }
    }
    if (perField.size === 0) throw new Error('sample dataset requires bands with comparisons');
    const width = Math.max(...[...perField.values()].map((v) => v.size));
    const rows = Array.from({ length: width }, (_, i) =>
      Object.fromEntries(
        [...perField.entries()].map(([f, vals]) => {
          const arr = [...vals];
          return [f, arr[i % arr.length]];
        })
      )
    );
    return JSON.stringify(rows, null, 2);
  }
  const policyFields = $derived.by(() => {
    const fields: string[] = [];
    for (const r of rules) {
      // Leading identifiers (skip quoted strings and bare numbers); a field is the
      // first token of a comparison, so take identifiers not preceded by a dot.
      for (const m of r.when.matchAll(/[A-Za-z_][A-Za-z0-9_]*/g)) {
        const tok = m[0];
        const before = r.when[m.index - 1];
        if (before === '.' || before === '"' || before === "'") continue;
        if (RESERVED.has(tok.toLowerCase()) || fields.includes(tok)) continue;
        fields.push(tok);
      }
    }
    return fields;
  });
  // A sample dataset row built from the policy's own fields, so the backtest example
  // exercises the real bands instead of a documented-but-wrong `score`.
  const sampleRow = $derived.by(() => {
    if (policyFields.length === 0) return '{}';
    const obj = Object.fromEntries(policyFields.slice(0, 4).map((f) => [f, 0]));
    return JSON.stringify(obj);
  });
  const datasetPlaceholder = $derived(`[\n  ${sampleRow}\n]`);

  // disposition backtest (preview the draft over a dataset)
  let btDataset = $state('[\n  {}\n]');
  let btReport = $state<PolicyBacktestReport | null>(null);
  let btRunning = $state(false);

  const selected = $derived(policies.find((p) => p.policy_id === selectedId) ?? null);

  // The policy whose version history is expanded (one row at a time).
  let historyId = $state('');
  function toggleHistory(policyId: string) {
    historyId = historyId === policyId ? '' : policyId;
  }

  const DISPOSITION_TONE: Record<string, BadgeTone> = {
    approve: 'ok',
    decline: 'danger',
    refer: 'warn'
  };
  // The disposition mix of a published spec: how many bands land on each
  // disposition, plus the default. Drives the per-row mix indicator so an
  // operator can read a policy's leaning without opening the editor.
  function dispositionMix(spec: PolicySpec | undefined) {
    const counts = new Map(DISPOSITIONS.map((d) => [d, 0]));
    for (const r of spec?.rules ?? []) {
      const c = counts.get(r.disposition);
      if (c !== undefined) counts.set(r.disposition, c + 1);
    }
    return DISPOSITIONS.map((d) => ({ disposition: d, count: counts.get(d) ?? 0 })).filter(
      (c) => c.count > 0
    );
  }

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
    // Drop the previous policy's preview-impact table and error — they belong to a
    // different policy and would otherwise render under this one's editor.
    btReport = null;
    error = '';
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
    if (!selectedId) {
      error = 'Select a policy to publish.';
      return;
    }
    const spec = draftSpec();
    if (spec.rules.length === 0) {
      error = 'Add at least one rule before publishing.';
      return;
    }
    publishing = true;
    try {
      const r = await publishPolicy(key, selectedId, spec);
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
    // Drop the result if another policy is selected mid-flight — its impact table
    // and errors describe the previously selected policy's draft.
    const requested = selectedId;
    try {
      const parsed = JSON.parse(btDataset);
      if (!Array.isArray(parsed)) {
        throw new Error('Backtest dataset must be a JSON array of input objects.');
      }
      const dataset = parsed as Record<string, unknown>[];
      const compare = selected && selected.latest > 0 ? selected.latest : undefined;
      const report = await policyBacktest(key, requested, {
        spec: draftSpec(),
        compare_version: compare,
        dataset
      });
      if (selectedId === requested) btReport = report;
    } catch (e) {
      if (selectedId === requested) error = msg(e);
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
    <button
      type="submit"
      disabled={creating || !cFlow || !roleAtLeast($user?.role, 'editor')}
      title={!roleAtLeast($user?.role, 'editor') ? 'Requires the editor role' : undefined}
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
          <tr
            ><th>Name</th><th>Flow</th><th>Version</th><th>Mix</th><th>Published</th><th>Author</th
            ><th></th></tr
          >
        </thead>
        <tbody>
          {#each policies as p (p.policy_id)}
            {@const latest = p.versions?.at(-1)}
            {@const mix = dispositionMix(latest?.spec)}
            <tr class:sel={p.policy_id === selectedId}>
              <td>{p.name}</td>
              <td class="mono">{p.flow_slug}</td>
              <td>{p.latest > 0 ? `v${p.latest}` : '—'}</td>
              <td>
                {#if mix.length > 0}
                  <span class="mix">
                    {#each mix as c (c.disposition)}
                      <Badge tone={DISPOSITION_TONE[c.disposition]}>{c.disposition} {c.count}</Badge
                      >
                    {/each}
                  </span>
                {:else}<span class="muted">—</span>{/if}
              </td>
              <td class="muted"
                >{#if latest?.published_at}<RelativeTime
                    value={latest.published_at}
                  />{:else if p.updated_at}<RelativeTime value={p.updated_at} />{:else}—{/if}</td
              >
              <td class="muted">{latest?.published_by ?? '—'}</td>
              <td>
                <div class="row-actions">
                  <button class="link" onclick={() => edit(p.policy_id)}>Edit bands</button>
                  {#if (p.versions?.length ?? 0) > 0}
                    <button
                      class="link"
                      onclick={() => toggleHistory(p.policy_id)}
                      aria-expanded={historyId === p.policy_id}
                      >{historyId === p.policy_id ? 'Hide history' : 'History'}</button
                    >
                  {/if}
                </div>
              </td>
            </tr>
            {#if historyId === p.policy_id}
              <tr class="history-row">
                <td colspan="7">
                  <div class="history" data-testid="version-history">
                    <p class="muted">Published versions (newest first):</p>
                    <ul>
                      {#each [...p.versions].reverse() as v (v.version)}
                        <li>
                          <b>v{v.version}</b>
                          <span class="hist-mix">
                            {#each dispositionMix(v.spec) as c (c.disposition)}
                              <Badge tone={DISPOSITION_TONE[c.disposition]}
                                >{c.disposition} {c.count}</Badge
                              >
                            {/each}
                          </span>
                          <span class="muted"
                            >{#if v.published_at}<RelativeTime
                                value={v.published_at}
                              />{/if}{#if v.published_by}
                              · {v.published_by}{/if}</span
                          >
                        </li>
                      {/each}
                    </ul>
                  </div>
                </td>
              </tr>
            {/if}
          {/each}
        </tbody>
      </table>
    </div>
  {/if}

  {#if selected}
    <section class="editor" data-testid="band-editor">
      <h2>Bands — {selected.name} <span class="mono muted">→ {selected.flow_slug}</span></h2>
      <p class="muted">
        Each rule's condition is an expression over the flow's output — reference whatever fields it
        emits (e.g. <code>risk == "low"</code> or <code>fico_score &gt;= 700</code>).
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
            onchange={(e) => setRule(i, { disposition: e.currentTarget.value as Disposition })}
            aria-label={`band ${i} disposition`}
          >
            {#each DISPOSITIONS as d (d)}<option value={d}>{d}</option>{/each}
          </select>
          <input
            class="code"
            value={r.code ?? ''}
            oninput={(e) => setRule(i, { code: e.currentTarget.value })}
            placeholder="code"
            title={r.code || undefined}
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
      {#if rules.some((r) => r.when.trim())}
        <div class="band-preview" data-testid="band-preview">
          <p class="muted">This policy reads, in order:</p>
          <ol>
            {#each bandPreview as line, i (i)}<li>{line}</li>{/each}
          </ol>
        </div>
      {/if}
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
          disabled={publishing || !roleAtLeast($user?.role, 'editor')}
          title={!roleAtLeast($user?.role, 'editor') ? 'Requires the editor role' : undefined}
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
        <button
          type="button"
          class="link"
          onclick={() => (btDataset = sampleImpactDataset())}
          disabled={policyFields.length === 0}
          title={policyFields.length
            ? 'Prefill rows that exercise every band (below / at / above each threshold)'
            : 'Add bands with comparisons first'}
          data-testid="sample-impact">Sample dataset</button
        >
      </div>
      <textarea
        class="when"
        bind:value={btDataset}
        aria-label="backtest dataset"
        rows="4"
        placeholder={datasetPlaceholder}
      ></textarea>
      {#if policyFields.length > 0}
        <p class="muted note">
          These bands key on {#each policyFields.slice(0, 6) as f, i (f)}{i > 0 ? ', ' : ''}<code
              >{f}</code
            >{/each} — give each dataset row those fields (they're compared against the flow's output).
        </p>
      {/if}
      {#if btReport && selected.latest === 0 && btReport.summary.total <= 1}
        <p class="muted note" data-testid="preview-unpublished">
          This policy has never been published, so there is no prior version to diff against and the
          dataset above is empty. Add a row or two of representative inputs (e.g.
          <code>{sampleRow}</code>) and run the preview again to see how these draft bands would
          distribute dispositions.
        </p>
      {:else if btReport}
        <div class="table-wrap">
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
        </div>
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
  .band-preview {
    margin: 0.6rem 0;
    padding: 0.6rem 0.9rem;
    border-left: 3px solid var(--accent);
    background: var(--surface-2);
    border-radius: var(--radius-sm);
  }
  .band-preview p {
    margin: 0 0 0.3rem;
    font-size: 0.8rem;
  }
  .band-preview ol {
    margin: 0;
    padding-left: 1.2rem;
    font-size: 0.9rem;
    font-family: var(--font-mono);
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
  .row-actions {
    display: flex;
    gap: 0.7rem;
    align-items: center;
    white-space: nowrap;
  }
  .mix,
  .hist-mix {
    display: inline-flex;
    flex-wrap: wrap;
    gap: 0.25rem;
  }
  .history-row td {
    background: var(--surface-2);
    padding: 0.6rem 0.9rem;
  }
  .history p {
    margin: 0 0 0.4rem;
    font-size: 0.82rem;
  }
  .history ul {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 0.35rem;
  }
  .history li {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    flex-wrap: wrap;
    font-size: 0.88rem;
  }
  .note {
    font-size: 0.85rem;
    margin: 0.5rem 0 0;
  }
  .note code {
    background: var(--surface-2);
    padding: 0.05rem 0.3rem;
    border-radius: 4px;
    font-family: var(--font-mono);
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
    flex-wrap: wrap;
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
    width: 10rem;
    font-family: var(--font-mono);
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
