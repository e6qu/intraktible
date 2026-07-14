// SPDX-License-Identifier: AGPL-3.0-or-later
// Post-deploy smoke against the LIVE GitHub Pages demo (not a local build). CI's demo
// suite runs a locally-served build with a fresh browser, so it can't catch a broken
// DEPLOY or a returning-visitor failure. This loads the real URL twice — a fresh visitor
// and a returning visitor whose persisted delta is incompatible — and fails (non-zero)
// if the demo doesn't boot to a working app in either case.
import { chromium } from '@playwright/test';

const url = process.argv[2];
if (!url) {
  console.error('usage: node scripts/live-demo-smoke.mjs <demo-url>');
  process.exit(2);
}

const browser = await chromium.launch();

// Loads url (optionally poisoning localStorage first) and resolves 'BOOTED' | 'FAILED'
// with the on-page error, waiting up to 2 min for the ~11 MB engine.
async function attempt(poison) {
  const page = await browser.newPage();
  if (poison) {
    await page.addInitScript(() => {
      localStorage.setItem(
        'intraktible-event-delta',
        '[{"stale":"incompatible with the current seed"}]'
      );
    });
  }
  await page.goto(url, { waitUntil: 'domcontentloaded', timeout: 60000 });
  const outcome = await Promise.race([
    page
      .waitForFunction(() => '__demo' in window, { timeout: 120000 })
      .then(() => 'BOOTED')
      .catch(() => null),
    page
      .waitForSelector('.err h1', { timeout: 120000 })
      .then(() => 'FAILED')
      .catch(() => null)
  ]);
  const err = await page.evaluate(() => document.querySelector('.err pre')?.textContent ?? null);
  await page.close();
  return { outcome, err };
}

// GitHub Pages' CDN can lag a moment right after deploy — retry the fresh load.
async function attemptWithRetries(poison, label) {
  for (let i = 1; i <= 4; i++) {
    const { outcome, err } = await attempt(poison);
    if (outcome === 'BOOTED') {
      console.log(`✓ ${label}: booted`);
      return true;
    }
    console.log(`… ${label}: attempt ${i} -> ${outcome ?? 'timeout'}${err ? ` (${err})` : ''}`);
    await new Promise((r) => setTimeout(r, 15000));
  }
  console.error(`✗ ${label}: demo did not boot at ${url}`);
  return false;
}

const fresh = await attemptWithRetries(false, 'fresh visitor');
const returning = await attemptWithRetries(true, 'returning visitor (incompatible saved session)');
await browser.close();

if (!fresh || !returning) process.exit(1);
console.log('live demo smoke passed');
