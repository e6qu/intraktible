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
  // Retry everywhere (not only CI): the pre-push hook is a CI-equivalent gate, and
  // this is a parallel browser suite sharing one backend + an eventually-consistent
  // projection, so an occasional timing blip (projection lag, hydration) is inherent
  // and must not fail a push. A genuinely-broken test still fails all attempts.
  retries: 2,
  // `dot` keeps streamed stdout to ~one char per test. The pre-push hook captures
  // hook output in a NON-BLOCKING pipe, and the verbose `list` reporter (per-test
  // lines + failure traces) overflowed it — a BlockingIOError aborted the whole run
  // and cascaded into spurious failures. The full report is written to disk (HTML)
  // for debugging rather than streamed.
  reporter: process.env.CI ? 'github' : [['dot'], ['html', { open: 'never' }]],
  use: {
    baseURL: 'http://localhost:5173',
    trace: 'on-first-retry'
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
  webServer: [
    {
      // Wipe the event log so every run starts from EMPTY state. The log persists
      // across runs, and flows etc. accumulate; the engine list caps rendering at 100
      // rows, so once the backlog exceeds that a freshly-created flow renders past the
      // cap and "create a flow" assertions fail deterministically. A clean log keeps
      // each run's read models bounded and deterministic.
      //
      // Run a BUILT BINARY, not `go run`: `go run` execs a child that survives the
      // parent's teardown signal, orphaning a server on :8080 that the next run would
      // silently reuse (stale/half-dead → mass toBeVisible failures across unrelated
      // specs). The binary is killed cleanly, so reuseExistingServer:false below
      // always gets a fresh, empty backend.
      command:
        'rm -rf web/.pw-data && go build -o bin/intraktible-e2e ./cmd/intraktible && ./bin/intraktible-e2e serve --addr=:8080 --data-dir=web/.pw-data --modules=all',
      cwd: '..',
      url: 'http://localhost:8080/healthz',
      reuseExistingServer: false,
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
      // `vite build` is verbose; like the backend above, keep it off the pre-push
      // hook's non-blocking pipe (stderr still surfaces a real build failure).
      stdout: 'ignore',
      stderr: 'pipe',
      timeout: 180_000
    }
  ]
});
