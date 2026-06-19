<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- A small breadcrumb for detail pages: a back-link to the section list plus the
     current item, so a deep page (a decision, a case, an agent) always shows where
     it sits and offers one click back up. -->
<script lang="ts">
  import Icon from '$lib/Icon.svelte';

  let {
    sectionHref,
    sectionLabel,
    current
  }: { sectionHref: string; sectionLabel: string; current: string } = $props();
</script>

<nav class="crumbs" aria-label="Breadcrumb">
  <a href={sectionHref}><Icon name="chevron-down" size={14} /> {sectionLabel}</a>
  <span class="sep" aria-hidden="true">/</span>
  <span class="current" aria-current="page">{current}</span>
</nav>

<style>
  .crumbs {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    margin-bottom: 0.6rem;
    font-size: 0.85rem;
    color: var(--fg-subtle);
  }
  .crumbs a {
    display: inline-flex;
    align-items: center;
    gap: 0.2rem;
    min-height: 24px;
    color: var(--fg-muted);
  }
  /* The back-chevron points left. */
  .crumbs a :global(svg) {
    transform: rotate(90deg);
  }
  .crumbs a:hover {
    color: var(--fg);
    text-decoration: none;
  }
  .sep {
    color: var(--border-strong, var(--border));
  }
  .current {
    color: var(--fg);
    font-weight: 550;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    max-width: 24rem;
  }
</style>
