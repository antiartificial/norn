# Private MySQL restore admission — 2026-09-26

`norn-mysql-maintenance admit-restore` signs a one-attempt restore request from
a retained source operation and a named destination in the active database
catalog. The operator supplies no SQL bytes, artifact path, target credentials,
or physical binding. Admission verifies the signed staging and retention
receipts, checks that the exact source fence epoch and stopped/account-locked
state are still held, and resolves a
different physical target from the catalog. It performs no MySQL write.

Build from `v2/api`, then run:

```sh
go build -buildvcs=false -o ./norn-mysql-maintenance ./cmd/norn-mysql-maintenance
./norn-mysql-maintenance admit-restore \
  --database-url-file /private/control-url \
  --audit-key-file /private/audit-key \
  --authority CONTROL_AUTHORITY_UUID \
  --source-operation-id RETAINED_SOURCE_UUID \
  --target-profile CATALOG_PROFILE \
  --target-logical-id CATALOG_LOGICAL_ID \
  --target-database EXPECTED_DATABASE \
  --actor-issuer OPERATOR_ISSUER \
  --actor-subject OPERATOR_SUBJECT \
  --request-key STABLE_RESTORE_REQUEST_KEY
```

The command prints `restore_operation_id=... status=queued`. Pass that exact
ID to the separate [restore command](m2-private-mysql-restore-command-2026-09-26.md).
Use the same selection and request key to retrieve the same accepted operation
after an uncertain command response. A conflicting source selection for that
request identity fails instead of changing the signed request.

The disposable WordPress fixture runs this command between the compiled
[source](m2-private-mysql-source-command-2026-09-26.md) and restore commands.
It rejects a wrong target before acceptance, proves same-key replay, and
checks that the signed request contains the expected catalog target and
retained source artifact. This is a local qualification; the remote-provider,
separate-node, managed-MySQL, and Mini rollback gates remain open.
