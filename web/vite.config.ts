// SPDX-License-Identifier: AGPL-3.0-or-later
import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

// Dev proxies the API to the Go server; prod is embedded in the binary.
export default defineConfig({
  plugins: [sveltekit()],
  server: { proxy: { '/v1': 'http://localhost:8080', '/healthz': 'http://localhost:8080' } }
});
