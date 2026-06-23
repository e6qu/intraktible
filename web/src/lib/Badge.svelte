<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Shared status pill. One badge style across the app (decision/case status,
  disposition, MRM coverage, key lifecycle) so the same state reads with the same
  colour everywhere — see $lib/badge for the value→tone maps. Pass a tone + the label
  as children: <Badge tone={statusTone(d.status)}>{d.status}</Badge>.
-->
<script lang="ts">
  import type { Snippet } from 'svelte';
  import type { Tone } from '$lib/badge';

  let { tone = 'neutral', children }: { tone?: Tone; children: Snippet } = $props();
</script>

<span class="badge {tone}">{@render children()}</span>

<style>
  .badge {
    display: inline-flex;
    align-items: center;
    gap: 0.3em;
    padding: 0.1rem 0.5rem;
    border-radius: 999px;
    font-size: 0.74rem;
    font-weight: 500;
    line-height: 1.5;
    white-space: nowrap;
    border: 1px solid transparent;
  }
  .ok {
    color: var(--ok, #16a34a);
    background: color-mix(in srgb, var(--ok, #16a34a) 14%, transparent);
    border-color: color-mix(in srgb, var(--ok, #16a34a) 30%, transparent);
  }
  .warn {
    color: var(--warn, #b45309);
    background: color-mix(in srgb, var(--warn, #d97706) 16%, transparent);
    border-color: color-mix(in srgb, var(--warn, #d97706) 32%, transparent);
  }
  .danger {
    color: var(--danger, #dc2626);
    background: color-mix(in srgb, var(--danger, #dc2626) 13%, transparent);
    border-color: color-mix(in srgb, var(--danger, #dc2626) 30%, transparent);
  }
  .info {
    color: var(--accent-ink, var(--accent));
    background: color-mix(in srgb, var(--accent) 13%, transparent);
    border-color: color-mix(in srgb, var(--accent) 28%, transparent);
  }
  .neutral {
    color: var(--fg-muted);
    background: var(--surface-2);
    border-color: var(--border);
  }
</style>
