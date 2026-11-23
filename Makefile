VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/singhsays/tidepull/internal/version.Version=$(VERSION) -X github.com/singhsays/tidepull/internal/version.Commit=$(shell git rev-parse --short HEAD 2>/dev/null)
GO      ?= go

# Prefer the locally installed golangci-lint; fall back to a pinned go run.
GOLANGCI_LINT_VERSION := v2.15.0
GOLANGCI_LINT := $(shell command -v golangci-lint 2>/dev/null || echo "$(GO) run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)")

# HTMX_VERSION and HTMX_SHA256 pin the vendored asset fetched by fetch-htmx;
# bump both together.
HTMX_VERSION := 2.0.4
HTMX_SHA256 := e209dda5c8235479f3166defc7750e1dbcd5a5c1808b7792fc2e6733768fb447

.PHONY: build build-linux install test fmt vet lint lint-fix check fetch-htmx

build:
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/tidepull ./cmd/tidepull

build-linux:
	GOOS=linux GOARCH=amd64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/tidepull-linux-amd64 ./cmd/tidepull

install:
	$(GO) install -trimpath -ldflags "$(LDFLAGS)" ./cmd/tidepull

test:
	$(GO) test -race ./...

fmt:
	$(GOLANGCI_LINT) fmt

vet:
	$(GO) vet ./...

lint:
	$(GOLANGCI_LINT) run

lint-fix:
	$(GOLANGCI_LINT) run --fix

# check is the local mirror of CI: run before committing.
check: vet lint test build

# fetch-htmx re-downloads the pinned htmx asset and verifies its hash.
fetch-htmx:
	curl -fsSL https://unpkg.com/htmx.org@$(HTMX_VERSION)/dist/htmx.min.js -o internal/server/assets/htmx.min.js
	echo "$(HTMX_SHA256)  internal/server/assets/htmx.min.js" | shasum -a 256 -c -
