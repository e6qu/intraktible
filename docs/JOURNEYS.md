<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->

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

A persona is _who is looking_, not a separate product. The platform is one API-first
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

   | Node               | What it does                                                 |
   | ------------------ | ------------------------------------------------------------ |
   | **input**          | the flow's entry point; exactly one per graph                |
   | **assignment**     | sets fields from expressions                                 |
   | **rule**           | when/then rules that write fields                            |
   | **split**          | routes on one boolean condition, down a `yes` or a `no` edge |
   | **scorecard**      | sums weighted factors into a score                           |
   | **decision table** | rows of conditions to outputs, under a hit policy            |
   | **2d matrix**      | looks a value up in a grid of rows × columns                 |
   | **code**           | a sandboxed Starlark script                                  |
   | **connect**        | calls a registered connector for external data               |
   | **predict**        | runs a registered model                                      |
   | **AI**             | runs an agent                                                |
   | **manual review**  | escalates to a case for a human                              |
   | **reason**         | emits adverse-action reason codes                            |
   | **output**         | the flow's verdict; at least one per graph                   |

   Outcome: a working draft graph.

3. Select a node to edit its logic in the side panel — assignment expressions, a
   split's condition, the model or agent a node calls. Outcome: changes are held in
   the working draft, not yet versioned.
4. Click **Publish**. Outcome: the draft becomes a new immutable version; `latest`
   advances. The active deployed version is what the decision API runs — publishing
   alone does not deploy. Publish is a dry compile: a graph that is disconnected,
   dead-ends, has a cycle, or has a split missing a branch is refused here rather
   than failing on a live decision.
5. Run a **test decision** with sample input, choosing the environment to run it
   against (**sandbox** by default). The Test panel deliberately exercises the
   published/deployed version in that environment, not any newer in-memory canvas
   edits; when the draft is dirty the UI says so explicitly. Outcome: you see the
   path taken node-by-node and the resulting disposition. The run is recorded in the
   chosen environment, so you can inspect or export its trace. (Tick **Preview** for
   the same published version without recording a decision.)

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
4. A _different_ user with the **approver** role (or higher) follows the shared
   approval notification to this request and approves it. The platform
   enforces four-eyes: the requester cannot approve their own request, and a
   non-approver is refused. Outcome: on approval the version goes live in production;
   on rejection it stays pending-rejected with the recorded reason.

You can also **roll back** an environment to its previously-live version, and
**schedule** a future deployment from the same tab. A future production window
enters the same maker-checker queue; approval creates a pending schedule rather than
deploying early. The scheduler activates it at `at`, reverts a time-box at `until`,
and canceling an active window restores its captured baseline without overwriting a
newer deliberate deployment.

### Run a decision and read its trace

Spans: **Flow builder** test panel or the decision API → **Decisions** (`/decisions`)
→ **Decision trace** (`/decisions/[decisionId]`).

1. Run a decision: either from the builder's test panel, or by calling
   `POST /v1/flows/{slug}/{env}/decide` with input data (a batch variant exists for a
   dataset). For retried integrations, supply an `Idempotency-Key`; the same
   payload returns the original logical decision while a conflicting reuse is
   refused. Business reference, correlation id, entity coordinates, bounded
   metadata, and a timeout can be attached at admission. The engine walks the deployed
   graph: it threads the input record through assignment, predict, AI, and split nodes.
   Connect, Predict, and AI calls occur only when their node is actually reached,
   receive the record after every upstream mutation, and leave durable
   requested/succeeded/failed evidence. A split evaluates one boolean condition
   and follows the edge labelled with the answer — `yes` or `no`. (Both edges are
   required at publish, so a live split can never take a branch that isn't wired. For
   more than two ways out, chain splits.) Outcome: a recorded decision with a status
   (`completed`/`failed`, or `suspended` while a manual-review node waits on a human),
   an output payload, reason codes, and — if a policy is bound
   to the flow — a **disposition** (`approve`, `decline`, or `refer`).
   Outside sandbox, the deployed graph must pin every AI node to a positive,
   immutable agent version, and the engine uses only the policy version a separate
   checker approved. A suspended decision records its exact policy selection and reuses it after human review
   rather than re-reading whatever is current at resume time.
