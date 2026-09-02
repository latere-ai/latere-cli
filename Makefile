# SPDX-FileCopyrightText: 2026 Latere AI
# SPDX-License-Identifier: MIT

# The verification contract for latere-cli.
#
# Every target here is one latere-ai/ci's go-verify workflow probes for and
# runs, so `make <target>` on a laptop is the same check the runner performs.
# The gates themselves live in latere.ai/x/ci-gate, pinned in go.mod; what
# each one asserts for this repository is in .lateregate.yaml.
#
# `make build` stays the local pre-commit gate: it is a superset of the
# pipeline, adding the module-tidy and vulnerability checks.

GO ?= go

.PHONY: check build
build: tidy compile fmt-check lint-modernize test vuln spec-lint ## Full verification gate (run before committing)

.PHONY: tidy
tidy: ## Fail if go.mod/go.sum are not tidy
	$(GO) mod tidy -diff

.PHONY: compile
compile: ## Compile all packages
	$(GO) build ./...

.PHONY: test
test: ## go vet, then the suite
	@go tool lateregate test

.PHONY: test-race
test-race: ## The suite under the detector
	@go tool lateregate race

.PHONY: test-hermetic
test-hermetic: ## The suite with only the Go toolchain on PATH
	@$(GO) tool lateregate hermetic

.PHONY: vuln
vuln: ## Scan for known vulnerabilities
	@go tool lateregate vuln

.PHONY: fmt
fmt: ## Format all Go sources in place
	gofmt -w .

.PHONY: fmt-check
fmt-check: ## Fail if any Go source is not gofmt-formatted
	@$(GO) tool lateregate fmt-check

# .golangci.yml is generated and gitignored: golangci-lint has no config
# inheritance, so the org's set is rendered from latere.ai/x/ci-gate on every
# run. Regenerating is what makes divergence impossible rather than merely
# detectable.
.PHONY: lint-config
lint-config: ## Render .golangci.yml from the shared template
	@$(GO) tool lateregate golangci

.PHONY: lint
lint: ## Run the linter CI runs, against the config renders
	@go tool lateregate lint

# Fails on code a standard library call or a language builtin already covers.
# Carries fixers golangci-lint's modernize linter does not, so it runs whether
# or not the linter does.
.PHONY: lint-modernize
lint-modernize: ## Fail on code the standard library already covers
	@$(GO) tool lateregate modernize

# specs/ records why each surface has the shape it has. A spec tree nobody
# checks drifts from the code within a milestone.
.PHONY: spec-lint
spec-lint: ## Check the spec tree agrees with itself
	@$(GO) tool lateregate spec-lint

# The repo-specific checks the shared pipeline cannot know about. `tidy` was a
# step in the old workflow and would otherwise be lost; `vuln` was local-only,
# and a dependency advisory is a fact about the module graph that nothing else
# here reports.
.PHONY: validate
validate: tidy vuln ## Repo-specific consistency checks

.PHONY: binary
binary: ## Build the latere binary into ./latere
	$(GO) build -o latere ./cmd/latere

.PHONY: hooks
hooks: ## Install repository git hooks (pre-commit gofmt guard)
	git config core.hooksPath .githooks
	@[ -e CLAUDE.md ] || [ -L CLAUDE.md ] || ln -s AGENTS.md CLAUDE.md
	@echo "installed git hooks (core.hooksPath=.githooks)"

.PHONY: help
help: ## List targets
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | \
		awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

# The whole shared bar. Every gate lives in lateregate, pinned as a tool in
# go.mod; this target is a name for `go tool lateregate` and nothing else.
# The plan: `go tool lateregate list`. One gate: `go tool lateregate <gate>`.
check:
	@go tool lateregate
