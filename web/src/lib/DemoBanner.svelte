<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  A slim strip shown only in the public GitHub Pages demo. Rendered behind a static
  `import.meta.env.VITE_DEMO` guard, so the whole component is dead-code-eliminated
  from the embedded production build (the {#if false} block is dropped at build).
  It tells the visitor the backend is an in-browser mock (interactive, but in-memory)
  and offers an identity switcher so they can view the app AS different roles — the
  role-gated nav/surfaces (admin-only Model risk / Audit, etc.) change live.

  The switcher reads window.__demo (set by the demo install) rather than importing
  the demo code, so this always-compiled component pulls no demo modules into the
  normal bundle.
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import { refreshUser } from '$lib/session';

  const demo = import.meta.env.VITE_DEMO;

  type DemoUser = { actor: string; name: string; role: string; title: string };
  type DemoControl = { users: DemoUser[]; current(): string; setUser(actor: string): void };

  let users = $state<DemoUser[]>([]);
  let currentActor = $state('');

  function control(): DemoControl | undefined {
    return (window as unknown as { __demo?: DemoControl }).__demo;
  }

  onMount(() => {
    const c = control();
    if (c) {
      users = c.users;
      currentActor = c.current();
    }
  });

  async function switchUser(actor: string): Promise<void> {
    const c = control();
    if (!c) return;
    c.setUser(actor);
    currentActor = actor;
    // Re-pull /v1/me so the app's $user store (and role-gated nav) updates live.
    await refreshUser();
  }
</script>

{#if demo}
  <div class="demo-strip" role="note">
    <span class="dot" aria-hidden="true"></span>
    <span class="msg">
      <b>Live demo.</b> Fully interactive, in your browser — no backend. Data is in-memory and resets
      on reload.
    </span>
    {#if users.length > 0}
      <label class="who">
        <span class="who-label">Viewing as</span>
        <select
          aria-label="Viewing as (switch role)"
          value={currentActor}
          onchange={(e) => switchUser(e.currentTarget.value)}
        >
          {#each users as u (u.actor)}
            <option value={u.actor}>{u.name} · {u.role}</option>
          {/each}
        </select>
      </label>
    {/if}
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
    gap: 0.7rem;
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
  .who {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    white-space: nowrap;
  }
  .who-label {
    opacity: 0.85;
  }
  .who select {
    font: inherit;
    font-size: 0.8rem;
    padding: 0.1rem 0.35rem;
    border-radius: 4px;
    border: 1px solid rgba(255, 255, 255, 0.5);
    background: rgba(255, 255, 255, 0.12);
    color: #fff;
  }
  .who select option {
    color: #1a1d23;
  }
  .src {
    color: #fff;
    text-decoration: underline;
    white-space: nowrap;
    font-weight: 500;
  }
</style>
