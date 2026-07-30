.DEFAULT_GOAL := help
SHELL := /bin/bash

# Migrations run as the owning role; the application connects as yol_app so that row
# level security applies. Never point the application at this URL.
DB_URL ?= postgres://yol:yol@localhost:5442/yol?sslmode=disable
MIGRATIONS := internal/db/migrations

.PHONY: help
help: ## List available targets
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN{FS=":.*?## "}{printf "  \033[1m%-16s\033[0m %s\n", $$1, $$2}'

.PHONY: dev-db
dev-db: ## Start local Postgres
	docker compose -f dev/compose.yaml up -d postgres

.PHONY: dev-db-stop
dev-db-stop: ## Stop local Postgres
	docker compose -f dev/compose.yaml down

.PHONY: db-reset
db-reset: ## Destroy and rebuild the local database from scratch
	docker compose -f dev/compose.yaml down -v
	docker compose -f dev/compose.yaml up -d postgres
	until docker compose -f dev/compose.yaml exec -T postgres pg_isready -U yol -d yol >/dev/null 2>&1; do sleep 1; done
	$(MAKE) migrate-up

.PHONY: migrate-up
migrate-up: ## Apply all migrations
	goose -dir $(MIGRATIONS) postgres "$(DB_URL)" up

.PHONY: migrate-down
migrate-down: ## Roll back the last migration
	goose -dir $(MIGRATIONS) postgres "$(DB_URL)" down

.PHONY: migrate-status
migrate-status: ## Show migration status
	goose -dir $(MIGRATIONS) postgres "$(DB_URL)" status

.PHONY: sqlc
sqlc: ## Regenerate typed queries
	sqlc generate

# Loads .env explicitly; a missing file fails here rather than defaulting silently.
define with_env
	set -a && . ./.env && set +a &&
endef

.PHONY: api
api: ## Run the control plane API
	$(with_env) go run ./cmd/api

.PHONY: agent
agent: ## Run the agent locally
	$(with_env) go run ./cmd/agent

.PHONY: web
web: ## Run the frontend dev server
	cd web && pnpm dev

.PHONY: test
test: ## Run Go tests (database tests skip unless the local database is up)
	YOL_TEST_DATABASE_URL="postgres://yol_app:yol_app@localhost:5442/yol?sslmode=disable" \
	YOL_TEST_OWNER_DATABASE_URL="$(DB_URL)" \
	go test ./...

.PHONY: lint
lint: ## Vet and format check
	go vet ./...
	test -z "$$(gofmt -l cmd internal)"

.PHONY: build
build: ## Build both binaries into bin/
	go build -o bin/yol-api ./cmd/api
	go build -o bin/yol-agent ./cmd/agent

.PHONY: tools
tools: ## Install dev tooling
	go install github.com/pressly/goose/v3/cmd/goose@latest
	go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest
