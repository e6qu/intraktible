// SPDX-License-Identifier: AGPL-3.0-or-later
import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

// Dev proxies the API to the Go server; prod is embedded in the binary.
export default defineConfig({
  plugins: [sveltekit()],
  // ws:true proxies the streaming WebSocket endpoint to the Go server in dev.
  server: {
    proxy: {
      '/v1': { target: 'http://localhost:8080', ws: true },
      '/healthz': 'http://localhost:8080'
    }
  }
});
