<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Hands the SvelteFlow instance API (screenToFlowPosition etc.) up to the page.
     useSvelteFlow only resolves inside the SvelteFlow context, so this child is
     the bridge that lets page-level handlers (e.g. drop-to-place from the node
     rail) translate screen coordinates into flow coordinates. Renders nothing. -->
<script lang="ts">
  import { useSvelteFlow } from '@xyflow/svelte';

  let { register }: { register: (api: ReturnType<typeof useSvelteFlow>) => void } = $props();
  const api = useSvelteFlow();
  $effect(() => {
    register(api);
  });
</script>
