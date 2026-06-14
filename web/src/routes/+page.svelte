<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  let key = $state('dev-sandbox-key');
  let name = $state('world');
  let out = $state('stats will appear here…');

  async function stats() {
    const r = await fetch('/v1/hello/stats', { headers: { 'X-Api-Key': key } });
    out = JSON.stringify(await r.json(), null, 2);
  }
  async function say() {
    const r = await fetch('/v1/hello', {
      method: 'POST',
      headers: { 'X-Api-Key': key, 'Content-Type': 'application/json' },
      body: JSON.stringify({ name })
    });
    out = `POST /v1/hello → ${r.status}\n` + JSON.stringify(await r.json(), null, 2);
    await stats();
  }
</script>

<main>
  <h1>intraktible — Phase 0 vertical slice</h1>
  <p>command → event log → projection → API → this UI. The Decision Engine builder
     (SvelteKit + Svelte Flow) lands in Phase 1.</p>
  <div class="row">
    <input bind:value={key} aria-label="API key" />
    <input bind:value={name} aria-label="name" />
    <button onclick={say}>Say hello</button>
    <button onclick={stats}>Refresh</button>
  </div>
  <pre>{out}</pre>
</main>

<style>
  main { max-width: 40rem; margin: 3rem auto; padding: 0 1rem; font-family: system-ui, sans-serif; }
  .row { display: flex; gap: .5rem; flex-wrap: wrap; margin: .6rem 0; }
  input, button { font: inherit; padding: .4rem .6rem; }
  pre { background: #8881; padding: .8rem; border-radius: .5rem; }
</style>
