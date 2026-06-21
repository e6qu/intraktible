// SPDX-License-Identifier: AGPL-3.0-or-later
// Persona: who is looking. A persona is a CONFIG-DRIVEN composition over the one
// public API — not a fork and not just a skin. Each persona declares its own
// navigation (an ordered subset of the shared catalog, optionally relabelled), a
// default home, and the primary actions to surface, so the same platform data is
// re-prioritised and re-routed for the viewer. It is persisted in localStorage and
// applied as <html data-persona="…"> so app.css can also swap accent/type/density.
// Persona is orthogonal to light/dark theme — every persona works in both.

import { writable } from 'svelte/store';
import type { CaseStatus, DecisionStatus, Environment, Variant } from './api';

export type Persona =
  | 'builder'
  | 'developer'
  | 'operator'
  | 'manager'
  | 'product'
  | 'showcase'
  | 'evaluator';

// NavId names an entry in the shared navigation catalog below.
export type NavId =
  | 'engine'
  | 'policies'
  | 'preapprovals'
  | 'decisions'
  | 'data'
  | 'cases'
  | 'agents'
  | 'models'
  | 'observability'
  | 'mrm'
  | 'keys'
  | 'audit';

export type NavItem = { id: NavId; href: string; label: string; icon: string };

// NAV is the full navigation catalog; each persona picks an ordered subset (and may
// relabel an item via PersonaConfig.terms). One source of truth for hrefs/icons. A
// Map (not a plain object) so variable-key lookups don't trip the object-injection lint.
export const NAV = new Map<NavId, NavItem>([
  ['engine', { id: 'engine', href: '/engine', label: 'Engine', icon: 'engine' }],
  ['policies', { id: 'policies', href: '/policies', label: 'Policies', icon: 'rule' }],
  [
    'preapprovals',
    { id: 'preapprovals', href: '/preapprovals', label: 'Approvals', icon: 'check' }
  ],
  ['decisions', { id: 'decisions', href: '/decisions', label: 'Decisions', icon: 'diagram' }],
  ['data', { id: 'data', href: '/data', label: 'Data', icon: 'database' }],
  ['cases', { id: 'cases', href: '/cases', label: 'Cases', icon: 'cases' }],
  ['agents', { id: 'agents', href: '/agents', label: 'Agents', icon: 'agents' }],
  ['models', { id: 'models', href: '/models', label: 'Models', icon: 'scorecard' }],
  [
    'observability',
    { id: 'observability', href: '/observability', label: 'Observability', icon: 'gauge' }
  ],
  ['mrm', { id: 'mrm', href: '/mrm', label: 'Model risk', icon: 'shield' }],
  ['keys', { id: 'keys', href: '/keys', label: 'API keys', icon: 'connect' }],
  ['audit', { id: 'audit', href: '/audit', label: 'Audit', icon: 'shield' }]
]);

export type Action = { label: string; href: string; icon: string };

// Which home composition a persona lands on. The three original archetypes keep
// their bespoke decks; the role personas use the config-driven PersonaHome.
export type HomeKind = 'builder' | 'operator' | 'showcase' | 'evaluator' | 'persona';

// PersonaLens is a persona's DEFAULT FOCUS on a shared list surface — the slice of
// the same data most relevant to that role, applied as the initial filter when the
// persona lands on the page (the user can still clear/change it). This is data
// re-prioritisation, not a skin or a fork: the page, its data, and its capabilities
// are identical across personas; only the initial lens differs. Surfaces a persona
// has no lens for show the full, unfiltered list.
// CaseSort orders the case queue: 'urgency' surfaces the soonest-due / overdue cases
// first (an operator works the queue top-down), 'recent' is the default store order.
export type CaseSort = 'urgency' | 'recent';

// DecisionColumn names a column of the decisions table. A persona's lens may pick an
// ordered subset (and order) so the same rows lead with what that role debugs by —
// a developer leads with status/duration, product with the experiment variant.
export type DecisionColumn =
  | 'status'
  | 'flow'
  | 'env'
  | 'version'
  | 'variant'
  | 'duration'
  | 'when';

// EmptyCopy overrides a list surface's empty state for a persona, so the message
// speaks the role's vocabulary and job (a developer's empty "Traces" reads very
// differently from an operator's empty queue).
export type EmptyCopy = { title: string; hint: string };

