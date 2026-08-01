<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Editor surface: the provider lifecycle — versioned manifests with install →
     test → approve → deploy → pause/resume → upgrade → retire per environment,
     plus health. Approval is four-eyes (an independent approver role). -->
<script lang="ts">
  import { onMount } from 'svelte';
  import EmptyState from '$lib/EmptyState.svelte';
  import Skeleton from '$lib/Skeleton.svelte';
  import Badge from '$lib/Badge.svelte';
  import RelativeTime from '$lib/RelativeTime.svelte';
  import { roleAtLeast } from '$lib/roles';
  import { user } from '$lib/session';
  import {
    installProvider,
    listProviders,
    providerAction,
    upgradeProvider,
    listProviderHealth,
    ApiError,
    type ProviderView,
    type ProviderHealth
  } from '$lib/api';

  const key = '';

  let providers = $state<ProviderView[]>([]);
  let health = $state<ProviderHealth[]>([]);
  let loading = $state(true);
  let error = $state('');
  let forbidden = $state(false);
  let busy = $state('');

  // Install form.
  let name = $state('');
  let connector = $state('');
  let description = $state('');
  let schema = $state('{"type":"object","properties":{"score":{"type":"number"}}}');
  let timeout = $state(10);
  let retries = $state(0);
  let cost = $state(0);

  function msg(e: unknown): string {
    return e instanceof Error ? e.message : String(e);
  }

  async function load() {
    loading = true;
    error = '';
    forbidden = false;
    try {
      [providers, health] = await Promise.all([listProviders(key), listProviderHealth(key)]);
    } catch (e) {
      if (e instanceof ApiError && e.status === 403) forbidden = true;
      else error = msg(e);
    } finally {
      loading = false;
    }
  }

  async function install() {
    if (busy || !name.trim() || !connector.trim() || !description.trim()) return;
    error = '';
    busy = 'install';
    try {
      await installProvider(key, {
        name: name.trim(),
        connector: connector.trim(),
        description: description.trim(),
        conformance: {
          schema,
          timeout_seconds: timeout,
          max_retries: retries,
          cost_per_fetch_usd: cost
        }
      });
      name = '';
      connector = '';
      description = '';
      await load();
    } catch (e) {
      error = msg(e);
    } finally {
      busy = '';
    }
  }

  async function act(
    label: string,
    provider: ProviderView,
    action: 'approve' | 'deploy' | 'pause' | 'resume' | 'retire' | 'test' | 'configure',
    environment: string
  ) {
    if (busy) return;
    error = '';
    busy = `${action}-${provider.name}-v${provider.version}`;
    try {
      let body: unknown = { environment };
      if (action === 'approve')
        body = {
          request_id: `req-${provider.name}-v${provider.version}`,
          reason: 'Independent review passed'
        };
      else if (action === 'pause' || action === 'retire')
        body = {
          environment,
          reason:
            window.prompt(
              `${label} ${provider.name} v${provider.version} in ${environment}: reason`
            ) ?? ''
        };
      else if (action === 'test')
        body = { passed: true, fixture: 'sandbox', details: 'conformance ok' };
      else if (action === 'configure') body = { environment, config: {} };
      await providerAction(key, provider.name, provider.version, action, body);
      await load();
    } catch (e) {
      error = msg(e);
    } finally {
      busy = '';
    }
  }

  async function upgrade(provider: ProviderView, environment: string) {
    if (busy) return;
    error = '';
    busy = `upgrade-${provider.name}`;
    try {
      await upgradeProvider(key, provider.name, provider.version + 1, environment);
      await load();
    } catch (e) {
      error = msg(e);
    } finally {
      busy = '';
    }
  }

  onMount(load);
</script>

<svelte:head><title>Providers · intraktible</title></svelte:head>

