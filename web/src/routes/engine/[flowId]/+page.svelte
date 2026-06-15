<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { onMount } from 'svelte';
  import { page } from '$app/stores';
  import { SvelteFlow, Background, Controls, type Node, type Edge } from '@xyflow/svelte';
  import '@xyflow/svelte/dist/style.css';
  import {
    getFlow,
    publishVersion,
    decide,
    type Flow,
    type GraphNode,
    type GraphEdge
  } from '$lib/api';
  import { layout } from '$lib/layout';

  const NODE_TYPES = [
    'input',
    'assignment',
    'rule',
    'split',
    'scorecard',
    'decision_table',
    '2d_matrix',
    'code',
    'connect',
    'ai',
    'output'
  ];

  interface EditNode {
    id: string;
    type: string;
    name: string;
    config: string;
  }

  let key = $state('dev-sandbox-key');
  let flow = $state<Flow | null>(null);
  let error = $state('');
  let publishMsg = $state('');

  // Engine-level editor model (the source of truth) and its Svelte Flow render.
  let editNodes = $state<EditNode[]>([]);
  let editEdges = $state<GraphEdge[]>([]);
  let counter = $state(0);
  let selectedId = $state<string | null>(null);
  let nodes = $state.raw<Node[]>([]);
  let edges = $state.raw<Edge[]>([]);

  let newType = $state('input');
  let edgeFrom = $state('');
  let edgeTo = $state('');
  let edgeBranch = $state('');

  let env = $state('production');
  let dataText = $state('{}');
  let entityType = $state('');
  let entityID = $state('');
  let result = $state('');

  const flowId = $page.params.flowId ?? '';
  const selected = $derived(editNodes.find((n) => n.id === selectedId) ?? null);

  function msg(e: unknown): string {
    return e instanceof Error ? e.message : String(e);
  }

  // Keep the canvas in sync with the editor model.
  $effect(() => {
    const pos = layout(editNodes, editEdges);
    nodes = editNodes.map((n) => ({
      id: n.id,
      position: pos.get(n.id) ?? { x: 0, y: 0 },
      data: { label: `${n.name || n.id} · ${n.type}` }
    }));
    edges = editEdges.map((e, i) => ({
      id: `e${i}`,
      source: e.from,
      target: e.to,
      label: e.branch
    }));
  });

  async function load() {
    error = '';
    try {
      flow = await getFlow(key, flowId);
      const version = flow.versions.at(-1);
      if (version) {
        editNodes = version.graph.nodes.map((n) => ({
          id: n.id,
          type: n.type,
          name: n.name ?? '',
          config: n.config ? JSON.stringify(n.config) : ''
        }));
        editEdges = version.graph.edges.map((e) => ({ from: e.from, to: e.to, branch: e.branch }));
        counter = editNodes.length;
      }
    } catch (e) {
      error = msg(e);
    }
  }

  function addNode() {
    const id = `n${++counter}`;
    editNodes = [...editNodes, { id, type: newType, name: '', config: '' }];
    selectedId = id;
  }
  function deleteNode(id: string) {
    editNodes = editNodes.filter((n) => n.id !== id);
    editEdges = editEdges.filter((e) => e.from !== id && e.to !== id);
    if (selectedId === id) selectedId = null;
  }
  function updateSelected(patch: Partial<EditNode>) {
    editNodes = editNodes.map((n) => (n.id === selectedId ? { ...n, ...patch } : n));
  }
  function addEdge() {
    if (!edgeFrom || !edgeTo) return;
    editEdges = [...editEdges, { from: edgeFrom, to: edgeTo, branch: edgeBranch || undefined }];
    edgeFrom = edgeTo = edgeBranch = '';
  }
  function deleteEdge(i: number) {
    editEdges = editEdges.filter((_, j) => j !== i);
  }

  async function publish() {
    publishMsg = '';
    error = '';
    try {
      const gnodes: GraphNode[] = editNodes.map((n) => ({
        id: n.id,
        type: n.type,
        name: n.name || undefined,
        config: n.config.trim() ? JSON.parse(n.config) : undefined
      }));
      const r = await publishVersion(key, flowId, { nodes: gnodes, edges: editEdges });
      publishMsg = `Published v${r.version}`;
      await load();
    } catch (e) {
      error = msg(e);
    }
  }

  async function run() {
    result = '';
    if (!flow) return;
    try {
      const entity = entityType && entityID ? { type: entityType, id: entityID } : undefined;
      result = JSON.stringify(
        await decide(key, flow.slug, env, JSON.parse(dataText), entity),
        null,
        2
      );
    } catch (e) {
      result = `Error: ${msg(e)}`;
    }
  }

  onMount(load);
</script>

