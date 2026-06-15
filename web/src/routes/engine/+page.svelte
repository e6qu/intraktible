<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { onMount } from 'svelte';
  import { listFlows, createFlow, type Flow } from '$lib/api';

  let key = $state('dev-sandbox-key');
  let flows = $state<Flow[]>([]);
  let slug = $state('');
  let name = $state('');
  let error = $state('');

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
    try {
      await createFlow(key, slug, name);
      slug = '';
      name = '';
      await load();
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    }
  }

  onMount(load);
</script>

<main>
  <h1>Decision Engine — Flows</h1>
  <div class="row">
    <input bind:value={key} aria-label="API key" />
    <button onclick={load}>Reload</button>
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
    <button type="submit">Create flow</button>
  </form>

  {#if error}<p class="err">{error}</p>{/if}

  {#if flows.length === 0}
    <p class="muted">No flows yet.</p>
  {:else}
    <ul>
      {#each flows as f (f.flow_id)}
        <li>
          <a href={`/engine/${f.flow_id}`}>{f.name}</a>
          <code>{f.slug}</code> — v{f.latest}
        </li>
      {/each}
    </ul>
  {/if}
</main>

<style>
  main {
    max-width: 48rem;
    margin: 2rem auto;
    padding: 0 1rem;
    font-family: system-ui, sans-serif;
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
  ul {
    padding-left: 1rem;
  }
  li {
    margin: 0.3rem 0;
  }
  code {
    background: #8881;
    padding: 0 0.3rem;
    border-radius: 0.3rem;
  }
  .err {
    color: #b00;
  }
  .muted {
    color: #888;
  }
</style>
