# Every check CI runs can be run locally with `make ci`.

GO_TOOLCHAIN          := go1.27.3
GOLANGCI_LINT_VERSION := v2.14.0
GOVULNCHECK_VERSION   := v1.9.0
TLA_TOOLS_VERSION     := v1.7.4
TLA_TOOLS_SHA256      := 936a262061c914694dfd669a543be24573c45d5aa0ff20a8b96b23d01e050e88

BIN           := $(CURDIR)/bin
GOLANGCI_LINT := $(BIN)/golangci-lint-$(GOLANGCI_LINT_VERSION)/golangci-lint
GOVULNCHECK   := $(BIN)/govulncheck-$(GOVULNCHECK_VERSION)/govulncheck
TLA_TOOLS     := $(BIN)/tla2tools-$(TLA_TOOLS_VERSION).jar

.PHONY: help fmt lint test test-integration vulncheck spec ci up down logs

help: ## Show the targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-18s %s\n", $$1, $$2}'

fmt: ## Fail if a file is not formatted
	@unformatted=$$(gofmt -l cmd internal migrations); \
	if [ -n "$$unformatted" ]; then echo "not formatted:"; echo "$$unformatted"; exit 1; fi

lint: $(GOLANGCI_LINT) ## Run the linters
	$(GOLANGCI_LINT) run

test: ## Run the unit tests (no Docker needed)
	go test -race -short ./...

test-integration: ## Run every test, starting Postgres and Valkey in containers
	go test -race -count=1 ./...

vulncheck: $(GOVULNCHECK) ## Check dependencies and the standard library for known vulnerabilities
	$(GOVULNCHECK) ./...

spec: $(TLA_TOOLS) ## Check the TLA+ model of the authorization protocol, and that every mutation is caught
	spec/check.sh $(TLA_TOOLS)

ci: fmt lint test-integration vulncheck ## Run what CI runs

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

# The official TLA+ tools, checked against a known hash before they run.
$(TLA_TOOLS):
	@mkdir -p $(BIN)
	curl -fsSL -o $@.download https://github.com/tlaplus/tlaplus/releases/download/$(TLA_TOOLS_VERSION)/tla2tools.jar
	echo "$(TLA_TOOLS_SHA256)  $@.download" | shasum -a 256 -c -
	mv $@.download $@
