SHELL := /bin/bash
.DEFAULT_GOAL := help

RUNTIME_TARGETS := build api cli host-agent ingress-observer ingress-publisher \
	ui doctor dev up up-fed down logs clean consul-dev nomad-dev consul-server nomad-server

.PHONY: help $(RUNTIME_TARGETS) test docs docs-build

help:
	@printf '%s\n' \
		'Norn V3: active implementation in v2/' \
		'  make build       Build API, CLI, and host/ingress tools' \
		'  make ui          Build dashboard assets' \
		'  make doctor      Check local runtime prerequisites' \
		'  make up          Start the local development runtime' \
		'  make dev         Run the development API in the foreground' \
		'  make test        Run API, CLI, and dashboard tests' \
		'  make docs-build  Build the versioned documentation' \
		'Historical Kubernetes source remains in the v3.0.0 tag.'

$(RUNTIME_TARGETS):
	+$(MAKE) -C v2 $@

test:
	cd v2/api && go test ./...
	cd v2/cli && go test ./...
	pnpm --dir v2/ui test

docs:
	pnpm --dir docs dev

docs-build:
	pnpm --dir docs build
