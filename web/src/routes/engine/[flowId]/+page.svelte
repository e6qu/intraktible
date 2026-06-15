<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { onMount } from 'svelte';
  import { page } from '$app/stores';
  import {
    SvelteFlow,
    Background,
    Controls,
    type Node,
    type Edge,
    type Connection
  } from '@xyflow/svelte';
  import '@xyflow/svelte/dist/style.css';
  import {
    getFlow,
    publishVersion,
    decide,
    exportFlow,
    exportDecision,
    type ExportFormat,
    type Flow,
    type GraphNode,
    type GraphEdge
  } from '$lib/api';
  import { layout } from '$lib/layout';
  import { theme } from '$lib/theme';
  import Icon from '$lib/Icon.svelte';
  import {
    asText,
    asNum,
    asCsv,
    fromCsv,
    cleanConfig,
    asCellText,
    parseCell,
    addUniqueEdge
  } from '$lib/nodeconfig';

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

  // Node types with a structured panel; the raw-JSON textarea stays available for
  // every type as the advanced view.
  const STRUCTURED = [
    'split',
    'connect',
    'ai',
    'manual_review',
    'output',
    'assignment',
    'code',
    'rule',
    'scorecard',
    'decision_table',
    '2d_matrix'
  ];

  // --- Rule node: rules[] = {when, then:[{target,expr}]} (two-level repeater) ---
  type Assign = { target?: string; expr?: string };
  type RuleClause = { when?: string; then?: Assign[] };
  function ruleClauses(): RuleClause[] {
    const r = nodeCfg().rules;
    return Array.isArray(r) ? (r as RuleClause[]) : [];
  }
  function ruleThen(i: number): Assign[] {
    const t = ruleClauses().at(i)?.then;
    return Array.isArray(t) ? t : [];
  }
  function addRule() {
    patchCfg({ rules: [...ruleClauses(), { when: '', then: [] }] });
  }
  function removeRule(i: number) {
    patchCfg({ rules: ruleClauses().filter((_, j) => j !== i) });
  }
  function setRuleWhen(i: number, when: string) {
    patchCfg({ rules: ruleClauses().map((c, j) => (j === i ? { ...c, when } : c)) });
  }
  function addRuleThen(i: number) {
    patchCfg({
      rules: ruleClauses().map((c, j) =>
        j === i ? { ...c, then: [...ruleThen(i), { target: '', expr: '' }] } : c
      )
    });
  }
  function removeRuleThen(i: number, k: number) {
    patchCfg({
      rules: ruleClauses().map((c, j) =>
        j === i ? { ...c, then: ruleThen(i).filter((_, m) => m !== k) } : c
      )
    });
  }
  function setRuleThen(i: number, k: number, patch: Assign) {
    patchCfg({
      rules: ruleClauses().map((c, j) =>
        j === i ? { ...c, then: ruleThen(i).map((t, m) => (m === k ? { ...t, ...patch } : t)) } : c
      )
    });
  }

  // --- Scorecard node: factors[] = {when, weight}, output? ---
  type Factor = { when?: string; weight?: number };
  function factors(): Factor[] {
    const f = nodeCfg().factors;
    return Array.isArray(f) ? (f as Factor[]) : [];
  }
  function addFactor() {
    patchCfg({ factors: [...factors(), { when: '', weight: 0 }] });
  }
  function removeFactor(i: number) {
    patchCfg({ factors: factors().filter((_, j) => j !== i) });
  }
  function setFactor(i: number, patch: Factor) {
    patchCfg({ factors: factors().map((f, j) => (j === i ? { ...f, ...patch } : f)) });
  }

  // --- Decision table: rows[] = {when, outputs:[{target,expr}]}, mode? ---
  type TableRow = { when?: string; outputs?: Assign[] };
  function tableRows(): TableRow[] {
    const r = nodeCfg().rows;
    return Array.isArray(r) ? (r as TableRow[]) : [];
  }
  function rowOutputs(i: number): Assign[] {
    const o = tableRows().at(i)?.outputs;
    return Array.isArray(o) ? o : [];
  }
  function addTableRow() {
    patchCfg({ rows: [...tableRows(), { when: '', outputs: [] }] });
  }
  function removeTableRow(i: number) {
    patchCfg({ rows: tableRows().filter((_, j) => j !== i) });
  }
  function setRowWhen(i: number, when: string) {
    patchCfg({ rows: tableRows().map((r, j) => (j === i ? { ...r, when } : r)) });
  }
  function addRowOutput(i: number) {
    patchCfg({
      rows: tableRows().map((r, j) =>
        j === i ? { ...r, outputs: [...rowOutputs(i), { target: '', expr: '' }] } : r
      )
    });
  }
  function removeRowOutput(i: number, k: number) {
    patchCfg({
      rows: tableRows().map((r, j) =>
        j === i ? { ...r, outputs: rowOutputs(i).filter((_, m) => m !== k) } : r
      )
    });
  }
  function setRowOutput(i: number, k: number, patch: Assign) {
    patchCfg({
      rows: tableRows().map((r, j) =>
        j === i
          ? { ...r, outputs: rowOutputs(i).map((o, m) => (m === k ? { ...o, ...patch } : o)) }
          : r
      )
    });
  }

  // --- 2D matrix: rows[]/cols[] = {when}, cells[r][c] = literal, output? ---
  type AxisCond = { when?: string };
  function matrixRows(): AxisCond[] {
    const r = nodeCfg().rows;
    return Array.isArray(r) ? (r as AxisCond[]) : [];
  }
  function matrixCols(): AxisCond[] {
    const c = nodeCfg().cols;
    return Array.isArray(c) ? (c as AxisCond[]) : [];
  }
  function matrixCells(): unknown[][] {
    const c = nodeCfg().cells;
    return Array.isArray(c) ? (c as unknown[][]) : [];
  }
  function cellText(r: number, c: number): string {
    const row = matrixCells().at(r);
    return asCellText(Array.isArray(row) ? row.at(c) : undefined);
  }
  function addMatrixRow() {
    patchCfg({ rows: [...matrixRows(), { when: '' }] });
  }
  function addMatrixCol() {
    patchCfg({ cols: [...matrixCols(), { when: '' }] });
  }
  function setMatrixRowWhen(i: number, when: string) {
    patchCfg({ rows: matrixRows().map((a, j) => (j === i ? { when } : a)) });
  }
  function setMatrixColWhen(i: number, when: string) {
    patchCfg({ cols: matrixCols().map((a, j) => (j === i ? { when } : a)) });
  }
  // Rebuild a rectangular rows×cols cell grid, preserving existing values and
  // setting [r][c] to the parsed literal — no dynamic key writes.
  function setCell(r: number, c: number, raw: string) {
    const cur = matrixCells();
    const grid = matrixRows().map((_, ri) =>
      matrixCols().map((_, ci) => {
        if (ri === r && ci === c) return parseCell(raw);
        const row = cur.at(ri);
        return Array.isArray(row) ? row.at(ci) : undefined;
      })
    );
    patchCfg({ cells: grid });
  }

  // The selected assignment node's {target, expr} rows (empty when none/invalid).
  function assignmentRows(): { target?: string; expr?: string }[] {
    const a = nodeCfg().assignments;
    return Array.isArray(a) ? (a as { target?: string; expr?: string }[]) : [];
  }
  function setAssignment(i: number, patch: { target?: string; expr?: string }) {
    patchCfg({
      assignments: assignmentRows().map((row, j) => (j === i ? { ...row, ...patch } : row))
    });
  }
  function addAssignment() {
    patchCfg({ assignments: [...assignmentRows(), { target: '', expr: '' }] });
  }
  function removeAssignment(i: number) {
    patchCfg({ assignments: assignmentRows().filter((_, j) => j !== i) });
  }

  // The selected node's config as an object (empty on blank/invalid JSON).
  function nodeCfg(): Record<string, unknown> {
    if (!selected || !selected.config.trim()) return {};
    try {
      return JSON.parse(selected.config) as Record<string, unknown>;
    } catch {
      return {};
    }
  }
  // Merge a patch into the config and write it back (empty fields are dropped).
  function patchCfg(patch: Record<string, unknown>) {
    updateSelected({ config: JSON.stringify(cleanConfig({ ...nodeCfg(), ...patch })) });
  }
  function addEdge() {
    if (!edgeFrom || !edgeTo) return;
    editEdges = [...editEdges, { from: edgeFrom, to: edgeTo, branch: edgeBranch || undefined }];
    edgeFrom = edgeTo = edgeBranch = '';
  }
  // Drag-to-connect on the canvas: dragging from a node's handle to another adds
  // an (unbranched) edge, deduplicated against the existing ones.
  function onConnect(conn: Connection) {
    editEdges = addUniqueEdge(editEdges, conn.source, conn.target);
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

  let lastDecisionId = $state('');
  async function run() {
    result = '';
    lastDecisionId = '';
    if (!flow) return;
    try {
      const entity = entityType && entityID ? { type: entityType, id: entityID } : undefined;
      const res = await decide(key, flow.slug, env, JSON.parse(dataText), entity);
      lastDecisionId = res.decision_id ?? '';
      result = JSON.stringify(res, null, 2);
    } catch (e) {
      result = `Error: ${msg(e)}`;
    }
  }
  async function downloadTrace() {
    exportMsg = '';
    try {
      const text = await exportDecision(key, lastDecisionId);
      const url = URL.createObjectURL(new Blob([text], { type: 'text/plain' }));
      const a = document.createElement('a');
      a.href = url;
      a.download = `${lastDecisionId}-trace.mmd`;
      a.click();
      URL.revokeObjectURL(url);
      exportMsg = 'Downloaded run trace';
    } catch (e) {
      error = msg(e);
    }
  }
  async function copyTrace() {
    exportMsg = '';
    try {
      await navigator.clipboard.writeText(await exportDecision(key, lastDecisionId));
      exportMsg = 'Copied run trace to clipboard';
    } catch (e) {
      error = msg(e);
    }
  }

  let exportMsg = $state('');
  function exportFilename(format: ExportFormat): string {
    const base = flow?.slug ?? flowId;
    if (format === 'bpmn') return `${base}.bpmn`;
    return format === 'mermaid-state' ? `${base}-state.mmd` : `${base}.mmd`;
  }
  async function downloadExport(format: ExportFormat) {
    exportMsg = '';
    try {
      const text = await exportFlow(key, flowId, format);
      const url = URL.createObjectURL(new Blob([text], { type: 'text/plain' }));
      const a = document.createElement('a');
      a.href = url;
      a.download = exportFilename(format);
      a.click();
      URL.revokeObjectURL(url);
      exportMsg = `Downloaded ${exportFilename(format)}`;
    } catch (e) {
      error = msg(e);
    }
  }
  async function copyExport(format: ExportFormat) {
    exportMsg = '';
    try {
      await navigator.clipboard.writeText(await exportFlow(key, flowId, format));
      exportMsg = `Copied ${format} to clipboard`;
    } catch (e) {
      error = msg(e);
    }
  }

  onMount(load);
</script>

<main>
  <p><a href="/engine">← all flows</a></p>
  <h1>{flow?.name ?? flowId}</h1>
  <div class="row">
    <input bind:value={key} aria-label="API key" />
    <button onclick={load}><Icon name="reload" size={15} /> Reload</button>
    <button class="primary" onclick={publish}
      ><Icon name="check" size={15} /> Publish version</button
    >
    {#if publishMsg}<span class="ok">{publishMsg}</span>{/if}
  </div>
  <div class="row export">
    <span class="exportlabel"><Icon name="diagram" size={15} /> Export</span>
    <div class="grp">
      <button onclick={() => downloadExport('mermaid')} title="Download Mermaid flowchart">
        <Icon name="download" size={14} /> Mermaid
      </button>
      <button
        class="icon"
        aria-label="Copy Mermaid"
        title="Copy Mermaid"
        onclick={() => copyExport('mermaid')}
      >
        <Icon name="copy" size={14} />
      </button>
    </div>
    <button onclick={() => downloadExport('mermaid-state')} title="Download Mermaid state diagram">
      <Icon name="download" size={14} /> State
    </button>
    <div class="grp">
      <button onclick={() => downloadExport('bpmn')} title="Download BPMN 2.0 XML">
        <Icon name="download" size={14} /> BPMN
      </button>
      <button
        class="icon"
        aria-label="Copy BPMN"
        title="Copy BPMN"
        onclick={() => copyExport('bpmn')}
      >
        <Icon name="copy" size={14} />
      </button>
    </div>
    {#if exportMsg}<span class="ok">{exportMsg}</span>{/if}
  </div>
  {#if error}<p class="err">{error}</p>{/if}

  <div class="grid">
    <div class="canvas" data-testid="flow-canvas">
      <SvelteFlow bind:nodes bind:edges onconnect={onConnect} colorMode={$theme} fitView>
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
            <button class="link" onclick={() => (selectedId = n.id)}>
              <span class="nodeicon" title={n.type}><Icon name={n.type} size={15} /></span>
              <span>{n.name || n.id}</span>
              <span class="nodetype">{n.type}</span>
            </button>
            <button class="x" aria-label={`delete ${n.id}`} onclick={() => deleteNode(n.id)}>
              <Icon name="trash" size={14} />
            </button>
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
        {#if selected.type === 'split'}
          <label
            >condition <input
              value={asText(nodeCfg().condition)}
              oninput={(e) => patchCfg({ condition: e.currentTarget.value })}
              aria-label="condition"
            /></label
          >
        {:else if selected.type === 'connect'}
          <label
            >connector <input
              value={asText(nodeCfg().connector)}
              oninput={(e) => patchCfg({ connector: e.currentTarget.value })}
              aria-label="connector"
            /></label
          >
          <label
            >output key <input
              value={asText(nodeCfg().output)}
              oninput={(e) => patchCfg({ output: e.currentTarget.value })}
              aria-label="connect output"
            /></label
          >
        {:else if selected.type === 'ai'}
          <label
            >agent <input
              value={asText(nodeCfg().agent)}
              oninput={(e) => patchCfg({ agent: e.currentTarget.value })}
              aria-label="agent"
            /></label
          >
          <label
            >output key <input
              value={asText(nodeCfg().output)}
              oninput={(e) => patchCfg({ output: e.currentTarget.value })}
              aria-label="ai output"
            /></label
          >
          <label
            >prompt <input
              value={asText(nodeCfg().prompt)}
              oninput={(e) => patchCfg({ prompt: e.currentTarget.value })}
              aria-label="ai prompt"
            /></label
          >
        {:else if selected.type === 'manual_review'}
          <label
            >company_name expr <input
              value={asText(nodeCfg().company_name)}
              oninput={(e) => patchCfg({ company_name: e.currentTarget.value })}
              aria-label="company_name expr"
            /></label
          >
          <label
            >case_type expr <input
              value={asText(nodeCfg().case_type)}
              oninput={(e) => patchCfg({ case_type: e.currentTarget.value })}
              aria-label="case_type expr"
            /></label
          >
          <label
            >sla_days <input
              type="number"
              value={asText(nodeCfg().sla_days)}
              oninput={(e) =>
                patchCfg({
                  sla_days: e.currentTarget.value === '' ? '' : Number(e.currentTarget.value)
                })}
              aria-label="sla_days"
            /></label
          >
        {:else if selected.type === 'output'}
          <label
            >fields (comma-separated; empty = whole context) <input
              value={asCsv(nodeCfg().fields)}
              oninput={(e) => patchCfg({ fields: fromCsv(e.currentTarget.value) })}
              aria-label="output fields"
            /></label
          >
        {:else if selected.type === 'code'}
          <label
            >code (Starlark)
            <textarea
              value={asText(nodeCfg().code)}
              oninput={(e) => patchCfg({ code: e.currentTarget.value })}
              aria-label="code"
              rows="4"
            ></textarea>
          </label>
        {:else if selected.type === 'assignment'}
          <p class="muted">assignments</p>
          {#each assignmentRows() as row, i (i)}
            <div class="row">
              <input
                value={asText(row.target)}
                oninput={(e) => setAssignment(i, { target: e.currentTarget.value })}
                aria-label={`assignment ${i} target`}
                placeholder="target"
              />
              <input
                value={asText(row.expr)}
                oninput={(e) => setAssignment(i, { expr: e.currentTarget.value })}
                aria-label={`assignment ${i} expr`}
                placeholder="expr"
              />
              <button
                class="x"
                aria-label={`remove assignment ${i}`}
                onclick={() => removeAssignment(i)}>✕</button
              >
            </div>
          {/each}
          <button onclick={addAssignment}>Add assignment</button>
        {:else if selected.type === 'rule'}
          <p class="muted">rules (when → then assignments)</p>
          {#each ruleClauses() as clause, i (i)}
            <div class="clause">
              <div class="row">
                <input
                  value={asText(clause.when)}
                  oninput={(e) => setRuleWhen(i, e.currentTarget.value)}
                  aria-label={`rule ${i} when`}
                  placeholder="when"
                />
                <button class="x" aria-label={`remove rule ${i}`} onclick={() => removeRule(i)}
                  >✕</button
                >
              </div>
              {#each ruleThen(i) as t, k (k)}
                <div class="row indent">
                  <input
                    value={asText(t.target)}
                    oninput={(e) => setRuleThen(i, k, { target: e.currentTarget.value })}
                    aria-label={`rule ${i} then ${k} target`}
                    placeholder="target"
                  />
                  <input
                    value={asText(t.expr)}
                    oninput={(e) => setRuleThen(i, k, { expr: e.currentTarget.value })}
                    aria-label={`rule ${i} then ${k} expr`}
                    placeholder="expr"
                  />
                  <button
                    class="x"
                    aria-label={`remove rule ${i} then ${k}`}
                    onclick={() => removeRuleThen(i, k)}>✕</button
                  >
                </div>
              {/each}
              <button onclick={() => addRuleThen(i)}>Add then</button>
            </div>
          {/each}
          <button onclick={addRule}>Add rule</button>
        {:else if selected.type === 'scorecard'}
          <label
            >output key <input
              value={asText(nodeCfg().output)}
              oninput={(e) => patchCfg({ output: e.currentTarget.value })}
              aria-label="scorecard output"
            /></label
          >
          <p class="muted">factors (when → weight)</p>
          {#each factors() as f, i (i)}
            <div class="row">
              <input
                value={asText(f.when)}
                oninput={(e) => setFactor(i, { when: e.currentTarget.value })}
                aria-label={`factor ${i} when`}
                placeholder="when"
              />
              <input
                type="number"
                step="any"
                value={asNum(f.weight)}
                oninput={(e) =>
                  setFactor(i, {
                    weight: e.currentTarget.value === '' ? 0 : Number(e.currentTarget.value)
                  })}
                aria-label={`factor ${i} weight`}
                placeholder="weight"
                size="6"
              />
              <button class="x" aria-label={`remove factor ${i}`} onclick={() => removeFactor(i)}
                >✕</button
              >
            </div>
          {/each}
          <button onclick={addFactor}>Add factor</button>
        {:else if selected.type === 'decision_table'}
          <label
            >mode
            <select
              value={asText(nodeCfg().mode) || 'first'}
              onchange={(e) => patchCfg({ mode: e.currentTarget.value })}
              aria-label="decision table mode"
            >
              <option value="first">first match</option>
              <option value="all">all matches</option>
            </select>
          </label>
          <p class="muted">rows (when → outputs)</p>
          {#each tableRows() as row, i (i)}
            <div class="clause">
              <div class="row">
                <input
                  value={asText(row.when)}
                  oninput={(e) => setRowWhen(i, e.currentTarget.value)}
                  aria-label={`row ${i} when`}
                  placeholder="when"
                />
                <button class="x" aria-label={`remove row ${i}`} onclick={() => removeTableRow(i)}
                  >✕</button
                >
              </div>
              {#each rowOutputs(i) as o, k (k)}
                <div class="row indent">
                  <input
                    value={asText(o.target)}
                    oninput={(e) => setRowOutput(i, k, { target: e.currentTarget.value })}
                    aria-label={`row ${i} output ${k} target`}
                    placeholder="target"
                  />
                  <input
                    value={asText(o.expr)}
                    oninput={(e) => setRowOutput(i, k, { expr: e.currentTarget.value })}
                    aria-label={`row ${i} output ${k} expr`}
                    placeholder="expr"
                  />
                  <button
                    class="x"
                    aria-label={`remove row ${i} output ${k}`}
                    onclick={() => removeRowOutput(i, k)}>✕</button
                  >
                </div>
              {/each}
              <button onclick={() => addRowOutput(i)}>Add output</button>
            </div>
          {/each}
          <button onclick={addTableRow}>Add row</button>
        {:else if selected.type === '2d_matrix'}
          <label
            >output key <input
              value={asText(nodeCfg().output)}
              oninput={(e) => patchCfg({ output: e.currentTarget.value })}
              aria-label="matrix output"
            /></label
          >
          <p class="muted">row conditions</p>
          {#each matrixRows() as r, i (i)}
            <div class="row">
              <input
                value={asText(r.when)}
                oninput={(e) => setMatrixRowWhen(i, e.currentTarget.value)}
                aria-label={`matrix row ${i} when`}
                placeholder="row when"
              />
            </div>
          {/each}
          <button onclick={addMatrixRow}>Add row</button>
          <p class="muted">column conditions</p>
          {#each matrixCols() as c, i (i)}
            <div class="row">
              <input
                value={asText(c.when)}
                oninput={(e) => setMatrixColWhen(i, e.currentTarget.value)}
                aria-label={`matrix col ${i} when`}
                placeholder="col when"
              />
            </div>
          {/each}
          <button onclick={addMatrixCol}>Add column</button>
          {#if matrixRows().length && matrixCols().length}
            <p class="muted">cells [row][col] (literal values)</p>
            {#each matrixRows() as r, i (i)}
              <div class="row">
                <span class="cellrow">{asText(r.when) || `row ${i}`}</span>
                {#each matrixCols() as c, j (j)}
                  <input
                    value={cellText(i, j)}
                    oninput={(e) => setCell(i, j, e.currentTarget.value)}
                    aria-label={`matrix cell ${i} ${j}`}
                    title={asText(c.when)}
                    size="6"
                  />
                {/each}
              </div>
            {/each}
          {/if}
        {/if}
        <label
          >{STRUCTURED.includes(selected.type) ? 'config (JSON, advanced)' : 'config (JSON)'}
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
    {#if lastDecisionId}
      <div class="row">
        <span class="exportlabel"><Icon name="diagram" size={15} /> Run trace</span>
        <button onclick={downloadTrace} title="Download the run as a Mermaid sequence diagram">
          <Icon name="download" size={14} /> Sequence
        </button>
        <button
          class="icon"
          aria-label="Copy run trace"
          title="Copy sequence diagram"
          onclick={copyTrace}
        >
          <Icon name="copy" size={14} />
        </button>
      </div>
    {/if}
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
    color: var(--fg-muted);
  }
  .canvas {
    height: 460px;
    border: 1px solid var(--border-strong);
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
    color: var(--fg-subtle);
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
    padding: 0.15rem 0.2rem;
    color: var(--fg);
    cursor: pointer;
    text-align: left;
    display: inline-flex;
    align-items: center;
    gap: 0.45rem;
    flex: 1;
    min-width: 0;
    justify-content: flex-start;
  }
  button.link:hover {
    color: var(--link);
    background: none;
  }
  .nodeicon {
    display: inline-flex;
    color: var(--accent);
  }
  .nodetype {
    margin-left: auto;
    font-size: 0.7rem;
    color: var(--fg-subtle);
    font-family: ui-monospace, monospace;
  }
  button.x {
    border: none;
    background: none;
    color: var(--fg-subtle);
    cursor: pointer;
    padding: 0.15rem;
  }
  button.x:hover {
    color: var(--danger);
    background: none;
  }
  .export {
    align-items: center;
    gap: 0.4rem;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    padding: 0.35rem 0.6rem;
    background: var(--surface);
  }
  .exportlabel {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    font-size: 0.8rem;
    color: var(--fg-muted);
    font-weight: 550;
  }
  .export .grp {
    display: inline-flex;
  }
  .export .grp button:first-child {
    border-top-right-radius: 0;
    border-bottom-right-radius: 0;
  }
  .export .grp button.icon {
    border-top-left-radius: 0;
    border-bottom-left-radius: 0;
    border-left: none;
    padding: 0.4rem 0.5rem;
  }
  pre {
    background: #8881;
    padding: 0.8rem;
    border-radius: 0.5rem;
    min-height: 2rem;
  }
  .err {
    color: var(--danger);
  }
  .ok {
    color: var(--ok);
  }
  .muted {
    font-size: 0.8rem;
    color: var(--fg-subtle);
    margin: 0.5rem 0 0.2rem;
  }
  .clause {
    border-left: 2px solid #8883;
    padding-left: 0.5rem;
    margin: 0.3rem 0;
  }
  .row.indent {
    margin-left: 1rem;
  }
  .cellrow {
    font-size: 0.75rem;
    color: var(--fg-subtle);
    min-width: 5rem;
  }
</style>
