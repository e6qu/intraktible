<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Header notifications bell: a per-user inbox of human-review tasks (assigned / due
     soon / overdue), @-mentions, and deploy/monitor alerts. Shows an unread badge and a
     dropdown; clicking an item opens its subject (case/decision/flow) and marks it read. -->
<script lang="ts">
  import { onMount } from 'svelte';
  import { goto } from '$app/navigation';
  import Icon from '$lib/Icon.svelte';
  import RelativeTime from '$lib/RelativeTime.svelte';
  import { appHref } from '$lib/paths';
  import { listNotifications, markNotificationRead, type Notification } from '$lib/api';
  import { toast } from '$lib/toast';

  const key = '';
  let items = $state<Notification[]>([]);
  // A failed fetch must not read as an empty inbox ("all caught up"); track it so the
  // panel shows the failure and a retry instead.
  let loadError = $state('');
  const unread = $derived(items.filter((n) => !n.read).length);

  function msg(e: unknown): string {
    return e instanceof Error ? e.message : String(e);
  }
  async function load() {
    try {
      items = await listNotifications(key);
      loadError = '';
    } catch (e) {
      loadError = msg(e);
    }
  }
  async function markRead(n: Notification) {
    if (n.read) return;
    try {
      await markNotificationRead(key, n.notification_id);
      await load();
    } catch (e) {
      // A silently-failed mark-read leaves a stale unread badge — surface it.
      toast.error(msg(e));
    }
  }
  // Mark every unread item read in one action, reporting any that failed rather than
  // leaving the badge silently stale.
  async function markAllRead() {
    const pending = items.filter((n) => !n.read);
    if (pending.length === 0) return;
    const results = await Promise.allSettled(
      pending.map((n) => markNotificationRead(key, n.notification_id))
    );
    const failed = results.filter((r) => r.status === 'rejected').length;
    if (failed > 0) toast.error(`Couldn't mark ${failed} of ${pending.length} notifications read`);
    await load();
  }
  // The route a notification's subject lives on, so a reviewer clicking a task reminder
  // or an @-mention lands on the case/decision/flow/agent/entity/model instead of just
  // marking it read in place.
  function subjectHref(n: Notification): string | undefined {
    if (!n.subject_id) return undefined;
    if (n.subject_type === 'case') return appHref(`/cases/${n.subject_id}`);
    if (n.subject_type === 'decision') return appHref(`/decisions/${n.subject_id}`);
    if (n.subject_type === 'flow') return appHref(`/engine/${n.subject_id}`);
    if (n.subject_type === 'agent') return appHref(`/agents/${encodeURIComponent(n.subject_id)}`);
    if (n.subject_type === 'model') return appHref('/models');
    if (n.subject_type === 'entity') {
      // The entity subject is "<type>/<id>" (one wire segment); split on the first
      // slash so each half is escaped into the /data/<type>/<id> route.
      const slash = n.subject_id.indexOf('/');
      if (slash < 0) return undefined;
      const t = n.subject_id.slice(0, slash);
      const eid = n.subject_id.slice(slash + 1);
      return appHref(`/data/${encodeURIComponent(t)}/${encodeURIComponent(eid)}`);
    }
    return undefined;
  }
  // Open the item's subject and mark it read (the dropdown closes via the navigation).
  function open(n: Notification) {
    void markRead(n);
    const href = subjectHref(n);
    if (href) {
      if (el) el.open = false;
      void goto(href);
    }
  }
  // Escape closes the dropdown and returns focus to the bell (a window listener,
  // so it works while focus is still on the summary), and arrow keys rove focus
  // across the items — the ARIA menu pattern the persona menu uses. The handler
  // acts only while the dropdown is open and the event originates inside it.
  function onKeydown(e: KeyboardEvent) {
    if (!el?.open || !el.contains(e.target as Node)) return;
    if (e.key === 'Escape') {
      e.preventDefault();
      el.open = false;
      summaryEl?.focus();
      return;
    }
    const opts = Array.from(el.querySelectorAll<HTMLButtonElement>('.item'));
    if (opts.length === 0) return;
    const i = opts.indexOf(document.activeElement as HTMLButtonElement);
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      opts[(i + 1) % opts.length]?.focus();
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      (i <= 0 ? opts.at(-1) : opts[i - 1])?.focus();
    } else if (e.key === 'Home') {
      e.preventDefault();
      opts[0]?.focus();
    } else if (e.key === 'End') {
      e.preventDefault();
      opts.at(-1)?.focus();
    }
  }
  let el = $state<HTMLDetailsElement | null>(null);
  let summaryEl = $state<HTMLElement | null>(null);
  // Close on an outside click (details has no native dismiss), matching the
  // persona menu in +layout.
  $effect(() => {
    function onDocClick(e: MouseEvent): void {
      if (el?.open && !el.contains(e.target as Node)) el.open = false;
    }
    document.addEventListener('click', onDocClick);
    return () => document.removeEventListener('click', onDocClick);
  });
  onMount(load);
