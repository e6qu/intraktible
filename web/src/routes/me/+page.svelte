<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import Icon from '$lib/Icon.svelte';
  import { appHref } from '$lib/paths';
  import { user, signOut } from '$lib/session';
  import { toast } from '$lib/toast';

  let busy = $state(false);

  async function doSignOut(): Promise<void> {
    if (busy) return;
    busy = true;
    try {
      const logoutURL = await signOut();
      if (logoutURL) {
        window.location.assign(logoutURL);
        return;
      }
      window.location.assign(appHref('/v1/auth/signed-out'));
    } catch (error) {
      toast.error(error instanceof Error ? error.message : String(error));
      window.location.assign(appHref('/v1/auth/signed-out'));
    } finally {
      busy = false;
    }
  }
</script>

<main class="account-page">
  {#if $user}
    <p class="eyebrow">Your account</p>
    <h1>Signed in as {$user.actor}</h1>
    <p class="lede">This session is managed by your configured identity provider.</p>

    <section class="account-card" aria-label="Current account details">
      <dl>
        <div>
          <dt>Identity</dt>
          <dd>{$user.actor}</dd>
        </div>
        <div>
          <dt>Role</dt>
          <dd>{$user.role}</dd>
        </div>
        <div>
          <dt>Organization</dt>
          <dd>{$user.org}</dd>
        </div>
        <div>
          <dt>Workspace</dt>
          <dd>{$user.workspace}</dd>
        </div>
      </dl>
      <button class="signout" type="button" onclick={doSignOut} disabled={busy}>
        <Icon name="signout" size={16} />
        {busy ? 'Signing out…' : 'Sign out'}
      </button>
    </section>
  {:else}
    <p class="eyebrow">Your account</p>
    <h1>You are not signed in</h1>
    <p class="lede">Sign in to view the identity and role assigned to this browser session.</p>
    <a class="signin" href={appHref('/login')}>Sign in</a>
  {/if}
</main>

<style>
  .account-page {
    max-width: 48rem;
    margin: 3rem auto;
    padding: 0 1rem;
  }
  .eyebrow {
    margin: 0;
    color: var(--accent);
    font-weight: 750;
    letter-spacing: 0.08em;
    text-transform: uppercase;
    font-size: 0.78rem;
  }
  h1 {
    margin: 0.35rem 0 0.65rem;
  }
  .lede {
    color: var(--fg-subtle);
    max-width: 44rem;
  }
  .account-card {
    margin-top: 1.5rem;
    padding: 1.25rem;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--surface);
  }
  dl {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 1rem;
    margin: 0 0 1.5rem;
  }
  dt {
    color: var(--fg-subtle);
    font-size: 0.85rem;
  }
  dd {
    margin: 0.2rem 0 0;
    font-weight: 650;
    overflow-wrap: anywhere;
  }
  .signout,
  .signin {
    display: inline-flex;
    align-items: center;
    gap: 0.45rem;
    padding: 0.55rem 0.8rem;
    border-radius: var(--radius);
    font: inherit;
    font-weight: 650;
  }
  .signout {
    border: 1px solid var(--danger);
    background: var(--danger);
    color: white;
  }
  .signin {
    background: var(--accent);
    color: white;
    text-decoration: none;
  }
  @media (max-width: 36rem) {
    dl {
      grid-template-columns: 1fr;
    }
  }
</style>
