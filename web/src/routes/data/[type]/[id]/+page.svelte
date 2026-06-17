<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { onMount } from 'svelte';
  import { page } from '$app/stores';
  import { displayEntries } from '$lib/kv';
  import RelativeTime from '$lib/RelativeTime.svelte';
  import {
    getEntity,
    listEntityEvents,
    getEntityFeatures,
    type Entity,
    type EntityEvent,
    type FeatureValue
  } from '$lib/api';

  const key = '';
  const type = $page.params.type ?? '';
  const id = $page.params.id ?? '';

  let entity = $state<Entity | null>(null);
  let events = $state<EntityEvent[]>([]);
  let featureValues = $state<FeatureValue[]>([]);
  let error = $state('');
  let loading = $state(true);

  function msg(e: unknown): string {
    return e instanceof Error ? e.message : String(e);
  }
  async function load() {
    error = '';
    loading = true;
    try {
      [entity, events, featureValues] = await Promise.all([
        getEntity(key, type, id),
        listEntityEvents(key, type, id),
        // Computed features are best-effort (none defined for this type is fine).
        getEntityFeatures(key, type, id).catch(() => [])
      ]);
    } catch (e) {
      error = msg(e);
    } finally {
      loading = false;
    }
  }
  onMount(load);
</script>

<main>
  <p><a href="/data">← context data</a></p>
  <h1>{type} / {id}</h1>
  {#if error}<p class="err">{error}</p>{/if}
  {#if loading}
    <p class="muted">Loading…</p>
  {:else if entity}
    <section>
      <h2>Attributes</h2>
      {#if displayEntries(entity.attributes).length === 0}
        <p class="muted">No attributes.</p>
      {:else}
        <dl class="kv">
          {#each displayEntries(entity.attributes) as [k, v] (k)}
            <dt>{k}</dt>
            <dd>{v}</dd>
          {/each}
        </dl>
      {/if}
    </section>

    {#if featureValues.length > 0}
      <section>
        <h2>Computed features</h2>
        <div class="features">
          {#each featureValues as f (f.name)}
            <span class="feat">{f.name} <b>{f.value}</b></span>
          {/each}
        </div>
      </section>
    {/if}

    <section>
      <h2>Event timeline <span class="muted">({events.length})</span></h2>
      {#if events.length === 0}
        <p class="muted">No events.</p>
      {:else}
        <ul class="timeline">
          {#each events as ev (ev.seq)}
            <li>
              <span class="ev-name">{ev.event_name}</span>
              <span class="muted"><RelativeTime value={ev.occurred_at} /></span>
              {#if ev.data}<pre>{JSON.stringify(ev.data)}</pre>{/if}
            </li>
          {/each}
        </ul>
      {/if}
    </section>
  {:else}
    <p class="muted">Entity not found.</p>
  {/if}
</main>

<style>
  main {
    max-width: 52rem;
    margin: 2rem auto;
    padding: 0 1.25rem;
  }
  section {
    margin: 1.25rem 0;
  }
  h2 {
    font-size: 1.05rem;
  }
  .kv {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 0.3rem 1rem;
    margin: 0.4rem 0;
  }
  .kv dt {
    color: var(--fg-subtle);
    font-size: 0.85rem;
  }
  .kv dd {
    margin: 0;
  }
  .features {
    display: flex;
    flex-wrap: wrap;
    gap: 0.6rem;
  }
  .feat {
    padding: 0.3rem 0.6rem;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--surface);
    font-size: 0.9rem;
  }
  .timeline {
    list-style: none;
    padding: 0;
  }
  .timeline li {
    padding: 0.5rem 0;
    border-bottom: 1px solid var(--border);
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.6rem;
  }
  .ev-name {
    font-weight: 600;
  }
  .timeline pre {
    margin: 0;
    font-size: 0.8rem;
    background: var(--surface-2);
    padding: 0.3rem 0.5rem;
    border-radius: var(--radius);
  }
  .muted {
    color: var(--fg-subtle);
  }
  .err {
    color: var(--danger);
  }
</style>
