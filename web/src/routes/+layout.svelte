<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Root layout: global stylesheet, the app header (brand + nav + theme toggle),
     and a slot for the routed page. -->
<script lang="ts">
  import '../app.css';
  import { onMount } from 'svelte';
  import { get } from 'svelte/store';
  import { page } from '$app/stores';
  import { afterNavigate, goto } from '$app/navigation';
  import Icon from '$lib/Icon.svelte';
  import { initTheme, toggleTheme, theme as themeStore } from '$lib/theme';
  import {
    initPersona,
    setPersona,
    persona as personaStore,
    PERSONAS,
    personaConfig,
    navFor
  } from '$lib/persona';
  import { user, refreshUser, signOut } from '$lib/session';
  import { listSsoProviders } from '$lib/api';
  import { appHref } from '$lib/paths';
  import { openPalette } from '$lib/palette';
  import { openGuide } from '$lib/guide';
  import { helpFor } from '$lib/help/registry';
  import { resetRecorder } from '$lib/recorder';
  import { buildCurrentPageExport } from '$lib/aiexport';
  import { copyText } from '$lib/clipboard';
  import { toast } from '$lib/toast';
  import Toasts from '$lib/Toasts.svelte';
  import CommandPalette from '$lib/CommandPalette.svelte';
  import ShortcutsOverlay from '$lib/ShortcutsOverlay.svelte';
  import PageGuide from '$lib/PageGuide.svelte';
  import NotificationsBell from '$lib/NotificationsBell.svelte';
  import DemoBanner from '$lib/DemoBanner.svelte';
  import BuildInfo from '$lib/BuildInfo.svelte';

  let { children } = $props();
  let theme = $state<'light' | 'dark'>('light');
  let persona = $state(initPersona());
  let authReady = $state(false);
  let authError = $state('');

  async function initializeAuthentication(): Promise<void> {
    authReady = false;
    authError = '';
    try {
      await refreshUser();
    } catch (error) {
      authError = error instanceof Error ? error.message : String(error);
      authReady = true;
      return;
    }
    if (get(user) || isLogin) {
      authReady = true;
      return;
    }
    let providers: string[];
    try {
      providers = await listSsoProviders();
    } catch (error) {
      authError = error instanceof Error ? error.message : String(error);
      authReady = true;
      return;
    }
    if (providers.includes('shauth')) {
      const returnTo = `${window.location.pathname}${window.location.search}`;
      const query = new URLSearchParams({ return_to: returnTo });
      window.location.assign(`${appHref('/v1/auth/oidc/shauth/login')}?${query}`);
      return;
    }
    await goto(appHref('/login'));
    authReady = true;
  }

  onMount(() => {
    theme = initTheme();
    initPersona();
    void initializeAuthentication();
    const unsubTheme = themeStore.subscribe((t) => (theme = t));
    const unsubPersona = personaStore.subscribe((p) => (persona = p));
    return () => {
      unsubTheme();
      unsubPersona();
    };
  });

  // The API-call recorder is per-navigation: reset it when the route changes so
  // the "Export for AI" document only describes the current page's visit. Not on
  // 'enter' (initial load) — the buffer is empty then and the first page's calls
  // may already be in flight.
  afterNavigate((nav) => {
    if (nav.type !== 'enter') resetRecorder();
  });

  // One-click "copy this page for AI" — the same document the guide panel offers.
  async function copyForAI(): Promise<void> {
    await copyText(buildCurrentPageExport($page.route.id, $page.url.pathname), 'Copied for AI');
  }

  // Signing out clears the session server-side and in the store. Every path ends
  // at the persistent app-local landing; /login would immediately start silent
  // SSO when Shauth is configured and make a deliberate sign-out look ineffective.
  async function doSignOut(): Promise<void> {
    try {
      const logoutURL = await signOut();
      if (logoutURL) {
        window.location.assign(logoutURL);
        return;
      }
      window.location.assign(appHref('/v1/auth/signed-out'));
      return;
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e));
      window.location.assign(appHref('/v1/auth/signed-out'));
      return;
    }
  }

  // Navigation is the current persona's ordered (and optionally relabelled) subset
  // of the shared catalog. The signed-in role (from /v1/me) also drops admin-only
  // surfaces for non-admins, so a manager/executive never lands on a 403 dead-end.
  const nav = $derived(navFor(persona, $user?.role));

  const path = $derived($page.url.pathname);
  // A per-page browser title from the help registry (which already names every page),
  // so tabs/bookmarks read "Decision trace · intraktible" rather than "untitled page".
  const pageTitle = $derived(helpFor($page.route.id ?? '')?.title);
  // The sign-in screen shows only minimal chrome (brand + theme) — not the full
  // authenticated nav/account controls.
  // Route id (not the pathname) so it matches under a base path too — comparing the raw
  // pathname to "/login" is always false on the /<repo>/demo/ deploy, which would leak
  // the full app chrome onto the sign-in screen.
  const isLogin = $derived(($page.route.id ?? '') === '/login');
  // Compare against the BASE-PREFIXED href (what's actually rendered): under a base
  // path (e.g. the /intraktible/demo/ deploy) the pathname carries the prefix, so
  // comparing the raw "/engine" would never match and no item would read as current.
  function active(prefixedHref: string): boolean {
    return path === prefixedHref || path.startsWith(prefixedHref + '/');
  }

  const currentPersona = $derived(personaConfig(persona));
  // A local, deterministic avatar avoids leaking a user's identity to an external
  // image host while still making the authenticated account immediately recognizable.
  function initials(actor: string): string {
    const words = actor
      .trim()
      .split(/[^\p{L}\p{N}]+/u)
      .filter(Boolean);
    return (words.length > 1 ? words.slice(0, 2) : words.slice(0, 1))
      .map((word) => [...word][0]?.toLocaleUpperCase() ?? '')
      .join('');
  }
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

