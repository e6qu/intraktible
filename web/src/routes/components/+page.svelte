<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { onMount } from 'svelte';
  import {
    createReusableComponent,
    createComponentUpgradeDrafts,
    listReusableComponents,
    listComponentConsumers,
    publishReusableComponent,
    retireReusableComponent,
    type ComponentConsumer,
    type FlowGraph,
    type ReusableComponent
  } from '$lib/api';
  import EmptyState from '$lib/EmptyState.svelte';
  import Icon from '$lib/Icon.svelte';
  import Skeleton from '$lib/Skeleton.svelte';
  import { roleAtLeast } from '$lib/roles';
  import { user } from '$lib/session';
  import { toast } from '$lib/toast';

  const key = '';
  let components = $state<ReusableComponent[]>([]);
  let consumers = $state(new Map<string, ComponentConsumer[]>());
  let loading = $state(true);
  let error = $state('');
  let busy = $state(false);
  let slug = $state('');
  let name = $state('');
  let description = $state('');
  let selectedId = $state('');
  let graphText = $state(
    JSON.stringify(
      {
        nodes: [
          { id: 'input', type: 'input' },
          { id: 'rule', type: 'rule', name: 'Reusable logic' },
          { id: 'output', type: 'output' }
        ],
        edges: [
          { from: 'input', to: 'rule' },
          { from: 'rule', to: 'output' }
        ]
      },
      null,
      2
    )
  );
  let inputSchemaText = $state('{}');
  let outputSchemaText = $state('{}');
  let allowBreaking = $state(false);
  let breakingReason = $state('');
  let selectedUpgrades = $state(new Set<string>());
  let retireConfirmId = $state('');

  const selected = $derived(
    components.find((component) => component.component_id === selectedId) ?? null
  );

  function msg(value: unknown): string {
    return value instanceof Error ? value.message : String(value);
  }

  async function load() {
    loading = true;
    error = '';
    try {
      components = await listReusableComponents(key);
      if (selectedId && !components.some((component) => component.component_id === selectedId)) {
        selectedId = '';
      }
      const impact = await Promise.all(
        components.flatMap((component) =>
          (component.versions ?? []).map(async (version) => ({
            key: `${component.component_id}@${version.version}`,
            items: await listComponentConsumers(key, component.component_id, version.version)
          }))
        )
      );
      consumers = new Map(impact.map((entry) => [entry.key, entry.items]));
    } catch (e) {
      error = msg(e);
    } finally {
      loading = false;
    }
  }

  async function create() {
    if (busy || !slug.trim() || !name.trim()) return;
    busy = true;
    try {
      const result = await createReusableComponent(key, {
        slug: slug.trim(),
        name: name.trim(),
        description: description.trim() || undefined
      });
      slug = '';
      name = '';
      description = '';
      selectedId = result.component_id;
      toast.success('Reusable component created');
      await load();
    } catch (e) {
      toast.error(msg(e));
    } finally {
      busy = false;
    }
  }

  function parseJSON(label: string, value: string): unknown {
    try {
      return JSON.parse(value);
    } catch (_parseError) {
      throw new Error(`${label} is not valid JSON`);
    }
  }

  function compatibleTarget(component: ReusableComponent, fromVersion: number): number {
    return (
      component.versions?.find(
        (version) =>
          version.compatibility.from_version === fromVersion &&
          version.compatibility.status === 'compatible'
      )?.version ?? 0
    );
  }

  function upgradeKey(componentId: string, version: number, flowId: string): string {
    return `${componentId}@${version}:${flowId}`;
  }

  function toggleUpgrade(componentId: string, version: number, flowId: string) {
    const next = new Set(selectedUpgrades);
    const item = upgradeKey(componentId, version, flowId);
    if (next.has(item)) next.delete(item);
    else next.add(item);
    selectedUpgrades = next;
  }

  async function createUpgradeDrafts(
    component: ReusableComponent,
    fromVersion: number,
    toVersion: number
  ) {
    if (busy) return;
    const flowIds = (consumers.get(`${component.component_id}@${fromVersion}`) ?? [])
      .filter(
        (consumer) =>
          consumer.consumer_kind === 'flow' &&
          selectedUpgrades.has(
            upgradeKey(component.component_id, fromVersion, consumer.consumer_id)
          )
      )
      .map((consumer) => consumer.consumer_id);
    if (flowIds.length === 0) return;
    busy = true;
    try {
      const result = await createComponentUpgradeDrafts(key, component.component_id, {
        from_version: fromVersion,
        to_version: toVersion,
        flow_ids: flowIds,
        title: `Upgrade ${component.name} from v${fromVersion} to v${toVersion}`
      });
      selectedUpgrades = new Set(
        [...selectedUpgrades].filter(
          (item) =>
            !flowIds.some(
              (flowId) => item === upgradeKey(component.component_id, fromVersion, flowId)
            )
        )
      );
      toast.success(
        `Created ${result.drafts.length} governed upgrade draft${result.drafts.length === 1 ? '' : 's'}`
      );
    } catch (e) {
      toast.error(msg(e));
    } finally {
      busy = false;
    }
  }

  async function publishVersion() {
    if (!selected || busy) return;
    busy = true;
    try {
      const graph = parseJSON('Graph', graphText) as FlowGraph;
      const result = await publishReusableComponent(key, selected.component_id, {
        graph,
        input_schema: parseJSON('Input schema', inputSchemaText),
        output_schema: parseJSON('Output schema', outputSchemaText),
        allow_breaking: allowBreaking || undefined,
        breaking_change_reason: allowBreaking ? breakingReason.trim() : undefined
      });
      allowBreaking = false;
      breakingReason = '';
      toast.success(`Published ${selected.name} v${result.version}`);
      await load();
    } catch (e) {
      toast.error(msg(e));
    } finally {
      busy = false;
    }
  }

  async function retire(component: ReusableComponent) {
    if (busy) return;
    busy = true;
    try {
      await retireReusableComponent(key, component.component_id);
      retireConfirmId = '';
      toast.success(`${component.name} retired`);
      await load();
    } catch (e) {
      toast.error(msg(e));
    } finally {
      busy = false;
    }
  }

  onMount(load);
