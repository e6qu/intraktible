<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { page } from '$app/stores';
  import { displayEntries } from '$lib/kv';
  import CommentThread from '$lib/CommentThread.svelte';
  import EmptyState from '$lib/EmptyState.svelte';
  import Skeleton from '$lib/Skeleton.svelte';
  import RelativeTime from '$lib/RelativeTime.svelte';
  import {
    getEntity,
    listEntityEvents,
    getEntityFeatures,
    getConsents,
    grantConsent,
    withdrawConsent,
    ApiError,
    type Entity,
    type EntityEvent,
    type FeatureValue,
    type ConsentRecord,
    type ConsentEvidence
  } from '$lib/api';
  import { appHref } from '$lib/paths';
  import { roleAtLeast } from '$lib/roles';
  import { user } from '$lib/session';
  import { toast } from '$lib/toast';

  const canManageConsent = $derived(roleAtLeast($user?.role, 'operator'));
  let consentPurpose = $state('');
  // Default to a non-consent basis: for decisioning, the basis is usually contract or
  // legitimate interest, not "consent" (which is rarely freely given). See the hint.
  let consentBasis = $state('contract');
  let consentBusy = $state(false);
  // Evidence fields — how the authorization was obtained, and a tamper-evident
  // reference to the signed artifact (the file is hashed locally; its bytes never
  // leave this machine, so the record respects data residency).
  let consentMethod = $state('');
  let consentReference = $state('');
  let consentNotice = $state('');
  let consentHash = $state('');
  let consentHashAlgo = $state('');
  let hashing = $state(false);

  async function hashEvidenceFile(e: Event) {
    const input = e.target as HTMLInputElement;
    const file = input.files?.[0];
    if (!file) {
      consentHash = '';
      consentHashAlgo = '';
      return;
    }
    hashing = true;
    try {
      const digest = await crypto.subtle.digest('SHA-256', await file.arrayBuffer());
      consentHash = [...new Uint8Array(digest)]
        .map((b) => b.toString(16).padStart(2, '0'))
        .join('');
      consentHashAlgo = 'sha-256';
      if (!consentReference.trim()) consentReference = file.name;
    } finally {
      hashing = false;
    }
  }

  const key = '';
  // Derive from the route params so navigating between sibling entities reloads.
  const type = $derived($page.params.type ?? '');
  const id = $derived($page.params.id ?? '');

  let entity = $state<Entity | null>(null);
  let events = $state<EntityEvent[]>([]);
  let featureValues = $state<FeatureValue[]>([]);
  // The entity IS the data subject (keyed "type/id"); its consents are the
  // permissible-purpose record a compliance reviewer checks.
  let consents = $state<ConsentRecord[]>([]);
  let error = $state('');
  // A missing entity (real 404) is an expected state — a stale/mistyped id — and gets
  // the "not found" EmptyState, not the raw error string with a Retry that can't succeed.
  let notFound = $state(false);
  let loading = $state(true);

  function msg(e: unknown): string {
    return e instanceof Error ? e.message : String(e);
  }
  async function load() {
    error = '';
    notFound = false;
    loading = true;
    // Clear the prior entity so a failed reload can't leave the previous entity's
    // attributes/events on screen under an error.
    entity = null;
    events = [];
    featureValues = [];
    consents = [];
    // Drop a stale response when sibling navigation changes type/id mid-flight.
    const reqType = type;
    const reqId = id;
    try {
      const [ent, evs, feats, cons] = await Promise.all([
        getEntity(key, type, id),
        listEntityEvents(key, type, id),
        // Computed features are best-effort (none defined for this type is fine).
        getEntityFeatures(key, type, id).catch(() => []),
        // Consents are best-effort (a subject with none is normal).
        getConsents(key, `${type}/${id}`).catch(() => [])
      ]);
      if (type !== reqType || id !== reqId) return;
      [entity, events, featureValues, consents] = [ent, evs, feats, cons];
    } catch (e) {
      if (type === reqType && id === reqId) {
        if (e instanceof ApiError && e.status === 404) notFound = true;
        else error = msg(e);
      }
    } finally {
      if (type === reqType && id === reqId) loading = false;
    }
  }
  // The subject key matches the decide integration + seeder convention: "<type>/<id>".
  const subject = $derived(`${type}/${id}`);
  async function reloadConsents() {
    consents = await getConsents(key, subject).catch(() => []);
  }
  async function recordConsent() {
    if (!consentPurpose.trim()) {
      toast.error('A purpose is required.');
      return;
    }
    const evidence: ConsentEvidence = {};
    if (consentMethod) evidence.method = consentMethod;
    if (consentReference.trim()) evidence.reference = consentReference.trim();
    if (consentHash) {
      evidence.content_hash = consentHash;
      evidence.hash_algo = consentHashAlgo;
    }
    if (consentNotice.trim()) evidence.notice_version = consentNotice.trim();
    consentBusy = true;
    try {
      await grantConsent(key, {
        subject,
        purpose: consentPurpose.trim(),
        basis: consentBasis,
        evidence: Object.keys(evidence).length ? evidence : undefined
      });
      toast.success('Basis recorded.');
      consentPurpose = '';
      consentMethod = '';
      consentReference = '';
      consentNotice = '';
      consentHash = '';
      consentHashAlgo = '';
      await reloadConsents();
    } catch (e) {
      toast.error(msg(e));
    } finally {
      consentBusy = false;
    }
  }
  async function revokeConsent(purpose: string) {
    consentBusy = true;
    try {
      await withdrawConsent(key, { subject, purpose });
      toast.success('Consent withdrawn.');
      await reloadConsents();
    } catch (e) {
      toast.error(msg(e));
    } finally {
      consentBusy = false;
    }
  }

  $effect(() => {
    void type;
    void id; // reload on initial mount and sibling navigation
    void load();
  });
