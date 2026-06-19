<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- The landing page is persona-aware: the active persona's config picks its home
     composition (a bespoke deck for the original archetypes, else the config-driven
     PersonaHome). The persona is a client-side preference anyone can switch (lib/persona). -->
<script lang="ts">
  import { persona, personaConfig } from '$lib/persona';
  import { user } from '$lib/session';
  import { loadDashboard, type DashboardData } from '$lib/dashboard';
  import BuilderDeck from '$lib/dashboards/BuilderDeck.svelte';
  import OperatorDeck from '$lib/dashboards/OperatorDeck.svelte';
  import ShowcaseDeck from '$lib/dashboards/ShowcaseDeck.svelte';
  import PersonaHome from '$lib/dashboards/PersonaHome.svelte';
  import EvaluatorTour from '$lib/dashboards/EvaluatorTour.svelte';
  import Welcome from '$lib/dashboards/Welcome.svelte';

  // The persona's config picks its home composition — the three original
  // archetypes keep bespoke decks; role personas use the config-driven home.
  const home = $derived(personaConfig($persona).home);

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

  // Load once per signed-in session — and reset on sign-out so a re-login within
  // the session reloads. A single effect covers mount and later auth changes
  // (the previous onMount + effect pair double-loaded on first paint).
  let loaded = $state(false);
  $effect(() => {
    if ($user) {
      if (!loaded) {
        loaded = true;
        load();
      }
    } else {
      loaded = false;
      loading = false;
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
  {#if home === 'showcase'}
    <ShowcaseDeck {data} />
  {:else if home === 'operator'}
    <OperatorDeck {data} />
  {:else if home === 'builder'}
    <BuilderDeck {data} />
  {:else if home === 'evaluator'}
    <EvaluatorTour {data} />
  {:else}
    <PersonaHome {data} />
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
