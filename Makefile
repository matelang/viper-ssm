# Run `make` for the target list. `make check` runs what CI runs.

GO ?= go
GOLANGCI_LINT ?= golangci-lint
GOVULNCHECK ?= golang.org/x/vuln/cmd/govulncheck@latest

# The AWS emulator the integration tier reads and writes against.
FLOCI_ENDPOINT ?= http://localhost:4566

.DEFAULT_GOAL := help

.PHONY: help
help: ## List targets
	@grep -hE '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-8s\033[0m %s\n", $$1, $$2}'

.PHONY: check
check: tidy vet lint test ## Run everything CI runs

.PHONY: test
test: ## Run tests with the race detector
	$(GO) test -race -count=1 -coverprofile=coverage.out ./...

.PHONY: test-integration
test-integration: ## Run the integration tier against Floci (skips if it is not up)
	FLOCI_ENDPOINT=$(FLOCI_ENDPOINT) $(GO) test -tags=integration -race -count=1 ./...

.PHONY: test-integration-strict
test-integration-strict: ## Same, but fail rather than skip when Floci is down (what CI runs)
	FLOCI_ENDPOINT=$(FLOCI_ENDPOINT) FLOCI_REQUIRED=1 \
		$(GO) test -tags=integration -race -count=1 ./...

.PHONY: cover
cover: test ## Report coverage per function
	$(GO) tool cover -func=coverage.out

.PHONY: vet
vet: ## Run go vet
	$(GO) vet ./...

.PHONY: lint
lint: ## Run golangci-lint
	$(GOLANGCI_LINT) run

.PHONY: fmt
fmt: ## Format, and apply the fixes linters can make
	$(GOLANGCI_LINT) fmt
	$(GOLANGCI_LINT) run --fix

.PHONY: tidy
tidy: ## Check go.mod and go.sum are tidy
	$(GO) mod tidy -diff

.PHONY: vuln
vuln: ## Check dependencies against the Go vulnerability database
	$(GO) run $(GOVULNCHECK) ./...

.PHONY: clean
clean: ## Remove build and coverage output
	rm -f coverage.out
	$(GO) clean -testcache
