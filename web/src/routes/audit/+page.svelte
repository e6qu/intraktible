<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { goto, afterNavigate } from '$app/navigation';
  import { page } from '$app/stores';
  import { get } from 'svelte/store';
  import Icon from '$lib/Icon.svelte';
  import RelativeTime from '$lib/RelativeTime.svelte';
  import Skeleton from '$lib/Skeleton.svelte';
  import EmptyState from '$lib/EmptyState.svelte';
  import { onMount } from 'svelte';
  import { toast } from '$lib/toast';
  import { appHref } from '$lib/paths';
  import {
    listAuditPage,
    auditExportUrl,
    getPrivacy,
    setPrivacy,
    listApiKeys,
    createApiKey,
    rotateApiKey,
    revokeApiKey,
    type AuditEntry,
    type AuditFilter,
    type ManagedApiKey,
    type Role,
    type Scope
  } from '$lib/api';

  // API calls authenticate via the session cookie (empty key → no X-Api-Key).
  const key = '';
  let list = $state<AuditEntry[]>([]);
  let error = $state('');
  let forbidden = $state(false);
  let loading = $state(true);

  // Filter inputs (bound to the form). The URL query string is the source of
  // truth, so a filtered audit view is deep-linkable, bookmarkable, and the
  // browser back/forward buttons step through filter changes.
  let fStream = $state('');
  let fActor = $state('');
  let fType = $state('');
  let fResource = $state('');
  let fSince = $state('');
  let fUntil = $state('');
  let hideNodeSteps = $state(false);
  let offset = $state(0);
  let total = $state(0);
  const PAGE = 100;
  const NODE_EVAL = 'decision.run.node_evaluated';

  function msg(e: unknown): string {
    return e instanceof Error ? e.message : String(e);
  }
  // toRFC3339 turns a datetime-local value into an RFC3339 instant (the API wants one).
  function toRFC3339(local: string): string | undefined {
    if (!local) return undefined;
    const d = new Date(local);
    return isNaN(d.getTime()) ? undefined : d.toISOString();
  }
  function filter(): AuditFilter {
    return {
      stream: fStream.trim() || undefined,
      actor: fActor.trim() || undefined,
      type: fType.trim() || undefined,
      resource: fResource.trim() || undefined,
      since: toRFC3339(fSince),
      until: toRFC3339(fUntil),
      exclude_type: hideNodeSteps ? NODE_EVAL : undefined,
      limit: PAGE,
      offset
    };
  }
  // The CSV export must match the rows on screen, which are driven by the applied
  // (URL) filter — not the inputs the user may have edited but not yet applied.
  let applied = $state<AuditFilter>({});
  let csvUrl = $derived(auditExportUrl(applied));

  // Apply pushes the current inputs into the URL; the effect below re-fetches.
  // (Applying a new filter resets to the first page.)
  function applyFilters() {
    offset = 0;
    pushURL();
  }
  function pushURL() {
    const p = new URLSearchParams();
    const f = filter();
    if (f.stream) p.set('stream', f.stream);
    if (f.actor) p.set('actor', f.actor);
    if (f.type) p.set('type', f.type);
    if (f.resource) p.set('resource', f.resource);
    if (fSince) p.set('since', fSince);
    if (fUntil) p.set('until', fUntil);
    if (hideNodeSteps) p.set('hide_node_steps', '1');
    if (offset) p.set('offset', String(offset));
    const qs = p.toString();
    goto(qs ? `?${qs}` : $page.url.pathname, { keepFocus: true, noScroll: true });
  }
  function pageBy(delta: number) {
    offset = Math.max(0, offset + delta * PAGE);
    pushURL();
  }

  // A generation token so overlapping loads (afterNavigate + Apply + paging) can't
  // resolve out of order and show stale rows/total.
  let loadSeq = 0;
  async function load() {
    const seq = ++loadSeq;
    error = '';
    forbidden = false;
    loading = true;
    try {
      const page = await listAuditPage(key, filter());
      if (seq !== loadSeq) return; // a newer load superseded this one
      list = page.entries;
      total = page.total;
    } catch (e) {
      if (seq !== loadSeq) return;
      const m = msg(e);
      // The audit trail is admin-only; surface that clearly rather than as a raw 403.
      if (m.includes('admin') || m.includes('403')) {
        forbidden = true;
      } else {
        error = m;
      }
    } finally {
      if (seq === loadSeq) loading = false;
    }
  }

  // --- PII masking config (admin) ---
  let maskFields = $state('');
  let maskSaving = $state(false);
  let maskNote = $state('');
  // Gate Save on a successful load: without this, a failed/never-completed load
  // leaves the field empty, and saving would silently wipe the existing config.
  let privacyLoaded = $state(false);
  async function loadPrivacy() {
    try {
      const cfg = await getPrivacy(key);
      maskFields = (cfg.fields ?? []).join(', ');
      maskNote = cfg.updated_by ? `last set by ${cfg.updated_by}` : '';
      privacyLoaded = true;
    } catch {
      /* non-admins simply do not see the editor populated */
      privacyLoaded = false;
    }
  }
  async function savePrivacy() {
    if (!privacyLoaded) {
      toast.error('Masking config has not loaded — refusing to overwrite it');
      return;
    }
    maskSaving = true;
    try {
      const fields = maskFields
        .split(/[\s,]+/)
        .map((f) => f.trim())
        .filter(Boolean);
      await setPrivacy(key, fields);
      toast.success('Masking fields saved');
      await loadPrivacy();
    } catch (e) {
      toast.error(msg(e));
    } finally {
      maskSaving = false;
    }
  }
  onMount(loadPrivacy);

  // --- Managed API tokens (admin) ---
  let keys = $state<ManagedApiKey[]>([]);
  let kName = $state('');
  let kActor = $state('');
  let kRole = $state<Role>('viewer');
  let kScope = $state<Scope>('sandbox');
  let kExpires = $state('');
  let kCreating = $state(false);
  // The generated secret is shown once, right after creation/rotation, and never
  // again. secretNote explains how long a rotated-away secret keeps working.
  let newSecret = $state('');
  let secretNote = $state('');
  // Rotations roll the new secret out over an hour-long grace window so the old
  // one keeps authenticating until the operator finishes redeploying.
  const ROTATE_GRACE_SECONDS = 3600;
  async function loadKeys() {
    try {
      keys = await listApiKeys(key);
    } catch {
      /* non-admins simply do not see the token list populated */
    }
  }
  async function createKey() {
    if (!kName.trim() || !kActor.trim()) {
      toast.error('name and actor are required');
      return;
    }
    kCreating = true;
    try {
      const { secret } = await createApiKey(key, {
        name: kName.trim(),
        actor: kActor.trim(),
        role: kRole,
        scope: kScope,
        expires_at: kExpires ? new Date(kExpires).toISOString() : undefined
      });
      newSecret = secret;
      secretNote = '';
      kName = '';
      kActor = '';
      kExpires = '';
      toast.success('API token created');
      await loadKeys();
    } catch (e) {
      toast.error(msg(e));
    } finally {
      kCreating = false;
    }
  }
  async function rotateKey(id: string) {
    try {
      const { api_key, secret } = await rotateApiKey(key, id, ROTATE_GRACE_SECONDS);
      newSecret = secret;
      secretNote = api_key.prev_hash_expires_at
        ? `The previous secret keeps working until ${new Date(api_key.prev_hash_expires_at).toLocaleString()}.`
        : '';
      toast.success('Token rotated');
      await loadKeys();
    } catch (e) {
      toast.error(msg(e));
    }
  }
  async function revokeKey(id: string) {
    try {
      await revokeApiKey(key, id);
      toast.success('Token revoked');
      await loadKeys();
    } catch (e) {
      toast.error(msg(e));
    }
  }
  function keyStatus(k: ManagedApiKey): string {
    if (k.revoked_at) return 'revoked';
    if (k.expires_at && new Date(k.expires_at) <= new Date()) return 'expired';
    return 'active';
  }
  onMount(loadKeys);

  // The URL drives the view: afterNavigate fires on mount, on Apply (goto), and on
  // back/forward — hydrate the inputs from the query string and fetch.
  afterNavigate(() => {
    const sp = get(page).url.searchParams;
    fStream = sp.get('stream') ?? '';
    fActor = sp.get('actor') ?? '';
    fType = sp.get('type') ?? '';
    fResource = sp.get('resource') ?? '';
    fSince = sp.get('since') ?? '';
    fUntil = sp.get('until') ?? '';
    hideNodeSteps = sp.get('hide_node_steps') === '1';
    offset = Number(sp.get('offset') ?? '0') || 0;
    // CSV export tracks the applied filter (rows on screen) but not the page window.
    applied = { ...filter(), limit: undefined, offset: undefined };
    void load();
  });
