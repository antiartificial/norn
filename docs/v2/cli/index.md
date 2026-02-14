# CLI

The Norn CLI is a Charm-powered terminal client built with Bubble Tea, Lip Gloss, and Cobra. It provides rich TUI rendering for deploy progress, log streaming, and app status.

## Installation

```bash
cd norn/v2
make build     # builds bin/norn
make install   # copies to ~/go/bin/norn
```

## Configuration

The CLI needs to know where the Norn API is running:

| Variable | Default | Description |
|----------|---------|-------------|
| `NORN_URL` | `http://localhost:8800` | API base URL |

Or use the `--api` flag on any command:

```bash
norn --api=https://norn.example.com status
```

## Quick Reference

| Command | Description |
|---------|-------------|
| [`norn status`](/v2/cli/commands#status) | List all apps with health and status |
| [`norn app`](/v2/cli/commands#app) | Detailed view of a single app |
| [`norn deploy`](/v2/cli/commands#deploy) | Deploy with live pipeline progress |
| [`norn restart`](/v2/cli/commands#restart) | Rolling restart |
| [`norn rollback`](/v2/cli/commands#rollback) | Rollback to previous deployment |
| [`norn scale`](/v2/cli/commands#scale) | Scale a task group |
| [`norn logs`](/v2/cli/commands#logs) | Stream live logs |
| [`norn health`](/v2/cli/commands#health) | Check backing service health |
| [`norn stats`](/v2/cli/commands#stats) | Deployment and cluster statistics |
| [`norn secrets`](/v2/cli/commands#secrets) | Manage app secrets |
| [`norn snapshots`](/v2/cli/commands#snapshots) | Database snapshot management |
| [`norn cron`](/v2/cli/commands#cron) | Cron job management |
| [`norn invoke`](/v2/cli/commands#invoke) | Invoke a function |
| [`norn saga`](/v2/cli/commands#saga) | View saga event log |
| [`norn validate`](/v2/cli/commands#validate) | Validate infraspec files |
| [`norn forge`](/v2/cli/commands#forge) | Set up cloudflared routing |
| [`norn teardown`](/v2/cli/commands#teardown) | Remove cloudflared routing |
| [`norn version`](/v2/cli/commands#version) | Version and API endpoint info |

## Version

```bash
$ norn version
norn v2 abc1234
api: http://localhost:8800
```