2. On Decisions, filter by flow, environment, status, business/correlation
   reference, entity, scalar metadata, or free-text decision id to find the run.
   Outcome: a list showing status, disposition, and latency per run. Running,
   retrying, suspended, abandoned, failed, and completed are distinct operational
   states.
3. Open the decision. Outcome: the **trace** — the verdict and its reason codes, the
   node-by-node path with the branch taken at each split, input/output payloads at
   each step, caller tracking fields, recovery owner/attempt/error, and effect
   delivery evidence. You can export the trace, and if the flow routed to manual
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
   your traffic and your tests never exercise. For an effectful graph, provide
   explicit dependency mocks in the Test section; Coverage records nothing,
   never calls a connector/model/agent, reports failed synthetic runs separately,
   and refuses a reached dependency with no mock.

### Screen fair-lending impact and serve an adverse-action notice

Spans: **Fair lending** (`/fairlending`) → **Decision trace**
(`/decisions/[decisionId]`) → **Compliance** (`/compliance`).

1. On Fair lending, choose the protected-class attribute explicitly and run the
   four-fifths screen for a flow. Optionally select an exact experiment, cohort, and
   arm to inspect a governed treatment population without blending unlike versions.
   The platform does not infer protected class, and the report is a screening signal
   rather than a legal conclusion. Outcome: selection rates and impact ratios by
   group, with the configured threshold and cohort dimensions called out.
2. Configure the workspace creditor identification used in notices. If a notice is
   based on a consumer report, also configure the reporting agency; that path fails
   loudly rather than producing an incomplete FCRA disclosure.
3. In the flow, use a **reason** node to produce specific principal reasons and bind a
   policy that can decline. Outcome: every adverse decision carries the reasons that
   will appear in its notice.
4. Open Compliance and work the **Adverse-action queue**, ordered with the age of the
   decline so the 30-day clock is visible. Follow a row to the decision, preview or
   download the rendered notice, choose the delivery method, and **Record as issued**.
   Outcome: an immutable issuance record captures who served it, when, how, the
   principal reasons, whether a consumer report was involved, and the exact rendered
   Markdown plus its hash; the decision leaves the pending queue. **Download issued
   artifact** always returns those retained bytes, while **Download current preview**
   deliberately re-renders current settings. A settings/template change or full
   projection replay cannot alter the issued document.

### Contest an automated decline and record human reconsideration

Spans: **Decision trace** (`/decisions/[decisionId]`) ↔ **Compliance**
(`/compliance`).

1. On a solely automated decline, **Log contest** with the channel and an optional
   note. Outcome: the decision is labelled `contest — awaiting review`, and an
   actionable link appears in Compliance's human-review queue.
2. A reviewer follows that link, selects the basis and whether the original outcome
   is **upheld** or **overturned**, and records a required rationale. Outcome: an
   immutable reconsideration is attached without rewriting the original decision,
   and the contest resolves.
3. Compliance retains the human-review audit trail, including the reviewer, basis,
   outcome, and link back to the decision. The open-contest queue no longer includes
   the resolved item.

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

1. An editor/admin first publishes an immutable **case type**: typed/required context
   fields, dynamic state transitions and allowed roles, priorities, dispositions and
   reasons, service calendar, evidence gates, PII readers, and role scan order. They
   configure ordered queues and reviewer skill/capacity/jurisdiction/conflict profiles.
   Existing cases remain pinned to the version under which they opened.
2. A decision hits a **manual review** node. Outcome: the decision records a
   `MANUAL_REVIEW` reason code, and the case opens with the exact active type version,
   validated context, recorded business deadline, priority, and source-decision link.
   The scheduler routes it by attributes, skill, capacity, jurisdiction, priority,
   age, and conflicts. Competing replicas record one queue/assignee winner.
3. On the Case queue, use text/structured filters or a personal saved view, inspect
   possible duplicates, and sort by urgency. Select work for one bounded backend-owned
   bulk assignment/status/priority/disposition operation; its durable per-item manifest
   reports partial failures. Rebalance preserves the highest-priority/oldest work and
   moves only unrouted, inactive-owner, or capacity-overflow cases.
