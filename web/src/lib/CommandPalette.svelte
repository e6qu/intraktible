<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Global ⌘K command palette: jump to any page, switch persona or theme, or sign
     out — all from the keyboard. Opens on Cmd/Ctrl-K (or the header trigger),
     filters as you type, and is driven with ↑/↓/Enter/Esc. -->
<script lang="ts">
  import { goto } from '$app/navigation';
  import Icon from '$lib/Icon.svelte';
  import { theme, setTheme } from '$lib/theme';
  import { setPersona, PERSONAS, NAV, isAdminOnlyRoute } from '$lib/persona';
  import { roleAtLeast } from '$lib/roles';
  import { user, signOut } from '$lib/session';
  import { paletteOpen, closePalette, togglePalette } from '$lib/palette';
  import { openShortcuts } from '$lib/shortcuts';
  import {
    listFlows,
    listAgents,
    listCases,
    listPolicies,
    listModels,
    listEntities
  } from '$lib/api';
  import { appHref } from '$lib/paths';
  import { toast } from '$lib/toast';

  type Cmd = {
    id: string;
    section: string;
    label: string;
    hint?: string;
    icon: string;
    keywords: string;
    run: () => void;
  };

  let query = $state('');
  let selected = $state(0);
  let inputEl = $state<HTMLInputElement | null>(null);
  // The element focused before the palette opened, so focus returns there on close
  // (a screen-reader/keyboard user isn't dumped back at the top of the page).
  let restoreFocusEl: HTMLElement | null = null;
  // Tenant entities (flows/agents/cases) loaded when the palette opens, so the
  // search can jump straight to a specific flow/agent/case by name.
  let dynamic = $state<Cmd[]>([]);

  function navCmd(href: string, label: string, icon: string): Cmd {
    return {
      id: `nav:${href}`,
      section: 'Go to',
      label,
      icon,
      keywords: `${label} ${href} open page navigate`,
      run: () => goto(appHref(href))
    };
  }

  // The full command set is derived so the theme/persona/auth-dependent entries
  // stay in sync with the current state.
  // "Go to" commands cover the FULL page catalog (the palette is a jump-anywhere power
  // tool, not scoped to the persona's default nav), gated by the signed-in role so it
  // never offers an admin-only page (audit/mrm/keys) to a non-admin.
  const navCommands = $derived([
    navCmd('/', 'Home dashboard', 'home'),
    ...[...NAV.values()]
      .filter((item) => roleAtLeast($user?.role, 'admin') || !isAdminOnlyRoute(item.href))
      .map((item) => navCmd(item.href, item.label, item.icon))
  ]);

  async function signOutFromPalette(): Promise<void> {
    try {
      const logoutURL = await signOut();
      if (logoutURL) {
        window.location.assign(logoutURL);
        return;
      }
      window.location.assign(appHref('/v1/auth/signed-out'));
    } catch (error) {
      toast.error(error instanceof Error ? error.message : String(error));
      window.location.assign(appHref('/v1/auth/signed-out'));
    }
  }

  const commands = $derived<Cmd[]>([
    ...navCommands,
    {
      id: 'theme',
      section: 'Appearance',
      label: $theme === 'dark' ? 'Switch to light theme' : 'Switch to dark theme',
      icon: $theme === 'dark' ? 'sun' : 'moon',
      keywords: 'theme dark light mode appearance toggle',
      run: () => setTheme($theme === 'dark' ? 'light' : 'dark')
    },
    ...PERSONAS.map(
      (p): Cmd => ({
        id: `persona:${p.id}`,
        section: 'View as',
        label: `View as ${p.label}`,
        hint: p.blurb,
        icon: p.icon,
        keywords: `persona view as ${p.label} ${p.blurb}`,
        run: () => setPersona(p.id)
      })
    ),
    {
      id: 'shortcuts',
      section: 'Help',
      label: 'Keyboard shortcuts',
      hint: 'press ?',
      icon: 'search',
      keywords: 'keyboard shortcuts help hotkeys ?',
      run: () => openShortcuts()
    },
    $user
      ? {
          id: 'signout',
          section: 'Account',
          label: 'Sign out',
          icon: 'signout',
          keywords: 'sign out log out account',
          run: () => void signOutFromPalette()
        }
      : {
          id: 'signin',
          section: 'Account',
          label: 'Sign in',
          icon: 'user',
          keywords: 'sign in log in account',
          run: () => goto(appHref('/login'))
        }
  ]);

  function matches(cmds: Cmd[], q: string): Cmd[] {
    const tokens = q.toLowerCase().split(/\s+/).filter(Boolean);
    if (tokens.length === 0) return cmds;
    return cmds.filter((c) => {
      const hay = `${c.label} ${c.section} ${c.keywords}`.toLowerCase();
      return tokens.every((t) => hay.includes(t));
    });
  }

  // With no query, show only the static commands (a clean launcher); once typing,
  // search the entities too and cap how many entity hits render.
  const filtered = $derived(
    query.trim() === ''
      ? commands
      : [...matches(commands, query), ...matches(dynamic, query).slice(0, 12)]
  );

  function run(c: Cmd | undefined): void {
    if (!c) return;
    closePalette();
    c.run();
  }

  let loadSeq = 0;
  let loadError = $state('');
  const dynamicBySource = new Map<string, Cmd[]>();
  function appendDynamic<T>(
    seq: number,
    source: string,
    request: Promise<T>,
    commandsFor: (value: T) => Cmd[]
  ): void {
    void request.then(
      (value) => {
        if (seq !== loadSeq) return;
        dynamicBySource.set(source, commandsFor(value));
        dynamic = [...dynamicBySource.values()].flat();
      },
      (reason: unknown) => {
        if (seq !== loadSeq) return;
        const message = `${source}: ${String(reason)}`;
        loadError = loadError === '' ? message : `${loadError} · ${message}`;
      }
    );
  }

  function loadDynamic(remainingRefreshes = 2): void {
    const seq = ++loadSeq;
    loadError = '';
    appendDynamic(seq, 'flows', listFlows(''), (flows) =>
      flows.map(
        (f): Cmd => ({
          id: `flow:${f.flow_id}`,
          section: 'Flows',
          label: f.name || f.slug,
          hint: f.slug,
          icon: 'engine',
          keywords: `flow ${f.name} ${f.slug}`,
          run: () => goto(appHref(`/engine/${f.flow_id}`))
        })
      )
    );
    appendDynamic(seq, 'agents', listAgents(''), (agents) =>
      agents.map(
        (a): Cmd => ({
          id: `agent:${a.name}`,
          section: 'Agents',
          label: a.name,
          icon: 'agents',
          keywords: `agent ${a.name}`,
          run: () => goto(appHref(`/agents/${encodeURIComponent(a.name)}`))
        })
      )
    );
    appendDynamic(seq, 'cases', listCases('', {}), (cases) =>
      cases.map(
        (c): Cmd => ({
          id: `case:${c.case_id}`,
          section: 'Cases',
          label: c.company_name,
          hint: c.status,
          icon: 'cases',
          keywords: `case ${c.company_name} ${c.status}`,
          run: () => goto(appHref(`/cases/${c.case_id}`))
        })
      )
    );
    appendDynamic(seq, 'policies', listPolicies(''), (policies) =>
      policies.map(
        (p): Cmd => ({
          id: `policy:${p.policy_id}`,
          section: 'Policies',
          label: p.name,
          hint: p.flow_slug,
          icon: 'clipboard',
          keywords: `policy ${p.name} ${p.flow_slug}`,
          run: () => goto(appHref('/policies'))
        })
      )
    );
    appendDynamic(seq, 'models', listModels(''), (models) =>
      models.map(
        (m): Cmd => ({
          id: `model:${m.name}`,
          section: 'Models',
          label: m.name,
          hint: m.kind,
          icon: 'gauge',
          keywords: `model ${m.name} ${m.kind}`,
          run: () => goto(appHref('/models'))
        })
      )
    );
    appendDynamic(seq, 'entities', listEntities(''), (entities) =>
      entities.map(
        (en): Cmd => ({
          id: `entity:${en.entity_type}/${en.entity_id}`,
          section: 'Context',
          label: `${en.entity_type} / ${en.entity_id}`,
          icon: 'database',
          keywords: `entity ${en.entity_type} ${en.entity_id}`,
          run: () =>
            goto(
              appHref(
                `/data/${encodeURIComponent(en.entity_type)}/${encodeURIComponent(en.entity_id)}`
              )
            )
        })
      )
    );
    if (remainingRefreshes > 0) {
      window.setTimeout(() => {
        if (seq === loadSeq && $paletteOpen) loadDynamic(remainingRefreshes - 1);
      }, 750);
    }
  }

  // Reset query/selection each time the palette opens, focus the input, and
  // refresh the entity index.
  $effect(() => {
    if ($paletteOpen) {
      query = '';
      selected = 0;
      dynamicBySource.clear();
      dynamic = [];
      restoreFocusEl = document.activeElement as HTMLElement | null;
      queueMicrotask(() => inputEl?.focus());
      if ($user) void loadDynamic();
    } else {
      // Drop the stale index so a reopen never flashes old/other-tenant entities,
      // and bump the token so an in-flight loadDynamic can't repopulate it.
      loadSeq++;
      dynamicBySource.clear();
      dynamic = [];
      restoreFocusEl?.focus();
      restoreFocusEl = null;
    }
  });
  // Typing always re-highlights the top match.
  $effect(() => {
    void query;
    selected = 0;
  });

  // Keep the highlighted row in view when arrowing past the visible window, so a
  // keyboard user never loses the selection off-screen. block:'nearest' is a no-op
  // when the row is already visible (e.g. on mouse hover), so it never fights the
  // pointer or jumps the list unnecessarily. Ids can contain '/' and ':', so look up
  // by getElementById (a CSS selector would need escaping).
  $effect(() => {
    const cur = filtered.at(selected);
    if (cur) document.getElementById(cur.id)?.scrollIntoView({ block: 'nearest' });
  });

  // Global shortcut: Cmd/Ctrl-K toggles the palette from anywhere.
  $effect(() => {
    function onKey(e: KeyboardEvent): void {
      if ((e.metaKey || e.ctrlKey) && (e.key === 'k' || e.key === 'K')) {
        e.preventDefault();
        togglePalette();
      }
    }
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  });

  function onInputKey(e: KeyboardEvent): void {
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      selected = filtered.length ? (selected + 1) % filtered.length : 0;
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      selected = filtered.length ? (selected - 1 + filtered.length) % filtered.length : 0;
    } else if (e.key === 'Enter') {
      e.preventDefault();
      run(filtered.at(selected));
    } else if (e.key === 'Escape') {
      e.preventDefault();
      closePalette();
    } else if (e.key === 'Tab') {
      // Pin focus to the search input so Tab can't escape the modal dialog.
      e.preventDefault();
    }
  }
