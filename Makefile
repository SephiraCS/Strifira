.PHONY: dev dev-all dev-backend dev-frontend dev-start-services dev-reset migrate-up migrate-down seed reset-dev test test-fast test-slow test-fast-backend test-fast-frontend test-slow-backend test-slow-frontend test-slow-live test-migrate check-backend check-gofmt lint docker-up docker-down

# Load .env into shell commands
DOTENV := $(shell [ -f .env ] && echo "set -a && . ./.env && set +a &&" || echo "")
DOCKER_SOCKET_GID = $$(docker run --rm -v /var/run/docker.sock:/docker.sock:ro alpine:3.22 stat -c '%g' /docker.sock)
DEV_MACHINE_COMPOSE = DEV_MACHINE_DOCKER_GID="$(DOCKER_SOCKET_GID)" docker compose

dev:
	$(MAKE) migrate-up
	@echo "Starting backend and frontend..."
	$(MAKE) -j2 dev-backend dev-frontend

dev-all: dev-start-services dev

dev-start-services:
	@echo "Starting Postgres..."
	$(DEV_MACHINE_COMPOSE) up -d --wait postgres
	@echo "Applying migrations required by the Dev Machine gateway role..."
	$(MAKE) migrate-up
	@echo "Ensuring local Dev Machine runtime images exist..."
	$(DEV_MACHINE_COMPOSE) --profile dev-machine-images build dev-machine-ide dev-machine-browser dev-machine-collector dev-machine-egress dev-machine-agent-claude dev-machine-agent-opencode dev-machine-agent-codex
	@echo "Starting Dev Machine control plane and wildcard TLS proxy (*.machines.localhost)..."
	@echo "Note: this dev proxy binds 127.0.0.1:80 and 127.0.0.1:443; stop the selfhosting stack first if those ports are occupied."
	$(DEV_MACHINE_COMPOSE) --profile dev-machines rm -sf machine-gateway-db-provision >/dev/null 2>&1 || true
	$(DEV_MACHINE_COMPOSE) --profile dev-machines up -d --build --wait machine-gateway-db-provision machine-gateway machine-manager machine-proxy

dev-reset:
	$(MAKE) reset-dev
	$(MAKE) seed
	$(MAKE) dev-start-services
	$(MAKE) dev

dev-full: dev-start-services
	@echo "Starting backend and frontend..."
	$(MAKE) -j2 dev-backend dev-frontend

dev-backend:
	$(DOTENV) cd BE && go run ./cmd/server

dev-frontend:
	$(DOTENV) cd UI && npm run dev

migrate-up:
	$(DOTENV) cd BE && go run ./cmd/server migrate up

migrate-down:
	$(DOTENV) cd BE && go run ./cmd/server migrate down

seed:
	bash scripts/seed.sh

reset-dev:
	bash scripts/reset_dev.sh

# Tests (AGENTS.md): integration tests only, in exactly two suites.
#
#   fast  critical paths; blocks every deploy to alpha (ci.yml).
#   slow  everything else; runs in slow.yml after each push to main and
#         nightly, files an issue on failure, never blocks a deploy.
#
# Membership:
#   Go          tests named TestSlow* are slow; every other test is fast.
#   Playwright  FAST_SPECS in UI/playwright.config.ts; other specs are slow.
#   Live checks scripts/check-*-live.* are slow.
#
# Backend suites need PostgreSQL (TEST_DATABASE_URL;
# `make dev-start-services` locally). TEST_DATABASE_URL must name a separate
# test database (in the environment or .env); it is migrated first, the
# targets refuse to run without it or when it names the .env DATABASE_URL
# (scripts/ci/check-test-database.sh), and the suites run with DATABASE_URL
# set to it, so tests that read only DATABASE_URL never touch the app database.
#
# The frontend suites and the live checks build into UI/build and serve on
# fixed ports, so `test` and `test-slow` run their parts one after another
# even under `make -j` (each part still uses -j inside).
# Locally prefix with `nice -n 19` (AGENTS.md); CI runs them as is.
GO_SLOW_TESTS := ^TestSlow
GO_TEST_FLAGS ?= -race -count=1 -timeout=6m
E2E_FLAGS ?=

test:
	$(MAKE) test-fast
	$(MAKE) test-slow

test-fast: test-fast-backend test-fast-frontend

test-slow:
	$(MAKE) test-slow-backend test-slow-frontend
	$(MAKE) test-slow-live

# Formatting and vet; not a test suite, but CI runs both next to test-fast.
check-backend: check-gofmt
	cd BE && go vet ./...

check-gofmt:
	@cd BE && unformatted=$$(gofmt -l .) && if [ -n "$$unformatted" ]; then \
		echo "gofmt needed (run gofmt -w . in BE):"; echo "$$unformatted"; exit 1; fi

test-migrate:
	@$(DOTENV) sh scripts/ci/check-test-database.sh
	$(DOTENV) cd BE && dir=$$(mktemp -d) && trap 'rm -rf "$$dir"' EXIT && \
		go build -o "$$dir/strifira-server" ./cmd/server && \
		DATABASE_URL="$$TEST_DATABASE_URL" "$$dir/strifira-server" migrate up

test-fast-backend: test-migrate
	$(DOTENV) cd BE && DATABASE_URL="$$TEST_DATABASE_URL" go test $(GO_TEST_FLAGS) -skip '$(GO_SLOW_TESTS)' ./...

test-slow-backend: test-migrate
	$(DOTENV) cd BE && DATABASE_URL="$$TEST_DATABASE_URL" go test $(GO_TEST_FLAGS) -run '$(GO_SLOW_TESTS)' ./...

test-fast-frontend:
	cd UI && E2E_SUITE=fast npx playwright test $(E2E_FLAGS)

test-slow-frontend:
	cd UI && E2E_SUITE=slow npx playwright test $(E2E_FLAGS)

# Disposable PostgreSQL/backend/browser checks. The AI provider check needs a
# real provider and runs only when STRIFIRA_LIVE_PROVIDER_KEY, STRIFIRA_LIVE_PROVIDER_URL
# and STRIFIRA_LIVE_PROVIDER_MODEL are set. check-dev-box-live.py needs an
# authenticated coding CLI on a dev VM and is run by hand only.
test-slow-live:
	node scripts/check-screen-share-live.mjs
	@if [ -n "$${STRIFIRA_LIVE_PROVIDER_KEY:-}" ] && [ -n "$${STRIFIRA_LIVE_PROVIDER_URL:-}" ] && [ -n "$${STRIFIRA_LIVE_PROVIDER_MODEL:-}" ]; then \
		dir=$$(mktemp -d) && trap 'rm -rf "$$dir"' EXIT && umask 077 && \
		printf 'CLIPROXYAPI_API_KEY=%s\n' "$$STRIFIRA_LIVE_PROVIDER_KEY" >"$$dir/provider.env" && \
		python3 scripts/check-ai-provider-live.py --provider-env-file "$$dir/provider.env" \
			--provider-base-url "$$STRIFIRA_LIVE_PROVIDER_URL" --model "$$STRIFIRA_LIVE_PROVIDER_MODEL" \
			$${STRIFIRA_LIVE_PROVIDER_TRUST_ORIGIN:+--trust-provider-origin}; \
	else \
		echo "Skipping the AI provider live check: STRIFIRA_LIVE_PROVIDER_KEY/_URL/_MODEL are not set."; \
	fi

lint:
	cd BE && golangci-lint run ./...
	cd UI && npm run lint

docker-up:
	docker compose --profile app up --build -d

docker-down:
	docker compose down
