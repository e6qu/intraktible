// SPDX-License-Identifier: AGPL-3.0-or-later
// The signed-in identity, shared app-wide. The UI authenticates with the session
// cookie (set by /v1/login), so pages no longer carry an API key — they just read
// this store to know who is signed in.

import { writable } from 'svelte/store';
import { currentUser, logout as apiLogout, ApiError, type Identity } from '$lib/api';

export const user = writable<Identity | null>(null);

// refreshUser re-reads the current identity from /v1/me. currentUser() already maps a
// real 401 (no session) to null; any OTHER failure is transient (a network blip, a 5xx)
// and must NOT visually log the user out — keep the current session and rethrow so the
// caller can surface it. Mapping every error to null made a momentary /v1/me blip drop
// the nav and read as a sign-out.
export async function refreshUser(): Promise<void> {
  try {
    user.set(await currentUser());
  } catch (e) {
    if (e instanceof ApiError && e.status === 401) {
      user.set(null);
      return;
    }
    throw e;
  }
}

// signOut clears the local identity only after the server confirms revocation,
// then returns the identity-provider logout URL when this was an SSO session.
// Keeping the identity on failure prevents the browser from presenting a false
// signed-out state while its still-valid cookie can access the application.
export async function signOut(): Promise<string> {
  const logoutURL = await apiLogout();
  user.set(null);
  return logoutURL;
}
