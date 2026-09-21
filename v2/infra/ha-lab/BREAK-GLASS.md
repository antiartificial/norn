# Break-Glass Access

Emergency/manual access paths for a running lab. All of these are for the
disposable HA lab; treat every credential below as sensitive. Replace `<lab>`
with the `name_prefix` from `terraform/terraform.tfvars` (e.g. `norn-trinity`).

## Where the secrets live

Two forms of the same material, both mode `0600`, under `~/.config/norn/ha-lab/`:

```
<lab>.env         # plaintext WORKING copy every script sources (print path: lab secrets path <lab>)
<lab>.sops.env    # SEALED, multi-recipient copy — the shared + backed-up artifact (SOPS/age)
<lab>.recipients  # age public keys allowed to open the sealed copy
```

Source the working copy to load everything:

```sh
set -a; source "$(scripts/lab secrets path <lab>)"; set +a
```

A new operator, or a machine that only has the sealed copy, materializes the
working copy from the sealed one — see §0.

Key entries:

| Variable | What it unlocks |
|---|---|
| `NORN_HA_NOMAD_MANAGEMENT_TOKEN` | Nomad root ACL token (full control plane) |
| `NORN_HA_CONSUL_MANAGEMENT_TOKEN` | Consul management token |
| `NORN_HA_POSTGRES_PASSWORD` | **co-located Patroni PostgreSQL superuser (`postgres`)** |
| `NORN_HA_NORN_DB_PASSWORD` | app DB role (`norn_test`) on the co-located PG |
| `NORN_HA_BACKUP_ACCESS_KEY` / `_SECRET_KEY` | DO Spaces key (state + WAL + staging feeds) |
| `NORN_HA_BACKUP_BUCKET` / `_REGION` / `_ENDPOINT` | Spaces bucket + endpoint |
| `NORN_HA_CONSUL_PATRONI_TOKEN` | Patroni DCS token |
| `NORN_HA_NOMAD_GOSSIP_KEY` | Nomad gossip encryption key |

Node IPs (SSH targets):

```sh
doctl compute droplet list --format Name,PublicIPv4 --no-header | grep <lab>
```

## 0. Operator identity, sealing & recovery (multi-operator)

Secrets live in three tiers; the **root-of-trust never lives in the cluster or
in Spaces alone**:

| Tier | What | Where |
|---|---|---|
| Root-of-trust | your personal age **private** key; the DO/Spaces bootstrap creds | your **password manager**, per operator |
| Operational | the lab secret (DB / ACL / API tokens, …) | working `<lab>.env` + sealed `<lab>.sops.env` |
| Sealed backup | ciphertext copy of the sealed file | DO Spaces (`secrets/<lab>.sops.env`, versioned) |

**Identify & guard your local secret**

```sh
scripts/lab secrets operator-init     # once per machine: creates ~/.config/norn/operator-age.key (0600)
scripts/lab secrets operator-id       # prints your age recipient (safe to share)
```

- `~/.config/norn/operator-age.key` is your break-glass identity — the **only**
  thing that opens a sealed secret. Copy it into your password manager. If a
  laptop is lost, an existing operator runs `secrets rm-operator <lab> <that
  recipient>` and rotates.
- The working `<lab>.env` is plaintext at `0600`; the sealed `<lab>.sops.env`
  and `<lab>.recipients` are safe to sync / back up (ciphertext / public keys).

**Onboard / offboard operators**

```sh
scripts/lab secrets add-operator <lab> age1...   # grant (colleague runs `secrets operator-id`) + re-seal
scripts/lab secrets operators <lab>              # list recipients
scripts/lab secrets rm-operator <lab> age1...    # revoke + re-seal (then rotate — see NOTE it prints)
```

**Read a value (break-glass; no persistent plaintext)**

```sh
scripts/lab secrets view <lab> NORN_HA_NOMAD_MANAGEMENT_TOKEN   # one key
scripts/lab secrets view <lab>                                  # everything
```

