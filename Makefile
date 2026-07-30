.DEFAULT_GOAL := help
SHELL := /bin/bash

# Migrations run as the owning role; the application connects as yol_app so that row
# level security applies. Never point the application at this URL.
DB_URL ?= postgres://yol:yol@localhost:5442/yol?sslmode=disable
MIGRATIONS := internal/db/migrations

COMPOSE := docker compose -f dev/compose.yaml
VPS_KEY := dev/fake-vps/keys/id_ed25519
VPS_SSH := ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR -i $(VPS_KEY)
N ?= 1

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

.PHONY: vps-keys
vps-keys: ## Generate the harness SSH key if it is missing
	@test -f $(VPS_KEY) || { \
		mkdir -p dev/fake-vps/keys; \
		ssh-keygen -t ed25519 -N '' -C yol-dev-harness -f $(VPS_KEY) -q; \
		echo "generated $(VPS_KEY)"; }
	@cp $(VPS_KEY).pub dev/fake-vps/authorized_keys

.PHONY: vps-up
vps-up: vps-keys ## Start the stand-in servers (ports 2201-2203)
	$(COMPOSE) --profile vps up -d --build vps-1 vps-2 vps-3
	@echo "waiting for all three to accept connections and run containers"
	@for port in 2201 2202 2203; do \
		until $(VPS_SSH) -o ConnectTimeout=2 -p $$port root@localhost 'docker info >/dev/null 2>&1' 2>/dev/null; do sleep 1; done; \
		echo "  ready on $$port"; \
	done

.PHONY: vps-down
vps-down: ## Stop the stand-in servers, keeping their disks
	$(COMPOSE) --profile vps stop vps-1 vps-2 vps-3

.PHONY: vps-reset
vps-reset: ## Destroy the stand-in servers and their disks
	$(COMPOSE) --profile vps down -v vps-1 vps-2 vps-3

.PHONY: vps-ssh
vps-ssh: ## Open a shell on a stand-in server (make vps-ssh N=2)
	$(VPS_SSH) -p 220$(N) root@localhost

.PHONY: vps-status
vps-status: ## Show what each stand-in server is running
	@for n in 1 2 3; do \
		printf '\033[1mvps-%s\033[0m (ssh 220%s, http 810%s)\n' $$n $$n $$n; \
		$(VPS_SSH) -o ConnectTimeout=2 -p 220$$n root@localhost \
			'echo "  systemd $$(systemctl is-system-running)  docker $$(systemctl is-active docker)"; \
			 docker ps --format "  {{.Names}} {{.Status}}" | head -10' 2>/dev/null \
			|| echo "  not reachable"; \
	done

.PHONY: migrate-up
migrate-up: ## Apply all migrations
	goose -dir $(MIGRATIONS) postgres "$(DB_URL)" up
	@YOL_OWNER_DATABASE_URL="$(DB_URL)" go run ./cmd/jobsmigrate

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

.PHONY: test-e2e
test-e2e: ## Drive the API end to end (rebuilds the database, starts the API)
	./dev/e2e.sh -v

.PHONY: vps-messy
vps-messy: ## Make vps-2 look like a server already in use, for testing discovery
	@$(VPS_SSH) -p 2202 root@localhost 'set -e; \
		docker rm -f their-nginx their-postgres old-worker >/dev/null 2>&1 || true; \
		docker run -d --name their-nginx --restart=unless-stopped -p 80:80 -p 443:443 nginx:alpine >/dev/null; \
		docker run -d --name their-postgres --restart=unless-stopped -e POSTGRES_PASSWORD=theirs -p 5432:5432 postgres:16-alpine >/dev/null; \
		docker run -d --name old-worker alpine:latest sh -c "sleep 1" >/dev/null; \
		docker volume create their-data >/dev/null; \
		while [ "$$(docker inspect -f "{{.State.Status}}" old-worker)" = "running" ]; do sleep 1; done; \
		echo "vps-2 now has their nginx on 80/443, a hand-run postgres, a dead worker and a stray volume"'

.PHONY: test-live
test-live: ## Run checks against the harness servers (needs make vps-up)
	YOL_LIVE_HOST=localhost YOL_LIVE_PORT=2202 YOL_LIVE_KEY=$(CURDIR)/$(VPS_KEY) YOL_LIVE_MESSY=1 \
	go test -tags live -count=1 -v ./internal/ssh/

.PHONY: verify-phase1
verify-phase1: ## Check the promises made about a customer's server, against the harness
	./dev/verify-phase1.sh

.PHONY: lint
lint: ## Vet and format check
	go vet ./...
	test -z "$$(gofmt -l cmd internal)"

.PHONY: build
build: ## Build both binaries into bin/
	go build -o bin/yol-api ./cmd/api
	go build -o bin/yol-agent ./cmd/agent

.PHONY: build-agent-linux
build-agent-linux: ## Build the agent for a server (static, no dependencies)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o bin/yol-agent-linux-arm64 ./cmd/agent
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o bin/yol-agent-linux-amd64 ./cmd/agent
	@ls -lh bin/yol-agent-linux-* | awk '{print "  " $$9, $$5}' 

.PHONY: tools
tools: ## Install dev tooling
	go install github.com/pressly/goose/v3/cmd/goose@latest
	go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest
