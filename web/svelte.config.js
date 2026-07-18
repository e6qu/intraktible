// SPDX-License-Identifier: AGPL-3.0-or-later
import adapter from '@sveltejs/adapter-static';
import { vitePreprocess } from '@sveltejs/vite-plugin-svelte';

/** @type {import('@sveltejs/kit').Config} */
export default {
  preprocess: vitePreprocess(),
  kit: {
    // Static SPA, embedded into the Go binary at platform/web/assets.
    adapter: adapter({
      pages: 'build',
      assets: 'build',
      fallback: 'index.html',
      precompress: false
    }),
    // Served at the origin root when embedded in the binary (BASE_PATH unset), and
    // under a sub-path for the public GitHub Pages demo (build:demo sets
    // /intraktible/demo — project Pages serve under /<repo>/). Internal links read
    // this via $app/paths `base`, so the same source is portable.
    paths: { base: process.env.BASE_PATH || '' },
    // Never expose the shell's entire environment through SvelteKit's generated
    // $env modules. Besides protecting incidental process coordinates, this
    // keeps a valid-but-keyword environment variable from generating invalid
    // TypeScript declarations.
    env: { publicPrefix: 'PUBLIC_', privatePrefix: 'INTRAKTIBLE_' }
  }
};
