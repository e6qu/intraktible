<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { onMount } from 'svelte';
  import Icon from '$lib/Icon.svelte';
  import { toast } from '$lib/toast';
  import {
    listConnectors,
    defineConnector,
    listFeatures,
    defineFeature,
    listEntities,
    type Connector,
    type Feature,
    type Entity
  } from '$lib/api';

  // API calls authenticate via the session cookie (empty key → no X-Api-Key).
  const key = '';
  let connectors = $state<Connector[]>([]);
  let features = $state<Feature[]>([]);
  let entities = $state<Entity[]>([]);
  let error = $state('');

  function msg(e: unknown): string {
    return e instanceof Error ? e.message : String(e);
  }
  async function load() {
    error = '';
    try {
      [connectors, features, entities] = await Promise.all([
        listConnectors(key),
        listFeatures(key),
        listEntities(key)
      ]);
    } catch (e) {
      error = msg(e);
    }
  }

  // --- Connector define form ---
  let cName = $state('');
  let cType = $state('mock_bureau');
  let cConfig = $state('');
  let cBusy = $state(false);
  async function addConnector() {
    error = '';
    cBusy = true;
    try {
      const body: { name: string; type: string; config?: unknown } = {
        name: cName.trim(),
        type: cType
      };
      if (cConfig.trim()) body.config = JSON.parse(cConfig);
      await defineConnector(key, body);
      toast.success(`Connector ${cName} defined`);
      cName = '';
      cConfig = '';
      await load();
    } catch (e) {
      error = msg(e);
    } finally {
      cBusy = false;
    }
  }

  // --- Feature define form ---
  let fName = $state('');
  let fEntityType = $state('');
  let fEventName = $state('');
  let fAgg = $state('count');
  let fField = $state('');
  let fWindow = $state('24');
  let fBusy = $state(false);
  async function addFeature() {
    error = '';
    fBusy = true;
    try {
      const body = {
        name: fName.trim(),
        entity_type: fEntityType.trim(),
        event_name: fEventName.trim(),
        aggregation: fAgg,
        field: fField.trim() || undefined,
        window_hours: parseInt(fWindow, 10) || 0
      };
      await defineFeature(key, body);
      toast.success(`Feature ${fName} defined`);
      fName = '';
      fField = '';
      await load();
    } catch (e) {
      error = msg(e);
    } finally {
      fBusy = false;
    }
  }

  onMount(load);
</script>

