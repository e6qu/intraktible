<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { onMount } from 'svelte';
  import { page } from '$app/stores';
  import Icon from '$lib/Icon.svelte';
  import Copyable from '$lib/Copyable.svelte';
  import { getDecision, exportDecision, type Decision, type RunExportFormat } from '$lib/api';
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
  const RUN_EXPORTS: { format: RunExportFormat; label: string; ext: string; mime: string }[] = [
    { format: 'mermaid', label: 'Sequence', ext: 'mmd', mime: 'text/plain' },
    { format: 'dot', label: 'DOT', ext: 'dot', mime: 'text/vnd.graphviz' },
    { format: 'json', label: 'JSON', ext: 'json', mime: 'application/json' }
  ];
  async function downloadTrace(e: (typeof RUN_EXPORTS)[number]) {
    try {
      const text = await exportDecision(key, id, e.format);
      const url = URL.createObjectURL(new Blob([text], { type: e.mime }));
      const a = document.createElement('a');
      a.href = url;
      a.download = e.format === 'json' ? `${id}.json` : `${id}-trace.${e.ext}`;
      a.click();
      URL.revokeObjectURL(url);
      toast.success(`Downloaded ${e.label}`);
    } catch (err) {
      error = msg(err);
    }
  }
  async function copyTrace(format: RunExportFormat) {
    try {
      await navigator.clipboard.writeText(await exportDecision(key, id, format));
      toast.success('Copied to clipboard');
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
      {#if d.disposition}
        <dt>disposition</dt>
        <dd>
          <span class="disp {d.disposition}">{d.disposition}</span>
          {#if d.disposition_reason}<span class="muted"> · {d.disposition_reason}</span>{/if}
        </dd>
      {/if}
      <dt>duration</dt>
      <dd>{d.duration_ms ?? 0} ms</dd>
      <dt>decision id</dt>
      <dd><Copyable value={d.decision_id} label="decision id" /></dd>
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
      {#each RUN_EXPORTS as e (e.format)}
        <span class="grp">
          <button onclick={() => downloadTrace(e)} title={`Download ${e.label}`}>
            <Icon name="download" size={14} />
            {e.label}
          </button>
          <button
            class="icon"
            aria-label={`Copy ${e.label}`}
            title={`Copy ${e.label}`}
            onclick={() => copyTrace(e.format)}
          >
            <Icon name="copy" size={14} />
          </button>
        </span>
      {/each}
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
  .disp {
    padding: 0.05rem 0.5rem;
    border-radius: 999px;
    font-size: 0.78rem;
    font-weight: 600;
    text-transform: capitalize;
    background: var(--surface-2);
    color: var(--fg-muted);
  }
  .disp.approve {
    background: color-mix(in srgb, var(--ok) 18%, transparent);
    color: var(--ok);
  }
  .disp.decline {
    background: color-mix(in srgb, var(--danger) 16%, transparent);
    color: var(--danger);
  }
  .disp.refer {
    background: color-mix(in srgb, var(--warn) 18%, transparent);
    color: var(--warn);
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
    flex-wrap: wrap;
  }
  .grp {
    display: inline-flex;
  }
  .grp button:first-child {
    border-top-right-radius: 0;
    border-bottom-right-radius: 0;
  }
  .grp button.icon {
    border-top-left-radius: 0;
    border-bottom-left-radius: 0;
    border-left: none;
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
