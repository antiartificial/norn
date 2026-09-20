# Observations & Gotchas

A running log of non-obvious things learned running this lab against live
DigitalOcean, so we don't rediscover them the hard way. Newest first.

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