</script>

<main class="admin-surface">
  <span class="admin-tag"><Icon name="shield" size={13} /> Admin</span>
  <div class="head">
    <h1><Icon name="shield" size={20} /> Audit log</h1>
    <div class="actions">
      <a class="btn" href={csvUrl} download="audit.csv" data-testid="audit-csv">
        <Icon name="download" size={14} /> CSV
      </a>
      <button onclick={() => load()}><Icon name="reload" size={15} /> Reload</button>
    </div>
  </div>
  <p class="muted">
    Every recorded action across this workspace — who did what, when — read straight from the
    immutable event log.
  </p>

  <details class="masking" data-testid="masking-config">
    <summary
      ><Icon name="shield" size={14} /> PII masking
      <span class="muted">— field-level redaction</span></summary
    >
    <p class="muted">
      Comma- or space-separated field names (case-insensitive). Their values are redacted in
      decision input/output and exports at the read boundary — the raw event log is untouched.
    </p>
    <div class="mask-row">
      <input
        bind:value={maskFields}
        aria-label="masked fields"
        placeholder="ssn, dob, email, phone"
      />
      <button
        onclick={savePrivacy}
        disabled={maskSaving || !privacyLoaded}
        data-testid="save-masking"
      >
        {maskSaving ? 'Saving…' : 'Save'}
      </button>
    </div>
    {#if maskNote}<p class="muted note">{maskNote}</p>{/if}
  </details>

  <details class="tokens" data-testid="api-keys-config">
    <summary
      ><Icon name="shield" size={14} /> API tokens
      <span class="muted">— durable keys for the decide/data APIs</span></summary
    >
    <p class="muted">
      Each token is generated once and stored hashed — the secret below is shown a single time.
      Scope and role bound what the token can do; revoke takes effect immediately.
    </p>
    <div class="token-form">
      <input bind:value={kName} aria-label="token name" placeholder="name (e.g. CI bot)" />
      <input bind:value={kActor} aria-label="token actor" placeholder="actor (e.g. ci@acme)" />
      <select bind:value={kRole} aria-label="token role">
        <option value="viewer">viewer</option>
        <option value="operator">operator</option>
        <option value="editor">editor</option>
        <option value="approver">approver</option>
        <option value="admin">admin</option>
      </select>
      <select bind:value={kScope} aria-label="token scope">
        <option value="sandbox">sandbox</option>
        <option value="production">production</option>
      </select>
      <input
        type="datetime-local"
        bind:value={kExpires}
        aria-label="token expiry"
        title="optional expiry"
      />
      <button onclick={createKey} disabled={kCreating} data-testid="create-token">
        {kCreating ? 'Creating…' : 'Create token'}
      </button>
    </div>
    {#if newSecret}
      <div class="secret" data-testid="new-secret">
        <span class="muted">Copy this secret now — it will not be shown again:</span>
        <code>{newSecret}</code>
        <button
          class="dismiss"
          onclick={() => {
            newSecret = '';
            secretNote = '';
          }}
          aria-label="dismiss secret">Done</button
        >
        {#if secretNote}<p class="muted note secret-note">{secretNote}</p>{/if}
      </div>
    {/if}
    {#if keys.length > 0}
      <div class="table-wrap">
        <table class="token-table">
          <thead>
            <tr><th>Name</th><th>Actor</th><th>Role</th><th>Scope</th><th>Status</th><th></th></tr>
          </thead>
          <tbody>
            {#each keys as k (k.id)}
              <tr>
                <td>{k.name}</td>
                <td class="muted">{k.identity.actor}</td>
                <td>{k.role}</td>
                <td><span class="badge">{k.scope}</span></td>
                <td><span class="status {keyStatus(k)}">{keyStatus(k)}</span></td>
                <td class="row-actions">
                  <a
                    class="audit-link"
                    href={appHref(`/audit?resource=${encodeURIComponent(k.id)}`)}>Audit</a
                  >
                  {#if keyStatus(k) === 'active'}
                    <button class="rotate" onclick={() => rotateKey(k.id)}>Rotate</button>
                    <button class="revoke" onclick={() => revokeKey(k.id)}>Revoke</button>
                  {/if}
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </details>

  <form
    class="filters"
    onsubmit={(e) => {
      e.preventDefault();
      applyFilters();
    }}
  >
    <input bind:value={fStream} placeholder="stream" aria-label="stream filter" size="12" />
    <input bind:value={fActor} placeholder="actor" aria-label="actor filter" size="12" />
    <input bind:value={fType} placeholder="event type" aria-label="type filter" size="16" />
    <input
      bind:value={fResource}
      placeholder="resource id"
      aria-label="resource filter"
      size="16"
    />
    <label class="from"
      >from <input type="datetime-local" bind:value={fSince} aria-label="from time" /></label
    >
    <label class="to"
      >to <input type="datetime-local" bind:value={fUntil} aria-label="to time" /></label
    >
    <label class="hide-nodes">
      <input type="checkbox" bind:checked={hideNodeSteps} aria-label="hide node steps" />
      Hide node steps
    </label>
    <button type="submit">Apply</button>
  </form>

  {#if loading}
    <Skeleton rows={6} />
  {:else if forbidden}
    <p class="muted" data-testid="audit-forbidden">
      The audit log is restricted to the <strong>admin</strong> role.
    </p>
  {:else if error}
    <p class="err">{error}</p>
  {:else if list.length === 0}
    <EmptyState
      icon="shield"
      title="No matching audit events"
      hint="Nothing matches the current filter. Clear or widen the filters above, or act on the workspace to record new events."
    />
  {:else}
    <div class="table-wrap">
      <table>
        <thead>
          <tr><th>When</th><th>Actor</th><th>Stream</th><th>Event</th><th>Details</th></tr>
        </thead>
        <tbody>
          {#each list as e (e.seq)}
            <tr>
              <td class="muted" title={new Date(e.time).toLocaleString()}
                ><RelativeTime value={e.time} /></td
              >
              <td>{e.actor}</td>
              <td><span class="badge">{e.stream}</span></td>
              <td>{e.type}</td>
              <td class="payload">
                {#if e.payload !== undefined}
                  <details>
                    <summary>view</summary>
                    <pre>{JSON.stringify(e.payload, null, 2)}</pre>
                  </details>
                {:else}
                  <span class="muted">—</span>
                {/if}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
    <div class="pager">
      <span class="muted">{offset + 1}–{offset + list.length} of {total}</span>
      <span class="spacer"></span>
      <button onclick={() => pageBy(-1)} disabled={offset === 0}>← Prev</button>
      <button onclick={() => pageBy(1)} disabled={offset + list.length >= total}>Next →</button>
    </div>
  {/if}
</main>

<style>
  .pager {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    margin-top: 0.75rem;
  }
  .pager .spacer {
    flex: 1;
  }
  .pager button {
    font: inherit;
    padding: 0.35rem 0.7rem;
  }
  .filters .hide-nodes,
  .filters .from,
  .filters .to {
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
    font-size: 0.85rem;
    color: var(--fg-muted);
  }
  main {
    max-width: 68rem;
    margin: 2rem auto;
    padding: 0 1.25rem;
  }
  .masking,
  .tokens {
    border: 1px solid var(--admin-border, var(--border));
    border-radius: 10px;
    padding: 0.6rem 0.9rem;
    margin: 0.8rem 0;
  }
  .masking summary,
  .tokens summary {
    cursor: pointer;
    font-weight: 600;
  }
  .token-form {
    display: flex;
    flex-wrap: wrap;
    gap: 0.5rem;
    margin-top: 0.6rem;
  }
  .token-form input,
  .token-form select {
    padding: 0.4rem 0.55rem;
    border: 1px solid var(--admin-border, var(--border));
    border-radius: 8px;
    background: var(--admin-surface-2, var(--surface-2));
    color: inherit;
    font: inherit;
  }
  .secret {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 0.5rem;
    margin-top: 0.6rem;
    padding: 0.5rem 0.7rem;
    border-radius: 8px;
    background: color-mix(in srgb, var(--accent) 10%, var(--surface));
    border: 1px solid var(--accent);
  }
  .secret code {
    font-family: var(--mono, monospace);
    font-size: 0.85rem;
    user-select: all;
  }
  .secret .dismiss {
    margin-left: auto;
  }
  .token-table {
    margin-top: 0.7rem;
  }
  .status {
    display: inline-block;
    padding: 0.05rem 0.5rem;
    border-radius: 999px;
    font-size: 0.76rem;
    font-weight: 600;
  }
  .status.active {
    color: var(--ok, #15803d);
    background: color-mix(in srgb, var(--ok, #15803d) 14%, transparent);
  }
  .status.revoked,
  .status.expired {
    color: var(--fg-subtle);
    background: var(--surface-2);
  }
  .row-actions {
    display: flex;
    gap: 0.6rem;
    align-items: center;
  }
  .audit-link {
    font-size: 0.82rem;
    color: var(--link);
  }
  .rotate {
    font-size: 0.82rem;
    color: var(--link);
  }
  .revoke {
    font-size: 0.82rem;
    color: var(--danger);
  }
  .secret-note {
    flex-basis: 100%;
    margin: 0;
  }
  .mask-row {
    display: flex;
    gap: 0.5rem;
    margin-top: 0.5rem;
  }
  .mask-row input {
    flex: 1;
    padding: 0.4rem 0.55rem;
    border: 1px solid var(--admin-border, var(--border));
    border-radius: 8px;
    background: var(--admin-surface-2, var(--surface-2));
    color: inherit;
    font: inherit;
  }
  .note {
    margin: 0.4rem 0 0;
    font-size: 0.8rem;
  }
  .admin-tag {
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
    padding: 0.15rem 0.55rem;
    margin-bottom: 0.6rem;
    border-radius: 999px;
    font-size: 0.7rem;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    color: var(--on-accent);
    background: linear-gradient(135deg, var(--accent), var(--accent-2));
  }
  .head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    flex-wrap: wrap;
    gap: 0.5rem;
  }
  h1 {
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }
  .actions {
    display: flex;
    gap: 0.5rem;
  }
  .btn {
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
    padding: 0.4rem 0.7rem;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--surface);
    color: var(--fg);
    font-size: 0.88rem;
    text-decoration: none;
  }
  .btn:hover {
    background: var(--surface-2);
  }
  .filters {
    display: flex;
    flex-wrap: wrap;
    gap: 0.5rem;
    margin: 0.75rem 0;
  }
  table {
    width: 100%;
    border-collapse: collapse;
    margin-top: 0.5rem;
  }
  th {
    text-align: left;
    font-size: 0.78rem;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--fg-subtle);
    padding: 0.5rem 0.6rem;
    border-bottom: 1px solid var(--border);
  }
  td {
    padding: 0.55rem 0.6rem;
    border-bottom: 1px solid var(--border);
    font-size: 0.9rem;
    vertical-align: top;
  }
  .badge {
    display: inline-block;
    padding: 0.1rem 0.5rem;
    border-radius: 999px;
    font-size: 0.78rem;
    font-weight: 550;
    background: var(--surface-2);
    color: var(--fg-muted);
  }
  .payload pre {
    margin: 0.4rem 0 0;
    padding: 0.5rem;
    background: var(--surface-2);
    border-radius: var(--radius);
    font-size: 0.8rem;
    max-width: 28rem;
    overflow: auto;
  }
  .payload summary {
    cursor: pointer;
    color: var(--link);
    font-size: 0.85rem;
  }
  .muted {
    color: var(--fg-subtle);
  }
  .err {
    color: var(--danger);
  }
</style>
