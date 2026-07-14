<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Build provenance shown at the bottom of every page: the short git SHA and the
     build/deploy time in UTC, injected at build time by Vite (see vite.config.ts). -->
<script lang="ts">
  const sha = __APP_GIT_SHA__;

  function fmtUtc(iso: string): string {
    const d = new Date(iso);
    if (Number.isNaN(d.getTime())) return iso;
    const p = (n: number) => String(n).padStart(2, '0');
    return `${d.getUTCFullYear()}-${p(d.getUTCMonth() + 1)}-${p(d.getUTCDate())} ${p(d.getUTCHours())}:${p(d.getUTCMinutes())} UTC`;
  }

  const built = fmtUtc(__APP_BUILD_TIME__);
</script>

<footer class="build-info" aria-label="build information">
  <span class="mono">{sha}</span>
  <span class="sep" aria-hidden="true">·</span>
  <span class="mono">{built}</span>
</footer>

<style>
  .build-info {
    padding: 0.5rem 0.75rem;
    text-align: center;
    font-size: 0.7rem;
    line-height: 1;
    color: var(--fg-subtle);
    border-top: 1px solid var(--border);
  }
  .mono {
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    font-variant-numeric: tabular-nums;
    user-select: text;
  }
  .sep {
    margin: 0 0.5rem;
  }
</style>
