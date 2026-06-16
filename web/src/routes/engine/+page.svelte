<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { onMount } from 'svelte';
  import Icon from '$lib/Icon.svelte';
  import { listFlows, createFlow, type Flow } from '$lib/api';

  // API calls authenticate via the session cookie (empty key -> no X-Api-Key header).
  const key = '';
  let flows = $state<Flow[]>([]);
  let slug = $state('');
  let name = $state('');
  let error = $state('');
  let busy = $state(false);

  async function load() {
    error = '';
    try {
      flows = await listFlows(key);
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    }
  }

  async function create() {
    error = '';
    busy = true;
    try {
      await createFlow(key, slug, name);
      slug = '';
      name = '';
      await load();
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      busy = false;
    }
  }

  // liveVersion reads a flow's deployed version for an environment (via entries,
  // not a computed index, to stay clear of detect-object-injection).
  function liveVersion(flow: Flow, env: string): number | undefined {
    const found = Object.entries(flow.deployments ?? {}).find(([e]) => e === env);
    return found?.[1]?.version;
  }

  onMount(load);
</script>

<main>
  <div class="head">
    <h1>Decision Engine — Flows</h1>
    <button onclick={load}><Icon name="reload" size={15} /> Reload</button>
  </div>

  <form
    class="row"
    onsubmit={(e) => {
      e.preventDefault();
      create();
    }}
  >
    <input bind:value={slug} placeholder="slug" aria-label="slug" />
    <input bind:value={name} placeholder="name" aria-label="name" />
    <button type="submit" disabled={busy}>{busy ? 'Creating…' : 'Create flow'}</button>
  </form>

  {#if error}<p class="err">{error}</p>{/if}

  {#if flows.length === 0}
    <p class="muted">No flows yet.</p>
  {:else}
    <div class="table-wrap">
      <table>
        <thead>
          <tr><th>Name</th><th>Slug</th><th>Latest</th><th>Sandbox</th><th>Production</th></tr>
        </thead>
        <tbody>
          {#each flows as f (f.flow_id)}
            <tr>
              <td><a href={`/engine/${f.flow_id}`}>{f.name}</a></td>
              <td><code>{f.slug}</code></td>
              <td>v{f.latest}</td>
              <td>
                {#if liveVersion(f, 'sandbox')}<span class="badge live"
                    >v{liveVersion(f, 'sandbox')}</span
                  >{:else}<span class="muted">—</span>{/if}
              </td>
              <td>
                {#if liveVersion(f, 'production')}<span class="badge live"
                    >v{liveVersion(f, 'production')}</span
                  >{:else}<span class="muted">—</span>{/if}
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
  .head {
    display: flex;
    align-items: center;
    justify-content: space-between;
  }
  .row {
    display: flex;
    gap: 0.5rem;
    flex-wrap: wrap;
    margin: 0.6rem 0;
  }
  input,
  button {
    font: inherit;
    padding: 0.4rem 0.6rem;
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
    font-size: 0.92rem;
  }
  code {
    background: var(--surface-2);
    padding: 0 0.3rem;
    border-radius: 0.3rem;
  }
  .badge {
    display: inline-block;
    padding: 0.1rem 0.5rem;
    border-radius: 999px;
    font-size: 0.78rem;
    background: var(--surface-2);
    color: var(--fg-muted);
  }
  .badge.live {
    background: color-mix(in srgb, var(--ok) 18%, transparent);
    color: var(--ok);
  }
  .err {
    color: var(--danger);
  }
  .muted {
    color: var(--fg-subtle);
  }
</style>
