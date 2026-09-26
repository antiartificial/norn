# Private deployed MySQL source command — 2026-09-26

`v2/api/cmd/norn-mysql-maintenance source` admits a source snapshot from one
signed, successful deployment and a fresh Nomad observation. It claims that
exact signed one-attempt operation, stops the selected job, locks its runtime
MySQL account, stages SQL with the catalog-bound snapshot credential, then
publishes and verifies an immutable S3 artifact. It removes the local SQL
stage only after the signed retention receipt is durable. A same-key replay
returns the retained operation ID without repeating external effects.

Build from `v2/api`:

```sh
go build -buildvcs=false -o ./norn-mysql-maintenance ./cmd/norn-mysql-maintenance
./norn-mysql-maintenance source \
  --database-url-file /private/control-url \
  --audit-key-file /private/audit-key \
  --authority CONTROL_AUTHORITY_UUID \
  --secrets-dir /private/mysql-secrets \
  --nomad-url https://nomad.example.invalid:4646 \
  --selection-file /private/source-selection.json \
  --source-database EXPECTED_SOURCE_DATABASE \
  --actor-issuer OPERATOR_ISSUER \
  --actor-subject OPERATOR_SUBJECT \
  --request-key STABLE_SOURCE_REQUEST_KEY \
  --stage-dir /private/sql-stage \
  --dump-tool-path /usr/local/bin/mysqldump \
  --s3-endpoint s3.example.invalid:443 \
  --s3-bucket IMMUTABLE_BUCKET \
  --s3-prefix MYSQL_PREFIX \
  --s3-region REGION \
  --s3-access-key-file /private/s3-access-key \
  --s3-secret-key-file /private/s3-secret-key \
  --s3-spool-dir /private/s3-spool \
  --s3-spool-capacity BYTES
```

The owner-only, single-line JSON selection file is the Go JSON form of
`store.MySQLSourceSnapshotAdmissionRequest`: `Binding` names the exact signed
deployment, source target, profile, logical ID, region and catalog revision;
`Maintenance` contains the catalog credential references; and
`DumpToolSHA256` is the digest of the selected `mysqldump` binary. Admission
rederives the source and Nomad allocation identity from signed control-plane
records and the live job. A mismatched selection is rejected before stopping
the app. The S3 bucket must already have object lock and versioning enabled,
and the credential must permit the conditional-create probe and publication.
For a disposable numeric loopback S3 endpoint only, add `--s3-loopback-http`.

The disposable WordPress fixture passed with the compiled source, restore,
and recovery commands. It rejected a wrong source database before consuming
the queued operation, proved a same-key retained replay, restored the retained
bytes, and served WordPress from the recovered database. This remains a local
qualification; remote-provider durability, separate-node materialization,
managed MySQL, and Mini rollback remain open gates.
