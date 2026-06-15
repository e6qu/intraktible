# Licensing & Dependency-Compliance Policy

## Project license
**intraktible is licensed under `AGPL-3.0-or-later`** (GNU Affero General Public License v3.0 or any
later version). Full text: [`../LICENSE`](../LICENSE).

- **SPDX identifier:** `AGPL-3.0-or-later`.
- Every source file carries an SPDX header:
  - Go: `// SPDX-License-Identifier: AGPL-3.0-or-later`
  - Svelte/TS: `<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->` / `// SPDX-License-Identifier: AGPL-3.0-or-later`
- Because intraktible is a **network service**, the AGPL §13 "remote interaction" clause applies: a
  hosted instance must offer its (modified) source to users. We will expose a source/offer link in
  the UI and an `/about`/`/source` endpoint.

## Hard rule: dependencies must be AGPL-compatible
A dependency may only be used if its license permits inclusion in an AGPL-3.0-or-later work
(compatibility is one-way — the dependency's terms must allow being combined into our copyleft work).

### ✅ Allowed (compatible) licenses
Public-domain-equivalent and permissive: **MIT, BSD-2-Clause, BSD-3-Clause, ISC, 0BSD, Unlicense,
Zlib, Apache-2.0, PostgreSQL, MPL-2.0**. Copyleft compatible: **LGPL-2.1-or-later, LGPL-3.0,
GPL-2.0-or-later, GPL-3.0-or-later, AGPL-3.0-(or-later)**.

### ❌ Disallowed (incompatible or non-free)
- **GPL-2.0-only** / **LGPL-2.1-only** (no "or later") — incompatible with v3.
- **SSPL** (e.g. MongoDB server, some Redis 7.4+ modules), **BUSL/BSL 1.1** (e.g. HashiCorp
  Terraform/Vault/Consul/Nomad post-2023, Redpanda, Sentry server), **Elastic License (ELv2)**,
  **Commons Clause**-encumbered, **CC-BY-NC**, any **proprietary / source-available / non-OSI** license.
- Apache-1.1, original 4-clause BSD (advertising clause), and anything with added field-of-use or
  patent-retaliation terms beyond Apache-2.0.

> Note: **Apache-2.0 is one-way compatible** — Apache code may be used in our AGPL project, but not
> the reverse. That's exactly our direction, so Apache-2.0 deps are fine.

### Dev-only tools
Tools that are **not linked into or distributed with** the binary (linters, generators, CI) don't
constrain our distributable license. (e.g. `golangci-lint` is GPL-3.0-or-later — fine to *use*, and
GPL-3.0 is AGPL-compatible regardless.)

## Vetted dependencies (initial)
| Dependency | Role | License | OK |
|---|---|---|---|
| dgraph-io/badger | embedded event log/KV | Apache-2.0 | ✅ |
| google/cel-go | rule/condition eval | Apache-2.0 | ✅ |
| expr-lang/expr | expression eval | MIT | ✅ |
| google/starlark-go | Code node (sandbox) | BSD-3-Clause | ✅ |
| jackc/pgx | Postgres driver | MIT | ✅ |
| modernc.org/sqlite | pure-Go SQLite | BSD-3-Clause | ✅ |
| go-chi/chi (or std net/http) | routing | MIT (BSD for stdlib) | ✅ |
| golang.org/x/tools (deadcode), x/vuln (govulncheck) | tooling | BSD-3-Clause | ✅ |
| mibk/dupl | copy-paste detection (Go) | MIT | ✅ |
| jscpd | copy-paste detection (web) | MIT | ✅ |
| go.opentelemetry.io/otel | telemetry | Apache-2.0 | ✅ |
| google/go-licenses | license CI check | Apache-2.0 | ✅ |
| Svelte / SvelteKit | frontend | MIT | ✅ |
| @xyflow/svelte (Svelte Flow) | flow builder | MIT | ✅ |
| Vite / Vitest | build/test | MIT | ✅ |
| Playwright | e2e test | Apache-2.0 | ✅ |
| golangci-lint | linter (dev-only) | GPL-3.0-or-later | ✅ (dev tool) |

_AI provider SDKs (Anthropic/OpenAI/Google/Ollama) and an optional ONNX runtime are added behind the
pluggable interfaces; each must be license-checked at add time (all currently MIT/Apache-2.0)._

## Enforcement (CI)
- **Go:** `go-licenses check ./...` against the allowlist; fail the build on any disallowed/unknown
  license. Generate `THIRD_PARTY_NOTICES.md` via `go-licenses report`.
  - **Manually vetted exception:** `modernc.org/mathutil` (a transitive dep of the pure-Go SQLite
    driver) is `--ignore`d in the `licenses` make target. Its `LICENSE` is plainly **BSD-3-Clause**
    (three clauses + the standard disclaimer) but go-licenses' classifier cannot match its
    Go-project wording, so it reports "Unknown". Every other modernc / SQLite dependency
    (`sqlite`, `libc`, `memory`, `bigfft`, `uuid`, `go-humanize`, `go-isatty`, `go-strftime`)
    classifies cleanly as BSD-3-Clause / MIT.
- **Web:** `license-checker`/`jscpd`'s allowlist (or `npx license-checker --onlyAllow "MIT;BSD-2-Clause;BSD-3-Clause;ISC;Apache-2.0;0BSD;MPL-2.0"`).
- A short **`ADDING-A-DEPENDENCY` checklist** in CONTRIBUTING: confirm SPDX license ∈ allowlist,
  record it, regenerate notices.
- Adding a dependency on the denylist is a **build failure**, not a warning.
