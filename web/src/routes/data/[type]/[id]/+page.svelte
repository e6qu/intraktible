<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { page } from '$app/stores';
  import { displayEntries } from '$lib/kv';
  import CommentThread from '$lib/CommentThread.svelte';
  import EmptyState from '$lib/EmptyState.svelte';
  import RelativeTime from '$lib/RelativeTime.svelte';
  import {
    getEntity,
    listEntityEvents,
    getEntityFeatures,
    type Entity,
    type EntityEvent,
    type FeatureValue
  } from '$lib/api';
  import { appHref } from '$lib/paths';

  const key = '';
  // Derive from the route params so navigating between sibling entities reloads.
  const type = $derived($page.params.type ?? '');
  const id = $derived($page.params.id ?? '');

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
    // Clear the prior entity so a failed reload can't leave the previous entity's
    // attributes/events on screen under an error.
    entity = null;
    events = [];
    featureValues = [];
    // Drop a stale response when sibling navigation changes type/id mid-flight.
    const reqType = type;
    const reqId = id;
    try {
      const [ent, evs, feats] = await Promise.all([
        getEntity(key, type, id),
        listEntityEvents(key, type, id),
        // Computed features are best-effort (none defined for this type is fine).
        getEntityFeatures(key, type, id).catch(() => [])
      ]);
      if (type !== reqType || id !== reqId) return;
      [entity, events, featureValues] = [ent, evs, feats];
    } catch (e) {
      if (type === reqType && id === reqId) error = msg(e);
    } finally {
      if (type === reqType && id === reqId) loading = false;
    }
  }
  $effect(() => {
    void type;
    void id; // reload on initial mount and sibling navigation
    void load();
  });
</script>

<main>
  <p><a href={appHref('/data')}>← context data</a></p>
  <h1>{type} / {id}</h1>
  {#if loading}
    <p class="muted">Loading…</p>
  {:else if error}
    <p class="err">{error} <button class="link" onclick={() => load()}>Retry</button></p>
  {:else if entity}
    <section>
      <h2>Attributes</h2>
      {#if displayEntries(entity.attributes).length === 0}
        <EmptyState
          icon="database"
          title="No attributes"
          hint="This entity has no stored attributes yet — they accrue as decisions and events reference it."
        />
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
        <EmptyState
          icon="diagram"
          title="No events"
          hint="No events have been recorded for this entity. Events appear as the workspace records activity against it."
        />
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

    <section>
      <h2>Discussion</h2>
      <p class="muted disc-hint">
        Discuss this entity's data with the team — @mention a colleague to notify them.
      </p>
      <!-- Subject key matches the seeder's convention: "<type>/<id>", one escaped
           path segment on the wire (encodeURIComponent in the API client). -->
      <CommentThread subjectType="entity" subjectId={`${type}/${id}`} title="Entity discussion" />
    </section>
  {:else}
    <EmptyState
      icon="database"
      title="Entity not found"
      hint="No entity matches this type and id. It may not exist yet, or the id may be mistyped."
    />
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
  .disc-hint {
    margin: 0.2rem 0 0;
    font-size: 0.85rem;
  }
  .err {
    color: var(--danger);
  }
  button.link {
    background: none;
    border: none;
    color: var(--accent);
    cursor: pointer;
    padding: 0.2rem;
    font: inherit;
  }
</style>
