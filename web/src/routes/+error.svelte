<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Root error boundary: a 404 URL or a load() throw renders here inside the app shell
     (the +layout header stays), instead of SvelteKit's default unstyled page. -->
<script lang="ts">
  import { page } from '$app/stores';
  import Icon from '$lib/Icon.svelte';
  import { appHref } from '$lib/paths';

  const notFound = $derived($page.status === 404);
</script>

<main>
  <span class="code">{$page.status}</span>
  <h1>
    <Icon name={notFound ? 'search' : 'alert'} size={22} />
    {notFound ? 'Page not found' : 'Something went wrong'}
  </h1>
  <p class="msg">
    {$page.error?.message ??
      (notFound
        ? "This URL doesn't match any page in the app."
        : 'An unexpected error occurred while loading this page.')}
  </p>
  <p><a class="home" href={appHref('/')}>← Back to home</a></p>
</main>

<style>
  main {
    max-width: 40rem;
    margin: 4rem auto;
    padding: 0 1.25rem;
    text-align: center;
  }
  .code {
    display: inline-block;
    font-size: 0.8rem;
    font-weight: 700;
    letter-spacing: 0.08em;
    color: var(--fg-subtle);
    font-variant-numeric: tabular-nums;
  }
  h1 {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    margin: 0.4rem 0 0.6rem;
  }
  .msg {
    color: var(--fg-muted);
    margin: 0 0 1.4rem;
  }
  .home {
    display: inline-flex;
    align-items: center;
    padding: 0.5rem 1rem;
    border-radius: var(--radius);
    background: var(--accent);
    color: var(--on-accent);
    font-weight: 600;
  }
  .home:hover {
    background: var(--accent-2);
    text-decoration: none;
  }
</style>
