<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Developer persona: manage durable API keys through the documented /v1/api-keys
     API (no UI-only backdoor). A created/rotated secret is revealed exactly once. -->
<script lang="ts">
  import { onMount } from 'svelte';
  import Icon from '$lib/Icon.svelte';
  import EmptyState from '$lib/EmptyState.svelte';
  import Skeleton from '$lib/Skeleton.svelte';
  import RelativeTime from '$lib/RelativeTime.svelte';
  import Badge from '$lib/Badge.svelte';
  import { lifecycleTone } from '$lib/badge';
  import {
    listApiKeys,
    createApiKey,
    rotateApiKey,
    revokeApiKey,
    ROLES,
    SCOPES,
    type ManagedApiKey,
    type Role,
    type Scope,
    type CreateApiKeyRequest
  } from '$lib/api';

  // Authenticates via the session cookie (empty key → no X-Api-Key header).
  // ROLES/SCOPES are the generated (Go-sourced) option lists — no hand-duplication.
  const key = '';

  let keys = $state<ManagedApiKey[]>([]);
  let error = $state('');
  let forbidden = $state(false);
  let loading = $state(true);

  // new-key form
  let name = $state('');
  let actor = $state('');
  let role = $state<Role>('operator');
  let scope = $state<Scope>('sandbox');
  let expires = $state('');
  let busy = $state(false);

  // The just-minted secret, shown once (from create or rotate). On rotate, `note`
  // explains how long the previous secret keeps authenticating.
  let revealed = $state<{ id: string; secret: string; note?: string } | null>(null);

  // Rotations roll the new secret out over an hour-long grace window so the old
  // one keeps authenticating until the operator finishes redeploying. This matches
  // the audit page so rotating from either surface behaves identically.
  const ROTATE_GRACE_SECONDS = 3600;

  function msg(e: unknown): string {
    return e instanceof Error ? e.message : String(e);
  }
  function status(k: ManagedApiKey): string {
    if (k.revoked_at) return 'revoked';
    if (k.expires_at && new Date(k.expires_at).getTime() < Date.now()) return 'expired';
    return 'active';
  }

  async function load() {
    loading = true;
    error = '';
    forbidden = false;
    try {
      keys = await listApiKeys(key);
    } catch (e) {
      const m = msg(e);
      // Listing keys is admin-only; surface that clearly, not as a raw 403.
      if (m.includes('admin') || m.includes('403')) {
        forbidden = true;
      } else {
        error = m;
      }
    } finally {
      loading = false;
    }
  }

  async function create() {
    if (busy) return; // Enter fires onsubmit directly, bypassing the disabled button
    error = '';
    busy = true;
    try {
      const req: CreateApiKeyRequest = { name: name.trim(), actor: actor.trim(), role, scope };
      if (expires) req.expires_at = new Date(expires).toISOString();
      const { api_key, secret } = await createApiKey(key, req);
      revealed = { id: api_key.id, secret };
      name = '';
      actor = '';
      expires = '';
      await load();
    } catch (e) {
      error = msg(e);
    } finally {
      busy = false;
    }
  }

  // The key currently being rotated/revoked. A double-click on Rotate would
  // otherwise mint TWO real secrets — the second overwrites `revealed`, so the
  // first (valid, server-side) secret is shown to no one and is unrecoverable.
  let mutating = $state<string | null>(null);

  async function rotate(id: string) {
    if (mutating) return;
    error = '';
    mutating = id;
    try {
      const { api_key, secret } = await rotateApiKey(key, id, ROTATE_GRACE_SECONDS);
      const note = api_key.prev_hash_expires_at
        ? `The previous secret keeps working until ${new Date(api_key.prev_hash_expires_at).toLocaleString()}.`
        : undefined;
      revealed = { id, secret, note };
      await load();
    } catch (e) {
      error = msg(e);
    } finally {
      mutating = null;
    }
  }

  async function revoke(id: string) {
    if (mutating) return;
    error = '';
    mutating = id;
    try {
      await revokeApiKey(key, id);
      if (revealed?.id === id) revealed = null;
      await load();
    } catch (e) {
      error = msg(e);
    } finally {
      mutating = null;
    }
  }

  async function copy(secret: string) {
    try {
      await navigator.clipboard.writeText(secret);
    } catch {
      // Clipboard may be unavailable (no permission); the secret is selectable anyway.
    }
  }

  onMount(load);
</script>

