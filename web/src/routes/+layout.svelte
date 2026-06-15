<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Root layout: global stylesheet, the app header (brand + nav + theme toggle),
     and a slot for the routed page. -->
<script lang="ts">
  import '../app.css';
  import { onMount } from 'svelte';
  import { page } from '$app/stores';
  import Icon from '$lib/Icon.svelte';
  import { initTheme, toggleTheme, theme as themeStore } from '$lib/theme';

  let { children } = $props();
  let theme = $state<'light' | 'dark'>('light');

  onMount(() => {
    theme = initTheme();
    return themeStore.subscribe((t) => (theme = t));
  });

  const nav = [
    { href: '/engine', label: 'Engine', icon: 'engine' },
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