</script>

<main>
  <p><a href={appHref('/data')}>← context data</a></p>
  <h1>{type} / {id}</h1>
  {#if loading}
    <Skeleton rows={5} />
  {:else if notFound}
    <EmptyState
      icon="database"
      title="Entity not found"
      hint="No entity matches this type and id. It may not exist yet, or the id may be mistyped."
    />
  {:else if error}
    <p class="err">{error} <button class="link" onclick={() => load()}>Retry</button></p>
  {:else if entity}
    <section>
      <h2>Attributes</h2>
      {#if displayEntries(entity.attributes).length === 0}
        <EmptyState
          icon="database"
          title="No attributes"
          hint="This entity has no stored attributes yet — they accrue as decisions and events reference it."
        />
      {:else}
        <dl class="kv">
          {#each displayEntries(entity.attributes) as [k, v] (k)}
            <dt>{k}</dt>
            <dd>{v}</dd>
          {/each}
        </dl>
      {/if}
    </section>

    {#if featureValues.length > 0}
      <section>
        <h2>Computed features</h2>
        <div class="features">
          {#each featureValues as f (f.name)}
            <span class="feat"
              >{f.name} <b>{f.value}</b>
              <small
                class="lineage"
                title="feature definition version · events that fed this value"
              >
                v{f.version} · {f.event_count} ev{f.cached ? ' · cached' : ''}
              </small></span
            >
          {/each}
        </div>
      </section>
    {/if}

    {#if consents.length > 0 || canManageConsent}
      <section>
        <h2>Lawful basis <span class="muted">(purpose limitation)</span></h2>
        <p class="muted small">
          The lawful basis your organization has recorded for processing this subject. A decision
          that pulls data for a purpose requires an active basis here — the permissible-purpose
          record for GDPR / GLBA. For credit decisioning the basis is usually
          <em>contract</em> or <em>legitimate interest</em>, not <em>consent</em> (which is rarely freely
          given given the power imbalance).
        </p>
        {#if consents.length > 0}
          <div class="table-wrap">
            <table>
              <thead>
                <tr
                  ><th>Purpose</th><th>Status</th><th>Basis</th><th>Evidence</th><th>Recorded</th
                  ><th></th></tr
                >
              </thead>
              <tbody>
                {#each consents as c (c.purpose)}
                  <tr>
                    <td>{c.purpose}</td>
                    <td>
                      {#if c.granted}
                        <span class="badge ok">active</span>
                      {:else}
                        <span class="badge">withdrawn</span>
                      {/if}
                    </td>
                    <td class="muted">{c.basis || '—'}</td>
                    <td class="muted small">
                      {#if c.evidence}
                        {#if c.evidence.method}<span class="badge">{c.evidence.method}</span>{/if}
                        {#if c.evidence.reference}<div class="ev-ref" title={c.evidence.reference}>
                            {c.evidence.reference}
                          </div>{/if}
                        {#if c.evidence.content_hash}
                          <div
                            class="ev-hash"
                            title={`${c.evidence.hash_algo}: ${c.evidence.content_hash}`}
                          >
                            ⛓ {c.evidence.content_hash.slice(0, 10)}…
                          </div>
                        {/if}
                        {#if c.evidence.notice_version}<div class="ev-ref">
                            notice {c.evidence.notice_version}
                          </div>{/if}
                      {:else}
                        —
                      {/if}
                    </td>
                    <td class="muted"
                      ><RelativeTime value={c.granted_at ?? c.withdrawn_at ?? ''} /></td
                    >
                    <td>
                      {#if c.granted && canManageConsent}
                        <button
                          class="link"
                          disabled={consentBusy}
                          onclick={() => revokeConsent(c.purpose)}>Withdraw</button
                        >
                      {/if}
                    </td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        {:else}
          <p class="muted">No lawful basis recorded for this subject yet.</p>
        {/if}
        {#if canManageConsent}
          <div class="consent-form">
            <input bind:value={consentPurpose} placeholder="purpose (e.g. credit_underwriting)" />
            <select bind:value={consentBasis} aria-label="lawful basis">
              <option value="contract">contract</option>
              <option value="legitimate_interest">legitimate_interest</option>
              <option value="legal_obligation">legal_obligation</option>
              <option value="consent">consent</option>
            </select>
            <select bind:value={consentMethod} aria-label="how obtained">
              <option value="">how obtained…</option>
              <option value="e_signature">e_signature</option>
              <option value="wet_signature">wet_signature</option>
              <option value="scanned_document">scanned_document</option>
              <option value="click_through">click_through</option>
              <option value="verbal">verbal</option>
            </select>
            <input bind:value={consentReference} placeholder="document reference" />
            <input bind:value={consentNotice} placeholder="notice version" />
            <label
              class="file-label"
              title="Hashed locally — the file's bytes never leave this device"
            >
              <input type="file" onchange={hashEvidenceFile} />
              {#if consentHash}⛓ {consentHash.slice(0, 10)}…{:else if hashing}hashing…{:else}attach
                + hash{/if}
            </label>
            <button class="btn" disabled={consentBusy || hashing} onclick={recordConsent}
              >Record basis</button
            >
          </div>
          <p class="muted small">
            Attaching a file hashes it in your browser (SHA-256) and stores only the fingerprint +
            name — the document itself stays in your own system of record.
          </p>
        {/if}
      </section>
    {/if}

    <section>
      <h2>Event timeline <span class="muted">({events.length})</span></h2>
      {#if events.length === 0}
        <EmptyState
          icon="diagram"
          title="No events"
          hint="No events have been recorded for this entity. Events appear as the workspace records activity against it."
        />
      {:else}
        <ul class="timeline">
          {#each events as ev (ev.seq)}
            <li>
              <span class="ev-name">{ev.event_name}</span>
              <span class="muted"><RelativeTime value={ev.occurred_at} /></span>
              {#if ev.data}<pre>{JSON.stringify(ev.data)}</pre>{/if}
            </li>
          {/each}
        </ul>
      {/if}
    </section>

    <section>
      <h2>Discussion</h2>
      <p class="muted disc-hint">
        Discuss this entity's data with the team — @mention a colleague to notify them.
      </p>
      <!-- Subject key matches the seeder's convention: "<type>/<id>", one escaped
           path segment on the wire (encodeURIComponent in the API client). -->
      <CommentThread subjectType="entity" subjectId={`${type}/${id}`} title="Entity discussion" />
    </section>
  {/if}
</main>

<style>
  main {
    max-width: 52rem;
    margin: 2rem auto;
    padding: 0 1.25rem;
  }
  section {
    margin: 1.25rem 0;
  }
  h2 {
    font-size: 1.05rem;
  }
  .kv {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 0.3rem 1rem;
    margin: 0.4rem 0;
  }
  .kv dt {
    color: var(--fg-subtle);
    font-size: 0.85rem;
  }
  .kv dd {
    margin: 0;
  }
  .features {
    display: flex;
    flex-wrap: wrap;
    gap: 0.6rem;
  }
  .feat {
    padding: 0.3rem 0.6rem;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--surface);
    font-size: 0.9rem;
  }
  .lineage {
    color: var(--fg-subtle);
    font-size: 0.75rem;
    margin-left: 0.3rem;
  }
  .small {
    font-size: 0.82rem;
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
    padding: 0.35rem 0.6rem;
    border-bottom: 1px solid var(--border);
  }
  .badge {
    display: inline-block;
    padding: 0.05rem 0.45rem;
    border-radius: 999px;
    font-size: 0.78rem;
    background: var(--surface-2);
    color: var(--fg-muted);
  }
  .badge.ok {
    background: var(--ok-bg, #dcfce7);
    color: var(--ok, #166534);
  }
  .consent-form {
    display: flex;
    flex-wrap: wrap;
    gap: 0.5rem;
    margin-top: 0.6rem;
  }
  .consent-form input,
  .consent-form select {
    font: inherit;
    padding: 0.35rem 0.5rem;
    border: 1px solid var(--border);
    border-radius: 6px;
    background: var(--surface);
    color: var(--fg);
  }
  .file-label {
    display: inline-flex;
    align-items: center;
    padding: 0.35rem 0.5rem;
    border: 1px dashed var(--border);
    border-radius: 6px;
    background: var(--surface);
    color: var(--fg-subtle);
    font-size: 0.85rem;
    cursor: pointer;
  }
  .file-label input[type='file'] {
    display: none;
  }
  .ev-ref {
    font-size: 0.78rem;
    max-width: 12rem;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .ev-hash {
    font-size: 0.78rem;
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  }
  .btn {
    font: inherit;
    padding: 0.35rem 0.75rem;
    border: 1px solid var(--border);
    border-radius: 6px;
    background: var(--surface);
    color: var(--fg);
    cursor: pointer;
  }
  .btn:disabled {
    opacity: 0.5;
    cursor: default;
  }
  .timeline {
    list-style: none;
    padding: 0;
  }
  .timeline li {
    padding: 0.5rem 0;
    border-bottom: 1px solid var(--border);
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.6rem;
  }
  .ev-name {
    font-weight: 600;
  }
  .timeline pre {
    margin: 0;
    font-size: 0.8rem;
    background: var(--surface-2);
    padding: 0.3rem 0.5rem;
    border-radius: var(--radius);
  }
  .muted {
    color: var(--fg-subtle);
  }
  .disc-hint {
    margin: 0.2rem 0 0;
    font-size: 0.85rem;
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
</style>
