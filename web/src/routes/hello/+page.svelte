<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- The Phase-0 vertical slice (command → event log → projection → API → UI),
     kept as a live, minimal demo of the backbone. Moved off the landing page so
     the home view can be a real, persona-aware dashboard. -->
<script lang="ts">
  import { onMount } from 'svelte';
  import Icon from '$lib/Icon.svelte';
  import { getStats, sayHello } from '$lib/api';
  import { appHref } from '$lib/paths';

  // API calls authenticate via the session cookie (empty key → no X-Api-Key).
  const key = '';
  let name = $state('world');
  let out = $state('loading stats…');

  async function stats() {
    try {
      out = JSON.stringify(await getStats(key), null, 2);
    } catch (err) {
      out = `Error: ${err instanceof Error ? err.message : String(err)}`;
    }
  }
  async function say() {
    try {
      const result = await sayHello(key, name);
      out = `POST /v1/hello → seq ${result.seq}\n` + JSON.stringify(result, null, 2);
      await stats();
    } catch (err) {
      out = `Error: ${err instanceof Error ? err.message : String(err)}`;
    }
  }
  onMount(stats);
</script>

<main>
  <p class="back"><a href={appHref('/')}>← dashboard</a></p>
  <h1>Phase 0 vertical slice</h1>
  <p class="muted">command → event log → projection → API → this UI.</p>
  <div class="row">
    <input bind:value={name} aria-label="name" placeholder="name" />
    <button class="primary" onclick={say}>Say hello</button>
    <button onclick={stats}><Icon name="reload" size={14} /> Refresh</button>
  </div>
  <pre>{out}</pre>
</main>

<style>
  main {
    max-width: 52rem;
    margin: 2.5rem auto;
    padding: 0 1.25rem;
  }
  .back {
    margin: 0 0 0.5rem;
    font-size: 0.85rem;
  }
  .row {
    display: flex;
    gap: 0.5rem;
    flex-wrap: wrap;
    margin: 0.6rem 0;
  }
  .muted {
    color: var(--fg-subtle);
  }
  pre {
    min-height: 2rem;
  }
</style>
