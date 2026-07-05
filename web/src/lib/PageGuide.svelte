<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Per-page guide: a right slide-over that explains the current page — what it's for,
  what you can do here, and the key flows — keyed by the route id ($page.route.id).
  A slide-over (not a centered modal) so the content reads alongside the UI it
  describes. Content lives in $lib/help/registry; pages need no per-page markup.
  Opened from the header guide button or the command palette ($lib/guide store).
-->
<script lang="ts">
  import { page } from '$app/stores';
  import Icon from '$lib/Icon.svelte';
  import { appHref } from '$lib/paths';
  import { guideOpen, closeGuide } from '$lib/guide';
  import { helpFor } from '$lib/help/registry';
  import { buildCurrentPageExport, exportFilename } from '$lib/aiexport';
  import { copyText } from '$lib/clipboard';
  import { toast } from '$lib/toast';

  const help = $derived(helpFor($page.route.id));

  // "Export for AI": the same guide content plus the page's recorded API calls
  // and a summary of what it currently shows, as one markdown document.
  async function copyForAI(): Promise<void> {
    await copyText(buildCurrentPageExport($page.route.id, $page.url.pathname), 'Copied for AI');
  }
  function downloadForAI(): void {
    const text = buildCurrentPageExport($page.route.id, $page.url.pathname);
    const url = URL.createObjectURL(new Blob([text], { type: 'text/markdown' }));
    const a = document.createElement('a');
    a.href = url;
    a.download = exportFilename($page.route.id ?? '');
    a.click();
    // Revoke on a later tick — a synchronous revoke can race the browser's blob
    // fetch and abort the download.
    setTimeout(() => URL.revokeObjectURL(url), 0);
    toast.success('Downloaded page export');
  }
  let panelEl = $state<HTMLElement | null>(null);
  let closeEl = $state<HTMLButtonElement | null>(null);
  let restoreFocusEl: HTMLElement | null = null;

  // Auto-close on navigation so the guide never shows the wrong page's content.
  let lastPath = $state($page.url.pathname);
  $effect(() => {
    if ($page.url.pathname !== lastPath) {
      lastPath = $page.url.pathname;
      closeGuide();
    }
  });

  // On open, remember the trigger and move focus into the panel; restore on close.
  $effect(() => {
    if ($guideOpen) {
      restoreFocusEl = document.activeElement as HTMLElement | null;
      queueMicrotask(() => closeEl?.focus());
    } else if (restoreFocusEl) {
      restoreFocusEl.focus();
      restoreFocusEl = null;
    }
  });

  function onKeydown(e: KeyboardEvent): void {
    if (e.key === 'Escape') {
      e.preventDefault();
      closeGuide();
      return;
    }
    if (e.key === 'Tab' && panelEl) {
      // Trap focus within the panel.
      const f = panelEl.querySelectorAll<HTMLElement>('a[href], button:not([disabled]), summary');
      if (f.length === 0) return;
      const first = f[0];
      const last = f[f.length - 1];
      if (e.shiftKey && document.activeElement === first) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && document.activeElement === last) {
        e.preventDefault();
        first.focus();
      }
    }
  }
</script>

