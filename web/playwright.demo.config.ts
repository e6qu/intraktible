// SPDX-License-Identifier: AGPL-3.0-or-later
import { defineConfig, devices } from '@playwright/test';

// Smoke-tests the static, backend-less GitHub Pages demo: builds with BASE_PATH=/demo
// + VITE_DEMO=true (so the in-browser mock backend is installed) and serves it via
// `vite preview` under the /demo base. No Go backend — every /v1 call is intercepted
// client-side. This is deliberately separate from playwright.config.ts (which needs
// the real backend) and doubles as the Pages workflow's pre-deploy gate.
export default defineConfig({
  testDir: './e2e-demo',
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 2 : 0,
  reporter: process.env.CI ? 'github' : [['dot'], ['html', { open: 'never' }]],
  use: {
    // Trailing slash so relative goto('engine') resolves under the /demo base.
    baseURL: 'http://localhost:4173/demo/',
    trace: 'on-first-retry'
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
  webServer: {
    command:
      'BASE_PATH=/demo VITE_DEMO=true vite build && BASE_PATH=/demo vite preview --port 4173 --strictPort',
    url: 'http://localhost:4173/demo/',
    reuseExistingServer: !process.env.CI,
    stdout: 'ignore',
    stderr: 'pipe',
    timeout: 180_000
  }
});
