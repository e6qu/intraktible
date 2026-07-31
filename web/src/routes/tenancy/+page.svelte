<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Admin surface: tenant administration — platform organization lifecycle and
     org-level workspace/membership administration through the documented
     /v1/platform/orgs + /v1/orgs APIs. An org's first admin secret is revealed once. -->
<script lang="ts">
  import { onMount } from 'svelte';
  import EmptyState from '$lib/EmptyState.svelte';
  import Skeleton from '$lib/Skeleton.svelte';
  import Badge from '$lib/Badge.svelte';
  import CodeSnippet from '$lib/CodeSnippet.svelte';
  import {
    listOrganizations,
    createOrganization,
    organizationAction,
    listWorkspaces,
    createWorkspace,
    workspaceAction,
    listMemberships,
    grantMembership,
    revokeMembership,
    ApiError,
    type TenancyOrganization,
    type TenancyWorkspace,
    type TenancyMembership
  } from '$lib/api';

  const key = '';

  let orgs = $state<TenancyOrganization[]>([]);
  let selectedOrg = $state('');
  let workspaces = $state<TenancyWorkspace[]>([]);
  let selectedWorkspace = $state('');
  let memberships = $state<TenancyMembership[]>([]);
  let loading = $state(true);
  let error = $state('');
  let forbidden = $state(false);
  let busy = $state('');

  // Create-org form.
  let newOrgKey = $state('');
  let newOrgDisplay = $state('');
  let newOrgPlan = $state('');
  let newOrgAdmin = $state('');
  // Create-workspace form.
  let newWsKey = $state('');
  let newWsDisplay = $state('');
  // Grant-membership form.
  let newMember = $state('');
  let newMemberRole = $state<TenancyMembership['role']>('viewer');
  // The just-created org's one-time admin secret.
  let revealed = $state<{ org: string; keyId: string; secret: string } | null>(null);

  function msg(e: unknown): string {
    return e instanceof Error ? e.message : String(e);
  }

  async function loadOrgs() {
    loading = true;
    error = '';
    forbidden = false;
    try {
      orgs = await listOrganizations(key);
      if (selectedOrg === '' && orgs.length > 0) {
        selectedOrg = orgs[0].key;
        await loadWorkspaces();
      }
    } catch (e) {
      if (e instanceof ApiError && e.status === 403) forbidden = true;
      else error = msg(e);
    } finally {
      loading = false;
    }
  }

  async function loadWorkspaces() {
    if (!selectedOrg) {
      workspaces = [];
      memberships = [];
      return;
    }
    error = '';
    try {
      workspaces = await listWorkspaces(key, selectedOrg);
      if (selectedWorkspace === '' && workspaces.length > 0) {
        selectedWorkspace = workspaces[0].key;
      }
    } catch (e) {
      error = msg(e);
    }
  }

  async function loadMemberships() {
    if (!selectedOrg || !selectedWorkspace) {
      memberships = [];
      return;
    }
    error = '';
    try {
      memberships = await listMemberships(key, selectedOrg, selectedWorkspace);
    } catch (e) {
      error = msg(e);
    }
  }

  async function createOrg() {
    if (busy || !newOrgKey.trim() || !newOrgDisplay.trim() || !newOrgAdmin.trim()) return;
    error = '';
    busy = 'create-org';
    try {
      const created = await createOrganization(key, {
        key: newOrgKey.trim(),
        display: newOrgDisplay.trim(),
        config: { plan: newOrgPlan.trim() },
        admin_actor: newOrgAdmin.trim()
      });
      revealed = {
        org: created.org_key,
        keyId: created.admin_key_id,
        secret: created.admin_key_secret
      };
      newOrgKey = '';
      newOrgDisplay = '';
      newOrgPlan = '';
      newOrgAdmin = '';
      await loadOrgs();
    } catch (e) {
      error = msg(e);
    } finally {
      busy = '';
    }
  }

  async function orgLifecycle(org: string, action: 'suspend' | 'resume' | 'delete') {
    if (busy) return;
    const reason =
      action === 'resume' ? '' : (window.prompt(`Reason for ${action} on ${org}`) ?? '');
    if (action !== 'resume' && !reason.trim()) return;
    error = '';
    busy = `org-${action}-${org}`;
    try {
      await organizationAction(key, org, action, reason.trim());
      await loadOrgs();
    } catch (e) {
      error = msg(e);
    } finally {
      busy = '';
    }
  }

  async function createWs() {
    if (busy || !selectedOrg || !newWsKey.trim() || !newWsDisplay.trim()) return;
    error = '';
    busy = 'create-ws';
    try {
      await createWorkspace(key, selectedOrg, {
        key: newWsKey.trim(),
        display: newWsDisplay.trim(),
        config: {}
      });
      newWsKey = '';
      newWsDisplay = '';
      await loadWorkspaces();
    } catch (e) {
      error = msg(e);
    } finally {
      busy = '';
    }
  }

  async function wsLifecycle(ws: string, action: 'suspend' | 'resume' | 'delete') {
    if (busy || !selectedOrg) return;
    const reason =
      action === 'resume' ? '' : (window.prompt(`Reason for ${action} on ${ws}`) ?? '');
    if (action !== 'resume' && !reason.trim()) return;
    error = '';
    busy = `ws-${action}-${ws}`;
    try {
      await workspaceAction(key, selectedOrg, ws, action, reason.trim());
      await loadWorkspaces();
    } catch (e) {
      error = msg(e);
    } finally {
      busy = '';
    }
  }

  async function grant() {
    if (busy || !selectedOrg || !selectedWorkspace || !newMember.trim()) return;
    error = '';
    busy = 'grant';
    try {
      await grantMembership(key, selectedOrg, selectedWorkspace, newMember.trim(), newMemberRole);
      newMember = '';
      await loadMemberships();
    } catch (e) {
      error = msg(e);
    } finally {
      busy = '';
    }
  }

  async function revoke(actor: string) {
    if (busy || !selectedOrg || !selectedWorkspace) return;
    const reason = window.prompt(`Reason for revoking ${actor}`) ?? '';
    if (!reason.trim()) return;
    error = '';
    busy = `revoke-${actor}`;
    try {
      await revokeMembership(key, selectedOrg, selectedWorkspace, actor, reason.trim());
      await loadMemberships();
    } catch (e) {
      error = msg(e);
    } finally {
      busy = '';
    }
  }

  onMount(loadOrgs);
