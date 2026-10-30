# Every check CI runs can be run locally with `make ci`.

GO_TOOLCHAIN          := go1.27.3
GOLANGCI_LINT_VERSION := v2.14.0
GOVULNCHECK_VERSION   := v1.9.0

BIN           := $(CURDIR)/bin
GOLANGCI_LINT := $(BIN)/golangci-lint-$(GOLANGCI_LINT_VERSION)/golangci-lint
GOVULNCHECK   := $(BIN)/govulncheck-$(GOVULNCHECK_VERSION)/govulncheck

.PHONY: help fmt lint test test-integration vulncheck web ci up down logs proof

help: ## Show the targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-18s %s\n", $$1, $$2}'

fmt: ## Fail if a file is not formatted
	@unformatted=$$(gofmt -l cmd internal migrations tests); \
	if [ -n "$$unformatted" ]; then echo "not formatted:"; echo "$$unformatted"; exit 1; fi

lint: $(GOLANGCI_LINT) ## Run the linters
	$(GOLANGCI_LINT) run

test: ## Run the unit tests (no Docker needed)
	go test -race -short ./...

test-integration: ## Run every test, starting Postgres in a container
	go test -race -count=1 ./...

vulncheck: $(GOVULNCHECK) ## Check dependencies and the standard library for known vulnerabilities
	$(GOVULNCHECK) ./...

web: ## Test and build the web app, with its type check
	cd web && npm ci --no-audit --no-fund && npm test && NEXT_TELEMETRY_DISABLED=1 npm run build

ci: fmt lint test-integration vulncheck web ## Run what CI runs

up: ## Start the local environment and wait until it is healthy
	docker compose up --build --wait

down: ## Stop the local environment and delete its data
	docker compose down --volumes

logs: ## Follow the logs of the local environment
	docker compose logs --follow

# Tools are pinned and built with the project's toolchain, one folder per
# version, so a version bump installs the new one.
$(GOLANGCI_LINT):
	GOBIN=$(dir $@) GOTOOLCHAIN=$(GO_TOOLCHAIN) go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

$(GOVULNCHECK):
	GOBIN=$(dir $@) GOTOOLCHAIN=$(GO_TOOLCHAIN) go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)

proof: ## Load test with faults, then the consistency checker (step 9), on a running environment
	tests/load/run.sh
