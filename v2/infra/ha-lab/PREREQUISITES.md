# Prerequisites — what you need before `lab up`

Run `scripts/lab preflight` (or `lab preflight <name>`) any time; it checks every item below
and tells you exactly what's missing. This page is the "you need **X** from **Y** with **Z**
permissions" reference behind those checks.

## Credentials

| Credential | Where to get it (Y) | Permissions (Z) | How it's supplied |
|---|---|---|---|
| **DO API token** | cloud.digitalocean.com → API → Tokens | **read + write** | `doctl auth init` (the lab reuses your doctl context) — **required** |
| **DO Container Registry** | cloud.digitalocean.com → Container Registry | must exist | `doctl registry create <name>` once; docker-config is **auto-generated** by `lab artifacts ensure` — **required** |
| **SSH key in DO** | cloud.digitalocean.com → Settings → Security | — | `doctl compute ssh-key import`; `lab up` **auto-detects** the fingerprint — **required** |
| **DO Spaces key** | cloud.digitalocean.com → API → Spaces Keys | **read + write** | prompted once, or **auto-minted** if `DIGITALOCEAN_TOKEN` is exported (see Auto-mint) |
| **Tailscale auth key** | login.tailscale.com → Settings → Keys (the **antiartificial** tailnet) | reusable, tag `tag:norn-pilot-node` | prompted once, or **auto-minted** if `TS_API_KEY`+`TS_TAILNET` are exported |
| **GitHub auth** | `gh auth login` | repo create + deploy-key | needed only for `lab deploy-app` (the demo workload) |

`doctl`, `gh`, `jq`, and `curl` must be installed locally; `lab bootstrap-tools` installs the
rest (terraform, ansible, cosign, age, sops) into `.tools/bin`.

## Auto-mint (optional — "hand me the master key and I'll mint the scoped ones")

If you'd rather not create the Spaces key and Tailscale key by hand, export a **parent**
credential and `lab up` mints scoped children for you:

```bash
# DO Spaces key is minted from your DO API token (already exported by the lab from doctl)
export DIGITALOCEAN_TOKEN="$(doctl auth token)"

# Tailscale auth key is minted from a Tailscale API access token
export TS_API_KEY="tskey-api-..."      # login.tailscale.com → Settings → Keys → API access token
export TS_TAILNET="antiartificial.github"   # your tailnet name
```

With those present, `lab artifacts ensure-external <name>` mints a reusable,
`tag:norn-pilot-node` Tailscale key and a scoped DO Spaces key and writes them into the
lab's `.env`. Without them, you're prompted once and the values are saved for re-runs.

## What you are NOT asked for

Everything else is generated locally and never leaves your machine: gossip/ACL tokens and DB
passwords (`secrets`), the age key + git deploy key + encrypted app secret (`catalog sync`),
the cosign signing keypair (`supply-chain init`), and the internal PKI (`pki`).
