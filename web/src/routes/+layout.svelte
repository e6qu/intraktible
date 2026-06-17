<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Root layout: global stylesheet, the app header (brand + nav + theme toggle),
     and a slot for the routed page. -->
<script lang="ts">
  import '../app.css';
  import { onMount } from 'svelte';
  import { page } from '$app/stores';
  import Icon from '$lib/Icon.svelte';
  import { initTheme, toggleTheme, theme as themeStore } from '$lib/theme';
  import { initPersona, setPersona, persona as personaStore, PERSONAS } from '$lib/persona';
  import { user, refreshUser, signOut } from '$lib/session';
  import { openPalette } from '$lib/palette';
  import Toasts from '$lib/Toasts.svelte';
  import CommandPalette from '$lib/CommandPalette.svelte';
  import ShortcutsOverlay from '$lib/ShortcutsOverlay.svelte';

  let { children } = $props();
  let theme = $state<'light' | 'dark'>('light');
  let persona = $state(initPersona());

  onMount(() => {
    theme = initTheme();
    initPersona();
    void refreshUser();
    const unsubTheme = themeStore.subscribe((t) => (theme = t));
    const unsubPersona = personaStore.subscribe((p) => (persona = p));
    return () => {
      unsubTheme();
      unsubPersona();
    };
  });

  const nav = [
    { href: '/engine', label: 'Engine', icon: 'engine' },
    { href: '/policies', label: 'Policies', icon: 'rule' },
    { href: '/decisions', label: 'Decisions', icon: 'diagram' },
    { href: '/data', label: 'Data', icon: 'database' },
    { href: '/cases', label: 'Cases', icon: 'cases' },
    { href: '/agents', label: 'Agents', icon: 'agents' },
    { href: '/audit', label: 'Audit', icon: 'shield' }
  ];

  const path = $derived($page.url.pathname);
  function active(href: string): boolean {
    return path === href || path.startsWith(href + '/');
  }

  const currentPersona = $derived(PERSONAS.find((p) => p.id === persona) ?? PERSONAS[0]);
  let personaEl = $state<HTMLDetailsElement | null>(null);
  let menuEl = $state<HTMLDivElement | null>(null);

  function choose(id: typeof persona): void {
    setPersona(id);
    closeMenu();
  }
  function closeMenu(): void {
    if (personaEl) {
      personaEl.open = false;
      personaEl.querySelector<HTMLElement>('summary')?.focus();
    }
  }
  // Roving focus across the option buttons (ARIA menu keyboard pattern).
  function menuKeydown(e: KeyboardEvent): void {
    const opts = menuEl
      ? Array.from(menuEl.querySelectorAll<HTMLButtonElement>('.persona-opt'))
      : [];
    if (opts.length === 0) return;
    const i = opts.indexOf(document.activeElement as HTMLButtonElement);
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      opts[(i + 1) % opts.length]?.focus();
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      opts[(i - 1 + opts.length) % opts.length]?.focus();
    } else if (e.key === 'Home') {
      e.preventDefault();
      opts[0]?.focus();
    } else if (e.key === 'End') {
      e.preventDefault();
      opts.at(-1)?.focus();
    } else if (e.key === 'Escape') {
      e.preventDefault();
      closeMenu();
    }
  }
  // On open, move focus to the active option so arrow keys work immediately.
  function onPersonaToggle(): void {
    if (personaEl?.open) {
      menuEl?.querySelector<HTMLButtonElement>('.persona-opt.on')?.focus();
    }
  }
  // Close the persona menu on an outside click (details has no native dismiss).
  $effect(() => {
    function onDocClick(e: MouseEvent): void {
      if (personaEl?.open && !personaEl.contains(e.target as Node)) personaEl.open = false;
    }
    document.addEventListener('click', onDocClick);
    return () => document.removeEventListener('click', onDocClick);
  });
</script>

