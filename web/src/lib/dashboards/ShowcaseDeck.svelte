<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Showcase persona: the Story. Editorial, marketing-grade view for stakeholders
     and executives — a serif hero, oversized count-up headline metrics, and a
     narrative of the platform's surfaces. Atmosphere + staggered reveals. -->
<script lang="ts">
  import Icon from '$lib/Icon.svelte';
  import {
    decisionStats,
    deployStats,
    decisionsByDay,
    pct,
    type DashboardData
  } from '$lib/dashboard';

  let { data }: { data: DashboardData } = $props();

  const ds = $derived(decisionStats(data.decisions));
  const dep = $derived(deployStats(data.flows));
  const trend = $derived(decisionsByDay(data.decisions));
  const trendMax = $derived(Math.max(1, ...trend.map((t) => t.count)));

  // Headline metrics — the numbers a stakeholder remembers.
  const metrics = $derived([
    {
      to: ds.total,
      fmt: (n: number) => Math.round(n).toLocaleString(),
      label: 'Decisions made',
      sub: 'event-sourced & replayable'
    },
    {
      to: ds.completionRate * 100,
      fmt: (n: number) => `${Math.round(n)}%`,
      label: 'Straight-through',
      sub: 'completed without error'
    },
    {
      to: ds.p50Ms,
      fmt: (n: number) => `${Math.round(n)}ms`,
      label: 'Median latency',
      sub: 'p95 ' + ds.p95Ms + 'ms'
    },
    {
      to: dep.live,
      fmt: (n: number) => String(Math.round(n)),
      label: 'Flows in production',
      sub: 'governed by four-eyes'
    }
  ]);

  const surfaces = [
    {
      icon: 'engine',
      title: 'Decide',
      desc: 'Versioned flows on a visual canvas — deployed, A/B-tested, and backtested before they ever touch production.'
    },
    {
      icon: 'database',
      title: 'Contextualise',
      desc: 'Real-time features and connectors feed every decision the freshest view of each entity.'
    },
    {
      icon: 'cases',
      title: 'Review',
      desc: 'When judgement is needed, a case opens with full SLA tracking and an immutable trail.'
    },
    {
      icon: 'agents',
      title: 'Reason',
      desc: 'AI agents with tools and structured output, every run recorded and replayable.'
    }
  ];

  // Count-up on mount; snaps to the final value under reduced-motion.
  function countUp(node: HTMLElement, params: { to: number; fmt: (n: number) => string }) {
    let raf = 0;
    function run(p: { to: number; fmt: (n: number) => string }) {
      const reduce =
        typeof window !== 'undefined' &&
        window.matchMedia?.('(prefers-reduced-motion: reduce)').matches;
      if (reduce || p.to === 0) {
        node.textContent = p.fmt(p.to);
        return;
      }
      const dur = 1100;
      const start = performance.now();
      const step = (now: number) => {
        const t = Math.min(1, (now - start) / dur);
        const eased = 1 - Math.pow(1 - t, 3);
        node.textContent = p.fmt(p.to * eased);
        if (t < 1) raf = requestAnimationFrame(step);
      };
      raf = requestAnimationFrame(step);
    }
    run(params);
    return {
      update(p: { to: number; fmt: (n: number) => string }) {
        cancelAnimationFrame(raf);
        run(p);
      },
      destroy() {
        cancelAnimationFrame(raf);
      }
    };
  }
</script>

