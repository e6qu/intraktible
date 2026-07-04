// SPDX-License-Identifier: AGPL-3.0-or-later
// Per-page guide content, keyed by SvelteKit route id ($page.route.id) — the stable,
// base-path-independent key. One entry per page; adding a page = adding one entry.
//
// Style rules (keep the guide scannable + dispassionate):
//  - summary: 1–2 sentences, what the page is FOR (not its features).
//  - capabilities: 3–6 verb-first bullets, one line each, things you do on THIS page.
//  - journeys: one per distinct flow a user performs on the page (≤10), each 3–7
//    steps; a step is one action and its outcome. Every page documents every flow —
//    /login is the sole journey-less exception (a single form, nothing to walk).
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
      ],
      journeys: [
        {
          name: 'View the platform as a different role',
          steps: [
            'Open the account menu in the header (your avatar and persona name).',
            'Under View as, pick a persona — a check marks the active one.',
            'Return here: the dashboard now leads with that persona’s tiles, queues, and shortcuts.'
          ]
        },
        {
          name: 'Jump into today’s work',
          steps: [
            'Scan the readout tiles for anything failing, pending, or overdue.',
            'Open a queue panel (for example needs review) to land on the underlying page.',
            'Use Search (⌘K) in the header when the surface you want is not on this deck.'
          ]
        }
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
        'Create a flow from scratch, from a template, from an AI draft, or import one as code.',
        'See each flow’s latest version and what is deployed to sandbox and production.',
        'Open a flow to build, version, test, and deploy it.'
      ],
      journeys: [
        {
          name: 'Start a flow from scratch',
          steps: [
            'Enter a Slug (for example loan-origination) and a Name, then click Create flow.',
            'You land in the builder — add nodes from the left rail and wire them input → output.',
            'Click Publish version to create v1.'
          ]
        },
        {
          name: 'Draft a flow with AI',
          steps: [
            'Open Draft a flow with AI.',
            'Describe the decision logic you want, or click an example prompt.',
            'Click Generate flow and review the drafted nodes.',
            'Click Open in builder → to load the draft on the canvas, or Discard it.'
          ]
        },
        {
          name: 'Start from a template',
          steps: [
            'Open New from template.',
            'Read each card’s purpose and node-type chips.',
            'Click Use template — the flow is created and the builder opens.'
          ]
        },
        {
          name: 'Import a flow as code',
          steps: [
            'Open Import flow (as code).',
            'Paste flow JSON (the builder’s Export → JSON) or choose a .json file — a bundle of flows also works.',
            'Click Import — the toast reports what was created or updated and at which version.'
          ]
        },
        {
          name: 'Check what is live where',
          steps: [
            'Search flows by name or slug.',
            'Read the Latest, Sandbox, and Production columns — not deployed and the ↑ vN ready pill flag gaps.',
            'Click a flow’s Name to open the builder and deploy from there.'
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
        'Build a decision flow as a graph of nodes, then version, test, deploy, and monitor it. The active version is what the decision API runs.',
      capabilities: [
        'Compose the graph on the canvas: add nodes from the left rail, wire them by dragging between handles, edit each node in the inspector.',
        'Test-run, backtest, what-if, assert, and batch-decide from the Test & analyze tab.',
        'Publish the draft as a new immutable version; diff any two versions.',
        'Deploy per environment, promote to production with four-eyes review, or roll back.',
        'Watch the flow with monitors and webhooks; draft logic with the copilot.'
      ],
      journeys: [
        {
          name: 'Author a flow from a blank board',
          steps: [
            'Click a node type on the left rail — or drag it onto the board — starting with Input.',
            'Add the logic nodes (Rule, Split, Scorecard, Decision table…) and finish with an Output node.',
            'Wire nodes by dragging between their handles; label a split’s outgoing edges via the edge inspector’s branch field.',
            'Select a node to open the node inspector and edit its name, lane, and type-specific config — edits apply to the draft as you type.',
            'Click Auto-layout to tidy the board.',
            'Click Publish version — the draft becomes the new immutable version.'
          ]
        },
        {
          name: 'Test-run and read the verdict',
          steps: [
            'Open the Test & analyze tab.',
            'Pick an environment; tick Preview (don’t record) for a dry run that records nothing.',
            'Click Sample input to prefill from the input schema, or edit the input JSON yourself.',
            'Click Run and read the verdict card: disposition, status, reason codes, and duration.',
            'For a recorded run, follow View the recorded decision → to its full trace.'
          ]
        },
        {
          name: 'Backtest and what-if a change',
          steps: [
            'In Backtest, click Sample dataset for a varied 8-row set from the input schema, or paste your own JSON array.',
            'Optionally set compare version to diff the draft against a published version.',
            'Click Run backtest — read the completed, failed, and changed counts and the per-row Baseline vs Candidate table; nothing is recorded.',
            'For one input, use What-if: click Sample sweep (or name a field and list values yourself), then Run what-if to see where the outcome flips.'
          ]
        },
        {
          name: 'Guard the flow with assertions',
          steps: [
            'In Assertions, write cases as a JSON array of {name, input, expect}.',
            'Click Save tests, then Run tests.',
            'Read the pass/fail table — failing assertions block a promote unless you force it.'
          ]
        },
        {
          name: 'Decide a batch',
          steps: [
            'In Batch decide, click Sample dataset or paste a JSON array of input rows (up to 500).',
            'Click Run batch — every row records a real decision on the environment chosen in Test run.',
            'Read the decided, completed, failed, and rejected counts; open any row’s view link for its trace.',
            'Optionally promote outcomes to standing grants: set Entity type, Key field, and Grant on under Promote to pre-approvals, then click Promote.'
          ]
        },
        {
          name: 'Deploy and promote across environments',
          steps: [
            'Open Deploy & versions — the Live strip shows what each environment runs.',
            'Pick a version and an environment, then click Deploy; sandbox and staging apply immediately.',
            'Or use the Promote row to carry a version from one environment to the next.',
            'Click rollback next to an environment to revert it to its previous live version.'
          ]
        },
        {
          name: 'Ship to production with four-eyes',
          steps: [
            'Choose production (four-eyes) as the target and click Propose for review — a pending request appears under Deployment requests.',
            'A different user with the approver role opens the request and clicks Approve or Reject — the proposer cannot approve their own request.',
            'They record a decision reason and click Confirm approve — the version goes live in production.'
          ]
        },
        {
          name: 'Watch the flow with monitors',
          steps: [
            'Open the Monitors tab and click Capture baseline to snapshot the current output distribution.',
            'Pick a Metric (failure_rate, refer_rate, distribution_drift_psi…), a When and a Threshold, then click Add monitor.',
            'Click Check & notify — each monitor reads firing, ok, or no data with its current value.',
            'Add an endpoint under Notification webhooks to push alerts when a monitor fires.'
          ]
        },
        {
          name: 'Draft logic with the copilot',
          steps: [
            'Open the Copilot tab.',
            'Click Explain this flow for a plain-language readout, or describe the logic you want in the prompt.',
            'Click Suggest logic for advice, or Generate & apply a flow to load a server-validated graph onto the canvas.',
            'Review the generated draft, then click Publish version to keep it.'
          ]
        },
        {
          name: 'Work the canvas faster',
          steps: [
            'Press v for the Select tool (drag to marquee-select; middle/right-drag pans) or h for Pan.',
            'Press f to toggle Focus — the board takes over the viewport; Esc exits.',
            'Press t to toggle the Tools panel for list-based node and edge editing.',
            'Click Collapse to shrink the board to a summary bar; click the bar to expand it again.'
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
        'Filter by flow, environment, status (including suspended), variant, or decision id.',
        'See status, disposition, and latency per run.',
        'Open a decision to read its node-by-node trace.'
      ],
      journeys: [
        {
          name: 'Find a run',
          steps: [
            'Set Flow (slug), Env, Status, or Variant — or paste an id fragment into Filter by ID.',
            'Click Apply — the match count updates.',
            'Page with ← Prev / Next → (25 per page) and click a row’s flow link to open its trace.'
          ]
        },
        {
          name: 'Triage suspended decisions',
          steps: [
            'Set Status to suspended and click Apply.',
            'Open a run — suspended means it is paused at a human task, durable in the event log.',
            'Resume it from the trace page with Approve, Decline, or Refer.'
          ]
        }
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
        'Resume a suspended decision; ask what single change would flip a decline.',
        'Inspect the input and output payloads, and export the trace.',
        'Open the case it opened, if it routed to manual review.'
      ],
      journeys: [
        {
          name: 'Read a trace',
          steps: [
            'Read the verdict card: the disposition, its reason, and a pre-approval tag when a stored grant was honoured.',
            'Check Reason codes for the adverse-action detail.',
            'Follow the Node trace timeline — each step is a node, its output, and at a split the branch taken.',
            'Compare the Input and Output panels; the fields list links to the flow, the policy, and the opened case.'
          ]
        },
        {
          name: 'Resume a suspended decision',
          steps: [
            'A suspended run shows the Paused for human review panel.',
            'Click Approve, Decline, or Refer to record the outcome.',
            'The same decision resumes and completes — the toast reads Resumed with your outcome.'
          ]
        },
        {
          name: 'Ask what would change it',
          steps: [
            'On a declined or referred decision, find What would change this?.',
            'Click Find what would flip it.',
            'Read the flip rows — each is the smallest single-field change that flips the outcome, all else held equal.'
          ]
        },
        {
          name: 'Export the trace',
          steps: [
            'Scroll to Export trace.',
            'Pick a format: Sequence (Mermaid), DOT (Graphviz), or JSON.',
            'Click the format to download it, or its copy button to copy to the clipboard.'
          ]
        }
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
        'Preview a draft’s impact against replayed decisions before publishing.'
      ],
      journeys: [
        {
          name: 'Create a policy',
          steps: [
            'Enter a policy name and pick the flow it governs.',
            'Click Create policy — it appears in the table without a version.',
            'Click Edit bands on its row to open the band editor.'
          ]
        },
        {
          name: 'Edit bands and preview the impact',
          steps: [
            'Click Add band and fill each rule: a when expression over the flow’s output, a disposition (approve, decline, or refer), and a code + description.',
            'Order matters — the first matching band wins; set the default disposition for when nothing matches.',
            'Click Sample dataset (rows that exercise every band) or paste your own JSON array, then Preview impact — nothing is recorded.',
            'Read the draft vs published table: the approve / decline / refer / failed mix and how many rows would change disposition.'
          ]
        },
        {
          name: 'Publish a version',
          steps: [
            'Check the band preview — “This policy reads, in order:” lists exactly what will apply.',
            'Click Publish version — subsequent decisions on the flow use it.',
            'Open History on the row to see published versions with their disposition mix and author.'
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
        'See active, expired, and revoked grants and how often each has been honoured.',
        'Revoke a grant with a reason.'
      ],
      journeys: [
        {
          name: 'Grant a pre-approval',
          steps: [
            'Under Grant, set the Entity type and Entity ID and pick the Disposition (approve or decline).',
            'Optionally bind it to a flow and set Valid for (days).',
            'Edit Terms — the JSON returned as the decision output when the grant is honoured.',
            'Click Grant pre-approval — it appears under Existing with status active.'
          ]
        },
        {
          name: 'Watch grants being honoured',
          steps: [
            'Read the Honored column — how many decide calls each grant answered without running the flow.',
            'Check Expires and Status: active, expired, or revoked.',
            'A decision honoured this way carries a pre-approval tag on its trace.'
          ]
        },
        {
          name: 'Revoke a grant',
          steps: [
            'Click Revoke on an active row.',
            'Type a Revoke reason.',
            'Click Confirm revoke — the status flips to revoked and the engine stops honouring it.'
          ]
        }
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
        'Browse entities as decisions and events reference them; open one to see its computed features.'
      ],
      journeys: [
        {
          name: 'Define a connector',
          steps: [
            'Optionally click a template chip under Start from a template to prefill the form.',
            'Enter a name, pick a type (http, graphql, sql, plaid, stripe…), and set the config JSON.',
            'Click Define connector — a flow’s Connect node now calls it by name.'
          ]
        },
        {
          name: 'Define a feature',
          steps: [
            'Set Name, Entity, and Event, then pick an Aggregation — count, or sum with its Field.',
            'Set Window (hours) and read the plain-language preview line under the form.',
            'Click Define feature — Rule and Split nodes can read it as features.<name>.'
          ]
        },
        {
          name: 'Browse an entity',
          steps: [
            'Find it under Entities — entities appear when a decision references one, an event records one, or you create one via the API.',
            'Click its ID to open the entity page.',
            'Read its attributes, computed features, and event timeline there.'
          ]
        }
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
      ],
      journeys: [
        {
          name: 'Inspect what a decision sees',
          steps: [
            'Read Attributes — stored key/values that accrue as decisions and events reference the entity.',
            'Check Computed features — the current value of each defined feature for this entity.',
            'Walk the Event timeline to see the raw events those features aggregate.'
          ]
        }
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
        'Filter by status; the queue orders by urgency (soonest-due and overdue first).',
        'Select rows to bulk-assign or bulk-complete.',
        'Open a case to see its decision context and history, or open one by hand.',
        'Run an SLA sweep to flag cases past their due time.'
      ],
      journeys: [
        {
          name: 'Work the open queue',
          steps: [
            'Set status to needs_review — the summary bar shows Due soon and Overdue counts.',
            'Open the top case via its Company link and read its context.',
            'Click Assign to me on the case if it is unassigned.',
            'Record notes, set the status, and resolve it — the case leaves the open queue.'
          ]
        },
        {
          name: 'Bulk-assign or close cases',
          steps: [
            'Tick the checkbox on each row, or the header checkbox to select all.',
            'In the bulk bar, type an assignee and click Assign.',
            'Or click Mark completed to resolve the whole selection after the confirm.',
            'Click clear to drop the selection.'
          ]
        },
        {
          name: 'Sweep SLAs',
          steps: [
            'Click Run SLA sweep (operator role).',
            'Overdue open cases are flagged as SLA-breached; the toast counts them.',
            'Watch the Overdue and Due soon tiles and the Days left column to catch the next breaches early.'
          ]
        },
        {
          name: 'Open a case by hand',
          steps: [
            'Enter Company, Type, and SLA days.',
            'Click Open case — it joins the queue as needs_review.',
            'Assign and work it like any escalated case.'
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
      ],
      journeys: [
        {
          name: 'Review and resolve',
          steps: [
            'Read the header badges (status, SLA urgency) and the Context facts; follow source decision for the full trace.',
            'Click Assign to me, or type an assignee and click Assign.',
            'Add notes as you investigate — each lands in the Notes list with its author.',
            'Set status to in_progress while working, then click ✓ Resolve case — the outcome is recorded.'
          ]
        },
        {
          name: 'Audit the trail',
          steps: [
            'Read Notes for the reviewer commentary, with authors and times.',
            'Read Activity — an immutable timeline of every action on the case with its actor.',
            'Follow source decision to replay the run that opened the case.'
          ]
        }
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
      ],
      journeys: [
        {
          name: 'Define an agent',
          steps: [
            'Open + Define agent.',
            'Set the Name; optionally pin a Provider and Model.',
            'Write the System prompt, list Tools comma-separated, and paste an Output schema (JSON Schema) for structured output.',
            'Click Define agent — a flow’s AI node can now reference it by name.'
          ]
        },
        {
          name: 'Watch usage and cost',
          steps: [
            'Read the summary chips: Runs, Completed, Failed, Tokens, and Cost when pricing is configured.',
            'Scan the table’s Runs column for volume per agent.',
            'Click an agent’s Name to drill into its runs, versions, and evals.'
          ]
        }
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
      ],
      journeys: [
        {
          name: 'Run the agent',
          steps: [
            'Type a prompt and click Run (operator role).',
            'Read the output panel: the status badge, the run id, and the response text.',
            'For token-by-token output, use Stream a run: type the prompt, pick SSE or WebSocket, and click Stream.'
          ]
        },
        {
          name: 'Score it against an eval set',
          steps: [
            'Under Offline eval, edit the JSON array of cases ({name, prompt, mode, expect}).',
            'Click Save cases, then Run eval — golden cases record nothing.',
            'Read the pass-rate: passed/total, the percentage badge, and the per-case ✓/✗ list with failure detail.'
          ]
        },
        {
          name: 'Escalate a run to a human',
          steps: [
            'Find the run under Runs.',
            'Click Escalate and confirm — a review case opens.',
            'Follow → Open case to work it in the Cases queue.'
          ]
        }
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
        'Capture a baseline and set a drift (PSI) monitor per model.'
      ],
      journeys: [
        {
          name: 'Define a model',
          steps: [
            'Enter a Name and click a starter (logistic, gbm, expression, external) to load a spec.',
            'Edit the Spec (JSON) — the kind and its parameters live in the spec.',
            'Click Define model — a flow’s Predict node can reference it by name.'
          ]
        },
        {
          name: 'Baseline the model and watch drift',
          steps: [
            'Click Drift on the model’s row to expand its drift panel.',
            'Once predictions exist, click Capture baseline — PSI compares later traffic against it.',
            'Set alert PSI > and click Set monitor to alert when drift crosses the threshold.',
            'Read the panel: the PSI value and its label (stable, moderate shift, significant drift), the decile histogram, and ⚠ firing when over threshold.'
          ]
        }
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
      ],
      journeys: [
        {
          name: 'Set an objective for a flow',
          steps: [
            'Find the flow’s card under Service-level objectives.',
            'Click Set objective (or Edit objective on a tracked flow).',
            'Enter Success target % and Latency target ms, then click Set objective.'
          ]
        },
        {
          name: 'Read attainment',
          steps: [
            'Each card’s badge reads meeting or breaching.',
            'Compare Success against its target and check how much Error budget is left.',
            'Check Latency against the ≤ ms target; Clear objective stops tracking a flow.'
          ]
        },
        {
          name: 'Track AI spend',
          steps: [
            'Read the AI usage summary: Runs, Prompt tokens, Completion tokens, and Cost.',
            'Scan the per-model table to see which model drives the usage.',
            'If Cost is missing, set INTRAKTIBLE_AI_PRICES to attribute dollar cost — tokens show regardless.'
          ]
        }
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
            'Read the summary strip: models by kind, Deployed, Unvalidated, and Open issues.',
            'Scan the Issues column — red entries are open gaps; a green ✓ means none.',
            'Check Validation (assertion, eval, and shadow coverage) and Monitoring (PSI, firing monitors, SLO met or breach) per row.',
            'Click an entry’s Model name to open the underlying flow, agent, or model and close the gap.'
          ]
        },
        {
          name: 'Export the report',
          steps: [
            'Click Export CSV for a spreadsheet, or Export Markdown for a document.',
            'The file reflects the inventory at the As of timestamp shown.',
            'Attach it to your validation pack or circulate it to stakeholders.'
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
        'Rotate a key (the old secret keeps working for an hour) or revoke it.',
        'See each key’s scope, role, and status.',
        'Copy a ready-made curl command for the decision API.'
      ],
      journeys: [
        {
          name: 'Mint a key and call the API',
          steps: [
            'Set Name and Actor, pick a Role and Scope, and optionally an expiry.',
            'Click Create key — the secret is shown once.',
            'Click Copy in the revealed-secret box; the secret is stored hashed and cannot be retrieved again.',
            'Use the curl command shown (also under How you’ll call the decision API): X-Api-Key against /v1/flows/{slug}/{env}/decide.'
          ]
        },
        {
          name: 'Rotate a key',
          steps: [
            'Click Rotate on the key’s row.',
            'Copy the new secret from the one-time box.',
            'Swap it into callers — the previous secret keeps working until the timestamp shown (a one-hour grace).'
          ]
        },
        {
          name: 'Revoke a key',
          steps: [
            'Click Revoke on the key’s row.',
            'Its Status flips to revoked and calls with it fail immediately.',
            'Mint a replacement if a service still needs access.'
          ]
        }
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
        'Filter by stream, type, actor, resource, and time; the filter lives in the URL.',
        'Expand any event to its JSON payload; include or hide per-node decision steps.',
        'Export the matching rows to CSV.',
        'Manage API tokens and PII masking from the same page.'
      ],
      journeys: [
        {
          name: 'Filter the trail',
          steps: [
            'Set any of stream, actor, event type, resource id, and a from / to time.',
            'Node steps are hidden by default; untick Hide node steps to include per-node decision.run.node_evaluated events.',
            'Click Apply — the filter is written to the URL, so the view is shareable and bookmarkable.',
            'Page through the matches with ← Prev / Next →.'
          ]
        },
        {
          name: 'Inspect an event',
          steps: [
            'Find the row: When, Actor, Stream, Event.',
            'Click view under Details to expand the JSON payload.',
            'Untick Hide node steps (hidden by default) to see a decision’s node-by-node evaluation as individual events.'
          ]
        },
        {
          name: 'Export to CSV',
          steps: [
            'Apply the filter you want to export.',
            'Click CSV — the download matches the applied filter, not just the visible page.',
            'Open audit.csv — one row per event with its payload.'
          ]
        },
        {
          name: 'Manage API tokens here',
          steps: [
            'Open the API tokens panel.',
            'Set name, actor, role, scope, and an optional expiry, then click Create token; copy the one-time secret.',
            'Rotate or Revoke a token from its row; its Audit link filters the trail to that token’s actor.'
          ]
        }
      ]
    }
  ],
  [
    '/hello',
    {
      title: 'Phase 0 vertical slice',
      summary:
        'A live, minimal demo of the backbone: one command through the event log, a projection, the API, and this UI.',
      capabilities: [
        'Record a hello command and see its sequence number.',
        'Refresh the stats projection to watch the count advance.'
      ],
      journeys: [
        {
          name: 'Exercise the backbone',
          steps: [
            'Type a name and click Say hello — a command is recorded to the event log.',
            'Read the output: POST /v1/hello → seq N and the response JSON.',
            'Click Refresh to re-read the stats projection and see the count advance.'
          ]
        }
      ]
    }
  ],
  [
    '/login',
    {
      title: 'Sign in',
      summary:
        'Exchange an API key for a session cookie; the rest of the UI authenticates with that cookie.',
      capabilities: [
        'Enter an API key and click Sign in — you land on the dashboard.',
        'Sign in with a configured SSO provider from the buttons below the form.'
      ]
    }
  ]
]);

// helpFor resolves the guide content for a route id (or undefined when none).
export function helpFor(routeId: string | null | undefined): PageHelp | undefined {
  return routeId ? HELP.get(routeId) : undefined;
}