4. Open a case. Its context uses the active role layout and pinned field labels; PII
   fields outside the role's read policy are masked by the backend. Update only the
   fields that this exact version marks editable for the caller's role; the backend
   revalidates the merged typed context and serializes concurrent edits before replay.
   Link typed
   decisions/entities/agents/connectors/cases/alerts as evidence, or register immutable
   attachment metadata (hash and approved external storage pointer). The pointer is not
   returned by ordinary reads: **Reveal storage reference** first records actor, purpose,
   and time, refuses erased data, then exposes the pointer for the external artifact
   system. Retention, legal hold, and erasure state stay visible.
5. **Assign** or explicitly take over the task, follow only a configured state edge,
   and add notes/discussion. Record a configured disposition/reason only after required
   evidence is present. Assignment and terminal writes are permanent claims, so two
   reviewers cannot both own or terminally decide one case.
6. If the disposition requires second review, the case stays open. Deterministic QA
   sampling assigns a different reviewer, who records agreement, disagreement, or an
   explicit override and can return feedback. Only agreement/override becomes a
   validated outcome for downstream agent/model evaluation; one reviewer is never
   treated as ground truth.
7. For a suspended decision, the case directs the operator to record
   approve/decline/refer on the exact decision trace. That replayable outcome resumes
   the pinned execution, completes this case, and retires every shared task notification.
   SLA due-soon/breach events notify the queue, move breached work to its configured
   escalation queue after restart-safe reconciliation, and drive bounded webhook
   attempts with explicit retry/dead-letter history. Analytics and audit export report
   workload/capacity, first action, resolution, ageing, routing, SLA, and QA evidence.

### Configure an AI agent, review its runs, escalate to a case

Spans: **Agent Manager** (`/agents`) → **Agent** (`/agents/[name]`) → **Case queue**.

This is the low-level registry and direct-run path used for development, interactive
testing, and flow AI-node dependencies. Production specialist-agent and reviewer-assist
operations use the governed release journey below.

1. On Agent Manager, define an agent: a prompt/system, a model, optional tools, and an
   optional structured-output schema. Outcome: an immutable new version with run/cost
   stats across the workspace; an identical definition is idempotent.
2. Open the agent and **run** it with a prompt. Durable async is the normal
   operator path: choose the immutable version, timeout, maximum attempts, and
   optional business/correlation references. Outcome: admission returns
   immediately and the page follows the worker-owned attempt through running,
   retrying, completed, failed, cancelled, timed-out, or dead-letter state.
   Cancel requests and explicit retry (with acknowledgement after dead-letter)
   are available in place. Request/response and token streaming remain available
   for interactive use. Every terminal output is recorded in history and token/cost rollups.
3. Review the **run history** and the offline **eval** pass-rate (the agent scored
   against a set of eval cases). Outcome: a view of how the agent behaves and where it
   regresses across versions.
4. **Escalate** a run to a case for human review. Outcome: a case opens in the queue
   (`agent_review` type) referencing the originating run, and is worked exactly like a
   manual-review case. (Agents also run inside a flow via an **AI** node, where their
   output feeds downstream nodes.)

### Govern, evaluate, and operate a specialist agent

Spans: **Governed agents** (`/agents/governance`) → **Agent release**
(`/agents/governance/[templateId]`) → **Case workbench** (`/cases/[caseId]`) →
**Model risk** (`/mrm`).

1. Register a reusable specialist template with its task, owner, and purpose. Create an
   immutable release that pins instructions, provider/model, typed input/output,
   evidence and citation rules, trust policy, allowed tools and approval modes,
   token/cost/time budgets, and retry and human-review controls.
2. Publish an immutable evaluation suite. Its representative and adversarial cases can
   use deterministic or governed semantic graders, segment tags, severity, and repeated
   trials. Run a campaign against the release, inspect exact provider/model/invocation
   evidence and uncertainty, adjudicate individual trials where human judgment is
   required, compare an exact-suite baseline and challenger, and export reproducible
   JSON or CSV evidence. A required failing gate blocks release review.
3. Assign and request review. A different approver follows the notification, inspects
   the exact release and evaluation material, and approves or rejects it with a reason.
   Deploy that approved release to one environment immediately or on a schedule; an
   environment has one active binding for the template. Pause, roll back, retire, and
   explicit resume preserve the exact lineage and approval constraints.
