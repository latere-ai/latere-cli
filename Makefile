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

.PHONY: fmt-check
fmt-check: ## Fail if any Go source is not gofmt-formatted
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt: unformatted files:"; echo "$$out"; exit 1; fi

.PHONY: hooks
hooks: ## Install repository git hooks (pre-commit gofmt guard)
	git config core.hooksPath .githooks
	@echo "installed git hooks (core.hooksPath=.githooks)"

.PHONY: lint
lint: ## Stricter lint via golangci-lint (has pre-existing findings; not in `make build`)
	golangci-lint run ./...

# lint-modernize fails on code that a standard library call already covers.
# It runs the toolchain modernizers, which overlap golangci-lint's modernize
# linter but add three it does not carry: buildtag, hostport, and the
# go:fix inline directives. newexpr and errorsastype are off for the reasons
# recorded in .golangci.yml.
# Only a non-empty patch fails the target. go fix also exits non-zero when a
# package does not type-check, which is a build error rather than a finding,
# so stderr is dropped and the decision rests on the patch alone.
.PHONY: lint-modernize
lint-modernize: ## Fail on code the standard library already covers
	@patch=$$($(GO) fix -diff -newexpr=false -errorsastype=false ./... 2>/dev/null); \
	if [ -n "$$patch" ]; then \
		echo "$$patch"; \
		echo "go fix: the diff above is already in the standard library; apply it with go fix"; \
		exit 1; \
	fi

.PHONY: binary
binary: ## Build the latere binary into ./latere
	$(GO) build -o latere ./cmd/latere

.PHONY: help
help: ## List targets
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | \
		awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'
