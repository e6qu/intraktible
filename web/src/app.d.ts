// SPDX-License-Identifier: AGPL-3.0-or-later
declare global {
  namespace App {}
  // Build provenance, replaced by Vite `define` at build time (see vite.config.ts).
  const __APP_GIT_SHA__: string;
  const __APP_BUILD_TIME__: string;
}
export {};
