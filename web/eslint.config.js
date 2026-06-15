// SPDX-License-Identifier: AGPL-3.0-or-later
import { defineConfig } from 'eslint/config';
import js from '@eslint/js';
import tseslint from 'typescript-eslint';
import svelte from 'eslint-plugin-svelte';
import security from 'eslint-plugin-security';
import globals from 'globals';

// Strict lint + JS/TS SAST for the web app. Type checking is handled separately
// by svelte-check (strict tsconfig); eslint here enforces correctness/style and,
// via eslint-plugin-security, static security analysis.
export default defineConfig(
  {
    ignores: [
      '.svelte-kit/',
      'build/',
      'node_modules/',
      'playwright-report/',
      'test-results/',
      '.pw-data/'
    ]
  },
  js.configs.recommended,
  ...tseslint.configs.strict,
  ...svelte.configs['flat/recommended'],
  security.configs.recommended,
  {
    languageOptions: {
      globals: { ...globals.browser, ...globals.node }
    },
    rules: {
      // Allow intentionally-unused identifiers prefixed with `_` (e.g. typed but
      // unused mock parameters that exist only to shape a call signature).
      '@typescript-eslint/no-unused-vars': [
        'error',
        { argsIgnorePattern: '^_', varsIgnorePattern: '^_', caughtErrorsIgnorePattern: '^_' }
      ]
    }
  },
  {
    files: ['**/*.svelte'],
    languageOptions: {
      parserOptions: { parser: tseslint.parser }
    }
  }
);
