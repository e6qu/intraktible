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
  import {
    listModels,
    defineModel,
    trainModel,
    modelDrift,
    getModelPerformance,
    captureModelBaseline,
    setModelMonitor,
    type Model,
    type ModelDrift,
    type ModelPerformance,
    type TrainReport,
    type TrainRow
  } from '$lib/api';
  import { roleAtLeast } from '$lib/roles';
  import { user } from '$lib/session';
  import Badge from '$lib/Badge.svelte';
  import CommentThread from '$lib/CommentThread.svelte';
  import Hint from '$lib/Hint.svelte';
  import { toast } from '$lib/toast';
  import type { Tone } from '$lib/badge';

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

  // The model whose drift readout is open, its loaded report, and the chosen window.
  let driftOpen = $state('');
  let drift = $state<ModelDrift | null>(null);
  let driftWindow = $state(0); // 0 = all-time, else N days
  let thresholdInput = $state('');
  // True while the open drift row is fetching its report (so the row shows a
  // loading line instead of looking empty).
  let driftLoading = $state(false);

  // An at-a-glance drift status per model, fetched once on load so the table shows
  // which models are drifting without the operator expanding each row.
  type DriftStatus = { tone: Tone; label: string };
  // A Map (not a plain object) so the keyed writes below stay clean under the
  // object-injection lint, matching the STARTERS map above.
  let driftStatus = $state<Map<string, DriftStatus>>(new Map());
  function statusFromDrift(d: ModelDrift): DriftStatus {
    if (d.count === 0) return { tone: 'neutral', label: 'no data' };
    if (!d.has_baseline || d.psi == null) return { tone: 'neutral', label: 'no baseline' };
    if (d.firing) return { tone: 'danger', label: 'drifting' };
    if (d.psi >= 0.25) return { tone: 'danger', label: 'significant' };
    if (d.psi >= 0.1) return { tone: 'warn', label: 'moderate' };
    return { tone: 'ok', label: 'stable' };
  }
  // Fetch every model's all-time drift status concurrently; a single failure must
  // not blank the table, so each is settled independently and only successes land.
  // A token drops an older sweep entirely, and successes merge per model, so a
  // slow sweep can't clobber newer statuses (e.g. the per-row loadDrift updates).
  let driftStatusSeq = 0;
  async function loadDriftStatuses(ms: Model[]) {
    const seq = ++driftStatusSeq;
    const settled = await Promise.allSettled(
      ms.map(
        async (m): Promise<[string, DriftStatus]> => [
          m.name,
          statusFromDrift(await modelDrift(key, m.name, 0))
        ]
      )
    );
    if (seq !== driftStatusSeq) return; // a newer sweep superseded this one
    const next = new Map(driftStatus);
    for (const res of settled) {
      if (res.status === 'fulfilled') next.set(res.value[0], res.value[1]);
    }
    driftStatus = next;
  }

  function msg(e: unknown): string {
    return e instanceof Error ? e.message : String(e);
  }
  // histBarHeight normalizes a decile bucket to a 0–100% bar height. Guards against
  // a non-finite bucket/peak (a null from the wire would otherwise make Math.max
  // return NaN and collapse every bar to NaN%).
  function histBarHeight(c: number, hist: number[]): number {
    const finite = hist.filter((n) => Number.isFinite(n));
    const peak = Math.max(1, ...finite);
    return Number.isFinite(c) ? Math.round((c / peak) * 100) : 0;
  }
  function psiLabel(psi: number): string {
    if (psi < 0.1) return 'stable';
    if (psi < 0.25) return 'moderate shift';
    return 'significant drift';
  }
  // The open drift row's own failure text, so a failed fetch shows an inline
  // error + Retry instead of the row saying "Loading drift…" forever.
  let driftError = $state('');
  // The open model's reconciled performance (from recorded actuals), fetched alongside
  // its drift; null when none recorded yet.
  let perf = $state<ModelPerformance | null>(null);
  async function loadDrift(m: string) {
    drift = null;
    perf = null;
    driftLoading = true;
    driftError = '';
    error = '';
    // Switching the window/model fires concurrent requests; capture what THIS call is
    // for and drop its result if either changed before it resolved (last-requested
    // wins, not last-to-arrive).
    const reqModel = m;
    const reqWindow = driftWindow;
    try {
      const [got, gotPerf] = await Promise.all([
        modelDrift(key, m, driftWindow),
        // Performance is best-effort (a model with no recorded actuals is fine).
        getModelPerformance(key, m).catch(() => null)
      ]);
      if (driftOpen !== reqModel || driftWindow !== reqWindow) return;
      drift = got;
      perf = gotPerf;
      thresholdInput = drift.threshold ? String(drift.threshold) : '';
      // Keep the at-a-glance row badge in sync with what the open row shows.
      driftStatus = new Map(driftStatus).set(m, statusFromDrift(got));
    } catch (e) {
      if (driftOpen === reqModel && driftWindow === reqWindow) driftError = msg(e);
    } finally {
      if (driftOpen === reqModel && driftWindow === reqWindow) driftLoading = false;
    }
  }
  // The model whose discussion thread is open (one at a time, like drift).
  let discussOpen = $state('');
  function toggleDiscuss(m: string) {
    discussOpen = discussOpen === m ? '' : m;
  }

  async function toggleDrift(m: string) {
    if (driftOpen === m) {
      driftOpen = '';
      drift = null;
      driftError = '';
      return;
    }
    driftOpen = m;
    driftWindow = 0;
    await loadDrift(m);
  }
  async function captureBaseline(m: string) {
    try {
      await captureModelBaseline(key, m);
      await loadDrift(m);
      toast.success(`Captured baseline for ${m}`);
    } catch (e) {
      toast.error(msg(e));
    }
  }
  async function saveThreshold(m: string) {
    // An empty field clears the monitor (threshold 0); a non-numeric entry is a
    // mistake — surface it instead of silently coercing to 0 (which would disable
    // the monitor the operator was trying to set).
    const raw = thresholdInput.trim();
    let threshold = 0;
    if (raw !== '') {
      threshold = Number(raw);
      if (!Number.isFinite(threshold) || threshold < 0) {
        toast.error('Threshold must be a non-negative number (or empty to clear).');
        return;
      }
    }
    try {
      await setModelMonitor(key, m, threshold);
      await loadDrift(m);
      toast.success(threshold === 0 ? 'Drift monitor cleared' : 'Drift threshold saved');
    } catch (e) {
      toast.error(msg(e));
    }
  }
  function starter(kind: string) {
    spec = STARTERS.get(kind) ?? spec;
  }
  async function load() {
    loading = true;
    error = '';
    try {
      models = await listModels(key);
      void loadDriftStatuses(models); // populate row badges without blocking the table
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
      const defined = name.trim();
      await defineModel(key, { name: defined, spec: parsed });
      name = '';
      await load();
      toast.success(`Defined model ${defined}`);
    } catch (e) {
      toast.error(msg(e));
    } finally {
      busy = false;
    }
  }

  // Training: fit a logistic model from a labelled dataset (JSON array of
  // {features, label}). The response is the trained model plus a report.
  let trainName = $state('');
  let trainData = $state(
    '[\n  { "features": { "fico": 720, "dti": 0.2 }, "label": 1 },\n  { "features": { "fico": 580, "dti": 0.5 }, "label": 0 }\n]'
  );
  let trainBusy = $state(false);
  let trainReport = $state<TrainReport | null>(null);
  async function train() {
    if (trainBusy) return;
    trainBusy = true;
    trainReport = null;
    try {
      const dataset = JSON.parse(trainData) as TrainRow[];
      if (!Array.isArray(dataset))
        throw new Error('dataset must be a JSON array of {features, label}');
      const named = trainName.trim();
      trainReport = await trainModel(key, { name: named, dataset });
      await load();
      toast.success(`Trained model ${named}: CV AUC ${trainReport.cv_auc.toFixed(3)}`);
    } catch (e) {
      toast.error(msg(e));
    } finally {
      trainBusy = false;
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
      <button
        type="submit"
        disabled={busy || !roleAtLeast($user?.role, 'editor')}
        title={!roleAtLeast($user?.role, 'editor') ? 'Requires the editor role' : undefined}
        >{busy ? 'Saving…' : 'Define model'}</button
      >
    </div>
    <label class="field"
      >Spec (JSON)
      <textarea bind:value={spec} aria-label="model spec" rows="9" spellcheck="false"
      ></textarea></label
    >
  </form>

  <details class="train">
    <summary>Train a logistic model from a dataset</summary>
    <p class="muted">
      Fit a logistic-regression model from labelled examples instead of hand-authoring coefficients.
      The fit is deterministic and cross-validated; the result is an ordinary model a Predict node
      serves.
    </p>
    <form
      class="define"
      onsubmit={(e) => {
        e.preventDefault();
        train();
      }}
    >
      <div class="row">
        <label
          >Model name <input
            bind:value={trainName}
            placeholder="risk"
            aria-label="model to train"
            required
          /></label
        >
        <button
          type="submit"
          disabled={trainBusy || !roleAtLeast($user?.role, 'editor')}
          title={!roleAtLeast($user?.role, 'editor') ? 'Requires the editor role' : undefined}
          >{trainBusy ? 'Training…' : 'Train model'}</button
        >
      </div>
      <label class="field"
        >Dataset — JSON array of {'{ features, label }'} (label 0 or 1)
        <textarea bind:value={trainData} aria-label="training dataset" rows="7" spellcheck="false"
        ></textarea></label
      >
    </form>
    {#if trainReport}
      <div class="report" data-testid="train-report">
        <div class="metrics">
          <span><b>{trainReport.rows}</b> rows · <b>{trainReport.positives}</b> positive</span>
          <span>CV AUC <b>{trainReport.cv_auc.toFixed(3)}</b></span>
          <span>CV accuracy <b>{(trainReport.cv_accuracy * 100).toFixed(1)}%</b></span>
          <span>log-loss <b>{trainReport.train_log_loss.toFixed(3)}</b></span>
        </div>
        <table class="importance">
          <thead>
            <tr><th>Feature</th><th>Coefficient</th><th>Importance</th></tr>
          </thead>
          <tbody>
            {#each trainReport.importance as fi (fi.feature)}
              <tr>
                <td>{fi.feature}</td>
                <td>{fi.coefficient.toFixed(4)}</td>
                <td>{(fi.importance * 100).toFixed(1)}%</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </details>

  {#if loading}
    <Skeleton rows={3} />
  {:else if error}
    <p class="err">{error}</p>
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
          <tr
            ><th>Name</th><th>Kind</th><th>Owner</th><th>Updated</th><th
              ><Hint label="Drift"
                >Population Stability Index (PSI) of the model's recent prediction distribution
                versus its captured baseline — <code>&gt;0.2</code> signals meaningful drift. "no baseline"
                means none has been captured yet.</Hint
              ></th
            ><th></th></tr
          >
        </thead>
        <tbody>
          {#each models as m (m.name)}
            <tr>
              <td>{m.name}</td>
              <td><span class="badge">{m.kind || '—'}</span></td>
              <td class="muted">{m.owner || '—'}</td>
              <td class="muted"><RelativeTime value={m.updated_at} /></td>
              <td>
                {#if driftStatus.get(m.name)}
                  {@const s = driftStatus.get(m.name)}
                  <Badge tone={s?.tone ?? 'neutral'}>{s?.label}</Badge>
                {:else}
                  <span class="muted">…</span>
                {/if}
              </td>
              <td class="rowactions"
                ><button class="link" onclick={() => toggleDrift(m.name)}
                  >{driftOpen === m.name ? 'Hide drift' : 'Drift'}</button
                >
                <button
                  class="link"
                  onclick={() => toggleDiscuss(m.name)}
                  aria-label={`discuss ${m.name}`}
                  >{discussOpen === m.name ? 'Hide discussion' : 'Discuss'}</button
                ></td
              >
            </tr>
            {#if discussOpen === m.name}
              <tr class="drift-row" data-testid="model-discussion">
                <td colspan="6">
                  <CommentThread subjectType="model" subjectId={m.name} title="Model discussion" />
                </td>
              </tr>
            {/if}
            {#if driftOpen === m.name}
              <tr class="drift-row" data-testid="model-drift">
                <td colspan="6">
                  {#if driftError}
                    <p class="err">
                      Couldn't load drift: {driftError}
                      <button class="link" onclick={() => loadDrift(m.name)}>Retry</button>
                    </p>
                  {:else if driftLoading || !drift}
                    <p class="muted" aria-busy="true">Loading drift…</p>
                  {:else if drift.count === 0}
                    <p class="muted">
                      No predictions recorded yet for this model — run decisions through a Predict
                      node that references it.
                    </p>
                  {:else}
                    <div class="drift-head">
                      <span><b>{drift.count}</b> predictions</span>
                      <label class="win">
                        window
                        <select
                          aria-label="drift window"
                          value={String(driftWindow)}
                          onchange={(e) => {
                            driftWindow = Number(e.currentTarget.value);
                            loadDrift(m.name);
                          }}
                        >
                          <option value="0">all-time</option>
                          <option value="7">last 7 days</option>
                          <option value="30">last 30 days</option>
                        </select>
                      </label>
                      {#if drift.psi != null}
                        <span class="psi {psiLabel(drift.psi).split(' ')[0]}"
                          >PSI {drift.psi.toFixed(3)} · {psiLabel(drift.psi)}</span
                        >
                        {#if drift.firing}<span class="psi significant" data-testid="drift-firing"
                            >⚠ firing (&gt; {drift.threshold})</span
                          >{/if}
                        {#if drift.alerting}<span
                            class="psi significant"
                            data-testid="drift-alerting"
                            title="The drift scheduler has pushed an alert to your webhooks"
                            >📤 alert pushed</span
                          >{/if}
                      {:else}
                        <span class="muted">no baseline captured yet</span>
                      {/if}
                      <button
                        onclick={() => captureBaseline(m.name)}
                        disabled={!roleAtLeast($user?.role, 'editor')}
                        title={!roleAtLeast($user?.role, 'editor')
                          ? 'Requires the editor role'
                          : undefined}>Capture baseline</button
                      >
                      <label class="win">
                        alert PSI &gt;
                        <input
                          class="thresh"
                          bind:value={thresholdInput}
                          aria-label="drift threshold"
                          placeholder="0.25"
                          inputmode="decimal"
                        />
                      </label>
                      <button
                        onclick={() => saveThreshold(m.name)}
                        disabled={!roleAtLeast($user?.role, 'editor')}
                        title={!roleAtLeast($user?.role, 'editor')
                          ? 'Requires the editor role'
                          : undefined}>Set monitor</button
                      >
                    </div>
                    <div class="hist" aria-label="Predicted-probability distribution (deciles)">
                      {#each drift.hist as c, i (i)}
                        <div class="hist-col" title={`${i * 10}–${i * 10 + 10}%: ${c}`}>
                          <div
                            class="hist-bar"
                            style="height:{histBarHeight(c, drift.hist)}%"
                          ></div>
                        </div>
                      {/each}
                    </div>
                    {#if drift.features && drift.features.length > 0}
                      <div class="covariate" data-testid="covariate-drift">
                        <p class="sub">Covariate drift (input features vs baseline)</p>
                        <table class="ftable">
                          <thead>
                            <tr><th>Feature</th><th>Mean shift</th><th>Var ratio</th><th></th></tr>
                          </thead>
                          <tbody>
                            {#each drift.features as f (f.feature)}
                              <tr>
                                <td>{f.feature}</td>
                                <td>{f.mean_shift.toFixed(2)}σ</td>
                                <td>{f.var_ratio.toFixed(2)}×</td>
                                <td>
                                  {#if f.drifting}<span class="psi significant">drifting</span
                                    >{:else}<span class="muted">stable</span>{/if}
                                </td>
                              </tr>
                            {/each}
                          </tbody>
                        </table>
                      </div>
                    {/if}
                    {#if perf && perf.count > 0}
                      <div class="perf" data-testid="model-performance">
                        <p class="sub">Live performance (from {perf.count} recorded actuals)</p>
                        <div class="metrics">
                          <span>AUC <b>{perf.auc.toFixed(3)}</b></span>
                          <span>Accuracy <b>{(perf.accuracy * 100).toFixed(1)}%</b></span>
                          <span>Brier <b>{perf.brier.toFixed(3)}</b></span>
                        </div>
                      </div>
                    {/if}
                  {/if}
                </td>
              </tr>
            {/if}
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
  .train {
    margin: 0.5rem 0 1rem;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    padding: 0.5rem 0.8rem;
  }
  .train summary {
    cursor: pointer;
    font-weight: 600;
  }
  .report {
    margin-top: 0.6rem;
  }
  .report .metrics {
    display: flex;
    flex-wrap: wrap;
    gap: 1rem;
    font-size: 0.9rem;
    margin-bottom: 0.5rem;
  }
  .covariate,
  .perf {
    margin-top: 0.6rem;
  }
  .sub {
    font-size: 0.8rem;
    color: var(--fg-subtle);
    margin: 0.4rem 0 0.2rem;
  }
  table.ftable {
    border-collapse: collapse;
    font-size: 0.85rem;
  }
  table.ftable th,
  table.ftable td {
    text-align: left;
    padding: 0.15rem 0.8rem 0.15rem 0;
  }
  .perf .metrics {
    display: flex;
    gap: 1.2rem;
    font-size: 0.9rem;
  }
  table.importance {
    border-collapse: collapse;
    font-size: 0.85rem;
  }
  table.importance th,
  table.importance td {
    text-align: left;
    padding: 0.2rem 0.8rem 0.2rem 0;
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
  .link {
    border: none;
    background: none;
    padding: 0;
    color: var(--link, var(--accent-ink));
    cursor: pointer;
  }
  .rowactions {
    white-space: nowrap;
  }
  .rowactions .link + .link {
    margin-left: 0.6rem;
  }
  .drift-row td {
    background: var(--surface-2);
  }
  .drift-head {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.8rem;
    margin-bottom: 0.6rem;
    font-size: 0.9rem;
  }
  .psi {
    padding: 0.1rem 0.5rem;
    border-radius: 999px;
    font-size: 0.78rem;
    background: color-mix(in srgb, var(--ok, #16a34a) 16%, transparent);
    color: var(--ok, #16a34a);
  }
  .psi.moderate {
    background: color-mix(in srgb, #d97706 18%, transparent);
    color: #b45309;
  }
  .psi.significant {
    background: color-mix(in srgb, var(--danger) 16%, transparent);
    color: var(--danger);
  }
  .win {
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
    font-size: 0.8rem;
    color: var(--fg-subtle);
  }
  .thresh {
    width: 4rem;
    padding: 0.2rem 0.4rem;
    font: inherit;
  }
  .hist {
    display: flex;
    align-items: flex-end;
    gap: 0.2rem;
    height: 4rem;
  }
  .hist-col {
    flex: 1;
    display: flex;
    align-items: flex-end;
    height: 100%;
  }
  .hist-bar {
    width: 100%;
    min-height: 2px;
    border-radius: 2px 2px 0 0;
    background: var(--accent);
  }
</style>
