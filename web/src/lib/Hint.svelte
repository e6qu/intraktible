<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  A discreet inline help affordance: a small circled "i" that toggles a short popover
  explaining a term or control in place — without the verbosity of a doc page. For a
  page-level overview use the PageGuide ("?" in the header); use Hint for spot
  explanations (jargon like "disposition", or "what does this control do").
  Usage: <Hint label="Disposition">The outcome: approve, decline, or refer.</Hint>
-->
<script lang="ts">
  import type { Snippet } from 'svelte';
  import { tick } from 'svelte';

  let { label, children }: { label: string; children: Snippet } = $props();
  let open = $state(false);
  let btn: HTMLButtonElement | undefined = $state();
  let pop: HTMLDivElement | undefined = $state();

  async function toggle(): Promise<void> {
    open = !open;
    if (open) {
      await tick();
      pop?.focus();
    }
  }
  function onKey(e: KeyboardEvent): void {
    if (e.key === 'Escape' && open) {
      open = false;
      btn?.focus();
    }
  }
  function onDocClick(e: MouseEvent): void {
    if (!open) return;
    const t = e.target as Node;
    if (btn?.contains(t) || pop?.contains(t)) return;
    open = false;
  }
</script>

<svelte:document onclick={onDocClick} />
<svelte:window onkeydown={onKey} />

<span class="hint">
  <button
    bind:this={btn}
    type="button"
    class="hint-btn"
    aria-expanded={open}
    aria-label="About {label}"
    onclick={toggle}>i</button
  >
  {#if open}
    <div bind:this={pop} class="hint-pop" role="note" tabindex="-1">
      <strong>{label}</strong>
      <span>{@render children()}</span>
    </div>
  {/if}
</span>

<style>
  .hint {
    position: relative;
    display: inline-flex;
    vertical-align: middle;
  }
  .hint-btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 1.05rem;
    height: 1.05rem;
    padding: 0;
    border-radius: 999px;
    border: 1px solid var(--border);
    background: var(--surface-2);
    color: var(--fg-muted);
    font-size: 0.7rem;
    font-style: italic;
    font-weight: 600;
    line-height: 1;
    cursor: pointer;
  }
  .hint-btn:hover {
    color: var(--fg);
    border-color: color-mix(in srgb, var(--accent) 45%, var(--border));
  }
  .hint-pop {
    position: absolute;
    top: calc(100% + 0.35rem);
    left: 50%;
    transform: translateX(-50%);
    z-index: 40;
    width: max-content;
    max-width: 18rem;
    padding: 0.6rem 0.75rem;
    border-radius: 8px;
    border: 1px solid var(--border);
    background: var(--surface);
    box-shadow: 0 8px 24px rgb(0 0 0 / 0.18);
    font-size: 0.82rem;
    font-weight: 400;
    line-height: 1.45;
    color: var(--fg-muted);
    text-align: left;
    white-space: normal;
    /* Reset inherited heading styling (Hint often sits inside an <h2>). */
    text-transform: none;
    letter-spacing: normal;
  }
  .hint-pop strong {
    display: block;
    color: var(--fg);
    font-size: 0.8rem;
    margin-bottom: 0.15rem;
  }
</style>