</script>

<main>
  <header>
    <div>
      <h1>Reusable components</h1>
      <p>Publish exact-version decision subflows, inspect every consumer, and retire safely.</p>
    </div>
    <button onclick={load} disabled={loading}><Icon name="reload" size={15} /> Reload</button>
  </header>

  {#if roleAtLeast($user?.role, 'editor')}
    <form
      class="create"
      onsubmit={(event) => {
        event.preventDefault();
        void create();
      }}
    >
      <label>Slug <input bind:value={slug} placeholder="affordability-gate" /></label>
      <label>Name <input bind:value={name} placeholder="Affordability gate" /></label>
      <label
        >Description <input
          bind:value={description}
          placeholder="Shared underwriting logic"
        /></label
      >
      <button class="primary" disabled={busy || !slug.trim() || !name.trim()}
        >Create component</button
      >
    </form>
  {/if}

  {#if error}<p class="err">{error}</p>{/if}
  {#if loading}
    <Skeleton rows={5} />
  {:else if components.length === 0}
    <EmptyState
      icon="subflow"
      title="No reusable components"
      hint="Create a component, publish its first immutable version, then pin it from a flow's Reusable component node."
    />
  {:else}
    <div class="workspace">
      <section class="registry">
        <h2>Registry</h2>
        {#each components as component (component.component_id)}
          <article class:selected={component.component_id === selectedId}>
            <button class="open" onclick={() => (selectedId = component.component_id)}>
              <span>
                <b>{component.name}</b>
                <small>{component.slug}</small>
              </span>
              <span>v{component.latest}{component.retired ? ' · retired' : ''}</span>
            </button>
            {#if component.description}<p>{component.description}</p>{/if}
            {#each component.versions ?? [] as version (version.version)}
              <div class="version">
                <code>v{version.version} · {version.etag.slice(0, 10)}</code>
                <span>
                  {#if version.compatibility.status === 'incompatible'}
                    <strong class="blocked">migration required</strong> ·
                  {:else if version.compatibility.status === 'compatible'}
                    <strong class="safe">compatible</strong> ·
                  {/if}
                  {(consumers.get(`${component.component_id}@${version.version}`) ?? []).length}
                  consumer{(consumers.get(`${component.component_id}@${version.version}`) ?? [])
                    .length === 1
                    ? ''
                    : 's'}
                </span>
              </div>
              {#if version.compatibility.status === 'incompatible'}
                <div class="compatibility">
                  <b>Automatic upgrades from v{version.compatibility.from_version} are blocked.</b>
                  {#if version.breaking_change_reason}<p>{version.breaking_change_reason}</p>{/if}
                  <ul>
                    {#each version.compatibility.issues ?? [] as issue (`${issue.path}:${issue.code}`)}
                      <li><code>{issue.path}</code> — {issue.message}</li>
                    {/each}
                  </ul>
                </div>
              {/if}
              {#if (consumers.get(`${component.component_id}@${version.version}`) ?? []).length}
                <ul>
                  {#each consumers.get(`${component.component_id}@${version.version}`) ?? [] as consumer (`${consumer.consumer_kind}:${consumer.consumer_id}:${consumer.consumer_version}`)}
                    <li>
                      {#if consumer.consumer_kind === 'flow' && compatibleTarget(component, version.version)}
                        <label class="consumer-select">
                          <input
                            type="checkbox"
                            checked={selectedUpgrades.has(
                              upgradeKey(
                                component.component_id,
                                version.version,
                                consumer.consumer_id
                              )
                            )}
                            onchange={() =>
                              toggleUpgrade(
                                component.component_id,
                                version.version,
                                consumer.consumer_id
                              )}
                          />
                          flow <code>{consumer.consumer_id}</code> v{consumer.consumer_version}
                        </label>
                      {:else}
                        {consumer.consumer_kind} <code>{consumer.consumer_id}</code>
                        v{consumer.consumer_version}
                        {#if consumer.consumer_kind === 'component'}
                          <small>upgrade the direct parent component first</small>
                        {/if}
                      {/if}
                    </li>
                  {/each}
                </ul>
                {#if compatibleTarget(component, version.version)}
                  <button
                    onclick={() =>
                      createUpgradeDrafts(
                        component,
                        version.version,
                        compatibleTarget(component, version.version)
                      )}
                    disabled={busy ||
                      !roleAtLeast($user?.role, 'editor') ||
                      !(consumers.get(`${component.component_id}@${version.version}`) ?? []).some(
                        (consumer) =>
                          consumer.consumer_kind === 'flow' &&
                          selectedUpgrades.has(
                            upgradeKey(
                              component.component_id,
                              version.version,
                              consumer.consumer_id
                            )
                          )
                      )}
                  >
                    Create selected upgrade drafts to v{compatibleTarget(
                      component,
                      version.version
                    )}
                  </button>
                {/if}
              {/if}
            {/each}
            {#if !component.retired && roleAtLeast($user?.role, 'editor')}
              {#if retireConfirmId === component.component_id}
                <div class="retire-confirm" role="group" aria-label={`Retire ${component.name}`}>
                  <span>
                    Existing exact pins keep working, but no new versions can be published.
                  </span>
                  <button class="danger" onclick={() => retire(component)} disabled={busy}>
                    {busy ? 'Retiring…' : 'Confirm retire'}
                  </button>
                  <button onclick={() => (retireConfirmId = '')} disabled={busy}>Keep active</button
                  >
                </div>
              {:else}
                <button
                  class="danger"
                  onclick={() => (retireConfirmId = component.component_id)}
                  disabled={busy}>Retire</button
                >
              {/if}
            {/if}
          </article>
        {/each}
      </section>

      <section class="publisher">
        <h2>Publish immutable version</h2>
        {#if !selected}
          <p class="muted">Choose a component from the registry.</p>
        {:else if selected.retired}
          <p class="warn">This component is retired. Its existing versions remain resolvable.</p>
        {:else}
          <p>
            Publishing <b>{selected.name}</b> as v{selected.latest + 1}. Nested component references
            are expanded recursively and cycles are rejected.
          </p>
          <label>Source graph <textarea bind:value={graphText} rows="14"></textarea></label>
          <div class="schemas">
            <label>Input schema <textarea bind:value={inputSchemaText} rows="6"></textarea></label>
            <label>Output schema <textarea bind:value={outputSchemaText} rows="6"></textarea></label
            >
          </div>
          {#if selected.latest > 0}
            <label class="breaking">
              <input type="checkbox" bind:checked={allowBreaking} />
              Publish an explicitly breaking contract for a coordinated migration
            </label>
            {#if allowBreaking}
              <label>
                Migration reason
                <textarea
                  bind:value={breakingReason}
                  rows="3"
                  placeholder="Explain what consumers must change before upgrading their exact pin."
                ></textarea>
              </label>
              <p class="warn">
                The server still computes compatibility. This acknowledgement permits the immutable
                version to exist; it does not make automatic consumer upgrades safe.
              </p>
            {/if}
          {/if}
          <button
            class="primary"
            onclick={publishVersion}
            disabled={busy ||
              !roleAtLeast($user?.role, 'editor') ||
              (allowBreaking && !breakingReason.trim())}
            >{busy ? 'Publishing…' : `Publish v${selected.latest + 1}`}</button
          >
        {/if}
      </section>
    </div>
  {/if}
</main>

<style>
  main {
    max-width: 1180px;
    margin: 0 auto;
    padding: 1.4rem;
  }
  header,
  .open,
  .version {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.8rem;
  }
  .safe {
    color: var(--ok);
  }
  .blocked {
    color: var(--danger);
  }
  .compatibility {
    margin: 0.45rem 0 0.75rem;
    padding: 0.65rem;
    border: 1px solid color-mix(in srgb, var(--danger) 35%, var(--border));
    border-radius: var(--radius-sm);
    background: color-mix(in srgb, var(--danger) 7%, transparent);
    font-size: 0.82rem;
  }
  .compatibility p,
  .compatibility ul {
    margin: 0.35rem 0 0;
  }
  .consumer-select {
    display: inline-flex;
    flex-direction: row;
    align-items: center;
    gap: 0.4rem;
  }
  .breaking {
    display: flex;
    flex-direction: row;
    align-items: center;
    gap: 0.55rem;
  }
  .retire-confirm {
    display: grid;
    grid-template-columns: minmax(12rem, 1fr) auto auto;
    align-items: center;
    gap: 0.5rem;
    margin-top: 0.55rem;
    padding: 0.55rem;
    border: 1px solid color-mix(in srgb, var(--danger) 35%, var(--border));
    border-radius: var(--radius-sm);
    font-size: 0.78rem;
  }
  h1,
  header p {
    margin: 0;
  }
  header p {
    color: var(--fg-muted);
  }
  .create {
    display: grid;
    grid-template-columns: repeat(3, minmax(10rem, 1fr)) auto;
    align-items: end;
    gap: 0.7rem;
    margin: 1rem 0;
    padding: 0.8rem;
    border: 1px solid var(--border);
    border-radius: 10px;
  }
  label {
    display: grid;
    gap: 0.3rem;
    color: var(--fg-muted);
    font-size: 0.8rem;
  }
  .workspace {
    display: grid;
    grid-template-columns: minmax(18rem, 0.8fr) minmax(24rem, 1.2fr);
    gap: 1rem;
  }
  .registry,
  .publisher {
    padding: 0.9rem;
    border: 1px solid var(--border);
    border-radius: 10px;
    background: var(--surface);
  }
  h2 {
    margin-top: 0;
    font-size: 1rem;
  }
  article {
    margin-bottom: 0.6rem;
    padding: 0.7rem;
    border: 1px solid var(--border);
    border-radius: 8px;
  }
  article.selected {
    border-color: var(--accent);
  }
  .open {
    width: 100%;
    padding: 0;
    border: 0;
    background: transparent;
    color: inherit;
    text-align: left;
  }
  .open span:first-child {
    display: grid;
  }
  small {
    color: var(--fg-muted);
  }
  .version {
    margin-top: 0.45rem;
    padding-top: 0.45rem;
    border-top: 1px solid var(--border);
    font-size: 0.78rem;
  }
  ul {
    margin: 0.35rem 0;
    padding-left: 1.2rem;
    color: var(--fg-muted);
    font-size: 0.78rem;
  }
  .danger {
    color: var(--danger);
  }
  textarea {
    width: 100%;
    box-sizing: border-box;
    resize: vertical;
    font-family: var(--font-mono);
  }
  .schemas {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 0.7rem;
    margin: 0.7rem 0;
  }
  .err {
    color: var(--danger);
  }
  .warn {
    color: var(--warn);
  }
  .muted {
    color: var(--fg-muted);
  }
  @media (max-width: 780px) {
    .create,
    .workspace,
    .schemas {
      grid-template-columns: 1fr;
    }
    .retire-confirm {
      grid-template-columns: 1fr;
    }
  }
</style>
