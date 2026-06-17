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
    batchDecide,
    preapproveBatch,
    exportFlow,
    exportDecision,
    getFlowMetrics,
    listMonitors,
    defineMonitor,
    deleteMonitor,
    checkMonitors,
    captureBaseline,
    getDrift,
    getAssertions,
    setAssertions,
    runAssertions,
    type AssertionReport,
    listWebhooks,
    subscribeWebhook,
    deleteWebhook,
    MONITOR_METRICS,
    type Monitor,
    type MonitorCheck,
    type DriftReport,
    type Webhook,
    backtestFlow,
    deployVersion,
    promoteFlow,
    requestDeployment,
    approveDeployment,
    rejectDeployment,
    type ExportFormat,
    type Flow,
    type FlowMetrics,
    type BacktestReport,
    type BatchReport,
    type PreApproveBatchReport,
    type GraphNode,
    type GraphEdge
  } from '$lib/api';
  import { toast } from '$lib/toast';
  import { diffGraphs, diffIsEmpty } from '$lib/diff';
  import { layout, type XY } from '$lib/layout';
  import { theme } from '$lib/theme';
  import Icon from '$lib/Icon.svelte';
  import CommentThread from '$lib/CommentThread.svelte';
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
    'reason',
    'output'
  ];

  interface EditNode {
    id: string;
    type: string;
    name: string;
    config: string;
    pos?: XY; // saved canvas position; absent → auto-laid-out until placed/dragged
  }

  // API calls authenticate via the session cookie (empty key -> no X-Api-Key header).
  const key = '';
  let flow = $state<Flow | null>(null);
  let error = $state('');
  let metrics = $state<FlowMetrics | null>(null);

  // loadMetrics fetches the flow's analytics roll-up (non-fatal if none yet).
  async function loadMetrics() {
    try {
      metrics = await getFlowMetrics(key, flowId);
    } catch {
      metrics = null;
    }
  }

  // automation rate from the policy disposition breakdown: auto-handled
  // (approve+decline) over the total dispositioned decisions.
  const automation = $derived.by(() => {
    const d = metrics?.by_disposition;
    if (!d) return null;
    const auto = (d.approve ?? 0) + (d.decline ?? 0);
    const refer = d.refer ?? 0;
    const total = auto + refer;
    return total ? { auto, refer, rate: Math.round((auto / total) * 100) } : null;
  });

  // Engine-level editor model (the source of truth) and its Svelte Flow render.
  let editNodes = $state<EditNode[]>([]);
  let editEdges = $state<GraphEdge[]>([]);
  // The version's input schema is preserved across edits and republishes (the
  // builder edits the graph, not the schema) and can be brought in by an import.
  let inputSchema = $state<unknown>(undefined);
  let importText = $state('');
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

  // Canvas sync is explicit, not a reactive $effect over editNodes: a reactive
  // rebuild re-ran auto-layout on every edit, so adding/editing a node shoved the
  // others around. Instead each node keeps its own saved position (pos), drags are
  // folded back before any rebuild, and auto-layout only fills nodes that have no
  // position yet (a freshly loaded positionless graph) or runs on explicit Relax.
  function foldPositions() {
    const live = new Map(nodes.map((n) => [n.id, n.position]));
    editNodes = editNodes.map((n) => {
      const p = live.get(n.id);
      return p ? { ...n, pos: { x: p.x, y: p.y } } : n;
    });
  }
  function syncCanvas() {
    foldPositions();
    const auto = layout(editNodes, editEdges);
    nodes = editNodes.map((n) => ({
      id: n.id,
      position: n.pos ?? auto.get(n.id) ?? { x: 0, y: 0 },
      data: { label: `${n.name || n.id} · ${n.type}` }
    }));
    edges = editEdges.map((e, i) => ({
      id: `e${i}`,
      source: e.from,
      target: e.to,
      label: e.branch
    }));
  }
  // Relax: adopt the auto-layout for every node (the only thing that moves nodes
  // the user already placed — and only when they ask for it).
  function relax() {
    const auto = layout(editNodes, editEdges);
    editNodes = editNodes.map((n) => ({ ...n, pos: auto.get(n.id) ?? n.pos }));
    syncCanvas();
  }

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
          config: n.config ? JSON.stringify(n.config) : '',
          pos: n.position
        }));
        editEdges = version.graph.edges.map((e) => ({ from: e.from, to: e.to, branch: e.branch }));
        inputSchema = version.input_schema;
        counter = editNodes.length;
        syncCanvas();
      }
      // Default the version-diff selectors to the two most recent versions.
      const vs = flow.versions;
      if (vs.length > 0) {
        diffB = String(vs[vs.length - 1].version);
        diffA = String(vs[vs.length >= 2 ? vs.length - 2 : vs.length - 1].version);
      }
    } catch (e) {
      error = msg(e);
    }
  }

  // Version diff (client-side structural compare of two published versions)
  let diffA = $state('');
  let diffB = $state('');
  function versionGraph(v: string) {
    const n = parseInt(v, 10);
    return flow?.versions.find((x) => x.version === n)?.graph;
  }
  let graphDiff = $derived.by(() => {
    const a = versionGraph(diffA);
    const b = versionGraph(diffB);
    return a && b ? diffGraphs(a, b) : null;
  });

  function addNode() {
    foldPositions(); // capture any drags so they survive the rebuild
    const id = `n${++counter}`;
    editNodes = [...editNodes, { id, type: newType, name: '', config: '', pos: nextNodePos() }];
    selectedId = id;
    syncCanvas();
  }
  // nextNodePos drops a new node just below-right of the selected one (or the last
  // placed node), so it lands in free space without nudging anything already there.
  function nextNodePos(): XY {
    const anchor = editNodes.find((n) => n.id === selectedId)?.pos ?? editNodes.at(-1)?.pos;
    if (anchor) return { x: anchor.x + 40, y: anchor.y + 90 };
    return { x: 60, y: 60 };
  }
  function deleteNode(id: string) {
    foldPositions();
    editNodes = editNodes.filter((n) => n.id !== id);
    editEdges = editEdges.filter((e) => e.from !== id && e.to !== id);
    if (selectedId === id) selectedId = null;
    syncCanvas();
  }
  function updateSelected(patch: Partial<EditNode>) {
    editNodes = editNodes.map((n) => (n.id === selectedId ? { ...n, ...patch } : n));
    syncCanvas();
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
    '2d_matrix',
    'reason'
  ];

  // Rule node: rules[] = {when, then:[{target,expr}]} (two-level repeater)
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

  // Scorecard node: factors[] = {when, weight}, output?
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

  // Reason node: reasons[] = {when, code, description}
  type Reason = { when?: string; code?: string; description?: string };
  function reasons(): Reason[] {
    const r = nodeCfg().reasons;
    return Array.isArray(r) ? (r as Reason[]) : [];
  }
  function addReason() {
    patchCfg({ reasons: [...reasons(), { when: '', code: '', description: '' }] });
  }
  function removeReason(i: number) {
    patchCfg({ reasons: reasons().filter((_, j) => j !== i) });
  }
  function setReason(i: number, patch: Reason) {
    patchCfg({ reasons: reasons().map((r, j) => (j === i ? { ...r, ...patch } : r)) });
  }

  // Decision table: rows[] = {when, outputs:[{target,expr}]}, mode?
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

  // 2D matrix: rows[]/cols[] = {when}, cells[r][c] = literal, output?
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
    syncCanvas();
  }
  // Drag-to-connect on the canvas: dragging from a node's handle to another adds
  // an (unbranched) edge, deduplicated against the existing ones.
  function onConnect(conn: Connection) {
    editEdges = addUniqueEdge(editEdges, conn.source, conn.target);
    syncCanvas();
  }
  function deleteEdge(i: number) {
    editEdges = editEdges.filter((_, j) => j !== i);
    syncCanvas();
  }

  let publishing = $state(false);
  async function publish() {
    error = '';
    publishing = true;
    try {
      foldPositions(); // persist the current canvas layout with the version
      const gnodes: GraphNode[] = editNodes.map((n) => ({
        id: n.id,
        type: n.type,
        name: n.name || undefined,
        config: n.config.trim() ? JSON.parse(n.config) : undefined,
        position: n.pos
      }));
      const r = await publishVersion(key, flowId, { nodes: gnodes, edges: editEdges }, inputSchema);
      toast.success(`Published v${r.version}`);
      await load();
    } catch (e) {
      error = msg(e);
    } finally {
      publishing = false;
    }
  }

  // importJSON loads a flow export (or a bare {graph} / {nodes,edges} object) onto
  // the canvas so it can be reviewed and published — the inverse of JSON export.
  function importJSON(text: string): void {
    error = '';
    if (!text.trim()) {
      error = 'Import failed: paste a flow export or a graph object first';
      return;
    }
    try {
      const raw = JSON.parse(text) as Record<string, unknown>;
      const g = (raw.graph ?? raw) as { nodes?: unknown; edges?: unknown };
      if (!Array.isArray(g.nodes)) {
        throw new Error(
          'expected a "nodes" array (a flow export, or a {graph}/{nodes,edges} object)'
        );
      }
      type N = { id?: string; type?: string; name?: string; config?: unknown; position?: XY };
      type E = { from?: string; to?: string; branch?: string };
      editNodes = (g.nodes as N[]).map((n) => ({
        id: String(n.id ?? ''),
        type: String(n.type ?? ''),
        name: n.name ?? '',
        config: n.config !== undefined ? JSON.stringify(n.config) : '',
        pos: n.position
      }));
      editEdges = Array.isArray(g.edges)
        ? (g.edges as E[]).map((e) => ({
            from: String(e.from ?? ''),
            to: String(e.to ?? ''),
            branch: e.branch
          }))
        : [];
      inputSchema = 'input_schema' in raw ? raw.input_schema : undefined;
      counter = editNodes.length;
      selectedId = '';
      importText = '';
      syncCanvas();
      toast.success(
        `Imported ${editNodes.length} node${editNodes.length === 1 ? '' : 's'} — review, then Publish`
      );
    } catch (e) {
      error = 'Import failed: ' + msg(e);
    }
  }

  async function importFile(ev: Event): Promise<void> {
    const input = ev.currentTarget as HTMLInputElement;
    const file = input.files?.item(0);
    if (!file) return;
    importJSON(await file.text());
    input.value = '';
  }

  let lastDecisionId = $state('');
  let running = $state(false);
  async function run() {
    result = '';
    lastDecisionId = '';
    if (!flow) return;
    running = true;
    try {
      const entity = entityType && entityID ? { type: entityType, id: entityID } : undefined;
      const res = await decide(key, flow.slug, env, JSON.parse(dataText), entity);
      lastDecisionId = res.decision_id ?? '';
      result = JSON.stringify(res, null, 2);
      void loadMetrics();
    } catch (e) {
      result = `Error: ${msg(e)}`;
    } finally {
      running = false;
    }
  }
  async function downloadTrace() {
    try {
      const text = await exportDecision(key, lastDecisionId);
      const url = URL.createObjectURL(new Blob([text], { type: 'text/plain' }));
      const a = document.createElement('a');
      a.href = url;
      a.download = `${lastDecisionId}-trace.mmd`;
      a.click();
      URL.revokeObjectURL(url);
      toast.success('Downloaded run trace');
    } catch (e) {
      error = msg(e);
    }
  }
  async function copyTrace() {
    try {
      await navigator.clipboard.writeText(await exportDecision(key, lastDecisionId));
      toast.success('Copied run trace to clipboard');
    } catch (e) {
      error = msg(e);
    }
  }

  function exportFilename(format: ExportFormat): string {
    const base = flow?.slug ?? flowId;
    if (format === 'bpmn') return `${base}.bpmn`;
    if (format === 'dot') return `${base}.dot`;
    if (format === 'json') return `${base}.json`;
    return format === 'mermaid-state' ? `${base}-state.mmd` : `${base}.mmd`;
  }
  async function downloadExport(format: ExportFormat) {
    try {
      const text = await exportFlow(key, flowId, format);
      const url = URL.createObjectURL(new Blob([text], { type: 'text/plain' }));
      const a = document.createElement('a');
      a.href = url;
      a.download = exportFilename(format);
      a.click();
      URL.revokeObjectURL(url);
      toast.success(`Downloaded ${exportFilename(format)}`);
    } catch (e) {
      error = msg(e);
    }
  }
  async function copyExport(format: ExportFormat) {
    try {
      await navigator.clipboard.writeText(await exportFlow(key, flowId, format));
      toast.success(`Copied ${format} to clipboard`);
    } catch (e) {
      error = msg(e);
    }
  }

  let btDataset = $state('[\n  {}\n]');
  let btCompare = $state('');
  let btReport = $state<BacktestReport | null>(null);
  let btRunning = $state(false);
  async function runBacktest() {
    error = '';
    btReport = null;
    if (!flow) return;
    btRunning = true;
    try {
      const dataset = JSON.parse(btDataset) as Record<string, unknown>[];
      const body: { compare_version?: number; dataset: Record<string, unknown>[] } = { dataset };
      const cv = parseInt(btCompare, 10);
      if (!Number.isNaN(cv) && cv > 0) body.compare_version = cv;
      btReport = await backtestFlow(key, flowId, body);
      toast.success(`Backtested ${btReport.summary.total} records`);
    } catch (e) {
      error = msg(e);
    } finally {
      btRunning = false;
    }
  }

  // Batch decisioning: decide a whole dataset, each row a recorded decision
  let batchDataset = $state('[\n  {}\n]');
  let batchReport = $state<BatchReport | null>(null);
  let batchRunning = $state(false);
  async function runBatch() {
    error = '';
    batchReport = null;
    if (!flow) return;
    batchRunning = true;
    try {
      const dataset = JSON.parse(batchDataset) as Record<string, unknown>[];
      const entity = entityType && entityID ? { type: entityType, id: entityID } : undefined;
      batchReport = await batchDecide(key, flow.slug, env, dataset, entity);
      toast.success(
        `Decided ${batchReport.total} (${batchReport.completed} ok, ${batchReport.rejected} rejected)`
      );
      await loadMetrics();
    } catch (e) {
      error = msg(e);
    } finally {
      batchRunning = false;
    }
  }

  // Promote a batch into pre-approvals: grant a time-boxed pre-decision for
  // every row the bound policy approves, keyed by a field in each row. ---
  let paEntityType = $state('applicant');
  let paEntityKey = $state('applicant_id');
  let paDisposition = $state('approve');
  let paValidDays = $state(30);
  let paReport = $state<PreApproveBatchReport | null>(null);
  let paRunning = $state(false);
  async function runPreapproveBatch() {
    error = '';
    paReport = null;
    if (!flow) return;
    paRunning = true;
    try {
      const dataset = JSON.parse(batchDataset) as Record<string, unknown>[];
      paReport = await preapproveBatch(key, flow.slug, env, {
        dataset,
        entity_type: paEntityType.trim(),
        entity_key: paEntityKey.trim(),
        disposition: paDisposition,
        valid_days: paValidDays
      });
      toast.success(
        `Granted ${paReport.granted} pre-approval${paReport.granted === 1 ? '' : 's'} of ${paReport.total}`
      );
      await loadMetrics();
    } catch (e) {
      error = msg(e);
    } finally {
      paRunning = false;
    }
  }

  let depVersion = $state('');
  let depEnv = $state('sandbox');
  let depChallenger = $state('');
  let depChallengerPct = $state('');
  let deploying = $state(false);

  function liveVersion(environment: string): number | undefined {
    // Lookup via entries (not a computed object index) to stay clear of the
    // detect-object-injection lint — the deployments map comes from JSON.
    const found = Object.entries(flow?.deployments ?? {}).find(([e]) => e === environment);
    return found?.[1]?.version;
  }

  async function submitDeploy() {
    error = '';
    if (!flow) return;
    const version = parseInt(depVersion, 10) || flow.latest;
    const body: {
      environment: string;
      version: number;
      challenger_version?: number;
      challenger_pct?: number;
    } = { environment: depEnv, version };
    const cv = parseInt(depChallenger, 10);
    const pct = parseInt(depChallengerPct, 10);
    if (!Number.isNaN(cv) && cv > 0) {
      body.challenger_version = cv;
      if (!Number.isNaN(pct) && pct > 0) body.challenger_pct = pct;
    }
    deploying = true;
    try {
      // Production is gated by four-eyes: propose for review instead of deploying.
      if (depEnv === 'production') {
        const r = await requestDeployment(key, flowId, body);
        toast.success(`Proposed v${version} for production (request ${r.request_id.slice(0, 8)})`);
      } else {
        await deployVersion(key, flowId, body);
        toast.success(`Deployed v${version} to ${depEnv}`);
      }
      await load();
    } catch (e) {
      error = msg(e);
    } finally {
      deploying = false;
    }
  }

  let promoteFrom = $state('sandbox');
  let promoteTo = $state('staging');
  let promoteForce = $state(false);
  let promoting = $state(false);
  async function submitPromote() {
    error = '';
    if (!flow) return;
    promoting = true;
    try {
      const r = await promoteFlow(key, flowId, promoteFrom, promoteTo, promoteForce);
      toast.success(
        r.promoted
          ? `Promoted v${r.version} ${promoteFrom} → ${promoteTo}`
          : `Proposed v${r.version} for ${promoteTo} (request ${(r.request_id ?? '').slice(0, 8)})`
      );
      await load();
    } catch (e) {
      error = msg(e);
    } finally {
      promoting = false;
    }
  }

  // All deployment requests (newest first) — pending and decided stay visible so
  // the approval/rejection explanation persists with the request.
  let allRequests = $derived([...(flow?.deployment_requests ?? [])].reverse());

  async function approve(reqId: string) {
    const reason = prompt('Approval note (the explanation recorded with this decision):', '');
    if (reason === null) return; // cancelled
    error = '';
    try {
      await approveDeployment(key, flowId, reqId, reason.trim());
      toast.success('Deployment approved and live');
      await load();
    } catch (e) {
      error = msg(e);
    }
  }

  async function reject(reqId: string) {
    const reason = prompt('Reason for rejecting this deployment:', '');
    if (reason === null) return; // cancelled
    error = '';
    try {
      await rejectDeployment(key, flowId, reqId, reason.trim());
      toast.success('Deployment request rejected');
      await load();
    } catch (e) {
      error = msg(e);
    }
  }

  let monitors = $state<Monitor[]>([]);
  let monMetric = $state('failure_rate');
  let monOp = $state('gt');
  let monThreshold = $state(0.05);
  let monDesc = $state('');
  let monBusy = $state(false);
  // Rates are fractions (0–1); volume and latency are absolute.
  const monIsRate = $derived(monMetric.endsWith('_rate'));
  async function loadMonitors() {
    try {
      monitors = await listMonitors(key, flowId);
    } catch {
      monitors = [];
    }
  }
  function fmtActual(m: Monitor): string {
    if (!m.status.computable) return 'no data';
    return m.metric.endsWith('_rate')
      ? `${Math.round(m.status.actual * 100)}%`
      : String(Math.round(m.status.actual));
  }
  function fmtThreshold(m: Monitor): string {
    const sym = m.op === 'gt' ? '>' : '<';
    return m.metric.endsWith('_rate')
      ? `${sym} ${Math.round(m.threshold * 100)}%`
      : `${sym} ${m.threshold}`;
  }
  async function addMonitor() {
    error = '';
    monBusy = true;
    try {
      await defineMonitor(key, flowId, {
        metric: monMetric,
        op: monOp,
        threshold: monThreshold,
        description: monDesc.trim() || undefined
      });
      monDesc = '';
      toast.success('Monitor added');
      await loadMonitors();
    } catch (e) {
      error = msg(e);
    } finally {
      monBusy = false;
    }
  }
  async function removeMonitor(m: Monitor) {
    try {
      await deleteMonitor(key, flowId, m.monitor_id);
      toast.success('Monitor removed');
      await loadMonitors();
    } catch (e) {
      toast.error(msg(e));
    }
  }
  // Check & notify: evaluate the monitors and push firing ones to webhooks.
  let lastCheck = $state<MonitorCheck | null>(null);
  let checking = $state(false);
  async function checkNow() {
    error = '';
    checking = true;
    try {
      lastCheck = await checkMonitors(key, flowId);
      await loadMonitors();
      const n = lastCheck.fired.length;
      const sent = lastCheck.deliveries?.length ?? 0;
      toast.success(n === 0 ? 'No monitors firing' : `${n} firing → ${sent} webhook(s) notified`);
    } catch (e) {
      error = msg(e);
    } finally {
      checking = false;
    }
  }

  // Notification webhooks (tenant-wide delivery targets)
  let webhooks = $state<Webhook[]>([]);
  let hookURL = $state('');
  let hookNote = $state('');
  let hookBusy = $state(false);
  async function loadWebhooks() {
    try {
      webhooks = await listWebhooks(key);
    } catch {
      webhooks = [];
    }
  }
  async function addWebhook() {
    error = '';
    hookBusy = true;
    try {
      await subscribeWebhook(key, hookURL.trim(), hookNote.trim());
      hookURL = '';
      hookNote = '';
      toast.success('Webhook added');
      await loadWebhooks();
    } catch (e) {
      error = msg(e);
    } finally {
      hookBusy = false;
    }
  }
  async function removeWebhook(wid: string) {
    try {
      await deleteWebhook(key, wid);
      toast.success('Webhook removed');
      await loadWebhooks();
    } catch (e) {
      toast.error(msg(e));
    }
  }

  let drift = $state<DriftReport | null>(null);
  async function loadDrift() {
    try {
      drift = await getDrift(key, flowId);
    } catch {
      drift = null;
    }
  }
  async function captureBaselineNow() {
    error = '';
    try {
      await captureBaseline(key, flowId);
      toast.success('Baseline captured');
      await loadDrift();
      await loadMonitors();
    } catch (e) {
      error = msg(e);
    }
  }
  function pct(n: number): string {
    return `${Math.round(n * 100)}%`;
  }

  let assertText = $state(
    '[\n  {\n    "name": "example",\n    "input": {},\n    "expect": {}\n  }\n]'
  );
  let assertReport = $state<AssertionReport | null>(null);
  let assertBusy = $state(false);
  async function loadAssertions() {
    try {
      const cases = await getAssertions(key, flowId);
      if (cases.length) assertText = JSON.stringify(cases, null, 2);
    } catch {
      /* leave the placeholder */
    }
  }
  async function saveAssertions() {
    error = '';
    assertBusy = true;
    try {
      const cases = JSON.parse(assertText);
      await setAssertions(key, flowId, cases);
      toast.success('Assertions saved');
    } catch (e) {
      error = msg(e);
    } finally {
      assertBusy = false;
    }
  }
  async function runAssertionsNow() {
    error = '';
    assertReport = null;
    assertBusy = true;
    try {
      assertReport = await runAssertions(key, flowId);
      toast.success(`${assertReport.passed}/${assertReport.total} assertions passed`);
    } catch (e) {
      error = msg(e);
    } finally {
      assertBusy = false;
    }
  }

  onMount(() => {
    void load();
    void loadMetrics();
    void loadMonitors();
    void loadWebhooks();
    void loadDrift();
    void loadAssertions();
  });