4. In a governed case, request or receive a policy-triggered summary, evidence
   extraction, prioritisation, next-best action, or draft disposition. The durable
   worker pins the case evidence snapshot and exact release. Every supported claim
   cites evidence visible to the reviewer; stale citations and unsupported claims are
   called out. Human-before-call tools enter an approval queue and continue through the
   same claimed worker path after an authorized decision.
5. Accept, edit, reject, retry, or escalate the suggestion. The system records the
   observed evidence head, suggestion/final proofs, value-free differences, time saved,
   reviewer action, later independent QA, and validated outcome. Accepting an assist
   never resolves the case: the accountable reviewer separately performs the governed
   disposition or status command. Provider failure, malformed output, cancellation, or
   dead letter is visible and retryable but never owns queue entry, SLA, or case
   resolution.
6. Operators follow quality, adoption, edit/reject, QA, outcome, latency, token, and
   cost evidence back to the exact release, trial, assist, run, case, and segment.
   A threshold breach latches one critical safety incident across replicas, blocks new
   admission, and pauses the binding. Resolving the incident starts a fresh evidence
   window; a separately authorized explicit resume is still required. There is no
   silent provider/model fallback.
7. Subject erasure crypto-shreds generated suggestions and reviewer-edited content.
   Immutable hashes, citation identities, stale state, value-free differences,
   reviewer actions, and the final case disposition remain as accountability evidence;
   a missing operational decryption key is an error, not an invented erasure.

### Register a predictive model and monitor drift

Spans: **Models** (`/models`), referenced from a flow's **predict** node.

1. Define a model from a spec—or train a logistic model from a labelled dataset.
   Supported kinds: **logistic**, **GBM**, **expression**, or an **external**
   endpoint. Outcome: a versioned model hosted as data.
2. Request approval. A different actor with the **approver** role follows the shared
   notification to the exact Governance panel and records independent validation for
   the current version: the holdout dataset, named metrics, pass/fail, and substantive
   notes. Authentication supplies the validator identity; the model owner cannot
   validate their own work. Approval is disabled and the backend refuses it until the
   latest independent record passes, then the checker records an approve/reject
   reason. A changed definition needs fresh validation and approval; unapproved models
   are refused outside sandbox.
3. Expand the model's **Drift** readout on the Models page and **capture a baseline**.
   Outcome: the current score distribution is recorded as the reference for drift.
4. Set a drift monitor: alert when **PSI** (Population Stability Index) exceeds a
   threshold. Outcome: the model's drift status shows the current PSI versus the
   baseline and whether the monitor is firing; the drift state surfaces on the model
   list and in the model-risk inventory.
5. Record the realized business fact on the completed decision or its experiment:
   choose an outcome key, binary or continuous value, event time, source-system
   record, and label version. A later correction supersedes the prior value without
   erasing its history. The backend derives the flow/version/environment, experiment
   arm, and Predict-node/model lineage from immutable execution evidence; callers do
   not author those facts.
6. On Models, select that outcome key as **Performance evidence** (and the Predict node
   only when the flow invokes the same model more than once). Outcome: current,
   corrected binary outcomes produce calibration, accuracy, Brier score, and realized
   AUC for the exact model-version and experiment cohort. The model-specific actuals
   endpoint remains compatibility-only.
7. When model logic is redefined, recapture its drift baseline and collect new
   current-version outcomes. Outcome: prediction, baseline, and performance evidence
   restart as one homogeneous version cohort instead of blending unlike models.

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

### Run a governed experiment and act on valid evidence

Spans: **Experiments** (`/experiments`) → **Experiment detail** → **Decision trace** →
flow deployment.

1. Create a draft over exact published flow versions. State the hypothesis, owner,
   environment, stable subject-key expression, eligibility expression, salted
   allocations, primary KPI, minimum sample/effect, optional guardrails, and
   start/stop window. Outcome: an immutable cohort definition instead of an
   untracked per-request percentage.
2. Start it directly in sandbox. A production start becomes a maker-checker request;
   an independent approver must approve it before behavior changes. Outcome: the
   launch decision and explanation are durable governance evidence.
3. Repeatedly decide for the same subject. Assignment is stable across retries,
   replicas, and restarts, while exposure is counted only after execution reaches and
   completes the experimental treatment. The decision trace links back to its exact
   experiment, cohort, and arm.