<svelte:head>
  <title>{pageTitle ? `${pageTitle} · intraktible` : 'intraktible'}</title>
</svelte:head>

<a class="skip-link" href="#main">Skip to content</a>
<DemoBanner />
<header>
  <a class="brand" href={appHref('/')}>
    <span class="mark"><Icon name="logo" size={20} /></span>
    <span class="wordmark">intraktible</span>
  </a>
  {#if !isLogin && authReady && !authError && $user}
    <nav aria-label="Primary">
      {#each nav as item (item.href)}
        <a
          href={appHref(item.href)}
          class="navlink"
          class:active={active(appHref(item.href))}
          aria-current={active(appHref(item.href)) ? 'page' : undefined}
          title={item.label}
        >
          <Icon name={item.icon} size={16} />
          <span class="navlabel">{item.label}</span>
        </a>
      {/each}
    </nav>
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
    {#if $user}<NotificationsBell />{/if}
    <!-- One account-and-view control: the persona switcher (reshapes the UI for the
         viewer's role) plus the signed-in identity and sign-out, instead of competing
         top-bar controls. Role is switched separately, in the demo banner strip. -->
    <details
      class="persona"
      bind:this={personaEl}
      data-testid="persona-switch"
      ontoggle={onPersonaToggle}
    >
      <summary
        class="persona-trigger"
        title="Account & view — {currentPersona.blurb}"
        aria-haspopup="menu"
        aria-label={$user
          ? `Account and view — signed in as ${$user.actor}, viewing as ${currentPersona.label}`
          : `View as — current: ${currentPersona.label}`}
      >
        {#if $user}
          <span class="avatar user-avatar" data-testid="user-avatar" aria-hidden="true"
            >{initials($user.actor)}</span
          >
          <span class="persona-name" data-testid="user-identity" data-shauth-user={$user.actor}
            >{$user.actor}</span
          >
        {:else}
          <span class="avatar"><Icon name={currentPersona.icon} size={16} /></span>
          <span class="persona-name">{currentPersona.label}</span>
        {/if}
        <span class="caret"><Icon name="chevron-down" size={13} /></span>
      </summary>
      <div
        class="persona-menu"
        role="menu"
        aria-label="Account and view"
        tabindex="-1"
        bind:this={menuEl}
        onkeydown={menuKeydown}
      >
        {#if $user}
          <p class="acct-id" data-testid="auth-status">Signed in as <b>{$user.actor}</b></p>
          <a class="acct-action" href={appHref('/me')}><Icon name="user" size={14} /> My account</a>
        {:else}
          <p class="acct-id muted" data-testid="auth-status">Not signed in</p>
        {/if}
        <p class="persona-hint">View as</p>
        <p class="persona-sub">
          Reshape the whole UI for a different job — your sign-in &amp; data stay the same.
        </p>
        {#each PERSONAS as p (p.id)}
          <button
            class="persona-opt"
            class:on={persona === p.id}
            role="menuitemradio"
            aria-checked={persona === p.id}
            onclick={() => choose(p.id)}
          >
            <span class="opt-avatar" data-p={p.id}><Icon name={p.icon} size={16} /></span>
            <span class="opt-text"><b>{p.label}</b><small>{p.blurb}</small></span>
            {#if persona === p.id}<span class="opt-check"><Icon name="check" size={14} /></span
              >{/if}
          </button>
        {/each}
        {#if $user}
          <button class="acct-action" data-shauth-sign-out onclick={doSignOut}>
            <Icon name="signout" size={14} /> Sign out
          </button>
        {:else}
          <a class="acct-action" href={appHref('/login')}>Sign in</a>
        {/if}
      </div>
    </details>
    <button
      class="guide-trigger"
      onclick={copyForAI}
      aria-label="Copy this page for AI"
      title="Copy for AI — a machine-readable export of this page (what it is, its API calls, its content)"
      data-testid="ai-copy-trigger"
      disabled={!helpFor($page.route.id)}
    >
      <Icon name="copy" size={16} />
    </button>
    <button
      class="guide-trigger"
      onclick={openGuide}
      aria-label="Guide for this page"
      title="Page guide — what this page is for and how to use it"
      data-testid="guide-trigger"
      disabled={!helpFor($page.route.id)}
    >
      <Icon name="help" size={17} />
    </button>
  {:else}
    <span class="grow"></span>
  {/if}
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
  {#if authReady && !authError}
    {@render children()}
  {:else if authReady}
    <section class="auth-failure" role="alert" aria-labelledby="auth-failure-title">
      <h1 id="auth-failure-title">Unable to verify your session</h1>
      <p>{authError}</p>
      <button type="button" onclick={() => void initializeAuthentication()}>Try again</button>
    </section>
  {:else}
    <div class="auth-loading" role="status" aria-live="polite">Checking your session…</div>
  {/if}
</div>

<BuildInfo />
<CommandPalette />
<ShortcutsOverlay />
<PageGuide />
<Toasts />

<style>
  .auth-loading {
    min-height: 60vh;
    display: grid;
    place-items: center;
    color: var(--fg-subtle);
  }

  .auth-failure {
    width: min(34rem, calc(100% - 2rem));
    margin: 4rem auto;
    padding: 1.5rem;
    border: 1px solid var(--danger);
    border-radius: var(--radius);
    background: var(--surface);
  }

  .auth-failure h1 {
    margin-top: 0;
  }

  .auth-failure button {
    padding: 0.55rem 0.8rem;
    border: 0;
    border-radius: var(--radius);
    background: var(--accent);
    color: white;
    font: inherit;
    font-weight: 650;
  }

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
    /* Let the nav shrink below its content width and scroll horizontally, so the
       brand, persona switcher, and theme toggle stay pinned and the header never
       overflows the viewport — however many nav items there are. */
    nav {
      gap: 0;
      min-width: 0;
      overflow-x: auto;
      scrollbar-width: none;
      -webkit-overflow-scrolling: touch;
      /* A right-edge fade plus trailing pad so the last icon, when the scroll clips
         it against the pinned header controls, reads as "there's more" rather than a
         half-glyph glitch. Scoped to the icon-only mobile nav, not the desktop one. */
      mask-image: linear-gradient(to right, #000, #000 88%, transparent);
      -webkit-mask-image: linear-gradient(to right, #000, #000 88%, transparent);
      padding-right: 0.5rem;
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
    /* Shrink + scroll rather than pushing the trailing header controls (theme toggle)
       off-screen when the full-label nav doesn't fit (~720–1300px). */
    min-width: 0;
    overflow-x: auto;
    scrollbar-width: none;
  }
  nav::-webkit-scrollbar {
    display: none;
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
    white-space: nowrap;
  }
  .navlink:hover {
    background: var(--surface-2);
    color: var(--fg);
    text-decoration: none;
  }
  .navlink.active {
    background: color-mix(in srgb, var(--accent) 14%, transparent);
    /* --accent-ink (not raw --accent) is the text-safe accent — some personas' --accent
       is a low-contrast colour (e.g. evaluator's slate) that fails WCAG AA as text. */
    color: var(--accent-ink);
  }
  .grow {
    flex: 1;
  }
  .acct-id {
    margin: 0.2rem 0.5rem 0.35rem;
    font-size: 0.82rem;
    color: var(--fg-muted);
  }
  .acct-action {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    width: 100%;
    margin-top: 0.3rem;
    padding: 0.45rem 0.5rem;
    border: none;
    border-top: 1px solid var(--border);
    border-radius: 0 0 var(--radius-sm) var(--radius-sm);
    background: none;
    color: var(--fg-muted);
    font: inherit;
    text-align: left;
    cursor: pointer;
  }
  .acct-action:hover {
    background: var(--surface-2);
    color: var(--fg);
    text-decoration: none;
  }
  .toggle,
  .guide-trigger {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 36px;
    height: 36px;
    padding: 0;
    border-radius: 999px;
  }
  .guide-trigger {
    border: 1px solid var(--border);
    background: var(--surface);
    color: var(--fg-muted);
    cursor: pointer;
  }
  .guide-trigger:hover:not(:disabled) {
    color: var(--fg);
    border-color: var(--accent);
  }
  .guide-trigger:disabled {
    opacity: 0.4;
    cursor: default;
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
  .user-avatar {
    /* Use an opaque fill: the accessibility audit can measure it, unlike the
       transparent computed background of the decorative gradient. */
    background: var(--accent);
    font-size: 0.72rem;
    font-weight: 750;
    letter-spacing: 0.04em;
  }
  .persona-name {
    font-weight: 550;
    white-space: nowrap;
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
    margin: 0.2rem 0.5rem 0.15rem;
    font-size: 0.7rem;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--fg-subtle);
  }
  .persona-sub {
    margin: 0 0.5rem 0.5rem;
    font-size: 0.74rem;
    line-height: 1.35;
    color: var(--fg-subtle);
    max-width: 17rem;
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
  .opt-avatar[data-p='developer'] {
    background: linear-gradient(135deg, #6366f1, #4338ca);
  }
  .opt-avatar[data-p='manager'] {
    background: linear-gradient(135deg, #0ea5e9, #0369a1);
  }
  .opt-avatar[data-p='product'] {
    background: linear-gradient(135deg, #8b5cf6, #6d28d9);
  }
  .opt-avatar[data-p='evaluator'] {
    background: linear-gradient(135deg, #64748b, #334155);
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
