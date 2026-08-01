<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Editor surface: installable solution packs — signed, versioned,
     dependency-pinned manifests with install → upgrade → rollback → retire. -->
<script lang="ts">
  import { onMount } from 'svelte';
  import EmptyState from '$lib/EmptyState.svelte';
  import Skeleton from '$lib/Skeleton.svelte';
  import Badge from '$lib/Badge.svelte';
  import RelativeTime from '$lib/RelativeTime.svelte';
  import { definePack, listPacks, packAction, retirePack, ApiError, type PackView } from '$lib/api';

  const key = '';

  let packs = $state<PackView[]>([]);
  let loading = $state(true);
  let error = $state('');
  let forbidden = $state(false);
  let busy = $state('');

  // Define form.
  let name = $state('');
  let title = $state('');
  let description = $state('');
  let signature = $state('');
  let flowID = $state('');

  function msg(e: unknown): string {
    return e instanceof Error ? e.message : String(e);
  }

  async function load() {
    loading = true;
    error = '';
    forbidden = false;
    try {
      packs = await listPacks(key);
    } catch (e) {
      if (e instanceof ApiError && e.status === 403) forbidden = true;
      else error = msg(e);
    } finally {
      loading = false;
    }
  }

  async function define() {
    if (
      busy ||
      !name.trim() ||
      !title.trim() ||
      !description.trim() ||
      !signature.trim() ||
      !flowID.trim()
    )
      return;
    error = '';
    busy = 'define';
    try {
      await definePack(key, {
        name: name.trim(),
        title: title.trim(),
        description: description.trim(),
        signature: signature.trim(),
        artifacts: [{ kind: 'flow', id: flowID.trim(), content: { graph: '...' } }]
      });
      name = '';
      title = '';
      description = '';
      signature = '';
      flowID = '';
      await load();
    } catch (e) {
      error = msg(e);
    } finally {
      busy = '';
    }
  }

  async function act(pack: PackView, action: 'install' | 'upgrade' | 'rollback') {
    if (busy) return;
    error = '';
    busy = `${action}-${pack.name}`;
    try {
      const version =
        action === 'rollback'
          ? pack.installed - 1
          : action === 'upgrade'
            ? pack.installed + 1
            : pack.installed || 1;
      await packAction(key, pack.name, action, version);
      await load();
    } catch (e) {
      error = msg(e);
    } finally {
      busy = '';
    }
  }

  async function retire(pack: PackView) {
    if (busy) return;
    const reason = window.prompt(`Reason for retiring ${pack.name}`) ?? '';
    if (!reason.trim()) return;
    error = '';
    busy = `retire-${pack.name}`;
    try {
      await retirePack(key, pack.name, reason.trim());
      await load();
    } catch (e) {
      error = msg(e);
    } finally {
      busy = '';
    }
  }

  onMount(load);
</script>

<svelte:head><title>Solution packs · intraktible</title></svelte:head>

<main class="page-shell" data-testid="packs-page">
  <header class="page-head">
    <div>
      <p class="eyebrow">Ecosystem</p>
      <h1>Solution packs</h1>
      <p class="lede">
        Signed, versioned, dependency-pinned pack manifests bundling governed artifacts. Install
        into a workspace, upgrade along a declared path, roll back, or retire.
      </p>
    </div>
    <button class="secondary" disabled={loading} onclick={() => void load()}>Reload</button>
  </header>

  {#if error}<p class="error" role="alert">{error}</p>{/if}

  {#if loading}
    <Skeleton rows={6} />
  {:else if forbidden}
    <EmptyState title="Restricted" hint="Solution packs are gated to editor and operator roles." />
  {:else}
    <section class="panel" aria-label="Solution packs">
      <div class="section-head">
        <div>
          <p class="eyebrow">Lifecycle</p>
          <h2>Packs</h2>
        </div>
      </div>
      {#if packs.length === 0}
        <EmptyState title="No packs" hint="Define the first solution pack below." />
      {:else}
        <ul class="cards">
          {#each packs as pack (pack.name)}
            <li class="card">
              <div class="row">
                <strong>{pack.name}</strong>
                {#if pack.installed > 0}
                  <Badge tone="ok">installed v{pack.installed}</Badge>
                {:else if pack.retired}
                  <Badge tone="danger">retired</Badge>
                {:else}
                  <Badge tone="neutral">not installed</Badge>
                {/if}
              </div>
              <p class="muted">
                {Object.keys(pack.manifests ?? {}).length} version(s) · updated <RelativeTime
                  value={pack.updated_at}
                />
              </p>
              <div class="actions">
                {#if pack.installed === 0 && !pack.retired}
                  <button
                    class="text-btn"
                    disabled={!!busy}
                    onclick={() => void act(pack, 'install')}>Install</button
                  >
                {/if}
                {#if pack.installed > 0}
                  <button
                    class="text-btn"
                    disabled={!!busy}
                    onclick={() => void act(pack, 'upgrade')}>Upgrade</button
                  >
                  <button
                    class="text-btn"
                    disabled={!!busy}
                    onclick={() => void act(pack, 'rollback')}>Roll back</button
                  >
                  <button
                    class="text-btn danger-btn"
                    disabled={!!busy}
                    onclick={() => void retire(pack)}>Retire</button
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
          void define();
        }}
      >
        <h3>Define a solution pack</h3>
        <label>Name<input bind:value={name} placeholder="credit" /></label>
        <label>Title<input bind:value={title} placeholder="Credit origination pack" /></label>
        <label
          >Description<input
            bind:value={description}
            placeholder="Complete credit origination journey."
          /></label
        >
        <label>Signature<input bind:value={signature} placeholder="sig" /></label>
        <label>Bundled flow id<input bind:value={flowID} placeholder="credit-stp" /></label>
        <button disabled={busy === 'define'}>Define version</button>
      </form>
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
  .error {
    color: var(--danger);
  }
</style>
