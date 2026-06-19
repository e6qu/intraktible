<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { onMount } from 'svelte';
  import { goto } from '$app/navigation';
  import { login, listSsoProviders, listSamlProviders } from '$lib/api';
  import { user } from '$lib/session';

  let apiKey = $state('dev-sandbox-key');
  let error = $state('');
  // Each entry is a provider to render a "Sign in with …" button for, with the
  // login path for its protocol.
  let ssoButtons = $state<{ label: string; href: string }[]>([]);

  const PROVIDER_LABELS = new Map([
    ['google', 'Google'],
    ['aws', 'AWS']
  ]);
  const providerLabel = (p: string) =>
    PROVIDER_LABELS.get(p) ?? p.charAt(0).toUpperCase() + p.slice(1);

  onMount(async () => {
    const [oidc, saml] = await Promise.all([listSsoProviders(), listSamlProviders()]);
    ssoButtons = [
      ...oidc.map((p) => ({ label: providerLabel(p), href: `/v1/auth/oidc/${p}/login` })),
      ...saml.map((p) => ({
        label: `${providerLabel(p)} (SAML)`,
        href: `/v1/auth/saml/${p}/login`
      }))
    ];
  });

  async function submit() {
    error = '';
    try {
      user.set(await login(apiKey));
      await goto('/');
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    }
  }
</script>

<main>
  <h1>Sign in</h1>
  <p>Exchange an API key for a session — then the UI authenticates with a cookie.</p>
  <form
    class="row"
    onsubmit={(e) => {
      e.preventDefault();
      submit();
    }}
  >
    <input bind:value={apiKey} type="password" placeholder="API key" aria-label="API key" />
    <button type="submit">Sign in</button>
  </form>
  {#if error}<p class="err" data-testid="login-error">{error}</p>{/if}

  {#if ssoButtons.length > 0}
    <div class="sso" data-testid="sso-providers">
      <div class="divider"><span>or</span></div>
      {#each ssoButtons as b (b.href)}
        <a class="sso-btn" href={b.href}>Sign in with {b.label}</a>
      {/each}
    </div>
  {/if}
</main>

<style>
  main {
    max-width: 32rem;
    margin: 3rem auto;
    padding: 0 1rem;
    font-family: var(--font-ui);
  }
  .row {
    display: flex;
    gap: 0.5rem;
    flex-wrap: wrap;
    align-items: center;
  }
  input,
  button {
    font: inherit;
    padding: 0.4rem 0.6rem;
  }
  .err {
    color: var(--danger);
  }
  .sso {
    margin-top: 1.25rem;
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
    max-width: 18rem;
  }
  .divider {
    display: flex;
    align-items: center;
    gap: 0.6rem;
    color: var(--fg-subtle);
    font-size: 0.85rem;
  }
  .divider::before,
  .divider::after {
    content: '';
    flex: 1;
    height: 1px;
    background: var(--border);
  }
  .sso-btn {
    display: block;
    text-align: center;
    padding: 0.5rem 0.7rem;
    border: 1px solid var(--border);
    border-radius: var(--radius, 8px);
    background: var(--surface);
    color: var(--fg);
    text-decoration: none;
    font-size: 0.9rem;
  }
  .sso-btn:hover {
    background: var(--surface-2);
  }
</style>