{#if $guideOpen && help}
  <div class="g-root">
    <button class="g-backdrop" aria-label="Close guide" onclick={closeGuide}></button>
    <div
      class="g-panel"
      role="dialog"
      aria-modal="true"
      aria-labelledby="g-title"
      tabindex="-1"
      bind:this={panelEl}
      onkeydown={onKeydown}
    >
      <header class="g-head">
        <h2 id="g-title">{help.title}</h2>
        <button class="g-close" aria-label="Close guide" onclick={closeGuide} bind:this={closeEl}>
          <Icon name="plus" size={16} />
        </button>
      </header>
      <div class="g-export" role="group" aria-label="Export this page for AI">
        <button class="g-export-btn" onclick={copyForAI} data-testid="guide-copy-ai">
          <Icon name="copy" size={14} /> Copy for AI
        </button>
        <button class="g-export-btn" onclick={downloadForAI} data-testid="guide-download-ai">
          <Icon name="download" size={14} /> Download .md
        </button>
        <span class="g-export-hint">
          A machine-readable export: what this page is, the API calls behind it, and what it
          currently shows.
        </span>
      </div>
      <p class="g-summary">{help.summary}</p>

      <h3>What you can do here</h3>
      <ul class="g-caps">
        {#each help.capabilities as c (c)}<li>{c}</li>{/each}
      </ul>

      {#if help.journeys && help.journeys.length > 0}
        <h3>Flows, step by step</h3>
        {#each help.journeys as j, i (j.name)}
          <details class="g-journey" open={i === 0}>
            <summary>
              <span class="g-journey-name">{j.name}</span>
              <span class="g-journey-count">{j.steps.length} steps</span>
            </summary>
            <ol>
              {#each j.steps as s (s)}<li>{s}</li>{/each}
            </ol>
          </details>
        {/each}
      {/if}

      {#if help.links && help.links.length > 0}
        <nav class="g-links" aria-label="Related">
          {#each help.links as l (l.href)}<a href={appHref(l.href)}>{l.label} →</a>{/each}
        </nav>
      {/if}
    </div>
  </div>
{/if}

<style>
  .g-root {
    position: fixed;
    inset: 0;
    z-index: 200;
    display: flex;
    justify-content: flex-end;
  }
  .g-backdrop {
    position: absolute;
    inset: 0;
    border: none;
    cursor: pointer;
    background: color-mix(in srgb, #000 35%, transparent);
  }
  .g-panel {
    position: relative;
    width: min(26rem, 92vw);
    height: 100%;
    overflow-y: auto;
    background: var(--surface);
    border-left: 1px solid var(--border);
    box-shadow: var(--shadow, -8px 0 24px rgba(0, 0, 0, 0.18));
    padding: 1.1rem 1.25rem 2rem;
    animation: slide-in 0.16s ease;
  }
  @keyframes slide-in {
    from {
      transform: translateX(100%);
    }
    to {
      transform: translateX(0);
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .g-panel {
      animation: none;
    }
  }
  .g-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 1rem;
  }
  .g-head h2 {
    margin: 0;
    font-size: 1.1rem;
  }
  .g-close {
    display: inline-flex;
    border: 1px solid var(--border);
    background: var(--surface-2);
    color: var(--fg-muted);
    border-radius: 6px;
    padding: 0.3rem;
    cursor: pointer;
    transform: rotate(45deg);
  }
  .g-export {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.45rem;
    margin: 0.7rem 0 0.2rem;
    padding: 0.55rem 0.6rem;
    border: 1px solid color-mix(in srgb, var(--accent) 30%, var(--border));
    border-radius: var(--radius, 8px);
    background: color-mix(in srgb, var(--accent) 7%, var(--surface-2));
  }
  .g-export-btn {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    padding: 0.35rem 0.65rem;
    border: 1px solid var(--border);
    border-radius: 999px;
    background: var(--surface);
    color: var(--fg);
    font: inherit;
    font-size: 0.84rem;
    font-weight: 550;
    cursor: pointer;
  }
  .g-export-btn:hover {
    border-color: var(--accent);
  }
  .g-export-hint {
    flex-basis: 100%;
    font-size: 0.74rem;
    line-height: 1.35;
    color: var(--fg-subtle);
  }
  .g-summary {
    color: var(--fg-muted);
    margin: 0.5rem 0 1rem;
    line-height: 1.5;
  }
  h3 {
    font-size: 0.72rem;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--fg-subtle);
    margin: 1.1rem 0 0.4rem;
  }
  .g-caps {
    margin: 0;
    padding-left: 1.1rem;
    display: flex;
    flex-direction: column;
    gap: 0.3rem;
    line-height: 1.4;
  }
  .g-journey {
    border: 1px solid var(--border);
    border-radius: var(--radius, 8px);
    padding: 0.4rem 0.7rem;
    margin-bottom: 0.4rem;
    background: var(--surface-2);
  }
  .g-journey summary {
    cursor: pointer;
    font-weight: 550;
    font-size: 0.9rem;
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 0.6rem;
  }
  .g-journey-count {
    flex-shrink: 0;
    font-weight: 400;
    font-size: 0.75rem;
    color: var(--fg-subtle);
  }
  .g-journey ol {
    list-style: none;
    counter-reset: step;
    margin: 0.6rem 0 0.3rem;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 0.45rem;
    color: var(--fg-muted);
    line-height: 1.4;
  }
  .g-journey li {
    counter-increment: step;
    display: flex;
    gap: 0.55rem;
  }
  .g-journey li::before {
    content: counter(step);
    flex-shrink: 0;
    width: 1.25rem;
    height: 1.25rem;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    border: 1px solid var(--border);
    border-radius: 50%;
    background: var(--surface);
    color: var(--fg-subtle);
    font-size: 0.7rem;
    font-weight: 600;
    margin-top: 0.1rem;
  }
  .g-links {
    display: flex;
    flex-direction: column;
    gap: 0.3rem;
    margin-top: 1rem;
  }
  .g-links a {
    font-size: 0.9rem;
  }
</style>
