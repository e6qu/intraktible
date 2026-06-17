<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- A relative timestamp ("2m ago") that stays fresh via the shared clock, with
     the full timestamp on hover. Renders a semantic <time> element. -->
<script lang="ts">
  import { now, relativeTime, absoluteTime } from '$lib/time';
  let { value }: { value: string } = $props();
</script>

<time datetime={value} title={absoluteTime(value)}>{relativeTime(value, $now)}</time>
