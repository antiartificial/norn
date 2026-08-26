# DigitalOcean durability pilot

The durability pilot is a disposable, production-shaped Norn workload test. It
proves application-level recovery under repeatable faults without making the
HA lab a second production environment or placing provider credentials in Norn.

It runs on the existing [Linux HA lab](./ha-lab.md): Terraform/OpenTofu creates
only the isolated VPC, three Linux members, firewall, and load balancer;
Ansible converges Norn, Consul, Nomad, PostgreSQL, and private observability;
Norn deploys the pilot application; a separate Nomad job supplies test-only
Valkey and Redpanda dependencies.

## What it proves

```text
client request with Idempotency-Key
        │
        ▼
PostgreSQL job + transactional outbox ──► Redpanda topic
        │                                      │
        │                                      ▼
        └──────────── source of truth ◄── idempotent worker
                                               │
                                     Valkey read-through cache
                                     (never authoritative)
```

- A request is committed with an outbox entry in one PostgreSQL transaction.
- A publisher retries unpublished outbox rows after a process or queue outage.
- The worker records a message key and applies its effect in one PostgreSQL
  transaction before committing the consumer offset. Duplicate delivery is
  harmless and must leave `attempts == 1`.
- `/readyz` requires PostgreSQL, Valkey, and Redpanda. `/healthz` is liveness
  only. This ensures ingress does not route application traffic before its
  dependencies accept requests.
- Prometheus discovers passing pilot web allocations through Consul and records
  request, outbox, worker, cache-error, and dependency-error counters.

This is **not** a test of cache or broker high availability. The lab dependency
job runs one pinned Valkey and one pinned Redpanda broker, with data retained on
the selected disposable node while the lab exists. It is appropriate for
proving application durability, consumer recovery, and readiness behavior.
Production needs independently operated cache and a three-or-more-broker,
dedicated Redpanda deployment with its own replication, storage, upgrades, and
backup drills.

## Capacity and cost guard

Use three `s-4vcpu-8gb` or larger Linux members for this pilot. The existing
`s-2vcpu-4gb` HA-lab minimum is enough for the control-plane exercises but not
for a 2 GiB Redpanda test broker alongside PostgreSQL, Nomad, Consul, and
observability. Keep the required `owner` and UTC `expires_on` tags, use a
bounded SSH source CIDR, and review a saved plan before apply. The plan is
deliberately short-lived: retain only value-safe evidence, then run the guarded
destroy command.