<main>
  <h1><Icon name="connect" size={20} /> API keys</h1>
  <p class="muted">
    Durable keys for the decision API, scoped to an environment and a role. A key's secret is shown
    once at creation (and on rotation) — copy it now; it is stored hashed and cannot be retrieved.
  </p>

  <form
    class="define"
    onsubmit={(e) => {
      e.preventDefault();
      create();
    }}
  >
    <div class="row">
      <label
        >Name (required) <input
          bind:value={name}
          placeholder="ci-pipeline"
          aria-label="key name"
          required
        /></label
      >
      <label
        >Actor <input
          bind:value={actor}
          placeholder="svc-ci"
          aria-label="key actor"
          required
        /></label
      >
      <label
        >Role
        <select bind:value={role} aria-label="key role">
          {#each ROLES as r (r)}<option value={r}>{r}</option>{/each}
        </select></label
      >
      <label
        >Scope
        <select bind:value={scope} aria-label="key scope">
          {#each SCOPES as s (s)}<option value={s}>{s === '*' ? '* (any env)' : s}</option>{/each}
        </select></label
      >
      <label
        >Expires (optional)
        <input type="date" bind:value={expires} aria-label="key expiry" /></label
      >
      <button type="submit" disabled={busy}>{busy ? 'Creating…' : 'Create key'}</button>
    </div>
  </form>

  {#if error}<p class="err">{error}</p>{/if}

  {#if revealed}
    <div class="secret" data-testid="revealed-secret">
      <p><b>Copy your new secret now</b> — it won't be shown again.</p>
      <div class="secret-row">
        <code>{revealed.secret}</code>
        <button onclick={() => revealed && copy(revealed.secret)}>
          <Icon name="copy" size={14} /> Copy
        </button>
        <button class="ghost" onclick={() => (revealed = null)}>Dismiss</button>
      </div>
      {#if revealed.note}<p class="muted note">{revealed.note}</p>{/if}
    </div>
  {/if}

  {#if loading}
    <Skeleton rows={4} />
  {:else if forbidden}
    <EmptyState
      icon="connect"
      title="Restricted to the admin role"
      hint="Managing API keys is available only to admins. Ask an admin to create or rotate a key for you."
    />
  {:else if keys.length === 0}
    <EmptyState
      icon="connect"
      title="No API keys yet"
      hint="Create one above to call the decision API from a service or pipeline."
    />
  {:else}
    <div class="table-wrap">
      <table>
        <thead>
          <tr
            ><th>Name</th><th>Actor</th><th>Role</th><th>Scope</th><th>Created</th><th>Status</th
            ><th></th></tr
          >
        </thead>
        <tbody>
          {#each keys as k (k.id)}
            <tr class:revoked={status(k) !== 'active'}>
              <td>{k.name}</td>
              <td class="mono">{k.identity.actor}</td>
              <td>{k.role}</td>
              <td><span class="badge">{k.scope}</span></td>
              <td class="muted"><RelativeTime value={k.created_at} /></td>
              <td><Badge tone={lifecycleTone(status(k))}>{status(k)}</Badge></td>
              <td class="actions">
                {#if status(k) === 'active'}
                  <button class="link" onclick={() => rotate(k.id)} disabled={mutating !== null}
                    >Rotate</button
                  >
                  <button
                    class="link danger"
                    onclick={() => revoke(k.id)}
                    disabled={mutating !== null}>Revoke</button
                  >
                {:else}
                  <span class="muted">—</span>
                {/if}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</main>

<style>
  main {
    max-width: 56rem;
    margin: 2rem auto;
    padding: 0 1.25rem;
  }
  h1 {
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }
  .define {
    margin: 0.8rem 0;
  }
  .row {
    display: flex;
    flex-wrap: wrap;
    gap: 0.6rem;
    align-items: end;
  }
  label {
    display: inline-flex;
    flex-direction: column;
    gap: 0.15rem;
    font-size: 0.74rem;
    color: var(--fg-subtle);
  }
  input,
  select,
  button {
    font: inherit;
    padding: 0.4rem 0.6rem;
  }
  .secret {
    margin: 0.8rem 0;
    padding: 0.8rem 1rem;
    border: 1px solid var(--accent);
    border-radius: var(--radius);
    background: color-mix(in srgb, var(--accent) 8%, transparent);
  }
  .secret p {
    margin: 0 0 0.5rem;
  }
  .secret-row {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    flex-wrap: wrap;
  }
  .secret code {
    font-family: var(--font-mono);
    font-size: 0.9rem;
    padding: 0.3rem 0.5rem;
    background: var(--surface-2);
    border-radius: 4px;
    user-select: all;
  }
  .table-wrap {
    overflow-x: auto;
  }
  table {
    width: 100%;
    border-collapse: collapse;
    margin-top: 0.6rem;
  }
  th {
    text-align: left;
    font-size: 0.74rem;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--fg-subtle);
    padding: 0.45rem 0.6rem;
    border-bottom: 1px solid var(--border);
  }
  td {
    padding: 0.5rem 0.6rem;
    border-bottom: 1px solid var(--border);
    font-size: 0.9rem;
  }
  tr.revoked {
    opacity: 0.55;
  }
  .mono {
    font-family: var(--font-mono);
    font-size: 0.82rem;
  }
  .badge {
    display: inline-block;
    padding: 0.1rem 0.5rem;
    border-radius: 999px;
    font-size: 0.74rem;
    background: var(--surface-2);
    color: var(--fg-muted);
  }
  .actions {
    display: flex;
    gap: 0.6rem;
  }
  .link {
    border: none;
    background: none;
    padding: 0;
    color: var(--link, var(--accent-ink));
    cursor: pointer;
  }
  .link.danger {
    color: var(--danger);
  }
  .ghost {
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    background: none;
    color: var(--fg-muted);
  }
  .muted {
    color: var(--fg-subtle);
  }
  .note {
    margin: 0.5rem 0 0;
    font-size: 0.82rem;
  }
  .err {
    color: var(--danger);
  }
</style>
