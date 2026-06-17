<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- A reusable discussion thread for any workflow subject (deployment request,
     decision, case…). Lists the conversation chronologically and posts new
     comments. Drop it anywhere with a subject type + id. -->
<script lang="ts">
  import { onMount } from 'svelte';
  import RelativeTime from '$lib/RelativeTime.svelte';
  import { listComments, postComment, type Comment } from '$lib/api';
  import { toast } from '$lib/toast';

  let {
    subjectType,
    subjectId,
    title = 'Discussion'
  }: { subjectType: string; subjectId: string; title?: string } = $props();

  const key = '';
  let comments = $state<Comment[]>([]);
  let draft = $state('');
  let busy = $state(false);
  let error = $state('');

  function msg(e: unknown): string {
    return e instanceof Error ? e.message : String(e);
  }
  async function load() {
    error = '';
    try {
      comments = await listComments(key, subjectType, subjectId);
    } catch (e) {
      error = msg(e);
    }
  }
  async function post() {
    if (!draft.trim()) return;
    busy = true;
    try {
      await postComment(key, subjectType, subjectId, draft.trim());
      draft = '';
      await load();
    } catch (e) {
      toast.error(msg(e));
    } finally {
      busy = false;
    }
  }
  onMount(load);
</script>

<div class="thread" data-testid="comment-thread">
  <span class="label">{title}{comments.length ? ` (${comments.length})` : ''}</span>
  {#if error}<p class="err">{error}</p>{/if}
  {#if comments.length > 0}
    <ul>
      {#each comments as c (c.comment_id)}
        <li>
          <span class="meta"><b>{c.author}</b> · <RelativeTime value={c.at} /></span>
          <span class="body">{c.body}</span>
        </li>
      {/each}
    </ul>
  {:else}
    <p class="muted empty">No comments yet — add an explanation below.</p>
  {/if}
  <div class="compose">
    <textarea
      bind:value={draft}
      rows="2"
      aria-label="new comment"
      placeholder="Add a comment or explanation…"
    ></textarea>
    <button onclick={post} disabled={busy || !draft.trim()} data-testid="post-comment">
      {busy ? 'Posting…' : 'Comment'}
    </button>
  </div>
</div>

<style>
  .thread {
    margin-top: 0.6rem;
    padding-top: 0.5rem;
    border-top: 1px dashed var(--border);
  }
  .label {
    font-size: 0.78rem;
    font-weight: 600;
    color: var(--fg-muted);
    text-transform: uppercase;
    letter-spacing: 0.03em;
  }
  ul {
    list-style: none;
    padding: 0;
    margin: 0.4rem 0;
    display: flex;
    flex-direction: column;
    gap: 0.4rem;
  }
  li {
    display: flex;
    flex-direction: column;
    gap: 0.1rem;
    padding: 0.4rem 0.5rem;
    background: var(--surface-2);
    border-radius: 8px;
  }
  .meta {
    font-size: 0.76rem;
    color: var(--fg-subtle);
  }
  .body {
    white-space: pre-wrap;
    font-size: 0.9rem;
  }
  .empty {
    font-size: 0.85rem;
    margin: 0.4rem 0;
  }
  .compose {
    display: flex;
    gap: 0.4rem;
    align-items: flex-start;
  }
  textarea {
    flex: 1;
    font: inherit;
    padding: 0.4rem 0.5rem;
    border: 1px solid var(--border);
    border-radius: 8px;
    background: var(--surface-1);
    color: var(--fg);
    resize: vertical;
  }
  .err {
    color: var(--danger);
    font-size: 0.85rem;
  }
  .muted {
    color: var(--fg-subtle);
  }
</style>
