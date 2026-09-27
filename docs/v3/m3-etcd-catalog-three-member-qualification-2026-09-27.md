# M3 disposable etcd catalog activation qualification — 2026-09-27

The normal Fleet API's PostgreSQL catalog activation was exercised in the
existing disposable three-member TLS/RBAC fixture. This is local qualification
evidence, not M3 sign-off or permission to deploy to Fleet.

At Norn draft PR #77, parent head `2ce3315a` plus the process-test change in
this record, `v2/scripts/test-etcd-three-member-fleet` exited 0. Its
`TestEtcdFleetRuntimeProductionTLSRBACProcess` now uses a managed
`platform:operate` token to accept a signed catalog activation through the
ordinary production-profile binary, waits for the fenced worker's succeeded
receipt, and reads revision 1 through the redacted catalog endpoint. Each
process subtest runs with either no PostgreSQL URL or a poisoned PostgreSQL
URL. The etcd runtime principal is restricted to the fixture prefix; its
out-of-prefix reads and writes are denied.

The same process test passed on the initial three-member group, with one
member partitioned, with one member stopped, and after full-cluster snapshot
restore. The script also required a linearizable read and write to fail
without quorum and rejected a corrupted snapshot before restore. It removed
all qualification containers and its Docker network on exit; a post-run
inventory found neither remaining.

This proves the local catalog HTTP/worker path survives the fixture's
single-member faults and restored control store without control PostgreSQL.
It does not prove host-supervised Fleet member bootstrap or repair, a
multi-host/asymmetric partition, disk or quota alarms, certificate rotation,
version upgrades, sustained soak, provider workflow, or a live Fleet restore.
Those remain M3 release gates. The catalog path qualifies PostgreSQL services
only; MySQL catalog activation remains unsupported on etcd.