</script>

<svelte:head><title>Tenancy · intraktible</title></svelte:head>

<main class="page-shell" data-testid="tenancy-page">
  <header class="page-head">
    <div>
      <p class="eyebrow">Admin</p>
      <h1>Tenant administration</h1>
      <p class="lede">
        Platform organizations, their workspaces, and workspace memberships. An organization's first
        admin credential is revealed exactly once.
      </p>
    </div>
    <button class="secondary" disabled={loading} onclick={() => void loadOrgs()}>Reload</button>
  </header>

  {#if error}<p class="error" role="alert">{error}</p>{/if}

  {#if loading}
    <Skeleton rows={8} />
  {:else if forbidden}
    <EmptyState
      title="Restricted to the admin role"
      hint="Tenant administration is gated to platform and organization admins."
    />
  {:else}
    {#if revealed}
      <div class="panel reveal" data-testid="org-admin-secret">
        <h2>Organization created — save this credential now</h2>
        <p>
          This is the only time the org-admin secret for <b>{revealed.org}</b> is shown. Store it in a
          secret manager and unset any bootstrap key.
        </p>
        <CodeSnippet code={`admin key id:  ${revealed.keyId}\nadmin secret:  ${revealed.secret}`} />
        <button class="text-btn" onclick={() => (revealed = null)}>I have stored it safely</button>
      </div>
    {/if}

    <div class="split">
      <section class="panel" aria-label="Organizations">
        <div class="section-head">
          <div>
            <p class="eyebrow">Platform</p>
            <h2>Organizations</h2>
          </div>
        </div>
        {#if orgs.length === 0}
          <EmptyState title="No organizations" hint="Create the first organization below." />
        {:else}
          <ul class="cards">
            {#each orgs as org (org.key)}
              <li class="card" class:on={selectedOrg === org.key}>
                <button
                  class="card-head"
                  onclick={() => {
                    selectedOrg = org.key;
                    selectedWorkspace = '';
                    void loadWorkspaces();
                  }}
                >
                  <strong>{org.display}</strong>
                  <Badge tone={org.status === 'active' ? 'ok' : 'danger'}>{org.status}</Badge>
                </button>
                <p class="muted">
                  <code>{org.key}</code> · plan {org.config.plan ?? '—'} · {org.config
                    .max_workspaces ?? '∞'} max workspaces
                </p>
                {#if org.status === 'active'}
                  <button
                    class="text-btn"
                    disabled={busy === `org-suspend-${org.key}`}
                    onclick={() => void orgLifecycle(org.key, 'suspend')}>Suspend</button
                  >
                  <button
                    class="text-btn danger-btn"
                    disabled={busy === `org-delete-${org.key}`}
                    onclick={() => void orgLifecycle(org.key, 'delete')}>Delete</button
                  >
                {:else if org.status === 'suspended'}
                  <button
                    class="text-btn"
                    disabled={busy === `org-resume-${org.key}`}
                    onclick={() => void orgLifecycle(org.key, 'resume')}>Resume</button
                  >
                {/if}
              </li>
            {/each}
          </ul>
        {/if}

        <form
          class="inline-form"
          onsubmit={(e) => {
            e.preventDefault();
            void createOrg();
          }}
        >
          <h3>Create organization</h3>
          <label>Key<input bind:value={newOrgKey} placeholder="acme" /></label>
          <label>Display name<input bind:value={newOrgDisplay} placeholder="Acme Corp" /></label>
          <label>Plan<input bind:value={newOrgPlan} placeholder="enterprise" /></label>
          <label>Initial admin<input bind:value={newOrgAdmin} placeholder="alice" /></label>
          <button disabled={busy === 'create-org'}>Create & mint admin key</button>
        </form>
      </section>

      <section class="panel" aria-label="Workspaces">
        <div class="section-head">
          <div>
            <p class="eyebrow">{selectedOrg || 'select an org'}</p>
            <h2>Workspaces</h2>
          </div>
        </div>
        {#if !selectedOrg}
          <EmptyState
            title="Select an organization"
            hint="Pick one on the left to manage its workspaces."
          />
        {:else if workspaces.length === 0}
          <EmptyState
            title="No workspaces"
            hint="Create the first workspace for this organization."
          />
        {:else}
          <ul class="cards">
            {#each workspaces as ws (ws.key)}
              <li class="card" class:on={selectedWorkspace === ws.key}>
                <button
                  class="card-head"
                  onclick={() => {
                    selectedWorkspace = ws.key;
                    void loadMemberships();
                  }}
                >
                  <strong>{ws.display}</strong>
                  <Badge tone={ws.status === 'active' ? 'ok' : 'danger'}>{ws.status}</Badge>
                </button>
                <p class="muted">
                  <code>{ws.key}</code> · retention {ws.config.retention_days ?? '—'}d
                </p>
                {#if ws.status === 'active'}
                  <button
                    class="text-btn"
                    disabled={busy === `ws-suspend-${ws.key}`}
                    onclick={() => void wsLifecycle(ws.key, 'suspend')}>Suspend</button
                  >
                  <button
                    class="text-btn danger-btn"
                    disabled={busy === `ws-delete-${ws.key}`}
                    onclick={() => void wsLifecycle(ws.key, 'delete')}>Delete</button
                  >
                {:else if ws.status === 'suspended'}
                  <button
                    class="text-btn"
                    disabled={busy === `ws-resume-${ws.key}`}
                    onclick={() => void wsLifecycle(ws.key, 'resume')}>Resume</button
                  >
                {/if}
              </li>
            {/each}
          </ul>
        {/if}

        <form
          class="inline-form"
          onsubmit={(e) => {
            e.preventDefault();
            void createWs();
          }}
        >
          <h3>Create workspace</h3>
          <label>Key<input bind:value={newWsKey} placeholder="west" /></label>
          <label>Display name<input bind:value={newWsDisplay} placeholder="West" /></label>
          <button disabled={busy === 'create-ws' || !selectedOrg}>Create workspace</button>
        </form>
      </section>
    </div>

    <section class="panel" aria-label="Memberships">
      <div class="section-head">
        <div>
          <p class="eyebrow">{selectedOrg}/{selectedWorkspace || '—'}</p>
          <h2>Memberships</h2>
        </div>
      </div>
      {#if !selectedOrg || !selectedWorkspace}
        <EmptyState title="Select a workspace" hint="Pick a workspace to manage its memberships." />
      {:else if memberships.length === 0}
        <EmptyState title="No memberships" hint="Grant the first membership below." />
      {:else}
        <div class="table-wrap">
          <table>
            <thead
              ><tr><th>Actor</th><th>Role</th><th>Status</th><th>Granted by</th><th></th></tr
              ></thead
            >
            <tbody>
              {#each memberships as m (m.actor)}
                <tr>
                  <td><code>{m.actor}</code></td>
                  <td>{m.role}</td>
                  <td><Badge tone={m.status === 'active' ? 'ok' : 'warn'}>{m.status}</Badge></td>
                  <td>{m.granted_by}</td>
                  <td>
                    {#if m.status === 'active'}
                      <button
                        class="text-btn danger-btn"
                        disabled={busy === `revoke-${m.actor}`}
                        onclick={() => void revoke(m.actor)}>Revoke</button
                      >
                    {/if}
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {/if}

      <form
        class="inline-form"
        onsubmit={(e) => {
          e.preventDefault();
          void grant();
        }}
      >
        <h3>Grant membership</h3>
        <label>Actor<input bind:value={newMember} placeholder="carol" /></label>
        <label>
          Role
          <select bind:value={newMemberRole}>
            <option value="viewer">viewer</option>
            <option value="operator">operator</option>
            <option value="editor">editor</option>
            <option value="approver">approver</option>
            <option value="admin">admin</option>
          </select>
        </label>
        <button disabled={busy === 'grant' || !selectedWorkspace}>Grant</button>
      </form>
    </section>
  {/if}
</main>

<style>
  .page-shell {
    max-width: 90rem;
    margin: 0 auto;
    padding: 2rem;
  }
  .page-head,
  .section-head,
  .card-head {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 1rem;
  }
  .eyebrow {
    margin: 0 0 0.3rem;
    color: var(--accent-text);
    font: 600 0.72rem var(--font-mono);
    letter-spacing: 0.09em;
    text-transform: uppercase;
  }
  h1 {
    margin: 0;
    font-size: clamp(2rem, 4vw, 3.4rem);
  }
  h2 {
    margin: 0.1rem 0;
    font-size: 1.45rem;
  }
  .lede {
    margin: 0.4rem 0 0;
    color: var(--fg-muted);
    max-width: 46rem;
  }
  .panel {
    margin-top: 1.4rem;
    padding: 1.2rem;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius);
  }
  .split {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 1.2rem;
  }
  @media (max-width: 900px) {
    .split {
      grid-template-columns: 1fr;
    }
  }
  .cards {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    gap: 0.6rem;
  }
  .card {
    padding: 0.7rem;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
  }
  .card.on {
    border-color: var(--accent);
  }
  .card-head {
    width: 100%;
    background: none;
    border: none;
    padding: 0;
    cursor: pointer;
    text-align: left;
    font: inherit;
    color: inherit;
  }
  .muted {
    color: var(--fg-muted);
    font-size: 0.85rem;
  }
  .inline-form {
    margin-top: 1rem;
    padding-top: 1rem;
    border-top: 1px solid var(--border);
    display: grid;
    gap: 0.6rem;
  }
  .inline-form label {
    display: grid;
    gap: 0.2rem;
    font-size: 0.85rem;
  }
  .error {
    color: var(--danger);
  }
  .reveal {
    border-color: var(--warn);
  }
</style>
