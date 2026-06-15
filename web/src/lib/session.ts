// SPDX-License-Identifier: AGPL-3.0-or-later
// The signed-in identity, shared app-wide. The UI authenticates with the session
// cookie (set by /v1/login), so pages no longer carry an API key — they just read
// this store to know who is signed in.

import { writable } from 'svelte/store';
import { currentUser, logout as apiLogout, type Identity } from '$lib/api';

export const user = writable<Identity | null>(null);

// refreshUser re-reads the current identity from /v1/me (null when not signed in).
export async function refreshUser(): Promise<void> {
  try {
    user.set(await currentUser());
  } catch {
    user.set(null);
  }
}

// signOut revokes the session and clears the store.
export async function signOut(): Promise<void> {
  try {
    await apiLogout();
  } finally {
    user.set(null);
  }
}
