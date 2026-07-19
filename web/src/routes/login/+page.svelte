<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { onMount } from 'svelte';
  import { goto } from '$app/navigation';
  import { login, listSsoProviders, listSamlProviders } from '$lib/api';
  import { user } from '$lib/session';
  import { appHref } from '$lib/paths';

  // Prefill the sandbox key only in the public demo (VITE_DEMO), where the seed key is
  // public and there is no real secret to protect — a real deployment shows an empty
  // field so no bundled credential is ever suggested.
  let apiKey = $state(import.meta.env.VITE_DEMO ? 'dev-sandbox-key' : '');
  let error = $state('');
  let busy = $state(false);
  let loadingProviders = $state(true);
  let providerError = $state('');
  let delegatingToShauth = $state(false);
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
    try {
      const [oidc, saml] = await Promise.all([listSsoProviders(), listSamlProviders()]);
      if (oidc.includes('shauth')) {
        delegatingToShauth = true;
        window.location.assign(appHref('/v1/auth/oidc/shauth/login'));
        return;
      }
      ssoButtons = [
        ...oidc.map((p) => ({ label: providerLabel(p), href: `/v1/auth/oidc/${p}/login` })),
        ...saml.map((p) => ({
          label: `${providerLabel(p)} (SAML)`,
          href: `/v1/auth/saml/${p}/login`
        }))
      ];
    } catch (cause) {
      providerError = cause instanceof Error ? cause.message : String(cause);
    } finally {
      loadingProviders = false;
    }
  });

  async function submit() {
    if (busy) return; // Enter can fire onsubmit while a login is already in flight
    error = '';
    busy = true;
    try {
      user.set(await login(apiKey));
      await goto(appHref('/'));
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      busy = false;
    }
  }
</script>

<main>
  <h1>Sign in</h1>
  {#if loadingProviders || delegatingToShauth}
    <p role="status" aria-live="polite">
      {delegatingToShauth ? 'Continuing to Shauth…' : 'Checking available sign-in methods…'}
    </p>
  {:else if providerError}
    <section class="provider-error" role="alert" aria-labelledby="provider-error-title">
      <h2 id="provider-error-title">Sign-in methods are unavailable</h2>
      <p>{providerError}</p>
      <button type="button" onclick={() => window.location.reload()}>Try again</button>
    </section>
  {:else}
    <p>Exchange an API key for a session — then the UI authenticates with a cookie.</p>
    <form
      class="row"
      onsubmit={(e) => {
        e.preventDefault();
        submit();
      }}
    >
      <input bind:value={apiKey} type="password" placeholder="API key" aria-label="API key" />
      <button type="submit" disabled={busy}>{busy ? 'Signing in…' : 'Sign in'}</button>
    </form>
    {#if error}<p class="err" data-testid="login-error">{error}</p>{/if}
  {/if}

  {#if !loadingProviders && !delegatingToShauth && ssoButtons.length > 0}
    <div class="sso" data-testid="sso-providers">
      <div class="divider"><span>or</span></div>
      {#each ssoButtons as b (b.href)}
        <a class="sso-btn" href={appHref(b.href)}>Sign in with {b.label}</a>
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
  .provider-error {
    padding: 1rem;
    border: 1px solid var(--danger);
    border-radius: var(--radius);
    background: var(--surface);
  }
  .provider-error h2 {
    margin-top: 0;
    font-size: 1.1rem;
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
