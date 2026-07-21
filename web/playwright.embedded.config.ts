// SPDX-License-Identifier: AGPL-3.0-or-later
import { defineConfig, devices } from '@playwright/test';

const executablePath = process.env.PLAYWRIGHT_EXECUTABLE_PATH;
const embeddedPort = Number.parseInt(process.env.INTRAKTIBLE_E2E_EMBEDDED_PORT ?? '8080', 10);
if (!Number.isInteger(embeddedPort) || embeddedPort < 1 || embeddedPort > 65_535) {
  throw new Error('INTRAKTIBLE_E2E_EMBEDDED_PORT must be a TCP port from 1 through 65535');
}
const embeddedOrigin = `http://127.0.0.1:${embeddedPort}`;

// Runs the embedded-binary smoke (web/e2e-embedded) against the single
// `intraktible serve` artifact — the binary with the real SvelteKit UI embedded,
// serving API + UI on the configured loopback port. The binary must already be built with the real UI
// (the `make e2e-embedded` target does that); this config only starts and probes
// it. This is deliberately separate from playwright.config.ts (which targets the
// Vite preview server) so the shipping artifact is exercised end-to-end.
export default defineConfig({
  testDir: './e2e-embedded',
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 2 : 0,
  reporter: process.env.CI ? 'github' : 'list',
  use: {
    baseURL: embeddedOrigin,
    launchOptions: executablePath ? { executablePath } : undefined,
    trace: 'on-first-retry'
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
  webServer: {
    command: `cd .. && ./bin/intraktible serve --addr=127.0.0.1:${embeddedPort} --data-dir=web/.pw-data-embedded`,
    env: { INTRAKTIBLE_AI_STUB: '1' },
    url: `${embeddedOrigin}/healthz`,
    // Never reuse: a stale dev server on the selected port (e.g. a go-run orphan built with
    // the placeholder assets) would silently stand in for the artifact under
    // test — the same E1 gotcha fixed for the main config.
    reuseExistingServer: false,
    stdout: 'ignore',
    stderr: 'pipe',
    timeout: 120_000
  }
});
