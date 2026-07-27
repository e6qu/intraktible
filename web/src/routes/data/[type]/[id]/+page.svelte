<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { page } from '$app/stores';
  import { displayEntries } from '$lib/kv';
  import CommentThread from '$lib/CommentThread.svelte';
  import EmptyState from '$lib/EmptyState.svelte';
  import Skeleton from '$lib/Skeleton.svelte';
  import RelativeTime from '$lib/RelativeTime.svelte';
  import { now } from '$lib/time';
  import {
    getEntity,
    listEntityEvents,
    recordEntityEvent,
    getEntityFeatures,
    getConsents,
    grantConsent,
    withdrawConsent,
    getSharingStatus,
    whenPermitted,
    optOutSharing,
    optInSharing,
    getRetentionStatus,
    getErasureStatus,
    holdErasureSubject,
    releaseErasureSubject,
    eraseSubject,
    type RetentionStatus,
    type ErasureStatus,
    type SharingStatus,
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
  const canAdmin = $derived(roleAtLeast($user?.role, 'admin'));
  // GLBA sharing opt-out state for this subject (the opt-out mirror of consent).
  let sharingOptedOut = $state<boolean | null>(null);
  let sharingBusy = $state(false);
  // Statutory record-retention status — whether the subject may be erased yet.
  let retention = $state<RetentionStatus | null>(null);
  let erasureStatus = $state<ErasureStatus | null>(null);
  let governanceBusy = $state('');
  let holdReason = $state('');
  let eraseAcknowledged = $state(false);
  let consentPurpose = $state('');
  let consentExpiry = $state('');
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
  // The subject key matches the decide integration + seeder convention: "<type>/<id>".
  const subject = $derived(`${type}/${id}`);

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
  function objectJSON(text: string, label: string): Record<string, unknown> {
    const parsed = JSON.parse(text || '{}') as unknown;
    if (parsed === null || Array.isArray(parsed) || typeof parsed !== 'object') {
      throw new Error(`${label} must be a JSON object.`);
    }
    return parsed as Record<string, unknown>;
  }
  function consentActive(record: ConsentRecord): boolean {
    return record.active && (!record.expires_at || new Date(record.expires_at).getTime() > $now);
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
    sharingOptedOut = null;
    retention = null;
    erasureStatus = null;
    eraseAcknowledged = false;
    // Drop a stale response when sibling navigation changes type/id mid-flight.
    const reqType = type;
    const reqId = id;
    try {
      const [ent, evs, feats, cons, share, retained, erased] = await Promise.all([
        getEntity(key, type, id),
        listEntityEvents(key, type, id),
        // "None defined" and "none recorded" both come back as an empty list with a
        // 200, so only a 403 leaves these sections blank. Anything else reaches the
        // handler below instead of rendering as an entity that simply has no features.
        whenPermitted(getEntityFeatures(key, type, id), []),
        whenPermitted(getConsents(key, subject), []),
        whenPermitted<SharingStatus | null>(getSharingStatus(key, subject), null),
        whenPermitted<RetentionStatus | null>(getRetentionStatus(key, subject), null),
        whenPermitted<ErasureStatus | null>(getErasureStatus(key, subject), null)
      ]);
      if (type !== reqType || id !== reqId) return;
      [entity, events, featureValues, consents, retention, erasureStatus] = [
        ent,
        evs,
        feats,
        cons,
        retained,
        erased
      ];
      sharingOptedOut = share?.opted_out ?? null;
    } catch (e) {
      if (type === reqType && id === reqId) {
        if (e instanceof ApiError && e.status === 404) notFound = true;
        else error = msg(e);
      }
    } finally {
      if (type === reqType && id === reqId) loading = false;
    }
  }
  async function reloadConsents() {
    try {
      consents = await whenPermitted(getConsents(key, subject), []);
    } catch (e) {
      error = msg(e);
    }
  }
  async function reloadSubjectControls() {
    // A failed sharing read must not render as opted-in: this drives whether the
    // subject's data may be shared with third parties, so guessing the permissive
    // value is the one outcome that cannot be allowed to happen quietly.
    try {
      const [share, retained, erased] = await Promise.all([
        whenPermitted<SharingStatus | null>(getSharingStatus(key, subject), null),
        whenPermitted<RetentionStatus | null>(getRetentionStatus(key, subject), null),
        whenPermitted<ErasureStatus | null>(getErasureStatus(key, subject), null)
      ]);
      sharingOptedOut = share?.opted_out ?? null;
      retention = retained;
      erasureStatus = erased;
    } catch (e) {
      error = msg(e);
    }
  }
  async function toggleSharing() {
    if (sharingOptedOut === null) {
      toast.error('Sharing status is not available for this role.');
      return;
    }
    sharingBusy = true;
    try {
      if (sharingOptedOut) {
        await optInSharing(key, subject);
        toast.success('Sharing opt-out rescinded.');
      } else {
        await optOutSharing(key, { subject });
        toast.success('Recorded: opted out of information sharing.');
      }
      await reloadSubjectControls();
    } catch (e) {
      toast.error(msg(e));
    } finally {
      sharingBusy = false;
    }
  }

  async function placeHold() {
    if (!holdReason.trim()) {
      toast.error('Record why this legal hold is required.');
      return;
    }
    governanceBusy = 'hold';
    try {
      await holdErasureSubject(key, subject, holdReason.trim());
      holdReason = '';
      await reloadSubjectControls();
      toast.success(`Legal hold placed on ${subject}.`);
    } catch (e) {
      toast.error(msg(e));
    } finally {
      governanceBusy = '';
    }
  }

  async function releaseHold() {
    if (
      !confirm(
        `Release the legal hold on ${subject}? An erasure request or retention sweep may then permanently destroy its encryption key.`
      )
    )
      return;
    governanceBusy = 'release';
    try {
      await releaseErasureSubject(key, subject);
      await reloadSubjectControls();
      toast.success(`Legal hold released for ${subject}.`);
    } catch (e) {
      toast.error(msg(e));
    } finally {
      governanceBusy = '';
    }
  }

  async function eraseSubjectData() {
    if (!eraseAcknowledged) {
      toast.error('Acknowledge that crypto-shredding cannot be undone.');
      return;
    }
    if (
      !confirm(
        `Permanently erase protected data for ${subject}? Its encryption key will be destroyed and cannot be recovered.`
      )
    )
      return;
    governanceBusy = 'erase';
    try {
      await eraseSubject(key, subject);
      // Reload the event timeline too: protected values already held in browser
      // memory must be replaced immediately by the backend's "[erased]" view.
      await load();
      toast.success(`Protected data for ${subject} was crypto-shredded.`);
    } catch (e) {
      toast.error(msg(e));
    } finally {
      governanceBusy = '';
    }
  }
  let eventName = $state('');
  let eventData = $state('{}');
  let eventOccurredAt = $state('');
  let eventBusy = $state(false);
  async function recordEvent() {
    if (eventBusy) return;
    eventBusy = true;
    try {
      const occurredAt = eventOccurredAt ? new Date(eventOccurredAt) : null;
      if (occurredAt && Number.isNaN(occurredAt.getTime())) {
        throw new Error('Occurred at must be a valid date and time.');
      }
      await recordEntityEvent(key, {
        entity_type: type,
        entity_id: id,
        event_name: eventName.trim(),
        data: objectJSON(eventData, 'Event data'),
        occurred_at: occurredAt?.toISOString()
      });
      eventName = '';
      eventData = '{}';
      eventOccurredAt = '';
      await load();
      toast.success(`Event recorded for ${subject}.`);
    } catch (e) {
      toast.error(msg(e));
    } finally {
      eventBusy = false;
    }
  }
  async function recordConsent() {
    if (!consentPurpose.trim()) {
      toast.error('A purpose is required.');
      return;
    }
    const expiry = consentExpiry ? new Date(consentExpiry) : null;
    if (expiry && (Number.isNaN(expiry.getTime()) || expiry.getTime() <= Date.now())) {
      toast.error('Expiry must be a valid future date and time.');
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
        expires_at: expiry?.toISOString(),
        evidence: Object.keys(evidence).length ? evidence : undefined
      });
      toast.success('Basis recorded.');
      consentPurpose = '';
      consentExpiry = '';
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
          record for the EU General Data Protection Regulation and the US Gramm-Leach-Bliley Act.
          For credit decisioning the basis is usually
          <em>contract</em> or <em>legitimate interest</em>, not <em>consent</em> (which is rarely freely
          given because of the power imbalance).
        </p>
        <div class="sharing">
          <div>
            <span class="sharing-label">Information sharing</span>
            {#if sharingOptedOut === true}
              <span class="badge">opted out of sharing</span>
            {:else if sharingOptedOut === false}
              <span class="badge ok">sharing permitted</span>
            {:else}
              <span class="badge">status unavailable for this role</span>
            {/if}
            <span class="muted small"
              >— whether this subject's nonpublic personal information may be shared with
              unaffiliated third parties (under the US Gramm-Leach-Bliley Act). A decision that
              would share it is blocked once the subject has opted out.</span
            >
          </div>
          {#if canManageConsent}
            <button
              class="btn"
              disabled={sharingBusy || sharingOptedOut === null}
              onclick={toggleSharing}
            >
              {sharingOptedOut ? 'Rescind opt-out' : 'Record opt-out'}
            </button>
          {/if}
        </div>
        {#if retention}
          <p class="muted small retention">
            <span class="sharing-label">Record retention</span>
            {#if retention.retained}
              <span class="badge">retain until {retention.retain_until?.slice(0, 10)}</span> — a record
              about this subject must be kept (US Equal Credit Opportunity Act, Regulation B — 25 months),
              so an erasure request is refused until it lapses (Article 17(3)(b) of the EU General Data
              Protection Regulation).
            {:else}
              <span class="badge ok">no mandatory retention</span> — no record blocks an erasure request
              for this subject.
            {/if}
          </p>
        {/if}
        {#if consents.length > 0}
          <div class="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Purpose</th><th>Status</th><th>Basis</th><th>Evidence</th><th>Expires</th><th
                    >Recorded</th
                  ><th></th>
                </tr>
              </thead>
              <tbody>
                {#each consents as c (c.purpose)}
                  <tr>
                    <td>{c.purpose}</td>
                    <td>
                      {#if consentActive(c)}
                        <span class="badge ok">active</span>
                      {:else if c.granted}
                        <span class="badge">expired</span>
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
                    <td class="muted">
                      {#if c.expires_at}<RelativeTime value={c.expires_at} />{:else}—{/if}
                    </td>
                    <td class="muted"
                      ><RelativeTime value={c.granted_at ?? c.withdrawn_at ?? ''} /></td
                    >
                    <td>
                      {#if consentActive(c) && canManageConsent}
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
            <input
              bind:value={consentExpiry}
              type="datetime-local"
              aria-label="basis expiry"
              title="Optional — after this instant the basis is inactive even if it was never withdrawn"
            />
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

    {#if canAdmin}
      <section class="governance">
        <h2>Data governance <span class="badge">Admin</span></h2>
        <p class="muted small">
          Manage the encryption key for <b>{subject}</b>. Erasure destroys protected field values
          while retaining the append-only audit record.
        </p>
        {#if erasureStatus}
          <div class="governance-status">
            <span class="sharing-label">Subject key</span>
            {#if erasureStatus.erased}
              <span class="badge danger">crypto-shredded</span>
            {:else if erasureStatus.held}
              <span class="badge">legal hold active</span>
            {:else}
              <span class="badge ok">eligible when obligations allow</span>
            {/if}
          </div>

          {#if erasureStatus.held}
            <button
              class="btn danger-outline"
              disabled={governanceBusy !== ''}
              onclick={releaseHold}
            >
              {governanceBusy === 'release' ? 'Releasing…' : 'Release legal hold'}
            </button>
          {:else if !erasureStatus.erased}
            <div class="governance-form">
              <input
                aria-label="legal hold reason"
                bind:value={holdReason}
                placeholder="legal hold reason"
                disabled={governanceBusy !== ''}
              />
              <button class="btn" disabled={governanceBusy !== ''} onclick={placeHold}>
                {governanceBusy === 'hold' ? 'Placing…' : 'Place legal hold'}
              </button>
            </div>
            <p class="muted small">
              A hold can only protect a subject whose encryption key already exists. The backend
              will reject an unknown or already-erased subject rather than record a meaningless
              hold.
            </p>
          {/if}

          {#if !erasureStatus.erased}
            <div class="erase-panel">
              <p>
                <b>Right-to-erasure request</b><br />
                <span class="muted small">
                  This permanently destroys the subject's encryption key. It cannot be undone.
                </span>
              </p>
              {#if erasureStatus.held}
                <p class="blocked">Blocked by the active legal hold.</p>
              {:else if retention?.retained}
                <p class="blocked">
                  Blocked by statutory retention until {retention.retain_until?.slice(0, 10)}.
                </p>
              {/if}
              <label class="acknowledge">
                <input
                  type="checkbox"
                  bind:checked={eraseAcknowledged}
                  disabled={governanceBusy !== '' || erasureStatus.held || retention?.retained}
                />
                I understand that the protected data cannot be recovered.
              </label>
              <button
                class="btn danger-fill"
                disabled={governanceBusy !== '' ||
                  erasureStatus.held ||
                  retention?.retained ||
                  !eraseAcknowledged}
                onclick={eraseSubjectData}
              >
                {governanceBusy === 'erase' ? 'Erasing…' : 'Permanently erase protected data'}
              </button>
            </div>
          {:else}
            <p class="muted small">
              This subject is tombstoned: future attempts to record protected fields are refused,
              and encrypted values in the event timeline are irrecoverable.
            </p>
          {/if}
        {:else}
          <p class="muted">Erasure status is not available for this role.</p>
        {/if}
      </section>
    {/if}

    <section>
      <h2>Event timeline <span class="muted">({events.length})</span></h2>
      {#if roleAtLeast($user?.role, 'editor')}
        <form
          class="event-form"
          onsubmit={(e) => {
            e.preventDefault();
            void recordEvent();
          }}
        >
          <label
            >Event name
            <input
              bind:value={eventName}
              placeholder="transaction"
              aria-label="event name"
              required
            /></label
          >
          <label
            >Occurred at (optional)
            <input
              bind:value={eventOccurredAt}
              type="datetime-local"
              aria-label="event occurred at"
            /></label
          >
          <label class="event-data"
            >Data (JSON)
            <textarea bind:value={eventData} rows="3" aria-label="event data"></textarea>
          </label>
          <button class="btn" type="submit" disabled={eventBusy}>
            {eventBusy ? 'Recording…' : 'Record event'}
          </button>
        </form>
      {/if}
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
  .badge.danger {
    background: color-mix(in srgb, var(--danger) 14%, transparent);
    color: var(--danger);
  }
  .sharing {
    display: flex;
    align-items: center;
    justify-content: space-between;
    flex-wrap: wrap;
    gap: 0.5rem;
    margin: 0.4rem 0 0.9rem;
    padding: 0.5rem 0.7rem;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--surface-2);
  }
  .sharing-label {
    font-weight: 600;
    margin-right: 0.4rem;
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
  .governance {
    padding: 0.9rem;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--surface);
  }
  .governance-status {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    margin: 0.7rem 0;
  }
  .governance-form {
    display: flex;
    flex-wrap: wrap;
    gap: 0.5rem;
    margin-top: 0.7rem;
  }
  .governance-form input {
    flex: 1 1 16rem;
    min-width: 0;
    padding: 0.35rem 0.5rem;
    border: 1px solid var(--border);
    border-radius: 6px;
    background: var(--surface);
    color: var(--fg);
    font: inherit;
  }
  .btn.danger-outline {
    border-color: var(--danger);
    color: var(--danger);
  }
  .erase-panel {
    margin-top: 1rem;
    padding: 0.8rem;
    border: 1px solid color-mix(in srgb, var(--danger) 45%, var(--border));
    border-radius: var(--radius);
    background: color-mix(in srgb, var(--danger) 6%, var(--surface));
  }
  .erase-panel p {
    margin: 0 0 0.6rem;
  }
  .blocked {
    color: var(--danger);
    font-size: 0.85rem;
  }
  .acknowledge {
    display: flex;
    align-items: start;
    gap: 0.45rem;
    margin: 0.7rem 0;
    color: var(--fg-muted);
    font-size: 0.85rem;
  }
  .btn.danger-fill {
    border-color: var(--danger);
    background: var(--danger);
    color: var(--on-danger, white);
  }
  .event-form {
    display: grid;
    grid-template-columns: minmax(10rem, 1fr) minmax(12rem, 1fr) auto;
    align-items: end;
    gap: 0.6rem;
    margin: 0.6rem 0 1rem;
    padding: 0.75rem;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--surface);
  }
  .event-form label {
    display: grid;
    gap: 0.2rem;
    color: var(--fg-subtle);
    font-size: 0.78rem;
  }
  .event-form input,
  .event-form textarea {
    min-width: 0;
    padding: 0.4rem 0.5rem;
    border: 1px solid var(--border);
    border-radius: 6px;
    background: var(--surface);
    color: var(--fg);
    font: inherit;
  }
  .event-data {
    grid-column: 1 / -1;
  }
  @media (max-width: 44rem) {
    .event-form {
      grid-template-columns: 1fr;
    }
    .event-data {
      grid-column: auto;
    }
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
