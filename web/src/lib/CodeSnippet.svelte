<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- A copyable multi-line code block (mono): a titled <pre> with a copy button that
     flips to a check briefly. Used to SHOW the API contract in-product — e.g. the
     exact `decide` request a developer would make — so "API-first" is demonstrated,
     not just stated. -->
<script lang="ts">
  import Icon from '$lib/Icon.svelte';
  import { copyText } from '$lib/clipboard';

  let { code, label = 'snippet' }: { code: string; label?: string } = $props();
  let copied = $state(false);
  let timer: ReturnType<typeof setTimeout> | undefined;

  async function copy(): Promise<void> {
    if (await copyText(code, `Copied ${label}`)) {
      copied = true;
      clearTimeout(timer);
      timer = setTimeout(() => (copied = false), 1200);
    }
  }
</script>

<div class="snippet">
  <button class="copy" onclick={copy} title="Copy to clipboard" aria-label={`Copy ${label}`}>
    <Icon name={copied ? 'check' : 'clipboard'} size={13} />
    {copied ? 'Copied' : 'Copy'}
  </button>
  <pre data-testid="code-snippet"><code>{code}</code></pre>
</div>

<style>
  .snippet {
    position: relative;
    margin: 0.5rem 0;
  }
  .copy {
    position: absolute;
    top: 0.5rem;
    right: 0.5rem;
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
    padding: 0.15rem 0.5rem;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    background: var(--surface-2);
    color: var(--fg-muted);
    font-size: 0.74rem;
    cursor: pointer;
  }
  .copy:hover {
    border-color: var(--accent);
    color: var(--fg);
  }
  pre {
    margin: 0;
    padding: 0.8rem 0.9rem;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--surface-2);
    overflow-x: auto;
    font-family: var(--font-mono);
    font-size: 0.82rem;
    line-height: 1.5;
    color: var(--fg);
  }
</style>
