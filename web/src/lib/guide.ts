// SPDX-License-Identifier: AGPL-3.0-or-later
// Open/close state for the per-page guide slide-over, shared so the header trigger
// and the command palette can both open it (mirrors $lib/shortcuts).
import { writable } from 'svelte/store';

export const guideOpen = writable(false);
export const openGuide = (): void => guideOpen.set(true);
export const closeGuide = (): void => guideOpen.set(false);
export const toggleGuide = (): void => guideOpen.update((v) => !v);
