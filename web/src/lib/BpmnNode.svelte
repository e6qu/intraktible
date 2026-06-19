<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- A flow node rendered in BPMN notation (the engine's process view): the input
     is a start event, the output an end event, a split a diamond gateway, and
     every other node a task with a type marker. Same model, BPMN skin. -->
<script lang="ts">
  import { Handle, Position, type NodeProps } from '@xyflow/svelte';
  import Icon from '$lib/Icon.svelte';
  import { bpmnKind, nodeAccent } from '$lib/nodevis';

  type BpmnData = { type: string; name: string };
  let { data, selected }: NodeProps & { data: BpmnData } = $props();
  const kind = $derived(bpmnKind(data.type));
  const accent = $derived(nodeAccent(data.type));
  const label = $derived(data.name || data.type);
</script>

<div class="bpmn {kind}" class:selected style="--accent: {accent}">
  {#if data.type !== 'input'}<Handle type="target" position={Position.Left} />{/if}

  {#if kind === 'task'}
    <span class="ic"><Icon name={data.type} size={15} /></span>
    <span class="label">{label}</span>
  {:else}
    <span class="shape {kind}">
      {#if kind === 'gateway'}<span class="x">×</span>{:else}<Icon
          name={data.type}
          size={14}
        />{/if}
    </span>
    <span class="cap">{label}</span>
  {/if}

  {#if data.type !== 'output'}<Handle type="source" position={Position.Right} />{/if}
</div>

<style>
  /* Task: a rounded-rectangle activity. */
  .bpmn.task {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    min-width: 8rem;
    max-width: 13rem;
    padding: 0.5rem 0.7rem;
    background: var(--surface, #fff);
    color: var(--fg);
    border: 1.5px solid var(--border-strong);
    border-radius: 8px;
    font-size: 0.84rem;
  }
  .bpmn.task .ic {
    color: var(--accent);
    display: inline-flex;
  }
  .bpmn.task .label {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  /* Events + gateway: a small shape with a caption beneath. */
  .bpmn.start,
  .bpmn.end,
  .bpmn.gateway {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 0.2rem;
    background: transparent;
  }
  .shape {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 42px;
    height: 42px;
    color: var(--accent);
    background: var(--surface, #fff);
  }
  .shape.start {
    border: 2px solid var(--accent);
    border-radius: 50%;
  }
  .shape.end {
    border: 4px solid var(--accent);
    border-radius: 50%;
  }
  .shape.gateway {
    border: 1.5px solid var(--accent);
    /* a diamond via a rotated square's clip would hide the icon; use clip-path */
    clip-path: polygon(50% 0, 100% 50%, 50% 100%, 0 50%);
    background: color-mix(in srgb, var(--accent) 12%, var(--surface, #fff));
  }
  .gateway .x {
    font-size: 1.1rem;
    font-weight: 700;
    color: var(--accent);
  }
  .cap {
    font-size: 0.72rem;
    color: var(--fg-muted);
    max-width: 7rem;
    text-align: center;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .selected.task {
    border-color: var(--accent);
    box-shadow: 0 0 0 2px color-mix(in srgb, var(--accent) 40%, transparent);
  }
  .selected .shape {
    box-shadow: 0 0 0 2px color-mix(in srgb, var(--accent) 40%, transparent);
  }
</style>
