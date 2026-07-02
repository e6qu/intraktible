<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- A reusable discussion thread for any workflow subject (deployment request,
     decision, flow, policy…). Lists the conversation chronologically with one
     level of threaded replies, and posts new comments. Drop it anywhere with a
     subject type + id. -->
<script lang="ts">
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
  let replyTo = $state(''); // comment_id being replied to ('' = top-level)
  let busy = $state(false);
  let error = $state('');
  let loading = $state(true);

  // Top-level comments (no parent) and a parent_id → replies index. Replies are
  // only offered on top-level comments, so a single level of nesting is exact.
  const topLevel = $derived(comments.filter((c) => !c.parent_id));
  function repliesTo(id: string): Comment[] {
    return comments.filter((c) => c.parent_id === id);
  }

  function msg(e: unknown): string {
    return e instanceof Error ? e.message : String(e);
  }
  // A monotonically increasing token so only the newest load may write state:
  // subject identity alone can't order two loads for the SAME subject (e.g. the
  // pre-post list resolving after the post-inclusive reload and dropping the new
  // comment), and it still covers a subject swap mid-flight.
  let loadSeq = 0;
  async function load() {
    const seq = ++loadSeq;
    error = '';
    try {
      const got = await listComments(key, subjectType, subjectId);
      if (seq !== loadSeq) return; // a newer load superseded this one
      comments = got;
    } catch (e) {
      if (seq === loadSeq) error = msg(e);
    } finally {
      if (seq === loadSeq) loading = false;
    }
  }
  async function post() {
    if (!draft.trim()) return;
    busy = true;
    try {
      await postComment(key, subjectType, subjectId, draft.trim(), replyTo);
      draft = '';
      replyTo = '';
      await load();
    } catch (e) {
      toast.error(msg(e));
    } finally {
      busy = false;
    }
  }
  // Reload whenever the subject changes, so a caller that swaps subjectId in place
  // (without a keyed remount) shows the right thread instead of the first one
  // forever. Reading subjectType/subjectId registers them as effect dependencies.
  $effect(() => {
    void subjectType;
    void subjectId;
    loading = true;
    void load();
  });
</script>

<div class="thread" data-testid="comment-thread">
  <span class="label">{title}{comments.length ? ` (${comments.length})` : ''}</span>
  {#if error}<p class="err">{error}</p>{/if}
  {#if loading}
    <p class="muted empty">Loading…</p>
  {:else if topLevel.length > 0}
    <ul>
      {#each topLevel as c (c.comment_id)}
        <li>
          <span class="meta"><b>{c.author}</b> · <RelativeTime value={c.at} /></span>
          <span class="body">{c.body}</span>
          <button class="reply-btn" onclick={() => (replyTo = c.comment_id)}>Reply</button>
          {#each repliesTo(c.comment_id) as rep (rep.comment_id)}
            <div class="reply">
              <span class="meta"><b>{rep.author}</b> · <RelativeTime value={rep.at} /></span>
              <span class="body">{rep.body}</span>
            </div>
          {/each}
        </li>
      {/each}
    </ul>
  {:else}
    <p class="muted empty">No comments yet — add an explanation below.</p>
  {/if}
  <div class="compose">
    <div class="grow">
      {#if replyTo}
        <span class="replying"
          >Replying to a comment <button class="link" onclick={() => (replyTo = '')}>cancel</button
          ></span
        >
      {/if}
      <textarea
        bind:value={draft}
        rows="2"
        aria-label="new comment"
        placeholder={replyTo ? 'Write a reply…' : 'Add a comment or explanation…'}
      ></textarea>
    </div>
    <button onclick={post} disabled={busy || !draft.trim()} data-testid="post-comment">
      {busy ? 'Posting…' : replyTo ? 'Reply' : 'Comment'}
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
  .reply-btn {
    align-self: flex-start;
    margin-top: 0.15rem;
    background: none;
    border: none;
    padding: 0;
    font: inherit;
    font-size: 0.78rem;
    color: var(--accent-ink);
    cursor: pointer;
  }
  .reply {
    margin: 0.3rem 0 0 0.9rem;
    padding: 0.35rem 0.5rem;
    border-left: 2px solid var(--border);
    display: flex;
    flex-direction: column;
    gap: 0.1rem;
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
  .compose .grow {
    flex: 1;
    display: flex;
    flex-direction: column;
    gap: 0.2rem;
  }
  .replying {
    font-size: 0.76rem;
    color: var(--fg-subtle);
  }
  textarea {
    width: 100%;
    font: inherit;
    padding: 0.4rem 0.5rem;
    border: 1px solid var(--border);
    border-radius: 8px;
    background: var(--surface-1);
    color: var(--fg);
    resize: vertical;
    box-sizing: border-box;
  }
  .link {
    background: none;
    border: none;
    padding: 0;
    font: inherit;
    color: var(--accent-ink);
    cursor: pointer;
  }
  .err {
    color: var(--danger);
    font-size: 0.85rem;
  }
  .muted {
    color: var(--fg-subtle);
  }
</style>
