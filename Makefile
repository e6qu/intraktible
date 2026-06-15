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

.PHONY: all build run test test-short fmt fmtcheck vet typecheck lint sast deadcode dupl vuln licenses check ci precommit web clean

all: build

## build: compile the single binary (UI is embedded from platform/web/assets)
build:
	$(GO) build -o $(BIN) ./cmd/intraktible

## run: build then serve the modular monolith
run: build
	$(BIN) serve

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

## lint: golangci-lint (strict)
lint:
	$(GO) run github.com/golangci/golangci-lint/cmd/golangci-lint@latest run $(GO_DIRS)

## sast: static application security testing (gosec)
sast:
	$(GO) run github.com/securego/gosec/v2/cmd/gosec@latest -quiet -exclude-dir=node_modules ./...

## deadcode: report unreachable code
deadcode:
	$(GO) run golang.org/x/tools/cmd/deadcode@latest -test $(GO_PKGS)

## dupl: copy-paste detection (Go)
# Threshold 90 (tokens): at 48 it flagged idiomatic minimal code — HTTP handler
# preambles, sibling event-appliers, typed read-model delegators — as clones,
# pushing toward harmful over-abstraction. 90 still catches substantial copy-paste
# (golangci-lint's own dupl default is 150).
dupl:
	$(GO) run github.com/mibk/dupl@latest -threshold 90 $(GO_DIRS)

## vuln: known-vulnerability scan
vuln:
	$(GO) run golang.org/x/vuln/cmd/govulncheck@latest $(GO_PKGS)

## licenses: fail the build on any non-AGPL-compatible dependency
licenses:
	$(GO) run github.com/google/go-licenses@latest check $(GO_PKGS) --allowed_licenses="$(ALLOWED_LICENSES)"

## web: build the SvelteKit UI and embed it (requires npm)
web:
	cd web && npm ci && npm run build
	rm -rf platform/web/assets && cp -r web/build platform/web/assets

## check: fast local gate
check: fmtcheck vet typecheck test

## ci: full gate (matches .github/workflows/ci.yml and the pre-commit pipeline)
ci: fmtcheck vet typecheck lint sast test deadcode dupl vuln licenses

## precommit: run the pre-commit pipeline against the whole tree
precommit:
	pre-commit run --all-files

clean:
	rm -rf bin data
