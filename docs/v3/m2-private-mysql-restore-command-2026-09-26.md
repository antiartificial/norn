# Private retained MySQL restore command — 2026-09-26

`v2/api/cmd/norn-mysql-maintenance restore` consumes one explicitly selected,
already accepted and signed `database.mysql-restore` operation. It verifies
the expected target name before claiming that exact one-attempt operation,
opens a versioned and object-locked S3 bucket, verifies the retained source
artifact, prepares the empty target, then imports and verifies the signed SQL
expectation. The runtime mutation fence remains active after success. A
separate signed `recover` command must prove the source remains stopped and
unlock the target before any app is resumed.

Build from `v2/api`:

```sh
go build -buildvcs=false -o ./norn-mysql-maintenance ./cmd/norn-mysql-maintenance
./norn-mysql-maintenance restore \
  --database-url-file /private/control-url \
  --audit-key-file /private/audit-key \
  --authority CONTROL_AUTHORITY_UUID \
  --secrets-dir /private/mysql-secrets \
  --restore-operation-id SIGNED_RESTORE_UUID \
  --target-database EXPECTED_TARGET_DATABASE \
  --materialize-dir /private/materialized \
  --mysql-tool-path /usr/local/bin/mysql \
  --mysql-tool-sha256 EXPECTED_MYSQL_BINARY_SHA256 \
  --s3-endpoint s3.example.invalid:443 \
  --s3-bucket IMMUTABLE_BUCKET \
  --s3-prefix MYSQL_PREFIX \
  --s3-region REGION \
  --s3-access-key-file /private/s3-access-key \
  --s3-secret-key-file /private/s3-secret-key \
  --s3-spool-dir /private/s3-spool \
  --s3-spool-capacity BYTES
```

The input files must be owner-owned, owner-only regular files. Materialization
and S3 spool directories must be absolute, owner-owned, and owner-only. The S3
credential needs conditional-create probe permission as well as artifact read
access; object lock and versioning are checked before the operation is claimed.
For a disposable numeric loopback endpoint only, add `--s3-loopback-http`.

The preceding [source command](m2-private-mysql-source-command-2026-09-26.md)
can produce the signed retained artifact, and [restore admission](m2-private-mysql-restore-admission-2026-09-26.md)
selects its catalog target. This restore command does not accept a restore
request or choose a target itself. An interrupted import is
inspection-only; do not rerun the one-attempt operation. A successful command
is only a local restore proof until the remote-provider and separate-node gates
are qualified.

The disposable WordPress fixture passed with this compiled command, the
separate recovery command, and a fresh WordPress allocation on the restored
target. It rejected a wrong target before consuming the queued restore and
rejected tampered retained bytes before any restore intent. The fixture left
zero Docker containers and its original 290 volumes.
