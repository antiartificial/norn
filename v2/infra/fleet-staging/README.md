# Work-staging representative application contract

This is a **direct Fleet workload**, separate from the Mini development Norn and
from the retired disposable pilot. It reuses the reviewed `hello-norn-mysql`
Go image source at `../fleet-pilot/hello-norn-mysql`, but uses separate Nomad
job templates and exercise receipts in this directory. It stores synthetic
IDs in the work Fleet's managed **application MySQL** cluster, not Norn's
control PostgreSQL or etcd. No credentials, cluster IDs, node IDs or pilot
run IDs are checked in.

The Norn catalog InfraSpec remains `deploy: false`. Its current translator
does not preserve the reviewed direct job's namespace, source/image metadata,
restricted Traefik routers, liveness restart, or rolling update policy. The
first Fleet-fitness proof may submit this reviewed digest-pinned job directly
to Nomad. That proves the substrate and workload continuity, **not** Norn's
declarative deployment or upgrade capability.

## Native Norn scaling handoff

The running direct Nomad job `hello-norn-mysql` cannot be handed to Norn's
`app.scale` controller by adding its InfraSpec to `NORN_APPS_DIR`. Native scale
resolves the job from a completed Norn `app.deploy` operation and its active
Fleet route. The resulting job ID is specific to that deployment revision;
it is not `hello-norn-mysql`. External deployment admission records the direct
job's evidence but does not publish a native Fleet route or change its owner.

To prove Norn-operated app scale, deploy an equivalent **separate** pilot
revision through Norn after the catalog is promoted: replace the inert repo,
image digest and endpoint, preserve the direct job's runtime behavior in the
managed renderer, deliver the four runtime variable keys to the revision's
job-owned variable path, and enable deployment. Keep the existing direct job
running until the new managed job has healthy allocations and its route is
verified. Then issue `app.scale` for `web` from 2 to 3 through Norn and check
the operation receipt, desired count, three healthy allocations on eligible
app nodes and routed writes/reads; scale back to 2 and repeat. The test must
use the managed job ID from the active route. A raw `nomad job scale` against
the direct pilot is useful Fleet substrate evidence, but not Norn controller
evidence. The direct pilot may be retired after the replacement proves service
and data continuity; it is not a permanent fixture.

## Preconditions

- PR #574's separate `app` pool is merged and deployed to the **work** Fleet:
  three controls, two fixed ingress clients, two `app` clients with max 3.
  Check the actual Nomad node pool and provider state before using this job.
- The work Fleet has a provisioned application MySQL cluster with durable
  provider backup/restore evidence. Its firewall trusts the `app` pool tag.
- Create exactly `norn-work-staging` in Nomad and grant a reviewed staging
  operator only the required job/variable ACL plus the Nomad node-status read
  and `app` node-pool read capabilities for `measure_app_pool.py`.
  The probe reads the cluster's app-pool node inventory, so its ACL may need a
  separate short-lived, read-only operator token; do not broaden the web
  job's identity. No `norn-pilot-*` namespace.
- Build the application from a protected Norn source SHA and verify the
  digest's provenance and SBOM as described in
  `docs/v2/operations/hello-norn-mysql-bootstrap.md` from the repository root.
- The protected runner creates two least-privilege database identities and
  two job-owned Nomad variables: `nomad/jobs/hello-norn-mysql-migrate` for
  one-time `CREATE`, and `nomad/jobs/hello-norn-mysql` for runtime
  `SELECT/INSERT/UPDATE`. Each carries its own DSN, reviewed provider CA and
  pinned private DB IP. Only the runtime variable carries the owner-only
  synthetic `PILOT_WRITE_TOKEN` key required by the unchanged image contract.
  Never print these values, pass them as CLI arguments, or add them to Git.
- Review the staging HTTPS hostname, TLS certificate, private readiness
  listener, gateway route, and two ingress nodes. The load balancer passes
  TLS to Traefik on ingress, while the service allocates only on `app` nodes.

