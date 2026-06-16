<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { goto } from '$app/navigation';
  import { login } from '$lib/api';
  import { user } from '$lib/session';

  let apiKey = $state('dev-sandbox-key');
  let error = $state('');

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
</style>