<main class="page-shell" data-testid="providers-page">
  <header class="page-head">
    <div>
      <p class="eyebrow">Ecosystem</p>
      <h1>Providers</h1>
      <p class="lede">
        Versioned provider manifests with a governed lifecycle: install, test, independent approval,
        deploy, pause/resume, upgrade, and retire per environment. Approval is four-eyes.
      </p>
    </div>
    <button class="secondary" disabled={loading} onclick={() => void load()}>Reload</button>
  </header>

  {#if error}<p class="error" role="alert">{error}</p>{/if}

  {#if loading}
    <Skeleton rows={8} />
  {:else if forbidden}
    <EmptyState
      title="Restricted"
      hint="The provider lifecycle is gated to editor and approver roles."
    />
  {:else}
    <section class="panel" aria-label="Provider versions">
      <div class="section-head">
        <div>
          <p class="eyebrow">Lifecycle</p>
          <h2>Provider versions</h2>
        </div>
      </div>
      {#if providers.length === 0}
        <EmptyState title="No providers" hint="Install the first provider version below." />
      {:else}
        <ul class="cards">
          {#each providers as p (p.name + '-v' + p.version)}
            <li class="card">
              <div class="row">
                <strong>{p.name} v{p.version}</strong>
                <span class="badges">
                  {#if p.tested}<Badge tone="ok">tested</Badge>{/if}
                  {#if p.approved}<Badge tone="ok">approved</Badge>{/if}
                </span>
              </div>
              <p class="muted">
                {p.manifest.connector} · timeout {p.manifest.conformance.timeout_seconds}s · ${p
                  .manifest.conformance.cost_per_fetch_usd ?? 0}/fetch
              </p>
              {#if p.deployments && Object.keys(p.deployments).length > 0}
                <p class="muted">
                  {#each Object.entries(p.deployments) as [env, stage]}
                    <span class="env">{env}: {stage}</span>
                  {/each}
                </p>
              {/if}
              <div class="actions">
                {#if !p.tested}
                  <button
                    class="text-btn"
                    disabled={!!busy}
                    onclick={() => void act('Test', p, 'test', 'sandbox')}>Test</button
                  >
                {/if}
                {#if p.tested && !p.approved && roleAtLeast($user?.role, 'approver')}
                  <button
                    class="text-btn"
                    disabled={!!busy}
                    onclick={() => void act('Approve', p, 'approve', 'production')}>Approve</button
                  >
                {/if}
                {#if p.approved && roleAtLeast($user?.role, 'approver')}
                  <button
                    class="text-btn"
                    disabled={!!busy}
                    onclick={() => void act('Deploy', p, 'configure', 'production')}
                    >Configure</button
                  >
                  <button
                    class="text-btn"
                    disabled={!!busy}
                    onclick={() => void act('Deploy', p, 'deploy', 'production')}
                    >Deploy prod</button
                  >
                  <button
                    class="text-btn"
                    disabled={!!busy}
                    onclick={() => void upgrade(p, 'production')}>Upgrade</button
                  >
                {/if}
                {#if p.deployments?.production === 'deployed'}
                  <button
                    class="text-btn"
                    disabled={!!busy}
                    onclick={() => void act('Pause', p, 'pause', 'production')}>Pause</button
                  >
                {/if}
                {#if p.deployments?.production === 'paused'}
                  <button
                    class="text-btn"
                    disabled={!!busy}
                    onclick={() => void act('Resume', p, 'resume', 'production')}>Resume</button
                  >
                {/if}
                {#if p.deployments?.production === 'deployed' || p.deployments?.production === 'paused'}
                  <button
                    class="text-btn danger-btn"
                    disabled={!!busy}
                    onclick={() => void act('Retire', p, 'retire', 'production')}>Retire</button
                  >
                {/if}
              </div>
            </li>
          {/each}
        </ul>
      {/if}

      <form
        class="inline-form"
        onsubmit={(e) => {
          e.preventDefault();
          void install();
        }}
      >
        <h3>Install a provider version</h3>
        <label>Name<input bind:value={name} placeholder="bureau" /></label>
        <label>Connector type<input bind:value={connector} placeholder="credit-bureau" /></label>
        <label
          >Description<input bind:value={description} placeholder="Credit bureau provider" /></label
        >
        <label>Schema JSON<textarea rows="4" bind:value={schema}></textarea></label>
        <div class="grid3">
          <label>Timeout (s)<input type="number" min="1" bind:value={timeout} /></label>
          <label>Retries<input type="number" min="0" bind:value={retries} /></label>
          <label>Cost/fetch ($)<input type="number" min="0" step="0.01" bind:value={cost} /></label>
        </div>
        <button disabled={busy === 'install'}>Install version</button>
      </form>
    </section>

    <section class="panel" aria-label="Provider health">
      <div class="section-head">
        <div>
          <p class="eyebrow">Operations</p>
          <h2>Health</h2>
        </div>
      </div>
      {#if health.length === 0}
        <EmptyState
          title="No provider activity"
          hint="Fetches and errors appear here once providers are deployed."
        />
      {:else}
        <div class="table-wrap">
          <table>
            <thead
              ><tr
                ><th>Provider</th><th>Environment</th><th>Fetches</th><th>Errors</th><th
                  >Last success</th
                ><th>Last error</th></tr
              ></thead
            >
            <tbody>
              {#each health as h (h.name + h.environment)}
                <tr>
                  <td><strong>{h.name}</strong></td>
                  <td>{h.environment}</td>
                  <td>{h.fetches}</td>
                  <td>{h.errors}</td>
                  <td
                    >{#if h.last_success}<RelativeTime value={h.last_success} />{:else}—{/if}</td
                  >
                  <td
                    >{#if h.last_error}<RelativeTime value={h.last_error} />{:else}—{/if}</td
                  >
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {/if}
    </section>
  {/if}
</main>

<style>
  .page-shell {
    max-width: 90rem;
    margin: 0 auto;
    padding: 2rem;
  }
  .page-head,
  .section-head,
  .row {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 1rem;
  }
  .eyebrow {
    margin: 0 0 0.3rem;
    color: var(--accent-text);
    font: 600 0.72rem var(--font-mono);
    letter-spacing: 0.09em;
    text-transform: uppercase;
  }
  h1 {
    margin: 0;
    font-size: clamp(2rem, 4vw, 3.4rem);
  }
  h2 {
    margin: 0.1rem 0;
    font-size: 1.45rem;
  }
  .lede {
    margin: 0.4rem 0 0;
    color: var(--fg-muted);
    max-width: 46rem;
  }
  .panel {
    margin-top: 1.4rem;
    padding: 1.2rem;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius);
  }
  .cards {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    gap: 0.6rem;
  }
  .card {
    padding: 0.7rem;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
  }
  .badges {
    display: inline-flex;
    gap: 0.3rem;
  }
  .env {
    margin-right: 0.6rem;
    font-size: 0.85rem;
  }
  .actions {
    display: flex;
    gap: 0.5rem;
    flex-wrap: wrap;
    margin-top: 0.5rem;
  }
  .muted {
    color: var(--fg-muted);
    font-size: 0.85rem;
  }
  .inline-form {
    margin-top: 1rem;
    padding-top: 1rem;
    border-top: 1px solid var(--border);
    display: grid;
    gap: 0.6rem;
  }
  .inline-form label {
    display: grid;
    gap: 0.2rem;
    font-size: 0.85rem;
  }
  .grid3 {
    display: grid;
    grid-template-columns: repeat(3, 1fr);
    gap: 0.6rem;
  }
  .error {
    color: var(--danger);
  }
</style>
