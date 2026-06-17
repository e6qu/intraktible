<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Inline-SVG icon set (stroke = currentColor, so it inherits text colour and
  themes automatically). Hand-drawn 24×24 paths — no external icon dependency.
-->
<script lang="ts" module>
  // Each icon is a list of <path>/<shape> fragments rendered inside a 24×24 svg.
  // Stored in Maps (not plain objects) so name lookups don't trip the
  // object-injection lint. Fragments are static literals — never user input.
  const ICONS = new Map<string, string>([
    ['logo', '<path d="M4 7l8-4 8 4-8 4-8-4z"/><path d="M4 12l8 4 8-4"/><path d="M4 17l8 4 8-4"/>'],
    [
      'engine',
      '<circle cx="12" cy="12" r="3"/><path d="M12 2v3M12 19v3M2 12h3M19 12h3M5 5l2 2M17 17l2 2M19 5l-2 2M7 17l-2 2"/>'
    ],
    [
      'cases',
      '<rect x="3" y="7" width="18" height="13" rx="2"/><path d="M8 7V5a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/>'
    ],
    [
      'agents',
      '<rect x="5" y="8" width="14" height="11" rx="2"/><path d="M12 8V4M9 4h6"/><circle cx="9" cy="13" r="1"/><circle cx="15" cy="13" r="1"/>'
    ],
    ['home', '<path d="M3 11l9-8 9 8"/><path d="M5 10v10h14V10"/>'],
    ['plus', '<path d="M12 5v14M5 12h14"/>'],
    ['trash', '<path d="M3 6h18M8 6V4h8v2M6 6l1 14h10l1-14"/>'],
    ['play', '<path d="M7 4l13 8-13 8V4z"/>'],
    ['check', '<path d="M5 12l5 5L20 6"/>'],
    [
      'bell',
      '<path d="M18 8a6 6 0 1 0-12 0c0 7-3 9-3 9h18s-3-2-3-9"/><path d="M13.7 21a2 2 0 0 1-3.4 0"/>'
    ],
    [
      'copy',
      '<rect x="9" y="9" width="11" height="11" rx="2"/><path d="M5 15V5a2 2 0 0 1 2-2h8"/>'
    ],
    ['download', '<path d="M12 3v12M7 11l5 5 5-5M5 21h14"/>'],
    [
      'diagram',
      '<rect x="3" y="3" width="7" height="7" rx="1"/><rect x="14" y="14" width="7" height="7" rx="1"/><path d="M10 6h4a2 2 0 0 1 2 2v6"/>'
    ],
    [
      'signout',
      '<path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4"/><path d="M16 17l5-5-5-5M21 12H9"/>'
    ],
    ['reload', '<path d="M21 12a9 9 0 1 1-3-6.7L21 8M21 3v5h-5"/>'],
    ['shield', '<path d="M12 3l8 3v6c0 5-3.4 8-8 9-4.6-1-8-4-8-9V6z"/><path d="M9 12l2 2 4-4"/>'],
    [
      'database',
      '<ellipse cx="12" cy="5" rx="8" ry="3"/><path d="M4 5v6c0 1.7 3.6 3 8 3s8-1.3 8-3V5M4 11v6c0 1.7 3.6 3 8 3s8-1.3 8-3v-6"/>'
    ],
    [
      'sun',
      '<circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M2 12h2M20 12h2M5 5l1.5 1.5M17.5 17.5L19 19M19 5l-1.5 1.5M6.5 17.5L5 19"/>'
    ],
    ['moon', '<path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z"/>'],
    ['input', '<circle cx="12" cy="12" r="7"/><path d="M9 12h6M13 9l3 3-3 3"/>'],
    ['output', '<circle cx="12" cy="12" r="7"/><path d="M8 12h6M12 9l3 3-3 3"/>'],
    ['split', '<path d="M12 3l9 9-9 9-9-9 9-9z"/>'],
    ['rule', '<rect x="4" y="4" width="16" height="16" rx="2"/><path d="M8 9h8M8 13h8M8 17h5"/>'],
    [
      'decision_table',
      '<rect x="3" y="4" width="18" height="16" rx="2"/><path d="M3 9h18M3 14h18M9 4v16"/>'
    ],
    ['scorecard', '<path d="M12 3l2.5 5.5L20 9l-4 4 1 6-5-3-5 3 1-6-4-4 5.5-.5L12 3z"/>'],
    [
      '2d_matrix',
      '<rect x="3" y="3" width="18" height="18" rx="2"/><path d="M3 9h18M3 15h18M9 3v18M15 3v18"/>'
    ],
    ['code', '<path d="M8 8l-4 4 4 4M16 8l4 4-4 4M13 5l-2 14"/>'],
    [
      'ai',
      '<path d="M12 2a4 4 0 0 1 4 4 4 4 0 0 1 0 8 4 4 0 0 1-8 0 4 4 0 0 1 0-8 4 4 0 0 1 4-4z"/><circle cx="12" cy="10" r="1.5"/>'
    ],
    [
      'connect',
      '<rect x="3" y="4" width="18" height="6" rx="2"/><rect x="3" y="14" width="18" height="6" rx="2"/><path d="M12 10v4"/>'
    ],
    ['manual_review', '<circle cx="12" cy="8" r="3"/><path d="M5 21a7 7 0 0 1 14 0"/>'],
    ['assignment', '<path d="M5 12h14M12 5v14"/>'],
    ['reason', '<path d="M6 3h9l4 4v14H6z"/><path d="M14 3v5h5M9 13h6M9 17h4"/>'],
    ['user', '<circle cx="12" cy="8" r="3.5"/><path d="M5 20a7 7 0 0 1 14 0"/>'],
    ['chevron-down', '<path d="M6 9l6 6 6-6"/>'],
    ['search', '<circle cx="11" cy="11" r="7"/><path d="M21 21l-4.3-4.3"/>'],
    [
      'clipboard',
      '<rect x="8" y="4" width="8" height="4" rx="1"/><path d="M9 4H6a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V6a2 2 0 0 0-2-2h-3"/>'
    ],
    // persona "portraits" — a distinct silhouette per viewer type
    [
      'builder',
      '<circle cx="12" cy="7.5" r="3"/><path d="M5.5 20a6.5 6.5 0 0 1 13 0"/><path d="M8.5 11l-2 2 2 2M15.5 11l2 2-2 2"/>'
    ],
    [
      'operator',
      '<circle cx="12" cy="7.5" r="3"/><path d="M5.5 20a6.5 6.5 0 0 1 13 0"/><path d="M8 8.5a4 4 0 0 1 8 0M7.5 8.5v2M16.5 8.5v2"/>'
    ],
    [
      'showcase',
      '<circle cx="12" cy="7.5" r="3"/><path d="M5.5 20a6.5 6.5 0 0 1 13 0"/><path d="M12 1.6l.8 1.7 1.8.2-1.3 1.3.3 1.8-1.6-.9-1.6.9.3-1.8-1.3-1.3 1.8-.2z"/>'
    ]
  ]);

  function pathFor(name: string): string {
    return ICONS.get(name) ?? '<circle cx="12" cy="12" r="2"/>';
  }
</script>

<script lang="ts">
  let {
    name,
    size = 18,
    stroke = 1.8
  }: { name: string; size?: number; stroke?: number } = $props();
</script>

<svg
  width={size}
  height={size}
  viewBox="0 0 24 24"
  fill="none"
  stroke="currentColor"
  stroke-width={stroke}
  stroke-linecap="round"
  stroke-linejoin="round"
  aria-hidden="true"
>
  <!-- eslint-disable-next-line svelte/no-at-html-tags -->
  {@html pathFor(name)}
</svg>
