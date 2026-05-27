# voicelog — common dev tasks. Mirrors what CI runs so a local
# `make ci` should give the same answer as a pushed branch.

GO          ?= go
COVER_FILE  ?= coverage.out

.PHONY: help test test-race build vet lint vuln fmt tidy ci clean

help: ## Show this help.
	@awk 'BEGIN {FS = ":.*##"; printf "Targets:\n"} /^[a-zA-Z_-]+:.*?##/ { printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

test: ## Run the test suite (no race, fast).
	$(GO) test ./... -count=1

test-race: ## Run the test suite with the race detector. Requires CGO.
	CGO_ENABLED=1 $(GO) test -race -count=1 -coverprofile=$(COVER_FILE) -covermode=atomic ./...

build: ## Build both binaries (CGO disabled — same as production images).
	CGO_ENABLED=0 $(GO) build ./...

vet: ## go vet across the whole module.
	$(GO) vet ./...

lint: ## Run staticcheck (must already be installed: `go install honnef.co/go/tools/cmd/staticcheck@latest`).
	staticcheck ./...

vuln: ## Reachable-vuln scan via govulncheck (CI uses v1.1.4 — install matching).
	govulncheck ./...

fmt: ## gofmt -w on every Go file.
	$(GO) fmt ./...

tidy: ## go mod tidy.
	$(GO) mod tidy

ci: vet lint build test-race vuln ## Run the same gates CI runs.

clean: ## Remove build artefacts.
	rm -f $(COVER_FILE)
