<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { onMount } from 'svelte';
  import Icon from '$lib/Icon.svelte';
  import { getStats, sayHello, currentUser, logout, type Identity } from '$lib/api';

  const components = [
    {
      href: '/engine',
      icon: 'engine',
      title: 'Decision Engine',
      desc: 'Build versioned decision flows on a visual canvas; deploy, A/B, and export to Mermaid or BPMN.'
    },
    {
      href: '/cases',
      icon: 'cases',
      title: 'Case Manager',
      desc: 'Human-in-the-loop review queue with SLA tracking and a full audit trail.'
    },
    {
      href: '/agents',
      icon: 'agents',
      title: 'Agent Manager',
      desc: 'Define AI agents with tools and structured output; run sync or async, monitor, escalate.'
    }
  ];

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
  <section class="hero">
    <h1>intraktible</h1>
    <p class="tagline">
      An open-source agentic decision platform — event-sourced, replayable, fully self-hosted.
    </p>
    <p data-testid="auth-status" class="auth">
      {#if user}
        Signed in as <b>{user.actor}</b> <span class="muted">({user.org}/{user.workspace})</span>
        <button onclick={signOut}><Icon name="signout" size={14} /> Sign out</button>
      {:else}
        Not signed in — <a href="/login">sign in →</a>
      {/if}
    </p>
  </section>

  <section class="cards">
    {#each components as c (c.href)}
      <a class="card" href={c.href}>
        <span class="cardicon"><Icon name={c.icon} size={22} /></span>
        <span class="cardtitle">{c.title}</span>
        <span class="carddesc">{c.desc}</span>
      </a>
    {/each}
  </section>

  <section class="demo">
    <h2>Phase 0 vertical slice</h2>
    <p class="muted">command → event log → projection → API → this UI.</p>
    <div class="row">
      <input bind:value={key} aria-label="API key" placeholder="API key" />
      <input bind:value={name} aria-label="name" placeholder="name" />
      <button class="primary" onclick={say}>Say hello</button>
      <button onclick={stats}><Icon name="reload" size={14} /> Refresh</button>
    </div>
    <pre>{out}</pre>
  </section>
</main>

<style>
  main {
    max-width: 52rem;
    margin: 2.5rem auto;
    padding: 0 1.25rem;
  }
  .hero h1 {
    font-size: 2rem;
  }
  .tagline {
    color: var(--fg-muted);
    font-size: 1.05rem;
    margin-top: 0.25rem;
  }
  .auth {
    margin-top: 0.75rem;
  }
  .auth .muted {
    color: var(--fg-subtle);
  }
  .cards {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(14rem, 1fr));
    gap: 1rem;
    margin: 1.5rem 0 2rem;
  }
  .card {
    display: flex;
    flex-direction: column;
    gap: 0.4rem;
    padding: 1.1rem;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--surface);
    box-shadow: var(--shadow);
    color: var(--fg);
    transition:
      border-color 0.12s ease,
      transform 0.12s ease;
  }
  .card:hover {
    text-decoration: none;
    border-color: var(--accent);
    transform: translateY(-2px);
  }
  .cardicon {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 40px;
    height: 40px;
    border-radius: 10px;
    color: var(--on-accent);
    background: linear-gradient(135deg, var(--accent), var(--accent-2));
  }
  .cardtitle {
    font-weight: 650;
    font-size: 1.05rem;
  }
  .carddesc {
    color: var(--fg-muted);
    font-size: 0.9rem;
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