</script>

{#if $paletteOpen}
  <div class="cp-root">
    <button class="cp-backdrop" aria-label="Close command palette" onclick={closePalette}></button>
    <div class="cp-panel" role="dialog" aria-modal="true" aria-label="Command palette">
      <div class="cp-search">
        <Icon name="search" size={16} />
        <input
          bind:this={inputEl}
          bind:value={query}
          onkeydown={onInputKey}
          role="combobox"
          aria-expanded="true"
          aria-controls="cp-list"
          aria-activedescendant={filtered.at(selected)?.id}
          aria-label="Search commands"
          placeholder="Search pages, flows, cases, agents, actions…"
          autocomplete="off"
          spellcheck="false"
        />
        <kbd>esc</kbd>
      </div>
      <div class="cp-list" id="cp-list" role="listbox" aria-label="Commands">
        {#each filtered as c, i (c.id)}
          <button
            id={c.id}
            class="cp-item"
            class:sel={i === selected}
            role="option"
            aria-label={c.label}
            aria-selected={i === selected}
            onmousemove={() => (selected = i)}
            onclick={() => run(c)}
          >
            <span class="cp-ic"><Icon name={c.icon} size={16} /></span>
            <span class="cp-text">
              <span class="cp-label">{c.label}</span>
              {#if c.hint}<span class="cp-hint">{c.hint}</span>{/if}
            </span>
            <span class="cp-sec">{c.section}</span>
          </button>
        {:else}
          <p class="cp-empty">No matches for “{query}”.</p>
        {/each}
        {#if loadError}
          <p class="cp-empty err" role="alert">Some sources failed to index — {loadError}</p>
        {/if}
      </div>
      <div class="cp-foot">
        <span><kbd>↑</kbd><kbd>↓</kbd> navigate</span>
        <span><kbd>↵</kbd> select</span>
        <span><kbd>esc</kbd> close</span>
      </div>
    </div>
  </div>
{/if}

<style>
  .cp-root {
    position: fixed;
    inset: 0;
    z-index: 200;
    display: flex;
    justify-content: center;
    align-items: flex-start;
    padding-top: 14vh;
  }
  .cp-backdrop {
    position: absolute;
    inset: 0;
    border: none;
    padding: 0;
    background: color-mix(in srgb, #000 45%, transparent);
    backdrop-filter: blur(2px);
    cursor: default;
  }
  .cp-panel {
    position: relative;
    width: min(40rem, 92vw);
    max-height: 64vh;
    display: flex;
    flex-direction: column;
    overflow: hidden;
    background: var(--surface);
    border: 1px solid var(--border-strong);
    border-radius: var(--radius);
    box-shadow:
      0 10px 40px rgba(0, 0, 0, 0.35),
      0 2px 8px rgba(0, 0, 0, 0.2);
    animation: cp-in 0.12s ease both;
  }
  @keyframes cp-in {
    from {
      opacity: 0;
      transform: translateY(-6px) scale(0.99);
    }
  }
  .cp-search {
    display: flex;
    align-items: center;
    gap: 0.6rem;
    padding: 0.7rem 0.9rem;
    border-bottom: 1px solid var(--border);
    color: var(--fg-subtle);
  }
  .cp-search input {
    flex: 1;
    border: none;
    background: none;
    padding: 0;
    font-size: 1rem;
    color: var(--fg);
  }
  .cp-search input:focus {
    outline: none;
  }
  kbd {
    font-family: var(--font-mono);
    font-size: 0.7rem;
    padding: 0.05rem 0.35rem;
    border: 1px solid var(--border-strong);
    border-bottom-width: 2px;
    border-radius: 4px;
    color: var(--fg-muted);
    background: var(--surface-2);
  }
  .cp-list {
    overflow-y: auto;
    padding: 0.35rem;
  }
  .cp-item {
    display: flex;
    align-items: center;
    gap: 0.7rem;
    width: 100%;
    padding: 0.5rem 0.6rem;
    border: none;
    border-radius: var(--radius-sm);
    background: none;
    text-align: left;
    color: var(--fg);
    cursor: pointer;
  }
  .cp-item.sel {
    background: color-mix(in srgb, var(--accent) 14%, transparent);
  }
  .cp-ic {
    display: inline-flex;
    color: var(--accent-ink);
    flex: none;
  }
  .cp-text {
    display: flex;
    flex-direction: column;
    margin-right: auto;
    min-width: 0;
  }
  .cp-label {
    font-weight: 500;
  }
  .cp-hint {
    font-size: 0.8rem;
    color: var(--fg-subtle);
  }
  .cp-sec {
    font-size: 0.72rem;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--fg-subtle);
    flex: none;
  }
  .cp-empty {
    padding: 1.4rem;
    text-align: center;
    color: var(--fg-subtle);
  }
  .cp-foot {
    display: flex;
    gap: 1rem;
    padding: 0.5rem 0.9rem;
    border-top: 1px solid var(--border);
    font-size: 0.78rem;
    color: var(--fg-subtle);
  }
  .cp-foot span {
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
  }
  @media (max-width: 560px) {
    .cp-foot {
      display: none;
    }
  }
</style>
