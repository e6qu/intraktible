<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { onMount } from 'svelte';
  import { page } from '$app/stores';
  import { SvelteFlow, Background, Controls, type Node, type Edge } from '@xyflow/svelte';
  import '@xyflow/svelte/dist/style.css';
  import { getFlow, decide, type Flow } from '$lib/api';
  import { layout } from '$lib/layout';

  let key = $state('dev-sandbox-key');
  let flow = $state<Flow | null>(null);
  let nodes = $state.raw<Node[]>([]);
  let edges = $state.raw<Edge[]>([]);
  let error = $state('');

  let env = $state('production');
  let dataText = $state('{}');
  let result = $state('');

  const flowId = $page.params.flowId ?? '';

  async function load() {
    error = '';
    try {
      flow = await getFlow(key, flowId);
      const version = flow.versions.at(-1);
      if (!version) {
        nodes = [];
        edges = [];
        return;
      }
      const pos = layout(version.graph.nodes, version.graph.edges);
      nodes = version.graph.nodes.map((n) => ({
        id: n.id,
        position: pos.get(n.id) ?? { x: 0, y: 0 },
        data: { label: `${n.name || n.id} · ${n.type}` }
      }));
      edges = version.graph.edges.map((e, i) => ({
        id: `e${i}`,
        source: e.from,
        target: e.to,
        label: e.branch
      }));
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    }
  }

  async function run() {
    result = '';
    if (!flow) return;
    try {
      const data = JSON.parse(dataText);
      result = JSON.stringify(await decide(key, flow.slug, env, data), null, 2);
    } catch (e) {
      result = `Error: ${e instanceof Error ? e.message : String(e)}`;
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
  </div>
  {#if error}<p class="err">{error}</p>{/if}

  <div class="canvas" data-testid="flow-canvas">
    <SvelteFlow bind:nodes bind:edges fitView>
      <Background />
      <Controls />
    </SvelteFlow>
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
    <textarea bind:value={dataText} aria-label="input data" rows="3"></textarea>
    <pre data-testid="run-result">{result}</pre>
  </section>
</main>

<style>
  main {
    max-width: 56rem;
    margin: 2rem auto;
    padding: 0 1rem;
    font-family: system-ui, sans-serif;
  }
  .row {
    display: flex;
    gap: 0.5rem;
    flex-wrap: wrap;
    margin: 0.6rem 0;
  }
  input,
  button,
  select,
  textarea {
    font: inherit;
    padding: 0.4rem 0.6rem;
  }
  textarea {
    width: 100%;
    font-family: ui-monospace, monospace;
  }
  .canvas {
    height: 420px;
    border: 1px solid #ccc;
    border-radius: 0.5rem;
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
</style>