export type PersonaLens = {
  // The cases queue: WHICH cases (status) and in WHAT ORDER (sort) — an operator lands
  // on the open review queue, urgency-first.
  cases?: {
    status?: CaseStatus;
    sort?: CaseSort;
    empty?: EmptyCopy; // role-specific empty-queue message
  };
  // The decisions surface filters on several axes; a persona can focus any subset.
  decisions?: {
    status?: DecisionStatus; // e.g. a developer lands on failed traces to debug
    variant?: Variant; // e.g. product lands on the challenger (experiment) arm
    env?: Environment; // e.g. focus on production traffic
    columns?: DecisionColumn[]; // ordered visible columns (unset → the full default set)
    empty?: EmptyCopy; // role-specific empty-list message
  };
};

// HomeStatId names a tile the config-driven PersonaHome can surface, computed from the
// shared dashboard data (see dashboard.ts personaHomeStats). A persona picks the three
// (or so) that match its first question — a manager's "what's pending / who's overloaded"
// vs a developer's "what's failing and how slow" — over the SAME underlying data.
export type HomeStatId =
  | 'decisions'
  | 'completed'
  | 'failed'
  | 'flows'
  | 'p95'
  | 'completion_rate'
  | 'pending_approvals'
  | 'needs_review'
  | 'overdue'
  | 'unassigned'
  | 'challenger';

export type PersonaConfig = {
  id: Persona;
  label: string;
  blurb: string;
  icon: string; // an Icon name for the avatar/menu
  home: HomeKind; // the landing composition on "/"
  nav: NavId[]; // ordered navigation this persona sees
  actions: Action[]; // primary actions surfaced on the persona home
  terms?: Partial<Record<NavId, string>>; // per-persona nav relabels
  lens?: PersonaLens; // default filter focus on shared list surfaces
  homeStats?: HomeStatId[]; // PersonaHome tiles (unset → the default decisions/completed/flows)
};

const KEY = 'intraktible-persona';
export const defaultPersona: Persona = 'builder';

// The persona set, in switcher order. Each is a real role over the same API.
export const PERSONAS: PersonaConfig[] = [
  {
    id: 'builder',
    label: 'Workflow Designer',
    blurb: 'Author and version decision flows',
    icon: 'builder',
    home: 'builder',
    nav: ['engine', 'policies', 'data', 'models', 'decisions', 'agents'],
    actions: [
      { label: 'Open the flow builder', href: '/engine', icon: 'engine' },
      { label: 'Author policy bands', href: '/policies', icon: 'rule' },
      { label: 'Define context data', href: '/data', icon: 'database' }
    ]
  },
  {
    id: 'developer',
    label: 'Developer / Integrator',
    blurb: 'Integrate the decision API and debug traces',
    icon: 'agents',
    home: 'persona',
    nav: ['decisions', 'engine', 'keys', 'agents', 'data', 'observability', 'audit'],
    actions: [
      { label: 'Inspect decision traces', href: '/decisions', icon: 'diagram' },
      { label: 'Manage API keys', href: '/keys', icon: 'connect' },
      { label: 'Browse the API reference', href: '/docs', icon: 'code' },
      { label: 'Manage agents & tools', href: '/agents', icon: 'agents' }
    ],
    terms: { decisions: 'Traces' },
    // Land on failing traces, leading with the columns a debugger reads first
    // (status → duration → env), dropping the experiment variant.
    lens: {
      decisions: {
        status: 'failed',
        columns: ['status', 'flow', 'duration', 'env', 'version', 'when'],
        empty: {
          title: 'No failing traces',
          hint: 'Your integration is clean — nothing failed. Clear the status filter to see all traces.'
        }
      }
    },
    homeStats: ['failed', 'p95', 'completion_rate']
  },
  {
    id: 'operator',
    label: 'Risk Operator',
    blurb: 'Work the queues, SLAs, and monitors',
    icon: 'operator',
    home: 'operator',
    nav: ['cases', 'decisions', 'preapprovals', 'policies', 'observability', 'audit'],
    actions: [
      { label: 'Work the case queue', href: '/cases', icon: 'cases' },
      { label: 'Review pre-approvals', href: '/preapprovals', icon: 'check' },
      { label: 'Scan recent decisions', href: '/decisions', icon: 'diagram' }
    ],
    lens: {
      // The open queue, most-urgent first, with a queue-cleared message in the
      // operator's own terms.
      cases: {
        status: 'needs_review',
        sort: 'urgency',
        empty: {
          title: 'The review queue is clear',
          hint: 'No cases need review — every open case is within SLA. Widen the status filter to see the rest.'
        }
      }
    }
  },
  {
    id: 'manager',
    label: 'Team Manager',
    blurb: 'Approvals, reviewer workload, and SLA health',
    icon: 'check',
    home: 'persona',
    nav: ['preapprovals', 'cases', 'decisions', 'observability', 'mrm', 'audit'],
    actions: [
      { label: 'Clear pending approvals', href: '/preapprovals', icon: 'check' },
      { label: 'Check case load', href: '/cases', icon: 'cases' },
      { label: 'Review the audit trail', href: '/audit', icon: 'shield' }
    ],
    terms: { preapprovals: 'Approvals' },
    homeStats: ['pending_approvals', 'needs_review', 'overdue']
  },
  {
    id: 'product',
    label: 'Product / Experimentation',
    blurb: 'A/B, shadow, backtests, and policy impact',
    icon: 'diagram',
    home: 'persona',
    nav: ['engine', 'policies', 'models', 'decisions', 'data', 'observability'],
    actions: [
      { label: 'Backtest a flow', href: '/engine', icon: 'engine' },
      { label: 'Tune policy impact', href: '/policies', icon: 'rule' },
      { label: 'Manage models', href: '/models', icon: 'scorecard' },
      { label: 'Analyse decisions', href: '/decisions', icon: 'diagram' }
    ],
    // Land on the experiment arm, leading with the variant column.
    lens: {
      decisions: {
        variant: 'challenger',
        columns: ['variant', 'status', 'flow', 'env', 'duration', 'when']
      }
    },
    homeStats: ['challenger', 'decisions', 'completion_rate']
  },
  {
    id: 'showcase',
    label: 'Executive',
    blurb: 'KPIs, trends, and governance posture',
    icon: 'showcase',
    home: 'showcase',
    nav: ['decisions', 'cases', 'observability', 'mrm', 'audit'],
    actions: [{ label: 'View decision volume', href: '/decisions', icon: 'diagram' }]
  },
  {
    id: 'evaluator',
    label: 'Evaluator / Guest',
    blurb: 'A guided look at the platform',
    icon: 'search',
    home: 'evaluator',
    nav: ['engine', 'decisions'],
    actions: [
      { label: 'Explore the flow builder', href: '/engine', icon: 'engine' },
      { label: 'See decisions in action', href: '/decisions', icon: 'diagram' }
    ]
  }
];