4. Record decision-linked outcomes as the real-world facts arrive. Use a correction
   when a source revises a fact; never overwrite history or tell the platform which
   treatment/model produced it. Outcome: exposure and current corrected outcome join
   through backend-derived lineage.
5. Read live health and analysis: reached exposure, sample size, conversion or
   continuous estimates, confidence intervals, effect size, sample-ratio mismatch,
   and guardrail regressions. Drill into an exact decision when evidence looks wrong.
   Outcome: collecting, underpowered, invalid, inconclusive, and winner states remain
   distinct; directional movement alone is never labelled a winner.
6. Pause, complete, or cancel with a recorded reason. Promote an arm only after the
   evidence and normal deployment gates support it; production promotion still uses
   four-eyes. Outcome: a complete hypothesis → assignment → exposure → outcome →
   analysis → change-control trail survives replay.

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

### Run a durable population decision or backtest

Spans: **Population jobs** (`/population`) → **Population job detail**.

1. Create a decision or backtest job with an explicit flow, environment/version, and
   bounded input dataset. The platform records an immutable manifest (including exact
   experiment assignments where applicable) and per-item idempotency identities.
   Outcome: a resumable resource, not a request whose client must keep alive.
2. Start the job and watch progress, attempts, failures, and worker ownership. Pause
   or cancel deliberately; resume paused work or retry failed items without
   duplicating a logical row. Bounded concurrency and expiring worker leases allow
   another replica to recover work after a process dies.
3. Inspect partial failures while successful items remain available. When terminal,
   download the NDJSON result manifest with each row's exact decision/backtest
   result. Retention later expires result bodies without erasing the job's audit
   evidence. The synchronous batch and streaming APIs remain useful conveniences;
   this resource is the durable population contract.

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
4. **Publish** the version. Outcome: a new immutable policy version available for
   sandbox iteration. Publishing is not production activation: staging and production
   retain the last approved version (or refuse to decide when none has ever been
   approved).
5. **Request approval** for the latest version. Outcome: an actionable item in the
   shared approver inbox that opens this exact policy's Governance panel. Publishing
   another version invalidates the pending request without disturbing the serving
   version.
6. A different actor — neither the version author nor the requester — records an
   approval or rejection with a reason. Approval makes that exact version serve in
   staging and production; rejection leaves the prior approved version live. Every
   completed decision records the policy id and version that produced its disposition.

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

### Record lawful basis, expiry, and an information-sharing choice

Spans: **Entity** (`/data/[type]/[id]`) → **Compliance** (`/compliance`).

1. Open the entity that represents the data subject. Record the processing purpose,
   choose the lawful basis (for decisioning this is commonly contract, legal
   obligation, or legitimate interest rather than consent), and optionally attach
   evidence and a future expiry. Outcome: a versioned lawful-basis record tied to the
   subject and purpose.
2. Before expiry it is shown as **active**. At expiry the backend's authoritative
   clock makes it **expired** without needing a withdrawal event; the entity and
   Compliance views update their counts and no longer offer Withdraw for it.
   Explicit withdrawal remains available for an active record.
3. Record or rescind the subject's information-sharing opt-out on the same entity.
   This is a separate outbound-sharing election, not an inbound consent.
4. Open Compliance to see active, expired, withdrawn, and soon-to-expire lawful bases,
   the distribution by basis, and the tenant's sharing opt-out count. Outcome:
   operational obligations are visible without treating historical grants as current
    permission.

### Govern the model data-science lifecycle

Spans: **Modeling cockpit** (`/modeling`) → **Models** (`/models`) → **MRM** (`/mrm`).

1. A data owner defines an immutable **entity/event schema version** (fields,
   nullability, identifiers, relationships, classifications, lawful purposes,
   compatibility) with a quality contract — `block`, `refer`, or `warn` — plus
   completeness/validity/uniqueness rules. Request review; an independent checker
   approves it (four-eyes — the owner cannot). Outcome: an active governed contract;
   ingestion that violates a `block` rule is refused at the append boundary.
2. A `refer` violation opens an operator **quality incident** with severity, owner,
   affected subjects/assets, and correction lineage. An operator must
   **acknowledge ownership** before resolving it with evidence; the scheduler
   auto-resolves freshness incidents after a fresh record. Source watermarks, late
   arrivals, corrections, and retractions are visible in the cockpit.
