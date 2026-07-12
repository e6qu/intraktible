<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { page } from '$app/stores';
  import Icon from '$lib/Icon.svelte';
  import Copyable from '$lib/Copyable.svelte';
  import Breadcrumb from '$lib/Breadcrumb.svelte';
  import CommentThread from '$lib/CommentThread.svelte';
  import Skeleton from '$lib/Skeleton.svelte';
  import {
    getDecision,
    exportDecision,
    resumeDecision,
    decisionCounterfactual,
    adverseActionNotice,
    ApiError,
    type Decision,
    type Counterfactual,
    type RunExportFormat
  } from '$lib/api';
  import { toast } from '$lib/toast';
  import { appHref } from '$lib/paths';
  import { user } from '$lib/session';
  import { roleAtLeast } from '$lib/roles';
  import Badge from '$lib/Badge.svelte';
  import { statusTone, dispositionTone } from '$lib/badge';
  import { nodeAccent } from '$lib/nodevis';
  import Hint from '$lib/Hint.svelte';

  // API calls authenticate via the session cookie (empty key → no X-Api-Key).
  const key = '';
  // Derive from the route param so navigating between sibling decisions reloads.
  const id = $derived($page.params.decisionId ?? '');
  let d = $state<Decision | null>(null);
  let error = $state('');
  // A missing decision (real 404) gets the "stale link" copy; any other failure
  // surfaces the server's actual message rather than masquerading as not-found.
  let notFound = $state(false);

  function msg(e: unknown): string {
    return e instanceof Error ? e.message : String(e);
  }

  let notifying = $state(false);
  // Generate the ECOA / Reg B adverse-action notice for a declined decision and offer
  // it as a download. Fetched through the app fetch (so the demo's mock serves it) and
  // wrapped in a Blob — an <a href> would escape the mock and 404 on the static host.
  async function downloadNotice() {
    notifying = true;
    try {
      const text = await adverseActionNotice(key, id);
      const url = URL.createObjectURL(new Blob([text], { type: 'text/markdown' }));
      const a = document.createElement('a');
      a.href = url;
      a.download = `adverse-action-${id}.md`;
      a.click();
      setTimeout(() => URL.revokeObjectURL(url), 0);
      toast.success('Downloaded the adverse-action notice.');
    } catch (e) {
      toast.error(msg(e));
    } finally {
      notifying = false;
    }
  }
  function pretty(v: unknown): string {
    return v === undefined || v === null ? '—' : JSON.stringify(v, null, 2);
  }
  // A split node records which way it routed as { branch: "yes" | "no" }; surface
  // that as the decisive routing rather than leaving it buried in the raw output.
  function branchOf(output: unknown): string | null {
    if (output && typeof output === 'object' && 'branch' in output) {
      const b = (output as { branch?: unknown }).branch;
      return typeof b === 'string' ? b : null;
    }
    return null;
  }
  let resuming = $state(false);
  // Resume a decision paused at a durable human task: the reviewer's outcome is injected
  // and the flow runs on to completion (the same decision, not a new one).
  async function resume(outcome: string) {
    if (resuming) return;
    resuming = true;
    error = '';
    try {
      await resumeDecision(key, id, { decision: outcome });
      await load();
      toast.success(`Resumed — ${outcome}`);
    } catch (e) {
      toast.error(msg(e));
    } finally {
      resuming = false;
    }
  }
  async function load() {
    error = '';
    notFound = false;
    // Sibling navigation changes id mid-flight; drop a stale response so a slower
    // load for the previous decision can't clobber the one now shown.
    const reqId = id;
    try {
      const got = await getDecision(key, id);
      if (id !== reqId) return;
      d = got;
    } catch (e) {
      if (id !== reqId) return;
      if (e instanceof ApiError && e.status === 404) notFound = true;
      else error = msg(e);
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
      // Revoke on a later tick: revoking synchronously after click() can race the
      // browser's blob fetch and abort the download (notably for larger traces).
      setTimeout(() => URL.revokeObjectURL(url), 0);
      toast.success(`Downloaded ${e.label}`);
    } catch (err) {
      toast.error(msg(err));
    }
  }
  async function copyTrace(format: RunExportFormat) {
    try {
      await navigator.clipboard.writeText(await exportDecision(key, id, format));
      toast.success('Copied to clipboard');
    } catch (e) {
      toast.error(msg(e));
    }
  }
  // Counterfactual: the smallest single-field input change that flips a non-favorable
  // decision — searched on demand (it re-runs the flow many times).
  let cf = $state<Counterfactual | null>(null);
  let cfBusy = $state(false);
  async function loadCf() {
    if (cfBusy) return;
    cfBusy = true;
    // The search re-runs the flow many times; drop a late result (or error) if
    // sibling navigation swapped the decision mid-flight.
    const reqId = id;
    try {
      const got = await decisionCounterfactual(key, reqId);
      if (id === reqId) cf = got;
    } catch (e) {
      if (id === reqId) toast.error(msg(e));
    } finally {
      cfBusy = false;
    }
  }
  function fmtNum(n: number): string {
    return n.toLocaleString(undefined, { maximumFractionDigits: 2 });
  }
  $effect(() => {
    void id; // reload on initial mount and sibling navigation
    cf = null;
    // Reset the rendered decision too — otherwise a failed sibling load keeps
    // showing the previous decision (including its Resume panel) under the new id.
    d = null;
    void load();
  });
