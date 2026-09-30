# Paired disposable v3 Fleet launch plan — 2026-09-29

The shortest intended path to actual Norn v3 HA evidence is the existing paired
`disposable/management/nyc3` and `disposable/fleet/nyc3` topology. Do not create
a new M3 Terraform profile and do not reuse the external-Mac topology. The
paired infrastructure, retirement and normal five-node bootstrap exist.
PR #196 corrected paired Linux runner admission. PR #197's cleanup proof-reader
fix passed all 1,430 contract tests and merged as `172c2c3`. That exact protected
head passed 286 qualification tests; its source archive is built and verified.

This document is preparation, not spend approval. A new run needs a new
bounded window and explicit approval of the larger paired topology after its
resource envelope, current quote and initial backend plan are reviewable.

## Exact topology

The management root creates one project and VPC, two `s-2vcpu-4gb` Droplets
(authority and protected runner), and a two-node `db-s-2vcpu-4gb` PostgreSQL 16
cluster. The Fleet root creates a separate project and VPC, three
`s-2vcpu-4gb` control Droplets, two `s-1vcpu-2gb` ingress Droplets, one regional
load balancer, a two-node `db-s-2vcpu-4gb` PostgreSQL 16 runtime cluster and a
two-node `db-s-2vcpu-4gb` MySQL 8 cluster. Firewalls, databases, tags, project
attachments and run-scoped state accompany those billable resources.

The normal bootstrap installs Consul, Nomad, Norn and etcd on the three
control nodes. Norn uses `NORN_CONTROL_BACKEND=etcd`; it does not use the
managed runtime PostgreSQL cluster as its control store. The two ingress nodes
are Nomad clients. This is the real v3 topology that the external-Mac pilot did
not provide.

## Current read-only price basis

The DigitalOcean API was read on 2026-09-29 with the retained service-scoped
token. NYC3 reported `s-2vcpu-4gb` at $24/month or $0.03571/hour and
`s-1vcpu-2gb` at $12/month or $0.01786/hour. DigitalOcean's current managed
database price table reports `db-s-2vcpu-4gb` at $60.90/month or
$0.09063/hour per node. The regional load-balancer price is $12/month; the
four-hour projection below uses $0.01786/hour.

| Root | Monthly configuration quote | Four-hour usage projection |
| --- | ---: | ---: |
| Management | $169.80 | $1.01 |
| Fleet | $351.60 | $2.09 |
| Paired total | $521.40 | $3.10 |

The bounded request should reserve **$10.00 total** for four hours from first
billable create through verified deletion. The compute/database/load-balancer
projection is $3.10; the remaining $6.90 covers a two-hour teardown overrun,
network overage and a possible $5 Spaces account subscription minimum. The
current API credentials do not expose whether the account has already incurred
that Spaces base charge, so it must remain an explicit uncertainty rather than
being called included or free. This is not a promise of billing granularity.
The Droplet catalog was refreshed at `2026-09-29T23:59:50Z` with unchanged prices
and NYC3 availability. Bind fresh per-root quote values into the run
tfvars and the protected plans before create. The previous external-Mac $128.15 monthly quote
does not authorize this topology.

