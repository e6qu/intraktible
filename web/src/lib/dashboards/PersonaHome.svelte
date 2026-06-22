<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Config-driven persona home: a role-focused cockpit composed from the active
     persona's config (primary actions + its navigation) over the shared dashboard
     data. Personas without a bespoke deck (Developer, Manager, Product, Evaluator)
     land here, so adding a persona needs no new component — just config. -->
<script lang="ts">
  import Icon from '$lib/Icon.svelte';
  import { persona, personaConfig, navFor } from '$lib/persona';
  import { personaHomeStats, DEFAULT_HOME_STATS, type DashboardData } from '$lib/dashboard';
  import { appHref } from '$lib/paths';

  let { data }: { data: DashboardData } = $props();

  const cfg = $derived(personaConfig($persona));
  const nav = $derived(navFor($persona));
  // The at-a-glance tiles are the persona's chosen questions over the shared data —
  // a manager sees pending/overdue, a developer failed/latency — falling back to the
  // generic deck when a persona declares none.
  const tiles = $derived(personaHomeStats(cfg.homeStats ?? DEFAULT_HOME_STATS, data));
</script>

<main data-testid="persona-home">
  <header class="intro">
    <p class="role">Viewing as</p>
    <h1>{cfg.label}</h1>
    <p class="blurb">{cfg.blurb}</p>
  </header>

  <section class="at-a-glance" aria-label="At a glance">
    {#each tiles as t (t.id)}
      <div class="tile">
        <span class="n">{t.value}</span><span class="k">{t.label}</span>
      </div>
    {/each}
  </section>

  <h2>Start here</h2>
  <div class="actions">
    {#each cfg.actions as a (a.href)}
      <a class="action" href={appHref(a.href)}>
        <span class="ico"><Icon name={a.icon} size={20} /></span>
        <span class="lbl">{a.label}</span>
        <span class="go"><Icon name="chevron-down" size={16} /></span>
      </a>
    {/each}
  </div>

  <h2>Your workspaces</h2>
  <nav class="jump" aria-label="Persona sections">
    {#each nav as item (item.href)}
      <a class="chip" href={appHref(item.href)}><Icon name={item.icon} size={14} /> {item.label}</a>
    {/each}
  </nav>
</main>

<style>
  main {
    max-width: 60rem;
    margin: 2.5rem auto;
    padding: 0 1.25rem;
  }
  .intro {
    margin-bottom: 1.5rem;
  }
  .role {
    margin: 0;
    font-size: 0.72rem;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    color: var(--fg-subtle);
  }
  h1 {
    margin: 0.1rem 0 0.2rem;
  }
  .blurb {
    margin: 0;
    color: var(--fg-muted);
    font-size: 1.02rem;
  }
  .at-a-glance {
    display: flex;
    flex-wrap: wrap;
    gap: 0.8rem;
    margin: 1.2rem 0 2rem;
  }
  .tile {
    display: flex;
    flex-direction: column;
    min-width: 7rem;
    padding: 0.9rem 1.1rem;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--surface-2);
  }
  .tile .n {
    font-size: 1.6rem;
    font-weight: 650;
    font-variant-numeric: tabular-nums;
    color: var(--fg);
  }
  .tile .k {
    font-size: 0.78rem;
    color: var(--fg-subtle);
  }
  h2 {
    font-size: 0.95rem;
    margin: 1.6rem 0 0.6rem;
  }
  .actions {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(15rem, 1fr));
    gap: 0.7rem;
  }
  .action {
    display: flex;
    align-items: center;
    gap: 0.7rem;
    padding: 0.9rem 1rem;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--surface);
    color: var(--fg);
    font-weight: 550;
  }
  .action:hover {
    border-color: var(--accent);
    text-decoration: none;
  }
  .action .ico {
    display: inline-flex;
    color: var(--accent-ink, var(--accent));
  }
  .action .lbl {
    flex: 1;
  }
  .action .go {
    color: var(--fg-subtle);
    transform: rotate(-90deg);
  }
  .jump {
    display: flex;
    flex-wrap: wrap;
    gap: 0.4rem;
  }
  .chip {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    padding: 0.35rem 0.7rem;
    border: 1px solid var(--border);
    border-radius: 999px;
    background: var(--surface-2);
    color: var(--fg-muted);
    font-size: 0.85rem;
  }
  .chip:hover {
    color: var(--fg);
    border-color: var(--accent);
    text-decoration: none;
  }
</style>
