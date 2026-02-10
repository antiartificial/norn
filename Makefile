SHELL := /bin/bash

.PHONY: dev api ui infra infra-stop build

# Run API + UI concurrently for local dev
dev:
	@$(MAKE) -j2 api ui

api:
	cd api && go run .

ui:
	cd ui && pnpm dev

# Shared infra (Valkey + Redpanda)
infra:
	cd infra && docker compose up -d

infra-stop:
	cd infra && docker compose down

# Production build
build:
	cd ui && pnpm build
	cd api && CGO_ENABLED=0 go build -o ../bin/norn .
