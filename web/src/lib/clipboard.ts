// SPDX-License-Identifier: AGPL-3.0-or-later
// Copy text through the browser Clipboard API, failing loudly via a toast when
// the context or permissions do not support it.

import { toast } from '$lib/toast';

export async function copyText(text: string, label = 'Copied'): Promise<boolean> {
  try {
    if (!navigator.clipboard?.writeText) {
      throw new Error('Clipboard access is unavailable in this browser context');
    }
    await navigator.clipboard.writeText(text);
    toast.success(label);
    return true;
  } catch (e) {
    toast.error(e instanceof Error ? e.message : 'Couldn’t copy to clipboard');
    return false;
  }
}