</script>

<main>
  <Breadcrumb sectionHref="/decisions" sectionLabel="Decisions" current={id} />
  {#if notFound}
    <p class="err">This decision couldn't be loaded — it may not exist or the link is stale.</p>
  {:else if error}
    <p class="err">{error} <button class="link" onclick={() => load()}>Retry</button></p>
  {/if}
  {#if d}
    <div class="head">
      <h1>{d.slug} <Badge tone={statusTone(d.status)}>{d.status}</Badge></h1>
    </div>

    {#if d.status === 'suspended'}
      {@const cannotResume = resuming || !roleAtLeast($user?.role, 'operator')}
      <div class="resume" data-testid="resume-panel">
        <div class="resume-body">
          <b><Icon name="manual_review" size={16} /> Paused for human review</b>
          <p>
            This decision is suspended at a human task — it is durable (recorded in the event log,
            holding no running process) and resumes when you record an outcome.
          </p>
        </div>
        <div
          class="resume-actions"
          title={!roleAtLeast($user?.role, 'operator')
            ? 'Recording a review outcome requires the operator role'
            : undefined}
        >
          <button disabled={cannotResume} onclick={() => resume('approve')}>Approve</button>
          <button disabled={cannotResume} onclick={() => resume('decline')}>Decline</button>
          <button class="ghost" disabled={cannotResume} onclick={() => resume('refer')}
            >Refer</button
          >
        </div>
      </div>
    {/if}

    {#if d.disposition}
      <div class="verdict {dispositionTone(d.disposition)}" data-testid="verdict">
        <span class="verdict-mark" aria-hidden="true">
          <Icon
            name={d.disposition === 'refer'
              ? 'manual_review'
              : d.disposition === 'decline'
                ? 'alert'
                : 'check'}
            size={22}
          />
        </span>
        <div class="verdict-body">
          <span class="verdict-disp">
            {d.disposition}
            {#if d.preapproval_id}<span class="pa-tag">pre-approval</span>{/if}
          </span>
          {#if d.disposition_reason}<span class="verdict-reason">{d.disposition_reason}</span>{/if}
        </div>
      </div>
    {/if}

    <dl class="fields">
      <dt>flow</dt>
      <dd><a href={appHref(`/engine/${d.flow_id}`)}>{d.slug}</a></dd>
      <dt>version</dt>
      <dd>v{d.version}</dd>
      <dt>environment</dt>
      <dd>{d.environment}</dd>
      <dt>variant</dt>
      <dd>{d.variant ?? '—'}</dd>
      <dt>duration</dt>
      <dd>{d.duration_ms != null ? `${d.duration_ms} ms` : '—'}</dd>
      {#if d.policy_id}
        <dt>policy</dt>
        <dd>
          <a href={appHref('/policies')}
            >{d.policy_id}{d.policy_version ? ` v${d.policy_version}` : ''} →</a
          >
        </dd>
      {/if}
      {#if d.case_id}
        <dt>opened case</dt>
        <dd><a href={appHref(`/cases/${d.case_id}`)}>{d.case_id} →</a></dd>
      {/if}
      <dt>decision id</dt>
      <dd><Copyable value={d.decision_id} label="decision id" /></dd>
    </dl>

    {#if d.error}<p class="err">Error: {d.error}</p>{/if}

    <h2>
      Reason codes
      <Hint label="Reason codes"
        >The machine-readable reasons behind this outcome — emitted by reason nodes, a manual-review
        step, or the policy. They make a decision explainable and auditable after the fact.</Hint
      >
    </h2>
    {#if d.reason_codes && d.reason_codes.length}
      <ul class="reasons" data-testid="reason-codes">
        {#each d.reason_codes as rc (rc.code)}
          <li><span class="rcode">{rc.code}</span> {rc.description}</li>
        {/each}
      </ul>
    {:else}
      <p class="muted">No reason codes were emitted for this decision.</p>
    {/if}
    {#if d.disposition === 'decline'}
      <button class="notice-btn" onclick={downloadNotice} disabled={notifying}>
        <Icon name="shield" />
        {notifying ? 'Generating…' : 'Adverse-action notice'}
      </button>
      <p class="muted small">
        Renders the ECOA / Reg B notice from these reason codes and the workspace creditor settings
        (set on the Fair lending page). Requires the operator role.
      </p>
    {/if}

    {#if d.disposition === 'decline' || d.disposition === 'refer'}
      <h2>
        What would change this?
        <Hint label="Counterfactual"
          >The smallest single-field change to the input that would flip this decision to a more
          favorable outcome — a counterfactual that complements the adverse-action reason codes
          ("you'd be approved if…").</Hint
        >
      </h2>
      {#if !cf}
        <button
          onclick={loadCf}
          disabled={cfBusy || !roleAtLeast($user?.role, 'operator')}
          title={!roleAtLeast($user?.role, 'operator')
            ? 'Searching counterfactuals re-runs the flow (a recorded operation) — requires the operator role'
            : undefined}
          data-testid="cf-run"
        >
          {cfBusy ? 'Searching…' : 'Find what would flip it'}
        </button>
      {:else if cf.flips.length}
        <ul class="flips" data-testid="cf-flips">
          {#each cf.flips as f (f.field)}
            <li>
              <span class="flip-field">{f.field}</span>
              <span class="flip-change">
                {fmtNum(f.from)}
                <span class="arrow" aria-hidden="true"
                  >{f.direction === 'increase' ? '↑' : '↓'}</span
                >
                <b>{fmtNum(f.to)}</b>
              </span>
              <Badge tone={dispositionTone(f.disposition)}>{f.disposition}</Badge>
            </li>
          {/each}
        </ul>
        <p class="muted">
          Each row is the smallest change to one input that flips the outcome, all else held equal ({cf.searched}
          re-runs).
        </p>
      {:else}
        <p class="muted">
          No single-field change flips this decision — the outcome held across {cf.searched} probes.
        </p>
      {/if}
    {/if}

    <h2>
      Node trace
      <Hint label="Node trace"
        >The node-by-node path the engine walked for this decision: each step is a node, its output,
        and (at a split) the branch taken. This is the recorded execution — replayed, not re-run.</Hint
      >
    </h2>
    {#if d.nodes && d.nodes.length}
      <ol class="trace">
        {#each d.nodes as n, i (i)}
          <li style="--accent: {nodeAccent(n.type)}">
            <span class="dot"><Icon name={n.type} size={14} /></span>
            <div class="step">
              <div class="step-head">
                <span class="nid">{n.node_id}</span>
                <span class="ntype">{n.type}</span>
                {#if branchOf(n.output)}<span class="branch" data-testid="trace-branch"
                    >→ {branchOf(n.output)}</span
                  >{/if}
              </div>
              {#if n.output !== undefined}<code class="nout">{pretty(n.output)}</code>{/if}
            </div>
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

    <CommentThread subjectType="decision" subjectId={d.decision_id} title="Discussion" />
  {:else if !error && !notFound}
    <Skeleton rows={6} />
  {/if}
</main>

<style>
  main {
    max-width: 60rem;
    margin: 2rem auto;
    padding: 0 1.25rem;
  }
  .resume {
    display: flex;
    flex-wrap: wrap;
    gap: 1rem;
    align-items: center;
    justify-content: space-between;
    margin: 0.5rem 0 1rem;
    padding: 0.8rem 1rem;
    border: 1px solid color-mix(in srgb, var(--warn) 40%, var(--border));
    border-radius: 0.6rem;
    background: color-mix(in srgb, var(--warn) 10%, var(--surface));
  }
  .resume-body p {
    margin: 0.3rem 0 0;
    color: var(--fg-muted);
    font-size: 0.9rem;
    max-width: 48ch;
  }
  .resume-actions {
    display: flex;
    gap: 0.5rem;
  }
  .resume-actions button {
    font: inherit;
    padding: 0.4rem 0.9rem;
    border: 1px solid var(--border);
    border-radius: 6px;
    cursor: pointer;
  }
  .resume-actions button.ghost {
    background: transparent;
  }
  .head h1 {
    display: inline-flex;
    align-items: center;
    gap: 0.6rem;
  }
  .verdict {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    margin: 0.75rem 0 0.4rem;
    padding: 0.75rem 1rem;
    border-radius: 12px;
    border: 1px solid var(--tone, var(--border));
    background: color-mix(in srgb, var(--tone, var(--fg-muted)) 10%, var(--surface));
  }
  .verdict.ok {
    --tone: var(--ok);
  }
  .verdict.danger {
    --tone: var(--danger);
  }
  .verdict.warn {
    --tone: var(--warn);
  }
  .verdict.neutral {
    --tone: var(--fg-muted);
  }
  .verdict-mark {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 2.4rem;
    height: 2.4rem;
    border-radius: 999px;
    background: color-mix(in srgb, var(--tone) 20%, transparent);
    color: var(--tone);
    flex: none;
  }
  .verdict-body {
    display: flex;
    flex-direction: column;
    gap: 0.1rem;
    min-width: 0;
  }
  .verdict-disp {
    font-size: 1.35rem;
    font-weight: 700;
    text-transform: capitalize;
    color: var(--tone);
    line-height: 1.1;
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
  }
  .verdict-reason {
    font-size: 0.9rem;
    color: var(--fg-muted);
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
  ul.flips {
    list-style: none;
    padding: 0;
    margin: 0.4rem 0 0.5rem;
    display: flex;
    flex-direction: column;
    gap: 0.35rem;
  }
  ul.flips li {
    display: flex;
    align-items: center;
    gap: 0.7rem;
    padding: 0.35rem 0.6rem;
    border: 1px solid var(--border);
    border-radius: 8px;
    background: var(--surface-2);
  }
  .flip-field {
    font-family: var(--font-mono);
    font-weight: 600;
    min-width: 8rem;
  }
  .flip-change {
    font-variant-numeric: tabular-nums;
    flex: 1;
  }
  .flip-change .arrow {
    color: var(--accent-ink);
    font-weight: 700;
    margin: 0 0.15rem;
  }
  ul.reasons li {
    padding: 0.3rem 0;
    display: flex;
    align-items: baseline;
    gap: 0.6rem;
  }
  .pa-tag {
    padding: 0.05rem 0.45rem;
    border-radius: 999px;
    font-size: 0.7rem;
    font-weight: 600;
    text-transform: none;
    letter-spacing: 0.02em;
    background: color-mix(in srgb, var(--accent) 14%, transparent);
    color: var(--accent-ink);
  }
  .rcode {
    font-family: var(--font-mono);
    font-weight: 600;
    color: var(--accent-ink);
    background: var(--surface-2);
    padding: 0.05rem 0.4rem;
    border-radius: 0.3rem;
  }
  ol.trace {
    list-style: none;
    padding: 0;
    margin: 0.4rem 0 1rem;
  }
  /* A vertical rail connects the steps; each step's dot/rail picks up the node
     type's accent (--accent is set inline from nodeAccent()). */
  ol.trace li {
    --accent: var(--fg-subtle);
    position: relative;
    display: flex;
    align-items: flex-start;
    gap: 0.7rem;
    padding: 0 0 0.9rem 0;
  }
  ol.trace li::before {
    content: '';
    position: absolute;
    left: 0.7rem;
    top: 1.5rem;
    bottom: -0.05rem;
    width: 2px;
    background: var(--border);
  }
  ol.trace li:last-child::before {
    display: none;
  }
  .dot {
    position: relative;
    z-index: 1;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 1.5rem;
    height: 1.5rem;
    border-radius: 999px;
    flex: none;
    color: var(--accent);
    background: color-mix(in srgb, var(--accent) 16%, var(--surface));
    border: 1px solid color-mix(in srgb, var(--accent) 45%, transparent);
  }
  .step {
    flex: 1;
    min-width: 0;
    padding-top: 0.1rem;
  }
  .step-head {
    display: flex;
    align-items: baseline;
    flex-wrap: wrap;
    gap: 0.5rem;
  }
  .nid {
    font-weight: 600;
  }
  .ntype {
    font-size: 0.74rem;
    color: var(--fg-subtle);
    font-family: var(--font-mono);
  }
  .branch {
    font-size: 0.74rem;
    font-weight: 600;
    color: var(--accent-ink);
    background: color-mix(in srgb, var(--accent) 16%, transparent);
    padding: 0.05rem 0.4rem;
    border-radius: 999px;
  }
  .nout {
    display: block;
    margin-top: 0.3rem;
    background: var(--surface-2);
    padding: 0.2rem 0.5rem;
    border-radius: 6px;
    font-size: 0.82rem;
    max-width: 100%;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: pre;
    overflow-x: auto;
  }
  .cols {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 1rem;
  }
  /* min-width: 0 lets each panel's <pre> scroll within its box (the global
     pre { overflow: auto }) instead of an unbroken JSON string widening the
     grid column — and the whole page — past the viewport. */
  .cols > div {
    min-width: 0;
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
  .muted.small {
    font-size: 0.82rem;
    margin-top: 0.3rem;
  }
  .notice-btn {
    display: inline-flex;
    align-items: center;
    gap: 0.4rem;
    margin-top: 0.6rem;
    padding: 0.4rem 0.75rem;
    font: inherit;
    border: 1px solid var(--border);
    border-radius: 6px;
    background: var(--surface);
    color: var(--fg);
    cursor: pointer;
  }
  .notice-btn:disabled {
    opacity: 0.5;
    cursor: default;
  }
  .err {
    color: var(--danger);
  }
  button.link {
    background: none;
    border: none;
    color: var(--accent);
    cursor: pointer;
    padding: 0.2rem;
    font: inherit;
  }
  .ok {
    color: var(--ok);
  }
</style>
