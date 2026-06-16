<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { onMount } from 'svelte';
  import { page } from '$app/stores';
  import Icon from '$lib/Icon.svelte';
  import { getDecision, exportDecision, type Decision } from '$lib/api';
  import { toast } from '$lib/toast';

  // API calls authenticate via the session cookie (empty key → no X-Api-Key).
  const key = '';
  const id = $page.params.decisionId ?? '';
  let d = $state<Decision | null>(null);
  let error = $state('');

  function msg(e: unknown): string {
    return e instanceof Error ? e.message : String(e);
  }
  function pretty(v: unknown): string {
    return v === undefined || v === null ? '—' : JSON.stringify(v, null, 2);
  }
  async function load() {
    error = '';
    try {
      d = await getDecision(key, id);
    } catch (e) {
      error = msg(e);
    }
  }
  async function downloadTrace() {
    try {
      const text = await exportDecision(key, id);
      const url = URL.createObjectURL(new Blob([text], { type: 'text/plain' }));
      const a = document.createElement('a');
      a.href = url;
      a.download = `${id}-trace.mmd`;
      a.click();
      URL.revokeObjectURL(url);
      toast.success('Downloaded sequence diagram');
    } catch (e) {
      error = msg(e);
    }
  }
  async function copyTrace() {
    try {
      await navigator.clipboard.writeText(await exportDecision(key, id));
      toast.success('Copied sequence diagram');
    } catch (e) {
      error = msg(e);
    }
  }
  onMount(load);
</script>

<main>
  <p><a href="/decisions">← all decisions</a></p>
  {#if error}<p class="err">{error}</p>{/if}
  {#if d}
    <div class="head">
      <h1>{d.slug} <span class="badge {d.status}">{d.status}</span></h1>
    </div>

    <dl class="fields">
      <dt>flow</dt>
      <dd><a href={`/engine/${d.flow_id}`}>{d.slug}</a></dd>
      <dt>version</dt>
      <dd>v{d.version}</dd>
      <dt>environment</dt>
      <dd>{d.environment}</dd>
      <dt>variant</dt>
      <dd>{d.variant ?? '—'}</dd>
      <dt>duration</dt>
      <dd>{d.duration_ms ?? 0} ms</dd>
      <dt>decision id</dt>
      <dd class="mono">{d.decision_id}</dd>
    </dl>

    {#if d.error}<p class="err">Error: {d.error}</p>{/if}

    {#if d.reason_codes && d.reason_codes.length}
      <h2>Reason codes</h2>
      <ul class="reasons" data-testid="reason-codes">
        {#each d.reason_codes as rc (rc.code)}
          <li><span class="rcode">{rc.code}</span> {rc.description}</li>
        {/each}
      </ul>
    {/if}

    <h2>Node trace</h2>
    {#if d.nodes && d.nodes.length}
      <ol class="trace">
        {#each d.nodes as n, i (i)}
          <li>
            <span class="nodeicon"><Icon name={n.type} size={15} /></span>
            <span class="nid">{n.node_id}</span>
            <span class="ntype">{n.type}</span>
            {#if n.output !== undefined}<code class="nout">{pretty(n.output)}</code>{/if}
          </li>
        {/each}
      </ol>
    {:else}
      <p class="muted">No node trace recorded.</p>
    {/if}

    <div class="cols">
      <div>
        <h2>Input</h2>
        <pre>{pretty(d.data)}</pre>
      </div>
      <div>
        <h2>Output</h2>
        <pre>{pretty(d.output)}</pre>
      </div>
    </div>

    <div class="row">
      <span class="exportlabel"><Icon name="diagram" size={15} /> Export trace</span>
      <button onclick={downloadTrace}><Icon name="download" size={14} /> Sequence</button>
      <button class="icon" aria-label="Copy sequence diagram" onclick={copyTrace}>
        <Icon name="copy" size={14} />
      </button>
    </div>
  {:else if !error}
    <p class="muted">Loading…</p>
  {/if}
</main>

<style>
  main {
    max-width: 60rem;
    margin: 2rem auto;
    padding: 0 1.25rem;
  }
  .head h1 {
    display: inline-flex;
    align-items: center;
    gap: 0.6rem;
  }
  .badge {
    padding: 0.1rem 0.5rem;
    border-radius: 999px;
    font-size: 0.8rem;
    font-weight: 550;
    background: var(--surface-2);
    color: var(--fg-muted);
  }
  .badge.completed {
    background: color-mix(in srgb, var(--ok) 18%, transparent);
    color: var(--ok);
  }
  .badge.failed {
    background: color-mix(in srgb, var(--danger) 16%, transparent);
    color: var(--danger);
  }
  dl.fields {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 0.3rem 1rem;
    margin: 0.8rem 0;
  }
  dl.fields dt {
    color: var(--fg-subtle);
    font-size: 0.9rem;
  }
  .mono {
    font-family: var(--font-mono);
    font-size: 0.85rem;
  }
  ul.reasons {
    list-style: none;
    padding: 0;
    margin: 0.4rem 0 0.8rem;
  }
  ul.reasons li {
    padding: 0.3rem 0;
    display: flex;
    align-items: baseline;
    gap: 0.6rem;
  }
  .rcode {
    font-family: var(--font-mono);
    font-weight: 600;
    color: var(--accent);
    background: var(--surface-2);
    padding: 0.05rem 0.4rem;
    border-radius: 0.3rem;
  }
  ol.trace {
    list-style: none;
    counter-reset: step;
    padding: 0;
  }
  ol.trace li {
    counter-increment: step;
    display: flex;
    align-items: center;
    gap: 0.5rem;
    padding: 0.35rem 0.4rem;
    border-bottom: 1px solid var(--border);
  }
  ol.trace li::before {
    content: counter(step);
    color: var(--fg-subtle);
    font-size: 0.78rem;
    min-width: 1.2rem;
  }
  .nodeicon {
    display: inline-flex;
    color: var(--accent);
  }
  .nid {
    font-weight: 550;
  }
  .ntype {
    font-size: 0.75rem;
    color: var(--fg-subtle);
    font-family: var(--font-mono);
  }
  .nout {
    margin-left: auto;
    background: var(--surface-2);
    padding: 0.1rem 0.4rem;
    border-radius: 6px;
    font-size: 0.82rem;
    max-width: 22rem;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .cols {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 1rem;
  }
  @media (max-width: 640px) {
    .cols {
      grid-template-columns: 1fr;
    }
  }
  pre {
    min-height: 2rem;
    max-height: 18rem;
  }
  .row {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    margin-top: 1rem;
  }
  .exportlabel {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    font-size: 0.8rem;
    color: var(--fg-muted);
    font-weight: 550;
  }
  button.icon {
    padding: 0.4rem 0.5rem;
  }
  .muted {
    color: var(--fg-subtle);
  }
  .err {
    color: var(--danger);
  }
  .ok {
    color: var(--ok);
  }
</style>
