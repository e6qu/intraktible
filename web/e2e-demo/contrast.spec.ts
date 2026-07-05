// SPDX-License-Identifier: AGPL-3.0-or-later
// WCAG AA contrast audit — measured, not eyeballed. Prior UI/UX reviews judged contrast
// by looking at screenshots and repeatedly missed real failures on the small/secondary
// surfaces (muted labels, badge tints, the node-config panel). This test computes the
// actual contrast ratio for every text/background pair across representative routes in
// BOTH themes and fails if any normal text is < 4.5:1 (large text < 3:1), so low
// contrast can't silently regress.
import { test, expect, type Page } from '@playwright/test';

// Routes are RELATIVE so they resolve under the baseURL's /intraktible/demo/ base
// ('' is the home dashboard) — a leading-slash route escapes the base and lands on
// vite preview's "did you mean" hint page instead of the app.
const LIST_ROUTES = [
  '',
  'engine',
  'mrm',
  'observability',
  'preapprovals', // the .link.danger Revoke button regressed here, unmeasured
  'agents',
  'policies'
];

// The detail routes (builder canvas — svelte-flow edge labels live there — plus a
// decision trace and a case) carry ids the real backend minted for the seed, so
// they are resolved through the API once per test rather than hardcoded.
async function detailRoutes(page: Page): Promise<string[]> {
  await page.goto('');
  await page.waitForFunction(() => '__demo' in window);
  return await page.evaluate(async () => {
    const j = (r: Response) => r.json();
    const flows = (await fetch('/v1/flows').then(j)).flows as { slug: string; flow_id: string }[];
    const credit = flows.find((f) => f.slug === 'credit-decision');
    if (!credit) throw new Error('credit-decision missing from the seed');
    const decisions = (await fetch('/v1/decisions?limit=1').then(j)).decisions as {
      decision_id: string;
    }[];
    const cases = (await fetch('/v1/cases').then(j)).cases as { case_id: string }[];
    return [
      `engine/${credit.flow_id}`,
      `decisions/${decisions[0].decision_id}`,
      `cases/${cases[0].case_id}`
    ];
  });
}

// Persona only swaps the accent tokens (--accent / --accent-ink / --link), which colour
// the active nav + links — so persona-specific contrast bugs (e.g. an active nav item
// painted with a low-contrast raw --accent) only surface if every persona is checked,
// not just the default. The original audit missed exactly this.
const PERSONAS = [
  'builder',
  'operator',
  'showcase',
  'developer',
  'manager',
  'product',
  'evaluator'
];

// The audit runs in the browser: for each text-bearing element, resolve its foreground
// colour and the first opaque background up its ancestor chain, then return both so the
// node side can compute the WCAG ratio.
function collectPairs() {
  const nums = (s: string) =>
    s
      .split(/[,\s/]+/)
      .map(parseFloat)
      .filter((n) => !Number.isNaN(n));
  const parse = (c: string): [number, number, number, number] | null => {
    const rgb = c.match(/rgba?\(([^)]+)\)/);
    if (rgb) {
      const p = nums(rgb[1]);
      if (p.length >= 4 && p[3] === 0) return null;
      return [p[0], p[1], p[2], p[3] ?? 1];
    }
    // Chromium serializes color-mix() / relative colors as `color(srgb r g b / a)` with
    // 0..1 channels — the original rgba-only regex was BLIND to these, so every
    // color-mix-based fg/bg (the node strip labels, heat badges, …) went unmeasured.
    const srgb = c.match(/color\(srgb\s+([^)]+)\)/);
    if (srgb) {
      const p = nums(srgb[1]);
      if (p.length >= 4 && p[3] === 0) return null;
      return [p[0] * 255, p[1] * 255, p[2] * 255, p[3] ?? 1];
    }
    return null;
  };
  const bgOf = (el: Element): [number, number, number] => {
    let n: Element | null = el;
    while (n) {
      const c = parse(getComputedStyle(n).backgroundColor);
      if (c && c[3] > 0.5) return [c[0], c[1], c[2]];
      n = n.parentElement;
    }
    return [255, 255, 255];
  };
  const out: {
    fg: [number, number, number];
    bg: [number, number, number];
    large: boolean;
    cls: string;
    txt: string;
  }[] = [];
  for (const el of Array.from(document.querySelectorAll('*'))) {
    const txt = Array.from(el.childNodes)
      .filter((n) => n.nodeType === 3)
      .map((n) => (n.textContent ?? '').trim())
      .join('');
    if (txt.length < 2) continue;
    const r = el.getBoundingClientRect();
    if (r.width < 1 || r.height < 1) continue;
    const s = getComputedStyle(el);
    if (s.visibility === 'hidden' || s.opacity === '0') continue;
    const fg = parse(s.color);
    if (!fg) continue;
    const size = parseFloat(s.fontSize);
    const bold = parseInt(s.fontWeight) >= 600;
    const large = size >= 24 || (size >= 18.66 && bold);
    out.push({
      fg: [fg[0], fg[1], fg[2]],
      bg: bgOf(el),
      large,
      cls: (el.className || el.tagName).toString().split(' ')[0].slice(0, 24),
      txt: txt.slice(0, 24)
    });
  }
  return out;
}

