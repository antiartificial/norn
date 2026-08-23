# Norn DigitalOcean HA lab

This directory provisions a disposable, three-member Linux acceptance lab for
Norn's Consul, Nomad, and PostgreSQL production-readiness work. It is isolated
from the older multi-cloud k3s Terraform root and therefore has its own state,
VPC, firewall, expiry tag, generated inventory, and guarded destroy command.

The lab deliberately co-locates Consul server, Nomad server/client, Patroni,
PostgreSQL, HAProxy, Docker, and node-exporter on each 2 vCPU/4 GB droplet. That
is an economical fault-injection topology, not the recommended production
failure-domain layout.

## Ownership boundaries

- Terraform owns only DigitalOcean resources. It reads the API token from
  `DIGITALOCEAN_TOKEN`; no provider credential is accepted as an input variable.
- Cloud-init performs a secret-free OS/bootstrap step and writes the
  `/etc/norn-ha-lab` fault-injection guard.
- Ansible performs repeatable service convergence and rolling quorum checks.
- Runtime secrets live at `~/.config/norn/ha-lab/NAME.env` with mode `0600`.
  They are never put in cloud-init, Terraform state, inventory, or the repo.
- Drill logs are written below ignored `.evidence/`; durable production receipts
  should contain only their object-storage ID/checksum and measured RTO/RPO.

## One-path workflow

```bash
cd v2/infra/ha-lab
cp terraform/terraform.tfvars.example terraform/terraform.tfvars
# Review owner, expiry, region, SSH key, source /32, and current price.

scripts/lab bootstrap-tools
scripts/lab init
scripts/lab check
scripts/lab plan -var-file=terraform.tfvars
scripts/lab apply -var-file=terraform.tfvars
scripts/lab secrets init norn-ha-eval
scripts/lab pki control-init
scripts/lab converge
scripts/lab security-cutover cutover --confirm-disposable-lab
scripts/lab deploy-traefik
scripts/lab verify
scripts/lab receipt start database.restore isolated-pitr
scripts/lab control-failover
scripts/lab failover
scripts/lab pitr
scripts/lab norn-failover
scripts/lab security-cutover activate-production
scripts/lab reboot
scripts/lab status
scripts/lab replace-member norn-ha-eval-03 --confirm-disposable-lab
```

When recreating a fully destroyed cluster with preserved controller secrets,
run `scripts/lab secrets reset-security NAME --confirm-disposable-lab` before
`converge`. This retains keys but returns the new Consul/Nomad data plane to the
pre-cutover development phase so ACL/TLS state can be bootstrapped in order.

If a prior disposable teardown removed the Spaces backend bucket but the
controller still has its complete stale bucket/key tuple, run
`scripts/lab backup-repository reinit-missing` followed by
`scripts/lab state-backend recover-missing`.
The recovery obtains an independent bootstrap credential and refuses to rotate
anything unless the previous bucket is verifiably absent.

Provisioning is expected to be convergent: run `scripts/lab converge` twice,
reboot one member, rerun `converge`, and require the same three-member quorum.
The failover and PITR commands refuse to run unless the remote bootstrap marker
exists.

Destroy requires an exact opt-in and applies a saved destroy plan:

```bash
scripts/lab destroy --confirm-disposable-lab
scripts/lab backup-repository cleanup-destroyed --confirm-disposable-lab
```

Review the plan and save required evidence before confirming. Terraform state
is remote, versioned, and lockfile-enabled. The guarded backup cleanup verifies
that the current remote state is Terraform v4 with zero resources before it
removes every object version, the generated bucket, and its scoped key.

## Recovery scope

`scripts/lab failover` stops Patroni on the current primary, proves election of
a different primary and successful writes, then rejoins the old member.
`scripts/lab pitr` takes a physical base backup, writes records on both sides of
an exact timestamp, restores into an isolated PostgreSQL instance, and proves
that only the before-target record exists.

The default WAL archive is local to keep this disposable test self-contained.
It proves PostgreSQL recovery mechanics but does **not** prove off-host or
regional backup durability. Before production admission, configure pgBackRest
or WAL-G with versioned S3-compatible storage, retention lock where required,
and run the same restore after deleting the source node.

`scripts/lab security-cutover cutover --confirm-disposable-lab` performs the
existing-cluster migration in resumable stages: Consul TLS/ACL transition,
scoped client tokens, outbound TLS on every peer, inbound TLS/default-deny,
Nomad RPC upgrade mode, Nomad ACL bootstrap/workload identity, and final RPC
enforcement. It refuses the unguarded form. `control-failover` then stops the
elected Consul and Nomad leaders in turn and requires new leaders, three-member
rejoin, two running toy allocations, and one passing Norn API.

The host resolver forwards only `.consul` to the local Consul DNS listener so
Consul servers can refresh Nomad's HA JWKS URL. Nomad's HTTPS endpoint verifies
the server certificate and enforces ACLs, but does not require HTTP client
certificates: Consul's JWT auth method cannot present one. Nomad RPC and Consul
internal RPC remain mutually authenticated. Controller-held CA private keys are
mode `0600`; a real environment must move issuers to protected custody and add
certificate/token rotation.

`security-cutover activate-production` is a guarded final step. It proceeds
only when `profile.production` is the sole failed readiness check, rolls Norn
across all contenders, requires zero failed checks, and restores development
mode if convergence or the final gate fails.

`scripts/lab deploy-traefik` runs one digest-pinned ingress allocation on every
regional member. It stores the Consul catalog token at the job-owned Nomad
Variables path, keeps it out of Terraform and the job file, and waits for all
three allocations plus the private node-port `/ping` probe. Run it after the
security cutover so the Consul HTTPS endpoint and scoped Nomad Variables policy
are active. Terraform's regional load balancer is the only public caller of
that node port; application allocations remain dynamic, passing-only Consul
catalog targets.

`scripts/lab norn-failover` records a `node.failover` drill through the active
Norn API, stops that contender, waits for a different member to acquire the
Consul session lock, completes the same receipt through the promoted API, and
requires the resulting HMAC mutation receipts to verify. Cleanup always
restarts the former contender and then reasserts that only one API is passing.
This proves shared database-backed control/audit continuity. The replicated
private Git/SOPS catalog and registry inputs have also been exercised through a
promoted controller in the live acceptance run.

The `nornops` account has no general sudo policy. Its Docker group membership
is still effectively host-root authority and exists only because the current
build/deploy implementation drives the local Docker daemon; production should
move builds to an isolated builder and remove that membership.
