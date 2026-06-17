<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- A swimlane backdrop on the builder canvas: a labelled translucent band sized to
     cover the nodes assigned to that lane. Rendered behind the flow nodes (zIndex)
     and non-interactive, so it groups without getting in the way. -->
<script lang="ts">
  import type { NodeProps } from '@xyflow/svelte';
  type LaneData = { label: string; width: number; height: number };
  let { data }: NodeProps & { data: LaneData } = $props();
</script>

<div class="lane" style="width: {data.width}px; height: {data.height}px">
  <span class="label">{data.label}</span>
</div>

<style>
  .lane {
    border: 1px dashed var(--border-strong);
    border-radius: 10px;
    background: color-mix(in srgb, var(--accent) 4%, transparent);
    pointer-events: none;
  }
  .label {
    position: absolute;
    top: 0.3rem;
    left: 0.5rem;
    font-size: 0.7rem;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--fg-subtle);
  }
</style>
