# Quickstart — launch a Norn fleet in one command

Stand up a disposable, three-node Norn HA cluster on DigitalOcean, join it to
Tailscale, deploy a demo workload through Norn, and get your NornUI/CLI
connection details — from one command.

## 1. Get your credentials ready

Run the preflight; it names anything missing and where to get it:

```bash
cd v2/infra/ha-lab
scripts/lab preflight
```

You need a **DO API token** (`doctl auth init`), a **DO container registry**, and an
**SSH key in DO**. Spaces (state + WAL backup) is provisioned automatically. For cluster
access you also want a **Tailscale auth key** (antiartificial tailnet, `tag:norn-pilot-node`) —
prompted once, auto-minted, or skipped with `--no-tailscale`. See [PREREQUISITES.md](PREREQUISITES.md).

Optional — auto-mint the Tailscale key instead of pasting it:

```bash
export TS_API_KEY="tskey-api-…"           # Tailscale API access token
export TS_TAILNET="antiartificial.github" # your tailnet
```

## 2. Launch

```bash
scripts/lab up my-fleet
```

`up` describes the fleet (scaffolds `terraform.tfvars`, auto-filling your SSH key
and current IP), generates every key, provisions, joins Tailscale, converges the
platform, deploys the demo app **through Norn**, and prints how to connect. It is
idempotent — re-run to resume, or `scripts/lab up my-fleet --from converge` to
restart at a step. Useful flags: `--no-app`, `--no-tailscale`, `--yes`.

## 3. Connect NornUI / the CLI

```bash
scripts/lab connect
```

Prints the NornUI host (`http://<tailscale-ip>:8810`) and copies the API token to
your clipboard; also emits the `NORN_ADDR`/`NORN_API_TOKEN` env for the `norn` CLI.

## 4. Tear down (stops billing)

```bash
scripts/lab down my-fleet
```

Retype the fleet name to confirm; destroys droplets/LB/VPC/firewall and removes
the WAL backup bucket. Verify with `doctl compute droplet list`.

## Describe the fleet

`terraform/terraform.tfvars` is the fleet description — edit and re-run `lab up`:

| field | meaning |
|---|---|
| `name_prefix` | fleet/cluster name |
| `region` | DO region (e.g. `nyc3`) |
| `node_count` | odd number of co-located members (≥3) |
| `node_size` | droplet size (`s-2vcpu-4gb` dev; bump for production) |
| `enable_backups` | DO whole-droplet backups |
| `ssh_key_fingerprints` / `ssh_source_cidrs` | who can SSH, from where |

For the full manual step-by-step path (individual `lab` subcommands, drills,
production activation), see [README.md](README.md).
