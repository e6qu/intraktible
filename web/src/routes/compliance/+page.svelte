<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- The compliance officer's home: the tenant's regulatory work in one place — the
     adverse-action 30-day queue, the human-review (Article 22) audit trail, the lawful-
     basis / consent overview, and (for admins) data-governance holds & retention.
     Reads degrade per-section: a viewer sees the queue/audit/consent; the admin-only
     governance card appears only for admins (the erasure reads are admin-gated). -->
<script lang="ts">
  import { onMount } from 'svelte';
  import Skeleton from '$lib/Skeleton.svelte';
  import EmptyState from '$lib/EmptyState.svelte';
  import RelativeTime from '$lib/RelativeTime.svelte';
  import { appHref } from '$lib/paths';
  import { user } from '$lib/session';
  import { roleAtLeast } from '$lib/roles';
  import {
    listAdverseActions,
    listReconsiderations,
    listConsentRecords,
    listLegalHolds,
    getRetentionPolicy,
    listErasedSubjects,
    listSharingRecords,
    exportComplianceRegister,
    type AdverseActionItem,
    type Reconsideration,
    type ConsentRecord,
    type LegalHold,
    type RetentionPolicy,
    type SharingRecord
  } from '$lib/api';
  import { toast } from '$lib/toast';

  const key = '';

  // Download a compliance register as a CSV file. Fetched through the app fetch (so the
  // demo's wasm backend serves it) and wrapped in a Blob — an <a href> would escape the
  // in-browser backend and 404 on the static host.
  async function exportRegister(register: 'adverse-actions' | 'reconsiderations' | 'consent') {
    try {
      const text = await exportComplianceRegister(key, register, 'csv');
      const url = URL.createObjectURL(new Blob([text], { type: 'text/csv' }));
      const a = document.createElement('a');
      a.href = url;
      a.download = `${register}-register.csv`;
      a.click();
      setTimeout(() => URL.revokeObjectURL(url), 0);
      toast.success('Register downloaded.');
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e));
    }
  }
  const isAdmin = $derived(roleAtLeast($user?.role, 'admin'));

  let loading = $state(true);
  let pending = $state<AdverseActionItem[]>([]);
  let reviews = $state<Reconsideration[]>([]);
  let consents = $state<ConsentRecord[]>([]);
  let holds = $state<LegalHold[]>([]);
  let retention = $state<RetentionPolicy | null>(null);
  let erasedCount = $state(0);
  let sharing = $state<SharingRecord[]>([]);

  onMount(async () => {
    // Every source is best-effort: an admin-only read (holds/retention/erased) 403s
    // for a viewer and simply leaves its section empty rather than failing the page.
    const [p, r, c, h, ret, er, sh] = await Promise.all([
      listAdverseActions(key, 'pending').catch(() => []),
      listReconsiderations(key).catch(() => []),
      listConsentRecords(key).catch(() => []),
      listLegalHolds(key).catch(() => []),
      getRetentionPolicy(key).catch(() => null),
      listErasedSubjects(key).catch(() => []),
      listSharingRecords(key).catch(() => [])
    ]);
    [pending, reviews, consents, holds, retention, erasedCount, sharing] = [
      p,
      r,
      c,
      h,
      ret,
      er.length,
      sh
    ];
    loading = false;
  });

  const optedOut = $derived(sharing.filter((s) => s.opted_out).length);

  const DAY = 86_400_000;
  // "Now" for the age/expiry math. In the browser this is real wall-clock; the demo's
  // seeded timestamps are anchored near today, so the 30-day clock reads sensibly.
  const nowMs = Date.now();

  const overdue = $derived(pending.filter((a) => a.age_days > 30).length);
  const overturned = $derived(reviews.filter((r) => r.outcome === 'overturned').length);
  const active = $derived(consents.filter((c) => c.granted));
  const withdrawn = $derived(consents.filter((c) => !c.granted).length);
  const expiringSoon = $derived(
    active.filter((c) => c.expires_at && new Date(c.expires_at).getTime() - nowMs < 30 * DAY).length
  );

  // Consent counts by lawful basis, most common first.
  const byBasis = $derived.by(() => {
    const m = new Map<string, number>();
    for (const c of active)
      m.set(c.basis || 'unspecified', (m.get(c.basis || 'unspecified') ?? 0) + 1);
    return [...m.entries()].sort((a, b) => b[1] - a[1]);
  });

  function subjectHref(subject?: string): string | null {
    return subject && subject.includes('/') ? appHref(`/data/${subject}`) : null;
  }
