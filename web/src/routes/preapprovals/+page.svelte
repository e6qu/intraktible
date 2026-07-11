<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Pre-approvals: durable, time-boxed pre-decisions keyed by entity. A grant lets
     the decide path honor an instant approve/decline for that entity — skipping the
     flow run entirely — until it expires or is revoked. The durable arm of the
     policy/disposition framework. -->
<script lang="ts">
  import { onMount } from 'svelte';
  import Icon from '$lib/Icon.svelte';
  import EmptyState from '$lib/EmptyState.svelte';
  import Skeleton from '$lib/Skeleton.svelte';
  import RelativeTime from '$lib/RelativeTime.svelte';
  import { now } from '$lib/time';
  import { toast } from '$lib/toast';
  import {
    listPreApprovals,
    grantPreApproval,
    revokePreApproval,
    listFlows,
    type PreApproval,
    type Flow,
    type Disposition
  } from '$lib/api';
  import { roleAtLeast } from '$lib/roles';
  import { user } from '$lib/session';

  const key = '';
  const DISPOSITIONS = ['approve', 'decline'];

  let items = $state<PreApproval[]>([]);
  let flows = $state<Flow[]>([]);
  let error = $state('');
  let loading = $state(true);

  // grant form
  let gType = $state('applicant');
  let gId = $state('');
  let gDisp = $state<Disposition>('approve');
  let gFlow = $state('');
  let gDays = $state(30);
  let gTerms = $state('{\n  "limit": 5000\n}');
  let gNote = $state('');
  let granting = $state(false);

  function msg(e: unknown): string {
    return e instanceof Error ? e.message : String(e);
  }

  function expired(p: PreApproval): boolean {
    return new Date(p.valid_until).getTime() <= $now;
  }
  function effectiveStatus(p: PreApproval): 'active' | 'revoked' | 'expired' {
    if (p.status === 'revoked') return 'revoked';
    return expired(p) ? 'expired' : 'active';
  }

  async function load() {
    loading = true;
    error = '';
    try {
      [items, flows] = await Promise.all([listPreApprovals(key), listFlows(key)]);
    } catch (e) {
      error = msg(e);
    } finally {
      loading = false;
    }
  }

  async function grant() {
    if (granting) return; // Enter submits the form directly, bypassing the disabled button
    error = '';
    // A number input cleared to empty binds as null in Svelte 5; reject it rather
    // than posting valid_days:null (or a non-positive window) to the API.
    if (!Number.isInteger(gDays) || gDays < 1) {
      error = 'Valid days must be a whole number of at least 1.';
      return;
    }
    let terms: Record<string, unknown> | undefined;
    const raw = gTerms.trim();
    if (raw) {
      try {
        terms = JSON.parse(raw) as Record<string, unknown>;
      } catch {
        error = 'Terms must be valid JSON.';
        return;
      }
    }
    granting = true;
    try {
      await grantPreApproval(key, {
        entity_type: gType.trim(),
        entity_id: gId.trim(),
        disposition: gDisp,
        terms,
        flow_slug: gFlow || undefined,
        valid_days: gDays,
        note: gNote.trim() || undefined
      });
      toast.success(`Pre-approved ${gType}:${gId}`);
      gId = '';
      gNote = '';
      await load();
    } catch (e) {
      error = msg(e);
    } finally {
      granting = false;
    }
  }

  // Inline revoke: a styled, keyboard-accessible reason field replaces the native
  // window.prompt (which isn't styleable, traps focus, and is blocked by some
  // browsers).
  let revokingId = $state('');
  let revokeReason = $state('');
  let revokeBusy = $state(false);
  function startRevoke(p: PreApproval) {
    revokingId = p.preapproval_id;
    revokeReason = 'no longer valid';
  }
  function cancelRevoke() {
    revokingId = '';
    revokeReason = '';
  }
  async function confirmRevoke(p: PreApproval) {
    if (revokeBusy) return;
    revokeBusy = true;
    try {
      await revokePreApproval(key, p.entity_type, p.entity_id, revokeReason.trim());
      toast.success('Pre-approval revoked');
      cancelRevoke();
      await load();
    } catch (e) {
      toast.error(msg(e));
    } finally {
      revokeBusy = false;
    }
  }

  function termsPreview(t: Record<string, unknown> | undefined): string {
    if (!t || Object.keys(t).length === 0) return '—';
    return JSON.stringify(t);
  }

  onMount(load);
</script>

