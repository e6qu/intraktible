// SPDX-License-Identifier: AGPL-3.0-or-later
// The content model for the in-app page guide. Deliberately tight — one short
// summary, a few capabilities, and a few key flows — so the guide stays scannable
// and dispassionate (see registry.ts for the style rules the content follows).

export type HelpJourney = {
  name: string; // imperative, e.g. "Author and publish a version"
  steps: string[]; // 3–6 short steps; each one action and its outcome
};

export type HelpLink = { label: string; href: string };

export type PageHelp = {
  title: string; // the page as the user knows it
  summary: string; // 1–2 sentences: what this page is for
  capabilities: string[]; // 3–6 verb-first bullets: what you can do here
  journeys?: HelpJourney[]; // up to ~3 key flows; collapsed by default in the panel
  links?: HelpLink[];
};
