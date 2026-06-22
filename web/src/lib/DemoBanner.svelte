<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  A slim strip shown only in the public GitHub Pages demo. Rendered behind a static
  `import.meta.env.VITE_DEMO` guard, so the whole component is dead-code-eliminated
  from the embedded production build (the {#if false} block is dropped at build).
  It tells the visitor the backend is an in-browser mock: everything is interactive,
  but state lives in memory and resets on reload.
-->
<script lang="ts">
  const demo = import.meta.env.VITE_DEMO;
</script>

{#if demo}
  <div class="demo-strip" role="note">
    <span class="dot" aria-hidden="true"></span>
    <span class="msg">
      <b>Live demo.</b> Fully interactive, running entirely in your browser — no backend. Data is in-memory
      and resets on reload.
    </span>
    <a
      class="src"
      href="https://github.com/e6qu/intraktible"
      target="_blank"
      rel="noreferrer noopener">Source ↗</a
    >
  </div>
{/if}

<style>
  .demo-strip {
    display: flex;
    align-items: center;
    gap: 0.55rem;
    padding: 0.4rem 1rem;
    font-size: 0.82rem;
    background: var(--accent, #3b5bdb);
    color: #fff;
  }
  .dot {
    width: 0.55rem;
    height: 0.55rem;
    border-radius: 999px;
    background: #fff;
    box-shadow: 0 0 0 0 rgba(255, 255, 255, 0.7);
    animation: pulse 2.2s ease-out infinite;
    flex: none;
  }
  @media (prefers-reduced-motion: reduce) {
    .dot {
      animation: none;
    }
  }
  @keyframes pulse {
    0% {
      box-shadow: 0 0 0 0 rgba(255, 255, 255, 0.6);
    }
    70% {
      box-shadow: 0 0 0 0.5rem rgba(255, 255, 255, 0);
    }
    100% {
      box-shadow: 0 0 0 0 rgba(255, 255, 255, 0);
    }
  }
  .msg {
    flex: 1;
    min-width: 0;
  }
  .msg b {
    font-weight: 600;
  }
  .src {
    color: #fff;
    text-decoration: underline;
    white-space: nowrap;
    font-weight: 500;
  }
</style>
