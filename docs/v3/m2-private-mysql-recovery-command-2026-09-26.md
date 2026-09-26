# Private MySQL recovery command — 2026-09-26

`v2/api/cmd/norn-mysql-maintenance` now has a `recover` command for an already
completed, signed `database.mysql-restore` operation. It accepts a separate
one-attempt recovery operation under the control plane's audit HMAC key,
claims **only that operation ID**, reobserves the exact stopped Nomad source
and locked MySQL accounts, proves the restored target, unlocks its runtime
account, and releases the exact global mutation fence. A replay with the same
request key returns the original successful recovery operation. An interrupted
or uncertain effect remains fenced for inspection; the command does not claim
another queued operation or retry an already running one.

If the private recovery command's claim expires, control-plane recovery marks
its operation failed with `manualRecoveryRequired` and
`mysqlMaintenanceState=needs-inspection`, retaining the runtime fence and
evidence archive intent. The same rule covers an expired private source
command. This makes an interrupted command visible and terminal; it does not
automatically retry an ambiguous target unlock or release the fence.

The command requires an expected control authority, restore operation UUID,
and target database name. Read the PostgreSQL URL and current audit key from
owner-owned mode-`0600` regular files; provide any previous audit verification
keys through repeatable `--previous-audit-key-file` flags. The MySQL secret
directory must be owner-only and contain the catalog's exact referenced
secrets. Run the command on a host that can reach the control PostgreSQL,
Nomad, and MySQL endpoints. For example:

```sh
go build -buildvcs=false -o ./norn-mysql-maintenance ./cmd/norn-mysql-maintenance
./norn-mysql-maintenance recover \
  --database-url-file /private/control-url \
  --audit-key-file /private/audit-key \
  --authority CONTROL_AUTHORITY_UUID \
  --secrets-dir /private/mysql-secrets \
  --nomad-url https://nomad.example.invalid:4646 \
  --restore-operation-id RESTORE_OPERATION_UUID \
  --target-database EXPECTED_DATABASE \
  --actor-issuer OPERATOR_ISSUER \
  --actor-subject OPERATOR_SUBJECT \
  --request-key STABLE_RECOVERY_REQUEST_KEY
```

Add `--accept-only` to sign and return the recovery operation without claiming
it or changing MySQL. Then use `--inspect-only` with the same exact identity to observe the existing
signed recovery without accepting, claiming, unlocking, or releasing
anything. It returns the durable intent state and advisory checks for the
stopped source, target data, and target runtime account (`locked`, `unlocked`,
or `indeterminate`). An unavailable live check is reported as unverified;
the inspection result does not authorize a retry or fence release. A recovery
that succeeded already returns its terminal operation ID.

If an earlier recovery claim expired after target unlock intent, first use
`--inspect-only` with that earlier request key. When the target account is
independently observed **unlocked**, target data still matches the signed
artifact, and the source job/account remain stopped and locked, run a new
request key with `--reconcile-prior-recovery-id PRIOR_RECOVERY_UUID`. This
accepts a separate signed one-attempt operation bound to the failed recovery's
canonical digest and exact held fence. It rechecks the live evidence under
its own claim, releases the fence and writes a terminal receipt atomically.
It never repeats `ALTER USER`; the prior operation remains failed with a link
to its successful reconciliation. A locked or indeterminate target, changed
data, lost source stop, changed catalog, or stale fence keeps the fence held
for inspection. `--accept-only` can sign this reconciliation before execution.

Run the build from `v2/api`. The opt-in WordPress fixture builds this binary and passed the complete
signed deploy, guarded source stop, S3-emulated retention, restore, command
recovery, same-key replay, and fresh WordPress deployment on the recovered
database. It also rejected a foreign exact-ID claim before consuming its
queued source operation, and rejected a wrong target before accepting a
recovery operation. Docker returned to zero containers and the original
290 volumes after the run. The fixture does not prove a real remote provider,
separate-node recovery, managed MySQL, or Mini rollback.

This subcommand is limited to recovery. Separate [source](m2-private-mysql-source-command-2026-09-26.md),
[restore admission](m2-private-mysql-restore-admission-2026-09-26.md), and
[restore](m2-private-mysql-restore-command-2026-09-26.md) commands handle the
preceding stages. The public restore capability remains closed until the
remaining M2 release gates are proven.