<main>
  <header class="head">
    <div>
      <h1>Pre-approvals</h1>
      <p class="lede">
        Time-boxed pre-decisions keyed by entity. When a decision request names a pre-approved
        entity, the engine honors the stored disposition instantly and skips the flow.
      </p>
    </div>
  </header>

  {#if error}<p class="err" role="alert">{error}</p>{/if}

  <section class="grant" aria-label="Grant a pre-approval">
    <h2><Icon name="plus" size={16} /> Grant</h2>
    <div class="grid">
      <label>
        <span>Entity type</span>
        <input bind:value={gType} placeholder="applicant" />
      </label>
      <label>
        <span>Entity ID</span>
        <input bind:value={gId} placeholder="acme-co" />
      </label>
      <label>
        <span>Disposition</span>
        <select bind:value={gDisp}>
          {#each DISPOSITIONS as d (d)}<option value={d}>{d}</option>{/each}
        </select>
      </label>
      <label>
        <span>Bind to flow <em>(optional)</em></span>
        <select bind:value={gFlow}>
          <option value="">any flow</option>
          {#each flows as f (f.flow_id)}<option value={f.slug}>{f.slug}</option>{/each}
        </select>
      </label>
      <label>
        <span>Valid for (days)</span>
        <input type="number" min="1" max="3650" bind:value={gDays} />
      </label>
      <label class="wide">
        <span>Note <em>(optional)</em></span>
        <input bind:value={gNote} placeholder="why this entity is pre-approved" />
      </label>
      <label class="wide">
        <span>Terms <em>— returned as the decision output on honor</em></span>
        <textarea bind:value={gTerms} rows="4" spellcheck="false"></textarea>
      </label>
    </div>
    <div class="actions">
      <button
        class="primary"
        onclick={grant}
        disabled={granting || !gType.trim() || !gId.trim() || !roleAtLeast($user?.role, 'editor')}
        title={!roleAtLeast($user?.role, 'editor') ? 'Requires the editor role' : undefined}
      >
        <Icon name="check" size={15} />
        {granting ? 'Granting…' : 'Grant pre-approval'}
      </button>
    </div>
  </section>

  <h2 class="listhead">Existing</h2>
  {#if loading}
    <Skeleton rows={3} />
  {:else if items.length === 0}
    <EmptyState
      icon="check"
      title="No pre-approvals yet"
      hint="Grant one above to let the decide path honor an instant decision for that entity until it expires."
    />
  {:else}
    <div class="table-wrap">
      <table>
        <thead>
          <tr>
            <th>Entity</th>
            <th>Disposition</th>
            <th>Flow</th>
            <th>Terms</th>
            <th>Honored</th>
            <th>Expires</th>
            <th>Status</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          {#each items as p (p.preapproval_id)}
            {@const st = effectiveStatus(p)}
            <tr class:dim={st !== 'active'}>
              <td class="entity">{p.entity_type}<span class="sep">:</span>{p.entity_id}</td>
              <td><span class="disp {p.disposition}">{p.disposition}</span></td>
              <td class="muted">{p.flow_slug || 'any'}</td>
              <td><code class="terms">{termsPreview(p.terms)}</code></td>
              <td class="num">{p.honored_count}</td>
              <td
                >{#if st === 'revoked'}<span class="muted" title="Revoked — no longer honored"
                    >—</span
                  >{:else}<RelativeTime value={p.valid_until} />{/if}</td
              >
              <td><span class="status {st}">{st}</span></td>
              <td class="right">
                {#if st === 'active' && revokingId !== p.preapproval_id}
                  <button
                    class="link danger"
                    onclick={() => startRevoke(p)}
                    disabled={!roleAtLeast($user?.role, 'operator')}
                    title={!roleAtLeast($user?.role, 'operator')
                      ? 'Requires the operator role'
                      : undefined}>Revoke</button
                  >
                {/if}
              </td>
            </tr>
            {#if revokingId === p.preapproval_id}
              <tr class="revokerow">
                <td colspan="8">
                  <form
                    class="revoke"
                    onsubmit={(e) => {
                      e.preventDefault();
                      confirmRevoke(p);
                    }}
                  >
                    <label
                      >Revoke reason
                      <input
                        bind:value={revokeReason}
                        aria-label="revoke reason"
                        placeholder="no longer valid"
                      /></label
                    >
                    <button type="submit" class="danger" disabled={revokeBusy}>
                      {revokeBusy ? 'Revoking…' : 'Confirm revoke'}
                    </button>
                    <button type="button" class="link" onclick={cancelRevoke}>Cancel</button>
                  </form>
                </td>
              </tr>
            {/if}
            {#if p.note || p.revoked_reason}
              <tr class="noterow" class:dim={st !== 'active'}>
                <td colspan="8">
                  {#if p.note}<span class="note">{p.note}</span>{/if}
                  {#if p.revoked_reason}<span class="note revoked">revoked: {p.revoked_reason}</span
                    >{/if}
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
    max-width: 64rem;
    margin: 2rem auto;
    padding: 0 1.25rem;
  }
  .head h1 {
    margin: 0;
  }
  .lede {
    color: var(--fg-muted);
    max-width: 46rem;
    margin: 0.4rem 0 1.4rem;
  }
  h2 {
    display: inline-flex;
    align-items: center;
    gap: 0.4rem;
    font-size: 1.05rem;
  }
  .grant {
    border: 1px solid var(--border);
    border-radius: var(--radius, 12px);
    background: var(--surface-1);
    padding: 1rem 1.2rem 1.2rem;
  }
  .grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(11rem, 1fr));
    gap: 0.8rem;
    margin: 0.6rem 0;
  }
  label {
    display: flex;
    flex-direction: column;
    gap: 0.3rem;
    font-size: 0.85rem;
  }
  label.wide {
    grid-column: 1 / -1;
  }
  label span {
    color: var(--fg-subtle);
  }
  label em {
    font-style: normal;
    color: var(--fg-subtle);
    opacity: 0.8;
  }
  input,
  select,
  textarea {
    font: inherit;
    padding: 0.45rem 0.55rem;
    border: 1px solid var(--border);
    border-radius: 8px;
    background: var(--surface-2);
    color: var(--fg);
  }
  textarea {
    font-family: var(--font-mono);
    font-size: 0.85rem;
    resize: vertical;
  }
  .actions {
    margin-top: 0.6rem;
  }
  button.primary {
    display: inline-flex;
    align-items: center;
    gap: 0.4rem;
  }
  .listhead {
    margin-top: 1.8rem;
  }
  .table-wrap {
    overflow-x: auto;
    border: 1px solid var(--border);
    border-radius: var(--radius, 12px);
  }
  table {
    width: 100%;
    border-collapse: collapse;
    font-size: 0.9rem;
  }
  th,
  td {
    text-align: left;
    padding: 0.55rem 0.7rem;
    border-bottom: 1px solid var(--border);
    white-space: nowrap;
  }
  th {
    color: var(--fg-subtle);
    font-weight: 550;
    font-size: 0.78rem;
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }
  tr.dim td {
    opacity: 0.55;
  }
  tr.noterow td {
    padding-top: 0;
    border-bottom: 1px solid var(--border);
  }
  .entity {
    font-weight: 550;
  }
  .entity .sep {
    color: var(--fg-subtle);
    margin: 0 0.1rem;
  }
  .num {
    font-variant-numeric: tabular-nums;
  }
  .right {
    text-align: right;
  }
  .terms {
    background: var(--surface-2);
    padding: 0.1rem 0.4rem;
    border-radius: 6px;
    font-size: 0.8rem;
    max-width: 16rem;
    display: inline-block;
    overflow: hidden;
    text-overflow: ellipsis;
    vertical-align: bottom;
  }
  .note {
    color: var(--fg-muted);
    font-size: 0.82rem;
  }
  .note.revoked {
    color: var(--danger);
  }
  .disp,
  .status {
    padding: 0.05rem 0.5rem;
    border-radius: 999px;
    font-size: 0.76rem;
    font-weight: 600;
    text-transform: capitalize;
    background: var(--surface-2);
    color: var(--fg-muted);
  }
  .disp.approve,
  .status.active {
    background: color-mix(in srgb, var(--ok) 18%, transparent);
    color: var(--ok);
  }
  .disp.decline,
  .status.revoked {
    background: color-mix(in srgb, var(--danger) 16%, transparent);
    color: var(--danger);
  }
  .status.expired {
    background: color-mix(in srgb, var(--warn) 18%, transparent);
    color: var(--warn);
  }
  button.link {
    background: none;
    border: none;
    padding: 0;
    cursor: pointer;
    font: inherit;
    color: var(--accent);
  }
  button.link.danger {
    /* A text-link variant — keep the transparent background so the .danger fill rule
       (same specificity, later in source) can't win and paint red text on a red fill. */
    background: none;
    color: var(--danger);
  }
  .revoke {
    display: flex;
    align-items: end;
    gap: 0.6rem;
    flex-wrap: wrap;
  }
  .revoke label {
    display: flex;
    flex-direction: column;
    gap: 0.2rem;
    font-size: 0.85rem;
    color: var(--fg-subtle);
  }
  button.danger {
    background: var(--danger);
    color: var(--on-danger);
    border: none;
    border-radius: 0.35rem;
    padding: 0.4rem 0.75rem;
    cursor: pointer;
  }
  button.danger:disabled {
    opacity: 0.6;
    cursor: default;
  }
  .err {
    color: var(--danger);
  }
  .muted {
    color: var(--fg-subtle);
  }
</style>
