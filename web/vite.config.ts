// SPDX-License-Identifier: AGPL-3.0-or-later
import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

// Dev proxies the API to the Go server; prod is embedded in the binary.
export default defineConfig({
  plugins: [sveltekit()],
  // ws:true proxies the streaming WebSocket endpoint to the Go server. The same
  // proxy is configured for `preview` (the production build server) so the e2e
  // suite — which runs against `vite preview`, not `vite dev`, to avoid dev-mode
  // optimize/HMR transients — reaches the backend identically.
  server: {
    proxy: {
      '/v1': { target: 'http://localhost:8080', ws: true },
      '/healthz': 'http://localhost:8080'
    }
  },
  preview: {
    proxy: {
      '/v1': { target: 'http://localhost:8080', ws: true },
      '/healthz': 'http://localhost:8080'
    }
  }
});