**Back up to / recover from Spaces**

```sh
scripts/lab secrets seal <lab>     # encrypt working -> sealed, to every recipient
scripts/lab secrets push <lab>     # upload sealed -> Spaces (a new version each push)

# Fresh machine / disaster recovery (no working copy yet):
export NORN_HA_BACKUP_ACCESS_KEY=... NORN_HA_BACKUP_SECRET_KEY=... \
       NORN_HA_BACKUP_REGION=... NORN_HA_BACKUP_BUCKET=...   # from your password manager
scripts/lab secrets pull <lab>     # download sealed from Spaces
scripts/lab secrets open <lab>     # decrypt -> working <lab>.env (needs your operator key)
```

**Raw-tools fallback** — if the `lab` / `secrets` wrapper is unavailable, decrypt
with age/sops directly (this is the true offline break-glass):

```sh
export SOPS_AGE_KEY_FILE=~/.config/norn/operator-age.key
sops --config /dev/null -d --input-type binary --output-type binary \
     ~/.config/norn/ha-lab/<lab>.sops.env      # prints the decrypted .env to stdout
```

## 1. App nodes (SSH)

Root SSH with the operator key registered in `ssh_key_fingerprints`:

```sh
ssh root@<node-public-ip>
```

The three members are `<lab>-01/02/03`. From a node you have local access to
Nomad, Consul, Patroni, Docker, and the PG endpoints below.

## 2. Pods / allocations (exec + logs)

The control API and the Nomad/Consul UIs are **Tailscale/VPC-only** (never
public). Reach them from your laptop with `lab connect`, which prints a node's
Tailscale endpoint or an SSH-tunnel command:

```sh
lab connect                                            # prints NORN_ADDR + token
# tunnel fallback if you're not on the tailnet:
ssh -N -L 8810:127.0.0.1:8810 root@<node-public-ip>    # then NORN_ADDR=http://127.0.0.1:8810
```

**Escalation ladder — prefer the managed path; drop to the node only if needed.**

**(a) Managed exec — over the control API, no node login:**

```sh
export NORN_ADDR=http://<tailscale-or-tunnel-host>:8810
export NORN_API_TOKEN="$NORN_HA_NORN_API_TOKEN"
norn exec <app> -- sh                    # shell into a running allocation
norn exec <app> -p <process> -- <cmd>    # target a specific process/group
```

The NornUI web app exposes the same thing as a per-app browser terminal
(xterm). Both ride the `apps:exec` scope — the break-glass `NORN_API_TOKEN` is a
full-scope legacy token, so it works without device enrollment.

**(b) Guarded exec sessions** (`norn.exec/v1`) — the audited path for enrolled
operators: a device-bound token plus a one-time `X-Norn-Step-Up` challenge gate
`POST /api/v1/apps/<app>/exec-sessions`, and every session is recorded in the
`exec_sessions` table (`norn production` / admin can list them). Use this when
you want the exec attributed and logged rather than the fast token above.

**(c) Node-level `nomad alloc exec`** (deepest fallback — direct data plane):

Nomad's API is HTTPS + ACL after converge. `norn.env` on the node is not a
reliable source (its `NORN_NOMAD_ADDR` stays `http://…` until cutover fully
finishes) — set the mTLS env explicitly:

```sh
# on a node, or locally with the token from the secret file
export NOMAD_ADDR=https://127.0.0.1:4646
export NOMAD_CACERT=/etc/nomad.d/pki/nomad-ca.pem
export NOMAD_CLIENT_CERT=/etc/nomad.d/pki/nomad.pem
export NOMAD_CLIENT_KEY=/etc/nomad.d/pki/nomad-key.pem
export NOMAD_TOKEN="$NORN_HA_NOMAD_MANAGEMENT_TOKEN"

nomad job status <job>
alloc=$(nomad job status -json <job> | jq -r '[.[0].Allocations[]?]|sort_by(.CreateTime)|last|.ID')
nomad alloc exec -task <task> "$alloc" sh     # shell into the container
nomad alloc logs "$alloc"; nomad alloc logs -stderr "$alloc"
```

