<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Signed-out landing. No tenant data is readable yet, so this is the narrative
     entry point: what the platform is, the four surfaces, and a sign-in CTA. -->
<script lang="ts">
  import Icon from '$lib/Icon.svelte';
  import { appHref } from '$lib/paths';

  // A signed-out visitor can't read any of these surfaces (they'd 401), so every card
  // routes to sign-in rather than dead-ending on a protected route. The icon/title/desc
  // still narrate what each surface is.
  const surfaces = [
    {
      icon: 'engine',
      title: 'Decision Engine',
      desc: 'Versioned decision flows on a visual canvas — deploy, A/B, backtest, export.'
    },
    {
      icon: 'database',
      title: 'Context Layer',
      desc: 'Entities, windowed features, and connectors the flows decide on.'
    },
    {
      icon: 'cases',
      title: 'Case Manager',
      desc: 'Human-in-the-loop review with SLA tracking and a full audit trail.'
    },
    {
      icon: 'agents',
      title: 'Agent Manager',
      desc: 'AI agents with tools and structured output — run, monitor, escalate.'
    }
  ];
</script>

<main>
  <section class="hero">
    <span class="eyebrow">Open-source agentic decision platform</span>
    <h1>Decisions you can <em>replay</em>, govern, and trust.</h1>
    <p class="tagline">
      intraktible is an event-sourced decision platform — every automated determination is a
      versioned, replayable record. Self-hosted, deterministic, auditable end to end.
    </p>
    <p class="cta"><a class="signin" href={appHref('/login')}>Sign in to continue →</a></p>
  </section>

  <section class="cards">
    {#each surfaces as c, i (c.title)}
      <a class="card" href={appHref('/login')} style="--i:{i}">
        <span class="cardicon"><Icon name={c.icon} size={22} /></span>
        <span class="cardtitle">{c.title}</span>
        <span class="carddesc">{c.desc}</span>
      </a>
    {/each}
  </section>
</main>

<style>
  main {
    max-width: 64rem;
    margin: 0 auto;
    padding: 3.5rem 1.25rem 4rem;
  }
  .hero {
    max-width: 44rem;
  }
  .eyebrow {
    display: inline-block;
    font-size: 0.78rem;
    text-transform: uppercase;
    letter-spacing: 0.12em;
    color: var(--accent-ink);
    font-weight: 600;
    margin-bottom: 0.9rem;
  }
  .hero h1 {
    font-size: clamp(2rem, 5vw, 3.2rem);
    line-height: 1.05;
    margin: 0;
  }
  .hero h1 em {
    font-style: italic;
    color: var(--accent-ink);
  }
  .tagline {
    color: var(--fg-muted);
    font-size: 1.1rem;
    margin-top: 1rem;
    max-width: 38rem;
  }
  .cta {
    margin-top: 1.5rem;
  }
  .signin {
    display: inline-flex;
    align-items: center;
    padding: 0.6rem 1.1rem;
    border-radius: var(--radius);
    background: var(--accent);
    color: var(--on-accent);
    font-weight: 600;
  }
  .signin:hover {
    background: var(--accent-2);
    text-decoration: none;
  }
  .cards {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(13rem, 1fr));
    gap: 1rem;
    margin-top: 3rem;
  }
  .card {
    display: flex;
    flex-direction: column;
    gap: 0.4rem;
    padding: var(--pad-card);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--surface);
    box-shadow: var(--shadow);
    color: var(--fg);
    transition:
      border-color 0.15s ease,
      transform 0.15s ease;
    animation: rise 0.5s cubic-bezier(0.2, 0.7, 0.2, 1) both;
    animation-delay: calc(var(--i) * 70ms);
  }
  .card:hover {
    text-decoration: none;
    border-color: var(--accent);
    transform: translateY(-3px);
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
  @keyframes rise {
    from {
      opacity: 0;
      transform: translateY(12px);
    }
  }
</style>
