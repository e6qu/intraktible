// SPDX-License-Identifier: AGPL-3.0-or-later
// Paging within a URL-driven (applied) filter: rewrite ONLY the offset param of
// the current query string, so draft filter inputs the user has edited but not
// yet Applied never leak into a page turn.

// withOffset returns the query string with the offset param set (or removed for
// page one), leaving every other applied-filter param untouched.
export function withOffset(params: URLSearchParams, offset: number): string {
  const p = new URLSearchParams(params);
  if (offset > 0) p.set('offset', String(offset));
  else p.delete('offset');
  return p.toString();
}
