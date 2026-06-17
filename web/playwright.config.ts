// SPDX-License-Identifier: AGPL-3.0-or-later
import { defineConfig, devices } from '@playwright/test';

// End-to-end UI tests. Two servers are started: the Go backend (API + seeded dev
// key) on :8080, and the Vite dev server on :5173 which proxies /v1 and /healthz
// to the backend. Tests drive a real browser against the SvelteKit app.
//
// The `test:e2e` npm script sets NODE_OPTIONS=--disable-warning=DEP0205 to silence
// ONE upstream notice: Playwright's own ESM loader (registerESMLoader) still calls
// the Node-deprecated module.register(). It is code-targeted, so every other
// warning/deprecation still surfaces. Remove it once Playwright migrates to
// module.registerHooks().
export default defineConfig({
  testDir: './e2e',
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 2 : 0,
  reporter: process.env.CI ? 'github' : 'list',
  use: {
    baseURL: 'http://localhost:5173',
    trace: 'on-first-retry'
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
  webServer: [
    {
      command: 'go run ./cmd/intraktible serve --addr=:8080 --data-dir=web/.pw-data --modules=all',
      cwd: '..',
      url: 'http://localhost:8080/healthz',
      reuseExistingServer: !process.env.CI,
      // The backend logs a line per request at INFO on stdout. Piping that to the
      // test runner floods pre-commit's captured (non-blocking) stdout and aborts
      // the push with a BlockingIOError, so drop it; stderr still surfaces real errors.
      stdout: 'ignore',
      stderr: 'pipe',
      timeout: 120_000
    },
    {
      // Run e2e against the production build via `vite preview`, not `vite dev`:
      // the dev server's on-demand dep-optimization + HMR open a cold-start window
      // where partially-initialized modules throw transient client errors under the
      // parallel suite. The preview server has no such window, so the run is
      // deterministic and exercises the artifact that actually ships.
      command: 'vite build && vite preview --port 5173 --strictPort',
      url: 'http://localhost:5173',
      reuseExistingServer: !process.env.CI,
      timeout: 180_000
    }
  ]
});
