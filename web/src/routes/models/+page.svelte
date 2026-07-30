<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Predictive model registry: define models as data (logistic | gbm | expression |
     external), evaluated by the engine and referenced from a Predict node.
     Everything goes through the documented /v1/models API. -->
<script lang="ts">
  import { onMount, tick } from 'svelte';
  import { page } from '$app/stores';
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
    recordModelOutcome,
    whenPermitted,
    captureModelBaseline,
    setModelMonitor,
    requestModelApproval,
    approveModel,
    rejectModel,
    recordModelValidation,
    modelApproved,
    type Model,
    type ModelValidation,
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

  // The model whose governance (four-eyes approval + validation) panel is open.
  let govOpen = $state('');
  let govBusy = $state(false);
  let govDecision = $state<{ name: string; approve: boolean } | null>(null);
  let govReason = $state('');
  // Validation-evidence form fields for the open governance panel.
  let valDataset = $state('');
  let valMetrics = $state('');
  let valNotes = $state('');
  let valPassed = $state(true);

  function toggleGov(name: string) {
    govOpen = govOpen === name ? '' : name;
    govDecision = null;
    govReason = '';
  }

  // approvalStatus renders the model's four-eyes state as a badge tone + label.
  function approvalStatus(m: Model): { tone: Tone; label: string } {
    if (modelApproved(m)) return { tone: 'ok', label: 'approved' };
    if (m.pending) return { tone: 'warn', label: 'pending review' };
    return { tone: 'danger', label: 'not approved' };
  }

  function latestIndependentValidation(m: Model): ModelValidation | undefined {
    return [...(m.validations ?? [])]
      .reverse()
      .find(
        (v) =>
          v.version === (m.version ?? 0) &&
          (v.recorded_by ?? v.validator) !== m.owner &&
          !!(v.recorded_by ?? v.validator)
      );
  }

  // refreshModels reloads just the registry list (governance state changes) without
  // the heavier per-model drift refetch that the full load() does.
  async function refreshModels() {
    try {
      models = await listModels(key);
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    }
  }

  async function doRequestApproval(name: string) {
    govBusy = true;
    try {
      await requestModelApproval(key, name);
      toast.success('Approval requested.');
      await refreshModels();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e));
    } finally {
      govBusy = false;
    }
  }

  function startGovDecision(name: string, approve: boolean) {
    govDecision = { name, approve };
    govReason = '';
  }

  async function doApprove(m: Model, approve: boolean) {
    if (!m.pending) return;
    const reason = govReason.trim();
    if (!reason) {
      toast.error(`A reason for ${approve ? 'approval' : 'rejection'} is required.`);
      return;
    }
    govBusy = true;
    try {
      if (approve) await approveModel(key, m.name, m.pending.request_id, reason);
      else await rejectModel(key, m.name, m.pending.request_id, reason);
      toast.success(approve ? 'Model approved.' : 'Request rejected.');
      govDecision = null;
      govReason = '';
      await refreshModels();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e));
    } finally {
      govBusy = false;
    }
  }

  // parseMetrics reads the "name=value, …" metrics box into a numeric map, ignoring
  // blank entries and failing loudly on a non-numeric value rather than dropping it.
  function parseMetrics(raw: string): Record<string, number> {
    const out = new Map<string, number>();
    for (const pair of raw.split(',')) {
      const t = pair.trim();
      if (!t) continue;
      const [k, v] = t.split('=').map((x) => x.trim());
      const n = Number(v);
      if (!k || !Number.isFinite(n)) throw new Error(`bad metric "${t}" (use name=number)`);
      out.set(k, n);
    }
    return Object.fromEntries(out);
  }

  async function doRecordValidation(name: string) {
    govBusy = true;
    try {
      const metrics = parseMetrics(valMetrics);
      if (!valDataset.trim()) throw new Error('A validation dataset is required.');
      if (!Object.keys(metrics).length) throw new Error('At least one named metric is required.');
      if (!valNotes.trim()) throw new Error('Validation notes are required.');
      await recordModelValidation(key, name, {
        dataset: valDataset.trim(),
        metrics,
        notes: valNotes.trim(),
        passed: valPassed
      });
      toast.success('Validation evidence recorded.');
      valDataset = valMetrics = valNotes = '';
      valPassed = true;
      await refreshModels();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e));
    } finally {
      govBusy = false;
    }
  }

  // An at-a-glance drift status per model, fetched once on load so the table shows
  // which models are drifting without the operator expanding each row.
  type DriftStatus = { tone: Tone; label: string; error?: string };
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
  // Fetch every model's all-time drift status concurrently. A single failure does
  // not blank the registry, but it must land as an explicit unavailable badge.
  // A token drops an older sweep entirely, and successes merge per model, so a
  // slow sweep can't clobber newer statuses (e.g. the per-row loadDrift updates).
  let driftStatusSeq = 0;
  async function loadDriftStatuses(ms: Model[]) {
    const seq = ++driftStatusSeq;
    const results = await Promise.all(
      ms.map(async (model) => {
        try {
          return { model, status: statusFromDrift(await modelDrift(key, model.name, 0)) };
        } catch (error) {
          return { model, error };
        }
      })
    );
    if (seq !== driftStatusSeq) return; // a newer sweep superseded this one
    const next = new Map(driftStatus);
    for (const result of results) {
      if (result.status) next.set(result.model.name, result.status);
      else
        next.set(result.model.name, {
          tone: 'danger',
          label: 'unavailable',
          error: msg(result.error)
        });
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
  let actualLabel = $state('1');
  let actualDecisionID = $state('');
  let actualNodeID = $state('');
  let actualBusy = $state(false);
  let performanceOutcomeKey = $state('');
  let performanceNodeID = $state('');
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
    const reqOutcomeKey = performanceOutcomeKey.trim();
    const reqNodeID = performanceNodeID.trim();
    try {
      const [got, gotPerf] = await Promise.all([
        modelDrift(key, m, driftWindow),
        // A model with no recorded actuals comes back as an empty performance record
        // with a 200, so only a 403 blanks this panel; a real failure reaches the
        // handler below rather than reading as "this model has no measured outcomes".
        whenPermitted(
          getModelPerformance(key, m, {
            outcome_key: reqOutcomeKey || undefined,
            node_id: reqNodeID || undefined
          }),
          null
        )
      ]);
      if (
        driftOpen !== reqModel ||
        driftWindow !== reqWindow ||
        performanceOutcomeKey.trim() !== reqOutcomeKey ||
        performanceNodeID.trim() !== reqNodeID
      )
        return;
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
    performanceOutcomeKey = '';
    performanceNodeID = '';
    await loadDrift(m);
  }
  async function applyPerformanceSource(m: string) {
    if (!performanceOutcomeKey.trim() && performanceNodeID.trim()) {
      toast.error('Choose a business outcome key before filtering by Predict node.');
      return;
    }
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
  async function recordActual(m: string) {
    const label = Number(actualLabel);
    if (label !== 0 && label !== 1) {
      toast.error('Realized outcome must be 0 or 1.');
      return;
    }
    const decisionID = actualDecisionID.trim();
    if (!decisionID) {
      toast.error('Decision ID is required so the recorded prediction can be verified.');
      return;
    }
    actualBusy = true;
    try {
      await recordModelOutcome(key, m, {
        label,
        decision_id: decisionID,
        node_id: actualNodeID.trim() || undefined
      });
      actualDecisionID = '';
      actualNodeID = '';
      await loadDrift(m);
      toast.success(`Actual outcome recorded for ${m}.`);
    } catch (e) {
      toast.error(msg(e));
    } finally {
      actualBusy = false;
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
      const requestedGovernance = $page.url.searchParams.get('governance') ?? '';
      const requestedDiscussion = $page.url.searchParams.get('discussion') ?? '';
      if (requestedGovernance && requestedDiscussion) {
        throw new Error('Open either model governance or discussion, not both.');
      }
      const requestedModel = requestedGovernance || requestedDiscussion;
      if (requestedModel && !models.some((m) => m.name === requestedModel)) {
        throw new Error(`Model ${requestedModel} was not found in this workspace.`);
      }
      if (requestedGovernance) {
        govOpen = requestedGovernance;
      } else if (requestedDiscussion) {
        discussOpen = requestedDiscussion;
      }
      if (requestedModel) {
        await tick();
        document
          .getElementById(requestedGovernance ? 'model-governance' : 'model-discussion')
          ?.scrollIntoView({ block: 'start' });
      }
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
              ><Hint label="Approval"
                >Four-eyes governance: a model version must be approved by a different actor than
                its author before it can serve decisions outside the sandbox — the same
                maker-checker gate flows use.</Hint
              ></th
            ><th
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
                <Badge tone={approvalStatus(m).tone}>{approvalStatus(m).label}</Badge>
              </td>
              <td>
                {#if driftStatus.get(m.name)}
                  {@const s = driftStatus.get(m.name)}
                  <Badge tone={s?.tone ?? 'neutral'} title={s?.error}>{s?.label}</Badge>
                {:else}
                  <span class="muted">…</span>
                {/if}
              </td>
              <td class="rowactions"
                ><button class="link" onclick={() => toggleGov(m.name)}
                  >{govOpen === m.name ? 'Hide governance' : 'Governance'}</button
                >
                <button class="link" onclick={() => toggleDrift(m.name)}
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
            {#if govOpen === m.name}
              <tr id="model-governance" class="drift-row" data-testid="model-governance">
                <td colspan="7">
                  <div class="gov">
                    <div class="gov-status">
                      <b>Version {m.version ?? 1}</b>
                      {#if modelApproved(m)}
                        <Badge tone="ok">approved v{m.approved_version}</Badge>
                        {#if m.approved_by}<span class="muted">by {m.approved_by}</span>{/if}
                      {:else if m.pending}
                        <Badge tone="warn">pending review</Badge>
                        <span class="muted">requested by {m.pending.requested_by}</span>
                      {:else}
                        <Badge tone="danger">not approved</Badge>
                        <span class="muted">unapproved models are refused outside the sandbox</span>
                      {/if}
                    </div>

                    <div class="gov-actions">
                      {#if !m.pending && !modelApproved(m) && roleAtLeast($user?.role, 'editor')}
                        <button
                          class="btn"
                          disabled={govBusy}
                          onclick={() => doRequestApproval(m.name)}>Request approval</button
                        >
                      {/if}
                      {#if m.pending && roleAtLeast($user?.role, 'approver')}
                        {#if govDecision?.name === m.name}
                          <input
                            class="decision-reason"
                            bind:value={govReason}
                            aria-label="model decision reason"
                            placeholder={govDecision.approve
                              ? 'Why this model is approved'
                              : 'Why this request is rejected'}
                          />
                          <button
                            class:primary={govDecision.approve}
                            class="btn"
                            disabled={govBusy}
                            onclick={() => doApprove(m, govDecision?.approve ?? false)}
                          >
                            Confirm {govDecision.approve ? 'approval' : 'rejection'}
                          </button>
                          <button
                            class="btn"
                            disabled={govBusy}
                            onclick={() => {
                              govDecision = null;
                              govReason = '';
                            }}>Cancel</button
                          >
                        {:else}
                          <button
                            class="btn primary"
                            disabled={govBusy ||
                              m.owner === $user?.actor ||
                              m.pending.requested_by === $user?.actor ||
                              !latestIndependentValidation(m)?.passed}
                            title={m.owner === $user?.actor ||
                            m.pending.requested_by === $user?.actor
                              ? 'Four-eyes: you authored or requested this — a different approver must approve'
                              : !latestIndependentValidation(m)
                                ? 'Record independent validation for this version before approval'
                                : !latestIndependentValidation(m)?.passed
                                  ? 'The latest independent validation failed'
                                  : undefined}
                            onclick={() => startGovDecision(m.name, true)}>Approve</button
                          >
                          <button
                            class="btn"
                            disabled={govBusy}
                            onclick={() => startGovDecision(m.name, false)}>Reject</button
                          >
                          <span class="muted small"
                            >Four-eyes plus a passing independent validation of this version are
                            required.</span
                          >
                        {/if}
                      {/if}
                    </div>

                    <div class="gov-validation">
                      <h4>Validation evidence</h4>
                      {#if latestIndependentValidation(m)}
                        <p class="muted small">
                          Current independent validation:
                          <Badge tone={latestIndependentValidation(m)?.passed ? 'ok' : 'danger'}>
                            {latestIndependentValidation(m)?.passed ? 'pass' : 'fail'}
                          </Badge>
                          by {latestIndependentValidation(m)?.recorded_by ??
                            latestIndependentValidation(m)?.validator}
                        </p>
                      {:else}
                        <p class="muted">No independent validation for this version yet.</p>
                      {/if}
                      {#if m.validations && m.validations.length}
                        <ul class="val-list">
                          {#each m.validations as v (v.recorded_at)}
                            <li>
                              <Badge tone={v.passed ? 'ok' : 'danger'}
                                >{v.passed ? 'pass' : 'fail'}</Badge
                              >
                              v{v.version}
                              {#if v.dataset}· {v.dataset}{/if}
                              {#if v.metrics}· {Object.entries(v.metrics)
                                  .map(([k, val]) => `${k}=${val}`)
                                  .join(' ')}{/if}
                              {#if v.validator}<span class="muted">— {v.validator}</span>{/if}
                            </li>
                          {/each}
                        </ul>
                      {/if}

                      {#if roleAtLeast($user?.role, 'approver')}
                        <div class="val-form">
                          <input bind:value={valDataset} placeholder="dataset (e.g. backtest_Q1)" />
                          <input
                            bind:value={valMetrics}
                            placeholder="metrics (auc=0.81, ks=0.42)"
                          />
                          <input bind:value={valNotes} placeholder="independent review notes" />
                          <label class="pass"
                            ><input type="checkbox" bind:checked={valPassed} /> passed</label
                          >
                          <button
                            class="btn"
                            disabled={govBusy || m.owner === $user?.actor}
                            title={m.owner === $user?.actor
                              ? 'The model author cannot validate their own version'
                              : undefined}
                            onclick={() => doRecordValidation(m.name)}>Record validation</button
                          >
                        </div>
                      {/if}
                    </div>
                  </div>
                </td>
              </tr>
            {/if}
            {#if discussOpen === m.name}
              <tr id="model-discussion" class="drift-row" data-testid="model-discussion">
                <td colspan="7">
                  <CommentThread subjectType="model" subjectId={m.name} title="Model discussion" />
                </td>
              </tr>
            {/if}
            {#if driftOpen === m.name}
              <tr class="drift-row" data-testid="model-drift">
                <td colspan="7">
                  {#if roleAtLeast($user?.role, 'editor')}
                    <div class="actual-form">
                      <div>
                        <p class="sub">Legacy model-specific actual</p>
                        <p class="muted small">
                          Compatibility path for existing integrations. New evidence should be
                          recorded as a corrected business outcome from the decision detail, then
                          selected below. The backend still recovers the exact recorded probability
                          and model version; you cannot author them here.
                        </p>
                      </div>
                      <label>
                        Decision ID
                        <input
                          aria-label="actual decision id"
                          bind:value={actualDecisionID}
                          placeholder="required lineage"
                        />
                      </label>
                      <label>
                        Outcome
                        <select aria-label="actual outcome" bind:value={actualLabel}>
                          <option value="1">1 — positive</option>
                          <option value="0">0 — negative</option>
                        </select>
                      </label>
                      <label>
                        Predict node ID (only if repeated)
                        <input
                          aria-label="actual predict node id"
                          bind:value={actualNodeID}
                          placeholder="usually blank"
                        />
                      </label>
                      <button
                        class="btn"
                        disabled={actualBusy}
                        onclick={() => recordActual(m.name)}
                      >
                        {actualBusy ? 'Recording…' : 'Record actual'}
                      </button>
                    </div>
                  {/if}
                  <div class="performance-source">
                    <div>
                      <p class="sub">Performance evidence</p>
                      <p class="muted small">
                        Enter a business outcome key to measure this model from the corrected,
                        decision-linked facts also used by experiments. Leave it blank to inspect
                        legacy model-specific actuals.
                      </p>
                    </div>
                    <label>
                      Business outcome key
                      <input
                        aria-label="performance outcome key"
                        bind:value={performanceOutcomeKey}
                        placeholder="e.g. defaulted_90d"
                      />
                    </label>
                    <label>
                      Predict node ID (only if repeated)
                      <input
                        aria-label="performance predict node id"
                        bind:value={performanceNodeID}
                        placeholder="usually blank"
                      />
                    </label>
                    <button
                      class="btn"
                      disabled={driftLoading}
                      onclick={() => applyPerformanceSource(m.name)}
                    >
                      {driftLoading ? 'Loading…' : 'Apply evidence'}
                    </button>
                  </div>
                  {#if perf && perf.count > 0}
                    <div class="perf" data-testid="model-performance">
                      <p class="sub">
                        Live performance{perf.model_version
                          ? ` · model v${perf.model_version}`
                          : ''}
                        {perf.outcome_key ? ` · ${perf.outcome_key}` : ''}
                        (from {perf.count} recorded
                        {perf.count === 1 ? 'actual' : 'actuals'})
                      </p>
                      <div class="metrics">
                        <span>AUC <b>{perf.auc.toFixed(3)}</b></span>
                        <span>Accuracy <b>{(perf.accuracy * 100).toFixed(1)}%</b></span>
                        <span>Brier <b>{perf.brier.toFixed(3)}</b></span>
                      </div>
                      {#if perf.excluded_actuals}
                        <p class="muted small">
                          {perf.excluded_actuals}
                          {perf.excluded_actuals === 1 ? 'actual was' : 'actuals were'} excluded because
                          exact current-version prediction lineage was missing or ambiguous.
                        </p>
                      {/if}
                    </div>
                  {:else if perf?.outcome_key && !driftLoading}
                    <p class="muted">
                      No current-version predictions are joined to binary outcome
                      <code>{perf.outcome_key}</code> for this model and node selection.
                    </p>
                  {/if}
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
  .actual-form,
  .performance-source {
    display: grid;
    grid-template-columns: minmax(12rem, 2fr) repeat(3, minmax(8rem, 1fr)) auto;
    align-items: end;
    gap: 0.6rem;
    margin-bottom: 0.8rem;
    padding-bottom: 0.8rem;
    border-bottom: 1px solid var(--border);
  }
  .performance-source {
    grid-template-columns: minmax(12rem, 2fr) repeat(2, minmax(8rem, 1fr)) auto;
  }
  .actual-form label,
  .performance-source label {
    display: flex;
    flex-direction: column;
    gap: 0.2rem;
    color: var(--fg-subtle);
    font-size: 0.78rem;
  }
  .actual-form input,
  .actual-form select,
  .performance-source input {
    min-width: 0;
    padding: 0.35rem 0.45rem;
    border: 1px solid var(--border);
    border-radius: 6px;
    background: var(--surface);
    color: var(--fg);
    font: inherit;
  }
  .decision-reason {
    min-width: 16rem;
    border: 1px solid var(--border);
    border-radius: 6px;
    background: var(--surface);
    color: var(--fg);
  }
  @media (max-width: 900px) {
    .actual-form,
    .performance-source {
      grid-template-columns: 1fr 1fr;
    }
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
