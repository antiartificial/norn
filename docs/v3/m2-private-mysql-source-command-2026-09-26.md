# Private deployed MySQL source command — 2026-09-26

`v2/api/cmd/norn-mysql-maintenance source` admits a source snapshot from one
signed, successful deployment and a fresh Nomad observation. It claims that
exact signed one-attempt operation, stops the selected job, locks its runtime
MySQL account, stages SQL with the catalog-bound snapshot credential, then
publishes and verifies an immutable S3 artifact. It completes the operation
under its exact live claim only after the signed retention receipt is durable,
then removes the local SQL stage. Completion preserves the source runtime fence
and locked MySQL account for the separately accepted restore. A same-key replay
requires the successful terminal receipt and returns the source operation ID
without repeating external effects. A terminal replay also retries cleanup of
the exact signed local SQL stage if it remains. If terminalization or cleanup
fails, the source stays fenced for inspection.

`inspect-source` reads one explicit source operation without claiming it or
repeating an external effect:

```sh
./norn-mysql-maintenance inspect-source \
  --database-url-file /private/control-url \
  --audit-key-file /private/audit-key \
  --authority CONTROL_AUTHORITY_UUID \
  --schema public \
  --source-operation-id EXACT_SOURCE_OPERATION_ID
```

Its JSON reports operation/intent state, whether the claim lease is current,
whether the control-plane runtime fence is held, and whether the signed stage
and retention receipts verify. It omits SQL paths and credentials. A verified
retention receipt is historical proof; the command does not inspect the live
Nomad job, MySQL account, or remote object. Those require separate observations
before any reconciliation or release decision.

Add `--observe-external` to read the exact stopped Nomad revision,
catalog-bound MySQL account lock, and content-addressed object. It requires
`--secrets-dir`, `--nomad-url`, `--s3-endpoint`, `--s3-bucket`, `--s3-region`,
`--s3-access-key-file`, and `--s3-secret-key-file`; `--s3-prefix` selects the
original artifact namespace. The S3 verifier opens without the publisher's
conditional-write probe. Each result is an independent advisory boolean: an
absent object during `publish-intended` remains unresolved, and a present
object does not itself terminalize the source. Object verification reads the
whole object; `--observation-timeout` defaults to ten minutes. The optional
compiled-command WordPress exercise includes all three external checks, but
has not been rerun for this addition.

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

The PostgreSQL-backed source intent test rejects completion before retention,
proves terminal success after signed retention, keeps the runtime fence held,
and verifies expired-operation recovery cannot change the result. The
inspection path passed against disposable PostgreSQL 16 on 2026-09-26 for
stage-proved, ambiguous publish-intended, tampered retention, and terminal
retained-proved states. The advisory live observation also distinguished an
absent from a verified object during ambiguous publication without repeating
the upload. The CLI path is also included in the optional compiled
WordPress fixture; that fixture has not been rerun for this change. The
disposable WordPress fixture passed with the compiled source, restore,
and recovery commands. It rejected a wrong source database before consuming
the queued operation, proved a same-key retained replay, restored the retained
bytes, and served WordPress from the recovered database. This remains a local
qualification; remote-provider durability, separate-node materialization,
managed MySQL, and Mini rollback remain open gates.
