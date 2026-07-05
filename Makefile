# SPDX-License-Identifier: AGPL-3.0-or-later
GO ?= go
BIN := bin/intraktible

# AGPL-compatible licenses allowed for Go dependencies (see docs/LICENSING.md).
ALLOWED_LICENSES := MIT,BSD-2-Clause,BSD-3-Clause,ISC,0BSD,Unlicense,Apache-2.0,MPL-2.0,\
PostgreSQL,LGPL-2.1,LGPL-3.0,GPL-2.0,GPL-3.0,AGPL-3.0

# Our Go packages/dirs, excluding any vendored .go files under web/node_modules
# (npm deps such as `flatted` ship a Go port that is not ours). Go tooling
# descends into node_modules, so we filter it out of every analysis target.
GO_PKGS := $(shell $(GO) list ./... | grep -v /node_modules)
GO_DIRS := $(shell $(GO) list -f '{{.Dir}}' ./... | grep -v /node_modules)

.PHONY: all build run dev test test-short fmt fmtcheck vet typecheck tsenums lint sast deadcode dupl vuln licenses check ci precommit web dist e2e-embedded demo-seed clean

all: build

## build: compile the single binary (UI is embedded from platform/web/assets)
build:
	$(GO) build -o $(BIN) ./cmd/intraktible

## run: build then serve the modular monolith
run: build
	$(BIN) serve

## dev: full-stack hot-reload — Go API (:8080) + Vite UI (:5173, proxies /v1) at once.
# One command for UI work: the SvelteKit dev server hot-reloads and proxies API
# calls to the Go backend. Ctrl-C stops both (trap kills the process group).
dev:
	@command -v npm >/dev/null || { echo "make dev needs npm (Node 20+) for the UI dev server"; exit 1; }
	@[ -d web/node_modules ] || (cd web && npm install)
	@echo "▶ API http://localhost:8080  ·  UI http://localhost:5173  (dev key: dev-sandbox-key) — Ctrl-C to stop"
	@trap 'kill 0' INT TERM EXIT; \
		INTRAKTIBLE_AI_STUB=1 $(GO) run ./cmd/intraktible serve --addr=:8080 & \
		(cd web && npm run dev -- --port 5173 --strictPort) & \
		wait

## test: run all Go tests with the race detector
test:
	$(GO) test -race ./...

## test-short: run all Go tests without the race detector (fast pre-commit gate)
test-short:
	$(GO) test ./...

## fmt: format the tree
fmt:
	gofmt -w $(GO_DIRS)

## fmtcheck: fail if anything is unformatted (CI)
fmtcheck:
	@out=$$(gofmt -l $(GO_DIRS)); if [ -n "$$out" ]; then echo "unformatted:"; echo "$$out"; exit 1; fi

## vet: go vet
vet:
	$(GO) vet ./...

## typecheck: compile-only check (the Go compiler is the type checker)
typecheck:
	$(GO) build ./...

## tsenums: regenerate web/src/lib/enums.generated.ts from the Go enum constants
## (single source of truth). The drift-check test in cmd/tsenums fails the gate if
## the committed file is stale.
tsenums:
	$(GO) run ./cmd/tsenums

## lint: golangci-lint (strict)
lint:
	$(GO) run github.com/golangci/golangci-lint/cmd/golangci-lint@latest run $(GO_DIRS)

## sast: static application security testing (gosec)
sast:
	$(GO) run github.com/securego/gosec/v2/cmd/gosec@latest -quiet -exclude-dir=node_modules ./...

## deadcode: report unreachable code
deadcode:
	$(GO) run golang.org/x/tools/cmd/deadcode@latest -test $(GO_PKGS)

