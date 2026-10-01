# M4 disposable replica and placement qualification

Run `v2/scripts/test-m4-capacity-local.sh` to exercise Norn's replica side of
the M4 capacity gate without provider or Fleet mutation. The harness creates a
private temporary directory, starts a loopback Nomad server, three `app` node
pool clients with `raw_exec`, and a Unix-socket-only PostgreSQL server. It then
runs `TestM4ReplicaIntentAcrossThreeDisposableNomadClients` and removes every
temporary process and file.

The test submits a two-replica service with a hard `distinct_hosts` constraint,
verifies two running allocations on different clients, accepts a signed durable
scale operation to three, and verifies three distinct placements. It reads the
persisted desired replica intent, applies that intent to a redeploy, and proves
the three placements survive. A second durable scale operation returns the job
to two distinct running placements.

This is pre-live evidence for durable replica intent, redeploy precedence,
logical app-pool selection, and actual Nomad placement. It does not create or
remove a VM, exercise the protected Fleet runner, prove a drain or provider
retirement, use separate physical failure domains, apply production ACL/TLS,
measure traffic availability, prove N-1 resource headroom, or verify worker
acknowledgement. The Fleet 2 -> 3 -> 2 capacity/drain rehearsal and loaded live
gate remain separate required evidence.
