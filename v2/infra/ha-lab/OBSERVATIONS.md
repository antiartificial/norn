# Observations & Gotchas

A running log of non-obvious things learned running this lab against live
DigitalOcean, so we don't rediscover them the hard way. Newest first.

## Enhancement ideas (backlog)

- **Nicer `lab up` progress UX.** Today `lab up` streams raw Ansible task output.
  An engineer running it fresh would benefit from a zypper-style summary: a
  numbered list of the pipeline steps (external → state → apply → pki → cosign →
  catalog → registry → converge → cutover → traefik → app), the current step
  highlighted, elapsed time per step, and a clear final summary. The step list
  already exists in `scripts/up` (STEPS), so this is mostly a presentation layer.
- **`lab up --dry-run`.** Print the plan without touching anything: the resolved
  tfvars, a `terraform plan` summary, the ordered step list, and estimated cost
  (the `fleet validate` cost line), so an engineer can preview a launch. Pairs
  well with the progress UX above.

## `lab up` cutover fails on Nomad 1.9.7: `flag provided but not defined: -accessor`

**What bit us:** `lab up` reached `cutover`, the Ansible converge succeeded (PLAY
RECAP: 0 failed), then the `security-cutover` script died with
`flag provided but not defined: -accessor`.

**Why:** `scripts/security-cutover` seeds two Nomad **client** tokens with
pre-specified IDs via `nomad acl token create -accessor <id> -policy ... -`
(so Norn's controller + Prometheus authenticate with known tokens). The
`-accessor` import flag only exists in **Nomad ≥ 1.10**; the nodes install
**Nomad 1.9.7**, whose `acl token create` has no `-accessor`. Version skew
between the tooling and the pinned node binary. (The Nomad ACL *bootstrap* of
the management token earlier in the same script works fine on 1.9.7 — only the
two client-token imports fail.)

**Impact is narrow.** After converge the cluster is fully functional: Nomad ACL
is bootstrapped, all nodes ready, the management token works over the mTLS API
(`https://127.0.0.1:4646` with `/etc/nomad.d/pki/nomad{-ca,,-key}.pem`). Only the
two pre-seeded client tokens are missing, which degrades Norn-controller / the
Prometheus→Nomad scrape — not the data plane.

**Workaround used:** `scripts/deploy-trinity` talks to Nomad directly with the
mTLS env + the management token (from the lab secret file), so it does not depend
on cutover completing. Note `NORN_NOMAD_ADDR` in `/etc/norn/norn.env` stays the
stale `http://127.0.0.1:4646` until cutover finishes — use the HTTPS endpoint +
certs explicitly.

**Proper fixes (either):** bump the node Nomad to ≥ 1.10 (then `-accessor` works
as written), or rework `create_nomad_token` to let Nomad generate the IDs and
write the results back to the token consumers (norn.env + Prometheus config).

## DigitalOcean managed databases: engine + version drift

**What bit us:** `terraform apply` failed provisioning the managed cache and
MySQL with two 422s:

- `digitalocean_database_cluster.mysql` → `422 invalid cluster engine version`
  (we requested MySQL `"8"`).
- `digitalocean_database_cluster.redis` → `422 region 'nyc3' is not valid`
  (misleading — the real cause was the engine, not the region).

**Why:**

1. **DO replaced managed Redis with Valkey.** `redis` is no longer a valid
   engine slug; the API rejects it, and the error surfaces as a *region*
   validation failure rather than an engine one. Use `engine = "valkey"`
   (Valkey is Redis-protocol compatible, so app clients keep using `REDIS_*`
   config and the `tls://` host prefix unchanged).
2. **Managed engine versions are a moving target.** DO only offers specific
   versions and drops old ones. At time of writing: **MySQL `8.4`** (not `8`),
   **Valkey `8`**, **PostgreSQL `16`/`17`**. A plain major like `"8"` for MySQL
   is rejected.

**Rule of thumb — verify before you apply.** The catalog is authoritative and
changes over time; never hardcode a version on faith. Check with:

```sh
doctl databases options engines                       # valid engine slugs
doctl databases options versions --engine mysql       # e.g. [8.4]
doctl databases options versions --engine valkey      # e.g. [8]
doctl databases options slugs    --engine mysql       # size slugs + node counts
```

The node-count column on `slugs` matters: `db-s-1vcpu-1gb` is single-node only
(`[1]`); multi-node needs `db-s-1vcpu-2gb` or larger (`[1,2,3]`).

**Blast-radius note.** A failed `apply` still creates everything that
*did* validate (VPC, droplets, load balancer, firewall). Fix the offending
resource and resume — `lab up NAME --from apply` is idempotent and keeps the
already-created infrastructure.

**Applies to Norn / norn-fleet too:** any place that lets a user pick a managed
DB engine/size/version (e.g. the Fleet Builder catalog) should validate against
`doctl databases options` rather than a static list, and treat `redis` as an
alias that resolves to `valkey`.