<div class="atmosphere">
  <main class="story">
    <section class="hero">
      <span class="eyebrow">Agentic decision platform</span>
      <h1>Every decision, <em>accountable</em>.</h1>
      <p class="lede">
        intraktible turns automated judgement into a system of record. Each determination is
        versioned, explainable, and replayable — so you can move fast and still answer for it.
      </p>
    </section>

    <section class="metrics">
      {#each metrics as m, i (m.label)}
        <figure class="metric" style="--i:{i}">
          <span class="value" use:countUp={{ to: m.to, fmt: m.fmt }}>{m.fmt(0)}</span>
          <figcaption>
            <span class="m-label">{m.label}</span>
            <span class="m-sub">{m.sub}</span>
          </figcaption>
        </figure>
      {/each}
    </section>

    {#if trend.length > 0}
      <section class="trend" data-testid="exec-trend" style="--i:4">
        <div class="trend-head">
          <h2>Decision volume</h2>
          <p class="gov">
            <b>{dep.live}</b> in production · <b>{dep.pending}</b> awaiting four-eyes approval
          </p>
        </div>
        <div class="bars" role="img" aria-label="Decisions per day">
          {#each trend as t (t.day)}
            <div class="bar-col" title={`${t.day}: ${t.count}`}>
              <div class="bar" style="height:{Math.round((t.count / trendMax) * 100)}%"></div>
            </div>
          {/each}
        </div>
      </section>
    {/if}

    <section class="surfaces">
      {#each surfaces as s, i (s.title)}
        <article class="surface" style="--i:{i + 4}">
          <span class="s-icon"><Icon name={s.icon} size={20} /></span>
          <h3>{s.title}</h3>
          <p>{s.desc}</p>
        </article>
      {/each}
    </section>

    <p class="foot">
      Self-hosted · {data.flows.length} flow{data.flows.length === 1 ? '' : 's'} ·
      {pct(ds.completionRate)} success rate · open source under AGPL-3.0
    </p>
  </main>
</div>

<style>
  .atmosphere {
    position: relative;
    overflow: hidden;
    background:
      radial-gradient(
        60rem 40rem at 80% -10%,
        color-mix(in srgb, var(--accent) 14%, transparent),
        transparent 60%
      ),
      radial-gradient(
        50rem 36rem at -10% 10%,
        color-mix(in srgb, var(--accent-ink) 12%, transparent),
        transparent 55%
      );
  }
  /* Fine grain overlay for editorial texture. */
  .atmosphere::after {
    content: '';
    position: absolute;
    inset: 0;
    pointer-events: none;
    opacity: 0.4;
    background-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='120' height='120'%3E%3Cfilter id='n'%3E%3CfeTurbulence type='fractalNoise' baseFrequency='0.8' numOctaves='2' stitchTiles='stitch'/%3E%3C/filter%3E%3Crect width='100%25' height='100%25' filter='url(%23n)' opacity='0.035'/%3E%3C/svg%3E");
  }
  .story {
    position: relative;
    z-index: 1;
    max-width: 70rem;
    margin: 0 auto;
    padding: 4rem 1.5rem 5rem;
  }
  .hero {
    max-width: 46rem;
  }
  .eyebrow {
    display: inline-block;
    font-family: var(--font-mono);
    font-size: 0.75rem;
    text-transform: uppercase;
    letter-spacing: 0.18em;
    color: var(--accent-ink);
    margin-bottom: 1rem;
    animation: rise 0.6s ease both;
  }
  .hero h1 {
    font-size: clamp(2.6rem, 7vw, 4.6rem);
    line-height: 1;
    margin: 0;
    animation: rise 0.6s ease 0.05s both;
  }
  .hero h1 em {
    font-style: italic;
    color: var(--accent-ink);
  }
  .lede {
    font-size: clamp(1.05rem, 2vw, 1.3rem);
    line-height: 1.5;
    color: var(--fg-muted);
    margin-top: 1.3rem;
    max-width: 40rem;
    animation: rise 0.6s ease 0.12s both;
  }

  .metrics {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(11rem, 1fr));
    gap: 1.5rem 2rem;
    margin: 3.5rem 0;
    padding: 2rem 0;
    border-top: 1px solid var(--border);
    border-bottom: 1px solid var(--border);
  }
  .metric {
    margin: 0;
    animation: rise 0.6s ease both;
    animation-delay: calc(0.15s + var(--i) * 80ms);
  }
  .value {
    display: block;
    font-family: var(--font-display);
    font-weight: 600;
    font-size: clamp(2.6rem, 5vw, 3.6rem);
    line-height: 1;
    letter-spacing: -0.02em;
    color: var(--fg);
    font-variant-numeric: tabular-nums;
  }
  figcaption {
    margin-top: 0.6rem;
    display: flex;
    flex-direction: column;
  }
  .m-label {
    font-weight: 600;
    font-size: 0.98rem;
  }
  .m-sub {
    color: var(--fg-subtle);
    font-size: 0.85rem;
  }

  .trend {
    margin: 0 0 3.5rem;
    animation: rise 0.6s ease both;
    animation-delay: calc(0.15s + var(--i) * 80ms);
  }
  .trend-head {
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    justify-content: space-between;
    gap: 0.5rem;
  }
  .trend h2 {
    font-family: var(--font-display);
    font-size: 1.5rem;
    font-weight: 600;
    margin: 0;
  }
  .gov {
    margin: 0;
    color: var(--fg-muted);
    font-size: 0.95rem;
  }
  .bars {
    display: flex;
    align-items: flex-end;
    gap: 0.3rem;
    height: 7rem;
    margin-top: 1rem;
    padding-top: 0.5rem;
    border-bottom: 1px solid var(--border);
  }
  .bar-col {
    flex: 1;
    display: flex;
    align-items: flex-end;
    height: 100%;
  }
  .bar {
    width: 100%;
    min-height: 2px;
    border-radius: 3px 3px 0 0;
    background: linear-gradient(to top, var(--accent), var(--accent-2));
  }

  .surfaces {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(15rem, 1fr));
    gap: 1.3rem;
  }
  .surface {
    padding: 1.6rem;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: color-mix(in srgb, var(--surface) 80%, transparent);
    backdrop-filter: blur(4px);
    animation: rise 0.6s ease both;
    animation-delay: calc(0.15s + var(--i) * 80ms);
  }
  .s-icon {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 44px;
    height: 44px;
    border-radius: 12px;
    color: var(--on-accent);
    background: linear-gradient(135deg, var(--accent), var(--accent-2));
    margin-bottom: 0.9rem;
  }
  .surface h3 {
    font-family: var(--font-display);
    font-size: 1.5rem;
    margin: 0 0 0.4rem;
    font-weight: 600;
  }
  .surface p {
    margin: 0;
    color: var(--fg-muted);
    font-size: 0.95rem;
    line-height: 1.5;
  }
  .foot {
    margin-top: 3rem;
    text-align: center;
    color: var(--fg-subtle);
    font-family: var(--font-mono);
    font-size: 0.8rem;
    letter-spacing: 0.02em;
  }
  @keyframes rise {
    from {
      opacity: 0;
      transform: translateY(16px);
    }
  }
</style>
