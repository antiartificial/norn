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
| **DO Spaces** (state + WAL backup) | — | — | **fully automatic** — `lab up` mints a scoped key + versioned bucket via doctl (`backup-repository init`). No manual step. |
| **Tailscale auth key** | login.tailscale.com → Settings → Keys (the **antiartificial** tailnet) | reusable, tag `tag:norn-pilot-node` | prompted once, or **auto-minted** if `TS_API_KEY`+`TS_TAILNET` are exported. Skip entirely with `--no-tailscale` (VPC/SSH access). |
| **GitHub auth** | `gh auth login` | repo create + deploy-key | needed only for `lab deploy-app` (the demo workload) |

`doctl`, `gh`, `jq`, and `curl` must be installed locally; `lab bootstrap-tools` installs the
rest (terraform, ansible, cosign, age, sops) into `.tools/bin`.

## Auto-mint the Tailscale key (optional)

The Spaces key is always auto-provisioned from your doctl session. For Tailscale, if you'd
rather not create the auth key by hand, export a Tailscale API access token and `lab up` mints
a reusable, `tag:norn-pilot-node` key for you:

```bash
export TS_API_KEY="tskey-api-..."           # login.tailscale.com → Settings → Keys → API access token
export TS_TAILNET="antiartificial.github"   # your tailnet name
```

Without them you're prompted once and the value is saved for re-runs. Or skip Tailscale with
`lab up NAME --no-tailscale` and reach the cluster over the VPC / an SSH tunnel.

## What you are NOT asked for

Everything else is generated locally and never leaves your machine: gossip/ACL tokens and DB
passwords (`secrets`), the age key + git deploy key + encrypted app secret (`catalog sync`),
the cosign signing keypair (`supply-chain init`), and the internal PKI (`pki`).
