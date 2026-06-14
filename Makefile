# Verification entry points for latere-cli.
#
# `make build` is the gate referenced in CLAUDE.md: lint + govulncheck + test.
# It is a superset of CI (.github/workflows/ci.yaml runs tidy/vet/build/test),
# adding govulncheck. Run it before committing.

GO ?= go

.PHONY: build
build: tidy vet compile vuln test ## Full verification gate (run before committing)

.PHONY: tidy
tidy: ## Fail if go.mod/go.sum are not tidy
	$(GO) mod tidy -diff

.PHONY: vet
vet: ## go vet
	$(GO) vet ./...

.PHONY: compile
compile: ## Compile all packages
	$(GO) build ./...

.PHONY: test
test: ## Run tests
	$(GO) test ./...

.PHONY: vuln
vuln: ## Scan for known vulnerabilities
	$(GO) run golang.org/x/vuln/cmd/govulncheck@latest ./...

.PHONY: fmt
fmt: ## Format all Go sources in place
	gofmt -w .

.PHONY: lint
lint: ## Stricter lint via golangci-lint (has pre-existing findings; not in `make build`)
	golangci-lint run ./...

.PHONY: binary
binary: ## Build the latere binary into ./latere
	$(GO) build -o latere ./cmd/latere

.PHONY: help
help: ## List targets
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | \
		awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'
