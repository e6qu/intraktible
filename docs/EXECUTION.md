# How a decision runs

This explains the engine underneath intraktible: what a decision *is*, how the engine
executes one, where the verdict and its reasons come from, and how the in-browser demo
runs the very same model without a server.

## A decision is a walk through a graph

A **flow** is a directed graph of typed **nodes**. A decision is one walk through that
graph for a single input. The engine threads a mutable **record** (a flat map of
fields) from the `input` node along the edges; each node reads from the record and
writes back to it, until an `output` node ends the walk.

```
input → assignment → predict → split ─┬─ (low risk)  → output(approve)
                                       ├─ (mid risk)  → manual_review → output(refer)
                                       └─ (high risk) → output(decline)
```

The walk is deterministic: given the same input and the same published version, the
same path is taken and the same result is produced. Nothing branches on wall-clock time
or randomness.

## Node types

Each node does one job. The core set:

| Node | What it does |
|------|--------------|
| `input` | Entry point; seeds the record with the request's fields. |
| `assignment` | Computes new fields from expressions (e.g. `dti = debt / income`). |
| `predict` | Scores the record with a registered **model**, writing the score back. |
| `connect` | Fetches external data from a **connector** and merges it into the record. |
| `ai` | Calls a configured **AI agent**; its (optionally structured) reply joins the record. |
| `scorecard` / `decision_table` / `2d_matrix` | Tabular scoring and lookups. |
| `split` | Branches: evaluates each outgoing edge's condition and follows the first match. |
| `manual_review` | Routes the decision to a human — opens a **case** and emits a `MANUAL_REVIEW` reason code. |
| `reason` | Emits **reason codes** (a `code` + description) when its condition holds. |
| `output` | Ends the walk; the record at this point is the decision's output. |

A `split` with no matching branch and no default **fails the decision loudly** rather
than silently approving — a deliberate safety property, not a bug.

## Expressions

Conditions and assignments are written in a small, sandboxed expression language —
arithmetic, comparisons, boolean logic, ternaries, and dotted field access
(`predict.pd.probability`). It is evaluated by a hand-written parser, never `eval()`.
See [Expression language](EXPRESSIONS.md) for the grammar.

## Models

A `predict` node references a **model** from the registry. Models come in a few kinds —
`logistic` and `gbm` (gradient-boosted) classifiers, an `expression` model for simple
derived scores, and `external` models served over HTTP (egress-guarded). The model's
output (e.g. a probability) is written into the record for later nodes to branch on.

## From outcome to disposition

The raw walk produces an output record. A bound **policy** then maps that to a
**disposition** — `approve`, `decline`, or `refer` — by matching ordered **bands**
(first match wins; an unmatched record defers to `refer`). Alongside the disposition,
the decision carries **reason codes**: the *why* behind the outcome, emitted by `reason`
nodes, the `manual_review` node, and the policy. The reason codes are what make a
decision explainable and auditable after the fact.

## Every decision is an event

The engine is **event-sourced**. Running a decision appends an immutable event
recording the input, the full node-by-node **trace** (which nodes ran, the branch taken
at each split, each node's output), the disposition, and the reason codes. That event is
the source of truth: the decision detail page simply replays it. Because the record is
append-only, a decision can always be re-inspected and explained exactly as it ran —
this is the auditability the platform is built around.

Two execution modes share this engine:

- **Recorded** — the normal path; the decision is persisted to history, metrics, and the
  audit log. The flow builder's test run records to the **sandbox** environment so you
  can inspect its trace.
- **Preview** — a dry run (`"preview": true` on the decide call, or the builder's
  *Preview* toggle) that returns the full result but records nothing.

## Human-in-the-loop

When a flow reaches a `manual_review` node, the decision is escalated: a **case** opens
(with an SLA), linked back to the source decision. A reviewer triages and resolves it,
and every action lands on the case's immutable activity trail. AI agent runs can be
escalated to a case the same way.

## How the demo runs all of this

The public demo at `/demo/` has **no backend** — yet every decision is really executed.
The demo ships a faithful **client-side interpreter** of the same model:

- `web/src/lib/demo/engine.ts` walks a flow graph exactly as described above —
  evaluating expressions, scoring models, taking the matching split branch, opening
  cases on manual review, and producing a real disposition + reason-code trace.
- `web/src/lib/demo/install.ts` overrides `window.fetch` (and `EventSource`/`WebSocket`)
  so the app's normal `/v1` API calls are served from an in-memory store instead of a
  server. The UI does not know it's talking to a mock.
- State persists to `localStorage`, so a flow you build, deploy, and decide accumulates
  across reloads (with a **Reset** control), and the switched demo user drives the audit
  trail.

So when you run a test decision in the demo and read its trace, you are watching the
real execution model run in your browser — the same nodes, the same branching, the same
disposition logic — not a pre-recorded animation. The production engine is the Go
implementation under `decision-engine/`; the demo interpreter mirrors its behavior so
the demo faithfully represents the product.
