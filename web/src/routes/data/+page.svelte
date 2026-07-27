<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { onMount } from 'svelte';
  import Icon from '$lib/Icon.svelte';
  import EmptyState from '$lib/EmptyState.svelte';
  import Skeleton from '$lib/Skeleton.svelte';
  import RelativeTime from '$lib/RelativeTime.svelte';
  import { toast } from '$lib/toast';
  import {
    listConnectors,
    defineConnector,
    fetchConnector,
    listConnectorFetches,
    listConnectorCatalog,
    listFeatures,
    defineFeature,
    listEntities,
    recordEntity,
    type Connector,
    type ConnectorFetch,
    type ConnectorTemplate,
    type Feature,
    type Entity
  } from '$lib/api';
  import { appHref } from '$lib/paths';
  import { roleAtLeast } from '$lib/roles';
  import { user } from '$lib/session';

  // API calls authenticate via the session cookie (empty key → no X-Api-Key).
  const key = '';
  let connectors = $state<Connector[]>([]);
  let features = $state<Feature[]>([]);
  let entities = $state<Entity[]>([]);
  let error = $state('');

  function msg(e: unknown): string {
    return e instanceof Error ? e.message : String(e);
  }
  function objectJSON(text: string, label: string): Record<string, unknown> {
    const parsed = JSON.parse(text || '{}') as unknown;
    if (parsed === null || Array.isArray(parsed) || typeof parsed !== 'object') {
      throw new Error(`${label} must be a JSON object.`);
    }
    return parsed as Record<string, unknown>;
  }
  let loadingData = $state(false);
  // A generation token so overlapping loads (repeated Reload, or Reload during a
  // define follow-up load) can't resolve out of order and clobber the tables.
  let loadSeq = 0;
  async function load() {
    const seq = ++loadSeq;
    loadingData = true;
    error = '';
    try {
      const [c, f, e] = await Promise.all([
        listConnectors(key),
        listFeatures(key),
        listEntities(key)
      ]);
      if (seq !== loadSeq) return; // a newer load superseded this one
      connectors = c;
      features = f;
      entities = e;
    } catch (e) {
      if (seq === loadSeq) error = msg(e);
    } finally {
      if (seq === loadSeq) loadingData = false;
    }
  }

  let cName = $state('');
  let cType = $state('');
  let cConfig = $state('');
  let cBusy = $state(false);
  let catalog = $state<ConnectorTemplate[]>([]);
  let catalogLoaded = $state(false);
  let catalogError = $state('');
  const catalogTypes = $derived([...new Set(catalog.map((t) => t.type))]);
  // Selecting a catalog template scaffolds the define form (the operator then edits
  // the placeholder URL/DSN and names it).
  function useTemplate(t: ConnectorTemplate) {
    cType = t.type;
    cConfig = JSON.stringify(t.config, null, 2);
    if (!cName.trim()) cName = t.id;
  }
  async function loadCatalog() {
    catalogLoaded = false;
    catalogError = '';
    try {
      const next = await listConnectorCatalog(key);
      catalog = next;
      catalogLoaded = true;
      if (!next.some((t) => t.type === cType)) cType = next[0]?.type ?? '';
    } catch (e) {
      catalog = [];
      cType = '';
      catalogError = msg(e);
    }
  }
  async function addConnector() {
    if (cBusy) return; // Enter fires onsubmit directly, bypassing the disabled button
    if (!catalogLoaded || !cType) {
      error = 'Connector catalog has not loaded — refusing to define an unknown type.';
      return;
    }
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

  let selectedConnector = $state('');
  let connectorParams = $state('{\n  "subject": "applicant/demo"\n}');
  let connectorResponse = $state<unknown>(null);
  let connectorFetches = $state<ConnectorFetch[]>([]);
  let connectorTestError = $state('');
  let connectorBusy = $state(false);
  async function inspectConnector(name: string) {
    selectedConnector = name;
    connectorResponse = null;
    connectorFetches = [];
    connectorTestError = '';
    connectorBusy = true;
    try {
      connectorFetches = await listConnectorFetches(key, name);
    } catch (e) {
      connectorTestError = msg(e);
    } finally {
      connectorBusy = false;
    }
  }
  async function testConnector() {
    if (!selectedConnector || connectorBusy) return;
    connectorBusy = true;
    connectorTestError = '';
    connectorResponse = null;
    try {
      const params = JSON.parse(connectorParams || '{}') as unknown;
      const result = await fetchConnector(key, selectedConnector, params);
      connectorResponse = result.response;
      connectorFetches = await listConnectorFetches(key, selectedConnector);
      toast.success(`Connector ${selectedConnector} responded and the fetch was recorded`);
    } catch (e) {
      connectorTestError = msg(e);
    } finally {
      connectorBusy = false;
    }
  }

  let fName = $state('');
  let fEntityType = $state('');
  let fEventName = $state('');
  let fAgg = $state('count');
  let fField = $state('');
  let fWindow = $state('24');
  // Every aggregation but count reads a data field.
  const fNeedsField = $derived(fAgg !== 'count');
  // Plain-language preview of the feature being defined.
  const featurePreview = $derived(
    `${fAgg}${fNeedsField ? ` of ${fField || '…'}` : ''} of "${fEventName || '…'}" events per ${fEntityType || '…'} over ${fWindow || '…'}h`
  );
  let fBusy = $state(false);
  async function addFeature() {
    if (fBusy) return; // Enter fires onsubmit directly, bypassing the disabled button
    error = '';
    // A non-numeric window is a mistake — surface it rather than silently defining
    // a 0-hour feature that will never aggregate anything. Number('') is 0 (not NaN),
    // so an empty field must be rejected explicitly; the minimum window is 1 hour.
    const windowHours = Number(fWindow.trim());
    if (fWindow.trim() === '' || !Number.isInteger(windowHours) || windowHours < 1) {
      error = 'Window (hours) must be a whole number of at least 1.';
      return;
    }
    fBusy = true;
    try {
      const body = {
        name: fName.trim(),
        entity_type: fEntityType.trim(),
        event_name: fEventName.trim(),
        aggregation: fAgg,
        field: fField.trim() || undefined,
        window_hours: windowHours
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

  let entityType = $state('');
  let entityID = $state('');
  let entityAttributes = $state('{}');
  let entityBusy = $state(false);
  async function addEntity() {
    if (entityBusy) return;
    error = '';
    entityBusy = true;
    try {
      const attributes = objectJSON(entityAttributes, 'Attributes');
      const nextType = entityType.trim();
      const nextID = entityID.trim();
      await recordEntity(key, {
        entity_type: nextType,
        entity_id: nextID,
        attributes
      });
      const nextEntities = await listEntities(key);
      const recorded = nextEntities.find(
        (record) => record.entity_type === nextType && record.entity_id === nextID
      );
      if (
        !recorded ||
        !Object.entries(attributes).every(
          ([name, value]) => JSON.stringify(recorded.attributes[name]) === JSON.stringify(value)
        )
      ) {
        throw new Error(
          `Entity ${nextType}/${nextID} was applied without its recorded attributes.`
        );
      }
      toast.success(`Entity ${nextType}/${nextID} recorded`);
      entityID = '';
      entityAttributes = '{}';
      entities = nextEntities;
    } catch (e) {
      error = msg(e);
    } finally {
      entityBusy = false;
    }
  }

  onMount(() => {
    void load();
    void loadCatalog();
  });
</script>

<main>
  <div class="head">
    <h1><Icon name="database" size={20} /> Context data</h1>
    <button onclick={load} disabled={loadingData}
      ><Icon name="reload" size={15} /> {loadingData ? 'Loading…' : 'Reload'}</button
    >
  </div>
  <p class="muted">
    Connectors and features are the data a flow leans on — a Connect node calls a connector by name,
    and Rule/Split nodes read <code>features.*</code>. Define them here.
  </p>
  {#if error}<p class="err">{error}</p>{/if}

  <section>
    <h2>Connectors</h2>
    {#if catalog.length > 0}
      <div class="catalog" data-testid="connector-catalog">
        <span class="catalog-label">Start from a template:</span>
        {#each catalog as t (t.id)}
          <button class="chip" title={t.description} onclick={() => useTemplate(t)}>{t.name}</button
          >
        {/each}
      </div>
    {/if}
    {#if catalogError}
      <p class="err" data-testid="connector-catalog-error">
        Connector catalog unavailable: {catalogError}
        <button class="link" onclick={loadCatalog}>Retry</button>
      </p>
    {:else if catalogLoaded && catalog.length === 0}
      <p class="err">This deployment exposes no connector types.</p>
    {/if}
    <form
      class="row"
      onsubmit={(e) => {
        e.preventDefault();
        void addConnector();
      }}
    >
      <input bind:value={cName} placeholder="name" aria-label="connector name" size="14" required />
      <select bind:value={cType} aria-label="connector type">
        {#each catalogTypes as connectorType (connectorType)}
          <option value={connectorType}>{connectorType}</option>
        {/each}
      </select>
      <input
        bind:value={cConfig}
        placeholder={'config JSON e.g. {"url":"https://…"}'}
        aria-label="connector config"
        size="34"
      />
      <button
        type="submit"
        disabled={cBusy || !catalogLoaded || !cType || !roleAtLeast($user?.role, 'editor')}
        title={!roleAtLeast($user?.role, 'editor') ? 'Requires the editor role' : undefined}
        >{cBusy ? 'Saving…' : 'Define connector'}</button
      >
    </form>
    {#if loadingData && connectors.length === 0}
      <Skeleton rows={3} />
    {:else if connectors.length === 0}
      <!-- Onboard only on a successful empty load; a failed load surfaces `error`
           above, so an errored workspace isn't misread as fresh. -->
      {#if !error}
        <EmptyState
          icon="database"
          title="No connectors yet"
          hint="Define one above — a Connect node calls a connector by name to pull data into a decision."
        />
      {/if}
    {:else}
      <div class="table-wrap">
        <table>
          <thead>
            <tr><th>Name</th><th>Type</th><th>Config</th><th>Updated</th><th></th></tr>
          </thead>
          <tbody>
            {#each connectors as c (c.name)}
              <tr>
                <td>{c.name}</td>
                <td><span class="badge">{c.type}</span></td>
                <td class="config">{c.config ? JSON.stringify(c.config) : '—'}</td>
                <td class="muted"><RelativeTime value={c.updated_at} /></td>
                <td>
                  <button class="link" onclick={() => inspectConnector(c.name)}
                    >Inspect / test</button
                  >
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
    {#if selectedConnector}
      <div class="connector-test" data-testid="connector-test">
        <div class="panel-head">
          <h3>Test {selectedConnector}</h3>
          <button class="link" onclick={() => (selectedConnector = '')}>Close</button>
        </div>
        <p class="muted">
          Calls the real configured source through its timeout, retry, and circuit-breaker path. A
          successful response is recorded in the connector's immutable fetch history.
        </p>
        <textarea bind:value={connectorParams} rows="4" aria-label="connector test parameters"
        ></textarea>
        <button
          onclick={testConnector}
          disabled={connectorBusy || !roleAtLeast($user?.role, 'editor')}
          title={!roleAtLeast($user?.role, 'editor') ? 'Requires the editor role' : undefined}
          >{connectorBusy ? 'Testing…' : 'Test connector'}</button
        >
        {#if connectorTestError}
          <p class="err" data-testid="connector-test-error">{connectorTestError}</p>
        {/if}
        {#if connectorResponse !== null}
          <pre data-testid="connector-response">{JSON.stringify(connectorResponse, null, 2)}</pre>
        {/if}
        <h4>
          Recorded fetches
          <span class="muted" data-testid="connector-fetch-count">({connectorFetches.length})</span>
        </h4>
        {#if connectorFetches.length === 0 && !connectorBusy}
          <p class="muted">No successful fetches recorded for this connector.</p>
        {:else}
          <ul class="fetches">
            {#each connectorFetches.slice(0, 5) as f (f.fetch_id)}
              <li>
                <RelativeTime value={f.at} />
                <code>{f.fetch_id.slice(0, 12)}</code>
                <pre>{JSON.stringify(f.response, null, 2)}</pre>
              </li>
            {/each}
          </ul>
        {/if}
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
      <label
        >Name <input
          bind:value={fName}
          placeholder="txns_24h"
          aria-label="feature name"
          size="12"
          required
        /></label
      >
      <label
        >Entity <input
          bind:value={fEntityType}
          placeholder="applicant"
          aria-label="feature entity type"
          size="10"
          required
        /></label
      >
      <label
        >Event <input
          bind:value={fEventName}
          placeholder="transaction"
          aria-label="feature event name"
          size="10"
          required
        /></label
      >
      <label
        >Aggregation
        <select bind:value={fAgg} aria-label="feature aggregation">
          <option value="count">count</option>
          <option value="sum">sum</option>
          <option value="avg">avg</option>
          <option value="min">min</option>
          <option value="max">max</option>
          <option value="last">last</option>
          <option value="first">first</option>
          <option value="count_distinct">count_distinct</option>
        </select></label
      >
      <label
        >Field {fNeedsField ? '(required)' : '(unused for count)'}
        <input
          bind:value={fField}
          placeholder="amount"
          aria-label="feature field"
          size="8"
          required={fNeedsField}
        /></label
      >
      <label
        >Window (hours) <input
          bind:value={fWindow}
          placeholder="24"
          aria-label="feature window hours"
          size="6"
          inputmode="numeric"
        /></label
      >
      <button
        type="submit"
        disabled={fBusy || !roleAtLeast($user?.role, 'editor')}
        title={!roleAtLeast($user?.role, 'editor') ? 'Requires the editor role' : undefined}
        >{fBusy ? 'Saving…' : 'Define feature'}</button
      >
    </form>
    <p class="muted preview" data-testid="feature-preview">{featurePreview}</p>
    {#if loadingData && features.length === 0}
      <Skeleton rows={3} />
    {:else if features.length === 0}
      {#if !error}
        <EmptyState
          icon="database"
          title="No features yet"
          hint="Define one above — Rule and Split nodes read features.* to make data-driven decisions."
        />
      {/if}
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
    <form
      class="row entity-form"
      onsubmit={(e) => {
        e.preventDefault();
        void addEntity();
      }}
    >
      <label
        >Type <input
          bind:value={entityType}
          placeholder="applicant"
          aria-label="entity type"
          required
        /></label
      >
      <label
        >ID <input
          bind:value={entityID}
          placeholder="APP-123"
          aria-label="entity id"
          required
        /></label
      >
      <label class="attributes"
        >Attributes (JSON)
        <input
          bind:value={entityAttributes}
          placeholder={'{"tier":"gold"}'}
          aria-label="entity attributes"
        /></label
      >
      <button
        type="submit"
        disabled={entityBusy || !roleAtLeast($user?.role, 'editor')}
        title={!roleAtLeast($user?.role, 'editor') ? 'Requires the editor role' : undefined}
        >{entityBusy ? 'Recording…' : 'Create or update entity'}</button
      >
    </form>
    {#if loadingData && entities.length === 0}
      <Skeleton rows={3} />
    {:else if entities.length === 0}
      {#if !error}
        <EmptyState
          icon="database"
          title="No entities yet"
          hint="Create one above, or let a decision or event record it automatically."
        />
      {/if}
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
                    href={appHref(
                      `/data/${encodeURIComponent(e.entity_type)}/${encodeURIComponent(e.entity_id)}`
                    )}>{e.entity_id}</a
                  ></td
                >
                <td>{e.event_count}</td>
                <td class="muted"><RelativeTime value={e.updated_at} /></td>
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
  .catalog {
    display: flex;
    flex-wrap: wrap;
    gap: 0.4rem;
    align-items: center;
    margin: 0.4rem 0;
  }
  .catalog-label {
    font-size: 0.82rem;
    color: var(--fg-subtle);
  }
  .chip {
    padding: 0.2rem 0.6rem;
    border: 1px solid var(--border);
    border-radius: 999px;
    background: var(--surface-2);
    color: var(--fg);
    font-size: 0.82rem;
    cursor: pointer;
  }
  .chip:hover {
    border-color: var(--accent);
    color: var(--accent-ink, var(--accent));
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
    font-family: var(--font-mono);
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
  .preview {
    font-style: italic;
    font-size: 0.85rem;
    margin: 0.3rem 0 0;
  }
  .connector-test {
    margin-top: 0.8rem;
    padding: 0.8rem;
    border: 1px solid var(--border);
    border-radius: 0.65rem;
    background: var(--surface-1);
  }
  .panel-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
  }
  .panel-head h3,
  .connector-test h4 {
    margin: 0.2rem 0;
  }
  .connector-test textarea,
  .connector-test pre {
    width: 100%;
    box-sizing: border-box;
  }
  .connector-test pre {
    overflow: auto;
    padding: 0.55rem;
    border-radius: 0.4rem;
    background: var(--surface-2);
    white-space: pre-wrap;
  }
  .fetches {
    padding-left: 1.2rem;
  }
  .fetches li {
    margin: 0.45rem 0;
  }
  .entity-form .attributes {
    flex: 1 1 18rem;
  }
  .row label {
    display: inline-flex;
    flex-direction: column;
    gap: 0.15rem;
    margin: 0;
    font-size: 0.74rem;
    color: var(--fg-subtle);
  }
  .err {
    color: var(--danger);
  }
</style>
