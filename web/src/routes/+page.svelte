<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { onMount } from 'svelte';
  import { getStats, sayHello, currentUser, logout, type Identity } from '$lib/api';

  let key = $state('dev-sandbox-key');
  let name = $state('world');
  let out = $state('stats will appear here…');
  let user = $state<Identity | null>(null);

  async function refreshUser() {
    try {
      user = await currentUser();
    } catch {
      user = null;
    }
  }
  async function signOut() {
    await logout();
    await refreshUser();
  }
  onMount(refreshUser);

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
  <p>command → event log → projection → API → this UI.</p>
  <p data-testid="auth-status">
    {#if user}
      Signed in as <b>{user.actor}</b> ({user.org}/{user.workspace})
      <button onclick={signOut}>Sign out</button>
    {:else}
      Not signed in — <a href="/login">sign in →</a>
    {/if}
  </p>
  <p>
    <a href="/engine">Decision Engine builder →</a>
    &nbsp;·&nbsp;
    <a href="/cases">Case Manager queue →</a>
    &nbsp;·&nbsp;
    <a href="/agents">Agent Manager →</a>
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
