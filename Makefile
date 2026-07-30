.DEFAULT_GOAL := help
SHELL := /bin/bash

DB_URL ?= postgres://yol:yol@localhost:5433/yol?sslmode=disable
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

.PHONY: api
api: ## Run the control plane API
	go run ./cmd/api

.PHONY: agent
agent: ## Run the agent locally
	go run ./cmd/agent

.PHONY: web
web: ## Run the frontend dev server
	cd web && pnpm dev

.PHONY: test
test: ## Run Go tests
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