DigitalOcean classifies Basic Droplets as suitable for development/test and
low-traffic microservices; this pilot is an acceptance experiment, not a
sizing recommendation for production. Check current regional prices immediately
before an apply. [DigitalOcean’s plan guidance](https://docs.digitalocean.com/products/droplets/concepts/choosing-a-plan/)
describes the available CPU/memory ratios.

## Prepare

Start from a clean, already-proven HA lab. No `DIGITALOCEAN_TOKEN`, SSH private
key, registry credential, database secret, or Tailscale key belongs in the
repository, Terraform variables, cloud-init, Nomad job, or Norn app spec.

```bash
cd v2/infra/ha-lab
cp terraform/terraform.tfvars.example terraform/terraform.tfvars
# Set a unique name_prefix, owner, short expires_on, exact SSH /32, and
# node_size = "s-4vcpu-8gb" (or a reviewed larger size).

scripts/lab bootstrap-tools
scripts/lab init
scripts/lab check
scripts/lab plan -var-file=terraform.tfvars
# Review the plan before the following explicit apply.
scripts/lab apply -var-file=terraform.tfvars
scripts/lab secrets init <lab-name>
scripts/lab converge
scripts/lab deploy-traefik
scripts/lab verify
scripts/lab durability-pilot check
```

Build the pilot image with SBOM/provenance enabled. Save the printed immutable
digest; do not substitute a tag. `catalog durability-sync` creates or recovers
the dedicated `durability-pilot` branch in the existing private HA catalog,
updates its infraspec to that exact digest, encrypts the PostgreSQL/Valkey/
Redpanda connection settings to the existing age recipient, and enables
replication of the generated app declaration to every Norn contender. The
application receives a separate PostgreSQL role and database on the same
disposable Patroni cluster; it does not share the Norn control database. It
reuses the lab's read-only repository deploy key; no personal GitHub token is
placed on a server.

```bash
image="$(scripts/build-durability-pilot v1)"
scripts/lab catalog sync
scripts/lab catalog durability-sync "${image}" https://<access-protected-pilot-host>
scripts/lab converge --tags catalog,norn

# Use the authenticated Norn control client for these durable mutations.
norn validate --file /path/to/generated/norn-durability-pilot-infraspec.yaml
norn preflight norn-durability-pilot durability-pilot

# The replicated declaration deliberately remains deploy:false. Enable it only
# after reviewing preflight, using the dashboard or the versioned control API.
curl --fail-with-body -X PUT \
  -H "Authorization: Bearer ${NORN_TOKEN}" \
  -H 'Content-Type: application/json' \
  -d '{"enabled":true}' \
  https://<norn-control-host>/api/v1/apps/norn-durability-pilot/deployment

norn deploy norn-durability-pilot durability-pilot
```

Retain the returned operation/saga ID and wait for its receipt and endpoint
smoke before continuing. The pilot remains disabled by default until this
controller-only catalog synchronization has produced an encrypted secret bundle
and an immutable image reference.

## Deploy and test dependencies

Obtain image digests from their publishers, record both digests in test
evidence, and pass only digest references to the script. The current example
uses the vendor's Redpanda test-container pattern and a version-pinned Valkey
image. Do not use `latest`.

```bash
scripts/lab durability-pilot dependencies deploy \
  docker.redpanda.com/redpandadata/redpanda@sha256:<64-hex-digest> \
  valkey/valkey@sha256:<64-hex-digest>

# After Norn has deployed the two web allocations and singleton worker through
# the regional Traefik route:
scripts/lab durability-pilot exercise https://<private-or-edge-pilot-host> 50
```

Redpanda documents the single-broker container mode as a development/test
quickstart and explicitly recommends at least three dedicated brokers for a
production cluster. [Redpanda single-broker Docker guide](https://docs.redpanda.com/labs/docker-compose/single-broker/)
and [production topology guidance](https://docs.redpanda.com/streaming/25.1/deploy/deployment-option/self-hosted/kubernetes/k-production-deployment/)
are the source for that boundary. Valkey supports version-pinned container tags
and persistence settings; use an immutable digest in this qualification lane.
[Valkey container guidance](https://valkey.io/topics/valkey-bundle/)

## Fault matrix

Run one fault at a time. The exercise command submits every key twice, then
requires a completed job with exactly one applied attempt. Record UTC start/end
time, affected allocation ID, HTTP success/error counts, latency percentiles,
and the dashboard counters—never request bodies or credentials.

| Fault | Command | Required result |
|---|---|---|
| Worker restart | `scripts/lab durability-pilot fault worker` | Consumer group resumes; no duplicated applied job. |
| Redpanda restart | `scripts/lab durability-pilot fault redpanda` | Readiness withdraws while unavailable; queued outbox entries publish after recovery. |
| Web allocation replacement | Norn restart/scale operation, followed by `exercise` | Idempotency and outbox state survive replacement. |
| Norn API failover | `scripts/lab norn-failover`, then inspect durable operation/receipt | Exactly one control API returns; no ambiguous deploy state. |
| Database failover | `scripts/lab failover`, then `exercise` | Endpoint readiness reflects database recovery; jobs complete after the write path recovers. |
| Member reboot | `scripts/lab reboot`, then `exercise` | Scheduler/discovery quorum reforms and app allocations become ready. |

The HTTP benchmark belongs outside the test cluster. Use a bounded client with
fixed concurrency, warm-up, fixed duration, and an explicit error budget; do
not load test the public tunnel or unrelated Mini services. Grafana should
correlate the client’s p50/p95/p99 and error rate with Norn, Nomad, cAdvisor,
Valkey, Redpanda, and application counters.

## Explicit cleanup

Stop the test-only dependencies only after saving bounded evidence. Their local
data remains on disposable nodes so a failed test can be inspected; complete
cluster destruction removes it with the droplets.

```bash
scripts/lab durability-pilot teardown --confirm-disposable-lab --purge-pilot
scripts/lab expiry check
# Review the destroy plan, retain evidence, then remove every billable lab item.
scripts/lab destroy --confirm-disposable-lab
scripts/lab backup-repository cleanup-destroyed --confirm-disposable-lab
```

Confirm the dedicated project is empty, the load balancer and VPC are gone, and
the remote state reports zero resources. Revoke any short-lived registry or
tailnet enrollment material used for the lab. Do not destroy a production-like
environment with these commands.
