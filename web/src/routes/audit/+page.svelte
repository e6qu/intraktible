<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { onMount } from 'svelte';
  import Icon from '$lib/Icon.svelte';
  import RelativeTime from '$lib/RelativeTime.svelte';
  import { listAudit, auditExportUrl, type AuditEntry, type AuditFilter } from '$lib/api';

  // API calls authenticate via the session cookie (empty key → no X-Api-Key).
  const key = '';
  let list = $state<AuditEntry[]>([]);
  let error = $state('');
  let forbidden = $state(false);

  // Filter inputs (bound to the form); applied on Apply/Reload.
  let fStream = $state('');
  let fActor = $state('');
  let fType = $state('');
  let fResource = $state('');

  function msg(e: unknown): string {
    return e instanceof Error ? e.message : String(e);
  }
  function filter(): AuditFilter {
    return {
      stream: fStream.trim() || undefined,
      actor: fActor.trim() || undefined,
      type: fType.trim() || undefined,
      resource: fResource.trim() || undefined
    };
  }
  let csvUrl = $derived(auditExportUrl(filter()));

  async function load() {
    error = '';
    forbidden = false;
    try {
      list = await listAudit(key, filter());
    } catch (e) {
      const m = msg(e);
      // The audit trail is admin-only; surface that clearly rather than as a raw 403.
      if (m.includes('admin') || m.includes('403')) {
        forbidden = true;
      } else {
        error = m;
      }
    }
  }
  onMount(load);
</script>

<main class="admin-surface">
  <span class="admin-tag"><Icon name="shield" size={13} /> Admin</span>
  <div class="head">
    <h1><Icon name="shield" size={20} /> Audit log</h1>
    <div class="actions">
      <a class="btn" href={csvUrl} download="audit.csv" data-testid="audit-csv">
        <Icon name="download" size={14} /> CSV
      </a>
      <button onclick={load}><Icon name="reload" size={15} /> Reload</button>
    </div>
  </div>
  <p class="muted">
    Every recorded action across this workspace — who did what, when — read straight from the
    immutable event log.
  </p>

  <form
    class="filters"
    onsubmit={(e) => {
      e.preventDefault();
      void load();
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
    <button type="submit">Apply</button>
  </form>

  {#if forbidden}
    <p class="muted" data-testid="audit-forbidden">
      The audit log is restricted to the <strong>admin</strong> role.
    </p>
  {:else if error}
    <p class="err">{error}</p>
  {:else if list.length === 0}
    <p class="muted">No matching audit events.</p>
  {:else}
    <div class="table-wrap">
      <table>
        <thead>
          <tr><th>When</th><th>Actor</th><th>Stream</th><th>Event</th><th>Details</th></tr>
        </thead>
        <tbody>
          {#each list as e (e.seq)}
            <tr>
              <td class="muted"><RelativeTime value={e.time} /></td>
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
  {/if}
</main>

<style>
  main {
    max-width: 68rem;
    margin: 2rem auto;
    padding: 0 1.25rem;
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
