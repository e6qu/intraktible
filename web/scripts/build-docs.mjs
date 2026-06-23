// SPDX-License-Identifier: AGPL-3.0-or-later
// Renders the repository's Markdown docs into a small, themed, static docs site for
// GitHub Pages (served under /docs/). Single source of truth = the Markdown files;
// the rendered HTML is a build artifact (never committed). Run from web/:
//   npm run build:docs   →   writes web/build-docs/*.html
// The Pages workflow copies that into _site/docs/.
//
// Build-time script (not app code): fs paths are computed from a fixed doc list, so
// the non-literal-fs lint doesn't apply.
/* eslint-disable security/detect-non-literal-fs-filename */
import { marked } from 'marked';
import { readFileSync, writeFileSync, mkdirSync, copyFileSync, existsSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join, resolve } from 'node:path';

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '..', '..');
const OUT = resolve(ROOT, 'web', 'build-docs');

// The curated, public-facing doc set, in sidebar order. `slug` is the output page
// (README → index, the docs landing). Internal/dev-only docs are intentionally omitted.
const PAGES = [
  { file: 'README.md', slug: 'index', title: 'Overview', nav: 'Overview' },
  { file: 'docs/JOURNEYS.md', slug: 'journeys', title: 'User journeys', nav: 'User journeys' },
  {
    file: 'docs/API-FIRST.md',
    slug: 'api-first',
    title: 'API-first design',
    nav: 'API-first design'
  },
  {
    file: 'docs/EXPRESSIONS.md',
    slug: 'expressions',
    title: 'Expression language',
    nav: 'Expressions'
  },
  { file: 'AGENTS.md', slug: 'agents', title: 'AI agents', nav: 'AI agents' },
  {
    file: 'docs/ENTERPRISE.md',
    slug: 'enterprise',
    title: 'Enterprise & governance',
    nav: 'Enterprise & governance'
  },
  {
    file: 'docs/EXAMPLE.md',
    slug: 'example',
    title: 'End-to-end example',
    nav: 'End-to-end example'
  },
  { file: 'docs/LICENSING.md', slug: 'licensing', title: 'Licensing', nav: 'Licensing' }
];