Sources: the authenticated DigitalOcean `/v2/sizes` and `/v2/databases/options`
catalogs, plus the public
[managed database pricing](https://www.digitalocean.com/pricing/managed-databases)
and [load balancer pricing](https://www.digitalocean.com/pricing/load-balancers)
pages. Provider plan output remains the final resource-count check.

## Version and authority prerequisites

1. `pilot260929g` partial-precreate final-zero and policy restoration are
   complete; retain both evidence chains.
2. Use qualified protected `172c2c3` and its verified checkout archive.
   PR #196 separates the paired Linux/X64
   `norn-pilot-<run-id>-runner` from external-Mac macOS/ARM64 `...-mac`.
3. Pin Norn to the signed v3 integration candidate
   `8ac21f8e24bc3add8b1f4d3a1ae34fd5d2cd9b21`, or a newer separately signed and
   qualified v3 candidate. The `pilot260929g` runtime pin `ce047020` predates
   the v3 integration and is not eligible for this run.
4. Validate the exact Linux Norn archive, manifest, signature and digest. Also
   validate the protected-root pins and reported versions for `etcd`,
   `etcdctl`, `etcdutl`, Consul, Nomad and Traefik. The three etcd binaries must
   report the same version.
5. Choose a fresh run ID, hostname, two distinct VPC CIDRs, separate state keys,
   create expiry, Fleet retire-by and later management retire-by. Create fresh
   run-scoped credentials, PKI, TLS, owner inputs, keys and receipts. No signed
   `pilot260929g` artifact is reusable.
6. Prove empty provider/run inventory and configure fleet-first retirement plus
   the management-last finalizer before create. The paired lane cannot produce
   both exact plans before every external mutation: both plans need their new
   remote Spaces backends, and the protected Fleet plan also needs the
   run-bound self-hosted runner created by the management root.

## Mandatory staged planning sequence

Local preparation can bind a candidate commit, owner, fresh run ID, two
non-overlapping `/24` VPC ranges, exact bootstrap IP, SSH fingerprints, public
hostname, authority Tailscale hostname, signed Norn v3 artifact, tool pins,
per-root live quotes, expiration/retirement times, provider scopes, tfvars and
the cleanup/finalizer inputs. Those values are mandatory; do not copy approval
IDs, timestamps, state credentials or signed evidence from another epoch.

The workflow admission fix is merged and qualified. The first externally
mutating step is the paired backend bootstrap. It requires
a short-lived signed backend approval bound to the exact clean reviewed commit,
cost-approval digest, account/team/Spaces owner, run ID, retire-by and two
canonical declarations. It creates exactly two versioned buckets and two
bucket-scoped read/write keys, then revokes its temporary broad key. Until that
step succeeds, neither exact OpenTofu plan can initialize its remote backend.

After backend bootstrap, prepare and review the saved **management** plan on
the owner Mac. `scripts/disposable_create.py prepare` proves an empty state key,
fresh provider absence and the exact expected management resource set. A
separate short-lived create approval binds that saved plan before apply.
Management create and bootstrap are then required to establish the authority
and exact `norn-pilot-<run-id>-runner` Linux/X64 protected runner. Only after
that runner is registered and authenticated can `.github/workflows/plan.yml`
produce the protected **Fleet** plan; the workflow has no management target and
routes disposable Fleet planning to that run-bound runner after the admission
correction above.

Use three review points because both plans cannot exist before spend:

1. Approve backend bootstrap only, with zero compute, database or load-balancer
   creation. Review its canonical two-bucket/two-key plan and bounded cleanup
   path first. This step may activate the account's uncertain $5 Spaces minimum.
2. Review the exact saved management plan, then approve management create within
   the same four-hour retirement window. Its current resource projection is
   about $1.01 for four hours, plus the possible Spaces minimum already called
   out above.
3. Review the exact protected Fleet plan generated on the new runner, then
   approve Fleet create subject to the original **$10 total paired-run ceiling**,
   including all costs already incurred by stages 1 and 2. If Fleet approval is
   withheld or its plan is wrong, retire management and clean both backends.

These are three evidence-review stages, not a requirement to ask the user
three times. A single explicit authorization may cover this exact topology,
$10 total ceiling, four-hour lifetime and staged execution/retirement. Within
that authorization, validate each generated plan and produce its bounded
approval artifact. Ask again only if the plan exceeds the authorized resource
set, budget, time window or other material scope.

The local neutral input manifest is
`/Users/arti/.config/norn-fleet/v3-launch-prep-20260929/paired-plan/manifest.json`.
Its adjacent `paired-plan-proposal.json` selects `pilot260929h`, owner
`aaronbarton`, the verified account, canonical bucket/state names, hostname and
non-overlapping VPC CIDRs. It leaves absolute windows and run authority unset
until authorization and records the dependency that blocks each exact plan.

## Locally verified public binary material

Neutral preparation is retained at
`/Users/arti/.config/norn-fleet/v3-launch-prep-20260929`. The signed Linux amd64
Norn `8ac21f8e` bundle passed `platform-release-artifact verify` against the
release public key; `receipt.json` records the exact archive, manifest,
signature and SBOM digests. The older `ce047020` companion runtime must not
supply the Norn executable.

The official etcd v3.5.17 Linux amd64 archive matched its upstream
`SHA256SUMS`; `etcd-receipt.json` records URLs and individual executable pins.
All three executables were run locally in an isolated Linux amd64 container
with networking disabled and a read-only filesystem. `etcd`, `etcdctl` and
`etcdutl` each reported 3.5.17; `etcd-runtime-version-check.json` retains that
observation. These public inputs carry no run authority or spend approval.

## Four-hour execution window

Reserve the final hour for retirement. Stop expansion immediately if bootstrap
or readiness consumes the first two hours; preserve the retirement reserve.

1. Create and bootstrap management. Prove its PostgreSQL, authority, protected
   runner, private/Tailscale access and recovery inputs before Fleet create.
2. Apply the exact Fleet plan. Reconcile five nodes, load balancer, two managed
   database clusters, DNS, provider inventory and state before bootstrap.
3. Run the normal bootstrap and retain exact binary, PKI, host, ACL and service
   receipts. Require all three etcd members healthy, the exact member list,
   empty alarm list, accepted bootstrap marker, initial managed credential
   publication, bootstrap-role removal and runtime-role access.
4. Prove the authenticated Norn v3 API reads/capabilities and a separately
   labeled etcd read/write/CAS marker. Do not count direct etcd writes as Norn
   application-write evidence.
   Verify the Consul-locked active API identity and absence of PostgreSQL
   control-store configuration.
5. Stop one control member. Prove two-member etcd quorum, continuing Norn
   API reads and no alarms; restart the member and prove healthy rejoin.
6. Deploy the digest-pinned hello workload to both ingress nodes, migrate the
   managed MySQL database once, and prove load-balanced HTTPS write/read plus
   one-ingress continuity.
7. If time remains, create and verify an etcd snapshot and its digest. Do not
   perform destructive restore without its separate reviewed recovery plan.
8. Remove the workload, retire Fleet first, prove Fleet provider/state/DNS/
   Tailscale zero, then retire management and prove paired final zero. Retain a
   billing review after provider absence.

The exact command recipe is retained in the neutral prep directory as
`paired-plan/fitness-execution-recipe.md`. The released `ce047020` companion
fixture supplies `/version`, `/records/*`, `/readyz` and `/healthz` checks;
it supplies no Norn executable. The v3 Fleet-target API is revision-fenced but
has no standalone harmless create/delete cycle. This run therefore reports
Norn API continuity and direct etcd CAS separately; it does not fabricate a
Norn application CRUD or normal-release acceptance result.

## Milestone interpretation

Successful bootstrap, one-member loss/rejoin and Norn API continuity provide
meaningful M3 multi-host evidence. The workload and ingress check provide a
bounded M4 slice. Protected qualification and exact version receipts advance
M8. Complete retirement advances operational assurance but is not itself a v3
release sign-off.

The run does not complete M3 rotation, rolling upgrade, destructive restore or
soak requirements. Fixed two-node ingress cannot prove M4 `2→3→2`. It does not
upgrade the Mini, move a real application, complete the version matrix, or sign
M5–M9. Record only the checks actually observed.
