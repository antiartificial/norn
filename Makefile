SHELL  := /bin/bash
GREEN  := \033[0;32m
YELLOW := \033[0;33m
CYAN   := \033[0;36m
DIM    := \033[2m
RESET  := \033[0m
CHECK  := \xE2\x9C\x94
CROSS  := \xE2\x9C\x98
ARROW  := \xE2\x86\x92

.PHONY: help setup prereqs db dev api ui infra infra-stop build cli install test clean doctor

# ──────────────────────────────────────────────
# The one command to rule them all
# ──────────────────────────────────────────────

help: ## Show this help
	@echo ""
	@echo "  $(CYAN)norn$(RESET) — control plane for your infrastructure"
	@echo ""
	@echo "  $(GREEN)Getting started:$(RESET)"
	@echo "    make setup        one-time setup (prereqs + db + deps)"
	@echo "    make dev          start API + UI for local development"
	@echo ""
	@echo "  $(GREEN)Commands:$(RESET)"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "    $(CYAN)%-16s$(RESET) %s\n", $$1, $$2}'
	@echo ""
	@echo "  $(DIM)API runs on :8800 · UI runs on :5173 · CLI: bin/norn$(RESET)"
	@echo ""

# ──────────────────────────────────────────────
# Setup
# ──────────────────────────────────────────────

setup: prereqs db deps ## One-time setup: check tools, create DB, install deps
	@echo ""
	@printf "  $(GREEN)$(CHECK) All set.$(RESET) Run $(CYAN)make dev$(RESET) to start.\n"
	@echo ""

prereqs: ## Check that required tools are installed
	@echo ""
	@echo "  Checking prerequisites..."
	@echo ""
	@command -v go       >/dev/null 2>&1 && printf "  $(GREEN)$(CHECK)$(RESET) go        $(DIM)$$(go version | cut -d' ' -f3)$(RESET)\n"       || printf "  $(CROSS) go        $(YELLOW)brew install go$(RESET)\n"
	@command -v pnpm     >/dev/null 2>&1 && printf "  $(GREEN)$(CHECK)$(RESET) pnpm      $(DIM)$$(pnpm --version)$(RESET)\n"                   || printf "  $(CROSS) pnpm      $(YELLOW)npm i -g pnpm$(RESET)\n"
	@command -v psql     >/dev/null 2>&1 && printf "  $(GREEN)$(CHECK)$(RESET) postgres   $(DIM)$$(psql --version | head -1)$(RESET)\n"         || printf "  $(CROSS) postgres   $(YELLOW)Install Postgres.app$(RESET)\n"
	@command -v kubectl  >/dev/null 2>&1 && printf "  $(GREEN)$(CHECK)$(RESET) kubectl   $(DIM)$$(kubectl version --client -o json 2>/dev/null | grep gitVersion | tr -d ' \",' | cut -d: -f2)$(RESET)\n" || printf "  $(CROSS) kubectl   $(YELLOW)brew install kubectl$(RESET)\n"
	@command -v docker   >/dev/null 2>&1 && printf "  $(GREEN)$(CHECK)$(RESET) docker    $(DIM)$$(docker --version 2>/dev/null | cut -d' ' -f3 | tr -d ,)$(RESET)\n" || printf "  $(CROSS) docker    $(YELLOW)Install Docker Desktop$(RESET)\n"
	@command -v sops     >/dev/null 2>&1 && printf "  $(GREEN)$(CHECK)$(RESET) sops      $(DIM)$$(sops --version 2>/dev/null)$(RESET)\n"        || printf "  $(CROSS) sops      $(YELLOW)brew install sops$(RESET)\n"
	@command -v age      >/dev/null 2>&1 && printf "  $(GREEN)$(CHECK)$(RESET) age       $(DIM)$$(age --version 2>/dev/null)$(RESET)\n"         || printf "  $(CROSS) age       $(YELLOW)brew install age$(RESET)\n"
	@echo ""

db: ## Create the norn database (idempotent)
	@psql -U $$(whoami) -d postgres -tc "SELECT 1 FROM pg_roles WHERE rolname='norn'" | grep -q 1 \
		|| psql -U $$(whoami) -d postgres -c "CREATE USER norn WITH PASSWORD 'norn';" 2>/dev/null
	@psql -U $$(whoami) -d postgres -tc "SELECT 1 FROM pg_database WHERE datname='norn_db'" | grep -q 1 \
		|| psql -U $$(whoami) -d postgres -c "CREATE DATABASE norn_db OWNER norn;" 2>/dev/null
	@printf "  $(GREEN)$(CHECK)$(RESET) norn_db ready\n"

deps: ## Install all dependencies
	@printf "  $(ARROW) Installing UI dependencies...\n"
	@cd ui && pnpm install --silent 2>/dev/null
	@printf "  $(GREEN)$(CHECK)$(RESET) UI deps installed\n"
	@printf "  $(ARROW) Downloading Go modules (API)...\n"
	@cd api && go mod download 2>/dev/null
	@printf "  $(GREEN)$(CHECK)$(RESET) API modules downloaded\n"
	@printf "  $(ARROW) Downloading Go modules (CLI)...\n"
	@cd cli && go mod download 2>/dev/null
	@printf "  $(GREEN)$(CHECK)$(RESET) CLI modules downloaded\n"

# ──────────────────────────────────────────────
# Development
# ──────────────────────────────────────────────

dev: ## Start API + UI for local development
	@echo ""
	@printf "  $(CYAN)Starting Norn...$(RESET)\n"
	@printf "  $(DIM)API$(RESET)  $(ARROW) http://localhost:8800\n"
	@printf "  $(DIM)UI$(RESET)   $(ARROW) http://localhost:5173\n"
	@echo ""
	@$(MAKE) -j2 api ui

api:
	@cd api && go run .

ui:
	@cd ui && pnpm dev

# ──────────────────────────────────────────────
# Shared Infrastructure
# ──────────────────────────────────────────────

infra: ## Start Valkey + Redpanda (docker compose)
	@cd infra && docker compose up -d
	@printf "  $(GREEN)$(CHECK)$(RESET) Valkey on :6379 · Redpanda on :19092 · Console on :8090\n"

infra-stop: ## Stop shared infrastructure
	@cd infra && docker compose down

# ──────────────────────────────────────────────
# Build & Package
# ──────────────────────────────────────────────

build: ## Production build (API server + CLI + UI static)
	@printf "  $(ARROW) Building UI...\n"
	@cd ui && pnpm build
	@printf "  $(ARROW) Building API server...\n"
	@mkdir -p bin
	@cd api && CGO_ENABLED=0 go build -o ../bin/norn-server .
	@printf "  $(GREEN)$(CHECK)$(RESET) bin/norn-server ready\n"
	@printf "  $(ARROW) Building CLI...\n"
	@cd cli && CGO_ENABLED=0 go build -ldflags "-X norn/cli/cmd.Version=$$(git describe --tags --always --dirty 2>/dev/null || echo dev)" -o ../bin/norn .
	@printf "  $(GREEN)$(CHECK)$(RESET) bin/norn ready\n"

cli: ## Build just the CLI
	@mkdir -p bin
	@cd cli && CGO_ENABLED=0 go build -ldflags "-X norn/cli/cmd.Version=$$(git describe --tags --always --dirty 2>/dev/null || echo dev)" -o ../bin/norn .
	@printf "  $(GREEN)$(CHECK)$(RESET) bin/norn ready\n"

install: cli ## Build CLI and symlink to /usr/local/bin/norn
	@ln -sf $(CURDIR)/bin/norn /usr/local/bin/norn
	@printf "  $(GREEN)$(CHECK)$(RESET) norn installed $(ARROW) /usr/local/bin/norn\n"

test: ## Run all tests (Go + TypeScript)
	@printf "  $(ARROW) Running Go tests...\n"
	@cd api && go test ./... -count=1
	@printf "  $(GREEN)$(CHECK)$(RESET) Go tests passed\n"
	@printf "  $(ARROW) Running UI tests...\n"
	@cd ui && pnpm test
	@printf "  $(GREEN)$(CHECK)$(RESET) UI tests passed\n"

docker: ## Build Docker image
	@docker build -t norn:latest .
	@printf "  $(GREEN)$(CHECK)$(RESET) norn:latest built\n"

clean: ## Remove build artifacts
	@rm -rf bin ui/dist
	@printf "  $(GREEN)$(CHECK)$(RESET) cleaned\n"

# ──────────────────────────────────────────────
# Diagnostics
# ──────────────────────────────────────────────

doctor: ## Check health of all services
	@echo ""
	@echo "  Checking services..."
	@echo ""
	@pg_isready -q 2>/dev/null \
		&& printf "  $(GREEN)$(CHECK)$(RESET) PostgreSQL        running\n" \
		|| printf "  $(CROSS) PostgreSQL        not running\n"
	@psql -U norn -d norn_db -c "SELECT 1" >/dev/null 2>&1 \
		&& printf "  $(GREEN)$(CHECK)$(RESET) norn_db           accessible\n" \
		|| printf "  $(CROSS) norn_db           not accessible $(DIM)(run: make db)$(RESET)\n"
	@curl -sf http://localhost:8800/api/health >/dev/null 2>&1 \
		&& printf "  $(GREEN)$(CHECK)$(RESET) Norn API          running on :8800\n" \
		|| printf "  $(DIM)·$(RESET) Norn API          not running $(DIM)(run: make dev)$(RESET)\n"
	@curl -sf http://localhost:5173 >/dev/null 2>&1 \
		&& printf "  $(GREEN)$(CHECK)$(RESET) Norn UI           running on :5173\n" \
		|| printf "  $(DIM)·$(RESET) Norn UI           not running $(DIM)(run: make dev)$(RESET)\n"
	@docker compose -f infra/docker-compose.yml ps --status running 2>/dev/null | grep -q valkey \
		&& printf "  $(GREEN)$(CHECK)$(RESET) Valkey            running on :6379\n" \
		|| printf "  $(DIM)·$(RESET) Valkey            not running $(DIM)(run: make infra)$(RESET)\n"
	@docker compose -f infra/docker-compose.yml ps --status running 2>/dev/null | grep -q redpanda \
		&& printf "  $(GREEN)$(CHECK)$(RESET) Redpanda          running on :19092\n" \
		|| printf "  $(DIM)·$(RESET) Redpanda          not running $(DIM)(run: make infra)$(RESET)\n"
	@kubectl cluster-info >/dev/null 2>&1 \
		&& printf "  $(GREEN)$(CHECK)$(RESET) Kubernetes        $$(kubectl config current-context)\n" \
		|| printf "  $(DIM)·$(RESET) Kubernetes        not connected $(DIM)(start minikube)$(RESET)\n"
	@test -f "$(HOME)/.config/sops/age/keys.txt" \
		&& printf "  $(GREEN)$(CHECK)$(RESET) SOPS age key      configured\n" \
		|| printf "  $(CROSS) SOPS age key      missing $(DIM)(age-keygen -o ~/.config/sops/age/keys.txt)$(RESET)\n"
	@echo ""
