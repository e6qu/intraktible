<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Root layout: global stylesheet, the app header (brand + nav + theme toggle),
     and a slot for the routed page. -->
<script lang="ts">
  import '../app.css';
  import { onMount } from 'svelte';
  import { page } from '$app/stores';
  import Icon from '$lib/Icon.svelte';
  import { initTheme, toggleTheme, theme as themeStore } from '$lib/theme';
  import { user, refreshUser, signOut } from '$lib/session';

  let { children } = $props();
  let theme = $state<'light' | 'dark'>('light');

  onMount(() => {
    theme = initTheme();
    void refreshUser();
    return themeStore.subscribe((t) => (theme = t));
  });

  const nav = [
    { href: '/engine', label: 'Engine', icon: 'engine' },
    { href: '/decisions', label: 'Decisions', icon: 'diagram' },
    { href: '/cases', label: 'Cases', icon: 'cases' },
    { href: '/agents', label: 'Agents', icon: 'agents' }
  ];

  const path = $derived($page.url.pathname);
  function active(href: string): boolean {
    return path === href || path.startsWith(href + '/');
  }
</script>

<header>
  <a class="brand" href="/">
    <span class="mark"><Icon name="logo" size={20} /></span>
    intraktible
  </a>
  <nav>
    {#each nav as item (item.href)}
      <a href={item.href} class="navlink" class:active={active(item.href)}>
        <Icon name={item.icon} size={16} />
        <span>{item.label}</span>
      </a>
    {/each}
  </nav>
  <span class="auth" data-testid="auth-status">
    {#if $user}
      <span class="who">Signed in as <b>{$user.actor}</b></span>
      <button class="ghost" onclick={signOut}><Icon name="signout" size={14} /> Sign out</button>
    {:else}
      <span class="who muted">Not signed in</span>
      <a class="navlink" href="/login">Sign in</a>
    {/if}
  </span>
  <button
    class="toggle"
    onclick={() => (theme = toggleTheme(theme))}
    aria-label="Toggle dark mode"
    title={theme === 'dark' ? 'Switch to light' : 'Switch to dark'}
  >
    <Icon name={theme === 'dark' ? 'sun' : 'moon'} size={18} />
  </button>
</header>

<div class="page">
  {@render children()}
</div>

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
  .page {
    min-height: calc(100vh - 53px);
  }
</style>
