<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- The landing page is a persona-aware dashboard: the same platform data, re-laid
     out and re-prioritised for whoever is viewing (Builder / Operator / Showcase).
     The persona is a client-side preference anyone can switch (see lib/persona). -->
<script lang="ts">
  import { onMount } from 'svelte';
  import { persona } from '$lib/persona';
  import { user } from '$lib/session';
  import { loadDashboard, type DashboardData } from '$lib/dashboard';
  import BuilderDeck from '$lib/dashboards/BuilderDeck.svelte';
  import OperatorDeck from '$lib/dashboards/OperatorDeck.svelte';
  import ShowcaseDeck from '$lib/dashboards/ShowcaseDeck.svelte';
  import Welcome from '$lib/dashboards/Welcome.svelte';

  let data = $state<DashboardData | null>(null);
  let error = $state('');
  let loading = $state(true);

  async function load() {
    loading = true;
    error = '';
    try {
      data = await loadDashboard();
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      loading = false;
    }
  }

  onMount(() => {
    if ($user) load();
    else loading = false;
  });
  // Load once the user signs in within the session.
  let loaded = $state(false);
  $effect(() => {
    if ($user && !loaded) {
      loaded = true;
      load();
    }
  });
</script>

{#if !$user}
  <Welcome />
{:else if loading}
  <div class="boot" aria-busy="true" aria-label="Loading dashboard">
    <span class="dot"></span><span class="dot"></span><span class="dot"></span>
  </div>
{:else if error}
  <main class="errwrap"><p class="err">Couldn’t load the dashboard: {error}</p></main>
{:else if data}
  {#if $persona === 'showcase'}
    <ShowcaseDeck {data} />
  {:else if $persona === 'operator'}
    <OperatorDeck {data} />
  {:else}
    <BuilderDeck {data} />
  {/if}
{/if}

<style>
  .boot {
    display: flex;
    gap: 0.5rem;
    justify-content: center;
    align-items: center;
    min-height: 60vh;
  }
  .dot {
    width: 0.6rem;
    height: 0.6rem;
    border-radius: 999px;
    background: var(--accent);
    animation: pulse 1s ease-in-out infinite;
  }
  .dot:nth-child(2) {
    animation-delay: 0.15s;
  }
  .dot:nth-child(3) {
    animation-delay: 0.3s;
  }
  @keyframes pulse {
    0%,
    100% {
      opacity: 0.25;
      transform: scale(0.8);
    }
    50% {
      opacity: 1;
      transform: scale(1);
    }
  }
  .errwrap {
    max-width: 60rem;
    margin: 3rem auto;
    padding: 0 1.25rem;
  }
  .err {
    color: var(--danger);
  }
</style>
