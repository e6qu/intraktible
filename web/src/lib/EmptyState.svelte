<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- A designed empty state: an outlined icon, a headline, a hint, and an optional
     action slot. Replaces the bare "nothing here" muted text on list pages. -->
<script lang="ts">
  import Icon from '$lib/Icon.svelte';
  import type { Snippet } from 'svelte';

  let {
    icon = 'diagram',
    title,
    hint = '',
    action
  }: { icon?: string; title: string; hint?: string; action?: Snippet } = $props();
</script>

<div class="empty">
  <span class="glyph"><Icon name={icon} size={26} /></span>
  <p class="title">{title}</p>
  {#if hint}<p class="hint">{hint}</p>{/if}
  {#if action}<div class="action">{@render action()}</div>{/if}
</div>

<style>
  .empty {
    display: flex;
    flex-direction: column;
    align-items: center;
    text-align: center;
    gap: 0.45rem;
    padding: 3rem 1.5rem;
    margin: 1rem 0;
    border: 1px dashed var(--border-strong);
    border-radius: var(--radius);
    background: color-mix(in srgb, var(--surface) 60%, transparent);
    color: var(--fg-muted);
  }
  .glyph {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 52px;
    height: 52px;
    border-radius: 14px;
    color: var(--accent-ink);
    background: color-mix(in srgb, var(--accent) 12%, transparent);
    margin-bottom: 0.3rem;
  }
  .title {
    margin: 0;
    font-weight: 600;
    font-size: 1rem;
    color: var(--fg);
  }
  .hint {
    margin: 0;
    font-size: 0.9rem;
    max-width: 28rem;
  }
  .action {
    margin-top: 0.6rem;
  }
</style>
