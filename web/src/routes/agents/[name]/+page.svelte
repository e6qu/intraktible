<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { onDestroy } from 'svelte';
  import { page } from '$app/stores';
  import Icon from '$lib/Icon.svelte';
  import RelativeTime from '$lib/RelativeTime.svelte';
  import Badge from '$lib/Badge.svelte';
  import Skeleton from '$lib/Skeleton.svelte';
  import EmptyState from '$lib/EmptyState.svelte';
  import CommentThread from '$lib/CommentThread.svelte';
  import { statusTone } from '$lib/badge';
  import { toast } from '$lib/toast';
  import { waitForApplied } from '$lib/poll';
  import {
    getAgent,
    runAgent,
    startAgentRun,
    cancelAgentRun,
    retryAgentRun,
    listAgentRuns,
    escalateRun,
    listAgentVersions,
    getAgentEvals,
    setAgentEvals,
    runAgentEval,
    ApiError,
    type Agent,
    type AgentRun,
    type AgentVersion,
    type EvalCase,
    type EvalReport,
    type RunResult
  } from '$lib/api';
  import { appHref } from '$lib/paths';
  import { roleAtLeast } from '$lib/roles';
  import { user } from '$lib/session';

  // outputText renders a run/result's text, structured JSON, or error as a string
  // for the inline output panels and run cards.
  function outputText(o: { text?: string; structured?: unknown; error?: string }): string {
    if (o.error) return 'error: ' + o.error;
    if (o.text) return o.text;
    if (o.structured != null) return JSON.stringify(o.structured, null, 2);
    return '(no output)';
  }
  function truncate(s: string, n: number): string {
    return s.length > n ? s.slice(0, n) + '…' : s;
  }

  // API calls authenticate via the session cookie (empty key -> no X-Api-Key header).
  const key = '';
  let agent = $state<Agent | null>(null);
  let runs = $state<AgentRun[]>([]);
  let versions = $state<AgentVersion[]>([]);
  let error = $state('');
  // A missing agent (real 404) is a distinct, expected state — a stale/mistyped name —
  // shown as a "not found" EmptyState rather than a raw error with a doomed Retry.
  let notFound = $state(false);
  let loading = $state(true);

  let prompt = $state('');
  let asyncRun = $state(true);
  let runVersion = $state(0);
  let timeoutMs = $state(60_000);
  let maxAttempts = $state(3);
  let idempotencyKey = $state('');
  let businessReference = $state('');
  let correlationId = $state('');
  let lastRunID = $state('');
  const runControlsValid = $derived(
    Number.isInteger(runVersion) &&
      runVersion >= 0 &&
      (!asyncRun ||
        (Number.isInteger(timeoutMs) &&
          timeoutMs >= 1 &&
          timeoutMs <= 600_000 &&
          Number.isInteger(maxAttempts) &&
          maxAttempts >= 1 &&
          maxAttempts <= 10))
  );
  // The just-completed (non-stream) run's result, shown inline so a Run gives
  // visible output instead of silently appending to the Runs list.
  let lastResult = $state<RunResult | null>(null);

  // Offline eval: cases edited as JSON, run on demand (record-nothing), scored.
  let evalText = $state('');
  let evalReport = $state<EvalReport | null>(null);
  let evalBusy = $state(false);

  // Derive from the route param so navigating between sibling agents reloads
  // rather than showing the first agent's data.
  const name = $derived($page.params.name ?? '');

  let pollTimer: ReturnType<typeof setTimeout> | undefined;
  function schedulePoll() {
    if (pollTimer) clearTimeout(pollTimer);
    if (runs.some((r) => r.status === 'running' || r.status === 'retrying')) {
      pollTimer = setTimeout(() => void load(true), 1_000);
    }
  }

  async function load(background = false) {
    if (pollTimer) clearTimeout(pollTimer);
    error = '';
    notFound = false;
    if (!background) {
      loading = true;
      // Clear the prior agent so a failed navigation can't leave the previous
      // agent's data on screen under an error.
      agent = null;
      runs = [];
      versions = [];
    }
    // Drop a stale response when sibling navigation changes name mid-flight.
    const reqName = name;
    try {
      const [a, r, v, ec] = await Promise.all([
        getAgent(key, name),
        listAgentRuns(key, name),
        listAgentVersions(key, name),
        getAgentEvals(key, name)
      ]);
      if (name !== reqName) return;
      [agent, runs, versions] = [a, r, v];
      const tracked = lastRunID ? r.find((run) => run.run_id === lastRunID) : undefined;
      if (tracked) {
        lastResult = {
          run_id: tracked.run_id,
          status: tracked.status,
          text: tracked.text,
          structured: tracked.structured,
          error: tracked.error,
          seq: tracked.seq
        };
      }
      evalText = ec.length > 0 ? JSON.stringify(ec, null, 2) : '';
    } catch (e) {
      if (name === reqName) {
        if (e instanceof ApiError && e.status === 404) notFound = true;
        else error = e instanceof Error ? e.message : String(e);
      }
    } finally {
      if (name === reqName) {
        if (!background) loading = false;
        schedulePoll();
      }
    }
  }

  async function saveEvals() {
    error = '';
    evalBusy = true;
    try {
      const cases = (evalText.trim() ? JSON.parse(evalText) : []) as EvalCase[];
      await setAgentEvals(key, name, cases);
      toast.success(`Saved ${cases.length} eval case${cases.length === 1 ? '' : 's'}`);
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      evalBusy = false;
    }
  }

  async function runEval() {
    error = '';
    evalBusy = true;
    try {
      evalReport = await runAgentEval(key, name);
      toast.success(`${evalReport.passed}/${evalReport.total} eval cases passed`);
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      evalBusy = false;
    }
  }

  let running = $state(false);
  async function run() {
    error = '';
    running = true;
    try {
      const res = asyncRun
        ? await startAgentRun(key, name, prompt, {
            version: runVersion,
            timeoutMs,
            maxAttempts,
            idempotencyKey: idempotencyKey.trim() || undefined,
            businessReference: businessReference.trim() || undefined,
            correlationId: correlationId.trim() || undefined
          })
        : await runAgent(key, name, prompt, runVersion);
      lastRunID = res.run_id;
      lastResult = res;
      await load(true);
      // The inline panel already shows the output; a toast confirms the run landed (and
      // flags a failed run, which reads the same as success without it).
      if (res.status === 'failed') toast.error('Agent run failed');
      else if (res.status === 'running') toast.success('Run accepted by the durable worker queue');
      else toast.success('Run complete');
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      running = false;
    }
  }

  let runAction = $state('');
  function canRetry(r: AgentRun): boolean {
    return (
      ['failed', 'cancelled', 'timed_out', 'dead_letter'].includes(r.status) &&
      (r.attempt ?? 1) < (r.max_attempts ?? 3)
    );
  }
  async function cancelRun(r: AgentRun) {
    if (runAction || !confirm('Cancel this asynchronous run?')) return;
    runAction = r.run_id;
    try {
      await cancelAgentRun(key, r.run_id);
      toast.success('Cancellation requested');
      await load();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e));
    } finally {
      runAction = '';
    }
  }
  async function retryRun(r: AgentRun) {
    if (runAction) return;
    const reason = window.prompt('Why is this run safe and necessary to retry?')?.trim();
    if (!reason) return;
    const acknowledge = r.status === 'dead_letter';
    if (
      acknowledge &&
      !confirm(
        'This provider call may already have completed. Retrying can repeat an external side effect. Acknowledge and continue?'
      )
    )
      return;
    runAction = r.run_id;
    try {
      await retryAgentRun(key, r.run_id, reason, acknowledge);
      toast.success('Retry accepted');
      await load();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e));
    } finally {
      runAction = '';
    }
  }

  // The run currently being escalated (per-run, so the spinner only disables that
  // run's button — not every button on the page). The durable run.case_id is the
  // escalation state; it survives reload/replay and the backend returns it
  // idempotently if a request is retried.
  let escalating = $state('');
  // A completed run that has no linked case is the only one worth escalating.
  function canEscalate(r: AgentRun): boolean {
    return r.status === 'completed' && !r.case_id;
  }
  async function escalate(r: AgentRun) {
    if (escalating === r.run_id) return; // guard against double-click on this run
    // Escalation opens a real human-review case — confirm the side effect.
    if (!confirm('Open a human-review case from this run?')) return;
    error = '';
    escalating = r.run_id;
    try {
      // Carry the run's prompt into the case title so the reviewer sees what was
      // asked; the run's output and run_id ride along in the case context the
      // backend records, so the case is self-explanatory rather than a bare stub.
      const title = `Review: ${name} — "${truncate(r.prompt, 60)}"`;
      const { case_id } = await escalateRun(key, name, r.run_id, {
        company_name: title,
        case_type: 'agent_review',
        sla_days: 3
      });
      runs = runs.map((existing) =>
        existing.run_id === r.run_id ? { ...existing, case_id } : existing
      );
      toast.success(`Opened review case ${case_id.slice(0, 8)} (see Cases)`);
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      escalating = '';
    }
  }

  // --- streaming run (configurable transport: SSE or WebSocket) ---
  let transport = $state<'sse' | 'ws'>('sse');
  let streamPrompt = $state('');
  let streamText = $state('');
  let streaming = $state(false);
  // Track the live connection so we can tear it down on unmount or before
  // starting a new one — a leaked socket keeps mutating $state after navigation.
  let activeES: EventSource | null = null;
  let activeWS: WebSocket | null = null;

  function closeStream() {
    activeES?.close();
    activeES = null;
    activeWS?.close();
    activeWS = null;
  }

  // Fail the stream loudly: a single malformed frame must surface an error and
  // release the UI, not throw inside the event handler (which the EventSource /
  // WebSocket dispatcher swallows, leaving the button stuck on "Streaming…").
  function failStream(reason: string) {
    error = reason;
    streaming = false;
    closeStream();
  }

  async function finishStream(seq: unknown) {
    if (typeof seq !== 'number' || !Number.isFinite(seq) || seq <= 0) {
      failStream('stream completed without a durable event acknowledgement');
      return;
    }
    try {
      await waitForApplied(seq);
      streaming = false;
      closeStream();
      await load();
    } catch (e) {
      failStream(e instanceof Error ? e.message : String(e));
    }
  }
  // Parse one chunk frame's text, returning null on malformed/ill-shaped data.
  function chunkText(raw: string): string | null {
    try {
      const parsed: unknown = JSON.parse(raw);
      const t = (parsed as { text?: unknown })?.text;
      return typeof t === 'string' ? t : null;
    } catch (_parseError) {
      return null;
    }
  }

  function streamSSE() {
    streamText = '';
    streaming = true;
    const es = new EventSource(
      `/v1/agents/${encodeURIComponent(name)}/run/stream?prompt=${encodeURIComponent(streamPrompt)}`
    );
    activeES = es;
    es.addEventListener('chunk', (e) => {
      const t = chunkText((e as MessageEvent).data);
      if (t === null) {
        failStream('stream returned a malformed chunk');
        return;
      }
      streamText += t;
    });
    es.addEventListener('done', (e) => {
      try {
        const payload = JSON.parse((e as MessageEvent).data) as { seq?: unknown };
        // The server closes the SSE response after `done`; close locally first so
        // EventSource does not turn that normal EOF into an onerror/reconnect race
        // while the projection acknowledgement is still being awaited.
        closeStream();
        void finishStream(payload.seq);
      } catch (_parseError) {
        failStream('stream returned a malformed completion');
      }
    });
    es.onerror = () => failStream('stream failed');
  }

  function streamWS() {
    streamText = '';
    streaming = true;
    const proto = location.protocol === 'https:' ? 'wss' : 'ws';
    const ws = new WebSocket(
      `${proto}://${location.host}/v1/agents/${encodeURIComponent(name)}/run/ws`
    );
    activeWS = ws;
    ws.onopen = () => ws.send(JSON.stringify({ prompt: streamPrompt }));
    ws.onmessage = (e) => {
      let m: { type?: unknown; text?: unknown; seq?: unknown };
      try {
        m = JSON.parse(e.data);
      } catch (_parseError) {
        failStream('stream returned a malformed message');
        return;
      }
      if (m.type === 'chunk') streamText += typeof m.text === 'string' ? m.text : '';
      if (m.type === 'error') {
        // Surface the server's error text rather than stopping silently with partial
        // chunks (failStream sets `error` + tears the socket down); still reload the
        // recorded run so its failed status shows.
        failStream(typeof m.text === 'string' && m.text ? m.text : 'agent run failed');
        void load();
        return;
      }
      if (m.type === 'done') {
        closeStream();
        void finishStream(m.seq);
      }
    };
    ws.onerror = () => failStream('stream failed');
  }

  function runStream() {
    if (!agent || streaming) return;
    closeStream();
    if (transport === 'ws') streamWS();
    else streamSSE();
  }

  $effect(() => {
    // Reload whenever the route param changes (covers initial mount and sibling nav).
    void name;
    // Tear down any stream still running for the previous agent — otherwise the
    // old EventSource/WebSocket leaks and its handlers keep mutating this page's
    // state against the newly-selected agent — and clear its now-stale output.
    closeStream();
    streaming = false;
    streamText = '';
    // Per-agent run output and eval report belong to the previous agent — reset
    // them so sibling navigation doesn't show them under the new name.
    lastResult = null;
    lastRunID = '';
    evalReport = null;
    void load();
  });

  onDestroy(() => {
    closeStream();
    if (pollTimer) clearTimeout(pollTimer);
  });
