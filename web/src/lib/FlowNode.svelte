<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- A flow-builder node rendered as a typed card: a colour rail + icon for the
     node type, its name, a one-line config summary, and (after a test run) the
     node's last output. Registered as the 'flow' node type on the canvas. -->
<script lang="ts">
  import { Handle, Position, type NodeProps } from '@xyflow/svelte';
  import Icon from '$lib/Icon.svelte';
  import { nodeAccent } from '$lib/nodevis';

  // Svelte Flow passes the node's data; we carry the type, name, config summary,
  // and an optional last-run telemetry string.
  type FlowData = { type: string; name: string; summary: string; telemetry?: string };
  let { data, selected }: NodeProps & { data: FlowData } = $props();
  const accent = $derived(nodeAccent(data.type));
</script>

<div class="node" class:selected style="--rail: {accent}">
  {#if data.type !== 'input'}<Handle type="target" position={Position.Left} />{/if}
  <span class="ic" style="color: {accent}"><Icon name={data.type} size={16} /></span>
  <div class="body">
    <span class="name">{data.name || data.type}</span>
    <span class="sub">{data.type} · {data.summary}</span>
    {#if data.telemetry}<span class="telem" title="last test-run output">▸ {data.telemetry}</span
      >{/if}
  </div>
  {#if data.type !== 'output'}<Handle type="source" position={Position.Right} />{/if}
</div>

<style>
  .node {
    display: flex;
    align-items: flex-start;
    gap: 0.45rem;
    min-width: 9.5rem;
    max-width: 15rem;
    padding: 0.45rem 0.6rem 0.45rem 0.5rem;
    background: var(--surface, #fff);
    color: var(--fg);
    border: 1px solid var(--border-strong);
    border-left: 4px solid var(--rail);
    border-radius: 8px;
    box-shadow: var(--shadow, 0 1px 3px rgb(0 0 0 / 0.12));
    font-size: 0.82rem;
  }
  .node.selected {
    border-color: var(--rail);
    box-shadow: 0 0 0 2px color-mix(in srgb, var(--rail) 45%, transparent);
  }
  .ic {
    display: inline-flex;
    margin-top: 0.05rem;
  }
  .body {
    display: flex;
    flex-direction: column;
    gap: 0.05rem;
    min-width: 0;
  }
  .name {
    font-weight: 600;
    line-height: 1.15;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .sub {
    font-size: 0.7rem;
    color: var(--fg-subtle);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .telem {
    margin-top: 0.15rem;
    font-size: 0.7rem;
    font-variant-numeric: tabular-nums;
    color: color-mix(in srgb, var(--rail) 75%, var(--fg));
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
</style>
