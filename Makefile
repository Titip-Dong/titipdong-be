# Developer commands. Run `make` or `make help` to list them.
# Tools run through `go run pkg@version` so everyone (and CI) uses the same pinned
# versions without installing anything. The first run of each tool compiles it.

SQLC_VERSION          := v1.30.0
GOOSE_VERSION         := v3.28.0
GOLANGCI_LINT_VERSION := v2.14.0

SQLC          := go run github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION)
GOOSE         := go run github.com/pressly/goose/v3/cmd/goose@$(GOOSE_VERSION)
GOLANGCI_LINT := go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

.DEFAULT_GOAL := help

.PHONY: help
help: ## List available commands
	@grep -E '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

## Local database (compose.yaml)

.PHONY: db-up
db-up: ## Start local PostgreSQL and API containers, then wait until healthy
	docker compose up -d --wait

.PHONY: db-down
db-down: ## Stop local PostgreSQL and API containers, keeping database data
	docker compose down

.PHONY: db-reset
db-reset: ## Delete local PostgreSQL data and start fresh
	docker compose down -v
	docker compose up -d --wait

.PHONY: db-shell
db-shell: ## Open psql in the local PostgreSQL
	docker compose exec postgres psql -U titipdong -d titipdong

## Development

.PHONY: run
run: ## Run the API on the host (reads .env, applies migrations on startup)
	go run ./cmd/api

.PHONY: seed
seed: ## Create the demo accounts (idempotent)
	go run ./cmd/api seed-demo

.PHONY: openapi
openapi: ## Write the OpenAPI spec to openapi.json (no database needed)
	go run ./cmd/api openapi > openapi.json

.PHONY: build
build: ## Build the API binary into bin/
	go build -o bin/titipdong-api ./cmd/api

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf bin/

.PHONY: sqlc
sqlc: ## Generate internal/db from db/queries and migrations
	$(SQLC) generate

.PHONY: migration
migration: ## Create a migration: make migration name=create_trips
	@test -n "$(name)" || { echo "usage: make migration name=create_trips"; exit 1; }
	$(GOOSE) -dir db/migrations -s create $(name) sql

.PHONY: tidy
tidy: ## Tidy go.mod and go.sum
	go mod tidy

## Quality (what CI checks)

.PHONY: fmt
fmt: ## Format all Go code
	gofmt -w .

.PHONY: fmt-check
fmt-check: ## Fail if any Go file is not formatted
	@files="$$(gofmt -l .)"; test -z "$$files" || { echo "Not formatted:"; echo "$$files"; exit 1; }

.PHONY: vet
vet: ## Run go vet
	go vet ./...

.PHONY: lint
lint: ## Run golangci-lint
	$(GOLANGCI_LINT) run ./...

.PHONY: sqlc-check
sqlc-check: ## Fail if generated sqlc code is out of date
	$(SQLC) diff

.PHONY: test
test: ## Run unit tests (no database needed)
	go test ./...

.PHONY: check
check: fmt-check vet lint sqlc-check test ## Run every check CI runs
