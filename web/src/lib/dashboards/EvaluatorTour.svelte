<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Evaluator / Guest persona: a guided first look. An ordered walkthrough of the
     platform's core loop, each step linking to the live (sandbox) surface — so a
     prospect can try the product, not just read about it. All links go to real,
     API-backed pages; nothing here is a mock. -->
<script lang="ts">
  import Icon from '$lib/Icon.svelte';
  import { decisionStats, type DashboardData } from '$lib/dashboard';
  import { appHref } from '$lib/paths';

  let { data }: { data: DashboardData } = $props();
  const ds = $derived(decisionStats(data.decisions));

  const steps = [
    {
      icon: 'engine',
      title: 'Design a decision flow',
      desc: 'Open the visual builder and wire up rules, scorecards, decision tables, and AI nodes into a versioned flow.',
      href: '/engine',
      cta: 'Open the builder'
    },
    {
      icon: 'play',
      title: 'Run a decision',
      desc: 'Publish a version to the sandbox and decide an input — straight-through or referred to a human.',
      href: '/engine',
      cta: 'Test-run a flow'
    },
    {
      icon: 'diagram',
      title: 'Inspect the trace',
      desc: 'Every decision is event-sourced and replayable: see the node-by-node path, outputs, and reason codes.',
      href: '/decisions',
      cta: 'Browse decisions'
    },
    {
      icon: 'cases',
      title: 'Review an escalation',
      desc: 'When judgement is needed a case opens with an SLA and an immutable activity trail.',
      href: '/cases',
      cta: 'See the case queue'
    }
  ];
</script>

<main data-testid="evaluator-tour">
  <header class="intro">
    <span class="eyebrow">Guided tour</span>
    <h1>See intraktible in four steps</h1>
    <p class="lede">
      You're in a live sandbox. Walk the core loop — design, decide, explain, review — on real,
      API-backed screens.{#if ds.total > 0}
        There {ds.total === 1 ? 'is' : 'are'} already <b>{ds.total}</b>
        decision{ds.total === 1 ? '' : 's'} recorded to explore.{/if}
    </p>
  </header>

  <ol class="steps">
    {#each steps as s, i (s.title)}
      <li class="step">
        <span class="num">{i + 1}</span>
        <span class="s-icon"><Icon name={s.icon} size={20} /></span>
        <div class="body">
          <h2>{s.title}</h2>
          <p>{s.desc}</p>
          <a class="cta" href={appHref(s.href)}>{s.cta} <Icon name="chevron-down" size={14} /></a>
        </div>
      </li>
    {/each}
  </ol>

  <p class="foot">
    Prefer the numbers? Switch to the <b>Executive</b> view from the account menu for KPIs and trends.
  </p>
</main>

<style>
  main {
    max-width: 52rem;
    margin: 2.5rem auto;
    padding: 0 1.25rem;
  }
  .intro {
    margin-bottom: 1.8rem;
  }
  .eyebrow {
    font-family: var(--font-mono);
    font-size: 0.72rem;
    text-transform: uppercase;
    letter-spacing: 0.12em;
    color: var(--accent-ink, var(--accent));
  }
  h1 {
    margin: 0.3rem 0 0.4rem;
  }
  .lede {
    margin: 0;
    color: var(--fg-muted);
    font-size: 1.05rem;
    line-height: 1.5;
    max-width: 38rem;
  }
  .steps {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 0.8rem;
  }
  .step {
    display: grid;
    grid-template-columns: auto auto 1fr;
    align-items: start;
    gap: 0.9rem;
    padding: 1.1rem 1.2rem;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--surface);
  }
  .num {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 1.7rem;
    height: 1.7rem;
    border-radius: 999px;
    font-weight: 650;
    font-variant-numeric: tabular-nums;
    color: var(--on-accent);
    background: linear-gradient(135deg, var(--accent), var(--accent-2));
  }
  .s-icon {
    display: inline-flex;
    color: var(--accent-ink, var(--accent));
    margin-top: 0.1rem;
  }
  .body h2 {
    font-size: 1.1rem;
    margin: 0 0 0.25rem;
  }
  .body p {
    margin: 0 0 0.6rem;
    color: var(--fg-muted);
    font-size: 0.95rem;
    line-height: 1.45;
  }
  .cta {
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
    font-weight: 550;
    color: var(--link, var(--accent-ink));
  }
  .cta :global(svg) {
    transform: rotate(-90deg);
  }
  .foot {
    margin-top: 2rem;
    color: var(--fg-subtle);
    font-size: 0.9rem;
  }
</style>