const byID = new Map<Persona, PersonaConfig>(PERSONAS.map((p) => [p.id, p]));
const fallbackConfig = PERSONAS[0]; // defaultPersona is the first entry

// personaConfig returns the config for a persona (always defined for a valid id).
export function personaConfig(p: Persona): PersonaConfig {
  return byID.get(p) ?? fallbackConfig;
}

// navFor returns a persona's ordered, relabelled navigation items.
export function navFor(p: Persona): NavItem[] {
  const cfg = personaConfig(p);
  const terms = new Map(Object.entries(cfg.terms ?? {}));
  return cfg.nav
    .map((id) => NAV.get(id))
    .filter((item): item is NavItem => item !== undefined)
    .map((item) => {
      const label = terms.get(item.id);
      return label ? { ...item, label } : item;
    });
}

// personaLens returns a persona's default-focus filters for the shared list surfaces
// (empty for personas with no lens → the full, unfiltered list). A list page reads
// this for its INITIAL filter only; the user can change or clear it freely.
export function personaLens(p: Persona): PersonaLens {
  return personaConfig(p).lens ?? {};
}

// persona is the reactive current persona (kept in sync by initPersona/setPersona).
export const persona = writable<Persona>(defaultPersona);

function isPersona(v: string | null): v is Persona {
  return v != null && byID.has(v as Persona);
}

// resolvePersona is the stored choice, defaulting to the Workflow Designer.
export function resolvePersona(): Persona {
  if (typeof localStorage !== 'undefined') {
    const stored = localStorage.getItem(KEY);
    if (isPersona(stored)) return stored;
  }
  return defaultPersona;
}

function applyPersona(p: Persona): void {
  if (typeof document !== 'undefined') {
    document.documentElement.setAttribute('data-persona', p);
  }
}

// setPersona persists, applies, and publishes a persona.
export function setPersona(p: Persona): void {
  if (typeof localStorage !== 'undefined') {
    localStorage.setItem(KEY, p);
  }
  applyPersona(p);
  persona.set(p);
}

// initPersona resolves and publishes the active persona (call once on mount).
export function initPersona(): Persona {
  const p = resolvePersona();
  applyPersona(p);
  persona.set(p);
  return p;
}
