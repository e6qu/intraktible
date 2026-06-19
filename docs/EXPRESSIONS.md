<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->

# Expression language — stable contract

This is the versioned reference for the two expression surfaces a flow author writes
against: **conditions/expressions** (expr-lang) and the **Code node** (Starlark). It
is a stable contract — flows that decide today keep deciding the same way. Changes
that alter evaluation are versioned and called out in the changelog at the bottom.

> **Scope note (decision D9).** There is exactly one condition language (expr-lang)
> and one scripting language (Starlark). We deliberately do **not** add a second or
> "standard" expression engine (e.g. CEL, FEEL, or a DMN expression dialect): two
> engines double the security/determinism surface and split author knowledge for no
> capability the current two don't already cover. Requests for "DMN/FEEL support"
> are answered by the existing two languages.

## 1. Conditions & output expressions — expr-lang

Every `when` / `condition` / `expr` field across the node engines is an
[expr-lang](https://expr-lang.org) expression:

| Node | Field(s) | Kind |
| --- | --- | --- |
| Split | `condition` | boolean |
| Rule | `rules[].when`, `rules[].then[].expr` | boolean / value |
| Decision Table | `rows[].when`, `rows[].outputs[].expr` | boolean / value |
| Scorecard | `factors[].when` | boolean |
| 2D Matrix | `rows[].when`, `cols[].when` | boolean |
| Reason | `reasons[].when` | boolean |
| Assignment | `assignments[].expr` | value |

**Environment.** An expression reads the decision **input** fields by name, plus
anything earlier nodes have written into the context (assignments, rule/table
outputs), plus connector/agent results under `connect.<output>` / `ai.<output>` and
computed features under `features.*`. A `when` must evaluate to a boolean; a non-bool
`when` is a recorded **failed** decision, not a silent skip.

**Examples.**

```
score >= 700 && features.txns_24h < 5
income > 50000 ? "prime" : "near"
amount > 1000 and country in ["US", "CA"]
```

**Determinism.** Expressions are pure: no clock, no randomness, no I/O. The same
input always yields the same result, which is what makes a decision replayable from
its event stream.

## 2. The Code node — Starlark

The Code node runs a [Starlark](https://github.com/google/starlark-go) script for
logic that is awkward as a single expression. Starlark is a deterministic Python
dialect:

- The decision context is exposed as a `data` dict (`data["fico"]`).
- The script's **top-level variable assignments** are merged back into the context as
  the node's outputs; functions and locals are not captured.
- The sandbox has **no clock, randomness, or I/O**, recursion is disabled, and
  execution is bounded by a step limit — so it stays pure and replayable.

```python
score = data["fico"] + 10
if data["amount"] > 1000:
    decision = "APPROVE"
else:
    decision = "DECLINE"
```

Here `score` and `decision` become node outputs; a helper `def` would run but only
top-level names are merged.

## 3. Stability & versioning

These two languages are the public authoring contract. The grammar, the available
environment, and the determinism guarantees above are stable: an upgrade will not
silently change how an existing flow evaluates. Any change that could alter a
decision is versioned and recorded here.

| Version | Change |
| --- | --- |
| v1 | expr-lang conditions/expressions + Starlark Code node (no second engine — D9). |