3. A modeler defines a **dataset** (features, label horizon, deterministic
   partitions, population exclusion, historical consent, retention) and queues a
   **point-in-time snapshot**. A leased worker builds the rows, keeps missing labels
   censored, verifies the content hash after writing the encrypted blob, and
   publishes the immutable manifest. Admin-only **JSON/CSV export** is hash-verified.
4. The modeler queues a **deterministic training** job pinned to snapshot/code/
   runtime/parameters/seed. The worker writes and re-reads a content-addressed
   artifact, signs it Ed25519, and registers full production lineage. An
   **independent evaluation** records AUC/log-loss/Brier, calibration, threshold
   cost, Wilson intervals, segment/intersection fairness, temporal slices, and
   leakage findings.
5. A validator clears the artifact supply-chain gate (`validated` → `production`),
   records independent validation attesting leakage/calibration/fairness/threshold
   review against the signed hashes, and approves the model. Retirement is blocked
   while a dataset references the schema or a deployed flow runs the model.
6. **Lineage & challenger evidence** traces one production model from source schema
   → feature → dataset → snapshot → artifact → evaluation → serving, and champion/
   challenger comparison reads the same signed evidence.

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

1. Open the flow's shared draft and export its canonical source:
   `GET /v1/authoring/drafts/{draft_id}/export` returns byte-stable
   `intraktible.authoring/v1`. Layout-only coordinates are removed, embedded JSON is
   normalized, and exact reusable-component references use portable slugs. Export is
   audited and refuses workspace-classified sensitive fixtures. (The published-version
   export and Mermaid/BPMN/Graphviz downloads remain presentation/interchange tools,
   not the repository source contract.)
2. Import the canonical source with
   `POST /v1/authoring/import` (or `/import-bundle` for several at once), supplying
   an idempotency key. Bundle preflight resolves and compiles every flow before any is
   written. Outcome: a validated, recoverable draft plus an explicit target-identifier
   migration report in the target workspace—not an unreviewed publication.
3. Submit the imported revision as a changeset, attach required check evidence, and
   obtain independent review before publication and environment promotion. The UI,
   SDK, and `intraktible authoring` CLI use this same path. Evidence and discussion
   accept references rather than raw customer PII.

### Shadow-evaluate a candidate version on live traffic

Spans: **Flow builder** (`/engine/[flowId]`), Deploy & versions tab.

1. Set a **shadow** version for an environment (`PUT /v1/flows/{flow_id}/shadow`).
   Outcome: champion decisions in that environment also run through the shadow
   version from the same caller input and authoritative entity-feature snapshot. (A
   served experiment arm already has its own observed outcome evidence.) The
   candidate resolves its own connectors, pinned agents, and models, so these calls can
   send data or incur cost; consent, sharing, egress, model-approval, and immutable
   agent-version gates still apply. The shadow's result is recorded and never served
   to the caller. (A decision suspended for human review has no verdict yet, so nothing
   is compared.)
   The current champion cannot be selected as its own shadow; if a later deployment
   makes it champion, the cohort records a configuration error until another
   candidate is selected.
2. Read the divergence cohorts: exact live/candidate versions, policy version,
   experiment/cohort/arm dimensions, and how often the candidate matched, diverged,
   or errored. The current exact cohort is prominent and prior unlike cohorts remain
   inspectable rather than being overwritten or blended. Agreement means the
   exact governed policy outcome when a policy is bound, otherwise the same status and
   complete output. Follow a value-free sample to the live decision or inspect changed
   top-level fields / the candidate error. Outcome: explainable evidence about how one
   precise candidate would behave on real traffic before it serves any result.

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

Spans: **Compliance** (`/compliance`) → **Entity**
(`/data/[type]/[id]`). _Admin only._

1. On Compliance, set the scheduled retention window or run an acknowledged
   one-off retention sweep. Statutory record-retention rules always win; eligible
   subjects under legal hold are also skipped.
2. Search the governance register for a subject, or open its entity detail. Place a
   legal hold with a reason when an investigation/dispute requires preservation;
   release it explicitly when that obligation ends.
