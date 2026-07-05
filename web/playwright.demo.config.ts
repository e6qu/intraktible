// SPDX-License-Identifier: AGPL-3.0-or-later
import { defineConfig, devices } from '@playwright/test';

// Smoke-tests the static, server-less GitHub Pages demo: `build:demo` first runs
// `make wasm demo-seed` (the REAL Go backend compiled to wasm + its seed event
// log land in web/static/), then builds with BASE_PATH=/intraktible/demo +
// VITE_DEMO=true and serves the bundle via `vite preview`. Every /v1 call is
// served by the wasm backend booted in a worker — no HTTP server. This is
// deliberately separate from playwright.config.ts (which needs the native
// backend) and doubles as the Pages workflow's pre-deploy gate.
export default defineConfig({
  testDir: './e2e-demo',
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 2 : 0,
  reporter: process.env.CI ? 'github' : [['dot'], ['html', { open: 'never' }]],
  // Every full page load boots the embedded backend (a ~10 MB wasm download +
  // seed replay), so tests and assertions need more headroom than an HTTP suite —
  // and unbounded parallelism starves page loads (each worker instantiates its own
  // wasm engine per navigation), so parallelism is capped.
  timeout: 90_000,
  expect: { timeout: 20_000 },
  workers: 2,
  use: {
    // Trailing slash so relative goto('engine') resolves under the base. The base is
    // /intraktible/demo because GitHub project Pages serve under /<repo>/ — the demo
    // lives at https://e6qu.github.io/intraktible/demo/, so the SPA's asset/link base
    // must include the repo segment (a bare /demo 404s every chunk on Pages).
    baseURL: 'http://localhost:4173/intraktible/demo/',
    trace: 'on-first-retry'
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
  webServer: {
    command:
      'npm run build:demo && BASE_PATH=/intraktible/demo vite preview --port 4173 --strictPort',
    url: 'http://localhost:4173/intraktible/demo/',
    // Never reuse: a stale preview on :4173 (from an earlier build) silently
    // stands in for the bundle under test — the E1 gotcha, third surface.
    reuseExistingServer: false,
    stdout: 'ignore',
    stderr: 'pipe',
    timeout: 300_000
  }
});
