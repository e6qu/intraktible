<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->

# Code conventions

How we express things in this codebase, so a reader (human or AI agent) reaches
for the right abstraction instead of inferring one from sparse or partial usage.
These are forward-looking rules; the changelog of how they came to be is in
BUGS.md (TS1–TS9). When in doubt, match the nearest sibling that already follows
the rule, and prefer **failing loudly** over a silent fallback.

## Functional core, imperative shell
The domain core is pure (no clock, no I/O, no randomness) and deterministic, so a
recorded decision/run replays identically. Effects (connector/AI/model calls, the
clock, IDs) are resolved by the shell, injected into the input, and **recorded in
the event** — replay reads the recorded effect, never re-runs it. Don't perform
I/O or read the clock from a `domain` package or a projector's `Apply`.

## Domain enums are named types
An enum-like value (a status, kind, type, op, disposition, environment) is a
**named string type with a `Valid()` method** — never a bare `string` with a
detached `ValidX(string) bool`. This makes an invalid value catchable at the
boundary instead of a string that flows deep before failing.

Examples: `models.ModelKind`, `domain.CaseStatus`, `domain.SLAStatus`,
`agentdomain.RunStatus`, `connectordomain.ConnectorType`, `engine.Environment`,
`policy.Disposition`, `monitor.Metric` / `monitor.Op`, `auth.Role` / `auth.Scope`.

Boundary rule: the named type governs the **domain/validation layer**. **Event
payloads and read-model map keys stay `string`** (the wire/persistence boundary)
and convert at the edge — `Status: string(cmd.Status)` on emit, `T(p.Field)` in
the projector. A named string type marshals identically to a string, so the JSON
wire/stored format is unchanged. Keep a thin `ValidX(string) bool` helper only
where a raw request/path string is validated before it is typed.

**One vocabulary, one type.** Don't define the same value set twice. A pre-approval
disposition is `policy.Disposition` (the approve/decline subset), not a second
set of `"approve"/"decline"` consts.

## `platform/mo` (`Option[T]` / `Result[T]`) — a scalpel, not the default
Use `mo.Option`/`mo.Result` only where an *absent* or *failed* state is genuinely
easy to mishandle — the canonical case is a `(nil, nil)` sentinel where "absent"
could be confused with a constructed-but-nil value (`kms.FromEnv`). Idiomatic Go
`(T, ok)` and `(T, error)` are correct and **stay as-is** everywhere they read
clearly: `store.GetDoc` (`(T, bool, error)`), `identity.From`, `auth.Resolve`,
empty-collection `return nil, nil`. Do **not** convert these to `mo`; the sparse
usage of `mo` is deliberate, not an unfinished migration.

## Smart constructors at external-input boundaries
`identity.New(org, ws, actor)` is the validated constructor, used where an identity
is minted from **external input** — SSO (OIDC/SAML), an API-key request body.
Construction from already-trusted data (an event envelope, a fixed scheduler actor,
a test fixture) uses the struct literal + the existing `Valid()` check. Struct
fields are intentionally **not** unexported: that would churn hundreds of trusted
internal/test literals for no gain over the `Valid()` checks already in place.

## Publish-time flow validation
A flow is dry-compiled at publish (`domain.ValidateFlow`): every node's config is
decoded and every expression / Code-node script is compiled (no execution, no
reference resolution). A semantically-broken flow is rejected at publish, never
deferred to the first production decision. Add new node validation here, not only
in the executor.

## Projection store contract
The projection runtime requires its store to be either a `store.TxStore` (durable;
the checkpoint advances atomically with each event) or `store.Ephemeral` (rebuilt
from the log on restart). A new **durable** backend must implement `TxStore` —
`projection.New` panics on a durable, non-transactional store rather than letting
non-idempotent projector counters double-count on crash recovery.

## Deliberately NOT done (don't "finish" these)
- **Codebase-wide field unexporting / `NewX` for every command** — the
  validate-then-trust pattern (handlers call `Validate()` consistently) is the
  convention; an unused exported constructor only trips the deadcode gate.
- **Wholesale `Option`/`Result` conversion** — see the scalpel rule above.
- **A typed `PreResolved` effect seam** — the shell injects resolved effects under
  the reserved `connect`/`ai`/`predict` keys of the recorded input. Changing that
  shape would break replay of already-recorded decisions without a migration.