3. Request erasure from the entity or governance register. The UI explains and
   disables the action while statutory retention or a hold blocks it, and requires
   confirmation because the operation is irreversible.
4. Outcome: the subject's encryption key is destroyed, so sealed values in the
   append-only event log and every rebuilt projection render `[erased]`; the
   audit/event shape remains intact. The erased-subject register lists completed
   erasures—it is not an inventory of every subject in the workspace.

### Govern the workspace

Spans: **Model risk** (`/mrm`), **Observability** (`/observability`),
**Compliance** (`/compliance`), **Audit log** (`/audit`), **API keys** (`/keys`).
The MRM, compliance governance controls, audit, and API-keys pages are **admin
only** — they are hidden from a non-admin's navigation and home, and the pages gate
on the server as well.

- **Model risk (MRM).** The SR 11-7 / SS1/23 model inventory in one register: every
  flow, predictive model, and agent, each with its validation coverage (assertions for
  flows, current independent evidence for models, and governed release/campaign gates
  for specialist agents), live monitoring (success rate, safety incidents, assist
  adoption and QA/outcomes, cost, firing monitors, drift PSI), and any open governance
  gaps. Scan for entries with open gaps or failing validation, open an entry to read
  its evidence, and export the report (CSV or Markdown). _Admin only._
- **Observability.** The operational view across flows: set a success and latency
  objective (SLO) per flow and read attainment and remaining error budget, see AI
  usage and cost by model, and read how distributed tracing is emitted.
- **Compliance.** Operate consent, sharing opt-outs, legal holds, statutory
  retention, and erasure/retention sweeps from one register. _Governance mutations
  are admin only._
- **Audit log.** The immutable, event-sourced trail of who did what, when — flow
  publishes and deployments, decisions, case activity, key changes, and more. Filter
  by stream, type, actor, and time (the filter lives in the URL and is shareable),
  and export matching rows to CSV. _Admin only._
- **API keys.** Issue and manage the keys that authenticate decision-API calls. Create
  a key scoped to an environment and role, rotate it with a grace window, or revoke
  it. _Admin only._

### Administer tenants (organizations, workspaces, memberships)

Spans: **Tenancy** (`/tenancy`), CLI (`intraktible tenancy`). Admin only.

1. A platform principal (the bootstrap/platform API key) creates an **organization**:
   key, display name, plan, workspace quota, and an initial admin actor. The response
   returns the organization's **first admin key**, shown exactly once. Outcome: a
   governed tenant with a default `main` workspace and an admin member.
2. An org admin (or the platform principal) creates **workspaces** within the org and
   lifecycle-manages them (suspend/resume/delete). Workspace creation enforces the
   org's workspace quota. Outcome: sub-tenants the platform isolates by key prefix.
3. Grant **memberships** to actors with a role (viewer/operator/editor/approver/
   admin). Revoke or suspend them; the **last active admin** of a workspace cannot be
   revoked. Outcome: the provisioning record of who belongs to which workspace.
4. The platform principal can suspend/resume/delete an organization; deletion is
   blocked while any workspace is still active. Outcome: governed tenant lifecycle
   with audit.

### Operate a resilient production deployment

Spans: **Operations** (`/healthz`, `/readyz`, `/capacity`, `/metrics`), CLI
(`intraktible backup` / `restore` / `replay`).

1. Verify health: `/healthz` reflects projection and scheduler health (503 when
   degraded); `/readyz` stays 503 until a replica's projections catch up to the log
   head. Outcome: a load balancer routes only to current, healthy replicas.
2. Read service-level evidence on `/capacity`: projection applied/head/lag, the
   configured backpressure bound, process role, and scheduler health. Outcome:
   published SLO/SLA posture without inferring it.
3. Back up the system of record: `intraktible backup --data-dir=/data --out=<file>`
   streams the event log as NDJSON. Outcome: a portable recovery artifact (RPO is
   zero — every append is durable).
4. Restore and verify: `intraktible restore --data-dir=<fresh> --in=<file>` rebuilds
   the log byte-identically, then `intraktible replay --data-dir=<fresh>` confirms
   the projections replay cleanly. Outcome: tested recovery (RTO is replay time),
   not a runbook hope.