Consul (service catalog, KV) from a node:

```sh
export CONSUL_HTTP_ADDR=https://127.0.0.1:8501
export CONSUL_CACERT=/etc/consul.d/pki/ca.pem
export CONSUL_CLIENT_CERT=/etc/consul.d/pki/consul.pem
export CONSUL_CLIENT_KEY=/etc/consul.d/pki/consul-key.pem
export CONSUL_HTTP_TOKEN="$NORN_HA_CONSUL_MANAGEMENT_TOKEN"
consul members; consul catalog services
```

The Nomad (`:4646`) and Consul (`:8501`) web UIs bind each node's **private** IP.
Browse to a node's Tailscale IP (or through the SSH tunnel above) to reach them —
but both need the mTLS client cert + token, so the CLI above is usually the
faster break-glass than the browser UI.

## 3. Co-located PostgreSQL (Patroni)

Topology: Postgres on `:5432` per node; **HAProxy on `:6432` routes to the
current leader**. Superuser is `postgres` / `NORN_HA_POSTGRES_PASSWORD`.

Break-glass superuser session (run on a node; avoid the password in argv):

```sh
printf '127.0.0.1:6432:*:postgres:%s\n' "$NORN_HA_POSTGRES_PASSWORD" > /tmp/pg.pgpass
chmod 600 /tmp/pg.pgpass
PGPASSFILE=/tmp/pg.pgpass psql -h 127.0.0.1 -p 6432 -U postgres -d postgres
shred -u /tmp/pg.pgpass   # clean up when done
```

Find the leader / cluster state:

```sh
patronictl -c /etc/patroni/patroni.yml list      # or: curl -s http://<node-priv-ip>:8008/cluster
```

Databases in use: `norn_test` (toy/demo app), `feedmap_inventory` (pipeline).
From inside a container the same PG is reached at `172.17.0.1:6432` (the Docker
bridge gateway), `sslmode=require`.

## 4. Application DB roles (least-privilege, not the superuser)

Apps never use the superuser. Their role DSNs live in Nomad variables and are
the break-glass path for app-level DB access:

```sh
nomad var get -out=json nomad/jobs/<app>            # e.g. feedmap-trinity, feedmap-pipeline-initdb
```

- Trinity: managed MySQL creds under `nomad/jobs/feedmap-trinity` (`DB_*`).
- Pipeline: `feedmap_inventory_{owner,writer,reader}` DSNs under the
  `nomad/jobs/feedmap-pipeline-*` vars (owner=migrations, writer=ingest,
  reader=API). Rotate by `ALTER ROLE … PASSWORD` as superuser (§3) and
  rewriting the var.

## 5. DO Spaces (state, WAL, staging feeds)

```sh
AWS_ACCESS_KEY_ID="$NORN_HA_BACKUP_ACCESS_KEY" \
AWS_SECRET_ACCESS_KEY="$NORN_HA_BACKUP_SECRET_KEY" \
.tools/ansible/bin/python -c "import boto3,os; c=boto3.client('s3',endpoint_url='https://'+os.environ['SP'],region_name=os.environ['R']); print([o['Key'] for o in c.list_objects_v2(Bucket=os.environ['B']).get('Contents',[])][:20])"
# SP=<region>.digitaloceanspaces.com R=<region> B=$NORN_HA_BACKUP_BUCKET
```

(The lab's Spaces key is scoped to the state/backup bucket.)

## Notes

- These credentials are regenerated per `lab up`; a torn-down lab's secrets are
  useless. The secret file is the single source of truth while a lab is live.
- Prefer `PGPASSFILE`/`.pgpass` and env-var assignment over passwords on the
  command line (process list + shell history exposure).
