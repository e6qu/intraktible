<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { onDestroy } from 'svelte';
  import { page } from '$app/stores';
  import Icon from '$lib/Icon.svelte';
  import {
    getAgent,
    runAgent,
    listAgentRuns,
    escalateRun,
    type Agent,
    type AgentRun
  } from '$lib/api';

  // API calls authenticate via the session cookie (empty key -> no X-Api-Key header).
  const key = '';
  let agent = $state<Agent | null>(null);
  let runs = $state<AgentRun[]>([]);
  let error = $state('');

  let prompt = $state('');
  let lastRunID = $state('');

  // Derive from the route param so navigating between sibling agents reloads
  // rather than showing the first agent's data.
  const name = $derived($page.params.name ?? '');

  async function load() {
    error = '';
    // Drop a stale response when sibling navigation changes name mid-flight.
    const reqName = name;
    try {
      const [a, r] = await Promise.all([getAgent(key, name), listAgentRuns(key, name)]);
      if (name !== reqName) return;
      [agent, runs] = [a, r];
    } catch (e) {
      if (name === reqName) error = e instanceof Error ? e.message : String(e);
    }
  }

  let running = $state(false);
  async function run() {
    error = '';
    running = true;
    try {
      const res = await runAgent(key, name, prompt);
      lastRunID = res.run_id;
      await load();
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      running = false;
    }
  }

  let escalating = $state('');
  async function escalate(runID: string) {
    if (escalating) return; // guard against double-click opening duplicate cases
    error = '';
    escalating = runID;
    try {
      await escalateRun(key, name, runID, {
        company_name: 'Review from ' + name,
        case_type: 'agent_review',
        sla_days: 3
      });
      await load();
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
  // Parse one chunk frame's text, returning null on malformed/ill-shaped data.
  function chunkText(raw: string): string | null {
    try {
      const parsed: unknown = JSON.parse(raw);
      const t = (parsed as { text?: unknown })?.text;
      return typeof t === 'string' ? t : null;
    } catch {
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
    es.addEventListener('done', () => {
      streaming = false;
      closeStream();
      void load();
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
      let m: { type?: unknown; text?: unknown };
      try {
        m = JSON.parse(e.data);
      } catch {
        failStream('stream returned a malformed message');
        return;
      }
      if (m.type === 'chunk') streamText += typeof m.text === 'string' ? m.text : '';
      if (m.type === 'done' || m.type === 'error') {
        streaming = false;
        closeStream();
        void load();
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
    void load();
  });

  onDestroy(closeStream);
</script>

<main>
  <p><a href="/agents">← agents</a></p>
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
  {:else}
    <h1>{name}</h1>
  {/if}
  {#if error}<p class="err">{error}</p>{/if}

  <div class="row">
    <button onclick={load}>Reload</button>
  </div>

  <section class="actions">
    <div class="row">
      <input bind:value={prompt} placeholder="prompt" aria-label="prompt" />
      <button onclick={run} disabled={!agent || running}>{running ? 'Running…' : 'Run'}</button>
    </div>
    {#if lastRunID}<p class="muted">Last run: <code>{lastRunID}</code></p>{/if}
  </section>

  <section class="actions">
    <h2>Stream a run</h2>
    <div class="row">
      <input bind:value={streamPrompt} placeholder="prompt" aria-label="stream prompt" />
      <select bind:value={transport} aria-label="transport">
        <option value="sse">SSE</option>
        <option value="ws">WebSocket</option>
      </select>
      <button class="primary" onclick={runStream} disabled={!agent || streaming}>
        <Icon name="play" size={14} />
        {streaming ? 'Streaming…' : 'Stream'}
      </button>
    </div>
    {#if streamText || streaming}<pre data-testid="stream-output">{streamText}</pre>{/if}
  </section>

  {#if agent}
    <h2>Runs</h2>
    {#if runs.length === 0}<p class="muted">No runs.</p>{/if}
    <ul data-testid="runs">
      {#each runs as r (r.run_id)}
        <li>
          <code>{r.status}</code>
          — {r.text || (r.error ? 'error: ' + r.error : '(structured)')}
          <button
            onclick={() => escalate(r.run_id)}
            disabled={!!escalating}
            aria-label={`escalate ${r.run_id}`}
          >
            {escalating === r.run_id ? 'Escalating…' : 'Escalate'}
          </button>
        </li>
      {/each}
    </ul>
  {/if}
</main>

<style>
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
  .muted {
    color: var(--fg-subtle);
  }
</style>