function ratio(a: number[], b: number[]): number {
  const lum = (c: number[]): number => {
    const f = (x: number): number => {
      x /= 255;
      return x <= 0.03928 ? x / 12.92 : Math.pow((x + 0.055) / 1.055, 2.4);
    };
    return 0.2126 * f(c[0]) + 0.7152 * f(c[1]) + 0.0722 * f(c[2]);
  };
  const l1 = lum(a);
  const l2 = lum(b);
  return (Math.max(l1, l2) + 0.05) / (Math.min(l1, l2) + 0.05);
}

for (const theme of ['light', 'dark']) {
  test(`text meets WCAG AA contrast in ${theme} mode`, async ({ page }) => {
    // Every fresh render boots the embedded wasm backend, so the audit trims the
    // persona×route cross-product without losing either dimension: persona only
    // swaps the accent tokens (visible on every page's nav/links/chips), so each
    // persona is audited on the home dashboard, and the full route sweep — where
    // the route-specific surfaces live — runs under one persona per theme.
    test.setTimeout(600_000);
    const ROUTES = [...LIST_ROUTES, ...(await detailRoutes(page))];
    const failures: string[] = [];
    const audit = async (persona: string, route: string): Promise<void> => {
      await page.goto(route);
      // Wait for the app to actually render: a fresh load boots the embedded
      // backend behind a splash, so sampling at first paint would audit the
      // splash, not the page.
      await expect(page.locator('h1, h2').first()).toBeVisible();
      await page.evaluate(() => new Promise(requestAnimationFrame));
      const pairs = await page.evaluate(collectPairs);
      for (const p of pairs) {
        const cr = ratio(p.fg, p.bg);
        const min = p.large ? 3.0 : 4.5;
        if (cr < min - 0.05) {
          failures.push(
            `[${persona}] /${route} [.${p.cls}] ${cr.toFixed(2)}:1 (need ${min}) fg(${p.fg}) bg(${p.bg}) "${p.txt}"`
          );
        }
      }
    };
    // Persist the theme+persona BEFORE each navigation (init scripts run ahead
    // of the app), so the no-flash boot script paints the right theme from the
    // FIRST frame. Flipping the attribute on a live page raced the CSS color
    // transitions — the sampler read half-blended colors and reported phantom
    // contrast failures. Scripts accumulate; the one added last wins.
    const setChrome = (persona: string) =>
      page.addInitScript(
        ([t, p]) => {
          localStorage.setItem('intraktible-theme', t);
          localStorage.setItem('intraktible-persona', p);
        },
        [theme, persona]
      );
    for (const persona of PERSONAS) {
      await setChrome(persona);
      await audit(persona, '');
    }
    const sweeper = PERSONAS[0];
    await setChrome(sweeper);
    for (const route of ROUTES) {
      if (route !== '') await audit(sweeper, route);
    }
    expect(failures, `WCAG AA contrast failures in ${theme}:\n${failures.join('\n')}`).toEqual([]);
  });
}
