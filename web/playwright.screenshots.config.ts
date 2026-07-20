// SPDX-License-Identifier: AGPL-3.0-or-later
import base from './playwright.config';
import { defineConfig } from '@playwright/test';

export default defineConfig({
  ...base,
  testDir: './design-review',
  retries: 0
});
