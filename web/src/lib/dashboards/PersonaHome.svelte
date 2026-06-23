<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Config-driven persona home: a role-focused cockpit composed from the active
     persona's config (its at-a-glance KPIs + primary actions) over the shared
     dashboard data, plus a recent-activity panel. Personas without a bespoke deck
     (Developer, Manager, Product) land here, so adding a persona needs only config. -->
<script lang="ts">
  import Icon from '$lib/Icon.svelte';
  import Badge from '$lib/Badge.svelte';
  import RelativeTime from '$lib/RelativeTime.svelte';
  import { statusTone } from '$lib/badge';
  import { persona, personaConfig, actionsFor } from '$lib/persona';
  import { user } from '$lib/session';
  import { personaHomeStats, DEFAULT_HOME_STATS, type DashboardData } from '$lib/dashboard';
  import { appHref } from '$lib/paths';

  let { data }: { data: DashboardData } = $props();

  const cfg = $derived(personaConfig($persona));
  // Gate the primary actions by the signed-in role, like the header nav.
  const actions = $derived(actionsFor($persona, $user?.role));
  // The at-a-glance tiles are the persona's chosen questions over the shared data.
  const tiles = $derived(personaHomeStats(cfg.homeStats ?? DEFAULT_HOME_STATS, data));
  // Recent decisions — the workspace's live activity, newest first.
  const recent = $derived(
    [...data.decisions].sort((a, b) => b.started_at.localeCompare(a.started_at)).slice(0, 6)
  );
</script>

<main data-testid="persona-home">
  <header class="intro">
    <p class="role">Viewing as</p>
    <h1>{cfg.label}</h1>
    <p class="blurb">{cfg.blurb}</p>
  </header>

  <section class="at-a-glance" aria-label="At a glance">
    {#each tiles as t (t.id)}
      <a class="tile" href={appHref(t.href)}>
        <span class="n">{t.value}</span>
        <span class="k">{t.label}</span>
        {#if t.sub}<span class="sub">{t.sub}</span>{/if}
      </a>
    {/each}
  </section>

  <div class="cols">
    <section aria-label="Start here">
      <h2>Start here</h2>
      <div class="actions">
        {#each actions as a (a.href)}
          {@const external = a.href.startsWith('http')}
          <a
            class="action"
            href={appHref(a.href)}
            target={external ? '_blank' : undefined}
            rel={external ? 'noreferrer noopener' : undefined}
          >
            <span class="ico"><Icon name={a.icon} size={20} /></span>
            <span class="lbl">{a.label}</span>
            <span class="go">{external ? '↗' : ''}<Icon name="chevron-right" size={16} /></span>
          </a>
        {/each}
      </div>
    </section>

    <section aria-label="Recent activity">
      <h2>Recent decisions</h2>
      {#if recent.length === 0}
        <p class="empty">No decisions yet — run one from a flow.</p>
      {:else}
        <ul class="recent">
          {#each recent as d (d.decision_id)}
            <li>
              <Badge tone={statusTone(d.status)}>{d.status}</Badge>
              <a class="slug" href={appHref(`/decisions/${d.decision_id}`)}>{d.slug}</a>
              <span class="env">{d.environment}</span>
              <span class="when"><RelativeTime value={d.started_at} /></span>
            </li>
          {/each}
        </ul>
        <a class="more" href={appHref('/decisions')}>All decisions →</a>
      {/if}
    </section>
  </div>
</main>

<style>
  main {
    max-width: 64rem;
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
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(11rem, 1fr));
    gap: 0.8rem;
    margin: 1.2rem 0 2rem;
  }
  .tile {
    display: flex;
    flex-direction: column;
    gap: 0.1rem;
    padding: 0.9rem 1.1rem;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--surface);
    box-shadow: var(--shadow);
    color: var(--fg);
    transition: border-color 0.15s ease;
  }
  .tile:hover {
    border-color: var(--accent);
    text-decoration: none;
  }
  .tile .n {
    font-size: 1.7rem;
    font-weight: 650;
    font-variant-numeric: tabular-nums;
    line-height: 1.1;
  }
  .tile .k {
    font-size: 0.8rem;
    color: var(--fg-subtle);
  }
  .tile .sub {
    font-size: 0.74rem;
    color: var(--fg-muted);
    margin-top: 0.15rem;
  }
  .cols {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 1.5rem;
  }
  @media (max-width: 760px) {
    .cols {
      grid-template-columns: 1fr;
    }
  }
  h2 {
    font-size: 0.95rem;
    margin: 0 0 0.6rem;
  }
  .actions {
    display: flex;
    flex-direction: column;
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
    box-shadow: var(--shadow);
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
  }
  .recent {
    list-style: none;
    margin: 0;
    padding: 0;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--surface);
    box-shadow: var(--shadow);
  }
  .recent li {
    display: flex;
    align-items: center;
    gap: 0.6rem;
    padding: 0.5rem 0.8rem;
    border-bottom: 1px solid var(--border);
    font-size: 0.88rem;
  }
  .recent li:last-child {
    border-bottom: none;
  }
  .recent .slug {
    font-weight: 500;
    flex: 1;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .recent .env {
    color: var(--fg-subtle);
    font-size: 0.78rem;
  }
  .recent .when {
    color: var(--fg-subtle);
    font-size: 0.78rem;
    white-space: nowrap;
  }
  .more {
    display: inline-block;
    margin-top: 0.6rem;
    font-size: 0.85rem;
  }
  .empty {
    color: var(--fg-subtle);
    font-size: 0.9rem;
  }
</style>
