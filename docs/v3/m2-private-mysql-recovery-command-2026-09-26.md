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
