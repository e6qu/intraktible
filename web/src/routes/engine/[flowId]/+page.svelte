<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { page } from '$app/stores';
  import { beforeNavigate } from '$app/navigation';
  import {
    SvelteFlow,
    Background,
    Controls,
    MiniMap,
    SelectionMode,
    type Node,
    type Edge,
    type Connection
  } from '@xyflow/svelte';
  import '@xyflow/svelte/dist/style.css';
  import {
    getFlow,
    getDecision,
    listDecisions,
    flowNodeStats,
    flowCoverage,
    type Coverage,
    publishVersion,
    updateFlow,
    copilotExplain,
    copilotSuggest,
    copilotGenerate,
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
    whatif,
    type SweepReport,
    deployVersion,
    rollbackDeploy,
    scheduleDeploy,
    listSchedules,
    cancelSchedule,
    listGrants,
    addGrant,
    revokeGrant,
    type ScheduledDeploy,
    type FlowGrant,
    promoteFlow,
    setPromotionPolicy,
    getShadow,
    setShadow,
    type ShadowState,
    type EnvShadow,
    requestDeployment,
    approveDeployment,
    rejectDeployment,
    type ExportFormat,
    type DecideResult,
    type Flow,
    type FlowMetrics,
    type PromotionStagePolicy,
    type BacktestReport,
    type BatchReport,
    type PreApproveBatchReport,
    type GraphNode,
    type GraphEdge,
    type Disposition,
    type MonitorOp,
    type MonitorMetric,
    type Environment
  } from '$lib/api';
  import { toast } from '$lib/toast';
  import { appHref } from '$lib/paths';
  import { diffGraphs, diffIsEmpty } from '$lib/diff';
  import { layoutLanes, type XY } from '$lib/layout';
  import { theme } from '$lib/theme';
  import Icon from '$lib/Icon.svelte';
  import Skeleton from '$lib/Skeleton.svelte';
  import FlowBridge from '$lib/FlowBridge.svelte';
  import Badge from '$lib/Badge.svelte';
  import Hint from '$lib/Hint.svelte';
  import CodeSnippet from '$lib/CodeSnippet.svelte';
  import { statusTone, dispositionTone } from '$lib/badge';
  import CommentThread from '$lib/CommentThread.svelte';
  import FlowNode from '$lib/FlowNode.svelte';
  import BpmnNode from '$lib/BpmnNode.svelte';
  import LaneBand from '$lib/LaneBand.svelte';
  import {
    nodeSummary,
    telemetrySummary,
    nodeTypeLabel,
    NODE_TYPES,
    type NodeType
  } from '$lib/nodevis';
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
  import { roleAtLeast } from '$lib/roles';
  import { user } from '$lib/session';

  const ENVIRONMENTS = ['sandbox', 'staging', 'production'];

  interface EditNode {
    id: string;
    type: NodeType;
    name: string;
    config: string;
    pos?: XY; // saved canvas position; absent → auto-laid-out until placed/dragged
    lane?: string; // swimlane (free-form); absent → "Main"
  }

  // API calls authenticate via the session cookie (empty key -> no X-Api-Key header).
  const key = '';
  let flow = $state<Flow | null>(null);
  let error = $state('');
  let loading = $state(true);
  let metrics = $state<FlowMetrics | null>(null);

  // loadMetrics fetches the flow's analytics roll-up (non-fatal if none yet).
  async function loadMetrics() {
    const requested = flowId;
    try {
      const m = await getFlowMetrics(key, flowId);
      if (flowId !== requested) return; // dropped: a newer flow loaded mid-request
      metrics = m;
    } catch {
      if (flowId === requested) metrics = null;
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
  // The selected EDGE, as its index into editEdges (canvas edge ids are `e<i>`).
  // Node and edge selection are mutually exclusive — one inspector at a time.
  let selectedEdgeIdx = $state<number | null>(null);
  function selectNode(id: string | null) {
    selectedId = id;
    selectedEdgeIdx = null;
  }
  function selectEdge(i: number) {
    selectedEdgeIdx = i;
    selectedId = null;
  }
  let nodes = $state.raw<Node[]>([]);
  let edges = $state.raw<Edge[]>([]);
  let canvasView = $state<'cards' | 'bpmn'>('cards');
  // The canvas is the center stage: the node RAIL (left, always visible) is the
  // primary add path — click a type, the node lands by the selection and its
  // inspector opens. The fuller tools panel (typed add, node list, edge editor)
  // stays behind the toolbar toggle so the board starts unobstructed.
  const TOOLS_OPEN_KEY = 'intraktible-tools-open';
  let panelOpen = $state(
    typeof localStorage !== 'undefined' && localStorage.getItem(TOOLS_OPEN_KEY) === '1'
  );
  function togglePanel() {
    panelOpen = !panelOpen;
    if (typeof localStorage !== 'undefined')
      localStorage.setItem(TOOLS_OPEN_KEY, panelOpen ? '1' : '0');
  }
  // The design window has three sizes: board (the default, most of the viewport),
  // focus (a full-viewport takeover for pure design work, Esc exits), and collapsed
  // (a slim bar, so the workflow panels below get the whole page). Persisted so a
  // configure-heavy session stays collapsed across flows.
  type CanvasMode = 'board' | 'focus' | 'collapsed';
  const CANVAS_MODE_KEY = 'intraktible-canvas-mode';
  function storedCanvasMode(): CanvasMode {
    if (typeof localStorage === 'undefined') return 'board';
    const m = localStorage.getItem(CANVAS_MODE_KEY);
    return m === 'collapsed' ? 'collapsed' : 'board'; // focus is per-visit, never restored
  }
  let canvasMode = $state<CanvasMode>(storedCanvasMode());
  function setCanvasMode(m: CanvasMode) {
    canvasMode = m;
    if (typeof localStorage !== 'undefined') localStorage.setItem(CANVAS_MODE_KEY, m);
  }
  // Keyboard: Esc leaves focus mode; f toggles focus; t toggles the tools panel.
  // Window-level (the canvas div only sees keys when focus sits inside it), and
  // inert while the user is typing in any field.
  function typingIn(e: KeyboardEvent): boolean {
    const el = e.target as HTMLElement | null;
    return Boolean(
      el && (el.isContentEditable || ['INPUT', 'TEXTAREA', 'SELECT'].includes(el.tagName))
    );
  }
  function onCanvasKeydown(e: KeyboardEvent) {
    if (e.key === 'Escape' && canvasMode === 'focus') {
      e.preventDefault();
      setCanvasMode('board');
      return;
    }
    // Escape closes an open node/edge inspector (clearing the selection) even when a
    // field inside it holds focus — otherwise a mouse is the only way to dismiss it.
    // Checked before the typing guard for that reason.
    if (e.key === 'Escape' && (selectedId !== null || selectedEdgeIdx !== null)) {
      e.preventDefault();
      selectNode(null);
      return;
    }
    if (typingIn(e) || e.metaKey || e.ctrlKey || e.altKey || !flow) return;
    // The tool/mode shortcuts (f/t/v/h) fire only when the canvas surface is the
    // active area — otherwise reading the Deploy/Monitor panel and pressing 'f'
    // would yank the user into a full-viewport canvas takeover. The canvas pane
    // isn't focusable, so a click on it lands focus on the neutral page landmark
    // (#main, tabindex=-1); treat that — like the body or the canvas subtree — as
    // on-canvas. Focus on an actual control inside a panel is what we suppress.
    const active = document.activeElement;
    const onCanvas =
      active === null ||
      active === document.body ||
      active.id === 'main' ||
      active.closest('[data-testid="flow-canvas"]') !== null;
    if (!onCanvas) return;
    if (e.key === 'f') {
      e.preventDefault();
      setCanvasMode(canvasMode === 'focus' ? 'board' : 'focus');
    } else if (e.key === 't' && canvasMode !== 'collapsed') {
      e.preventDefault();
      togglePanel();
    } else if (e.key === 'v') {
      e.preventDefault();
      tool = 'select';
    } else if (e.key === 'h') {
      e.preventDefault();
      tool = 'pan';
    }
  }
  // The active pointer tool, Miro-style: SELECT (left-drag marquee-selects;
  // middle/right-drag pans) or PAN (left-drag moves the board). v / h switch.
  let tool = $state<'select' | 'pan'>('select');
  // The SvelteFlow instance API, registered by the FlowBridge child — the drop
  // handler needs screenToFlowPosition to land a dragged type under the cursor.
  let flowApi = $state<{
    screenToFlowPosition: (p: { x: number; y: number }) => { x: number; y: number };
  } | null>(null);
  function onRailDragStart(e: DragEvent, t: NodeType) {
    e.dataTransfer?.setData('application/x-intraktible-node-type', t);
    if (e.dataTransfer) e.dataTransfer.effectAllowed = 'copy';
  }
  function onCanvasDrop(e: DragEvent) {
    const t = e.dataTransfer?.getData('application/x-intraktible-node-type') as NodeType | '';
    if (!t || !flowApi) return;
    e.preventDefault();
    foldPositions();
    const id = `n${++counter}`;
    const pos = flowApi.screenToFlowPosition({ x: e.clientX, y: e.clientY });
    editNodes = [...editNodes, { id, type: t, name: '', config: '', pos }];
    selectNode(id);
    clearTelemetry();
    syncCanvas();
  }
  // The canvas is the always-visible primary surface (floated to the top via CSS
  // order); the operational panels live behind tabs so the page is no longer one
  // endless scroll. Default to Test — the most common post-edit action.
  type Tab = 'test' | 'deploy' | 'monitor' | 'discuss' | 'copilot';
  const TAB_IDS: Tab[] = ['test', 'deploy', 'monitor', 'discuss', 'copilot'];
  // Honour a ?tab= deep link (e.g. an approver following a "review this deploy" link
  // lands directly on the Deploy tab), falling back to Test.
  function tabFromUrl(): Tab {
    const t = $page.url.searchParams.get('tab') as Tab | null;
    return t && TAB_IDS.includes(t) ? t : 'test';
  }
  let tab = $state<Tab>(tabFromUrl());
  let tabbarEl = $state<HTMLElement | null>(null);
  // Switching panels scrolls the tabbar to the top of the view: with a tall board
  // above, the freshly opened panel was otherwise below the fold and a click
  // appeared to do nothing.
  function selectTab(t: Tab) {
    tab = t;
    // The error banner is a page-load failure; a stale one must not bleed across tabs.
    error = '';
    tabbarEl?.scrollIntoView({ behavior: 'smooth', block: 'start' });
  }
  const TABS: { id: Tab; label: string; hint: string }[] = [
    {
      id: 'test',
      label: 'Test & analyze',
      hint: 'Test runs, backtests, what-if, assertions, coverage, batch decide'
    },
    {
      id: 'deploy',
      label: 'Deploy & versions',
      hint: 'Deployments per environment, four-eyes requests, schedules, version diff, grants'
    },
    {
      id: 'monitor',
      label: 'Monitors',
      hint: 'Thresholds over live metrics + drift, with webhook alerts'
    },
    { id: 'discuss', label: 'Discussion', hint: 'Comment thread on this flow' },
    { id: 'copilot', label: 'Copilot', hint: 'Explain, improve, or generate the flow with AI' }
  ];
  // Typed cards and BPMN notation are alternate skins over the same flow model;
  // labelled backdrops still render as swimlanes.
  const nodeTypes = { flow: FlowNode, bpmn: BpmnNode, lane: LaneBand };
  // node id → last test-run output summary, shown on the card; cleared on edits
  // (a structure/config change makes the prior run's per-node output stale).
  let nodeTelemetry = $state(new Map<string, string>());
  // Bumped on every edit (clearTelemetry runs on edit): the loadTelemetry retry loop
  // spans ~1s, and an edit during that window changes the graph, so a late retry
  // would repaint the previous run's outputs onto the now-changed canvas.
  // loadTelemetry captures this and abandons if it changed — the flowId guard, for edits.
  let telemetryGen = 0;
  function clearTelemetry() {
    telemetryGen++;
    if (nodeTelemetry.size > 0) nodeTelemetry = new Map();
  }

  // --- Decision intelligence: heatmap, replay, coverage ---
  // Heatmap: node id -> traversal stats over recorded decisions.
  let nodeHeat = $state(new Map<string, { count: number; pct: number }>());
  let heatOn = $state(false);
  let heatBusy = $state(false);
  let heatTotal = $state(0);
  // Replay: node id -> its role on the decision path currently being stepped through.
  let replayState = $state(new Map<string, 'head' | 'trail'>());
  let replaying = $state(false);
  // Coverage / red-team report for the published graph.
  let coverageReport = $state<Coverage | null>(null);
  let coverageBusy = $state(false);

  async function toggleHeat() {
    if (heatOn) {
      heatOn = false;
      syncCanvas();
      return;
    }
    heatBusy = true;
    try {
      const s = await flowNodeStats(key, flowId);
      nodeHeat = new Map(s.nodes.map((n) => [n.node_id, { count: n.count, pct: n.pct }]));
      heatTotal = s.total;
      heatOn = true;
      syncCanvas();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e));
    } finally {
      heatBusy = false;
    }
  }

  const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
  // Replay the most recent recorded decision for this flow by lighting its node path,
  // one step at a time, so the decision's route is visible on the canvas.
  async function replayLatest() {
    if (replaying) return;
    const requested = flowId; // abandon the animation if sibling navigation swaps the flow
    replaying = true;
    try {
      const recent = (await listDecisions(key)).filter(
        (d) => d.flow_id === requested && (d.nodes?.length ?? 0) > 0
      );
      if (flowId !== requested) return;
      const d = recent[0];
      if (!d) {
        toast.error('No recorded decisions to replay for this flow yet.');
        return;
      }
      const path = (d.nodes ?? []).map((n) => n.node_id);
      for (let i = 0; i < path.length; i++) {
        if (flowId !== requested) return;
        const m = new Map<string, 'head' | 'trail'>();
        for (const nodeId of path.slice(0, i)) m.set(nodeId, 'trail');
        const head = path.at(i);
        if (head === undefined) throw new Error('replay path changed during playback');
        m.set(head, 'head');
        replayState = m;
        syncCanvas();
        await sleep(480);
      }
      await sleep(900);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e));
    } finally {
      replayState = new Map();
      replaying = false;
      syncCanvas();
    }
  }

  async function runCoverage() {
    if (coverageBusy) return;
    coverageBusy = true;
    try {
      coverageReport = await flowCoverage(key, flowId, { runs: 300 });
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e));
    } finally {
      coverageBusy = false;
    }
  }

  let newType = $state<NodeType>('input');
  let edgeFrom = $state('');
  let edgeTo = $state('');
  let edgeBranch = $state('');

  // Default the test run to sandbox — you'd never test directly against production,
  // and it keeps experimental runs out of the production decisions/cases surfaces.
  let env = $state<Environment>('sandbox');
  let dataText = $state('{}');
  // Live JSON validity for the test-run input, and a one-click skeleton built from
  // the flow's input schema so you don't have to hand-write the shape.
  const dataValid = $derived.by(() => {
    try {
      JSON.parse(dataText);
      return true;
    } catch {
      return false;
    }
  });
  type SchemaProp = { type?: string; example?: unknown; default?: unknown; enum?: unknown[] };
  // Prefer a representative value from the schema (example/default/enum) so the sample
  // input exercises a real branch instead of zeros — a test run that routes and returns
  // a disposition is far more instructive than one that fails "no branch matched".
  function sampleValue(p?: SchemaProp): unknown {
    if (p?.example !== undefined) return p.example;
    if (p?.default !== undefined) return p.default;
    if (Array.isArray(p?.enum) && p.enum.length) return p.enum[0];
    switch (p?.type) {
      case 'number':
      case 'integer':
        return 1;
      case 'boolean':
        return false;
      case 'array':
        return [];
      case 'object':
        return {};
      default:
        return '';
    }
  }
  const hasInputSchema = $derived(
    Boolean((inputSchema as { properties?: Record<string, SchemaProp> } | undefined)?.properties)
  );
  function sampleFromSchema() {
    const props = (inputSchema as { properties?: Record<string, SchemaProp> } | undefined)
      ?.properties;
    if (!props) return; // unreachable: the button is disabled without a schema
    // Build via fromEntries (no dynamic-key writes — keeps eslint-security clean).
    const obj = Object.fromEntries(Object.entries(props).map(([k, p]) => [k, sampleValue(p)]));
    dataText = JSON.stringify(obj, null, 2);
  }
  // A varied N-row dataset derived from the same schema: numbers spread ±40%
  // deterministically, enums cycle, booleans alternate — enough dispersion for a
  // backtest/what-if/batch to show real outcome mix instead of N identical rows.
  function sampleRow(i: number): Record<string, unknown> {
    const props = (inputSchema as { properties?: Record<string, SchemaProp> } | undefined)
      ?.properties;
    if (!props) throw new Error('sample dataset requires a published input schema');
    return Object.fromEntries(
      Object.entries(props).map(([k, p]) => {
        if (Array.isArray(p?.enum) && p.enum.length) return [k, p.enum[i % p.enum.length]];
        const v = sampleValue(p);
        if (typeof v === 'number') {
          const spread = v * (0.6 + (0.8 * ((i * 7) % 10)) / 9);
          return [k, p?.type === 'integer' ? Math.round(spread) : Math.round(spread * 100) / 100];
        }
        if (typeof v === 'boolean') return [k, i % 2 === 0];
        return [k, v];
      })
    );
  }
  const sampleDatasetJson = () =>
    JSON.stringify(
      Array.from({ length: 8 }, (_, i) => sampleRow(i)),
      null,
      2
    );
  const firstNumericField = $derived.by(() => {
    const props = (inputSchema as { properties?: Record<string, SchemaProp> } | undefined)
      ?.properties;
    if (!props) return null;
    const hit = Object.entries(props).find(
      ([, p]) => p?.type === 'number' || p?.type === 'integer'
    );
    return hit ? { name: hit[0], base: Number(sampleValue(hit[1])) } : null;
  });
  function sampleWhatIf() {
    const f = firstNumericField;
    if (!f) throw new Error('what-if sample requires a numeric schema field');
    wiField = f.name;
    wiValues = [0.5, 0.75, 1, 1.25, 1.5].map((m) => Math.round(f.base * m * 100) / 100).join(', ');
    wiBase = JSON.stringify(sampleRow(0), null, 2);
  }
  let entityType = $state('');
  let entityID = $state('');
  // The exact API call this test run is — so a developer can copy/paste the same
  // decision against the deployed flow (the "API-first" claim, shown not just stated).
  const apiSnippet = $derived.by(() => {
    let body: string;
    try {
      body = JSON.stringify({ data: JSON.parse(dataText) });
    } catch {
      body = '{"data": {}}';
    }
    return [
      `curl -X POST https://YOUR_HOST/v1/flows/${flow?.slug ?? flowId}/${env}/decide \\`,
      `  -H "X-Api-Key: YOUR_API_KEY" \\`,
      `  -H "Content-Type: application/json" \\`,
      `  -d '${body}'`
    ].join('\n');
  });
  let result = $state('');

  // Derive from the route param so navigating between sibling flows reloads.
  const flowId = $derived($page.params.flowId ?? '');
  const selected = $derived(editNodes.find((n) => n.id === selectedId) ?? null);
  // Existing lane names, for the lane-input autocomplete.
  const laneNames = $derived([
    ...new Set(editNodes.map((n) => n.lane).filter(Boolean))
  ] as string[]);

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
  // cardSummary is nodeSummary for most nodes, but a split's branch count lives in
  // its outgoing edges (the builder models branches as labelled edges, not a config
  // key), so it is computed here rather than from the node config.
  function cardSummary(n: { id: string; type: string; config: string }): string {
    if (n.type === 'split') {
      const count = editEdges.filter((e) => e.from === n.id).length;
      return count ? `${count} branch${count === 1 ? '' : 'es'}` : 'branch';
    }
    return nodeSummary(n.type, n.config);
  }

  function syncCanvas() {
    foldPositions();
    const { pos: auto } = layoutLanes(editNodes, editEdges);
    const flowNodeType = canvasView === 'bpmn' ? 'bpmn' : 'flow';
    const flowNodes = editNodes.map((n) => ({
      id: n.id,
      type: flowNodeType,
      position: n.pos ?? auto.get(n.id) ?? { x: 0, y: 0 },
      data: {
        type: n.type,
        name: n.name || n.id,
        summary: cardSummary(n),
        telemetry: nodeTelemetry.get(n.id),
        heat: heatOn ? (nodeHeat.get(n.id) ?? { count: 0, pct: 0 }) : undefined,
        replay: replayState.get(n.id)
      }
    }));
    // Lane backdrops are drawn (behind the cards) only when more than one lane is
    // in use, sized to the actual positions so they hold even after free dragging.
    nodes = [...laneBackdrops(flowNodes), ...flowNodes];
    edges = editEdges.map((e, i) => ({
      id: `e${i}`,
      source: e.from,
      target: e.to,
      label: e.branch
    }));
  }
  const LANE_NODE_W = 180;
  const LANE_NODE_H = 70;
  const LANE_INSET = 18;
  function laneBackdrops(flowNodes: Node[]): Node[] {
    const laneOf = new Map(editNodes.map((n) => [n.id, n.lane || 'Main']));
    if (new Set(laneOf.values()).size < 2) return []; // no lanes in use → no clutter
    const order: string[] = [];
    const box = new Map<string, { x0: number; y0: number; x1: number; y1: number }>();
    for (const fn of flowNodes) {
      const lane = laneOf.get(fn.id) ?? 'Main';
      const { x, y } = fn.position;
      const b = box.get(lane);
      if (!b) {
        box.set(lane, { x0: x, y0: y, x1: x + LANE_NODE_W, y1: y + LANE_NODE_H });
        order.push(lane);
      } else {
        b.x0 = Math.min(b.x0, x);
        b.y0 = Math.min(b.y0, y);
        b.x1 = Math.max(b.x1, x + LANE_NODE_W);
        b.y1 = Math.max(b.y1, y + LANE_NODE_H);
      }
    }
    return order.map((lane) => {
      const b = box.get(lane) as { x0: number; y0: number; x1: number; y1: number };
      const width = b.x1 - b.x0 + LANE_INSET * 2;
      const height = b.y1 - b.y0 + LANE_INSET * 2 + 8;
      return {
        id: `lane:${lane}`,
        type: 'lane',
        position: { x: b.x0 - LANE_INSET, y: b.y0 - LANE_INSET - 8 },
        draggable: false,
        selectable: false,
        connectable: false,
        zIndex: -1,
        data: { label: lane, width, height }
      };
    });
  }
  // Relax: adopt the lane-aware auto-layout for every node (the only thing that
  // moves nodes the user already placed — and only when they ask for it).
  function relax() {
    const { pos } = layoutLanes(editNodes, editEdges);
    editNodes = editNodes.map((n) => ({ ...n, pos: pos.get(n.id) ?? n.pos }));
    syncCanvas();
  }
  function setCanvasView(view: 'cards' | 'bpmn') {
    if (canvasView === view) return;
    canvasView = view;
    syncCanvas();
  }

  async function load() {
    error = '';
    loading = true;
    // Capture the flow this load is for; sibling navigation changes flowId while a
    // request is in flight, and a stale response must not clobber the new flow's
    // editor/canvas state (last-write-wins race).
    const requested = flowId;
    try {
      const loaded = await getFlow(key, flowId);
      if (flowId !== requested) return;
      flow = loaded;
      // Seed the editor from the flow's declared latest version, not the last
      // array element — the API does not guarantee versions are returned ordered.
      const byVersion = [...flow.versions].sort((a, b) => a.version - b.version);
      const version = flow.versions.find((v) => v.version === flow?.latest) ?? byVersion.at(-1);
      if (version) {
        editNodes = version.graph.nodes.map((n) => ({
          id: n.id,
          type: n.type as NodeType, // wire boundary: server graph carries the type as string
          name: n.name ?? '',
          config: n.config ? JSON.stringify(n.config) : '',
          pos: n.position,
          lane: n.lane
        }));
        editEdges = version.graph.edges.map((e) => ({ from: e.from, to: e.to, branch: e.branch }));
        inputSchema = version.input_schema;
        counter = editNodes.length;
        syncCanvas();
        // Prefill the test input from the schema so the FIRST Run routes a real branch
        // and returns a disposition, rather than failing "no branch matched" on {}.
        if (dataText === '{}') sampleFromSchema();
      }
      // Default the version-diff selectors to the two most recent versions (by
      // version number, independent of array order).
      if (byVersion.length > 0) {
        diffB = String(byVersion[byVersion.length - 1].version);
        diffA = String(
          byVersion[byVersion.length >= 2 ? byVersion.length - 2 : byVersion.length - 1].version
        );
      }
      // Snapshot the just-loaded graph as the saved baseline, so an author's later
      // edits register as unsaved and the leave-guard can warn before they're lost.
      savedFingerprint = graphFingerprint();
    } catch (e) {
      // Page-level load failure renders in the banner (the whole panel is unusable).
      error = msg(e);
    } finally {
      if (flowId === requested) loading = false;
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

  function addNodeOfType(t: NodeType) {
    newType = t;
    addNode();
  }
  function addNode() {
    foldPositions(); // capture any drags so they survive the rebuild
    const id = `n${++counter}`;
    editNodes = [...editNodes, { id, type: newType, name: '', config: '', pos: nextNodePos() }];
    selectedId = id;
    clearTelemetry();
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
    clearTelemetry();
    syncCanvas();
  }
  // Canvas Backspace/Delete removes elements from SvelteFlow's bound nodes/edges only;
  // without this the deletion is NOT folded back into editNodes/editEdges (the publish
  // source of truth), so the element reappears on the next syncCanvas() and — worse —
  // gets re-published. Reconcile the model with what the canvas deleted (the inverse of
  // onConnect's add-reconciliation).
  function onCanvasDelete(detail: {
    nodes: { id: string }[];
    edges: { source: string; target: string }[];
  }) {
    foldPositions();
    const delIds = new Set(detail.nodes.map((n) => n.id));
    if (delIds.size > 0) {
      editNodes = editNodes.filter((n) => !delIds.has(n.id));
      editEdges = editEdges.filter((e) => !delIds.has(e.from) && !delIds.has(e.to));
      if (selectedId && delIds.has(selectedId)) selectedId = null;
    }
    for (const e of detail.edges) {
      editEdges = editEdges.filter((ed) => !(ed.from === e.source && ed.to === e.target));
    }
    // Removing edges shifts their indices; the edge inspector binds by POSITION
    // (selectedEdge = editEdges[selectedEdgeIdx]), so clear it rather than let it
    // silently rebind to a different edge that slid into that slot (matches deleteEdge).
    if (selectedEdgeIdx !== null && (delIds.size > 0 || detail.edges.length > 0)) {
      selectedEdgeIdx = null;
    }
    clearTelemetry();
    syncCanvas();
  }
  function updateSelected(patch: Partial<EditNode>) {
    editNodes = editNodes.map((n) => (n.id === selectedId ? { ...n, ...patch } : n));
    clearTelemetry();
    syncCanvas();
  }

  // Node types with a structured panel; the raw-JSON textarea stays available for
  // every type as the advanced view.
  const STRUCTURED = [
    'split',
    'connect',
    'ai',
    'predict',
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

  // Scorecard bands: bands[] = {min, label, reason_codes:[{code, description}]}; the
  // summed score falls into the highest band whose min it reaches, labelling the
  // outcome (a grade) and emitting that band's adverse-action reason codes.
  type BandCode = { code?: string; description?: string };
  type Band = { min?: number; label?: string; reason_codes?: BandCode[] };
  function bands(): Band[] {
    const b = nodeCfg().bands;
    return Array.isArray(b) ? (b as Band[]) : [];
  }
  function addBand() {
    patchCfg({ bands: [...bands(), { min: 0, label: '', reason_codes: [] }] });
  }
  function removeBand(i: number) {
    patchCfg({ bands: bands().filter((_, j) => j !== i) });
  }
  function setBand(i: number, patch: Band) {
    patchCfg({ bands: bands().map((b, j) => (j === i ? { ...b, ...patch } : b)) });
  }
  function bandCodes(i: number): BandCode[] {
    const c = bands().at(i)?.reason_codes;
    return Array.isArray(c) ? c : [];
  }
  function addBandCode(i: number) {
    setBand(i, { reason_codes: [...bandCodes(i), { code: '', description: '' }] });
  }
  function removeBandCode(i: number, k: number) {
    setBand(i, { reason_codes: bandCodes(i).filter((_, j) => j !== k) });
  }
  function setBandCode(i: number, k: number, patch: BandCode) {
    setBand(i, {
      reason_codes: bandCodes(i).map((c, j) => (j === k ? { ...c, ...patch } : c))
    });
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
    if (!edgeFrom || !edgeTo) {
      toast.error('Pick both a from and a to node.');
      return;
    }
    if (edgeFrom === edgeTo) {
      toast.error('An edge must connect two different nodes.');
      return;
    }
    const branch = edgeBranch.trim();
    // A branched edge is distinct per branch; an unbranched one dedupes through the
    // same path as drag-connect so the manual form can't add a duplicate either.
    if (branch) {
      if (editEdges.some((e) => e.from === edgeFrom && e.to === edgeTo && e.branch === branch)) {
        toast.error('That branch edge already exists.');
        return;
      }
      editEdges = [...editEdges, { from: edgeFrom, to: edgeTo, branch }];
    } else {
      const next = addUniqueEdge(editEdges, edgeFrom, edgeTo);
      if (next === editEdges) {
        toast.error('That edge already exists.');
        return;
      }
      editEdges = next;
    }
    edgeFrom = edgeTo = edgeBranch = '';
    clearTelemetry();
    syncCanvas();
  }
  // Drag-to-connect on the canvas: dragging from a node's handle to another adds
  // an (unbranched) edge, deduplicated against the existing ones.
  function onConnect(conn: Connection) {
    editEdges = addUniqueEdge(editEdges, conn.source, conn.target);
    clearTelemetry();
    syncCanvas();
  }
  function deleteEdge(i: number) {
    editEdges = editEdges.filter((_, j) => j !== i);
    if (selectedEdgeIdx !== null) selectedEdgeIdx = null; // indices shifted
    clearTelemetry();
    syncCanvas();
  }
  const selectedEdge = $derived(
    selectedEdgeIdx !== null ? (editEdges.at(selectedEdgeIdx) ?? null) : null
  );
  function updateSelectedEdgeBranch(branch: string) {
    const i = selectedEdgeIdx;
    if (i === null) return;
    editEdges = editEdges.map((e, j) => (j === i ? { ...e, branch: branch || undefined } : e));
    clearTelemetry();
    syncCanvas();
  }
  // duplicateSelected clones the selected node (config, lane, name) next to the
  // original and moves selection to the copy — the fastest way to stamp out a
  // variant of an already-configured node.
  function duplicateSelected() {
    const src = editNodes.find((n) => n.id === selectedId);
    if (!src) throw new Error(`duplicate: selected node ${selectedId} is not in the draft`);
    foldPositions();
    const id = `n${++counter}`;
    editNodes = [
      ...editNodes,
      {
        ...src,
        id,
        name: src.name ? `${src.name} copy` : '',
        pos: src.pos ? { x: src.pos.x + 40, y: src.pos.y + 90 } : undefined
      }
    ];
    selectNode(id);
    clearTelemetry();
    syncCanvas();
  }

  // currentGraph maps the editor state to the {nodes, edges} graph shape — shared by
  // publish and the copilot (which explains the live, unpublished graph).
  function currentGraph(): { nodes: GraphNode[]; edges: typeof editEdges } {
    const nodes: GraphNode[] = editNodes.map((n) => {
      let config: unknown;
      if (n.config.trim()) {
        try {
          config = JSON.parse(n.config);
        } catch {
          // Name the offending node so the author can fix it, instead of a bare
          // "Unexpected token" with no clue which card is broken.
          throw new Error(`Node "${n.name || n.id}" has invalid JSON config`);
        }
        // A node config must be a JSON object — a bare number/array/string/null
        // parses fine but the engine drops it, silently losing the node's logic.
        if (typeof config !== 'object' || config === null || Array.isArray(config)) {
          throw new Error(`Node "${n.name || n.id}" config must be a JSON object`);
        }
      }
      return {
        id: n.id,
        type: n.type,
        name: n.name || undefined,
        config,
        position: n.pos,
        lane: n.lane || undefined
      };
    });
    return { nodes, edges: editEdges };
  }

  // Unsaved-changes tracking. The builder holds every edit in memory until Publish,
  // so leaving the page (a link, the browser back button, a reload) would silently
  // discard it. graphFingerprint serializes the LOGIC (nodes + edges + config), not
  // node positions — a pure re-layout is cosmetic and shouldn't nag. savedFingerprint
  // is set to the current graph after each load/publish; dirty is the diff.
  let savedFingerprint = $state('');
  function graphFingerprint(): string {
    return JSON.stringify({
      nodes: editNodes.map((n) => ({
        id: n.id,
        type: n.type,
        name: n.name,
        config: n.config,
        lane: n.lane
      })),
      edges: editEdges.map((e) => ({ from: e.from, to: e.to, branch: e.branch }))
    });
  }
  const dirty = $derived(flow !== null && !loading && graphFingerprint() !== savedFingerprint);

  // Warn before a navigation that would drop unsaved edits (SvelteKit link/back).
  beforeNavigate((nav) => {
    if (
      dirty &&
      !confirm('You have unsaved changes to this flow. Leave without publishing them?')
    ) {
      nav.cancel();
    }
  });
  // Warn before a browser-level reload/close too. Registered only while dirty.
  $effect(() => {
    if (!dirty) return;
    const handler = (e: BeforeUnloadEvent) => e.preventDefault();
    window.addEventListener('beforeunload', handler);
    return () => window.removeEventListener('beforeunload', handler);
  });

  let publishing = $state(false);
  async function publish() {
    error = '';
    publishing = true;
    try {
      foldPositions(); // persist the current canvas layout with the version
      const r = await publishVersion(key, flowId, currentGraph(), inputSchema);
      toast.success(
        r.published === false ? `Already at v${r.version} — no change` : `Published v${r.version}`
      );
      await load();
    } catch (e) {
      toast.error(msg(e));
    } finally {
      publishing = false;
    }
  }

  // Inline description edit: pencil → textarea → save via PATCH. The save failure
  // surfaces loudly in the edit block (the draft is kept so nothing is lost).
  let descEditing = $state(false);
  let descDraft = $state('');
  let descSaving = $state(false);
  let descError = $state('');
  function startDescEdit() {
    descDraft = flow?.description ?? '';
    descError = '';
    descEditing = true;
  }
  async function saveDescription() {
    if (descSaving) return;
    descSaving = true;
    descError = '';
    try {
      await updateFlow(key, flowId, { description: descDraft.trim() });
      descEditing = false;
      toast.success('Description saved');
      await load();
    } catch (e) {
      descError = msg(e);
      toast.error(msg(e));
    } finally {
      descSaving = false;
    }
  }

  // "Analyze with AI": the header robot button runs the same copilot explain the
  // Copilot tab offers, but surfaces the readout in a panel right by the header —
  // no tab switch needed. State is separate from the tab's so neither clobbers
  // the other.
  let analyzeOpen = $state(false);
  let analyzeBusy = $state(false);
  let analyzeOut = $state('');
  let analyzeError = $state('');
  async function analyzeFlow() {
    analyzeOpen = true;
    analyzeBusy = true;
    analyzeOut = '';
    analyzeError = '';
    try {
      analyzeOut = await copilotExplain(key, currentGraph());
    } catch (e) {
      analyzeError = msg(e);
    } finally {
      analyzeBusy = false;
    }
  }

  // Authoring copilot: explain the live graph, or suggest logic from a description.
  let copilotPrompt = $state('');
  let copilotOut = $state('');
  let copilotBusy = $state(false);
  async function explainFlow() {
    copilotBusy = true;
    copilotOut = '';
    error = '';
    try {
      copilotOut = await copilotExplain(key, currentGraph());
    } catch (e) {
      toast.error(msg(e));
    } finally {
      copilotBusy = false;
    }
  }
  async function suggestLogic() {
    if (!copilotPrompt.trim() || copilotBusy) return;
    copilotBusy = true;
    copilotOut = '';
    error = '';
    try {
      copilotOut = await copilotSuggest(key, copilotPrompt.trim());
    } catch (e) {
      toast.error(msg(e));
    } finally {
      copilotBusy = false;
    }
  }
  // Generate a server-validated graph and apply it to the canvas (reuses importJSON,
  // so the result is reviewed before publish). A model that can't produce a valid
  // flow surfaces the server's 422 message rather than applying anything.
  async function generateFlow() {
    if (!copilotPrompt.trim() || copilotBusy) return;
    copilotBusy = true;
    copilotOut = '';
    error = '';
    try {
      const graph = await copilotGenerate(key, copilotPrompt.trim());
      importJSON(JSON.stringify(graph));
      copilotOut = 'Generated a flow and applied it to the canvas — review it, then Publish.';
    } catch (e) {
      toast.error(msg(e));
    } finally {
      copilotBusy = false;
    }
  }

  // importJSON loads a flow export (or a bare {graph} / {nodes,edges} object) onto
  // the canvas so it can be reviewed and published — the inverse of JSON export.
  function importJSON(text: string): void {
    error = '';
    if (!text.trim()) {
      toast.error('Import failed: paste a flow export or a graph object first');
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
      type N = {
        id?: string;
        type?: string;
        name?: string;
        config?: unknown;
        position?: XY;
        lane?: string;
      };
      type E = { from?: string; to?: string; branch?: string };
      editNodes = (g.nodes as N[]).map((n) => ({
        id: String(n.id ?? ''),
        type: String(n.type ?? '') as NodeType, // wire boundary: imported graph type is free-form
        name: n.name ?? '',
        config: n.config !== undefined ? JSON.stringify(n.config) : '',
        pos: n.position,
        lane: n.lane
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
      selectedId = null; // null is the "nothing selected" sentinel; '' would match an empty-id node
      importText = '';
      syncCanvas();
      toast.success(
        `Imported ${editNodes.length} node${editNodes.length === 1 ? '' : 's'} — review, then Publish`
      );
    } catch (e) {
      toast.error('Import failed: ' + msg(e));
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
  // The parsed last run (for the verdict card) and the wall-clock round-trip, kept
  // alongside the raw `result` JSON (which still backs the data-testid="run-result"
  // <pre> behind the details). null until the first run / cleared on an error.
  let runResult = $state<DecideResult | null>(null);
  let runError = $state('');
  let runMs = $state<number | null>(null);
  // Preview = a dry run that records nothing (no decision/audit/metrics); off by
  // default, so an ordinary test records a sandbox decision you can inspect.
  let preview = $state(false);
  // The decide result's primary output fields (the `data` map) for the card — at
  // most a handful, the rest collapse into the raw JSON. Fields the flow PRODUCED
  // sort before fields that merely echo the submitted input, so the run's actual
  // contribution (a derived score, granted terms) isn't pushed out of the visible
  // slots by the echo; reason_codes render as badges above, not as a field.
  const runOutputFields = $derived.by(() => {
    const d = runResult?.data;
    if (!d || typeof d !== 'object' || Array.isArray(d)) return [];
    const echoed = new Set(dataValid ? Object.keys(JSON.parse(dataText)) : []);
    return Object.entries(d as Record<string, unknown>)
      .filter(([k]) => k !== 'reason_codes')
      .sort(([a], [b]) => Number(echoed.has(a)) - Number(echoed.has(b)));
  });
  // The run's reason codes ({code, description}[]) for the verdict badges.
  const runReasonCodes = $derived.by(() => {
    const d = runResult?.data;
    const codes = d && typeof d === 'object' ? (d as Record<string, unknown>).reason_codes : null;
    return Array.isArray(codes)
      ? codes.filter((c): c is { code: string; description?: string } =>
          Boolean(c && typeof c === 'object' && 'code' in c)
        )
      : [];
  });
  function fieldText(v: unknown): string {
    return typeof v === 'object' && v !== null ? JSON.stringify(v) : String(v);
  }
  async function run() {
    result = '';
    runResult = null;
    runError = '';
    runMs = null;
    lastDecisionId = '';
    if (!flow) return;
    running = true;
    const startedAt = performance.now();
    try {
      const entity = entityType && entityID ? { type: entityType, id: entityID } : undefined;
      const res = await decide(key, flow.slug, env, JSON.parse(dataText), entity, fetch, preview);
      runMs = Math.round(performance.now() - startedAt);
      runResult = res;
      lastDecisionId = res.decision_id ?? '';
      result = JSON.stringify(res, null, 2);
      // A preview records nothing, so there are no metrics to refresh and no recorded
      // decision to fetch a node trace from — the verdict card alone reflects the run.
      if (!preview) {
        void loadMetrics();
        void loadTelemetry(lastDecisionId);
      }
    } catch (e) {
      runError = msg(e);
      result = `Error: ${msg(e)}`;
    } finally {
      running = false;
    }
  }
  // After a test run, paint each node's last output onto its card from the
  // recorded decision's node trace (retries while the history projection catches up).
  async function loadTelemetry(decisionId: string) {
    if (!decisionId) return;
    // Capture the flow this telemetry belongs to: the retry loop spans ~1s, and
    // sibling navigation would otherwise paint THIS flow's node outputs onto the new
    // flow's canvas (stale async closure writing shared $state after navigation).
    const requested = flowId;
    const gen = telemetryGen;
    // The history projection lags the just-appended decision, so retry a few times
    // until its node trace is available (best-effort; the run result still shows).
    for (let attempt = 0; attempt < 5; attempt++) {
      if (flowId !== requested || telemetryGen !== gen) return; // navigated away or edited — abandon
      try {
        const d = await getDecision(key, decisionId);
        if (flowId !== requested || telemetryGen !== gen) return;
        const nodes = d.nodes ?? [];
        if (nodes.length > 0) {
          const t = new Map<string, string>();
          for (const n of nodes) t.set(n.node_id, telemetrySummary(n.output));
          nodeTelemetry = t;
          syncCanvas();
          return;
        }
      } catch {
        /* not available yet — retry */
      }
      await new Promise((r) => setTimeout(r, 200));
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
      // Revoke on a later tick — a synchronous revoke can race the browser's blob
      // fetch and abort the download.
      setTimeout(() => URL.revokeObjectURL(url), 0);
      toast.success('Downloaded run trace');
    } catch (e) {
      toast.error(msg(e));
    }
  }
  async function copyTrace() {
    try {
      await navigator.clipboard.writeText(await exportDecision(key, lastDecisionId));
      toast.success('Copied run trace to clipboard');
    } catch (e) {
      toast.error(msg(e));
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
      // Revoke on a later tick — a synchronous revoke can race the browser's blob
      // fetch and abort the download (esp. a larger BPMN/JSON export).
      setTimeout(() => URL.revokeObjectURL(url), 0);
      toast.success(`Downloaded ${exportFilename(format)}`);
    } catch (e) {
      toast.error(msg(e));
    }
  }
  async function copyExport(format: ExportFormat) {
    try {
      await navigator.clipboard.writeText(await exportFlow(key, flowId, format));
      toast.success(`Copied ${format} to clipboard`);
    } catch (e) {
      toast.error(msg(e));
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
      toast.error(msg(e));
    } finally {
      btRunning = false;
    }
  }

  // What-if: sweep one input field across values and see how the outcome shifts
  let wiBase = $state('{}');
  let wiField = $state('');
  let wiValues = $state('');
  let wiReport = $state<SweepReport | null>(null);
  let wiRunning = $state(false);
  async function runWhatif() {
    error = '';
    wiReport = null;
    if (!flow) return;
    wiRunning = true;
    try {
      const base = JSON.parse(wiBase) as Record<string, unknown>;
      // Values are comma-separated; numbers parse as numbers, everything else as a string.
      const values = wiValues
        .split(',')
        .map((v) => v.trim())
        .filter(Boolean)
        .map((v) => (Number.isNaN(Number(v)) ? v : Number(v)));
      if (!wiField.trim() || values.length === 0) {
        toast.error('Enter a field and at least one value');
        return;
      }
      wiReport = await whatif(key, flowId, { base, field: wiField.trim(), values });
      toast.success(`Swept ${values.length} values — ${wiReport.transitions} transition(s)`);
    } catch (e) {
      toast.error(msg(e));
    } finally {
      wiRunning = false;
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
      toast.error(msg(e));
    } finally {
      batchRunning = false;
    }
  }

  // Promote a batch into pre-approvals: grant a time-boxed pre-decision for
  // every row the bound policy approves, keyed by a field in each row. ---
  let paEntityType = $state('applicant');
  let paEntityKey = $state('applicant_id');
  let paDisposition = $state<Disposition>('approve');
  let paValidDays = $state(30);
  let paReport = $state<PreApproveBatchReport | null>(null);
  let paRunning = $state(false);
  async function runPreapproveBatch() {
    error = '';
    paReport = null;
    if (!flow) return;
    // A cleared number input binds as null in Svelte 5; reject it rather than
    // posting valid_days:null (or a non-positive window) to the API.
    if (!Number.isInteger(paValidDays) || paValidDays < 1) {
      toast.error('Valid days must be a whole number of at least 1.');
      return;
    }
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
      toast.error(msg(e));
    } finally {
      paRunning = false;
    }
  }

  let depVersion = $state('');
  let depEnv = $state<Environment>('sandbox');
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
      environment: Environment;
      version: number;
      challenger_version?: number;
      challenger_pct?: number;
    } = { environment: depEnv, version };
    const cv = parseInt(depChallenger, 10);
    const pct = parseInt(depChallengerPct, 10);
    if (!Number.isNaN(cv) && cv > 0) {
      body.challenger_version = cv;
      if (!Number.isNaN(pct) && pct > 0) {
        if (pct > 100) {
          toast.error('Challenger traffic % must be between 1 and 100.');
          return;
        }
        body.challenger_pct = pct;
      }
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
      toast.error(msg(e));
    } finally {
      deploying = false;
    }
  }

  // rollback reverts an environment to its previous live version (instant rollback).
  async function rollback(environment: string) {
    // Rollback changes which version serves live traffic — confirm first (like promote).
    if (!confirm(`Roll back ${environment} to its previous live version?`)) return;
    error = '';
    deploying = true;
    try {
      await rollbackDeploy(key, flowId, environment);
      toast.success(`Rolled back ${environment} to the previous version`);
      await load();
    } catch (e) {
      toast.error(msg(e));
    } finally {
      deploying = false;
    }
  }

  // Scheduled / time-boxed deploys.
  let schedules = $state<ScheduledDeploy[]>([]);
  let schEnv = $state('sandbox');
  let schVersion = $state('');
  let schAt = $state('');
  let schUntil = $state('');
  let schBusy = $state(false);
  async function loadSchedules() {
    const requested = flowId;
    try {
      const s = await listSchedules(key, flowId);
      if (flowId !== requested) return; // dropped: a newer flow loaded mid-request
      schedules = s;
    } catch (e) {
      // Surface a load failure (a toast) rather than showing an empty list that reads
      // as "none configured" — an operator could otherwise re-create existing schedules.
      if (flowId === requested) {
        schedules = [];
        toast.error(msg(e));
      }
    }
  }
  async function addSchedule() {
    error = '';
    schBusy = true;
    try {
      await scheduleDeploy(key, flowId, {
        environment: schEnv,
        version: parseInt(schVersion, 10) || flow?.latest || 1,
        at: new Date(schAt).toISOString(),
        until: schUntil ? new Date(schUntil).toISOString() : undefined
      });
      schAt = '';
      schUntil = '';
      toast.success('Deploy scheduled');
      await loadSchedules();
    } catch (e) {
      toast.error(msg(e));
    } finally {
      schBusy = false;
    }
  }
  async function dropSchedule(scheduleId: string) {
    if (!confirm('Cancel this scheduled deploy?')) return;
    try {
      await cancelSchedule(key, flowId, scheduleId);
      toast.success('Scheduled deploy cancelled');
      await loadSchedules();
    } catch (e) {
      toast.error(msg(e));
    }
  }

  // Per-flow access grants (admin).
  let grants = $state<FlowGrant[]>([]);
  let grantActor = $state('');
  let grantEnv = $state('*');
  let grantBusy = $state(false);
  async function loadGrants() {
    const requested = flowId;
    try {
      const g = await listGrants(key, flowId);
      if (flowId !== requested) return; // dropped: a newer flow loaded mid-request
      grants = g;
    } catch {
      if (flowId === requested) grants = []; // non-admins can't list — leave empty
    }
  }
  async function grant() {
    error = '';
    grantBusy = true;
    try {
      await addGrant(key, flowId, grantActor.trim(), grantEnv);
      grantActor = '';
      toast.success('Grant added');
      await loadGrants();
    } catch (e) {
      toast.error(msg(e));
    } finally {
      grantBusy = false;
    }
  }
  async function ungrant(grantId: string) {
    if (!confirm('Revoke this access grant?')) return;
    try {
      await revokeGrant(key, flowId, grantId);
      toast.success('Grant revoked');
      await loadGrants();
    } catch (e) {
      toast.error(msg(e));
    }
  }

  let promoteFrom = $state('sandbox');
  let promoteTo = $state('staging');
  let promoteForce = $state(false);
  let promoting = $state(false);
  let policySaving = $state(false);

  function promotionPolicyFor(environment: string): PromotionStagePolicy {
    const found = Object.entries(flow?.promotion_policy ?? {}).find(([e]) => e === environment);
    return (
      found?.[1] ?? {
        require_assertions: true,
        require_no_firing_monitors: true,
        allow_force: true,
        require_review: environment === 'production'
      }
    );
  }

  async function updatePromotionPolicy(environment: string, patch: Partial<PromotionStagePolicy>) {
    error = '';
    if (!flow) return;
    policySaving = true;
    try {
      const policy = Object.fromEntries(
        ENVIRONMENTS.map((e) => [
          e,
          {
            ...promotionPolicyFor(e),
            ...(e === environment ? patch : {}),
            ...(e === 'production' ? { require_review: true } : {})
          }
        ])
      ) as Record<string, PromotionStagePolicy>;
      const next = await setPromotionPolicy(key, flowId, policy);
      flow = { ...flow, promotion_policy: next };
      toast.success('Promotion policy saved');
    } catch (e) {
      toast.error(msg(e));
    } finally {
      policySaving = false;
    }
  }

  // --- Shadow deploys (evaluate a candidate version alongside live decisions) ---
  let shadow = $state<ShadowState>({ shadows: {}, report: {} });
  let shadowSaving = $state(false);
  async function loadShadow() {
    const requested = flowId;
    try {
      const s = await getShadow(key, flowId);
      if (flowId !== requested) return; // dropped: a newer flow loaded mid-request
      shadow = s;
    } catch {
      /* a viewer with no shadow data simply sees none */
    }
  }
  // Entries lookups (not computed indexing) to stay clear of detect-object-injection.
  function shadowVersionFor(environment: string): number {
    return Object.entries(shadow.shadows).find(([k]) => k === environment)?.[1] ?? 0;
  }
  function shadowReportFor(environment: string): EnvShadow | undefined {
    return Object.entries(shadow.report).find(([k]) => k === environment)?.[1];
  }
  async function updateShadow(environment: string, version: number) {
    error = '';
    shadowSaving = true;
    try {
      await setShadow(key, flowId, environment, version);
      toast.success(version ? `Shadowing v${version} in ${environment}` : `Shadow cleared`);
      await loadShadow();
    } catch (e) {
      toast.error(msg(e));
    } finally {
      shadowSaving = false;
    }
  }

  async function submitPromote() {
    error = '';
    if (!flow) return;
    // Promotion changes which version serves live traffic — confirm before doing it.
    const intoProd = promoteTo === 'production';
    const verb = intoProd ? 'open a four-eyes deploy request for' : 'promote the live version to';
    if (!confirm(`Really ${verb} ${promoteTo}? (from ${promoteFrom})`)) {
      return;
    }
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
      toast.error(msg(e));
    } finally {
      promoting = false;
    }
  }

  // All deployment requests (newest first) — pending and decided stay visible so
  // the approval/rejection explanation persists with the request.
  let allRequests = $derived([...(flow?.deployment_requests ?? [])].reverse());

  // The reason is collected inline in the request row (not a native prompt()):
  // the four-eyes decision is the governance showcase moment, so it reads like
  // product. `deciding` names the one request whose row is in reason-entry mode.
  let deciding = $state<{ reqId: string; verb: 'approve' | 'reject' } | null>(null);
  let decideReason = $state('');
  function startDecide(reqId: string, verb: 'approve' | 'reject') {
    deciding = { reqId, verb };
    decideReason = '';
  }
  async function confirmDecide() {
    if (!deciding) return;
    const { reqId, verb } = deciding;
    error = '';
    try {
      if (verb === 'approve') {
        await approveDeployment(key, flowId, reqId, decideReason.trim());
        toast.success('Deployment approved and live');
      } else {
        await rejectDeployment(key, flowId, reqId, decideReason.trim());
        toast.success('Deployment request rejected');
      }
      deciding = null;
      await load();
    } catch (e) {
      toast.error(msg(e));
    }
  }

  let monitors = $state<Monitor[]>([]);
  let monMetric = $state<MonitorMetric>('failure_rate');
  let monOp = $state<MonitorOp>('gt');
  let monThreshold = $state(0.05);
  let monDesc = $state('');
  let monBusy = $state(false);
  // Rates are fractions (0–1); volume and latency are absolute.
  const monIsRate = $derived(monMetric.endsWith('_rate'));
  async function loadMonitors() {
    const requested = flowId;
    try {
      const m = await listMonitors(key, flowId);
      if (flowId !== requested) return; // dropped: a newer flow loaded mid-request
      monitors = m;
    } catch (e) {
      // A failed load must not masquerade as "no monitors" (see loadAssertions).
      if (flowId === requested) {
        monitors = [];
        toast.error(msg(e));
      }
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
    // A cleared number input binds as null in Svelte 5; reject a missing/non-finite
    // threshold rather than posting null.
    if (typeof monThreshold !== 'number' || !Number.isFinite(monThreshold)) {
      toast.error('Monitor threshold must be a number.');
      return;
    }
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
      toast.error(msg(e));
    } finally {
      monBusy = false;
    }
  }
  async function removeMonitor(m: Monitor) {
    if (!confirm('Delete this monitor? It will stop evaluating and alerting.')) return;
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
      toast.error(msg(e));
    } finally {
      checking = false;
    }
  }

  // Notification webhooks (tenant-wide delivery targets)
  let webhooks = $state<Webhook[]>([]);
  let hookURL = $state('');
  let hookNote = $state('');
  let hookTemplate = $state('');
  let hookEvents = $state('');
  let hookBusy = $state(false);
  async function loadWebhooks() {
    try {
      webhooks = await listWebhooks(key);
    } catch (e) {
      // A failed load must not look like "no webhooks configured".
      webhooks = [];
      toast.error(msg(e));
    }
  }
  async function addWebhook() {
    error = '';
    hookBusy = true;
    try {
      await subscribeWebhook(key, hookURL.trim(), hookNote.trim(), {
        template: hookTemplate.trim(),
        events: hookEvents
          .split(',')
          .map((s) => s.trim())
          .filter(Boolean)
      });
      hookURL = '';
      hookNote = '';
      hookTemplate = '';
      hookEvents = '';
      toast.success('Webhook added');
      await loadWebhooks();
    } catch (e) {
      toast.error(msg(e));
    } finally {
      hookBusy = false;
    }
  }
  async function removeWebhook(wid: string) {
    if (!confirm('Remove this webhook? Notifications will stop being delivered to it.')) return;
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
    const requested = flowId;
    try {
      const d = await getDrift(key, flowId);
      if (flowId !== requested) return; // dropped: a newer flow loaded mid-request
      drift = d;
    } catch {
      if (flowId === requested) drift = null;
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
      toast.error(msg(e));
    }
  }
  function pct(n: number): string {
    return `${Math.round(n * 100)}%`;
  }

  const ASSERT_PLACEHOLDER =
    '[\n  {\n    "name": "example",\n    "input": {},\n    "expect": {}\n  }\n]';
  let assertText = $state(ASSERT_PLACEHOLDER);
  let assertReport = $state<AssertionReport | null>(null);
  let assertBusy = $state(false);
  async function loadAssertions() {
    const requested = flowId;
    try {
      const cases = await getAssertions(key, flowId);
      if (flowId !== requested) return; // dropped: a newer flow loaded mid-request
      // A flow with no cases resets to the placeholder — otherwise the previous
      // flow's assertions would stay editable (and saveable) onto this flow.
      assertText = cases.length ? JSON.stringify(cases, null, 2) : ASSERT_PLACEHOLDER;
    } catch (e) {
      if (flowId !== requested) return;
      // Reset the editor (the previous flow's cases must not be saveable here)
      // and surface the failure rather than quietly showing the placeholder.
      assertText = ASSERT_PLACEHOLDER;
      toast.error(msg(e));
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
      toast.error(msg(e));
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
      toast.error(msg(e));
    } finally {
      assertBusy = false;
    }
  }

  // All per-flow UI state that must not leak across sibling navigation: run
  // output, node overlays and one-shot reports all describe the previous flow.
  function resetFlowScopedState() {
    nodeTelemetry = new Map();
    heatOn = false;
    nodeHeat = new Map();
    heatTotal = 0;
    coverageReport = null;
    btReport = null;
    wiReport = null;
    batchReport = null;
    paReport = null;
    lastCheck = null;
    assertReport = null;
    // Synchronously, not just when loadAssertions settles — the previous flow's
    // cases must not be editable/saveable onto this flow even mid-fetch.
    assertText = ASSERT_PLACEHOLDER;
    result = '';
    runResult = null;
    runMs = null;
    lastDecisionId = '';
    deciding = null;
  }

  $effect(() => {
    void flowId; // reload every panel when the route flow changes (covers mount + sibling nav)
    resetFlowScopedState();
    void load();
    void loadMetrics();
    void loadMonitors();
    void loadWebhooks();
    void loadDrift();
    void loadAssertions();
    void loadShadow();
    void loadSchedules();
    void loadGrants();
  });
</script>

<svelte:window onkeydown={onCanvasKeydown} />

<main>
  {#if loading && !flow}
    <p><a href={appHref('/engine')}>← all flows</a></p>
    <Skeleton rows={6} />
  {:else if !flow}
    <p><a href={appHref('/engine')}>← all flows</a></p>
    <h1>{flowId}</h1>
    <p class="err">
      {error || "This flow couldn't be loaded — it may not exist or the link is stale."}
    </p>
    <p><button onclick={load}><Icon name="reload" size={15} /> Retry</button></p>
  {:else}
    <div class="flowhead">
      <a href={appHref('/engine')} class="backlink" title="All flows">←</a>
      <h1>{flow.name}</h1>
      {#if dirty}
        <span class="unsaved" title="You have edits that aren't published yet">● Unsaved edits</span
        >
      {/if}
      <div class="head-actions">
        <button
          class="iconbtn"
          onclick={analyzeFlow}
          disabled={analyzeBusy}
          title="Analyze this flow with AI"
          aria-label="Analyze this flow with AI"
          data-testid="analyze-flow"
        >
          <Icon name="robot" size={17} />
        </button>
        <button onclick={load} title="Re-fetch this flow and its versions"
          ><Icon name="reload" size={15} /> Reload</button
        >
        <button
          class="primary"
          onclick={publish}
          disabled={publishing || !roleAtLeast($user?.role, 'editor')}
          title={!roleAtLeast($user?.role, 'editor') ? 'Requires the editor role' : undefined}
          ><Icon name="check" size={15} /> {publishing ? 'Publishing…' : 'Publish version'}</button
        >
      </div>
    </div>
    {#if descEditing}
      <div class="flow-desc" data-testid="flow-description">
        <textarea
          bind:value={descDraft}
          rows="2"
          aria-label="flow description"
          placeholder="What this flow decides and for whom."
        ></textarea>
        <div class="desc-actions">
          <button class="primary" onclick={saveDescription} disabled={descSaving}
            >{descSaving ? 'Saving…' : 'Save description'}</button
          >
          <button onclick={() => (descEditing = false)} disabled={descSaving}>Cancel</button>
        </div>
        {#if descError}<p class="err">{descError}</p>{/if}
      </div>
    {:else if flow.description || roleAtLeast($user?.role, 'editor')}
      <p class="flow-desc muted" data-testid="flow-description">
        {flow.description || 'No description yet.'}
        {#if roleAtLeast($user?.role, 'editor')}
          <button
            class="pencil"
            onclick={startDescEdit}
            title="Edit description"
            aria-label="Edit description">✎</button
          >
        {/if}
      </p>
    {/if}
    {#if analyzeOpen}
      <section class="analyze" data-testid="analyze-panel">
        <div class="analyze-head">
          <b><Icon name="robot" size={15} /> AI analysis</b>
          <button class="linkbtn" onclick={() => (analyzeOpen = false)} aria-label="Close analysis"
            >Close</button
          >
        </div>
        {#if analyzeBusy}
          <p class="muted" aria-busy="true">Analyzing the flow…</p>
        {:else if analyzeError}
          <p class="err">{analyzeError}</p>
        {:else}
          <pre class="analyze-out" data-testid="analyze-output">{analyzeOut}</pre>
        {/if}
      </section>
    {/if}
    <details class="sharebar" data-testid="share-menu">
      <summary><Icon name="diagram" size={14} /> Export / Import</summary>
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
        <button
          onclick={() => downloadExport('mermaid-state')}
          title="Download Mermaid state diagram"
        >
          <Icon name="download" size={14} /> State
        </button>
        <div class="grp">
          <button onclick={() => downloadExport('bpmn')} title="Download BPMN 2.0 XML">
            <Icon name="download" size={14} /> BPMN XML
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
          <button
            class="icon"
            aria-label="Copy DOT"
            title="Copy DOT"
            onclick={() => copyExport('dot')}
          >
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
    </details>
    {#if metrics && metrics.total > 0}
      <div class="metrics">
        <span class="exportlabel"><Icon name="diagram" size={15} /> Analytics</span>
        <span><b>{metrics.total}</b> decision{metrics.total === 1 ? '' : 's'}</span>
        <span class="ok">{metrics.completed} completed</span>
        <span class="err">{metrics.failed} failed</span>
        <span class="muted">avg {metrics.avg_duration_ms} ms</span>
        {#if automation}
          <span class="ok" data-testid="automation-rate">{automation.rate}% automated</span>
          <span class="muted">{automation.refer} referred</span>
        {/if}
        {#each Object.entries(metrics.by_variant ?? {}) as [variant, v] (variant)}
          <span class="muted">{variant}: {v.completed}/{v.started}</span>
        {/each}
        <a href={appHref('/decisions')}>view runs →</a>
      </div>
    {/if}

    {#if tab === 'monitor'}
      <section class="monitors" data-testid="monitors-panel">
        <div class="mon-head">
          <h2>Monitors</h2>
          <div class="row">
            <button
              class="ghost"
              onclick={loadMonitors}
              title="Re-evaluate against current metrics"
            >
              <Icon name="reload" size={14} /> Refresh
            </button>
            <button
              class="ghost"
              onclick={captureBaselineNow}
              data-testid="capture-baseline"
              disabled={!roleAtLeast($user?.role, 'editor')}
              title={roleAtLeast($user?.role, 'editor')
                ? 'Snapshot the current disposition mix as the drift baseline'
                : 'Requires the editor role'}
            >
              <Icon name="scorecard" size={14} /> Capture baseline
            </button>
            <button
              class="ghost"
              onclick={checkNow}
              disabled={checking || !roleAtLeast($user?.role, 'editor')}
              data-testid="check-monitors"
              title={roleAtLeast($user?.role, 'editor')
                ? 'Evaluate and push firing monitors to webhooks'
                : 'Requires the editor role'}
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
          <button
            onclick={addMonitor}
            disabled={monBusy || !roleAtLeast($user?.role, 'editor')}
            title={!roleAtLeast($user?.role, 'editor') ? 'Requires the editor role' : undefined}
            data-testid="add-monitor">Add monitor</button
          >
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
                <button
                  class="link danger"
                  onclick={() => removeMonitor(m)}
                  disabled={!roleAtLeast($user?.role, 'editor')}
                  title={!roleAtLeast($user?.role, 'editor')
                    ? 'Requires the editor role'
                    : undefined}>remove</button
                >
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
              <span class="muted"
                >No baseline captured — use <b>Capture baseline</b> to set one.</span
              >
            {:else if !drift.has_current}
              <span class="muted">Baseline set; no dispositioned decisions yet.</span>
            {:else}
              <span class:err={drift.max_drift > 0.2} class:ok={drift.max_drift <= 0.2}
                >max drift {pct(drift.max_drift)}</span
              >
              <span
                class:err={drift.psi > 0.25}
                class:ok={drift.psi <= 0.1}
                title="population stability index">PSI {drift.psi.toFixed(3)}</span
              >
              <span class="muted" title="Kullback–Leibler divergence">KL {drift.kl.toFixed(3)}</span
              >
              {#each drift.buckets ?? [] as b (b.disposition)}
                <span class="muted"
                  >{b.disposition}: {pct(b.baseline)}→{pct(b.current)} ({b.delta >= 0
                    ? '+'
                    : ''}{pct(b.delta)})</span
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
                ><span role="img" aria-label={d.ok ? 'delivered' : 'failed'}
                  >{d.ok ? '✓' : '✗'}</span
                >
                {d.url}{d.status ? ` (${d.status})` : ''}</span
              >
            {/each}
          </div>
        {/if}

        <details class="webhooks">
          <summary
            >Notification webhooks <span class="muted"
              >(shared across flows · {webhooks.length})</span
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
            <label>
              Events
              <input
                bind:value={hookEvents}
                aria-label="webhook events"
                placeholder="monitor, drift (blank = all)"
              />
            </label>
            <button
              onclick={addWebhook}
              disabled={hookBusy || !hookURL.trim() || !roleAtLeast($user?.role, 'editor')}
              title={!roleAtLeast($user?.role, 'editor') ? 'Requires the editor role' : undefined}
              data-testid="add-webhook">Add webhook</button
            >
          </div>
          <label class="grow">
            Message template <span class="muted">(optional Go template, e.g. {`{{.flow_id}}`})</span
            >
            <input
              bind:value={hookTemplate}
              aria-label="webhook template"
              placeholder="leave blank to send the raw JSON payload"
            />
          </label>
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
                  <button
                    class="link danger"
                    onclick={() => removeWebhook(h.webhook_id)}
                    disabled={!roleAtLeast($user?.role, 'editor')}
                    title={!roleAtLeast($user?.role, 'editor')
                      ? 'Requires the editor role'
                      : undefined}>remove</button
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
    {/if}

    {#if tab === 'deploy'}
      <section class="deploy" data-testid="deploy-panel">
        <h2>Deployment</h2>
        <div class="live">
          <span class="exportlabel"><Icon name="check" size={15} /> Live</span>
          {#each ['sandbox', 'staging', 'production'] as e (e)}
            <span class="env">
              {e}:
              {#if liveVersion(e) !== undefined}<b>v{liveVersion(e)}</b>
                <button
                  class="linkbtn"
                  onclick={() => rollback(e)}
                  disabled={deploying || !roleAtLeast($user?.role, 'editor')}
                  title={!roleAtLeast($user?.role, 'editor')
                    ? 'Requires the editor role'
                    : 'Revert to the previous live version'}>rollback</button
                >{:else}<span class="muted">—</span>{/if}
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
            disabled={!flow || deploying || !roleAtLeast($user?.role, 'editor')}
            title={!roleAtLeast($user?.role, 'editor') ? 'Requires the editor role' : undefined}
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
            {#each ['sandbox', 'staging', 'production'] as e (e)}<option value={e}>{e}</option
              >{/each}
          </select>
          <span aria-hidden="true">→</span>
          <select bind:value={promoteTo} aria-label="promote to">
            {#each ['sandbox', 'staging', 'production'] as e (e)}<option value={e}>{e}</option
              >{/each}
          </select>
          <button
            class="primary"
            onclick={submitPromote}
            disabled={!flow || promoting || !roleAtLeast($user?.role, 'editor')}
            title={!roleAtLeast($user?.role, 'editor') ? 'Requires the editor role' : undefined}
            data-testid="promote-submit"
          >
            {promoting ? 'Working…' : promoteTo === 'production' ? 'Promote (review)' : 'Promote'}
          </button>
          <label class="force"
            ><input type="checkbox" bind:checked={promoteForce} aria-label="force promote" /> force</label
          >
          <span class="hint muted"
            >ships the live version up the chain; blocked if monitors are firing (prod via review).</span
          >
        </div>
        <details class="promotion-policy" data-testid="promotion-policy">
          <summary><Icon name="shield" size={15} /> Promotion policy</summary>
          <div class="policy-grid">
            {#each ENVIRONMENTS as e (e)}
              {@const p = promotionPolicyFor(e)}
              {@const noPolicy = !roleAtLeast($user?.role, 'editor')}
              {@const policyTitle = noPolicy ? 'Requires the editor role' : undefined}
              <div class="policy-stage">
                <b>{e}</b>
                <label title={policyTitle}>
                  <input
                    type="checkbox"
                    checked={p.require_no_firing_monitors}
                    disabled={policySaving || noPolicy}
                    onchange={(ev) =>
                      updatePromotionPolicy(e, {
                        require_no_firing_monitors: ev.currentTarget.checked
                      })}
                  />
                  no firing monitors
                </label>
                <label title={policyTitle}>
                  <input
                    type="checkbox"
                    checked={p.require_assertions}
                    disabled={policySaving || noPolicy}
                    onchange={(ev) =>
                      updatePromotionPolicy(e, { require_assertions: ev.currentTarget.checked })}
                  />
                  passing assertions
                </label>
                <label title={policyTitle}>
                  <input
                    type="checkbox"
                    checked={p.allow_force}
                    disabled={policySaving || noPolicy}
                    onchange={(ev) =>
                      updatePromotionPolicy(e, { allow_force: ev.currentTarget.checked })}
                  />
                  force override
                </label>
                <label title={policyTitle}>
                  <input
                    type="checkbox"
                    checked={p.require_review}
                    disabled={policySaving || e === 'production' || noPolicy}
                    onchange={(ev) =>
                      updatePromotionPolicy(e, { require_review: ev.currentTarget.checked })}
                  />
                  review request
                </label>
              </div>
            {/each}
          </div>
        </details>

        <details class="shadow-panel" data-testid="shadow-panel">
          <summary><Icon name="diagram" size={15} /> Shadow deploys</summary>
          <p class="hint muted">
            Run a candidate version alongside live decisions to measure how often it would diverge —
            its result is never returned to callers.
          </p>
          <div class="shadow-grid">
            {#each ENVIRONMENTS as e (e)}
              {@const rep = shadowReportFor(e)}
              <div class="shadow-stage">
                <b>{e}</b>
                <select
                  value={shadowVersionFor(e)}
                  disabled={shadowSaving || !roleAtLeast($user?.role, 'editor')}
                  title={!roleAtLeast($user?.role, 'editor')
                    ? 'Requires the editor role'
                    : undefined}
                  onchange={(ev) => updateShadow(e, parseInt(ev.currentTarget.value, 10))}
                  aria-label={`shadow version for ${e}`}
                >
                  <option value={0}>none</option>
                  {#each flow?.versions ?? [] as v (v.version)}
                    <option value={v.version}>v{v.version}</option>
                  {/each}
                </select>
                {#if rep && rep.total > 0}
                  <span class="shadow-stats muted">
                    {rep.matched}/{rep.total} match{rep.diverged
                      ? `, ${rep.diverged} diverged`
                      : ''}{rep.errored ? `, ${rep.errored} errored` : ''}
                  </span>
                {:else}
                  <span class="muted">no comparisons yet</span>
                {/if}
              </div>
            {/each}
          </div>
        </details>

        {#if allRequests.length > 0}
          <div class="requests" data-testid="deployment-requests">
            <h3>Deployment requests</h3>
            <div class="table-wrap">
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
                        {#if r.status === 'pending' && deciding?.reqId === r.request_id}
                          <input
                            bind:value={decideReason}
                            aria-label="decision reason"
                            placeholder={deciding.verb === 'approve'
                              ? 'approval note (recorded with this decision)'
                              : 'reason for rejecting'}
                          />
                          <button class="primary" onclick={confirmDecide}
                            >Confirm {deciding.verb}</button
                          >
                          <button onclick={() => (deciding = null)}>Cancel</button>
                        {:else if r.status === 'pending'}
                          <button
                            class="primary"
                            onclick={() => startDecide(r.request_id, 'approve')}
                            disabled={!roleAtLeast($user?.role, 'approver') ||
                              r.requested_by === $user?.actor}
                            title={!roleAtLeast($user?.role, 'approver')
                              ? 'Requires the approver role'
                              : r.requested_by === $user?.actor
                                ? 'Four-eyes: you requested this — a different approver must approve'
                                : undefined}>Approve</button
                          >
                          <button
                            onclick={() => startDecide(r.request_id, 'reject')}
                            disabled={!roleAtLeast($user?.role, 'approver')}
                            title={!roleAtLeast($user?.role, 'approver')
                              ? 'Requires the approver role'
                              : undefined}>Reject</button
                          >
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
          </div>
        {/if}

        <details class="schedules" data-testid="schedules-panel">
          <summary>Scheduled deploys <span class="muted">({schedules.length})</span></summary>
          <div class="row mon-form">
            <label
              >Env
              <select bind:value={schEnv}>
                <option value="sandbox">sandbox</option>
                <option value="staging">staging</option>
                <option value="production">production</option>
              </select></label
            >
            <label>Version <input bind:value={schVersion} placeholder="latest" /></label>
            <label>At <input type="datetime-local" bind:value={schAt} /></label>
            <label
              >Until <input
                type="datetime-local"
                bind:value={schUntil}
                aria-label="until (optional, time-boxed)"
              /></label
            >
            <button
              onclick={addSchedule}
              disabled={schBusy || !schAt || !roleAtLeast($user?.role, 'editor')}
              title={!roleAtLeast($user?.role, 'editor') ? 'Requires the editor role' : undefined}
              >Schedule</button
            >
          </div>
          {#if schedules.length > 0}
            <ul class="mon-list">
              {#each schedules as sc (sc.schedule_id)}
                <li>
                  <span>{sc.environment} v{sc.version} @ {new Date(sc.at).toLocaleString()}</span>
                  {#if sc.until}<span class="muted">→ {new Date(sc.until).toLocaleString()}</span
                    >{/if}
                  <span class="reqstatus {sc.status}">{sc.status}</span>
                  {#if sc.status === 'pending' || sc.status === 'active'}
                    <button
                      class="linkbtn"
                      onclick={() => dropSchedule(sc.schedule_id)}
                      disabled={!roleAtLeast($user?.role, 'editor')}
                      title={!roleAtLeast($user?.role, 'editor')
                        ? 'Requires the editor role'
                        : undefined}>cancel</button
                    >
                  {/if}
                </li>
              {/each}
            </ul>
          {/if}
        </details>

        <details class="grants" data-testid="grants-panel">
          <summary>Access grants <span class="muted">(admin · {grants.length})</span></summary>
          <p class="hint">
            With no grants, change-control follows the global roles. Add a grant to restrict who may
            deploy / roll back / schedule / promote this flow (per environment, or <code>*</code> for
            all).
          </p>
          <div class="row mon-form">
            <label class="grow"
              >Actor <input bind:value={grantActor} placeholder="user id / email" /></label
            >
            <label
              >Env
              <select bind:value={grantEnv}>
                <option value="*">all (*)</option>
                <option value="sandbox">sandbox</option>
                <option value="staging">staging</option>
                <option value="production">production</option>
              </select></label
            >
            <button
              onclick={grant}
              disabled={grantBusy || !grantActor.trim() || !roleAtLeast($user?.role, 'admin')}
              title={!roleAtLeast($user?.role, 'admin') ? 'Requires the admin role' : undefined}
              >Grant</button
            >
          </div>
          {#if grants.length > 0}
            <ul class="mon-list">
              {#each grants as g (g.grant_id)}
                <li>
                  <span><b>{g.actor}</b> — {g.environment}</span>
                  <button
                    class="linkbtn"
                    onclick={() => ungrant(g.grant_id)}
                    disabled={!roleAtLeast($user?.role, 'admin')}
                    title={!roleAtLeast($user?.role, 'admin')
                      ? 'Requires the admin role'
                      : undefined}>revoke</button
                  >
                </li>
              {/each}
            </ul>
          {/if}
        </details>
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
    {/if}

    {#if error}<p class="err">{error}</p>{/if}

    <div class="grid">
      <!-- svelte-ignore a11y_no_static_element_interactions -->
      <div
        class="canvas"
        class:focus={canvasMode === 'focus'}
        class:collapsed={canvasMode === 'collapsed'}
        data-testid="flow-canvas"
        aria-label="design window"
        ondragover={(e) => e.preventDefault()}
        ondrop={onCanvasDrop}
      >
        {#if canvasMode === 'collapsed'}
          <button
            class="canvas-expand"
            onclick={() => setCanvasMode('board')}
            data-testid="canvas-mode-board"
            title="Show the design window"
          >
            <Icon name="diagram" size={15} /> Design window — {editNodes.length} node{editNodes.length ===
            1
              ? ''
              : 's'}, {editEdges.length} edge{editEdges.length === 1 ? '' : 's'} · click to expand
          </button>
        {:else}
          <div class="canvas-tools">
            <div class="view-toggle" aria-label="canvas view">
              <button
                class:active={canvasView === 'cards'}
                aria-pressed={canvasView === 'cards'}
                onclick={() => setCanvasView('cards')}
                title="Show detailed node cards"
                data-testid="canvas-view-cards"
              >
                <Icon name="clipboard" size={14} /> Cards
              </button>
              <button
                class:active={canvasView === 'bpmn'}
                aria-pressed={canvasView === 'bpmn'}
                onclick={() => setCanvasView('bpmn')}
                title="Render the canvas in BPMN process notation"
                aria-label="Process view (BPMN notation)"
                data-testid="canvas-view-bpmn"
              >
                <Icon name="diagram" size={14} /> Process
              </button>
            </div>
            <div class="view-toggle" role="group" aria-label="pointer tool">
              <button
                class:active={tool === 'select'}
                aria-pressed={tool === 'select'}
                onclick={() => (tool = 'select')}
                title="Select (V) — drag to marquee-select nodes and edges; middle/right-drag pans"
                data-testid="tool-select"
              >
                <Icon name="cursor" size={14} /> Select
              </button>
              <button
                class:active={tool === 'pan'}
                aria-pressed={tool === 'pan'}
                onclick={() => (tool = 'pan')}
                title="Pan (H) — drag anywhere to move the board"
                data-testid="tool-pan"
              >
                <Icon name="hand" size={14} /> Pan
              </button>
            </div>
            <button
              class="relax-btn"
              onclick={relax}
              title="Auto-arrange every node by flow order (the only thing that moves nodes you've placed)"
              data-testid="relax-layout"
            >
              <Icon name="diagram" size={14} /> Auto-layout
            </button>
            <button
              class="relax-btn"
              onclick={togglePanel}
              aria-pressed={panelOpen}
              title={panelOpen ? 'Hide the tools panel for a full canvas' : 'Show the tools panel'}
              data-testid="toggle-panel"
            >
              <Icon name={panelOpen ? 'chevron-right' : 'plus'} size={14} />
              {panelOpen ? 'Hide panel' : 'Tools'}
            </button>
            <button
              class="relax-btn"
              class:active={heatOn}
              aria-pressed={heatOn}
              onclick={toggleHeat}
              disabled={heatBusy}
              title="Tint each node by how often it's traversed across this flow's recorded decisions"
              data-testid="heatmap-toggle"
            >
              <Icon name="gauge" size={14} />
              {heatBusy ? 'Loading…' : 'Heatmap'}
            </button>
            <button
              class="relax-btn"
              onclick={replayLatest}
              disabled={replaying}
              title="Animate a recent decision's path through the canvas, node by node"
              data-testid="replay-decision"
            >
              <Icon name="play" size={14} />
              {replaying ? 'Replaying…' : 'Replay'}
            </button>
            {#if heatOn}<span class="heat-legend" data-testid="heat-legend"
                >{heatTotal} decision{heatTotal === 1 ? '' : 's'} · count = traversals</span
              >{/if}
            <div class="view-toggle mode-toggle" aria-label="design window size">
              {#if canvasMode === 'focus'}
                <button
                  onclick={() => setCanvasMode('board')}
                  data-testid="canvas-mode-board"
                  title="Exit focus (Esc)"
                >
                  <Icon name="chevron-down" size={14} /> Exit focus
                </button>
              {:else}
                <button
                  onclick={() => setCanvasMode('focus')}
                  data-testid="canvas-mode-focus"
                  title="Take over the whole viewport for design work (Esc exits)"
                >
                  <Icon name="diagram" size={14} /> Focus
                </button>
                <button
                  onclick={() => setCanvasMode('collapsed')}
                  data-testid="canvas-mode-collapsed"
                  title="Collapse the design window to configure the workflow panels below"
                >
                  <Icon name="chevron-down" size={14} /> Collapse
                </button>
              {/if}
            </div>
          </div>
          <div class="node-rail" role="toolbar" aria-label="add node" data-testid="node-rail">
            {#each NODE_TYPES as t (t)}
              <button
                title={`Insert ${nodeTypeLabel(t)} node — click, or drag onto the board`}
                aria-label={`insert ${t} node`}
                draggable="true"
                ondragstart={(e) => onRailDragStart(e, t)}
                onclick={() => addNodeOfType(t)}
              >
                <Icon name={t} size={17} />
              </button>
            {/each}
          </div>
          <SvelteFlow
            bind:nodes
            bind:edges
            {nodeTypes}
            onconnect={onConnect}
            ondelete={onCanvasDelete}
            onnodeclick={({ node }) => selectNode(node.id)}
            onedgeclick={({ edge }) => selectEdge(Number(edge.id.slice(1)))}
            onpaneclick={() => selectNode(null)}
            panOnDrag={tool === 'pan' ? true : [1, 2]}
            selectionOnDrag={tool === 'select'}
            selectionMode={SelectionMode.Partial}
            colorMode={$theme}
            proOptions={{ hideAttribution: true }}
            fitView
            fitViewOptions={{ padding: 0.15, maxZoom: 1 }}
          >
            <Background />
            <Controls position="bottom-right" />
            <MiniMap position="bottom-right" zoomable pannable ariaLabel="board overview map" />
            <FlowBridge register={(api) => (flowApi = api)} />
          </SvelteFlow>
          {#if editNodes.length === 0}
            <div class="board-empty" data-testid="board-empty">
              <p>
                <b>Blank board.</b> Click a type on the left rail — or drag one here — to place your first
                node, then wire nodes by dragging between their handles.
              </p>
              <p class="muted">Start from <b>input</b>; every flow ends at an <b>output</b>.</p>
            </div>
          {/if}
          {#if panelOpen}
            <aside class="tools" aria-label="canvas tools">
              <h2>Add node</h2>
              <div class="row">
                <select bind:value={newType} aria-label="new node type">
                  {#each NODE_TYPES as t (t)}<option value={t}>{nodeTypeLabel(t)}</option>{/each}
                </select>
                <button onclick={addNode}>Add</button>
              </div>
              {#if editNodes.length <= 1}
                <p class="blank-hint">
                  New flow: pick a node (e.g. a <b>Scorecard</b> or <b>Decision table</b>) and
                  <b>Add</b>
                  it, then connect it from <code>input</code> on the canvas. <b>Auto-layout</b> arranges
                  them.
                </p>
              {/if}

              <h2>Nodes</h2>
              <ul class="nodes">
                {#each editNodes as n (n.id)}
                  <li class:sel={n.id === selectedId}>
                    <button class="link" onclick={() => (selectedId = n.id)}>
                      <span class="nodeicon" title={n.type}><Icon name={n.type} size={15} /></span>
                      <span>{n.name || n.id}</span>
                      <span class="nodetype">{n.type}</span>
                    </button>
                    <button
                      class="x"
                      aria-label={`delete ${n.id}`}
                      onclick={() => deleteNode(n.id)}
                    >
                      <Icon name="trash" size={14} />
                    </button>
                  </li>
                {/each}
              </ul>

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
                <input
                  bind:value={edgeBranch}
                  placeholder="branch"
                  aria-label="edge branch"
                  size="6"
                />
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
          {/if}

          {#if selectedEdge && selectedEdgeIdx !== null}
            <aside class="inspector" data-testid="edge-inspector" aria-label="edge inspector">
              <div class="insp-head">
                <span class="nodeicon"><Icon name="split" size={16} /></span>
                <b class="insp-title">{selectedEdge.from} → {selectedEdge.to}</b>
                <span class="nodetype">edge</span>
                <button
                  class="x"
                  aria-label="close inspector"
                  onclick={() => (selectedEdgeIdx = null)}
                  title="Close (the edge stays)">✕</button
                >
              </div>
              <label
                >branch <input
                  value={selectedEdge.branch ?? ''}
                  oninput={(e) => updateSelectedEdgeBranch(e.currentTarget.value)}
                  placeholder="e.g. yes / no — empty for an unconditional edge"
                  aria-label="edge branch label"
                /></label
              >
              <p class="muted">
                A split takes the edge whose branch matches its recorded choice; other node types
                follow their single unlabelled edge.
              </p>
              <div class="row insp-foot">
                <button
                  class="insp-delete"
                  onclick={() => selectedEdgeIdx !== null && deleteEdge(selectedEdgeIdx)}
                  title="Remove this edge from the draft"
                >
                  <Icon name="trash" size={14} /> Delete edge</button
                >
              </div>
            </aside>
          {/if}

          {#if selected}
            <aside class="inspector" data-testid="node-inspector" aria-label="node inspector">
              <div class="insp-head">
                <span class="nodeicon" title={selected.type}
                  ><Icon name={selected.type} size={16} /></span
                >
                <b class="insp-title">{selected.name || selected.id}</b>
                <span class="nodetype">{selected.type}</span>
                <button
                  class="x"
                  aria-label="close inspector"
                  onclick={() => (selectedId = null)}
                  title="Close (the node stays)">✕</button
                >
              </div>
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
                  onchange={(e) => updateSelected({ type: e.currentTarget.value as NodeType })}
                  aria-label="selected node type"
                >
                  {#each NODE_TYPES as t (t)}<option value={t}>{nodeTypeLabel(t)}</option>{/each}
                </select>
              </label>
              <label
                >lane <input
                  value={selected.lane ?? ''}
                  oninput={(e) => updateSelected({ lane: e.currentTarget.value || undefined })}
                  placeholder="Main"
                  aria-label="node lane"
                  list="lane-suggestions"
                /></label
              >
              <datalist id="lane-suggestions">
                {#each laneNames as l (l)}<option value={l}></option>{/each}
              </datalist>
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
              {:else if selected.type === 'predict'}
                <label
                  >model <input
                    value={asText(nodeCfg().model)}
                    oninput={(e) => patchCfg({ model: e.currentTarget.value })}
                    aria-label="predict model"
                    placeholder="risk"
                  /></label
                >
                <label
                  >output key <input
                    value={asText(nodeCfg().output)}
                    oninput={(e) => patchCfg({ output: e.currentTarget.value })}
                    aria-label="predict output"
                    placeholder="risk"
                  /></label
                >
                <p class="muted">
                  Reads <code>predict.&lt;output&gt;.probability</code> / <code>.score</code> downstream.
                </p>
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
                <label class="check"
                  ><input
                    type="checkbox"
                    checked={Boolean(nodeCfg().suspend)}
                    onchange={(e) => patchCfg({ suspend: e.currentTarget.checked })}
                    aria-label="suspend as a durable human task"
                  /> Suspend — pause the decision here until a reviewer acts (a durable human task; it
                  resumes from the decision's Resume control)</label
                >
                {#if nodeCfg().suspend}
                  <label
                    >output_key (where the reviewer's outcome is injected on resume) <input
                      value={asText(nodeCfg().output_key)}
                      oninput={(e) => patchCfg({ output_key: e.currentTarget.value })}
                      placeholder="review"
                      aria-label="output_key"
                    /></label
                  >
                {/if}
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
                      <button
                        class="x"
                        aria-label={`remove rule ${i}`}
                        onclick={() => removeRule(i)}>✕</button
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
                    <button
                      class="x"
                      aria-label={`remove factor ${i}`}
                      onclick={() => removeFactor(i)}>✕</button
                    >
                  </div>
                {/each}
                <button onclick={addFactor}>Add factor</button>
                <label
                  >band output key <input
                    value={asText(nodeCfg().band)}
                    oninput={(e) => patchCfg({ band: e.currentTarget.value })}
                    aria-label="scorecard band output"
                    placeholder="band"
                  /></label
                >
                <p class="muted">bands (score ≥ min → grade + reason codes)</p>
                {#each bands() as b, i (i)}
                  <div class="band">
                    <div class="row">
                      <input
                        type="number"
                        step="any"
                        value={asNum(b.min)}
                        oninput={(e) =>
                          setBand(i, {
                            min: e.currentTarget.value === '' ? 0 : Number(e.currentTarget.value)
                          })}
                        aria-label={`band ${i} min`}
                        placeholder="min"
                        size="6"
                      />
                      <input
                        value={asText(b.label)}
                        oninput={(e) => setBand(i, { label: e.currentTarget.value })}
                        aria-label={`band ${i} label`}
                        placeholder="grade"
                      />
                      <button
                        class="x"
                        aria-label={`remove band ${i}`}
                        onclick={() => removeBand(i)}>✕</button
                      >
                    </div>
                    {#each bandCodes(i) as c, k (k)}
                      <div class="row bandcode">
                        <input
                          value={asText(c.code)}
                          oninput={(e) => setBandCode(i, k, { code: e.currentTarget.value })}
                          aria-label={`band ${i} code ${k}`}
                          placeholder="code"
                          size="8"
                        />
                        <input
                          value={asText(c.description)}
                          oninput={(e) => setBandCode(i, k, { description: e.currentTarget.value })}
                          aria-label={`band ${i} code ${k} description`}
                          placeholder="description"
                        />
                        <button
                          class="x"
                          aria-label={`remove band ${i} code ${k}`}
                          onclick={() => removeBandCode(i, k)}>✕</button
                        >
                      </div>
                    {/each}
                    <button onclick={() => addBandCode(i)}>Add reason code</button>
                  </div>
                {/each}
                <button onclick={addBand}>Add band</button>
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
                    <button
                      class="x"
                      aria-label={`remove reason ${i}`}
                      onclick={() => removeReason(i)}>✕</button
                    >
                  </div>
                {/each}
                <button onclick={addReason}>Add reason</button>
              {:else if selected.type === 'decision_table'}
                <label
                  >hit policy
                  <select
                    value={asText(nodeCfg().hit) || 'first'}
                    onchange={(e) => patchCfg({ hit: e.currentTarget.value, mode: undefined })}
                    aria-label="decision table hit policy"
                  >
                    <option value="first">first match</option>
                    <option value="unique">unique (one match, else conflict)</option>
                    <option value="any">any (matches must agree)</option>
                    <option value="rule_order">rule order (collect, ordered)</option>
                    <option value="collect">collect (aggregate)</option>
                  </select>
                </label>
                {#if (asText(nodeCfg().hit) || 'first') === 'collect'}
                  <label
                    >aggregate
                    <select
                      value={asText(nodeCfg().aggregate) || ''}
                      onchange={(e) => patchCfg({ aggregate: e.currentTarget.value })}
                      aria-label="decision table aggregate"
                    >
                      <option value="">list (all values)</option>
                      <option value="sum">sum</option>
                      <option value="min">min</option>
                      <option value="max">max</option>
                      <option value="count">count</option>
                    </select>
                  </label>
                {/if}
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
                      <button
                        class="x"
                        aria-label={`remove row ${i}`}
                        onclick={() => removeTableRow(i)}>✕</button
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
              <div class="row insp-foot">
                <button
                  onclick={duplicateSelected}
                  title="Clone this node (config and lane) next to it"
                >
                  <Icon name="copy" size={14} /> Duplicate
                </button>
                <button
                  class="insp-delete"
                  onclick={() => selectedId && deleteNode(selectedId)}
                  title="Remove this node and its edges from the draft"
                >
                  <Icon name="trash" size={14} /> Delete node</button
                >
              </div>
            </aside>
          {/if}
        {/if}
      </div>
    </div>

    <nav class="tabbar" aria-label="builder panels" bind:this={tabbarEl}>
      {#each TABS as t (t.id)}
        <button
          class:active={tab === t.id}
          aria-pressed={tab === t.id}
          onclick={() => selectTab(t.id)}
          title={t.hint}
          data-testid={`tab-${t.id}`}>{t.label}</button
        >
      {/each}
    </nav>

    {#if tab === 'test'}
      <section data-testid="coverage-panel">
        <h2>
          Coverage / red-team
          <Hint label="Coverage"
            >Fuzzes hundreds of synthetic inputs through the published graph and reports which nodes
            and branches were exercised — surfacing branches the fuzz never reached and the
            disposition spread. A red-team for your policy.</Hint
          >
        </h2>
        <button onclick={runCoverage} disabled={coverageBusy} data-testid="run-coverage">
          {coverageBusy ? 'Fuzzing…' : 'Run coverage'}
        </button>
        {#if coverageReport}
          <div class="coverage" data-testid="coverage-report">
            <p class="muted">
              {coverageReport.runs} synthetic runs over {coverageReport.fields.length} input field{coverageReport
                .fields.length === 1
                ? ''
                : 's'}{coverageReport.fields.length
                ? ` (${coverageReport.fields.join(', ')})`
                : ''}.
            </p>
            {#if coverageReport.dispositions.approve + coverageReport.dispositions.refer + coverageReport.dispositions.decline > 0}
              <div class="cov-dispo">
                <Badge tone={dispositionTone('approve')}
                  >{coverageReport.dispositions.approve} approve</Badge
                >
                <Badge tone={dispositionTone('refer')}
                  >{coverageReport.dispositions.refer} refer</Badge
                >
                <Badge tone={dispositionTone('decline')}
                  >{coverageReport.dispositions.decline} decline</Badge
                >
              </div>
            {:else}
              <p class="muted">
                No policy is bound to this flow, so the fuzzed inputs produce no disposition spread
                — the node/branch coverage below still applies.
              </p>
            {/if}
            {#if coverageReport.dead_nodes.length || coverageReport.dead_branches.length}
              <div class="cov-dead">
                {#if coverageReport.dead_nodes.length}
                  <p>
                    <b>Not reached</b> in {coverageReport.runs} runs: {coverageReport.dead_nodes.join(
                      ', '
                    )}
                  </p>
                {/if}
                {#if coverageReport.dead_branches.length}
                  <p>
                    <b>Uncovered branches</b> — not taken in {coverageReport.runs} runs (a branch behind
                    a model score or a narrow input band may need targeted inputs to reach):
                  </p>
                  <ul>
                    {#each coverageReport.dead_branches as b (b.from + b.to + b.branch)}
                      <li><code>{b.from} → {b.to}</code> when <code>{b.branch}</code></li>
                    {/each}
                  </ul>
                {/if}
              </div>
            {:else}
              <p class="cov-clean">
                {coverageReport.branches.length
                  ? 'Full coverage — every node and branch was exercised.'
                  : 'Every node was exercised — this flow has no branches to cover.'}
              </p>
            {/if}
          </div>
        {/if}
      </section>

      <section>
        <h2>
          Test run
          <Hint label="Test run"
            >Executes this flow's graph against your input on the same engine production uses —
            walking nodes, taking the matching branch at each split, and applying the policy to
            produce a disposition. The result links to the recorded trace (or tick Preview to record
            nothing).</Hint
          >
        </h2>
        <div class="row">
          <select bind:value={env} aria-label="environment">
            {#each ENVIRONMENTS as e (e)}<option value={e}>{e}</option>{/each}
          </select>
          <button onclick={run} disabled={!flow || running || !dataValid}
            >{running ? 'Running…' : 'Run'}</button
          >
          <button
            type="button"
            class="link"
            onclick={sampleFromSchema}
            disabled={!hasInputSchema}
            title={hasInputSchema
              ? 'Prefill the input from the published input schema'
              : 'This version has no input schema — publish one to generate a sample'}
            >Sample input</button
          >
          <label class="preview-toggle" title="Run the flow without recording a decision">
            <input type="checkbox" bind:checked={preview} aria-label="preview (don't record)" />
            Preview (don't record)
          </label>
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
        <textarea bind:value={dataText} aria-label="input data" rows="3" class:invalid={!dataValid}
        ></textarea>
        {#if !dataValid}<p class="json-err">Not valid JSON — fix it before running.</p>{/if}
        {#if runError}<p class="err" data-testid="run-error">{runError}</p>{/if}
        {#if runResult}
          <div
            class="verdict-card {dispositionTone(runResult.disposition)}"
            data-testid="run-verdict"
          >
            <div class="verdict-top">
              {#if runResult.disposition}
                <Badge tone={dispositionTone(runResult.disposition)}>{runResult.disposition}</Badge>
              {/if}
              <Badge tone={statusTone(runResult.status)}>{runResult.status}</Badge>
              {#if runResult.preapproval_id}
                <Badge tone="ok">pre-approved</Badge>
                <span
                  class="dur"
                  title="Served instantly from grant {runResult.preapproval_id} — the flow did not run"
                  >honored · flow skipped</span
                >
              {/if}
              {#each runReasonCodes as rc (rc.code)}
                <span class="rcode" title={rc.description}>{rc.code}</span>
              {/each}
              {#if runMs != null}<span class="dur">{runMs} ms</span>{/if}
              {#if !lastDecisionId}
                <span class="dur" title="Preview run — no decision was recorded"
                  >preview · not recorded</span
                >
              {/if}
            </div>
            {#if runResult.error}
              <p class="verdict-err">{runResult.error}</p>
            {:else if runOutputFields.length}
              <dl class="verdict-out">
                {#each runOutputFields.slice(0, 6) as [k, v] (k)}
                  <dt>{k}</dt>
                  <dd>{fieldText(v)}</dd>
                {/each}
              </dl>
            {/if}
          </div>
        {/if}
        {#if result}
          <details class="raw-result">
            <summary>Raw result</summary>
            <pre data-testid="run-result">{result}</pre>
          </details>
        {:else}
          <pre data-testid="run-result" hidden>{result}</pre>
        {/if}
        {#if lastDecisionId}
          <div class="row">
            <a class="view-decision" href={appHref(`/decisions/${lastDecisionId}`)}
              >View the recorded decision →</a
            >
          </div>
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
        <details class="api-call">
          <summary>Call this via the API</summary>
          <p class="muted">
            The same decision over HTTP — issue a key on the API keys page and swap in your host.
          </p>
          <CodeSnippet code={apiSnippet} label="curl command" />
        </details>
      </section>

      <section>
        <h2>Backtest</h2>
        <p class="muted">
          Replay a dataset of inputs through the published flow — nothing is recorded. Leave the
          compare version blank to check the latest version, or set it to diff two versions before
          deploying.
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
          <button
            type="button"
            class="link"
            onclick={() => (btDataset = sampleDatasetJson())}
            disabled={!hasInputSchema}
            title={hasInputSchema
              ? 'Prefill a varied 8-row dataset from the input schema'
              : 'This version has no input schema — publish one to generate a sample'}
            data-testid="sample-backtest">Sample dataset</button
          >
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
            <div class="table-wrap">
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
            </div>
          {/if}
        {/if}
      </section>

      <section>
        <h2>What-if</h2>
        <p class="muted">
          Sweep one input field across a range and see how the decision shifts — nothing is
          recorded. A transition flags where the outcome changes (e.g. where an approve flips to a
          decline).
        </p>
        <div class="row">
          <input
            bind:value={wiField}
            placeholder="field (e.g. score)"
            aria-label="whatif field"
            size="16"
          />
          <input
            bind:value={wiValues}
            placeholder="values, comma-separated (e.g. 600, 650, 700)"
            aria-label="whatif values"
            size="30"
          />
          <button onclick={runWhatif} disabled={!flow || wiRunning} data-testid="run-whatif">
            {wiRunning ? 'Running…' : 'Run what-if'}
          </button>
          <button
            type="button"
            class="link"
            onclick={sampleWhatIf}
            disabled={!firstNumericField}
            title={firstNumericField
              ? 'Prefill a sweep over the schema’s first numeric field'
              : 'Needs a numeric field in the published input schema'}
            data-testid="sample-whatif">Sample sweep</button
          >
        </div>
        <textarea
          bind:value={wiBase}
          aria-label="whatif base input"
          rows="2"
          placeholder={'{ "other_field": 1 }'}
        ></textarea>
        {#if wiReport}
          <div class="metrics" data-testid="whatif-summary">
            <span>{wiReport.points.length} values</span>
            <span class="changed">{wiReport.transitions} transition(s)</span>
          </div>
          <div class="table-wrap">
            <table class="bt-table" data-testid="whatif-table">
              <thead>
                <tr><th>{wiReport.field}</th><th>Outcome</th></tr>
              </thead>
              <tbody>
                {#each wiReport.points as pt, i (i)}
                  <tr class:changed-row={pt.changed}>
                    <td>{JSON.stringify(pt.value)}</td>
                    <td>{pt.error || JSON.stringify(pt.output)}</td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        {/if}
      </section>

      <section>
        <h2>Assertions</h2>
        <p class="muted">
          Stored input→expected tests, run through the pure engine (no recorded decision). A case
          passes when every field in <code>expect</code> equals the flow's output. Failing
          assertions block a
          <b>promote</b> (override with force).
        </p>
        <div class="row">
          <button
            onclick={saveAssertions}
            disabled={!flow || assertBusy || !roleAtLeast($user?.role, 'editor')}
            title={!roleAtLeast($user?.role, 'editor') ? 'Requires the editor role' : undefined}
            data-testid="save-assertions">Save tests</button
          >
          <button
            onclick={runAssertionsNow}
            disabled={!flow || assertBusy}
            data-testid="run-assertions">Run tests</button
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
          <div class="table-wrap">
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
          </div>
        {/if}
      </section>

      <section>
        <h2>Batch decide</h2>
        <p class="muted">
          Decide a whole dataset on the <b>{env}</b> environment (from Test run above) — each row is
          a
          <b>recorded</b> decision (it shows in history, metrics, and the audit log), unlike a backtest.
          Up to 500 rows.
        </p>
        <div class="row">
          <button
            onclick={runBatch}
            disabled={!flow || batchRunning || !roleAtLeast($user?.role, 'operator')}
            title={!roleAtLeast($user?.role, 'operator')
              ? 'Each row is a recorded decision — requires the operator role'
              : undefined}
            data-testid="run-batch"
          >
            {batchRunning ? 'Deciding…' : 'Run batch'}
          </button>
          <button
            type="button"
            class="link"
            onclick={() => (batchDataset = sampleDatasetJson())}
            disabled={!hasInputSchema}
            title={hasInputSchema
              ? 'Prefill a varied 8-row dataset from the input schema'
              : 'This version has no input schema — publish one to generate a sample'}
            data-testid="sample-batch">Sample dataset</button
          >
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
            {#if batchReport.rejected > 0}<span class="changed"
                >{batchReport.rejected} rejected</span
              >{/if}
          </div>
          <div class="table-wrap">
            <table class="bt-table">
              <thead>
                <tr
                  ><th>#</th><th>Status</th><th>Disposition</th><th>Decision</th><th>Detail</th></tr
                >
              </thead>
              <tbody>
                {#each batchReport.results as r (r.index)}
                  <tr>
                    <td>{r.index}</td>
                    <td
                      class={r.status === 'completed'
                        ? 'ok'
                        : r.status === 'failed'
                          ? 'err'
                          : 'changed'}>{r.status}</td
                    >
                    <td>{r.disposition ?? '—'}</td>
                    <td>
                      {#if r.decision_id}<a href={appHref(`/decisions/${r.decision_id}`)}>view</a
                        >{:else}—{/if}
                    </td>
                    <td>{r.error || JSON.stringify(r.data)}</td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        {/if}
      </section>

      <section>
        <h2>Promote to pre-approvals</h2>
        <p class="muted">
          Run the dataset above through the flow's bound <a href={appHref('/policies')}>policy</a>
          and grant a time-boxed <a href={appHref('/preapprovals')}>pre-approval</a> for every row it
          disposes to the chosen disposition — keyed by a field in each row. Each grant's decision output
          becomes the stored offer terms, honored instantly the next time that entity is decided.
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
            disabled={!flow ||
              paRunning ||
              !paEntityType.trim() ||
              !paEntityKey.trim() ||
              !roleAtLeast($user?.role, 'editor')}
            title={!roleAtLeast($user?.role, 'editor')
              ? 'Granting pre-approvals from a run requires the editor role'
              : undefined}
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
            {#if paReport.rejected > 0}<span class="changed">{paReport.rejected} rejected</span
              >{/if}
          </div>
          <div class="table-wrap">
            <table class="bt-table">
              <thead>
                <tr><th>#</th><th>Entity</th><th>Disposition</th><th>Granted</th><th>Detail</th></tr
                >
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
          </div>
        {/if}
      </section>
    {/if}

    {#if tab === 'discuss' && flow}
      <section class="discussion" data-testid="flow-discussion">
        <h2>Discussion</h2>
        <CommentThread subjectType="flow" subjectId={flowId} title="Flow discussion" />
      </section>
    {/if}

    {#if tab === 'copilot'}
      <section class="copilot" data-testid="copilot">
        <h2>Authoring copilot</h2>
        <p class="muted">
          Ask the AI to explain this flow, or describe the decision logic you want and get a
          suggested node breakdown to build. (Backed by the configured AI provider.)
        </p>
        <div class="copilot-actions">
          <button onclick={explainFlow} disabled={copilotBusy}>Explain this flow</button>
        </div>
        <form
          class="copilot-ask"
          onsubmit={(e) => {
            e.preventDefault();
            suggestLogic();
          }}
        >
          <textarea
            bind:value={copilotPrompt}
            aria-label="copilot prompt"
            rows="3"
            placeholder="e.g. Auto-approve applicants with fico ≥ 720 and income ≥ 50k; refer 640–720; decline below 640."
          ></textarea>
          <div class="copilot-buttons">
            <button type="submit" disabled={copilotBusy || !copilotPrompt.trim()}>
              {copilotBusy ? 'Thinking…' : 'Suggest logic'}
            </button>
            <button
              type="button"
              class="primary"
              onclick={generateFlow}
              disabled={copilotBusy || !copilotPrompt.trim()}
            >
              Generate &amp; apply a flow
            </button>
          </div>
        </form>
        {#if copilotOut}
          <pre class="copilot-out" data-testid="copilot-output">{copilotOut}</pre>
        {/if}
      </section>
    {/if}
  {/if}
</main>

<style>
  main {
    max-width: 72rem;
    margin: 2rem auto;
    padding: 0 1rem;
    font-family: var(--font-ui);
    /* Float the canvas to the top as the primary surface; the operational panels
       sit behind the tab bar below it (CSS order avoids reordering the markup). */
    display: flex;
    flex-direction: column;
    gap: 1rem;
  }
  main > .grid {
    order: 1;
  }
  main > .tabbar {
    order: 2;
  }
  main > section {
    order: 3;
  }
  .copilot-actions {
    margin: 0.6rem 0;
  }
  .copilot-ask {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
  }
  .copilot-ask textarea {
    width: 100%;
    box-sizing: border-box;
    font: inherit;
    padding: 0.5rem 0.6rem;
    resize: vertical;
  }
  .copilot-buttons {
    display: flex;
    gap: 0.5rem;
    flex-wrap: wrap;
  }
  .copilot-out {
    margin-top: 0.8rem;
    padding: 0.8rem 1rem;
    background: var(--surface-2);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    white-space: pre-wrap;
    font-family: var(--font-ui);
    font-size: 0.92rem;
    line-height: 1.45;
  }
  .tabbar {
    display: flex;
    gap: 0.25rem;
    flex-wrap: wrap;
    border-bottom: 1px solid var(--border);
    padding-bottom: 0;
  }
  .tabbar button {
    background: none;
    border: none;
    border-bottom: 2px solid transparent;
    border-radius: 0;
    padding: 0.5rem 0.8rem;
    color: var(--fg-muted);
    cursor: pointer;
    font-weight: 500;
  }
  .tabbar button.active {
    color: var(--fg);
    border-bottom-color: var(--accent);
  }
  button.primary {
    background: var(--accent);
    /* --on-accent is the per-persona contrast-safe ink on the accent fill; --accent-fg
       was never defined, so this fell back to white (2.15:1 on the builder's amber). */
    color: var(--on-accent);
    border: 1px solid var(--accent);
    font-weight: 600;
  }
  button.primary:disabled {
    opacity: 0.6;
  }
  textarea.invalid {
    border-color: var(--danger);
  }
  .json-err {
    margin: 0.2rem 0;
    color: var(--danger);
    font-size: 0.8rem;
  }
  .rcode {
    font-family: var(--font-mono);
    font-weight: 600;
    font-size: 0.78rem;
    color: var(--accent-ink);
    background: var(--surface-2);
    padding: 0.05rem 0.4rem;
    border-radius: 0.3rem;
  }
  .verdict-card {
    --tone: var(--fg-muted);
    margin: 0.5rem 0;
    padding: 0.7rem 0.9rem;
    border: 1px solid var(--tone);
    border-left-width: 4px;
    border-radius: var(--radius);
    background: color-mix(in srgb, var(--tone) 8%, var(--surface));
  }
  .verdict-card.ok {
    --tone: var(--ok);
  }
  .verdict-card.danger {
    --tone: var(--danger);
  }
  .verdict-card.warn {
    --tone: var(--warn);
  }
  .verdict-top {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    flex-wrap: wrap;
  }
  .verdict-top .dur {
    margin-left: auto;
    font-size: 0.82rem;
    color: var(--fg-subtle);
    font-variant-numeric: tabular-nums;
  }
  .preview-toggle {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    font-size: 0.85rem;
    color: var(--fg-muted);
    cursor: pointer;
    white-space: nowrap;
  }
  .verdict-err {
    margin: 0.5rem 0 0;
    color: var(--danger);
    font-size: 0.88rem;
  }
  .verdict-out {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 0.2rem 0.9rem;
    margin: 0.55rem 0 0;
    font-size: 0.88rem;
  }
  .verdict-out dt {
    color: var(--fg-subtle);
    font-family: var(--font-mono);
    font-size: 0.82rem;
  }
  .verdict-out dd {
    margin: 0;
    font-variant-numeric: tabular-nums;
    word-break: break-word;
  }
  .raw-result {
    margin: 0.3rem 0;
  }
  .raw-result summary {
    cursor: pointer;
    font-size: 0.82rem;
    color: var(--fg-muted);
    width: max-content;
  }
  .raw-result pre {
    margin-top: 0.4rem;
  }
  .api-call {
    margin: 0.8rem 0 0.2rem;
  }
  .api-call summary {
    cursor: pointer;
    font-size: 0.86rem;
    font-weight: 550;
    color: var(--fg);
    width: max-content;
  }
  .api-call .muted {
    margin: 0.4rem 0 0;
    font-size: 0.82rem;
  }
  /* Miro-style board: the canvas is the full center stage; the tools/inspector float
     over it as overlays rather than taking a fixed column. */
  .grid {
    position: relative;
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
  .check {
    display: flex;
    align-items: flex-start;
    gap: 0.4rem;
    font-size: 0.82rem;
    color: var(--fg-muted);
  }
  .check input {
    width: auto;
    margin-top: 0.15rem;
    flex: none;
  }
  .blank-hint {
    margin: 0.5rem 0 0.2rem;
    padding: 0.5rem 0.7rem;
    font-size: 0.82rem;
    color: var(--fg-muted);
    background: var(--surface-2);
    border-radius: 0.4rem;
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
    /* Clip everything to the board: svelte-flow's edge SVGs extend past the pane
       and were widening the page at narrow viewports (21px body h-scroll @390px). */
    overflow: hidden;
    /* Board: the design window is the page's center of gravity — take the viewport
       minus the compact header + tab strip, never less than the old fixed height. */
    height: calc(100vh - 15rem);
    min-height: 520px;
    border: 1px solid var(--border-strong);
    border-radius: 0.5rem;
    background: var(--surface);
  }
  /* Focus: a full-viewport takeover for pure design work (Esc or Exit focus). */
  .canvas.focus {
    position: fixed;
    inset: 0;
    z-index: 40;
    height: 100vh;
    min-height: 0;
    border: none;
    border-radius: 0;
  }
  /* Collapsed: a slim strip that hands the page to the workflow panels below. */
  .canvas.collapsed {
    height: auto;
    min-height: 0;
    border-style: dashed;
  }
  .canvas-expand {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    width: 100%;
    padding: 0.65rem 0.9rem;
    border: none;
    background: transparent;
    color: var(--fg-muted);
    font-size: 0.9rem;
    cursor: pointer;
  }
  .canvas-expand:hover {
    color: var(--fg);
    background: var(--surface-2);
  }
  .mode-toggle {
    margin-left: auto;
  }
  .board-empty {
    position: absolute;
    inset: 0;
    display: grid;
    place-content: center;
    text-align: center;
    pointer-events: none;
    color: var(--fg-muted);
    padding: 0 4rem;
  }
  .board-empty p {
    margin: 0.2rem 0;
    max-width: 34rem;
  }
  /* svelte-flow paints edge labels on its default white background; in dark theme that
     left the light-token label text near-invisible (1.22:1). Bind both to theme tokens
     so branch conditions read in either theme. */
  /* Zoom (+/-/fit/lock) sits at the bottom-right corner, Miro-style; nudge it
     left of the overview map that shares the corner. */
  .canvas :global(.svelte-flow__controls.bottom.right) {
    margin-right: 14.5rem;
  }
  :global(.svelte-flow__edge-label) {
    background: var(--surface);
    color: var(--fg);
    padding: 1px 5px;
    border-radius: 4px;
    /* The label sits on the edge midpoint — the natural click target for
       selecting a branch edge. svelte-flow pins pointer-events: all inline on
       it without wiring label clicks to onedgeclick, so clicks there died;
       force them through to the interaction path beneath instead. */
    pointer-events: none !important;
  }
  .canvas-tools {
    position: absolute;
    top: 0.6rem;
    left: 0.6rem;
    right: 0.6rem;
    z-index: 7;
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.4rem;
  }
  .view-toggle {
    display: inline-flex;
    border: 1px solid var(--border);
    border-radius: 8px;
    overflow: hidden;
    background: var(--surface-1);
    box-shadow: 0 1px 4px rgb(0 0 0 / 0.12);
  }
  .view-toggle button,
  .relax-btn {
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
    padding: 0.25rem 0.6rem;
    font-size: 0.8rem;
    background: var(--surface-1);
  }
  .view-toggle button {
    border: 0;
    border-radius: 0;
    color: var(--fg-muted);
    box-shadow: none;
  }
  .view-toggle button + button {
    border-left: 1px solid var(--border);
  }
  .view-toggle button.active {
    color: var(--fg);
    background: color-mix(in srgb, var(--accent) 14%, var(--surface-1));
  }
  .relax-btn.active {
    color: var(--fg);
    background: color-mix(in srgb, var(--accent) 16%, var(--surface-1));
    border-color: var(--accent);
  }
  .heat-legend {
    align-self: center;
    margin-left: 0.2rem;
    font-size: 0.72rem;
    color: var(--fg-muted);
  }
  .coverage {
    margin-top: 0.6rem;
    font-size: 0.84rem;
  }
  .cov-dispo {
    display: flex;
    gap: 0.4rem;
    margin: 0.4rem 0;
  }
  .cov-dead {
    border-left: 3px solid var(--danger);
    padding-left: 0.6rem;
    margin-top: 0.4rem;
  }
  .cov-dead ul {
    margin: 0.2rem 0;
    padding-left: 1.1rem;
  }
  .cov-dead code {
    font-size: 0.78rem;
  }
  .cov-clean {
    color: var(--accent-ink);
    font-weight: 600;
  }
  .relax-btn {
    border: 1px solid var(--border);
    border-radius: 8px;
    box-shadow: 0 1px 4px rgb(0 0 0 / 0.12);
  }
  aside {
    position: absolute;
    top: 3.1rem;
    max-height: calc(100% - 3.7rem);
    overflow-y: auto;
    padding: 0.2rem 0.9rem 0.9rem;
    background: color-mix(in srgb, var(--surface) 92%, transparent);
    backdrop-filter: blur(6px);
    border: 1px solid var(--border);
    border-radius: 0.7rem;
    box-shadow: 0 10px 30px rgb(0 0 0 / 0.18);
    z-index: 6;
    font-size: 0.92rem;
  }
  /* The build palette docks left; the inspector opens right where properties live
     in every design tool — selecting a node never covers what you're wiring. */
  aside.tools {
    left: 3.8rem;
    width: 16rem;
  }
  .node-rail {
    position: absolute;
    top: 3.1rem;
    left: 0.6rem;
    max-height: calc(100% - 3.7rem);
    overflow-y: auto;
    z-index: 6;
    display: flex;
    flex-direction: column;
    gap: 0.15rem;
    padding: 0.35rem;
    background: color-mix(in srgb, var(--surface) 92%, transparent);
    backdrop-filter: blur(6px);
    border: 1px solid var(--border);
    border-radius: 0.7rem;
    box-shadow: 0 10px 30px rgb(0 0 0 / 0.18);
  }
  .node-rail button {
    display: grid;
    place-items: center;
    width: 2.1rem;
    height: 2.1rem;
    padding: 0;
    border: none;
    border-radius: 0.5rem;
    background: transparent;
    color: var(--fg-muted);
    cursor: pointer;
  }
  .node-rail button:hover {
    background: var(--surface-2);
    color: var(--fg);
  }
  aside.inspector {
    right: 0.6rem;
    width: 22rem;
  }
  .insp-head {
    display: flex;
    align-items: center;
    gap: 0.45rem;
    position: sticky;
    top: 0;
    padding: 0.65rem 0 0.5rem;
    background: inherit;
    border-bottom: 1px solid var(--border);
    z-index: 1;
  }
  .insp-title {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .insp-head .x {
    margin-left: auto;
  }
  .insp-foot {
    margin-top: 0.8rem;
    padding-top: 0.6rem;
    border-top: 1px solid var(--border);
  }
  .insp-delete {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    color: var(--danger);
  }
  .flowhead {
    display: flex;
    align-items: center;
    gap: 0.7rem;
    margin: 0.4rem 0 0.5rem;
  }
  .flowhead h1 {
    margin: 0;
    font-size: 1.45rem;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .unsaved {
    flex: none;
    font-size: 0.75rem;
    font-weight: 600;
    color: var(--warn, #b26a00);
    white-space: nowrap;
  }
  .backlink {
    font-size: 1.2rem;
    text-decoration: none;
    padding: 0.1rem 0.4rem;
    border-radius: 0.4rem;
  }
  .backlink:hover {
    background: var(--surface-2);
  }
  .head-actions {
    margin-left: auto;
    display: flex;
    gap: 0.5rem;
    flex: none;
    align-items: center;
  }
  /* The circled robot — same visual language as the circled "i"/"?" affordances. */
  .iconbtn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 2.1rem;
    height: 2.1rem;
    padding: 0;
    border-radius: 999px;
    border: 1px solid var(--border);
    background: var(--surface-2);
    color: var(--fg-muted);
    cursor: pointer;
  }
  .iconbtn:hover {
    color: var(--fg);
    border-color: color-mix(in srgb, var(--accent) 45%, var(--border));
  }
  .iconbtn:disabled {
    opacity: 0.6;
    cursor: default;
  }
  .flow-desc {
    margin: -0.2rem 0 0.5rem;
    font-size: 0.9rem;
  }
  p.flow-desc {
    display: flex;
    align-items: baseline;
    gap: 0.4rem;
  }
  .flow-desc textarea {
    width: 100%;
    box-sizing: border-box;
    font: inherit;
    font-size: 0.9rem;
    padding: 0.45rem 0.6rem;
    border: 1px solid var(--border);
    border-radius: 8px;
    background: var(--surface);
    color: var(--fg);
    resize: vertical;
  }
  .desc-actions {
    display: flex;
    gap: 0.5rem;
    margin-top: 0.4rem;
  }
  .pencil {
    background: none;
    border: none;
    padding: 0 0.2rem;
    font: inherit;
    color: var(--fg-subtle);
    cursor: pointer;
  }
  .pencil:hover {
    color: var(--fg);
  }
  .analyze {
    border: 1px solid color-mix(in srgb, var(--accent) 35%, var(--border));
    border-radius: 10px;
    padding: 0.6rem 0.9rem;
    margin: 0 0 0.5rem;
    background: color-mix(in srgb, var(--accent) 5%, transparent);
    order: 0; /* stays with the header, above the canvas grid */
  }
  .analyze-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.6rem;
  }
  .analyze-head b {
    display: inline-flex;
    align-items: center;
    gap: 0.4rem;
  }
  .analyze-out {
    white-space: pre-wrap;
    word-break: break-word;
    margin: 0.4rem 0 0;
    font-family: var(--font-mono, monospace);
    font-size: 0.85rem;
  }
  .sharebar {
    margin: 0 0 0.5rem;
  }
  .sharebar > summary {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    cursor: pointer;
    font-size: 0.85rem;
    color: var(--fg-muted);
    padding: 0.25rem 0.5rem;
    border-radius: 0.4rem;
  }
  .sharebar > summary:hover {
    background: var(--surface-2);
    color: var(--fg);
  }
  .sharebar[open] {
    padding: 0.4rem 0.7rem 0.6rem;
    border: 1px solid var(--border);
    border-radius: 0.6rem;
    background: var(--surface-2);
  }
  @media (max-width: 720px) {
    /* Narrow screens can't fit the floating toolbar and the floating tools panel
       over the canvas without them colliding (both anchored top: 0.6rem). Drop the
       overlay model: stack the toolbar, canvas, and panel in normal flow instead. */
    .grid {
      display: flex;
      flex-direction: column;
      gap: 0.6rem;
    }
    .canvas {
      display: flex;
      flex-direction: column;
      height: auto;
      min-height: 0;
    }
    .canvas-tools {
      position: static;
      order: -1;
    }
    .canvas :global(.svelte-flow) {
      height: 60vh;
      min-height: 360px;
    }
    .node-rail {
      position: static;
      order: -1;
      flex-direction: row;
      flex-wrap: wrap;
    }
    aside {
      position: static;
      width: auto;
      max-height: none;
      box-shadow: none;
    }
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
    /* ≥24px touch/click target even though the glyph is small (WCAG 2.5.8). */
    min-width: 24px;
    min-height: 24px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
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
  .linkbtn {
    background: none;
    border: none;
    color: var(--fg-subtle);
    cursor: pointer;
    padding: 0 0 0 0.3rem;
    font-size: 0.78rem;
    text-decoration: underline;
  }
  .linkbtn:disabled {
    opacity: 0.5;
    cursor: default;
  }
  .deploy .hint {
    font-size: 0.8rem;
    margin: 0.4rem 0 0;
  }
  .promotion-policy {
    margin-top: 0.7rem;
    font-size: 0.86rem;
  }
  .promotion-policy summary {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    cursor: pointer;
    color: var(--fg-muted);
  }
  .policy-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(13rem, 1fr));
    gap: 0.6rem;
    margin-top: 0.55rem;
  }
  .policy-stage {
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
    padding: 0.5rem 0.6rem;
    border: 1px solid var(--border);
    border-radius: 8px;
    background: var(--surface-1);
  }
  .policy-stage b {
    font-size: 0.8rem;
    text-transform: uppercase;
    color: var(--fg-subtle);
  }
  .policy-stage label {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    margin: 0;
    font-size: 0.78rem;
    color: var(--fg-muted);
  }
  .shadow-panel {
    margin-top: 0.7rem;
    font-size: 0.86rem;
  }
  .shadow-panel summary {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    cursor: pointer;
    color: var(--fg-muted);
  }
  .shadow-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(13rem, 1fr));
    gap: 0.6rem;
    margin-top: 0.55rem;
  }
  .shadow-stage {
    display: flex;
    flex-direction: column;
    gap: 0.3rem;
    padding: 0.5rem 0.6rem;
    border: 1px solid var(--border);
    border-radius: 8px;
    background: var(--surface-1);
  }
  .shadow-stage b {
    font-size: 0.8rem;
    text-transform: uppercase;
    color: var(--fg-subtle);
  }
  .shadow-stats {
    font-size: 0.78rem;
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
  .changed-row td {
    background: color-mix(in srgb, var(--accent) 12%, transparent);
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
  .band {
    border-left: 2px solid #8883;
    padding-left: 0.5rem;
    margin: 0.35rem 0;
  }
  .row.bandcode {
    margin-left: 1rem;
  }
  .cellrow {
    font-size: 0.75rem;
    color: var(--fg-subtle);
    min-width: 5rem;
  }
</style>
