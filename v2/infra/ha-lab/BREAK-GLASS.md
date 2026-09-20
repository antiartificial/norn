# Break-Glass Access

Emergency/manual access paths for a running lab. All of these are for the
disposable HA lab; treat every credential below as sensitive. Replace `<lab>`
with the `name_prefix` from `terraform/terraform.tfvars` (e.g. `norn-trinity`).

## Where the secrets live

Every lab secret is in a single per-operator file, mode `0600`:

```
~/.config/norn/ha-lab/<lab>.env          # print the path: lab secrets path <lab>
```

Source it to load everything:

```sh
set -a; source "$(scripts/lab secrets path <lab>)"; set +a
```

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

## 1. App nodes (SSH)

Root SSH with the operator key registered in `ssh_key_fingerprints`:

```sh
ssh root@<node-public-ip>
```

The three members are `<lab>-01/02/03`. From a node you have local access to
Nomad, Consul, Patroni, Docker, and the PG endpoints below.

## 2. Pods / allocations (exec + logs)

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
