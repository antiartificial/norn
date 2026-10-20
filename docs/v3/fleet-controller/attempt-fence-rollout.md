# Protected runner attempt fence rollout

PostgreSQL migration 51 raises the control writer contract from 32 to 33. A
version 32 authority must refuse read-write startup after that migration;
read-only inspection remains allowed. Upgrade the authority before starting a
protected contraction runner. Do not roll the authority back to a version 32
writer while migration 51 is active.

etcd has no schema compatibility ledger or writer floor. Stop every older
etcd-backed Fleet authority process, verify none remains serving writes, then
start the new authority before allowing a protected runner. Otherwise an old
authority can still accept an attempt without a fence binding. The new
runner must obtain the exact attempt's server-owned `fenceBinding` and call
`POST /api/v1/fleet/plans/{planID}/attempts/{attemptID}/validate-fence`
immediately before its first backend read. Validation refuses an absent or
stale binding; the effect writer must also enforce its fence at write time.
