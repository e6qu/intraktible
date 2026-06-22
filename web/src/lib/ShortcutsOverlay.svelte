<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Keyboard shortcuts: a "?" help overlay plus the global single-key shortcuts it
     documents — `t` toggles the theme and `g`-then-key navigates (GitHub-style).
     All bare-key shortcuts are ignored while typing in a field or with a modifier
     held (so they never fight ⌘K or text entry). -->
<script lang="ts">
  import { goto } from '$app/navigation';
  import { theme, setTheme } from '$lib/theme';
  import { shortcutsOpen, closeShortcuts, GO_NAV } from '$lib/shortcuts';
  import { appHref } from '$lib/paths';

  let pendingG = $state(false);
  let gTimer: ReturnType<typeof setTimeout> | undefined;

  function inEditable(t: EventTarget | null): boolean {
    const el = t as HTMLElement | null;
    if (!el || !el.tagName) return false;
    const tag = el.tagName;
    return tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT' || el.isContentEditable;
  }

  $effect(() => {
    function onKey(e: KeyboardEvent): void {
      // Leave modifier combos (⌘K etc.) and in-field typing alone.
      if (e.metaKey || e.ctrlKey || e.altKey || inEditable(e.target)) return;

      if ($shortcutsOpen && e.key === 'Escape') {
        e.preventDefault();
        closeShortcuts();
        return;
      }
      if (e.key === '?') {
        e.preventDefault();
        shortcutsOpen.update((v) => !v);
        return;
      }
      if (pendingG) {
        pendingG = false;
        const target = GO_NAV.find((g) => g.key === e.key.toLowerCase());
        if (target) {
          e.preventDefault();
          closeShortcuts();
          goto(appHref(target.href));
        }
        return;
      }
      if (e.key === 'g') {
        pendingG = true;
        clearTimeout(gTimer);
        gTimer = setTimeout(() => (pendingG = false), 1200);
        return;
      }
      if (e.key === 't' || e.key === 'T') {
        e.preventDefault();
        setTheme($theme === 'dark' ? 'light' : 'dark');
      }
    }
    window.addEventListener('keydown', onKey);
    return () => {
      window.removeEventListener('keydown', onKey);
      clearTimeout(gTimer);
    };
  });
</script>

{#if $shortcutsOpen}
  <div class="sc-root">
    <button class="sc-backdrop" aria-label="Close keyboard shortcuts" onclick={closeShortcuts}
    ></button>
    <div class="sc-panel" role="dialog" aria-modal="true" aria-label="Keyboard shortcuts">
      <h2>Keyboard shortcuts</h2>
      <dl>
        <div>
          <dt><kbd>⌘</kbd><kbd>K</kbd> <span class="or">or</span> <kbd>Ctrl</kbd><kbd>K</kbd></dt>
          <dd>Command palette — go to anything</dd>
        </div>
        <div>
          <dt><kbd>?</kbd></dt>
          <dd>This help</dd>
        </div>
        <div>
          <dt><kbd>t</kbd></dt>
          <dd>Toggle light / dark theme</dd>
        </div>
        <div class="grp-h">
          <dt><kbd>g</kbd> then…</dt>
          <dd>Jump to a section</dd>
        </div>
        {#each GO_NAV as g (g.key)}
          <div>
            <dt class="indent"><kbd>g</kbd> <kbd>{g.key}</kbd></dt>
            <dd>{g.label}</dd>
          </div>
        {/each}
      </dl>
      <p class="sc-foot"><kbd>esc</kbd> to close</p>
    </div>
  </div>
{/if}

<style>
  .sc-root {
    position: fixed;
    inset: 0;
    z-index: 200;
    display: flex;
    justify-content: center;
    align-items: center;
    padding: 1rem;
  }
  .sc-backdrop {
    position: absolute;
    inset: 0;
    border: none;
    padding: 0;
    background: color-mix(in srgb, #000 45%, transparent);
    backdrop-filter: blur(2px);
    cursor: default;
  }
  .sc-panel {
    position: relative;
    width: min(30rem, 92vw);
    max-height: 80vh;
    overflow-y: auto;
    padding: 1.3rem 1.5rem;
    background: var(--surface);
    border: 1px solid var(--border-strong);
    border-radius: var(--radius);
    box-shadow: 0 10px 40px rgba(0, 0, 0, 0.35);
    animation: sc-in 0.12s ease both;
  }
  @keyframes sc-in {
    from {
      opacity: 0;
      transform: translateY(-6px) scale(0.99);
    }
  }
  .sc-panel h2 {
    margin: 0 0 0.9rem;
    font-size: 1.05rem;
    text-transform: none;
    letter-spacing: 0;
    color: var(--fg);
  }
  dl {
    margin: 0;
    display: flex;
    flex-direction: column;
    gap: 0.1rem;
  }
  dl div {
    display: flex;
    align-items: baseline;
    gap: 1rem;
    padding: 0.3rem 0;
  }
  dt {
    flex: 0 0 9rem;
    display: flex;
    align-items: center;
    gap: 0.25rem;
  }
  dt.indent {
    padding-left: 0.6rem;
  }
  dd {
    margin: 0;
    color: var(--fg-muted);
    font-size: 0.92rem;
  }
  .grp-h {
    margin-top: 0.6rem;
    border-top: 1px solid var(--border);
    padding-top: 0.7rem;
  }
  .grp-h dd {
    color: var(--fg-subtle);
    text-transform: uppercase;
    font-size: 0.72rem;
    letter-spacing: 0.05em;
  }
  .or {
    color: var(--fg-subtle);
    font-size: 0.78rem;
  }
  kbd {
    font-family: var(--font-mono);
    font-size: 0.75rem;
    min-width: 1.1rem;
    text-align: center;
    padding: 0.1rem 0.35rem;
    border: 1px solid var(--border-strong);
    border-bottom-width: 2px;
    border-radius: 4px;
    color: var(--fg);
    background: var(--surface-2);
  }
  .sc-foot {
    margin: 1rem 0 0;
    color: var(--fg-subtle);
    font-size: 0.8rem;
  }
</style>
