<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- A click-to-copy value (mono): shows the value with a copy icon that flips to a
     check for a moment after copying. For the IDs/etags developers grab to hit
     the API or correlate the event log. -->
<script lang="ts">
  import Icon from '$lib/Icon.svelte';
  import { copyText } from '$lib/clipboard';

  let { value, label }: { value: string; label?: string } = $props();
  let copied = $state(false);
  let timer: ReturnType<typeof setTimeout> | undefined;

  async function copy(): Promise<void> {
    if (await copyText(value, `Copied ${label ?? 'value'}`)) {
      copied = true;
      clearTimeout(timer);
      timer = setTimeout(() => (copied = false), 1200);
    }
  }
</script>

<button
  class="copyable"
  onclick={copy}
  title="Copy to clipboard"
  aria-label={`Copy ${label ?? value}`}
  data-testid="copyable"
>
  <span class="val">{value}</span>
  <span class="ic" class:done={copied}
    ><Icon name={copied ? 'check' : 'clipboard'} size={13} /></span
  >
</button>

<style>
  .copyable {
    display: inline-flex;
    align-items: center;
    gap: 0.4rem;
    padding: 0.1rem 0.45rem;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    background: var(--surface-2);
    color: var(--fg);
    font-family: var(--font-mono);
    font-size: 0.85rem;
    cursor: pointer;
    max-width: 100%;
  }
  .copyable:hover {
    border-color: var(--accent);
  }
  .val {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .ic {
    display: inline-flex;
    color: var(--fg-subtle);
    flex: none;
  }
  .ic.done {
    color: var(--ok);
  }
</style>
