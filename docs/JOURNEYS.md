# User journeys

intraktible is an agentic decision platform. You author a **decision flow** (a graph
of nodes that turns an input into a verdict), version and deploy it per environment,
then run decisions against it through a test panel or the decision API. Flows that
need a human escalate to a **case**; predictive models and LLM agents plug into the
same flows; and the whole workspace is governed through versioning, four-eyes
promotion, monitoring, model-risk inventory, and an immutable audit log.

The core loop is: **build a flow → deploy it → run decisions → escalate to human
review where needed → govern the result.** Everything else (policies, pre-approvals,
models, agents, context data) hangs off that loop.

## Personas

A persona is *who is looking*, not a separate product. The platform is one API-first
application; selecting a persona re-prioritises the same data and surfaces for a role
— it reorders and relabels the navigation, picks a default landing page, surfaces the
stats and actions that role asks first, and applies a default lens (an initial filter)
on shared list pages. The pages, their data, and their capabilities are identical
across personas; only the emphasis changes. You switch persona from the account menu;
the choice is local to your browser. Persona is orthogonal to light/dark theme and to
your role — your role still gates what you can actually do (see [Roles](#roles-and-gating)).

---

## Core journeys

### Author and publish a decision flow

Spans: **Flows** (`/engine`) → **Flow builder** (`/engine/[flowId]`).

1. On Flows, enter a slug and name and click **Create flow**. Outcome: an empty flow
   with a single input node, version 1, nothing deployed.
2. Open the flow. On the builder canvas, add nodes from the palette and connect them
   from input to output. There are fourteen node types:

   | Node | What it does |
   | --- | --- |
   | **input** | the flow's entry point; exactly one per graph |
   | **assignment** | sets fields from expressions |
   | **rule** | when/then rules that write fields |
   | **split** | routes on one boolean condition, down a `yes` or a `no` edge |
   | **scorecard** | sums weighted factors into a score |
   | **decision table** | rows of conditions to outputs, under a hit policy |
   | **2d matrix** | looks a value up in a grid of rows × columns |
   | **code** | a sandboxed Starlark script |
   | **connect** | calls a registered connector for external data |
   | **predict** | runs a registered model |
   | **AI** | runs an agent |
   | **manual review** | escalates to a case for a human |
   | **reason** | emits adverse-action reason codes |
   | **output** | the flow's verdict; at least one per graph |

   Outcome: a working draft graph.
3. Select a node to edit its logic in the side panel — assignment expressions, a
   split's condition, the model or agent a node calls. Outcome: changes are held in
   the working draft, not yet versioned.
4. Run a **test decision** with sample input, choosing the environment to run it
   against (**sandbox** by default). Outcome: you see the path taken node-by-node and
   the resulting disposition. The run is recorded in the chosen environment, so you
   can inspect or export its trace. (For a dry run that records nothing, the decision
   API accepts `"preview": true`, which returns the full result with no decision
   recorded.)
5. Click **Publish**. Outcome: the draft becomes a new immutable version; `latest`
   advances. The active deployed version is what the decision API runs — publishing
   alone does not deploy. Publish is a dry compile: a graph that is disconnected,
   dead-ends, has a cycle, or has a split missing a branch is refused here rather
   than failing on a live decision.

### Promote with four-eyes

Spans: **Flow builder** (`/engine/[flowId]`), Deploy & versions tab.

1. Deploy a version to **sandbox** or **staging** directly. Outcome: that version is
   live in the chosen non-production environment immediately.
2. Request a **production** deployment. Production cannot be deployed directly; the
   request becomes a **pending** deployment request recorded in the audit trail.
   Outcome: a pending request awaiting approval.
3. Argue it where it will be audited: each request carries an **approval
   discussion** — the requester explains the change, the approver asks questions,
   and the eventual approve/reject reason lands on the request itself. Outcome: the
   reasoning is part of the governance record, not a side channel.
4. A *different* user with the **approver** role (or higher) approves it. The platform
   enforces four-eyes: the requester cannot approve their own request, and a
   non-approver is refused. Outcome: on approval the version goes live in production;
   on rejection it stays pending-rejected with the recorded reason.

You can also **roll back** an environment to its previously-live version, and
**schedule** a future deployment from the same tab.

### Run a decision and read its trace

Spans: **Flow builder** test panel or the decision API → **Decisions** (`/decisions`)
→ **Decision trace** (`/decisions/[decisionId]`).

1. Run a decision: either from the builder's test panel, or by calling
   `POST /v1/flows/{slug}/{env}/decide` with input data (a batch variant exists for a
   dataset). The engine walks the deployed graph: it threads the input record through
   assignment, predict, AI, and split nodes. A split evaluates one boolean condition
   and follows the edge labelled with the answer — `yes` or `no`. (Both edges are
   required at publish, so a live split can never take a branch that isn't wired. For
   more than two ways out, chain splits.) Outcome: a recorded decision with a status
   (`completed`/`failed`, or `suspended` while a manual-review node waits on a human),
   an output payload, reason codes, and — if a policy is bound
   to the flow — a **disposition** (`approve`, `decline`, or `refer`).
2. On Decisions, filter by flow, environment, status, variant (champion/challenger),
   or decision id to find the run. Outcome: a list showing status, disposition, and
   latency per run.
3. Open the decision. Outcome: the **trace** — the verdict and its reason codes, the
   node-by-node path with the branch taken at each split, and the input and output
   payloads at each step. You can export the trace, and if the flow routed to manual
   review, open the **case** it opened.

### Explain and challenge a decision

Spans: **Decision trace** (`/decisions/[decisionId]`) → **Flow builder** analysis tab.

1. On a declined or referred decision's trace, run **"What would change this?"**
   (the counterfactual). Outcome: the smallest input changes that flip the outcome
   (e.g. "income 52,000 → 61,500 ⇒ approve"), ordered by how little they move —
   adverse-action explainability beyond the recorded reason codes.
2. On the flow's builder, **Replay** animates a recent decision's path across the
   canvas node by node, and **Heatmap** tints each node by how often recorded
   decisions traverse it. Outcome: where traffic actually flows, at a glance.
3. Run **Coverage / red-team** on the builder's Test tab. Outcome: a synthetic fan of
   inputs sweeps the graph and reports dead branches and unreached nodes — the paths
   your traffic and your tests never exercise.

### Resume a suspended decision (durable human task)

Spans: a manual-review node with **suspend** on → **Case queue** → **Decision trace**.

1. A decision reaches a manual-review node configured to **suspend**. Outcome: the
   run pauses durably (status `suspended`), records its state, and opens a case
   carrying the decision context — it survives restarts and waits for a human.
2. Find it: the case links the decision, or filter Decisions by status `suspended`.
3. On the decision's trace, use the **Resume** panel to record the reviewer outcome
   (e.g. approve/decline and any fields the flow reads downstream). Outcome: the run
   continues from the pause point through the remaining nodes to a terminal status,
   with the post-pause trace appended to the same decision — and any later
   manual-review node still opens its own case.

### Manual review: a case from escalation to resolution

Spans: a flow's manual-review node → **Case queue** (`/cases`) → **Case**
(`/cases/[caseId]`), linking back to **Decision trace**.

1. A decision hits a **manual review** node. Outcome: the decision records a
   `MANUAL_REVIEW` reason code, and a case opens automatically in the queue with the
   decision's output as its context, an SLA (default 3 days), `needs_review` status,
   and a link back to its source decision.
2. On the Case queue, filter to `needs_review` and sort by urgency (soonest-due and
   overdue first). Run an **SLA sweep** to flag cases past their due time. Outcome: a
   prioritised work list.
3. Open the top case. Read its decision context and, if needed, open the source
   decision's trace. Outcome: full context for the review.
4. **Assign** the case (e.g. to yourself) and record **notes**. Outcome: assignment
   and each note land on the case's immutable activity trail. Assigning is a claim: a
   case someone else already owns is refused rather than silently taken, so two
   reviewers cannot both believe they own it. Taking one over has to be asked for
   (`"reassign": true`).
5. Set the **status** to resolve it. Outcome: the case leaves the open queue; the
   status change is recorded on the trail.

### Configure an AI agent, review its runs, escalate to a case

Spans: **Agent Manager** (`/agents`) → **Agent** (`/agents/[name]`) → **Case queue**.

1. On Agent Manager, define an agent: a prompt/system, a model, optional tools, and an
   optional structured-output schema. Outcome: a versioned agent with run/cost stats
   across the workspace.
2. Open the agent and **run** it (request/response or streamed) with a prompt.
   Outcome: an output recorded as a run, with the run added to the agent's history and
   its token/cost rollups.
3. Review the **run history** and the offline **eval** pass-rate (the agent scored
   against a set of eval cases). Outcome: a view of how the agent behaves and where it
   regresses across versions.
4. **Escalate** a run to a case for human review. Outcome: a case opens in the queue
   (`agent_review` type) referencing the originating run, and is worked exactly like a
   manual-review case. (Agents also run inside a flow via an **AI** node, where their
   output feeds downstream nodes.)

### Register a predictive model and monitor drift

Spans: **Models** (`/models`), referenced from a flow's **predict** node.

1. Define a model from a spec. Supported kinds: **logistic**, **GBM**, **expression**,
   or an **external** endpoint. Outcome: a model hosted as data, with an owner, ready
   to be called from a Predict node.
2. Expand the model's **Drift** readout on the Models page and **capture a baseline**.
   Outcome: the current score distribution is recorded as the reference for drift.
3. Set a drift monitor: alert when **PSI** (Population Stability Index) exceeds a
   threshold. Outcome: the model's drift status shows the current PSI versus the
   baseline and whether the monitor is firing; the drift state surfaces on the model
   list and in the model-risk inventory.

### Watch a flow with monitors and get alerted

Spans: **Flow builder** Monitors tab → **Notifications** (the bell) / webhooks.

1. On the flow's Monitors tab, add a rule over the flow's live metrics — failure
   rate, refer rate, automation rate, latency, volume, or distribution drift against
   a captured baseline — with an operator and threshold. Outcome: a monitor evaluated
   `ok`/`firing` at read time.
2. Subscribe a **webhook** (or rely on the in-app bell). Run a **check**. Outcome:
   monitors that crossed their threshold on the ok→firing edge deliver to active
   webhooks and the notifications inbox; each delivery is recorded.
3. Investigate from the alert: the flow's metrics strip, heatmap, and recent
   decisions show what moved. Fix the flow or its policy, republish, and watch the
   monitor return to `ok`.

### Run a champion/challenger experiment

Spans: **Flow builder** Deploy tab → **Decisions** → flow metrics.

1. Deploy a champion version and a **challenger** version with a traffic percentage
   (e.g. v3 champion, v4 challenger at 20%). Outcome: the decide path routes that
   share of traffic to the challenger; every decision records which **variant**
   served it.
2. On Decisions, filter by variant to compare arms; the flow's metrics break down
   completed/failed and dispositions per variant. Outcome: an evidence-based read on
   the challenger.
3. Promote the winner (deploy it as champion — production via four-eyes) or drop the
   challenger. Outcome: one version serves 100% again, and the experiment's decisions
   remain in history, tagged by variant.

### Batch decide a dataset, then promote it to pre-approvals

Spans: **Flow builder** Test tab → **Pre-approvals**.

1. On the builder's Test tab, paste a dataset (up to 500 rows) into **Batch decide**
   and run it against the sandbox. Outcome: each row is a REAL recorded decision
   (history, metrics, audit), with a per-row status report — unlike a backtest, which
   records nothing.
2. Use **Promote to pre-approvals**: the rows run through the flow's bound policy,
   and every row the policy approves becomes a **grant** keyed by an id field in the
   row, with the decision output stored as the grant's terms. Outcome: a population
   of standing approvals.
3. Subsequent decide calls for those entities are honored instantly from their
   grants (each grant counts its honors) until expiry or revocation.

### Author a policy, backtest it, publish

Spans: **Policies** (`/policies`).

1. Create a policy bound to a flow (by slug). A policy is the disposition layer over a
   flow's output: ordered **bands** that map a condition to `approve`, `decline`, or
   `refer`, with a default. Outcome: an empty, versioned policy.
2. Author the ordered bands (condition → disposition). The first matching band wins;
   the default applies when none match. Outcome: a draft band set.
3. **Preview impact**: paste a dataset of input rows (or click **Sample dataset**) and
   backtest the draft against it. Each row is replayed through the bound flow and
   disposed by the draft bands; nothing is recorded, and the dataset is capped at 2000
   rows. Outcome: the mix of dispositions the draft would produce, so you see the shift
   before publishing.
4. **Publish** the version. Outcome: a new immutable policy version; subsequent
   decisions on the bound flow carry the disposition it produces, with the matched
   band's reason code attached.

### Grant a pre-approval

Spans: **Pre-approvals** (`/preapprovals`).

1. Grant a pre-approval for an entity — a disposition (default `approve`), optional
   terms, an optional bound flow, and an expiry. Outcome: a standing decision for that
   entity.
2. The engine honours an active, unexpired grant **instantly instead of running the
   flow** for that entity. Outcome: faster turnaround for known-good entities; each
   grant tracks how often it has been honoured. (A batch path can derive grants from a
   dataset run.)
3. Review active, expiring, and revoked grants, and **revoke** a grant with a reason.
   Outcome: the grant stops being honoured; future calls run the flow again.

### Set up context data and call it from a flow

Spans: **Context data** (`/data`) → **Entity** (`/data/[type]/[id]`) → **Flow builder**.

1. Define a **connector** to an external source (pick one from the catalog, or an
   HTTP/GraphQL/SQL connector of your own). Credentials are sealed at rest and masked
   everywhere they are read back. Outcome: a named source a flow can fetch signals
   from (resolved before execution, so the decision core does no I/O).
2. Define a **feature** — an aggregation (count or sum) over an entity type's events
   within a time window. Outcome: a feature computed at read time from recorded events.
3. Register **entities** and record **events**. Open an entity to see its attributes,
   its event history, and the current value of each feature. Outcome: the data a
   decision sees when it runs for that entity.
4. In the flow builder, add a **connect** node and point it at the connector, naming
   the field its response lands in. Outcome: every decision through the flow fetches
   the signal first, and the fetch is recorded against the connector.

### Stream a batch of decisions

Spans: the decision API.

1. `POST /v1/flows/{slug}/{env}/decide/stream` with a newline-delimited JSON body —
   one input record per line. Outcome: one recorded decision per line, each result
   streamed back as NDJSON the moment it is ready, rather than buffered until the
   whole batch finishes. Use it for a population too large to hold in one request
   (`…/decide/batch` caps at 500 rows).

### Discuss a decision, a case, an agent, or a model

Spans: any detail page → the **notifications** bell.

1. Open the thing you want to talk about — a decision, a case, a flow, a policy, an
   agent, a model, an entity — and write a comment on it. `@mention` a colleague to
   address them. Outcome: the comment is recorded against that subject and visible to
   everyone who opens it.
2. The mentioned person sees an unread count on the bell, opens their inbox, and
   follows the link back to the subject. Outcome: a review conversation lives next to
   the thing under review, not in a separate tool.

### Move a flow between workspaces (flow as code)

Spans: **Flows** (`/engine`) → **Flow builder**.

1. Export a flow version: `GET /v1/flows/{flow_id}/export` returns the graph as a
   document (the builder also exports Mermaid, BPMN, Graphviz DOT). Outcome: the flow
   as a file you can review, diff, and keep in version control.
2. Import it elsewhere with `POST /v1/flows/import` (or `/import-bundle` for several
   at once). Re-importing an identical document is a no-op; a changed one publishes a
   new version on the same slug. Outcome: the same flow, versioned, in the target
   workspace. Import runs the same publish-time dry compile, so a broken graph is
   refused at the boundary.

### Shadow-evaluate a candidate version on live traffic

Spans: **Flow builder** (`/engine/[flowId]`), Deploy & versions tab.

1. Set a **shadow** version for an environment (`PUT /v1/flows/{flow_id}/shadow`).
   Outcome: every live decision in that environment also runs through the shadow
   version, over the same input; the shadow's result is recorded and never returned to
   the caller. (A decision suspended for human review has no verdict yet, so nothing
   is compared.)
2. Read the divergence report: how often the shadow agreed with the live version.
   Outcome: evidence about how a candidate would behave on real traffic, before it
   takes any.

### Restrict who may change a flow

Spans: **Flow builder** (`/engine/[flowId]`).

1. Add a **grant** on the flow (`POST /v1/flows/{flow_id}/grants`) naming an actor and
   one environment they may change it in. Outcome: change control narrower than a role
   — an editor with no grant on this flow cannot deploy it there.
2. Set a **promotion policy** per environment: whether assertions must pass, whether
   firing monitors block, and whether a promotion may be forced. Production always
   requires review and never allows a force. Outcome: the gates each stage enforces,
   recorded on the flow.

### Erase a subject's data (right to erasure)

Spans: the erasure API. *Admin only.*

1. `GET /v1/erasure/subjects` lists the subjects the workspace holds data for. Outcome:
   what you would be erasing.
2. `POST /v1/erasure/subjects/{subject}` erases one: the subject's key is destroyed,
   so everything sealed under it — in the event log and in every projection — becomes
   unrecoverable, while the log itself stays append-only. Outcome: crypto-shredded
   personal data with the shape of the audit trail intact. `POST /v1/erasure/retention`
   does the same for every subject older than a retention limit.

### Govern the workspace

Spans: **Model risk** (`/mrm`), **Observability** (`/observability`), **Audit log**
(`/audit`), **API keys** (`/keys`). The MRM, audit, and API-keys pages are **admin
only** — they are hidden from a non-admin's navigation and home, and the pages gate
on the server as well.

- **Model risk (MRM).** The SR 11-7 / SS1/23 model inventory in one register: every
  flow, predictive model, and agent, each with its validation coverage (assertions for
  flows, a drift baseline for models, eval cases for agents), live monitoring (success
  rate, firing monitors, drift PSI), and any open governance gaps. Scan for entries
  with open gaps or failing validation, open an entry to read its evidence, and export
  the report (CSV or Markdown). *Admin only.*
- **Observability.** The operational view across flows: set a success and latency
  objective (SLO) per flow and read attainment and remaining error budget, see AI
  usage and cost by model, and read how distributed tracing is emitted.
- **Audit log.** The immutable, event-sourced trail of who did what, when — flow
  publishes and deployments, decisions, case activity, key changes, and more. Filter
  by stream, type, actor, and time (the filter lives in the URL and is shareable),
  and export matching rows to CSV. *Admin only.*
- **API keys.** Issue and manage the keys that authenticate decision-API calls. Create
  a key scoped to an environment and role, rotate it with a grace window, or revoke
  it. *Admin only.*

---

## By persona

Each persona below lives in a subset of the journeys above. The persona sets the
default landing page, the navigation order, the home stats/actions, and the initial
lens on shared lists.

| Persona | Label | Lands on | Lives in |
| --- | --- | --- | --- |
| builder | Workflow Designer | Builder home | Author & publish flows; policies; context data; models |
| developer | Developer / Integrator | Persona home | Run decisions & read traces; API keys; agents |
| operator | Risk Operator | Operator home | Manual review queue; pre-approvals; decisions |
| manager | Team Manager | Persona home | Four-eyes approvals; case load; audit |
| product | Product / Experimentation | Persona home | Policy backtests; A/B variants; models |
| showcase | Executive | Showcase home | KPIs, trends, governance posture |
| evaluator | Evaluator / Guest | Evaluator home | Guided look at builder, decisions, cases |

- **Workflow Designer (builder).** Spends the day on the canvas: authoring and
  versioning flows, wiring policy bands and context data, and referencing models from
  Predict nodes. Default persona. Lives in *Author and publish a flow*, *Author a
  policy*, and *Set up context data*.
- **Developer / Integrator.** Integrates the decision API and debugs. The decisions
  list is relabelled **Traces** and lands on failing traces, leading with
  status/duration/environment. Manages API keys and agents. Lives in *Run a decision
  and read its trace* and the agent journeys.
- **Risk Operator.** Works the queues. Lands on the open case queue, most-urgent
  first, and clears it; reviews pre-approvals and scans recent decisions. Lives in
  *Manual review* and *Grant a pre-approval*.
- **Team Manager.** Watches throughput and governance. Home stats lead with pending
  approvals, cases needing review, and overdue cases; reviews the audit trail. Lives
  in *Promote with four-eyes* (as the approver) and the queue/oversight side of
  *Manual review*.
- **Product / Experimentation.** Tunes impact. Lands on the **challenger** variant of
  decisions, leading with the variant column; backtests flows and policy changes and
  manages models. Lives in *Author a policy → backtest* and *Register a model*.
- **Executive (showcase).** Reads posture, not detail: decision volume, trends, case
  health, and governance (MRM/audit, when admin). Lives in the read side of
  *Govern the workspace*.
- **Evaluator / Guest.** A guided, minimal surface (builder, decisions, cases) for
  exploring the platform without a role's clutter. Walks an abbreviated version of the
  build → decide → review loop.

---

## Roles and gating

Actions are gated by role, ranked **viewer < operator < editor < approver < admin**.
A higher role includes the rights below it.

| Role | Can |
| --- | --- |
| viewer | Read-only across surfaces |
| operator | Work cases (assign, note, set status), run decisions |
| editor | Author and publish flows, policies, models, agents, context data |
| approver | Everything an editor can, plus approve/reject production deployment requests |
| admin | Everything, plus model risk, audit log, and API-key management |

Two gates matter most. **Four-eyes promotion**: approving a production deployment
requires the approver role *and* a different actor than the requester. **Admin-only
surfaces**: model risk, audit, and API keys are hidden from a non-admin's navigation
and home (so no dead-end 403s) and are enforced server-side as defence in depth.

Role gates what you *can do*; persona gates what you *see first*. They are independent:
any role can run under any persona.

## The in-app page guide

Every page has a built-in guide. Click the **?** button in the header to open the
guide for the current page — a one-line summary, the things you do on that page, and
its key flows, named to match the on-screen controls. This documentation is the
end-to-end view; the in-app guide is the per-page view.