</script>

<main class="wrap">
  <header class="head">
    <div>
      <h1>Compliance</h1>
      <p class="sub">
        The tenant's regulatory work in one view — adverse-action notices, human reviews of
        automated declines, lawful basis for processing, and data-governance holds.
      </p>
    </div>
  </header>

  {#if loading}
    <Skeleton rows={6} />
  {:else}
    <section class="kpis">
      <div class="kpi">
        <span class="kpi-label">Notices pending</span>
        <span class="kpi-num">{pending.length}</span>
        <span class="kpi-foot {overdue ? 'danger' : 'muted'}">
          {overdue} past the 30-day clock
        </span>
      </div>
      <div class="kpi">
        <span class="kpi-label">Human reviews</span>
        <span class="kpi-num">{reviews.length}</span>
        <span class="kpi-foot {overturned ? 'warn' : 'muted'}">{overturned} overturned</span>
      </div>
      <div class="kpi">
        <span class="kpi-label">Active lawful basis</span>
        <span class="kpi-num">{active.length}</span>
        <span class="kpi-foot {expiringSoon ? 'warn' : 'muted'}">
          {withdrawn} withdrawn · {expiringSoon} expiring
        </span>
      </div>
      {#if isAdmin}
        <div class="kpi">
          <span class="kpi-label">Legal holds</span>
          <span class="kpi-num">{holds.length}</span>
          <span class="kpi-foot muted">{erasedCount} subjects erased</span>
        </div>
      {/if}
    </section>

    <div class="grid">
      <section class="card">
        <h2>
          Adverse-action queue <span class="muted">({pending.length})</span>
          <button class="export" onclick={() => exportRegister('adverse-actions')}
            >Export register ↓</button
          >
        </h2>
        <p class="hint">
          Declined decisions awaiting their ECOA / Regulation B notice. The age is the 30-day clock;
          a row past 30 days is flagged.
        </p>
        {#if pending.length === 0}
          <EmptyState
            icon="check"
            title="Nothing pending"
            hint="Every declined decision has had its adverse-action notice issued."
          />
        {:else}
          <div class="table-wrap">
            <table>
              <thead>
                <tr><th>Decision</th><th>Subject</th><th>Age</th></tr>
              </thead>
              <tbody>
                {#each pending.slice(0, 8) as a (a.decision_id)}
                  <tr>
                    <td
                      ><a href={appHref(`/decisions/${a.decision_id}`)}
                        >{a.decision_id.slice(0, 12)}…</a
                      ></td
                    >
                    <td class="muted">
                      {#if subjectHref(a.subject)}
                        <a href={subjectHref(a.subject)}>{a.subject}</a>
                      {:else}
                        {a.subject || '—'}
                      {/if}
                    </td>
                    <td class:overdue={a.age_days > 30}>{a.age_days}d</td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
          {#if pending.length > 8}<p class="more">+{pending.length - 8} more</p>{/if}
        {/if}
      </section>

      <section class="card">
        <h2>
          Human-review audit <span class="muted">({reviews.length})</span>
          <button class="export" onclick={() => exportRegister('reconsiderations')}
            >Export register ↓</button
          >
        </h2>
        <p class="hint">
          Solely-automated declines a person reviewed (GDPR Article 22 / ECOA reconsideration).
        </p>
        {#if reviews.length === 0}
          <EmptyState
            icon="shield"
            title="No human reviews yet"
            hint="An automated decline that a reviewer upholds or overturns is recorded here."
          />
        {:else}
          <ul class="reviews">
            {#each reviews.slice(0, 6) as r (r.decision_id)}
              <li>
                {#if r.outcome === 'overturned'}
                  <span class="badge overturned">overturned</span>
                {:else}
                  <span class="badge">upheld</span>
                {/if}
                <a href={appHref(`/decisions/${r.decision_id}`)}>{r.decision_id.slice(0, 10)}…</a>
                <span class="muted small">{r.basis.replace('_', ' ')} · {r.reviewed_by}</span>
                <span class="muted small ago"><RelativeTime value={r.reviewed_at} /></span>
              </li>
            {/each}
          </ul>
        {/if}
      </section>

      <section class="card">
        <h2>
          Lawful basis <span class="muted">({consents.length})</span>
          <button class="export" onclick={() => exportRegister('consent')}>Export register ↓</button
          >
        </h2>
        <p class="hint">
          The basis your organization has recorded for processing each subject. Consent is one
          basis; for decisioning it is usually contract or legitimate interest.
        </p>
        {#if consents.length === 0}
          <EmptyState
            icon="database"
            title="No records"
            hint="Lawful-basis records appear as subjects are onboarded and decisions reference them."
          />
        {:else}
          <ul class="legend">
            {#each byBasis as [basis, n] (basis)}
              <li>
                <span class="key" data-basis={basis}></span>{basis.replace('_', ' ')} <b>{n}</b>
              </li>
            {/each}
          </ul>
          <p class="line">
            Active <b>{active.length}</b> · withdrawn <b>{withdrawn}</b> · expiring within 30 days
            <b class={expiringSoon ? 'warn' : ''}>{expiringSoon}</b>
          </p>
        {/if}
        <p class="line">
          Information-sharing opt-outs <b>{optedOut}</b> — subjects who declined sharing of their nonpublic
          personal information with unaffiliated third parties.
        </p>
      </section>

      {#if isAdmin}
        <section class="card">
          <h2>Data governance <span class="tag">Admin</span></h2>
          <p class="hint">Retention window, active legal holds, and crypto-shredded subjects.</p>
          <dl class="stats">
            <div>
              <dt>Retention</dt>
              <dd>
                {retention && retention.retention_days > 0
                  ? `${retention.retention_days}d`
                  : 'indefinite'}
              </dd>
            </div>
            <div>
              <dt>Legal holds</dt>
              <dd class={holds.length ? 'warn' : ''}>{holds.length}</dd>
            </div>
            <div>
              <dt>Erased subjects</dt>
              <dd>{erasedCount}</dd>
            </div>
          </dl>
          {#if holds.length > 0}
            <ul class="holds">
              {#each holds.slice(0, 5) as h (h.subject)}
                <li>
                  {#if subjectHref(h.subject)}
                    <a href={subjectHref(h.subject)}>{h.subject}</a>
                  {:else}
                    <span>{h.subject}</span>
                  {/if}
                  <span class="muted small">{h.reason || 'held'}</span>
                </li>
              {/each}
            </ul>
          {/if}
        </section>
      {/if}
    </div>
  {/if}
</main>

<style>
  .wrap {
    max-width: 72rem;
    margin: 2rem auto;
    padding: 0 1.25rem 3rem;
  }
  .head h1 {
    margin: 0;
  }
  .sub {
    color: var(--fg-muted);
    margin: 0.25rem 0 0;
    max-width: 46rem;
  }
  .kpis {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(13rem, 1fr));
    gap: var(--gap);
    margin: 1.4rem 0;
  }
  .kpi {
    display: flex;
    flex-direction: column;
    gap: 0.35rem;
    padding: var(--pad-card);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--surface);
    box-shadow: var(--shadow);
  }
  .kpi-label {
    font-size: 0.8rem;
    color: var(--fg-muted);
    text-transform: uppercase;
    letter-spacing: 0.05em;
  }
  .kpi-num {
    font-size: 2.6rem;
    font-weight: 600;
    line-height: 1;
    letter-spacing: -0.02em;
    font-variant-numeric: tabular-nums;
  }
  .kpi-foot {
    font-size: 0.82rem;
    color: var(--fg-muted);
  }
  .kpi-foot.warn {
    color: var(--warn);
  }
  .kpi-foot.danger {
    color: var(--danger);
  }
  .kpi-foot.muted {
    color: var(--fg-subtle);
  }
  .grid {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: var(--gap);
  }
  @media (max-width: 760px) {
    .grid {
      grid-template-columns: 1fr;
    }
  }
  .card {
    padding: var(--pad-card);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--surface);
    box-shadow: var(--shadow);
  }
  .card h2 {
    margin: 0 0 0.4rem;
    font-size: 0.95rem;
    font-weight: 600;
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }
  .tag {
    font-size: 0.68rem;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    padding: 0.05rem 0.4rem;
    border-radius: 4px;
    background: var(--surface-2);
    color: var(--fg-muted);
  }
  .export {
    margin-left: auto;
    font: inherit;
    font-size: 0.78rem;
    padding: 0.2rem 0.55rem;
    border: 1px solid var(--border);
    border-radius: 6px;
    background: var(--surface);
    color: var(--fg-muted);
    cursor: pointer;
  }
  .export:hover {
    border-color: var(--accent);
    color: var(--fg);
  }
  .hint {
    color: var(--fg-subtle);
    font-size: 0.82rem;
    margin: 0 0 0.8rem;
  }
  .muted {
    color: var(--fg-subtle);
  }
  .small {
    font-size: 0.8rem;
  }
  .table-wrap {
    overflow-x: auto;
  }
  table {
    border-collapse: collapse;
    width: 100%;
    font-size: 0.9rem;
  }
  th,
  td {
    text-align: left;
    padding: 0.35rem 0.5rem;
    border-bottom: 1px solid var(--border);
  }
  td.overdue {
    color: var(--danger);
    font-weight: 600;
  }
  .more {
    margin: 0.6rem 0 0;
    font-size: 0.82rem;
    color: var(--fg-subtle);
  }
  .reviews,
  .holds {
    list-style: none;
    padding: 0;
    margin: 0;
  }
  .reviews li,
  .holds li {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    padding: 0.35rem 0;
    border-bottom: 1px solid var(--border);
    flex-wrap: wrap;
  }
  .reviews .ago {
    margin-left: auto;
  }
  .badge {
    display: inline-block;
    padding: 0.05rem 0.5rem;
    border-radius: 999px;
    font-size: 0.75rem;
    background: var(--ok-bg, #dcfce7);
    color: var(--ok, #166534);
  }
  .badge.overturned {
    background: var(--warn-bg, #fef3c7);
    color: var(--warn, #92400e);
  }
  .legend {
    display: flex;
    flex-wrap: wrap;
    gap: 0.5rem 1rem;
    list-style: none;
    padding: 0;
    margin: 0 0 0.6rem;
    font-size: 0.88rem;
    color: var(--fg-muted);
  }
  .legend .key {
    display: inline-block;
    width: 0.7rem;
    height: 0.7rem;
    border-radius: 3px;
    margin-right: 0.35rem;
    vertical-align: -1px;
    background: var(--accent);
  }
  .legend .key[data-basis='consent'] {
    background: var(--warn);
  }
  .legend .key[data-basis='contract'] {
    background: var(--ok);
  }
  .legend .key[data-basis='legitimate_interest'] {
    background: var(--accent);
  }
  .line {
    margin: 0.6rem 0 0;
    color: var(--fg-muted);
    font-size: 0.88rem;
  }
  .line b.warn {
    color: var(--warn);
  }
  .stats {
    margin: 0;
    display: grid;
    grid-template-columns: repeat(3, 1fr);
    gap: 0.9rem 1rem;
  }
  .stats div {
    display: flex;
    flex-direction: column;
    gap: 0.15rem;
  }
  .stats dt {
    font-size: 0.8rem;
    color: var(--fg-muted);
  }
  .stats dd {
    margin: 0;
    font-size: 1.5rem;
    font-weight: 600;
    font-variant-numeric: tabular-nums;
  }
  .stats dd.warn {
    color: var(--warn);
  }
  a {
    color: var(--accent-ink);
  }
</style>
