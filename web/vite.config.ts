// SPDX-License-Identifier: AGPL-3.0-or-later
import { execSync } from 'node:child_process';
import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

// Build provenance, baked in at build time and shown in the footer on every page.
// Resolution order: an explicit env var (set by the Docker/prod build, where .git is
// absent) -> the git short SHA (local + the Pages build, which checks out .git) -> 'dev'.
function gitSha(): string {
  if (process.env.PUBLIC_GIT_SHA) return process.env.PUBLIC_GIT_SHA;
  try {
    return execSync('git rev-parse --short HEAD', { stdio: ['ignore', 'pipe', 'ignore'] })
      .toString()
      .trim();
  } catch {
    return 'dev';
  }
}
const buildTime = process.env.PUBLIC_BUILD_TIME || new Date().toISOString();
const apiTarget = process.env.INTRAKTIBLE_DEV_API_URL || 'http://localhost:8080';

// Dev proxies the API to the Go server; prod is embedded in the binary.
export default defineConfig({
  define: {
    __APP_GIT_SHA__: JSON.stringify(gitSha()),
    __APP_BUILD_TIME__: JSON.stringify(buildTime)
  },
  plugins: [sveltekit()],
  // ws:true proxies the streaming WebSocket endpoint to the Go server. The same
  // proxy is configured for `preview` (the production build server) so the e2e
  // suite — which runs against `vite preview`, not `vite dev`, to avoid dev-mode
  // optimize/HMR transients — reaches the backend identically.
  server: {
    proxy: {
      '/v1': { target: apiTarget, ws: true },
      '/auth': apiTarget,
      '/healthz': apiTarget,
      '/version': apiTarget
    }
  },
  preview: {
    proxy: {
      '/v1': { target: apiTarget, ws: true },
      '/auth': apiTarget,
      '/healthz': apiTarget,
      '/version': apiTarget
    }
  }
});
