<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { getStats, sayHello } from '$lib/api';

  let key = $state('dev-sandbox-key');
  let name = $state('world');
  let out = $state('stats will appear here…');

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
</script>

<main>
  <h1>intraktible — Phase 0 vertical slice</h1>
  <p>
    command → event log → projection → API → this UI.
    <a href="/engine">Open the Decision Engine builder →</a>
  </p>
  <div class="row">
    <input bind:value={key} aria-label="API key" />
    <input bind:value={name} aria-label="name" />
    <button onclick={say}>Say hello</button>
    <button onclick={stats}>Refresh</button>
  </div>
  <pre>{out}</pre>
</main>

<style>
  main {
    max-width: 40rem;
    margin: 3rem auto;
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
  pre {
    background: #8881;
    padding: 0.8rem;
    border-radius: 0.5rem;
  }
</style>