<main>
  <div class="head">
    <h1><Icon name="database" size={20} /> Context data</h1>
    <button onclick={load}><Icon name="reload" size={15} /> Reload</button>
  </div>
  <p class="muted">
    Connectors and features are the data a flow leans on — a Connect node calls a connector by name,
    and Rule/Split nodes read <code>features.*</code>. Define them here.
  </p>
  {#if error}<p class="err">{error}</p>{/if}

  <section>
    <h2>Connectors</h2>
    <form
      class="row"
      onsubmit={(e) => {
        e.preventDefault();
        void addConnector();
      }}
    >
      <input bind:value={cName} placeholder="name" aria-label="connector name" size="14" required />
      <select bind:value={cType} aria-label="connector type">
        <option value="mock_bureau">mock_bureau</option>
        <option value="http">http</option>
        <option value="sql">sql</option>
      </select>
      <input
        bind:value={cConfig}
        placeholder={'config JSON e.g. {"url":"https://…"}'}
        aria-label="connector config"
        size="34"
      />
      <button type="submit" disabled={cBusy}>{cBusy ? 'Saving…' : 'Define connector'}</button>
    </form>
    {#if connectors.length === 0}
      <p class="muted">No connectors yet.</p>
    {:else}
      <div class="table-wrap">
        <table>
          <thead>
            <tr><th>Name</th><th>Type</th><th>Config</th><th>Updated</th></tr>
          </thead>
          <tbody>
            {#each connectors as c (c.name)}
              <tr>
                <td>{c.name}</td>
                <td><span class="badge">{c.type}</span></td>
                <td class="config">{c.config ? JSON.stringify(c.config) : '—'}</td>
                <td class="muted">{new Date(c.updated_at).toLocaleString()}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </section>

  <section>
    <h2>Features</h2>
    <form
      class="row"
      onsubmit={(e) => {
        e.preventDefault();
        void addFeature();
      }}
    >
      <input bind:value={fName} placeholder="name" aria-label="feature name" size="14" required />
      <input
        bind:value={fEntityType}
        placeholder="entity type"
        aria-label="feature entity type"
        size="12"
        required
      />
      <input
        bind:value={fEventName}
        placeholder="event name"
        aria-label="feature event name"
        size="12"
        required
      />
      <select bind:value={fAgg} aria-label="feature aggregation">
        <option value="count">count</option>
        <option value="sum">sum</option>
      </select>
      <input bind:value={fField} placeholder="field (sum)" aria-label="feature field" size="10" />
      <input
        bind:value={fWindow}
        placeholder="window h"
        aria-label="feature window hours"
        size="8"
        inputmode="numeric"
      />
      <button type="submit" disabled={fBusy}>{fBusy ? 'Saving…' : 'Define feature'}</button>
    </form>
    {#if features.length === 0}
      <p class="muted">No features yet.</p>
    {:else}
      <div class="table-wrap">
        <table>
          <thead>
            <tr><th>Name</th><th>Entity</th><th>Event</th><th>Agg</th><th>Window</th></tr>
          </thead>
          <tbody>
            {#each features as f (f.name)}
              <tr>
                <td>{f.name}</td>
                <td>{f.entity_type}</td>
                <td>{f.event_name}</td>
                <td>{f.aggregation}{f.field ? `(${f.field})` : ''}</td>
                <td class="muted">{f.window_hours}h</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </section>

  <section>
    <h2>Entities</h2>
    {#if entities.length === 0}
      <p class="muted">
        No entities yet. They are created when a decision references one, or via the API.
      </p>
    {:else}
      <div class="table-wrap">
        <table>
          <thead>
            <tr><th>Type</th><th>ID</th><th>Events</th><th>Updated</th></tr>
          </thead>
          <tbody>
            {#each entities as e (e.entity_type + '/' + e.entity_id)}
              <tr>
                <td>{e.entity_type}</td>
                <td
                  ><a
                    href={`/data/${encodeURIComponent(e.entity_type)}/${encodeURIComponent(e.entity_id)}`}
                    >{e.entity_id}</a
                  ></td
                >
                <td>{e.event_count}</td>
                <td class="muted">{new Date(e.updated_at).toLocaleString()}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </section>
</main>

<style>
  main {
    max-width: 64rem;
    margin: 2rem auto;
    padding: 0 1.25rem;
  }
  .head {
    display: flex;
    align-items: center;
    justify-content: space-between;
  }
  h1 {
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }
  section {
    margin: 1.5rem 0;
  }
  h2 {
    font-size: 1.05rem;
    margin-bottom: 0.4rem;
  }
  .row {
    display: flex;
    flex-wrap: wrap;
    gap: 0.5rem;
    align-items: center;
    margin: 0.5rem 0;
  }
  table {
    width: 100%;
    border-collapse: collapse;
    margin-top: 0.4rem;
  }
  th {
    text-align: left;
    font-size: 0.78rem;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--fg-subtle);
    padding: 0.45rem 0.6rem;
    border-bottom: 1px solid var(--border);
  }
  td {
    padding: 0.5rem 0.6rem;
    border-bottom: 1px solid var(--border);
    font-size: 0.9rem;
  }
  .badge {
    display: inline-block;
    padding: 0.1rem 0.5rem;
    border-radius: 999px;
    font-size: 0.78rem;
    background: var(--surface-2);
    color: var(--fg-muted);
  }
  code {
    background: var(--surface-2);
    padding: 0 0.3rem;
    border-radius: 0.3rem;
  }
  .config {
    font-family: ui-monospace, monospace;
    font-size: 0.8rem;
    color: var(--fg-muted);
    max-width: 22rem;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .muted {
    color: var(--fg-subtle);
  }
  .err {
    color: var(--danger);
  }
</style>
