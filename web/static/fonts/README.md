<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->

# Vendored fonts

Self-hosted so the embedded binary has **no runtime CDN dependency** (and works
fully offline / air-gapped). All three families are licensed under the
**SIL Open Font License 1.1** (OFL), which permits bundling and redistribution.

| File(s)             | Family        | Upstream                                  | License |
| ------------------- | ------------- | ----------------------------------------- | ------- |
| `plex-sans-*.woff2` | IBM Plex Sans | https://github.com/IBM/plex               | OFL-1.1 |
| `plex-mono-*.woff2` | IBM Plex Mono | https://github.com/IBM/plex               | OFL-1.1 |
| `fraunces-*.woff2`  | Fraunces      | https://github.com/undercasetype/Fraunces | OFL-1.1 |

These are `latin`-subset WOFF2 builds (from Fontsource) to keep the payload small.
The numeric suffix is the font weight (e.g. `plex-sans-600.woff2` = Semibold).

Wired via `@font-face` in `src/app.css` and selected per persona by the
`[data-persona]` token sets there.
