// SPDX-License-Identifier: AGPL-3.0-or-later
// Copy text to the clipboard, failing loudly via a toast. Falls back to a hidden
// textarea + execCommand for non-secure contexts (e.g. plain-http self-hosting)
// where the async Clipboard API is unavailable.

import { toast } from '$lib/toast';

export async function copyText(text: string, label = 'Copied'): Promise<boolean> {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text);
      toast.success(label);
      return true;
    }
  } catch {
    // fall through to the legacy path
  }
  try {
    const ta = document.createElement('textarea');
    ta.value = text;
    ta.style.position = 'fixed';
    ta.style.opacity = '0';
    document.body.appendChild(ta);
    ta.select();
    // execCommand is deprecated but is the only copy path in a non-secure context
    // (plain-http self-hosting); cast through a local type to use it without the
    // deprecated-overload diagnostic.
    const legacy = document as unknown as { execCommand(cmd: string): boolean };
    const ok = legacy.execCommand('copy');
    document.body.removeChild(ta);
    if (ok) {
      toast.success(label);
      return true;
    }
  } catch {
    // fall through to the error toast
  }
  toast.error('Couldn’t copy to clipboard');
  return false;
}
