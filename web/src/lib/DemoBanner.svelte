<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  A slim strip shown only in the public GitHub Pages demo. Rendered behind a static
  `import.meta.env.VITE_DEMO` guard, so the whole component is dead-code-eliminated
  from the embedded production build (the {#if false} block is dropped at build).
  It tells the visitor the backend is an in-browser mock (interactive + persisted to
  localStorage), offers an identity switcher so they can act AS different roles (the
  role-gated nav/surfaces change live), and a Reset that clears local state.

  The controls read window.__demo (set by the demo install) rather than importing the
  demo code, so this always-compiled component pulls no demo modules into the normal
  bundle. Colours use var(--on-accent) (not a hardcoded white) so the strip stays
  readable on every persona accent in both themes.
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import { refreshUser } from '$lib/session';

  const demo = import.meta.env.VITE_DEMO;

  type DemoUser = { actor: string; name: string; role: string; title: string };
  type DemoControl = {
    users: DemoUser[];
    current(): string;
    setUser(actor: string): void;
    reset(): void;
  };

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

  function reset(): void {
    const c = control();
    if (!c) return;
    if (!confirm('Reset the demo? This clears all local changes and restores the seed.')) return;
    c.reset();
    location.reload();
  }
</script>

{#if demo}
  <div class="demo-strip" role="note">
    <span class="dot" aria-hidden="true"></span>
    <span class="msg">
      <b>Live demo.</b> Fully interactive, in your browser — no backend. Changes are saved locally and
      persist across reloads.
    </span>
    {#if users.length > 0}
      <label class="who">
        <span class="who-label">Signed in as</span>
        <select
          aria-label="Signed in as (switch demo user)"
          value={currentActor}
          onchange={(e) => switchUser(e.currentTarget.value)}
        >
          {#each users as u (u.actor)}
            <option value={u.actor}>{u.name} · {u.role}</option>
          {/each}
        </select>
      </label>
      <button class="reset" type="button" onclick={reset}>Reset</button>
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
    color: var(--on-accent, #fff);
  }
  .dot {
    width: 0.55rem;
    height: 0.55rem;
    border-radius: 999px;
    background: var(--on-accent, #fff);
    box-shadow: 0 0 0 0 color-mix(in srgb, var(--on-accent, #fff) 70%, transparent);
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
      box-shadow: 0 0 0 0 color-mix(in srgb, var(--on-accent, #fff) 60%, transparent);
    }
    70% {
      box-shadow: 0 0 0 0.5rem transparent;
    }
    100% {
      box-shadow: 0 0 0 0 transparent;
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
    border: 1px solid color-mix(in srgb, var(--on-accent, #fff) 50%, transparent);
    background: color-mix(in srgb, var(--on-accent, #fff) 12%, transparent);
    color: var(--on-accent, #fff);
  }
  .who select option {
    color: #1a1d23;
  }
  .reset {
    font: inherit;
    font-size: 0.78rem;
    padding: 0.15rem 0.6rem;
    border-radius: 999px;
    cursor: pointer;
    white-space: nowrap;
    border: 1px solid color-mix(in srgb, var(--on-accent, #fff) 55%, transparent);
    background: transparent;
    color: var(--on-accent, #fff);
  }
  .reset:hover {
    background: color-mix(in srgb, var(--on-accent, #fff) 15%, transparent);
  }
  .src {
    color: var(--on-accent, #fff);
    text-decoration: underline;
    white-space: nowrap;
    font-weight: 500;
  }
</style>
