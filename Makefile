# SPDX-License-Identifier: AGPL-3.0-or-later
GO ?= go
BIN := bin/intraktible

# AGPL-compatible licenses allowed for Go dependencies (see docs/LICENSING.md).
ALLOWED_LICENSES := MIT,BSD-2-Clause,BSD-3-Clause,ISC,0BSD,Unlicense,Apache-2.0,MPL-2.0,\
PostgreSQL,LGPL-2.1,LGPL-3.0,GPL-2.0,GPL-3.0,AGPL-3.0

.PHONY: all build run test fmt fmtcheck vet lint deadcode dupl vuln licenses check ci web clean

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

## fmt: format the tree
fmt:
	gofmt -w .

## fmtcheck: fail if anything is unformatted (CI)
fmtcheck:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "unformatted:"; echo "$$out"; exit 1; fi

## vet: go vet
vet:
	$(GO) vet ./...

## lint: golangci-lint (strict)
lint:
	$(GO) run github.com/golangci/golangci-lint/cmd/golangci-lint@latest run

## deadcode: report unreachable code
deadcode:
	$(GO) run golang.org/x/tools/cmd/deadcode@latest -test ./...

## dupl: copy-paste detection (Go)
dupl:
	$(GO) run github.com/mibk/dupl@latest -threshold 48 .

## vuln: known-vulnerability scan
vuln:
	$(GO) run golang.org/x/vuln/cmd/govulncheck@latest ./...

## licenses: fail the build on any non-AGPL-compatible dependency
licenses:
	$(GO) run github.com/google/go-licenses@latest check ./... --allowed_licenses="$(ALLOWED_LICENSES)"

## web: build the SvelteKit UI and embed it (requires npm)
web:
	cd web && npm ci && npm run build
	rm -rf platform/web/assets && cp -r web/build platform/web/assets

## check: fast local gate
check: fmtcheck vet test

## ci: full gate (matches .github/workflows/ci.yml)
ci: fmtcheck vet test lint deadcode dupl vuln licenses

clean:
	rm -rf bin data