</script>

<main>
  <p><a href={appHref('/agents')}>← agents</a></p>
  {#if !notFound}
    <div class="row reload-row">
      <button onclick={() => void load()} title="Re-fetch this agent, its runs, and evals"
        ><Icon name="reload" size={15} /> Reload</button
      >
    </div>
  {/if}
  {#if agent}
    <h1>{agent.name}</h1>
    <dl>
      <dt>model</dt>
      <dd>{agent.model || '—'}</dd>
      <dt>system</dt>
      <dd>{agent.system || '—'}</dd>
      <dt>runs</dt>
      <dd data-testid="run-count">{agent.runs}</dd>
    </dl>
  {:else if loading}
    <h1>{name}</h1>
    <Skeleton rows={5} />
  {:else if notFound}
    <EmptyState
      icon="agents"
      title="Agent not found"
      hint="No agent matches this name. It may have been removed, or the name may be mistyped."
    >
      {#snippet action()}
        <a href={appHref('/agents')}>← Back to agents</a>
      {/snippet}
    </EmptyState>
  {:else}
    <h1>{name}</h1>
  {/if}
  {#if error}<p class="err">{error}</p>{/if}

  {#if agent}
    <section class="actions">
      <div class="row">
        <input bind:value={prompt} placeholder="prompt" aria-label="prompt" />
        <label class="inline-check">
          <input type="checkbox" bind:checked={asyncRun} />
          durable async
        </label>
        <button
          onclick={run}
          disabled={running || !runControlsValid || !roleAtLeast($user?.role, 'operator')}
          title={!roleAtLeast($user?.role, 'operator') ? 'Requires the operator role' : undefined}
          >{running ? 'Running…' : 'Run'}</button
        >
      </div>
      <details class="run-controls">
        <summary>Execution and tracking</summary>
        <div class="control-grid">
          <label
            >Version
            <input type="number" min="0" bind:value={runVersion} aria-label="agent version" />
            <small>0 = latest</small>
          </label>
          {#if asyncRun}
            <label
              >Timeout (ms)
              <input type="number" min="1" max="600000" bind:value={timeoutMs} />
            </label>
            <label
              >Max attempts
              <input type="number" min="1" max="10" bind:value={maxAttempts} />
            </label>
            <label
              >Idempotency key
              <input bind:value={idempotencyKey} maxlength="256" placeholder="application-run-42" />
            </label>
            <label
              >Business reference
              <input bind:value={businessReference} maxlength="256" placeholder="application-42" />
            </label>
            <label
              >Correlation ID
              <input bind:value={correlationId} maxlength="256" placeholder="trace-42" />
            </label>
          {/if}
        </div>
        {#if asyncRun}
          <p class="muted small">
            Accepted work is durable before this page returns. Any worker replica can claim it;
            idempotent retries return the original run.
          </p>
        {/if}
      </details>
      {#if !runControlsValid}
        <p class="err small">
          Version must be non-negative; async timeout is 1–600000 ms and attempts are 1–10.
        </p>
      {/if}
      {#if lastResult}
        <div class="run-output" data-testid="run-result">
          <div class="run-output-head">
            <Badge tone={statusTone(lastResult.status)}>{lastResult.status}</Badge>
            <code class="muted">{lastResult.run_id}</code>
          </div>
          <pre class:err={lastResult.status === 'failed'}>{outputText(lastResult)}</pre>
        </div>
      {:else if lastRunID}
        <p class="muted">Last run: <code>{lastRunID}</code></p>
      {/if}
    </section>

    <section class="actions">
      <h2>Stream a run</h2>
      <div class="row">
        <input bind:value={streamPrompt} placeholder="prompt" aria-label="stream prompt" />
        <select bind:value={transport} aria-label="transport">
          <option value="sse">SSE</option>
          <option value="ws">WebSocket</option>
        </select>
        <button
          class="primary"
          onclick={runStream}
          disabled={streaming || !roleAtLeast($user?.role, 'operator')}
          title={!roleAtLeast($user?.role, 'operator') ? 'Requires the operator role' : undefined}
        >
          <Icon name="play" size={14} />
          {streaming ? 'Streaming…' : 'Stream'}
        </button>
      </div>
      {#if streamText || streaming}<pre data-testid="stream-output">{streamText}</pre>{/if}
    </section>
  {/if}

  {#if agent && versions.length > 0}
    <section class="actions" data-testid="versions">
      <h2>Versions <span class="muted">(registry · latest v{agent.latest})</span></h2>
      <ul>
        {#each versions as v (v.version)}
          <li>
            <b>v{v.version}</b>
            {#if v.version === agent.latest}<Badge tone="ok" title="Current published version"
                >latest</Badge
              >{/if}
            <code>{v.model || '—'}</code>
            <span class="muted">{v.system ? v.system.slice(0, 60) : ''}</span>
            <span class="muted"
              >· {new Date(v.published_at).toLocaleString()} · {v.etag.slice(0, 8)}</span
            >
          </li>
        {/each}
      </ul>
    </section>
  {/if}

  {#if agent}
    <section class="actions" data-testid="evals">
      <h2>Offline eval <span class="muted">(golden cases · record-nothing)</span></h2>
      <p class="muted">
        JSON array of cases: <code
          >{`{name, prompt, mode: contains|equals|json_subset, expect, expect_json}`}</code
        >
      </p>
      <textarea
        bind:value={evalText}
        rows="6"
        aria-label="eval cases"
        placeholder={'[{"name":"approves","prompt":"score 800","mode":"contains","expect":"approve"}]'}
      ></textarea>
      <div class="row">
        <button
          onclick={saveEvals}
          disabled={evalBusy || !roleAtLeast($user?.role, 'editor')}
          title={!roleAtLeast($user?.role, 'editor') ? 'Requires the editor role' : undefined}
          data-testid="save-evals">Save cases</button
        >
        <button
          class="primary"
          onclick={runEval}
          disabled={evalBusy || !roleAtLeast($user?.role, 'editor')}
          title={!roleAtLeast($user?.role, 'editor') ? 'Requires the editor role' : undefined}
          data-testid="run-evals">{evalBusy ? 'Running…' : 'Run eval'}</button
        >
      </div>
      {#if evalReport}
        <p data-testid="eval-summary">
          <b>{evalReport.passed}/{evalReport.total}</b> passed
          {#if evalReport.total > 0}<Badge
              tone={evalReport.failed === 0 ? 'ok' : 'warn'}
              title="Eval pass-rate"
              >{Math.round((evalReport.passed / evalReport.total) * 100)}%</Badge
            >{/if}
          {#if evalReport.failed > 0}<span class="err">({evalReport.failed} failed)</span>{/if}
        </p>
        <ul>
          {#each evalReport.results as r (r.name)}
            <li>
              <span
                class={r.passed ? 'ok' : 'err'}
                role="img"
                aria-label={r.passed ? 'passed' : 'failed'}>{r.passed ? '✓' : '✗'}</span
              >
              {r.name}
              {#if !r.passed && r.detail}<span class="muted">— {r.detail}</span>{/if}
            </li>
          {/each}
        </ul>
      {/if}
    </section>
  {/if}

  {#if agent}
    <h2>Runs</h2>
    {#if runs.length === 0}<p class="muted">No runs.</p>{/if}
    <ul class="runs" data-testid="runs">
      {#each runs as r (r.run_id)}
        <li class="run-card" class:failed={r.status === 'failed'}>
          <div class="run-card-head">
            <Badge tone={statusTone(r.status)}>{r.status}</Badge>
            <span class="muted"><RelativeTime value={r.at} /></span>
            {#if r.case_id}<span class="muted">· escalated</span>{/if}
            {#if r.status === 'running' || r.status === 'retrying'}
              <button
                class="escalate"
                onclick={() => cancelRun(r)}
                disabled={runAction === r.run_id || !roleAtLeast($user?.role, 'operator')}
                aria-label={`cancel ${r.run_id}`}
              >
                {runAction === r.run_id ? 'Cancelling…' : 'Cancel'}
              </button>
            {:else if canRetry(r)}
              <button
                class="escalate"
                onclick={() => retryRun(r)}
                disabled={runAction === r.run_id || !roleAtLeast($user?.role, 'operator')}
                aria-label={`retry ${r.run_id}`}
              >
                {runAction === r.run_id ? 'Retrying…' : 'Retry'}
              </button>
            {/if}
            {#if canEscalate(r)}
              <button
                class="escalate"
                onclick={() => escalate(r)}
                disabled={escalating === r.run_id || !roleAtLeast($user?.role, 'operator')}
                title={!roleAtLeast($user?.role, 'operator')
                  ? 'Requires the operator role'
                  : undefined}
                aria-label={`escalate ${r.run_id}`}
              >
                {escalating === r.run_id ? 'Escalating…' : 'Escalate'}
              </button>
            {/if}
          </div>
          {#if r.case_id}
            <p class="run-case">
              <a href={appHref(`/cases/${r.case_id}`)}>→ Open case {r.case_id}</a>
            </p>
          {/if}
          <p class="run-meta muted">
            {#if r.attempt}attempt {r.attempt}/{r.max_attempts ?? 3}{/if}
            {#if r.version}
              · version {r.version}{/if}
            {#if r.timeout_ms}
              · timeout {r.timeout_ms} ms{/if}
            {#if r.worker_owner}
              · worker {r.worker_owner.slice(0, 8)}{/if}
            {#if r.business_reference}
              · ref {r.business_reference}{/if}
            {#if r.correlation_id}
              · correlation {r.correlation_id}{/if}
            {#if r.cancel_requested}
              · cancellation requested{/if}
          </p>
          {#if r.prompt}<p class="run-prompt" title={r.prompt}>{truncate(r.prompt, 120)}</p>{/if}
          <pre class:err={r.status === 'failed'}>{outputText(r)}</pre>
        </li>
      {/each}
    </ul>

    <h2>Discussion</h2>
    <p class="muted disc-hint">
      Coordinate prompt, tool, and eval changes with the team — @mention a colleague to notify them.
    </p>
    <CommentThread subjectType="agent" subjectId={name} title="Agent discussion" />
  {/if}
</main>

<style>
  .reload-row {
    justify-content: flex-end;
    margin-top: -2.4rem;
  }
  main {
    max-width: 52rem;
    margin: 2rem auto;
    padding: 0 1rem;
    font-family: var(--font-ui);
  }
  .row {
    display: flex;
    gap: 0.5rem;
    flex-wrap: wrap;
    margin: 0.4rem 0;
    align-items: center;
  }
  input,
  button {
    font: inherit;
    padding: 0.4rem 0.6rem;
  }
  dl {
    display: grid;
    grid-template-columns: 8rem 1fr;
    gap: 0.2rem 1rem;
  }
  dt {
    color: var(--fg-subtle);
  }
  .actions {
    margin: 1rem 0;
    padding: 0.6rem;
    background: #8881;
    border-radius: 0.5rem;
  }
  .inline-check {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    color: var(--fg-muted);
  }
  .inline-check input {
    width: auto;
  }
  .run-controls {
    margin-top: 0.6rem;
  }
  .run-controls summary {
    cursor: pointer;
    color: var(--fg-muted);
  }
  .control-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(12rem, 1fr));
    gap: 0.55rem;
    margin-top: 0.65rem;
  }
  .control-grid label {
    display: grid;
    gap: 0.2rem;
    color: var(--fg-muted);
    font-size: 0.82rem;
  }
  .control-grid small {
    color: var(--fg-subtle);
  }
  .run-meta {
    margin: 0.35rem 0;
    font-size: 0.78rem;
  }
  ul {
    padding-left: 1rem;
  }
  li {
    margin: 0.3rem 0;
  }
  code {
    background: #8881;
    padding: 0 0.3rem;
    border-radius: 0.3rem;
  }
  .err {
    color: var(--danger);
  }
  .ok {
    color: var(--ok, green);
  }
  .muted {
    color: var(--fg-subtle);
  }
  .disc-hint {
    margin: 0.2rem 0 0;
    font-size: 0.85rem;
  }
  textarea {
    width: 100%;
    box-sizing: border-box;
    font: inherit;
    font-family: var(--font-mono, monospace);
    padding: 0.4rem 0.6rem;
  }
  .run-output {
    margin-top: 0.6rem;
  }
  .run-output-head {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    margin-bottom: 0.3rem;
  }
  pre {
    white-space: pre-wrap;
    word-break: break-word;
    background: var(--surface-2);
    border: 1px solid var(--border);
    border-radius: 0.4rem;
    padding: 0.5rem 0.65rem;
    margin: 0.3rem 0 0;
    font-family: var(--font-mono, monospace);
    font-size: 0.85rem;
  }
  pre.err {
    border-color: color-mix(in srgb, var(--danger) 40%, transparent);
    background: color-mix(in srgb, var(--danger) 8%, var(--surface-2));
    color: var(--danger);
  }
  ul.runs {
    list-style: none;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 0.6rem;
  }
  .run-card {
    margin: 0;
    padding: 0.6rem 0.7rem;
    background: var(--surface-2);
    border: 1px solid var(--border);
    border-left: 3px solid var(--border);
    border-radius: 0.5rem;
  }
  .run-card.failed {
    border-left-color: var(--danger);
  }
  .run-card-head {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.5rem;
  }
  .run-prompt {
    margin: 0.4rem 0 0;
    font-size: 0.88rem;
    color: var(--fg);
  }
  .run-case {
    margin: 0.4rem 0 0;
    font-size: 0.85rem;
  }
  .escalate {
    margin-left: auto;
  }
</style>
