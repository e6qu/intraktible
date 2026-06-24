// SPDX-License-Identifier: AGPL-3.0-or-later
// WCAG AA contrast audit — measured, not eyeballed. Prior UI/UX reviews judged contrast
// by looking at screenshots and repeatedly missed real failures on the small/secondary
// surfaces (muted labels, badge tints, the node-config panel). This test computes the
// actual contrast ratio for every text/background pair across representative routes in
// BOTH themes and fails if any normal text is < 4.5:1 (large text < 3:1), so low
// contrast can't silently regress.
import { test, expect } from '@playwright/test';

const ROUTES = ['/', '/engine', '/decisions/dec_1', '/cases/case_1', '/mrm', '/observability'];

// The audit runs in the browser: for each text-bearing element, resolve its foreground
// colour and the first opaque background up its ancestor chain, then return both so the
// node side can compute the WCAG ratio.
function collectPairs() {
  const parse = (c: string): [number, number, number, number] | null => {
    const m = c.match(/rgba?\(([^)]+)\)/);
    if (!m) return null;
    const p = m[1].split(',').map((s) => parseFloat(s));
    if (p.length >= 4 && p[3] === 0) return null;
    return [p[0], p[1], p[2], p[3] ?? 1];
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
    const failures: string[] = [];
    for (const route of ROUTES) {
      await page.goto(route);
      await page.evaluate((t) => document.documentElement.setAttribute('data-theme', t), theme);
      await page.waitForTimeout(150);
      const pairs = await page.evaluate(collectPairs);
      for (const p of pairs) {
        const cr = ratio(p.fg, p.bg);
        const min = p.large ? 3.0 : 4.5;
        if (cr < min - 0.05) {
          failures.push(
            `${route} [.${p.cls}] ${cr.toFixed(2)}:1 (need ${min}) fg(${p.fg}) bg(${p.bg}) "${p.txt}"`
          );
        }
      }
    }
    expect(failures, `WCAG AA contrast failures in ${theme}:\n${failures.join('\n')}`).toEqual([]);
  });
}