// slugFor maps a Markdown filename to its output page slug, so cross-doc .md links
// resolve to the rendered .html (README → index). Unknown files keep their basename.
function slugFor(name) {
  const base = name.replace(/^.*\//, '').replace(/\.md$/i, '');
  if (/^readme$/i.test(base)) return 'index';
  const hit = PAGES.find((p) => p.file.replace(/^.*\//, '').replace(/\.md$/i, '') === base);
  return hit ? hit.slug : base.toLowerCase();
}

// rewriteLinks points cross-doc .md links at the rendered .html pages (preserving any
// #anchor); external/absolute/anchor links are left alone.
function rewriteLinks(html) {
  return html.replace(/href="([^"]+)"/g, (m, href) => {
    if (/^(https?:|mailto:|#|\/)/.test(href)) return m;
    // eslint-disable-next-line security/detect-unsafe-regex -- trusted build-time input
    const match = href.match(/([^/]+)\.md(#.*)?$/i);
    if (!match) return m;
    return `href="${slugFor(match[1])}.html${match[2] ?? ''}"`;
  });
}

function sidebar(activeSlug) {
  return PAGES.map(
    (p) =>
      `<a href="${p.slug}.html"${p.slug === activeSlug ? ' class="active" aria-current="page"' : ''}>${p.nav}</a>`
  ).join('\n          ');
}

function template(page, bodyHtml) {
  return `<!doctype html>
<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Generated from ${page.file} by web/scripts/build-docs.mjs — do not edit by hand. -->
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
    <title>intraktible · ${page.title}</title>
    <meta name="description" content="intraktible documentation — ${page.title}." />
    <style>${CSS}</style>
  </head>
  <body>
    <header class="topbar">
      <a class="brand" href="index.html">intraktible <span>docs</span></a>
      <nav class="top-links">
        <a href="../demo/">Demo</a>
        <a href="../">Home</a>
        <a href="https://github.com/e6qu/intraktible">Source ↗</a>
      </nav>
    </header>
    <div class="layout">
      <nav class="sidebar" aria-label="Documentation">
          ${sidebar(page.slug)}
      </nav>
      <main class="content">${bodyHtml}</main>
    </div>
  </body>
</html>
`;
}

const CSS = `
:root{color-scheme:dark}
*{box-sizing:border-box}
body{margin:0;font-family:ui-sans-serif,system-ui,-apple-system,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;
  background:#0c0f17;color:#cdd5e3;line-height:1.6}
a{color:#8fa6ff;text-decoration:none}a:hover{text-decoration:underline}
.topbar{display:flex;align-items:center;justify-content:space-between;gap:1rem;
  padding:0.7rem 1.25rem;border-bottom:1px solid #1d2433;background:#0e1320;position:sticky;top:0;z-index:5}
.brand{color:#e7ecf5;font-weight:650;font-size:1.05rem}
.brand span{color:#6c7588;font-weight:500}
.top-links{display:flex;gap:1rem;font-size:0.9rem}
.layout{display:grid;grid-template-columns:16rem 1fr;max-width:64rem;margin:0 auto;gap:2rem;padding:0 1.25rem}
.sidebar{display:flex;flex-direction:column;gap:0.15rem;padding:1.5rem 0;position:sticky;top:3.4rem;align-self:start;
  max-height:calc(100vh - 3.4rem);overflow:auto}
.sidebar a{padding:0.35rem 0.6rem;border-radius:6px;color:#aeb7c7;font-size:0.92rem}
.sidebar a:hover{background:#161c29;text-decoration:none}
.sidebar a.active{background:#1b2333;color:#fff;font-weight:550}
.content{padding:1.5rem 0 4rem;min-width:0}
.content h1{font-size:2rem;margin:0.2rem 0 1rem;color:#f3f6fb;line-height:1.2}
.content h2{font-size:1.35rem;margin:2rem 0 0.6rem;color:#eef2f8;border-bottom:1px solid #1d2433;padding-bottom:0.3rem}
.content h3{font-size:1.08rem;margin:1.5rem 0 0.4rem;color:#e7ecf5}
.content p,.content li{color:#cdd5e3}
.content code{background:#161c29;padding:0.1rem 0.35rem;border-radius:4px;font-size:0.88em;
  font-family:ui-monospace,SFMono-Regular,Menlo,monospace;color:#e3b7ff}
.content pre{background:#0e1320;border:1px solid #1d2433;border-radius:8px;padding:1rem;overflow:auto}
.content pre code{background:none;padding:0;color:#cdd5e3}
.content table{border-collapse:collapse;width:100%;margin:1rem 0;font-size:0.92rem}
.content th,.content td{border:1px solid #1d2433;padding:0.45rem 0.7rem;text-align:left}
.content th{background:#11151f;color:#e7ecf5}
.content blockquote{border-left:3px solid #3b5bdb;margin:1rem 0;padding:0.2rem 1rem;color:#aeb7c7}
.content a{font-weight:500}
.content hr{border:none;border-top:1px solid #1d2433;margin:2rem 0}
@media (max-width:720px){.layout{grid-template-columns:1fr;gap:0.5rem}
  .sidebar{position:static;max-height:none;flex-flow:row wrap;border-bottom:1px solid #1d2433;padding-bottom:0.8rem}}
`;

marked.setOptions({ gfm: true, breaks: false });
mkdirSync(OUT, { recursive: true });
let built = 0;
for (const page of PAGES) {
  const src = join(ROOT, page.file);
  if (!existsSync(src)) {
    console.warn(`skip (missing): ${page.file}`);
    continue;
  }
  const md = readFileSync(src, 'utf8');
  const html = rewriteLinks(marked.parse(md));
  writeFileSync(join(OUT, `${page.slug}.html`), template(page, html));
  built += 1;
}
// A .nojekyll so GitHub Pages serves the assets verbatim (defensive).
writeFileSync(join(OUT, '.nojekyll'), '');
if (existsSync(join(ROOT, 'web', 'static', 'favicon.png'))) {
  copyFileSync(join(ROOT, 'web', 'static', 'favicon.png'), join(OUT, 'favicon.png'));
}
console.log(`docs site: built ${built} page(s) → ${OUT}`);