</script>

<svelte:window onkeydown={onKeydown} />

<details
  bind:this={el}
  class="bell"
  data-testid="notifications-bell"
  ontoggle={(e) => e.currentTarget.open && load()}
>
  <summary
    bind:this={summaryEl}
    aria-haspopup="true"
    aria-label={`Notifications${unread ? ` (${unread} unread)` : ''}`}
    title="Notifications"
  >
    <Icon name="bell" size={16} />
    {#if unread > 0}<span class="badge" data-testid="notif-badge">{unread}</span>{/if}
  </summary>
  <!-- A plain focusable dropdown, not an ARIA menu: the panel also holds a Retry
       button (in the error state) that the menu roving-focus pattern couldn't reach,
       so keeping menu/menuitem roles here would hide it from keyboard users. Arrow
       keys still rove across the items for convenience. -->
  <div class="panel" tabindex="-1">
    <div class="head">
      <p class="head-title">Notifications</p>
      {#if !loadError && unread > 0}
        <button class="markall" onclick={markAllRead} data-testid="notif-mark-all"
          >Mark all read</button
        >
      {/if}
    </div>
    {#if loadError}
      <p class="empty err" data-testid="notif-error">
        Couldn't load notifications — {loadError}
        <button class="retry" onclick={() => load()}>Retry</button>
      </p>
    {:else if items.length === 0}
      <p class="empty">You're all caught up.</p>
    {:else}
      <ul>
        {#each items as n (n.notification_id)}
          <li>
            <button class="item" class:unread={!n.read} onclick={() => open(n)}>
              <span class="meta">
                {#if n.kind === 'task'}<b>Review task</b>
                {:else if n.kind === 'mention'}<b>{n.author}</b> mentioned you on a {n.subject_type.replace(
                    /_/g,
                    ' '
                  )}
                {:else}<b>{n.author}</b> · {n.kind.replace(/_/g, ' ')}
                {/if}
                · <RelativeTime value={n.created_at} /></span
              >
              <span class="snip">{n.snippet}</span>
            </button>
          </li>
        {/each}
      </ul>
    {/if}
  </div>
</details>

<style>
  .bell {
    position: relative;
  }
  summary {
    list-style: none;
    cursor: pointer;
    display: inline-flex;
    align-items: center;
    position: relative;
    padding: 0.3rem;
    border-radius: 8px;
    color: var(--fg-muted);
  }
  summary::-webkit-details-marker {
    display: none;
  }
  summary:hover {
    background: var(--surface-2);
  }
  .badge {
    position: absolute;
    top: -2px;
    right: -2px;
    min-width: 1rem;
    height: 1rem;
    padding: 0 0.25rem;
    border-radius: 999px;
    background: var(--danger);
    color: var(--on-danger);
    font-size: 0.66rem;
    font-weight: 700;
    display: inline-flex;
    align-items: center;
    justify-content: center;
  }
  .panel {
    position: absolute;
    right: 0;
    top: calc(100% + 0.4rem);
    width: 22rem;
    max-width: 90vw;
    max-height: 24rem;
    overflow-y: auto;
    background: var(--surface-1);
    border: 1px solid var(--border);
    border-radius: 10px;
    box-shadow: var(--shadow, 0 8px 30px rgb(0 0 0 / 0.18));
    padding: 0.4rem;
    z-index: 50;
  }
  .head {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 0.5rem;
    margin: 0.2rem 0.4rem 0.4rem;
  }
  .head-title {
    margin: 0;
    font-size: 0.78rem;
    font-weight: 600;
    color: var(--fg-subtle);
    text-transform: uppercase;
    letter-spacing: 0.03em;
  }
  .markall {
    background: none;
    border: none;
    color: var(--accent-ink);
    cursor: pointer;
    padding: 0;
    font: inherit;
    font-size: 0.76rem;
  }
  .markall:hover {
    text-decoration: underline;
  }
  .empty {
    margin: 0.6rem 0.4rem;
    color: var(--fg-subtle);
    font-size: 0.88rem;
  }
  .empty.err {
    color: var(--danger);
  }
  .retry {
    background: none;
    border: none;
    color: var(--accent);
    cursor: pointer;
    padding: 0.2rem;
    font: inherit;
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .item {
    width: 100%;
    text-align: left;
    background: none;
    border: none;
    border-radius: 8px;
    padding: 0.45rem 0.5rem;
    cursor: pointer;
    display: flex;
    flex-direction: column;
    gap: 0.15rem;
    color: var(--fg);
  }
  .item:hover {
    background: var(--surface-2);
  }
  .item.unread {
    background: color-mix(in srgb, var(--accent) 8%, transparent);
  }
  .meta {
    font-size: 0.78rem;
    color: var(--fg-subtle);
  }
  .snip {
    /* Block-level, or the nowrap/ellipsis pair is inert on an inline span and a
       long snippet overflows BOTH panel edges (leading words clipped). */
    display: block;
    max-width: 100%;
    font-size: 0.88rem;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
</style>