## dupl: copy-paste detection (Go) — a real gate: any production clone fails
# Threshold 90 (tokens): at 48 it flagged idiomatic minimal code — HTTP handler
# preambles, sibling event-appliers, typed read-model delegators — as clones,
# pushing toward harmful over-abstraction. 90 still catches substantial copy-paste
# (golangci-lint's own dupl default is 150). Test files are excluded: e2e setup
# preambles are idiomatic repetition, and gating them would push tests toward
# indirection that hides what each case exercises.
dupl:
	@out=$$(find . -name '*.go' -not -name '*_test.go' -not -path './node_modules/*' -not -path './bin/*' \
		| $(GO) run github.com/mibk/dupl@latest -threshold 90 -files); \
	echo "$$out"; \
	echo "$$out" | grep -q '^Found total 0 clone groups' \
		|| { echo "dupl: production clones found — deduplicate (or consciously raise the threshold)"; exit 1; }

## vuln: known-vulnerability scan
vuln:
	$(GO) run golang.org/x/vuln/cmd/govulncheck@latest $(GO_PKGS)

## licenses: fail the build on any non-AGPL-compatible dependency
# --ignore modernc.org/mathutil: its LICENSE is plainly BSD-3-Clause (manually
# vetted; see docs/LICENSING.md) but go-licenses' classifier cannot match its
# Go-project wording. Every other modernc/SQLite dep classifies cleanly.
licenses:
	$(GO) run github.com/google/go-licenses@latest check $(GO_PKGS) \
		--allowed_licenses="$(ALLOWED_LICENSES)" --ignore modernc.org/mathutil

## web: build the SvelteKit UI and embed it (requires npm)
web:
	cd web && npm ci && npm run build
	rm -rf platform/web/assets && cp -r web/build platform/web/assets

## dist: the single self-contained artifact — build + embed the real UI, then the binary
dist: web build

## e2e-embedded: smoke the SHIPPING artifact — build+embed the real UI, build the
# binary, and run the embedded Playwright suite (HTTP + browser) against
# `intraktible serve` on :8080. Restores the committed placeholder assets after,
# so the working tree stays clean even if the smoke fails. Catches embedded-only
# breakage (e.g. a //go:embed that drops _app and ships a blank page).
e2e-embedded:
	@command -v npm >/dev/null || { echo "make e2e-embedded needs npm (Node 20+)"; exit 1; }
	@[ -d web/node_modules ] || (cd web && npm ci)
	@set -e; trap 'git checkout -q -- platform/web/assets 2>/dev/null || true' EXIT; \
		(cd web && npm run build); \
		rm -rf platform/web/assets && cp -r web/build platform/web/assets; \
		$(GO) build -o $(BIN) ./cmd/intraktible; \
		(cd web && npm run test:e2e:embedded)

## demo-seed: regenerate the demo workspace event log (web/static/demo-seed.json)
# by driving the REAL assembled backend in-process (see cmd/intraktible-seed).
# Explicit, not part of any build: event ids are random, so regeneration rewrites
# the (committed) asset even when the story is unchanged.
demo-seed:
	$(GO) run ./cmd/intraktible-seed -out web/static/demo-seed.json

## wasm: the browser deployment target — the SAME backend, compiled to wasm.
# Outputs the binary + Go's JS shim where the web build picks them up as assets.
wasm:
	GOOS=js GOARCH=wasm $(GO) build -ldflags="-s -w" -trimpath -o web/static/intraktible.wasm ./cmd/intraktible-wasm
	# rm first: the GOROOT source is mode 444, so a straight cp cannot overwrite
	# the previous copy on a re-run.
	rm -f web/static/wasm_exec.js
	cp "$$($(GO) env GOROOT)/lib/wasm/wasm_exec.js" web/static/wasm_exec.js

## check: fast local gate
check: fmtcheck vet typecheck test

## ci: full gate (matches .github/workflows/ci.yml and the pre-commit pipeline)
ci: fmtcheck vet typecheck lint sast test deadcode dupl vuln licenses

## precommit: run the pre-commit pipeline against the whole tree
precommit:
	pre-commit run --all-files

clean:
	rm -rf bin data
