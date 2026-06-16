<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Loading placeholder: shimmering bars that hold the layout while data loads,
     instead of a blank flash. `rows` bars at `height`; shimmer respects
     prefers-reduced-motion (app.css disables the animation). -->
<script lang="ts">
  let { rows = 4, height = '2.4rem' }: { rows?: number; height?: string } = $props();
  const bars = $derived(Array.from({ length: rows }, (_, i) => i));
</script>

<div class="skeleton" aria-busy="true" aria-label="Loading">
  {#each bars as i (i)}
    <div class="bar" style="height:{height}"></div>
  {/each}
</div>

<style>
  .skeleton {
    display: flex;
    flex-direction: column;
    gap: 0.55rem;
    margin: 1rem 0;
  }
  .bar {
    border-radius: var(--radius-sm);
    background: linear-gradient(
      90deg,
      var(--surface-2) 25%,
      color-mix(in srgb, var(--surface-2) 40%, var(--border)) 37%,
      var(--surface-2) 63%
    );
    background-size: 400% 100%;
    animation: shimmer 1.4s ease infinite;
  }
  @keyframes shimmer {
    0% {
      background-position: 100% 0;
    }
    100% {
      background-position: 0 0;
    }
  }
</style>