Run the one-time migration, verify its successful allocation, revoke the
migration DB identity, and purge its job and variable. Then submit the service
job with a verified immutable image digest, exact source SHA, and reviewed
hostname using `nomad job run -namespace=norn-work-staging` and the two HCL
files under `hello-norn-mysql/nomad/`. The service has two replicas constrained
to distinct `app` hosts and a one-at-a-time, auto-reverting rolling update.
It never stores application data on a client disk.

## Evidence sequence

All commands below run on a Nomad-ACL-authorized, protected operator runner.
Keep output receipts owner-only. `--app-node-ids-json` must be the two full,
observed Nomad app node UUIDs currently hosting the replicas. The exercise
verifies job namespace, app pool, image digest, source SHA, exact hostname,
distinct app nodes, real public write/read, and every acknowledged write.
It emits no response bodies or credentials.

1. Capture `measure_app_pool.py --phase before --expected-count 2` and a
   baseline `exercise.py` JSON receipt. Retain its `preDatabaseProof.id`.
2. Issue an operator-reviewed Fleet **app** desired-count change 2→3. Capture
   the protected plan/approval/apply and provider/SSH/bootstrap receipts.
   Run `continuity_probe.py` against the baseline record throughout the
   change, capturing its start/end UTC times, failures, p95/p99 and observed
   allocation IDs; retain the exact provider event time in the same record.
   Capture `measure_app_pool.py --phase expanded --expected-count 3`. Repeat
   the exercise and `read_known_record.py` for the baseline ID.
3. Issue the reviewed 3→2 drain/contraction. Confirm the selected node is
   drained, allocations remain on two distinct ready app nodes, and the
   exact removed provider identity is gone. Capture
   another `continuity_probe.py` receipt while the drain occurs, then capture
   `measure_app_pool.py --phase contracted --expected-count 2`, repeat the
   exercise and read the original baseline ID again. Run
   `compare_app_pool.py --before ... --expanded ... --contracted ...` to
   verify the three receipts reflect one added and one retired app identity.
4. For ingress failover, run a bounded `exercise.py` load while an authorized
   operator withdraws or stops **one ingress** node. Record the exact event
   time and node/LB health, public errors and p95/p99, all acknowledged-write
   readbacks, service allocations, and recovery. Restore ingress and repeat
   from the other side. A clean pass requires zero lost acknowledged writes,
   no database data loss, two healthy app allocations, and the selected
   availability threshold; report any public errors rather than hiding them.
5. For a signed Norn version upgrade, record old/new immutable platform
   digests, control quorum/leader, work-staging API and job/allocation state,
   then run the authorized rolling upgrade while `continuity_probe.py` reads
   the same baseline MySQL ID. Recheck that ID and release admission afterwards.
   This measures Norn control-plane continuity. Upgrade of this app image is
   a separate Nomad rolling-update exercise, and native Norn app release
   promotion remains unproven until translator parity closes.

`exercise.py --fault-allocation UUID` optionally tests one app allocation
replacement. It does not kill a whole ingress node or mutate provider state.
Never infer an ingress or Norn-version pass from an allocation-only result.
If the stopped allocation fails to recover within the bounded wait, the
exercise makes one five-second public read of its acknowledged pre-fault
record and emits that result with `nomadProvenanceVerified=false`. A successful
read is useful persistence evidence but cannot turn the failed replacement
test into a pass.

The scripts are intentionally read-only except the bounded synthetic writes
and the optional exact allocation stop. Fleet scale and ingress fault actions
remain in their protected workflows with their own approvals and evidence.
`continuity_probe.py` reports public read continuity and full allocation UUIDs;
the before/after Nomad and protected-workflow receipts must independently bind
those UUIDs to the reviewed app job. It does not alone prove a control-plane
upgrade, provider scale, or exact cause of any public error.
`measure_app_pool.py` and `exercise.py` require Nomad task state and
`DeploymentStatus.Healthy=true` as well as distinct app hosts. That is Nomad
health evidence, not a Consul or public-route assertion; the separate HTTPS
exercise and ingress receipts must prove external availability.