<main>
  <p><a href="/engine">← all flows</a></p>
  <h1>{flow?.name ?? flowId}</h1>
  <div class="row">
    <input bind:value={key} aria-label="API key" />
    <button onclick={load}>Reload</button>
    <button onclick={publish}>Publish version</button>
    {#if publishMsg}<span class="ok">{publishMsg}</span>{/if}
  </div>
  {#if error}<p class="err">{error}</p>{/if}

  <div class="grid">
    <div class="canvas" data-testid="flow-canvas">
      <SvelteFlow bind:nodes bind:edges fitView>
        <Background />
        <Controls />
      </SvelteFlow>
    </div>

    <aside>
      <h2>Add node</h2>
      <div class="row">
        <select bind:value={newType} aria-label="new node type">
          {#each NODE_TYPES as t (t)}<option value={t}>{t}</option>{/each}
        </select>
        <button onclick={addNode}>Add</button>
      </div>

      <h2>Nodes</h2>
      <ul class="nodes">
        {#each editNodes as n (n.id)}
          <li class:sel={n.id === selectedId}>
            <button class="link" onclick={() => (selectedId = n.id)}
              >{n.name || n.id} · {n.type}</button
            >
            <button class="x" aria-label={`delete ${n.id}`} onclick={() => deleteNode(n.id)}
              >✕</button
            >
          </li>
        {/each}
      </ul>

      {#if selected}
        <h2>Node: {selected.id}</h2>
        <label
          >name <input
            value={selected.name}
            oninput={(e) => updateSelected({ name: e.currentTarget.value })}
            aria-label="node name"
          /></label
        >
        <label
          >type
          <select
            value={selected.type}
            onchange={(e) => updateSelected({ type: e.currentTarget.value })}
            aria-label="selected node type"
          >
            {#each NODE_TYPES as t (t)}<option value={t}>{t}</option>{/each}
          </select>
        </label>
        <label
          >config (JSON)
          <textarea
            value={selected.config}
            oninput={(e) => updateSelected({ config: e.currentTarget.value })}
            aria-label="node config"
            rows="4"
          ></textarea>
        </label>
      {/if}

      <h2>Add edge</h2>
      <div class="row">
        <select bind:value={edgeFrom} aria-label="edge from">
          <option value="">from…</option>
          {#each editNodes as n (n.id)}<option value={n.id}>{n.id}</option>{/each}
        </select>
        <select bind:value={edgeTo} aria-label="edge to">
          <option value="">to…</option>
          {#each editNodes as n (n.id)}<option value={n.id}>{n.id}</option>{/each}
        </select>
        <input bind:value={edgeBranch} placeholder="branch" aria-label="edge branch" size="6" />
        <button onclick={addEdge}>Add edge</button>
      </div>
      <ul class="edges">
        {#each editEdges as e, i (i)}
          <li>
            {e.from} → {e.to}{e.branch ? ` (${e.branch})` : ''}
            <button class="x" aria-label={`delete edge ${i}`} onclick={() => deleteEdge(i)}
              >✕</button
            >
          </li>
        {/each}
      </ul>
    </aside>
  </div>

  <section>
    <h2>Test run</h2>
    <div class="row">
      <select bind:value={env} aria-label="environment">
        <option value="sandbox">sandbox</option>
        <option value="production">production</option>
      </select>
      <button onclick={run} disabled={!flow}>Run</button>
    </div>
    <div class="row">
      <input
        bind:value={entityType}
        placeholder="entity type (optional)"
        aria-label="entity type"
        size="16"
      />
      <input
        bind:value={entityID}
        placeholder="entity id (optional)"
        aria-label="entity id"
        size="16"
      />
    </div>
    <textarea bind:value={dataText} aria-label="input data" rows="3"></textarea>
    <pre data-testid="run-result">{result}</pre>
  </section>
</main>

<style>
  main {
    max-width: 72rem;
    margin: 2rem auto;
    padding: 0 1rem;
    font-family: system-ui, sans-serif;
  }
  .grid {
    display: grid;
    grid-template-columns: 1fr 22rem;
    gap: 1rem;
  }
  .row {
    display: flex;
    gap: 0.5rem;
    flex-wrap: wrap;
    margin: 0.5rem 0;
    align-items: center;
  }
  input,
  button,
  select,
  textarea {
    font: inherit;
    padding: 0.3rem 0.5rem;
  }
  textarea {
    width: 100%;
    font-family: ui-monospace, monospace;
  }
  label {
    display: block;
    margin: 0.4rem 0;
    font-size: 0.85rem;
    color: #555;
  }
  .canvas {
    height: 460px;
    border: 1px solid #ccc;
    border-radius: 0.5rem;
  }
  aside {
    font-size: 0.95rem;
  }
  h2 {
    font-size: 0.9rem;
    margin: 0.8rem 0 0.3rem;
    text-transform: uppercase;
    letter-spacing: 0.03em;
    color: #888;
  }
  ul.nodes,
  ul.edges {
    list-style: none;
    padding: 0;
    margin: 0;
  }
  ul li {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 0.15rem 0.3rem;
  }
  li.sel {
    background: #8882;
    border-radius: 0.3rem;
  }
  button.link {
    background: none;
    border: none;
    padding: 0;
    color: #06c;
    cursor: pointer;
    text-align: left;
  }
  button.x {
    border: none;
    background: none;
    color: #b00;
    cursor: pointer;
  }
  pre {
    background: #8881;
    padding: 0.8rem;
    border-radius: 0.5rem;
    min-height: 2rem;
  }
  .err {
    color: #b00;
  }
  .ok {
    color: #080;
  }
</style>
