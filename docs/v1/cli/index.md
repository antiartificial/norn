# CLI

The Norn CLI is a [Charm](https://charm.sh)-powered terminal client for managing your infrastructure. It uses Bubble Tea for interactive TUI components, Lip Gloss for styling, and Cobra for command parsing.

## Installation

### Build from source

```bash
cd norn
make cli            # builds to bin/norn
make install        # builds + symlinks to /usr/local/bin/norn
```

### Verify

```bash
norn version
```

## Configuration

The CLI connects to the Norn API server. Configure the URL with:

```bash
# Environment variable (recommended)
export NORN_URL=http://localhost:8800

# Or per-command flag
norn --api http://localhost:8800 status
```

Default: `http://localhost:8800`

## Global flags

| Flag | Description |
|------|-------------|
| `--api <url>` | Norn API URL (overrides `NORN_URL`) |

## Commands

| Command | Description |
|---------|-------------|
| [`norn status`](/v1/cli/commands#status) | List all apps with health, commit, hosts, services |
| [`norn deploy`](/v1/cli/commands#deploy) | Deploy a commit with live pipeline progress |
| [`norn restart`](/v1/cli/commands#restart) | Rolling restart with spinner |
| [`norn rollback`](/v1/cli/commands#rollback) | Rollback to previous deployment |
| [`norn logs`](/v1/cli/commands#logs) | Stream pod logs (fullscreen, scrollable) |
| [`norn secrets`](/v1/cli/commands#secrets) | List secret names |
| [`norn health`](/v1/cli/commands#health) | Check all backing services |
| [`norn version`](/v1/cli/commands#version) | Version and API endpoint info |
| [`norn forge`](/v1/cli/commands#forge) | Provision infrastructure for an app |
| [`norn teardown`](/v1/cli/commands#teardown) | Remove infrastructure for an app |
| [`norn cluster`](/v1/cli/cluster) | Manage k3s cluster nodes |

## Charm libraries

The CLI uses the Charm ecosystem:

- **[Bubble Tea](https://github.com/charmbracelet/bubbletea)** — TUI framework (Model-Update-View architecture)
- **[Lip Gloss](https://github.com/charmbracelet/lipgloss)** — Terminal styling (colors, borders, layout)
- **[Bubbles](https://github.com/charmbracelet/bubbles)** — Reusable TUI components (spinners, tables, viewports)
- **[Cobra](https://github.com/spf13/cobra)** — Command-line argument parsing

## Color palette

The CLI uses a consistent purple-themed color palette:

| Name | Hex | Usage |
|------|-----|-------|
| Primary | `#7C3AED` | Titles, banners, badges |
| Green | `#10B981` | Healthy, success, done |
| Red | `#EF4444` | Unhealthy, errors, failed |
| Yellow | `#F59E0B` | Warnings, running state |
| Cyan | `#06B6D4` | Role badges |
| Dim | `#6B7280` | Secondary text |
| White | `#F9FAFB` | Primary text |
