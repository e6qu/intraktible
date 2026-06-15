<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- App-wide toast container (announced to assistive tech via aria-live). -->
<script lang="ts">
  import Icon from '$lib/Icon.svelte';
  import { toasts, dismiss } from '$lib/toast';

  const icon = (kind: string) =>
    kind === 'success' ? 'check' : kind === 'error' ? 'trash' : 'diagram';
</script>

<div class="toasts" aria-live="polite" aria-atomic="false">
  {#each $toasts as t (t.id)}
    <div class="toast {t.kind}" role="status">
      <span class="ico"><Icon name={icon(t.kind)} size={15} /></span>
      <span class="msg">{t.message}</span>
      <button class="x" aria-label="Dismiss" onclick={() => dismiss(t.id)}>✕</button>
    </div>
  {/each}
</div>

<style>
  .toasts {
    position: fixed;
    bottom: 1rem;
    right: 1rem;
    z-index: 100;
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
    max-width: min(90vw, 26rem);
  }
  .toast {
    display: flex;
    align-items: center;
    gap: 0.6rem;
    padding: 0.6rem 0.8rem;
    border-radius: var(--radius);
    background: var(--surface);
    border: 1px solid var(--border);
    box-shadow: var(--shadow);
    font-size: 0.9rem;
  }
  .toast.success .ico {
    color: var(--ok);
  }
  .toast.error {
    border-color: color-mix(in srgb, var(--danger) 40%, var(--border));
  }
  .toast.error .ico {
    color: var(--danger);
  }
  .toast.info .ico {
    color: var(--accent);
  }
  .msg {
    flex: 1;
    min-width: 0;
  }
  .x {
    border: none;
    background: none;
    color: var(--fg-subtle);
    cursor: pointer;
    padding: 0;
  }
  .x:hover {
    color: var(--fg);
    background: none;
  }
</style>