5. Restrict and shed load deliberately: set `INTRAKTIBLE_IP_ALLOWLIST` to gate /v1
   to known CIDRs, and `INTRAKTIBLE_MAX_PROJECTION_LAG` to shed reads past a lag
   bound (writes always admitted). Outcome: explicit network policy and overload
   backpressure.

---

## By persona

Each persona below lives in a subset of the journeys above. The persona sets the
default landing page, the navigation order, the home stats/actions, and the initial
lens on shared lists.

| Persona   | Label                     | Lands on       | Lives in                                               |
| --------- | ------------------------- | -------------- | ------------------------------------------------------ |
| builder   | Workflow Designer         | Builder home   | Flows, policies, context, models, governed agents      |
| developer | Developer / Integrator    | Persona home   | Traces, API keys, direct and remote agent protocols    |
| operator  | Risk Operator             | Operator home  | Cases, cited assists, incidents, deployments           |
| manager   | Team Manager              | Persona home   | Approvals, agent quality/cost, case load, audit        |
| product   | Product / Experimentation | Persona home   | Governed experiments; population jobs; outcomes        |
| showcase  | Executive                 | Showcase home  | KPIs, trends, governance posture                       |
| evaluator | Evaluator / Guest         | Evaluator home | Guided flows, agent suites, decisions, and cases       |

- **Workflow Designer (builder).** Spends the day on the canvas: authoring and
  versioning flows, wiring policy bands and context data, and referencing models from
  Predict nodes, plus shaping governed specialist-agent releases. Default persona.
  Lives in _Author and publish a flow_, _Author a policy_, _Set up context data_, and
  _Govern, evaluate, and operate a specialist agent_.
- **Developer / Integrator.** Integrates the decision API and debugs. The decisions
  list is relabelled **Traces** and lands on failing traces, leading with
  status/duration/environment. Manages API keys and agents. Lives in _Run a decision
  and read its trace_ and the agent journeys.
- **Risk Operator.** Works the queues. Lands on the open case queue, most-urgent
  first, and clears it; reviews evidence-cited assists, agent incidents, pre-approvals,
  and recent decisions. Lives in _Manual review_, _Govern, evaluate, and operate a
  specialist agent_, and _Grant a pre-approval_.
- **Team Manager.** Watches throughput and governance. Home stats lead with pending
  approvals, cases needing review, and overdue cases; reviews the audit trail. Lives
  in _Promote with four-eyes_ and _Author a policy_ (as the approver), and the
  queue/oversight side of _Manual review_.
- **Product / Experimentation.** Tunes impact. Lands on experiment health, defines
  stable cohorts and decision-linked KPIs, runs durable population backtests, and
  promotes only supported changes. Lives in _Run a governed experiment_, _Run a
  durable population decision or backtest_, and _Register a model_.
- **Executive (showcase).** Reads posture, not detail: decision volume, trends, case
  health, and governance (MRM/audit, when admin). Lives in the read side of
  _Govern the workspace_.
- **Evaluator / Guest.** A guided, minimal surface (builder, decisions, cases) for
  exploring the platform without a role's clutter. Walks an abbreviated version of the
  build → decide → review loop.

---

## Roles and gating

Actions are gated by role, ranked **viewer < operator < editor < approver < admin**.
A higher role includes the rights below it.

| Role     | Can                                                                                |
| -------- | ---------------------------------------------------------------------------------- |
| viewer   | Read-only across surfaces                                                          |
| operator | Run decisions; work cases; issue notices; record contests/reviews and lawful basis |
| editor   | Author and publish flows, policies, models, agents, context data                   |
| approver | Everything an editor can, plus approve/reject production flow and agent releases    |
| admin    | Everything, plus model risk, audit log, and API-key management                     |

Two gates matter most. **Four-eyes promotion**: approving a production deployment
requires the approver role _and_ a different actor than the requester. **Admin-only
surfaces**: model risk, audit, and API keys are hidden from a non-admin's navigation
and home (so no dead-end 403s) and are enforced server-side as defence in depth.

Role gates what you _can do_; persona gates what you _see first_. They are independent:
any role can run under any persona.

## The in-app page guide

Every page has a built-in guide. Click the **?** button in the header to open the
guide for the current page — a one-line summary, the things you do on that page, and
its key flows, named to match the on-screen controls. This documentation is the
end-to-end view; the in-app guide is the per-page view.