<a class="skip-link" href="#main">Skip to content</a>
<header>
  <a class="brand" href="/">
    <span class="mark"><Icon name="logo" size={20} /></span>
    <span class="wordmark">intraktible</span>
  </a>
  <nav aria-label="Primary">
    {#each nav as item (item.href)}
      <a
        href={item.href}
        class="navlink"
        class:active={active(item.href)}
        aria-current={active(item.href) ? 'page' : undefined}
      >
        <Icon name={item.icon} size={16} />
        <span class="navlabel">{item.label}</span>
      </a>
    {/each}
  </nav>
  <span class="auth" data-testid="auth-status">
    {#if $user}
      <span class="who">Signed in as <b>{$user.actor}</b></span>
      <button class="ghost" onclick={signOut} aria-label="Sign out">
        <Icon name="signout" size={14} /> <span class="signout-label">Sign out</span>
      </button>
    {:else}
      <span class="who muted">Not signed in</span>
      <a class="navlink" href="/login">Sign in</a>
    {/if}
  </span>
  <button
    class="cmdk"
    onclick={openPalette}
    aria-label="Open command palette"
    title="Command palette (⌘K)"
    data-testid="cmdk-trigger"
  >
    <Icon name="search" size={14} />
    <span class="cmdk-label">Search</span>
    <kbd>⌘K</kbd>
  </button>
  <details
    class="persona"
    bind:this={personaEl}
    data-testid="persona-switch"
    ontoggle={onPersonaToggle}
  >
    <summary
      class="persona-trigger"
      title="Switch view — {currentPersona.blurb}"
      aria-haspopup="menu"
      aria-label="Switch view persona — current: {currentPersona.label}"
    >
      <span class="avatar"><Icon name={currentPersona.id} size={16} /></span>
      <span class="persona-name">{currentPersona.label}</span>
      <span class="caret"><Icon name="chevron-down" size={13} /></span>
    </summary>
    <div
      class="persona-menu"
      role="menu"
      aria-label="View persona"
      tabindex="-1"
      bind:this={menuEl}
      onkeydown={menuKeydown}
    >
      <p class="persona-hint">View as</p>
      {#each PERSONAS as p (p.id)}
        <button
          class="persona-opt"
          class:on={persona === p.id}
          role="menuitemradio"
          aria-checked={persona === p.id}
          onclick={() => choose(p.id)}
        >
          <span class="opt-avatar" data-p={p.id}><Icon name={p.id} size={16} /></span>
          <span class="opt-text"><b>{p.label}</b><small>{p.blurb}</small></span>
          {#if persona === p.id}<span class="opt-check"><Icon name="check" size={14} /></span>{/if}
        </button>
      {/each}
    </div>
  </details>
  <button
    class="toggle"
    onclick={() => (theme = toggleTheme(theme))}
    aria-label="Toggle dark mode"
    title={theme === 'dark' ? 'Switch to light' : 'Switch to dark'}
  >
    <Icon name={theme === 'dark' ? 'sun' : 'moon'} size={18} />
  </button>
</header>

<div id="main" class="page" tabindex="-1">
  {@render children()}
</div>
<CommandPalette />
<ShortcutsOverlay />
<Toasts />

<style>
  header {
    position: sticky;
    top: 0;
    z-index: 10;
    display: flex;
    align-items: center;
    gap: 1.25rem;
    padding: 0.6rem 1.25rem;
    background: color-mix(in srgb, var(--surface) 86%, transparent);
    backdrop-filter: blur(8px);
    border-bottom: 1px solid var(--border);
  }
  /* On narrow screens, condense the nav to icons and hide the brand wordmark. */
  @media (max-width: 720px) {
    header {
      gap: 0.6rem;
      padding: 0.55rem 0.75rem;
    }
    .navlabel {
      display: none;
    }
    .navlink {
      padding: 0.4rem 0.5rem;
    }
    nav {
      gap: 0.1rem;
    }
  }
  /* Phone: drop the brand wordmark, sign-out label, and persona caret to icons,
     and tighten nav, so the header (brand · nav · persona · theme) never
     overflows the viewport. */
  @media (max-width: 560px) {
    header {
      gap: 0.25rem;
      padding: 0.5rem 0.45rem;
    }
    .wordmark {
      display: none;
    }
    .signout-label {
      display: none;
    }
    .auth {
      gap: 0.25rem;
    }
    .auth .ghost {
      padding: 0.3rem 0.3rem;
    }
    /* Let the nav shrink below its content width and scroll horizontally, so the
       brand, persona switcher, and theme toggle stay pinned and the header never
       overflows the viewport — however many nav items there are. */
    nav {
      gap: 0;
      min-width: 0;
      overflow-x: auto;
      scrollbar-width: none;
      -webkit-overflow-scrolling: touch;
    }
    nav::-webkit-scrollbar {
      display: none;
    }
    .navlink {
      padding: 0.35rem 0.32rem;
      flex: 0 0 auto;
    }
    .persona-trigger {
      padding: 0.2rem 0.28rem;
    }
    .avatar {
      width: 24px;
      height: 24px;
    }
    .caret {
      display: none;
    }
    .toggle {
      width: 32px;
      height: 32px;
    }
  }
  .brand {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    font-weight: 700;
    letter-spacing: -0.02em;
    color: var(--fg);
    font-size: 1.05rem;
  }
  .brand:hover {
    text-decoration: none;
  }
  .mark {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 28px;
    height: 28px;
    border-radius: 8px;
    color: var(--on-accent);
    background: linear-gradient(135deg, var(--accent), var(--accent-2));
  }
  nav {
    display: flex;
    gap: 0.25rem;
    margin-right: auto;
  }
  .navlink {
    display: inline-flex;
    align-items: center;
    gap: 0.4rem;
    padding: 0.35rem 0.7rem;
    border-radius: 999px;
    color: var(--fg-muted);
    font-size: 0.9rem;
    font-weight: 500;
  }
  .navlink:hover {
    background: var(--surface-2);
    color: var(--fg);
    text-decoration: none;
  }
  .navlink.active {
    background: color-mix(in srgb, var(--accent) 14%, transparent);
    color: var(--accent);
  }
  .auth {
    display: inline-flex;
    align-items: center;
    gap: 0.6rem;
    font-size: 0.85rem;
  }
  .auth .who {
    color: var(--fg-muted);
  }
  .auth .muted {
    color: var(--fg-subtle);
  }
  .auth .ghost {
    border-color: transparent;
    background: none;
    color: var(--fg-muted);
    padding: 0.3rem 0.5rem;
  }
  .auth .ghost:hover {
    background: var(--surface-2);
    color: var(--fg);
  }
  @media (max-width: 640px) {
    .auth .who {
      display: none;
    }
  }
  .toggle {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 36px;
    height: 36px;
    padding: 0;
    border-radius: 999px;
  }

  /* ⌘K command-palette trigger — subtle, desktop-only (mobile uses the nav + the
     keyboard shortcut still works everywhere). */
  .cmdk {
    display: inline-flex;
    align-items: center;
    gap: 0.4rem;
    padding: 0.3rem 0.5rem;
    border: 1px solid var(--border);
    border-radius: 999px;
    background: var(--surface);
    color: var(--fg-muted);
    font-size: 0.82rem;
  }
  .cmdk:hover {
    background: var(--surface-2);
    color: var(--fg);
  }
  .cmdk kbd {
    font-family: var(--font-mono);
    font-size: 0.68rem;
    padding: 0.02rem 0.3rem;
    border: 1px solid var(--border-strong);
    border-radius: 4px;
    color: var(--fg-subtle);
    background: var(--surface-2);
  }
  @media (max-width: 720px) {
    .cmdk {
      display: none;
    }
  }

  /* Persona switcher — a "view-as" identity control. The avatar's colour IS the
     active persona's accent, so the current view is legible at a glance. */
  .persona {
    position: relative;
  }
  .persona-trigger {
    display: inline-flex;
    align-items: center;
    gap: 0.45rem;
    padding: 0.25rem 0.55rem 0.25rem 0.3rem;
    border: 1px solid var(--border);
    border-radius: 999px;
    background: var(--surface);
    color: var(--fg);
    font: inherit;
    font-size: 0.85rem;
    cursor: pointer;
    list-style: none;
    user-select: none;
  }
  .persona-trigger::-webkit-details-marker {
    display: none;
  }
  .persona-trigger:hover {
    background: var(--surface-2);
  }
  .avatar {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 26px;
    height: 26px;
    border-radius: 999px;
    color: var(--on-accent);
    background: linear-gradient(135deg, var(--accent), var(--accent-2));
  }
  .persona-name {
    font-weight: 550;
  }
  .caret {
    display: inline-flex;
    color: var(--fg-subtle);
    transition: transform 0.15s ease;
  }
  .persona[open] .caret {
    transform: rotate(180deg);
  }
  .persona-menu {
    position: absolute;
    top: calc(100% + 0.45rem);
    right: 0;
    z-index: 50;
    min-width: 16rem;
    padding: 0.4rem;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    box-shadow: var(--shadow);
  }
  .persona-hint {
    margin: 0.2rem 0.5rem 0.35rem;
    font-size: 0.7rem;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--fg-subtle);
  }
  .persona-opt {
    display: flex;
    align-items: center;
    gap: 0.6rem;
    width: 100%;
    padding: 0.45rem 0.5rem;
    border: none;
    border-radius: var(--radius-sm);
    background: none;
    text-align: left;
    cursor: pointer;
  }
  .persona-opt:hover {
    background: var(--surface-2);
  }
  .persona-opt.on {
    background: color-mix(in srgb, var(--accent) 12%, transparent);
  }
  .opt-avatar {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 30px;
    height: 30px;
    flex: none;
    border-radius: 999px;
    color: #fff;
  }
  .opt-avatar[data-p='builder'] {
    background: linear-gradient(135deg, #f59e0b, #d97706);
    color: #1c1503;
  }
  .opt-avatar[data-p='operator'] {
    background: linear-gradient(135deg, #14b8a6, #0d9488);
  }
  .opt-avatar[data-p='showcase'] {
    background: linear-gradient(135deg, #e11d48, #be123c);
  }
  .opt-text {
    display: flex;
    flex-direction: column;
    line-height: 1.25;
    margin-right: auto;
  }
  .opt-text b {
    font-weight: 600;
    font-size: 0.9rem;
  }
  .opt-text small {
    color: var(--fg-muted);
    font-size: 0.78rem;
  }
  .opt-check {
    display: inline-flex;
    color: var(--accent-ink);
  }
  @media (max-width: 640px) {
    .persona-name {
      display: none;
    }
  }
  .page {
    min-height: calc(100vh - 53px);
  }
</style>
