# Makefile

Norn includes a Makefile that provides convenient targets for common development and operations tasks. All commands should be run from the project root directory.

## Setup

These targets handle initial setup and dependency installation.

| Target | Description |
|--------|-------------|
| `make setup` | One-time setup: check tools, create DB, install deps |
| `make prereqs` | Check that required tools are installed |
| `make db` | Create the norn database (idempotent) |
| `make deps` | Install all dependencies (UI + API + CLI Go modules) |

**Usage:**

After cloning the Norn repository, run:
```bash
make setup
```

This ensures all prerequisites are installed, creates the database, and installs all dependencies needed for development.

## Development

These targets start services for local development.

| Target | Description |
|--------|-------------|
| `make dev` | Start API (:8800) + UI (:5173) for local development |
| `make api` | Start just the API server |
| `make ui` | Start just the UI dev server |

**Usage:**

For full-stack development:
```bash
make dev
```

This starts both the API server on port 8800 and the Vite development server for the UI on port 5173.

To work on just the API or UI:
```bash
make api   # API only
make ui    # UI only
```

## Infrastructure

These targets manage shared infrastructure services.

| Target | Description |
|--------|-------------|
| `make infra` | Start Valkey (:6379) + Redpanda (:19092) via Docker Compose |
| `make infra-stop` | Stop shared infrastructure |

**Usage:**

Start supporting services:
```bash
make infra
```

This launches:
- **Valkey** (Redis) on port 6379 for caching and session storage
- **Redpanda** (Kafka) on port 19092 for event streaming

Stop them when done:
```bash
make infra-stop
```

## Build

These targets create production-ready builds.

| Target | Description |
|--------|-------------|
| `make build` | Production build: API server + CLI + UI static |
| `make cli` | Build just the CLI to bin/norn |
| `make install` | Build CLI + symlink to /usr/local/bin/norn |
| `make docker` | Build Docker image (norn:latest) |

**Usage:**

Build everything for production:
```bash
make build
```

This compiles the API server binary, builds the CLI, and creates a static UI bundle in `ui/dist/`.

For CLI development:
```bash
make cli      # Build to bin/norn
make install  # Build and install to PATH
```

After `make install`, you can use `norn` from anywhere on your system.

To build a Docker image:
```bash
make docker
```

This creates the `norn:latest` image for containerized deployments.

## Testing

Run the test suite.

| Target | Description |
|--------|-------------|
| `make test` | Run all tests (Go + TypeScript) |

**Usage:**

```bash
make test
```

This executes both Go tests for the API and CLI, and TypeScript tests for the UI.

## Maintenance

These targets help maintain and debug the project.

| Target | Description |
|--------|-------------|
| `make clean` | Remove build artifacts (bin/ and ui/dist/) |
| `make doctor` | Check health of all services |

**Usage:**

Clean up build artifacts:
```bash
make clean
```

This removes the `bin/` directory (compiled binaries) and `ui/dist/` (static UI bundle). Use this when build outputs are stale or corrupted.

Check system health:
```bash
make doctor
```

This runs diagnostics on all Norn services and dependencies, reporting which components are working and which need attention.
