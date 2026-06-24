// SPDX-License-Identifier: AGPL-3.0-or-later
// Per-page guide content, keyed by SvelteKit route id ($page.route.id) — the stable,
// base-path-independent key. One entry per page; adding a page = adding one entry.
//
// Style rules (keep the guide scannable + dispassionate):
//  - summary: 1–2 sentences, what the page is FOR (not its features).
//  - capabilities: 3–6 verb-first bullets, one line each, things you do on THIS page.
//  - journeys: ≤3 flows, 3–6 steps; a step is one action and its outcome.
//  - present tense, second person, no praise adjectives or marketing.
//  - name real on-screen controls/statuses so the guide maps 1:1 to the UI.
// A coverage/caps test (help.test.ts) enforces these bounds.
import type { PageHelp } from './types';

const DOCS = 'https://github.com/e6qu/intraktible/tree/main/docs';

export const HELP = new Map<string, PageHelp>([
  [
    '/',
    {
      title: 'Home',
      summary:
        'Your starting point. The dashboard composes the same platform data for the persona you are viewing as — switch persona from the account menu to re-prioritise it.',
      capabilities: [
        'See at-a-glance health: decisions, cases needing review, agent runs, live flows.',
        'Jump to the surfaces that matter to your role.',
        'Switch persona (the whole UI re-prioritises) from the account menu.'
      ]
    }
  ],
  [
    '/engine',
    {
      title: 'Flows',
      summary:
        'The catalogue of decision flows. Each flow is versioned and deployed per environment.',
      capabilities: [
        'Create a flow, or import one as code.',
        'See each flow’s latest version and what is deployed to sandbox, staging, and production.',
        'Open a flow to build, version, test, and deploy it.'
      ],
      journeys: [
        {
          name: 'Start a new flow',
          steps: [
            'Enter a slug and name, then Create flow.',
            'Open it to add nodes and publish the first version.'
          ]
        }
      ]
    }
  ],
  [
    '/engine/[flowId]',
    {
      title: 'Flow builder',
      summary:
        'Build a decision flow as a graph of nodes, then version and test it. The active version is what the decision API runs.',
      capabilities: [
        'Add and connect nodes: input, assignment, split, predict, AI, manual review, output.',
        'Edit a node’s logic in the side panel; changes stay in the working draft.',
        'Run a test decision with sample input — record it to sandbox, or tick Preview for a dry run that records nothing.',
        'Publish the draft to create a new immutable version.',
        'Deploy a version to an environment, or roll back to the previous one.'
      ],
      journeys: [
        {
          name: 'Author and publish a version',
          steps: [
            'Add nodes from the palette and connect them from input to output.',
            'Select a node to edit its logic in the side panel.',
            'Run a test decision to confirm the path resolves as expected.',
            'Click Publish — the draft becomes the new active version.'
          ]
        },
        {
          name: 'Promote with four-eyes',
          steps: [
            'Deploy to sandbox or staging directly.',
            'Request a production deployment — it becomes a pending request.',
            'A different user with the approver role approves it; then it is live.'
          ]
        }
      ],
      links: [{ label: 'Decision API & expressions', href: DOCS }]
    }
  ],
  [
    '/decisions',
    {
      title: 'Decisions',
      summary:
        'Every decision the engine has run, event-sourced and replayable. Filter to find a run; open one to see its full trace.',
      capabilities: [
        'Filter by flow, environment, status, variant, or decision id.',
        'See status, disposition, and latency per run.',
        'Open a decision to read its node-by-node trace.'
      ]
    }
  ],
  [
    '/decisions/[decisionId]',
    {
      title: 'Decision trace',
      summary:
        'One decision’s replayable trace: the verdict, the path it took, and the inputs and outputs at each node.',
      capabilities: [
        'See the disposition and the reasons behind it.',
        'Follow the node-by-node path and the branch taken at each split.',
        'Inspect the input and output payloads, and export the trace.',
        'Open the case it opened, if it routed to manual review.'
      ]
    }
  ],
  [
    '/policies',
    {
      title: 'Policies',
      summary:
        'The disposition layer over a flow’s output: ordered bands that map a decision to approve, decline, or refer. Versioned, like flows.',
      capabilities: [
        'Create a policy bound to a flow.',
        'Author ordered bands (condition → disposition) and publish a version.',
        'Backtest a draft against recorded decisions to preview the impact before publishing.'
      ],
      journeys: [
        {
          name: 'Publish a policy change',
          steps: [
            'Edit the bands for the policy.',
            'Preview impact to see how dispositions would shift.',
            'Publish — the new version applies to subsequent decisions.'
          ]
        }
      ]
    }
  ],
  [
    '/preapprovals',
    {
      title: 'Pre-approvals',
      summary:
        'Standing decisions for known entities: a granted pre-approval is honoured instantly instead of running the flow, until it expires or is revoked.',
      capabilities: [
        'Grant a pre-approval for an entity, optionally bound to a flow, with terms and an expiry.',
        'See active, expiring, and revoked grants and how often each has been honoured.',
        'Revoke a grant.'
      ]
    }
  ],
  [
    '/data',
    {
      title: 'Context data',
      summary:
        'The data a decision sees: connectors that fetch external signals, features computed from event history, and the entities those describe.',
      capabilities: [
        'Define a connector to an external source.',
        'Define a feature (an aggregation over an entity’s events).',
        'Register entities and record events; open an entity to see its computed features.'
      ]
    }
  ],
  [
    '/data/[type]/[id]',
    {
      title: 'Entity',
      summary:
        'One entity’s attributes, its event history, and the feature values computed from it.',
      capabilities: [
        'See the entity’s attributes.',
        'Review its recorded events.',
        'See the current value of each feature for this entity.'
      ]
    }
  ],
  [
    '/cases',
    {
      title: 'Case queue',
      summary:
        'The work queue for decisions that need a human. A case opens here when a flow escalates to manual review or an agent run is escalated.',
      capabilities: [
        'Filter by status and sort by urgency (soonest-due and overdue first).',
        'Assign a case to a reviewer.',
        'Open a case to see its decision context and history.',
        'Sweep SLAs to flag cases past their due time.'
      ],
      journeys: [
        {
          name: 'Work the open queue',
          steps: [
            'Filter to needs_review and sort by urgency.',
            'Open the top case and read its decision context.',
            'Assign it to yourself if it is unassigned.',
            'Set the outcome — the case leaves the open queue.'
          ]
        }
      ]
    }
  ],
  [
    '/cases/[caseId]',
    {
      title: 'Case',
      summary:
        'One case for review: its decision context, SLA, notes, and an immutable activity trail.',
      capabilities: [
        'Read the decision context and open the source decision.',
        'Assign the case and record notes.',
        'Set the status; resolve it when the review is done.'
      ]
    }
  ],
  [
    '/agents',
    {
      title: 'Agent Manager',
      summary:
        'Configure and run LLM task-agents — a prompt, a model, tools, and an optional structured-output schema — and watch their usage and cost.',
      capabilities: [
        'Define an agent (prompt, model, tools, output schema).',
        'See run volume, token usage, and cost across agents.',
        'Open an agent to run it, version it, and score it against eval cases.'
      ]
    }
  ],
  [
    '/agents/[name]',
    {
      title: 'Agent',
      summary:
        'One agent: run it, review its run history and versions, and check it against an offline eval set.',
      capabilities: [
        'Run the agent (request/response or streamed) and see the output.',
        'Review past runs and escalate one to a case for human review.',
        'See the version history and the eval pass-rate.'
      ]
    }
  ],
  [
    '/models',
    {
      title: 'Models',
      summary:
        'Predictive models hosted as data (logistic, GBM, expression, or an external endpoint), referenced from a flow’s Predict node.',
      capabilities: [
        'Define a model from a spec.',
        'See each model’s owner and drift status at a glance.',
        'Open a model to capture a baseline and set a drift (PSI) monitor.'
      ]
    }
  ],
  [
    '/observability',
    {
      title: 'Observability',
      summary:
        'The operational view across flows: service-level objectives and attainment, AI usage and cost, and request tracing.',
      capabilities: [
        'Set a success and latency objective (SLO) per flow and see attainment.',
        'See AI usage and cost by model.',
        'Read how distributed tracing is emitted.'
      ]
    }
  ],
  [
    '/mrm',
    {
      title: 'Model risk',
      summary:
        'The SR 11-7 / SS1/23 model inventory: every flow, predictive model, and agent with its validation evidence, live monitoring, and open governance gaps. Admin only.',
      capabilities: [
        'See the full inventory of flows, models, and agents in one register.',
        'Review each entry’s validation coverage, drift, and firing monitors.',
        'Identify open governance gaps per entry.',
        'Export the report (CSV or Markdown) for stakeholders.'
      ],
      journeys: [
        {
          name: 'Review governance posture',
          steps: [
            'Scan the inventory for entries with open gaps or failing validation.',
            'Open an entry to read its validation and monitoring.',
            'Export the report to circulate the findings.'
          ]
        }
      ]
    }
  ],
  [
    '/keys',
    {
      title: 'API keys',
      summary:
        'Issue and manage the API keys that authenticate calls to the decision API. Admin only.',
      capabilities: [
        'Create a key scoped to an environment and role.',
        'Rotate a key (with a grace window) or revoke it.',
        'See each key’s scope, role, and status.'
      ]
    }
  ],
  [
    '/audit',
    {
      title: 'Audit log',
      summary:
        'The immutable event trail for the workspace — who did what, when. Filters are shareable via the URL. Admin only.',
      capabilities: [
        'Filter by stream, type, actor, and time; the filter lives in the URL.',
        'Export the matching rows to CSV.',
        'Manage API tokens from the same page.'
      ]
    }
  ]
]);

// helpFor resolves the guide content for a route id (or undefined when none).
export function helpFor(routeId: string | null | undefined): PageHelp | undefined {
  return routeId ? HELP.get(routeId) : undefined;
}