</script>

<main>
  <p><a href="/engine">← all flows</a></p>
  <h1>{flow?.name ?? flowId}</h1>
  <div class="row">
    <button onclick={load}><Icon name="reload" size={15} /> Reload</button>
    <button class="primary" onclick={publish} disabled={publishing}
      ><Icon name="check" size={15} /> {publishing ? 'Publishing…' : 'Publish version'}</button
    >
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
    <div class="grp">
      <button onclick={() => downloadExport('dot')} title="Download Graphviz DOT">
        <Icon name="download" size={14} /> DOT
      </button>
      <button class="icon" aria-label="Copy DOT" title="Copy DOT" onclick={() => copyExport('dot')}>
        <Icon name="copy" size={14} />
      </button>
    </div>
    <div class="grp">
      <button onclick={() => downloadExport('json')} title="Download flow JSON (re-importable)">
        <Icon name="download" size={14} /> JSON
      </button>
      <button
        class="icon"
        aria-label="Copy JSON"
        title="Copy JSON"
        onclick={() => copyExport('json')}
      >
        <Icon name="copy" size={14} />
      </button>
    </div>
  </div>
  <details class="importflow">
    <summary><Icon name="download" size={15} /> Import JSON</summary>
    <p class="importhint">
      Load a flow export (or a bare <code>graph</code> / <code>{'{nodes, edges}'}</code> object)
      onto the canvas, then <b>Publish</b> to save it as a new version — the inverse of JSON export.
    </p>
    <div class="row">
      <input
        type="file"
        accept="application/json,.json"
        aria-label="import flow file"
        onchange={importFile}
      />
    </div>
    <textarea
      class="importbox"
      bind:value={importText}
      placeholder={'{"graph":{"nodes":[…],"edges":[…]}}'}
      aria-label="import flow json"
      rows="4"
    ></textarea>
    <div class="row">
      <button onclick={() => importJSON(importText)} data-testid="import-load">
        <Icon name="download" size={14} /> Load into editor
      </button>
    </div>
  </details>
  {#if metrics && metrics.total > 0}
    <div class="metrics">
      <span class="exportlabel"><Icon name="diagram" size={15} /> Analytics</span>
      <span><b>{metrics.total}</b> decisions</span>
      <span class="ok">{metrics.completed} completed</span>
      <span class="err">{metrics.failed} failed</span>
      <span class="muted">avg {metrics.avg_duration_ms} ms</span>
      {#if automation}
        <span class="ok" data-testid="automation-rate">{automation.rate}% automated</span>
        <span class="muted">{automation.refer} referred</span>
      {/if}
      {#each Object.entries(metrics.by_variant) as [variant, v] (variant)}
        <span class="muted">{variant}: {v.completed}/{v.started}</span>
      {/each}
      <a href="/decisions">view runs →</a>
    </div>
  {/if}

  <section class="monitors" data-testid="monitors-panel">
    <div class="mon-head">
      <h2>Monitors</h2>
      <div class="row">
        <button class="ghost" onclick={loadMonitors} title="Re-evaluate against current metrics">
          <Icon name="reload" size={14} /> Refresh
        </button>
        <button
          class="ghost"
          onclick={captureBaselineNow}
          data-testid="capture-baseline"
          title="Snapshot the current disposition mix as the drift baseline"
        >
          <Icon name="scorecard" size={14} /> Capture baseline
        </button>
        <button
          class="ghost"
          onclick={checkNow}
          disabled={checking}
          data-testid="check-monitors"
          title="Evaluate and push firing monitors to webhooks"
        >
          <Icon name="check" size={14} /> Check &amp; notify
        </button>
      </div>
    </div>
    <p class="muted">
      Thresholds over this flow's live metrics — failure / refer / automation rate, latency, and
      volume. Each is evaluated against the analytics roll-up; a breached rule shows as
      <b>firing</b>.
    </p>
    <div class="row mon-form">
      <label>
        Metric
        <select bind:value={monMetric} aria-label="monitor metric">
          {#each MONITOR_METRICS as m (m)}<option value={m}>{m}</option>{/each}
        </select>
      </label>
      <label>
        When
        <select bind:value={monOp} aria-label="monitor op">
          <option value="gt">above</option>
          <option value="lt">below</option>
        </select>
      </label>
      <label>
        Threshold {monIsRate ? '(0–1)' : ''}
        <input
          type="number"
          step={monIsRate ? '0.01' : '1'}
          bind:value={monThreshold}
          aria-label="monitor threshold"
        />
      </label>
      <label class="grow">
        Note
        <input bind:value={monDesc} aria-label="monitor description" placeholder="optional" />
      </label>
      <button onclick={addMonitor} disabled={monBusy} data-testid="add-monitor">Add monitor</button>
    </div>
    {#if monitors.length > 0}
      <ul class="mon-list">
        {#each monitors as m (m.monitor_id)}
          <li class:firing={m.status.firing}>
            <span class="mon-state" class:firing={m.status.firing}
              >{m.status.firing ? 'firing' : m.status.computable ? 'ok' : 'no data'}</span
            >
            <span class="mon-rule"><b>{m.metric}</b> {fmtThreshold(m)}</span>
            <span class="mon-actual">now: {fmtActual(m)}</span>
            {#if m.description}<span class="muted">{m.description}</span>{/if}
            <button class="link danger" onclick={() => removeMonitor(m)}>remove</button>
          </li>
        {/each}
      </ul>
    {:else}
      <p class="muted">No monitors yet.</p>
    {/if}

    {#if drift}
      <div class="drift" data-testid="drift-panel">
        <span class="exportlabel"><Icon name="scorecard" size={15} /> Distribution drift</span>
        {#if !drift.has_baseline}
          <span class="muted">No baseline captured — use <b>Capture baseline</b> to set one.</span>
        {:else if !drift.has_current}
          <span class="muted">Baseline set; no dispositioned decisions yet.</span>
        {:else}
          <span class:err={drift.max_drift > 0.2} class:ok={drift.max_drift <= 0.2}
            >max drift {pct(drift.max_drift)}</span
          >
          {#each drift.buckets ?? [] as b (b.disposition)}
            <span class="muted"
              >{b.disposition}: {pct(b.baseline)}→{pct(b.current)} ({b.delta >= 0 ? '+' : ''}{pct(
                b.delta
              )})</span
            >
          {/each}
        {/if}
      </div>
    {/if}

    {#if lastCheck && lastCheck.deliveries && lastCheck.deliveries.length > 0}
      <div class="mon-deliveries" data-testid="check-deliveries">
        <span class="exportlabel">Last check delivered to:</span>
        {#each lastCheck.deliveries as d (d.webhook_id)}
          <span class={d.ok ? 'ok' : 'err'}
            >{d.ok ? '✓' : '✗'} {d.url}{d.status ? ` (${d.status})` : ''}</span
          >
        {/each}
      </div>
    {/if}

    <details class="webhooks">
      <summary
        >Notification webhooks <span class="muted">(shared across flows · {webhooks.length})</span
        ></summary
      >
      <div class="row mon-form">
        <label class="grow">
          Endpoint URL
          <input
            bind:value={hookURL}
            aria-label="webhook url"
            placeholder="https://hooks.example.com/alerts"
          />
        </label>
        <label>
          Note
          <input bind:value={hookNote} aria-label="webhook note" placeholder="optional" />
        </label>
        <button
          onclick={addWebhook}
          disabled={hookBusy || !hookURL.trim()}
          data-testid="add-webhook">Add webhook</button
        >
      </div>
      {#if webhooks.length > 0}
        <ul class="mon-list">
          {#each webhooks as h (h.webhook_id)}
            <li>
              <span class="mon-rule">{h.url}</span>
              {#if h.note}<span class="muted">{h.note}</span>{/if}
              <span class="mon-actual"
                >{h.delivery_count} sent{h.delivery_count > 0
                  ? h.last_ok
                    ? ' · last ok'
                    : ' · last failed'
                  : ''}</span
              >
              <button class="link danger" onclick={() => removeWebhook(h.webhook_id)}>remove</button
              >
            </li>
          {/each}
        </ul>
      {:else}
        <p class="muted">
          No webhooks. Add one, then <b>Check &amp; notify</b> pushes firing monitors to it.
        </p>
      {/if}
    </details>
  </section>

  <section class="deploy" data-testid="deploy-panel">
    <h2>Deployment</h2>
    <div class="live">
      <span class="exportlabel"><Icon name="check" size={15} /> Live</span>
      {#each ['sandbox', 'staging', 'production'] as e (e)}
        <span class="env">
          {e}:
          {#if liveVersion(e)}<b>v{liveVersion(e)}</b>{:else}<span class="muted">—</span>{/if}
        </span>
      {/each}
    </div>
    <div class="row">
      <input
        bind:value={depVersion}
        placeholder={`version (default latest v${flow?.latest ?? '?'})`}
        aria-label="deploy version"
        size="22"
        inputmode="numeric"
      />
      <select bind:value={depEnv} aria-label="deploy environment">
        <option value="sandbox">sandbox</option>
        <option value="staging">staging</option>
        <option value="production">production (four-eyes)</option>
      </select>
      <input
        bind:value={depChallenger}
        placeholder="challenger ver (optional)"
        aria-label="challenger version"
        size="18"
        inputmode="numeric"
      />
      <input
        bind:value={depChallengerPct}
        placeholder="challenger %"
        aria-label="challenger pct"
        size="10"
        inputmode="numeric"
      />
      <button
        class="primary"
        onclick={submitDeploy}
        disabled={!flow || deploying}
        data-testid="deploy-submit"
      >
        {#if deploying}Working…{:else if depEnv === 'production'}Propose for review{:else}Deploy{/if}
      </button>
    </div>
    <p class="hint muted">
      Production deploys require maker-checker approval: proposing creates a request that a
      <em>different</em> user must approve.
    </p>

    <div class="row promote-row">
      <span class="exportlabel"><Icon name="split" size={14} /> Promote</span>
      <select bind:value={promoteFrom} aria-label="promote from">
        {#each ['sandbox', 'staging', 'production'] as e (e)}<option value={e}>{e}</option>{/each}
      </select>
      <span aria-hidden="true">→</span>
      <select bind:value={promoteTo} aria-label="promote to">
        {#each ['sandbox', 'staging', 'production'] as e (e)}<option value={e}>{e}</option>{/each}
      </select>
      <button onclick={submitPromote} disabled={!flow || promoting} data-testid="promote-submit">
        {promoting ? 'Working…' : promoteTo === 'production' ? 'Promote (review)' : 'Promote'}
      </button>
      <label class="force"
        ><input type="checkbox" bind:checked={promoteForce} aria-label="force promote" /> force</label
      >
      <span class="hint muted"
        >ships the live version up the chain; blocked if monitors are firing (prod via review).</span
      >
    </div>

    {#if allRequests.length > 0}
      <div class="requests" data-testid="deployment-requests">
        <h3>Deployment requests</h3>
        <table>
          <thead>
            <tr><th>Env</th><th>Version</th><th>Status</th><th>Proposed by</th><th></th></tr>
          </thead>
          <tbody>
            {#each allRequests as r (r.request_id)}
              <tr>
                <td>{r.environment}</td>
                <td
                  >v{r.version}{#if r.challenger_version}
                    + v{r.challenger_version} @ {r.challenger_pct ?? 0}%{/if}</td
                >
                <td><span class="reqstatus {r.status}">{r.status}</span></td>
                <td>{r.requested_by}</td>
                <td class="reqactions">
                  {#if r.status === 'pending'}
                    <button class="primary" onclick={() => approve(r.request_id)}>Approve</button>
                    <button onclick={() => reject(r.request_id)}>Reject</button>
                  {:else}
                    <span class="muted"
                      >{r.status} by {r.decided_by ?? '—'}{#if r.reason}: {r.reason}{/if}</span
                    >
                  {/if}
                </td>
              </tr>
              <tr class="threadrow">
                <td colspan="5">
                  <CommentThread
                    subjectType="deployment_request"
                    subjectId={r.request_id}
                    title="Approval discussion"
                  />
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </section>

  {#if flow && flow.versions.length > 0}
    <section class="versions" data-testid="versions-panel">
      <h2>Versions</h2>
      <div class="vlist">
        {#each [...flow.versions].reverse() as v (v.version)}
          <span class="vchip">
            <b>v{v.version}</b>
            <code>{v.etag.slice(0, 8)}</code>
            {#if v.published_by}<span class="muted">by {v.published_by}</span>{/if}
          </span>
        {/each}
      </div>
      {#if flow.versions.length >= 2}
        <div class="row">
          <label
            >diff <select bind:value={diffA} aria-label="diff base version">
              {#each flow.versions as v (v.version)}<option value={String(v.version)}
                  >v{v.version}</option
                >{/each}
            </select></label
          >
          <span>→</span>
          <select bind:value={diffB} aria-label="diff candidate version">
            {#each flow.versions as v (v.version)}<option value={String(v.version)}
                >v{v.version}</option
              >{/each}
          </select>
        </div>
        {#if graphDiff}
          {#if diffIsEmpty(graphDiff)}
            <p class="muted" data-testid="version-diff">v{diffA} and v{diffB} are identical.</p>
          {:else}
            <ul class="diff" data-testid="version-diff">
              {#each graphDiff.nodesAdded as id (id)}<li>
                  <span class="add">+ node</span>
                  {id}
                </li>{/each}
              {#each graphDiff.nodesRemoved as id (id)}<li>
                  <span class="del">− node</span>
                  {id}
                </li>{/each}
              {#each graphDiff.nodesChanged as id (id)}<li>
                  <span class="chg">~ node</span>
                  {id}
                </li>{/each}
              {#each graphDiff.edgesAdded as e (e)}<li>
                  <span class="add">+ edge</span>
                  {e}
                </li>{/each}
              {#each graphDiff.edgesRemoved as e (e)}<li>
                  <span class="del">− edge</span>
                  {e}
                </li>{/each}
            </ul>
          {/if}
        {/if}
      {:else}
        <p class="muted">Publish another version to compare.</p>
      {/if}
    </section>
  {/if}

  {#if error}<p class="err">{error}</p>{/if}

  <div class="grid">
    <div class="canvas" data-testid="flow-canvas">
      <button
        class="relax-btn"
        onclick={relax}
        title="Auto-arrange every node by flow order (the only thing that moves nodes you've placed)"
        data-testid="relax-layout"
      >
        <Icon name="diagram" size={14} /> Relax layout
      </button>
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
        {:else if selected.type === 'reason'}
          <p class="muted">reason codes (when → code + description)</p>
          {#each reasons() as r, i (i)}
            <div class="row">
              <input
                value={asText(r.when)}
                oninput={(e) => setReason(i, { when: e.currentTarget.value })}
                aria-label={`reason ${i} when`}
                placeholder="when"
              />
              <input
                value={asText(r.code)}
                oninput={(e) => setReason(i, { code: e.currentTarget.value })}
                aria-label={`reason ${i} code`}
                placeholder="code"
                size="8"
              />
              <input
                value={asText(r.description)}
                oninput={(e) => setReason(i, { description: e.currentTarget.value })}
                aria-label={`reason ${i} description`}
                placeholder="description"
              />
              <button class="x" aria-label={`remove reason ${i}`} onclick={() => removeReason(i)}
                >✕</button
              >
            </div>
          {/each}
          <button onclick={addReason}>Add reason</button>
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
      <button onclick={run} disabled={!flow || running}>{running ? 'Running…' : 'Run'}</button>
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

  <section>
    <h2>Backtest</h2>
    <p class="muted">
      Replay a dataset of inputs through the published flow — nothing is recorded. Leave the compare
      version blank to check the latest version, or set it to diff two versions before deploying.
    </p>
    <div class="row">
      <input
        bind:value={btCompare}
        placeholder="compare version (optional)"
        aria-label="compare version"
        size="20"
        inputmode="numeric"
      />
      <button onclick={runBacktest} disabled={!flow || btRunning} data-testid="run-backtest">
        {btRunning ? 'Running…' : 'Run backtest'}
      </button>
    </div>
    <textarea
      bind:value={btDataset}
      aria-label="backtest dataset"
      rows="4"
      placeholder={'[\n  {"score": 720},\n  {"score": 540}\n]'}
    ></textarea>
    {#if btReport}
      <div class="metrics" data-testid="backtest-summary">
        <span>{btReport.summary.total} records</span>
        <span class="ok">{btReport.summary.baseline_completed} completed</span>
        {#if btReport.summary.baseline_failed > 0}
          <span class="err">{btReport.summary.baseline_failed} failed</span>
        {/if}
        {#if btReport.summary.compare}
          <span class="changed">{btReport.summary.changed} changed</span>
        {/if}
      </div>
      {#if btReport.summary.compare && btReport.records.length > 0}
        <table class="bt-table">
          <thead>
            <tr><th>#</th><th>Baseline</th><th>Candidate</th></tr>
          </thead>
          <tbody>
            {#each btReport.records as rec (rec.index)}
              <tr>
                <td>{rec.index}</td>
                <td>{rec.baseline.error || JSON.stringify(rec.baseline.output)}</td>
                <td>{rec.candidate?.error || JSON.stringify(rec.candidate?.output)}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      {/if}
    {/if}
  </section>

  <section>
    <h2>Assertions</h2>
    <p class="muted">
      Stored input→expected tests, run through the pure engine (no recorded decision). A case passes
      when every field in <code>expect</code> equals the flow's output. Failing assertions block a
      <b>promote</b> (override with force).
    </p>
    <div class="row">
      <button onclick={saveAssertions} disabled={!flow || assertBusy} data-testid="save-assertions"
        >Save tests</button
      >
      <button onclick={runAssertionsNow} disabled={!flow || assertBusy} data-testid="run-assertions"
        >Run tests</button
      >
    </div>
    <textarea bind:value={assertText} aria-label="assertion cases" rows="6" spellcheck="false"
    ></textarea>
    {#if assertReport}
      <div class="metrics" data-testid="assert-summary">
        <span>{assertReport.total} cases</span>
        <span class="ok">{assertReport.passed} passed</span>
        {#if assertReport.failed > 0}<span class="err">{assertReport.failed} failed</span>{/if}
      </div>
      <table class="bt-table">
        <thead>
          <tr><th>Case</th><th>Result</th><th>Detail</th></tr>
        </thead>
        <tbody>
          {#each assertReport.results as r (r.name)}
            <tr>
              <td>{r.name}</td>
              <td class={r.passed ? 'ok' : 'err'}>{r.passed ? 'pass' : 'fail'}</td>
              <td
                >{r.error ||
                  (r.mismatch && r.mismatch.length
                    ? `mismatch: ${r.mismatch.join(', ')}`
                    : 'ok')}</td
              >
            </tr>
          {/each}
        </tbody>
      </table>
    {/if}
  </section>

  <section>
    <h2>Batch decide</h2>
    <p class="muted">
      Decide a whole dataset on the <b>{env}</b> environment (from Test run above) — each row is a
      <b>recorded</b> decision (it shows in history, metrics, and the audit log), unlike a backtest. Up
      to 500 rows.
    </p>
    <div class="row">
      <button onclick={runBatch} disabled={!flow || batchRunning} data-testid="run-batch">
        {batchRunning ? 'Deciding…' : 'Run batch'}
      </button>
    </div>
    <textarea
      bind:value={batchDataset}
      aria-label="batch dataset"
      rows="4"
      placeholder={'[\n  {"score": 720},\n  {"score": 540}\n]'}
    ></textarea>
    {#if batchReport}
      <div class="metrics" data-testid="batch-summary">
        <span>{batchReport.total} decided</span>
        <span class="ok">{batchReport.completed} completed</span>
        {#if batchReport.failed > 0}<span class="err">{batchReport.failed} failed</span>{/if}
        {#if batchReport.rejected > 0}<span class="changed">{batchReport.rejected} rejected</span
          >{/if}
      </div>
      <table class="bt-table">
        <thead>
          <tr><th>#</th><th>Status</th><th>Disposition</th><th>Decision</th><th>Detail</th></tr>
        </thead>
        <tbody>
          {#each batchReport.results as r (r.index)}
            <tr>
              <td>{r.index}</td>
              <td
                class={r.status === 'completed' ? 'ok' : r.status === 'failed' ? 'err' : 'changed'}
                >{r.status}</td
              >
              <td>{r.disposition ?? '—'}</td>
              <td>
                {#if r.decision_id}<a href={`/decisions/${r.decision_id}`}>view</a>{:else}—{/if}
              </td>
              <td>{r.error || JSON.stringify(r.data)}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    {/if}
  </section>

  <section>
    <h2>Promote to pre-approvals</h2>
    <p class="muted">
      Run the dataset above through the flow's bound <a href="/policies">policy</a> and grant a
      time-boxed <a href="/preapprovals">pre-approval</a> for every row it disposes to the chosen disposition
      — keyed by a field in each row. Each grant's decision output becomes the stored offer terms, honored
      instantly the next time that entity is decided.
    </p>
    <div class="row pa-controls">
      <label>
        Entity type
        <input
          bind:value={paEntityType}
          aria-label="pre-approve entity type"
          placeholder="applicant"
        />
      </label>
      <label>
        Key field
        <input
          bind:value={paEntityKey}
          aria-label="pre-approve entity key"
          placeholder="applicant_id"
        />
      </label>
      <label>
        Grant on
        <select bind:value={paDisposition} aria-label="pre-approve disposition">
          <option value="approve">approve</option>
          <option value="decline">decline</option>
        </select>
      </label>
      <label>
        Valid (days)
        <input
          type="number"
          min="1"
          max="3650"
          bind:value={paValidDays}
          aria-label="pre-approve valid days"
        />
      </label>
      <button
        onclick={runPreapproveBatch}
        disabled={!flow || paRunning || !paEntityType.trim() || !paEntityKey.trim()}
        data-testid="run-preapprove"
      >
        {paRunning ? 'Granting…' : 'Promote'}
      </button>
    </div>
    {#if paReport}
      <div class="metrics" data-testid="preapprove-summary">
        <span>{paReport.total} rows</span>
        <span class="ok">{paReport.granted} granted</span>
        {#if paReport.skipped > 0}<span class="changed">{paReport.skipped} skipped</span>{/if}
        {#if paReport.failed > 0}<span class="err">{paReport.failed} failed</span>{/if}
        {#if paReport.rejected > 0}<span class="changed">{paReport.rejected} rejected</span>{/if}
      </div>
      <table class="bt-table">
        <thead>
          <tr><th>#</th><th>Entity</th><th>Disposition</th><th>Granted</th><th>Detail</th></tr>
        </thead>
        <tbody>
          {#each paReport.results as r (r.index)}
            <tr>
              <td>{r.index}</td>
              <td>{r.entity_id || '—'}</td>
              <td>{r.disposition ?? '—'}</td>
              <td class={r.granted ? 'ok' : 'changed'}>{r.granted ? 'yes' : 'no'}</td>
              <td>{r.error || r.reason || (r.granted ? 'pre-approved' : '')}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    {/if}
  </section>

  {#if flow}
    <section class="discussion" data-testid="flow-discussion">
      <h2>Discussion</h2>
      <CommentThread subjectType="flow" subjectId={flowId} title="Flow discussion" />
    </section>
  {/if}
</main>

<style>
  main {
    max-width: 72rem;
    margin: 2rem auto;
    padding: 0 1rem;
    font-family: var(--font-ui);
  }
  .grid {
    display: grid;
    grid-template-columns: 1fr 22rem;
    gap: 1rem;
  }
  @media (max-width: 860px) {
    .grid {
      grid-template-columns: 1fr;
    }
  }
  .row {
    display: flex;
    gap: 0.5rem;
    flex-wrap: wrap;
    margin: 0.5rem 0;
    align-items: center;
  }
  .pa-controls {
    align-items: flex-end;
  }
  .force {
    display: inline-flex;
    align-items: center;
    gap: 0.25rem;
    font-size: 0.85rem;
    color: var(--fg-muted);
  }
  .pa-controls label {
    display: flex;
    flex-direction: column;
    gap: 0.2rem;
    font-size: 0.8rem;
    color: var(--fg-subtle);
  }
  .pa-controls input {
    width: 9rem;
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
    font-family: var(--font-mono);
  }
  label {
    display: block;
    margin: 0.4rem 0;
    font-size: 0.85rem;
    color: var(--fg-muted);
  }
  .canvas {
    position: relative;
    height: 460px;
    border: 1px solid var(--border-strong);
    border-radius: 0.5rem;
  }
  .relax-btn {
    position: absolute;
    top: 0.5rem;
    right: 0.5rem;
    z-index: 5;
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
    padding: 0.25rem 0.6rem;
    font-size: 0.8rem;
    background: var(--surface-1);
    border: 1px solid var(--border);
    border-radius: 8px;
    box-shadow: 0 1px 4px rgb(0 0 0 / 0.12);
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
    font-family: var(--font-mono);
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
  .metrics {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.9rem;
    margin: 0.5rem 0;
    padding: 0.5rem 0.7rem;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--surface);
    font-size: 0.88rem;
  }
  .metrics .muted {
    color: var(--fg-subtle);
  }
  .monitors {
    margin: 0.6rem 0;
    padding: 0.7rem 0.9rem;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--surface);
  }
  .mon-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
  }
  .monitors h2 {
    margin: 0 0 0.3rem;
    font-size: 1rem;
  }
  button.ghost {
    background: none;
    border: 1px solid var(--border);
    color: var(--fg-muted);
    font-size: 0.8rem;
  }
  .mon-form {
    align-items: flex-end;
  }
  .mon-form label {
    display: flex;
    flex-direction: column;
    gap: 0.2rem;
    font-size: 0.78rem;
    color: var(--fg-subtle);
  }
  .mon-form label.grow {
    flex: 1;
    min-width: 8rem;
  }
  .mon-form input,
  .mon-form select {
    width: 8rem;
  }
  .mon-form label.grow input {
    width: 100%;
  }
  .mon-list {
    list-style: none;
    padding: 0;
    margin: 0.6rem 0 0;
  }
  .mon-list li {
    display: flex;
    align-items: center;
    gap: 0.6rem;
    padding: 0.35rem 0.4rem;
    border-bottom: 1px solid var(--border);
    font-size: 0.9rem;
  }
  .mon-list li.firing {
    background: color-mix(in srgb, var(--danger) 9%, transparent);
  }
  .mon-state {
    padding: 0.05rem 0.5rem;
    border-radius: 999px;
    font-size: 0.74rem;
    font-weight: 600;
    background: color-mix(in srgb, var(--ok) 18%, transparent);
    color: var(--ok);
    min-width: 3.5rem;
    text-align: center;
  }
  .mon-state.firing {
    background: color-mix(in srgb, var(--danger) 18%, transparent);
    color: var(--danger);
  }
  .mon-actual {
    color: var(--fg-muted);
    font-variant-numeric: tabular-nums;
  }
  .mon-deliveries,
  .drift {
    display: flex;
    flex-wrap: wrap;
    gap: 0.6rem;
    align-items: center;
    margin-top: 0.6rem;
    font-size: 0.82rem;
  }
  .webhooks {
    margin-top: 0.8rem;
    border-top: 1px solid var(--border);
    padding-top: 0.6rem;
  }
  .webhooks summary {
    cursor: pointer;
    font-weight: 550;
    font-size: 0.92rem;
  }
  .deploy {
    margin: 0.6rem 0;
    padding: 0.7rem 0.9rem;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--surface);
  }
  .deploy h2 {
    margin: 0 0 0.5rem;
    font-size: 1rem;
  }
  .deploy .live {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 1rem;
    margin-bottom: 0.6rem;
    font-size: 0.9rem;
  }
  .deploy .hint {
    font-size: 0.8rem;
    margin: 0.4rem 0 0;
  }
  .requests {
    margin-top: 0.8rem;
  }
  .requests h3 {
    font-size: 0.9rem;
    margin: 0 0 0.3rem;
  }
  .requests table {
    width: 100%;
    border-collapse: collapse;
  }
  .requests th,
  .requests td {
    text-align: left;
    padding: 0.4rem 0.5rem;
    border-bottom: 1px solid var(--border);
    font-size: 0.88rem;
  }
  .reqactions {
    display: flex;
    gap: 0.4rem;
    align-items: center;
  }
  .reqstatus {
    padding: 0.05rem 0.45rem;
    border-radius: 999px;
    font-size: 0.74rem;
    font-weight: 600;
    background: var(--surface-2);
    color: var(--fg-muted);
  }
  .reqstatus.pending {
    background: color-mix(in srgb, var(--warn) 18%, transparent);
    color: var(--warn);
  }
  .reqstatus.approved {
    background: color-mix(in srgb, var(--ok) 18%, transparent);
    color: var(--ok);
  }
  .reqstatus.rejected {
    background: color-mix(in srgb, var(--danger) 16%, transparent);
    color: var(--danger);
  }
  .versions {
    margin: 0.6rem 0;
    padding: 0.7rem 0.9rem;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--surface);
  }
  .versions h2 {
    margin: 0 0 0.5rem;
    font-size: 1rem;
  }
  .vlist {
    display: flex;
    flex-wrap: wrap;
    gap: 0.5rem;
    margin-bottom: 0.6rem;
  }
  .vchip {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    padding: 0.2rem 0.5rem;
    border: 1px solid var(--border);
    border-radius: 999px;
    font-size: 0.82rem;
  }
  .diff {
    list-style: none;
    padding: 0;
    margin: 0.5rem 0 0;
    font-family: var(--font-mono);
    font-size: 0.85rem;
  }
  .diff li {
    padding: 0.15rem 0;
  }
  .diff .add {
    color: var(--ok);
  }
  .diff .del {
    color: var(--danger);
  }
  .diff .chg {
    color: var(--accent);
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
  .importflow {
    margin: 0.6rem 0;
  }
  .importflow > summary {
    cursor: pointer;
    display: inline-flex;
    align-items: center;
    gap: 0.4rem;
    font-size: 0.9rem;
    color: var(--fg-muted);
    width: max-content;
  }
  .importflow > summary:hover {
    color: var(--fg);
  }
  .importhint {
    color: var(--fg-subtle);
    font-size: 0.85rem;
    margin: 0.5rem 0;
  }
  .importbox {
    width: 100%;
    box-sizing: border-box;
    resize: vertical;
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
  .changed {
    color: var(--accent);
    font-weight: 600;
  }
  .bt-table {
    width: 100%;
    border-collapse: collapse;
    margin-top: 0.5rem;
    font-size: 0.82rem;
  }
  .bt-table th,
  .bt-table td {
    border: 1px solid var(--border);
    padding: 0.35rem 0.5rem;
    text-align: left;
    vertical-align: top;
    word-break: break-word;
  }
  .bt-table th {
    color: var(--fg-subtle);
    font-weight: 600;
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
