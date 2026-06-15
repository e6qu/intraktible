// SPDX-License-Identifier: AGPL-3.0-or-later
import { defineConfig } from 'vitest/config';

// Unit tests for framework-agnostic logic (e.g. the API client). UI flows are
// covered by Playwright e2e, not here.
export default defineConfig({
  test: {
    environment: 'node',
    include: ['src/**/*.test.ts']
  }
});
