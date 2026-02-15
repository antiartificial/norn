SHELL := /bin/bash

.PHONY: dev api cli build doctor clean ui \
	consul-dev nomad-dev consul-server nomad-server \
	up up-fed down logs

VERSION ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "dev")

dev: doctor
	cd api && NORN_REGISTRY_URL=ghcr.io/antiartificial go run .

api:
	cd api && go build -ldflags "-X main.Version=$(VERSION)" -o ../bin/norn-api .

cli:
	cd cli && go build -ldflags "-X norn/v2/cli/cmd.Version=$(VERSION)" -o ../bin/norn .

build: api cli
	@echo "built api + cli → bin/"

ui:
	cd ui && pnpm build

doctor:
	@echo "checking dependencies..."
	@command -v go >/dev/null 2>&1 || { echo "go not found"; exit 1; }
	@command -v psql >/dev/null 2>&1 || echo "WARNING: psql not found"
	@command -v nomad >/dev/null 2>&1 || echo "WARNING: nomad not found"
	@command -v consul >/dev/null 2>&1 || echo "WARNING: consul not found"
	@command -v docker >/dev/null 2>&1 || echo "WARNING: docker not found"
	@echo "ready"

clean:
	rm -rf bin/

# --- Orchestrated dev (background, logs to dev/logs/) ---

up: doctor
	@mkdir -p dev/logs
	@echo "starting consul..."
	@consul agent -dev > dev/logs/consul.log 2>&1 &
	@sleep 1
	@echo "starting nomad..."
	@nomad agent -dev -bind 0.0.0.0 -config dev/nomad-dev.hcl \
	  > dev/logs/nomad.log 2>&1 &
	@sleep 2
	@echo "starting api..."
	@cd api && go run . > ../dev/logs/api.log 2>&1 &
	@sleep 1
	@echo "all services started. logs in dev/logs/"

up-fed: doctor
	@mkdir -p dev/logs
	@echo "starting consul..."
	@consul agent -config-file dev/consul-server.hcl > dev/logs/consul.log 2>&1 &
	@sleep 1
	@echo "starting nomad (requires sudo)..."
	@sudo nomad agent -config dev/nomad-server.hcl > dev/logs/nomad.log 2>&1 &
	@sleep 2
	@echo "starting api..."
	@cd api && NORN_REGISTRY_URL=ghcr.io/antiartificial go run . > ../dev/logs/api.log 2>&1 &
	@sleep 1
	@echo "all services started. logs in dev/logs/"

down:
	@echo "stopping services..."
	@-lsof -ti :8800 | xargs kill 2>/dev/null; true
	@-pkill -f "nomad agent" 2>/dev/null; true
	@-pkill -f "consul agent" 2>/dev/null; true
	@echo "all services stopped"

logs:
	@tail -f dev/logs/*.log

# --- Local dev (single-node, fast iteration) ---

consul-dev:
	consul agent -dev

nomad-dev:
	nomad agent -dev -bind 0.0.0.0 -config dev/nomad-dev.hcl

# --- Federated mode (for DO worker testing) ---

consul-server:
	consul agent -config-file dev/consul-server.hcl

nomad-server:
	sudo nomad agent -config dev/nomad-server.hcl
