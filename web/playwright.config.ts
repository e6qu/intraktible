// SPDX-License-Identifier: AGPL-3.0-or-later
import { defineConfig, devices } from '@playwright/test';

// End-to-end UI tests. Two servers are started: the Go backend (API + seeded dev
// key) on :8080, and the Vite dev server on :5173 which proxies /v1 and /healthz
// to the backend. Tests drive a real browser against the SvelteKit app.
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
      stdout: 'pipe',
      stderr: 'pipe',
      timeout: 120_000
    },
    {
      command: 'vite dev --port 5173 --strictPort',
      url: 'http://localhost:5173',
      reuseExistingServer: !process.env.CI,
      timeout: 120_000
    }
  ]
});
