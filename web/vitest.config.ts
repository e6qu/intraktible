// SPDX-License-Identifier: AGPL-3.0-or-later
import { defineConfig } from 'vitest/config';
import { resolve } from 'node:path';

// Unit tests for framework-agnostic logic (e.g. the API client). UI flows are
// covered by Playwright e2e, not here.
export default defineConfig({
  // Resolve the SvelteKit $lib alias so a test can import a module that
  // (transitively) imports $lib — e.g. dashboard.ts → $lib/api.
  resolve: {
    alias: { $lib: resolve('src/lib') }
  },
  test: {
    environment: 'node',
    include: ['src/**/*.test.ts']
  }
});
