# M2 source ambiguity reconciliation contract — 2026-09-26

Status: private signed admission, exact source-kind claim, fresh external proof, atomic reservation/fence transfer, and the private `reconcile-source` CLI are implemented in draft PR #76. A compiled Nomad/MySQL/S3-emulator restore rehearsal passed. A transferred successor whose claim expires before the next effect can now be named as the failed predecessor of another signed successor. Provider/managed-MySQL proof and release qualification remain open.

The admission slice signs one exact failed/manual-recovery predecessor at `stop-intended` or `lock-intended`. The successor uses the existing `database.mysql-source-snapshot` kind and the original exact source payload, with the predecessor digest, checkpoint, and fence epoch/owner in signed metadata. The claimed transition observes the exact stopped Nomad job and, for `lock-intended`, the locked/drained MySQL account. A signed proof and the predecessor's original intent are stored before the active source row and runtime fence change owner in one PostgreSQL transaction. The failed predecessor remains inspectable. The successor can continue the existing account-lock and signed SQL-stage path. A disposable PostgreSQL test covers negative observations, claim loss after observation, transfer, same-claim replay, tampered proof rejection, and continuation at both checkpoints. A separate compiled fixture proves the lost Nomad stop response through retention, restore, and a recovered WordPress deployment; it does not prove provider or managed-MySQL recovery.

Migration 40 adds only the predecessor-history table. It preserves the existing reader/writer 5/31 schema compatibility floor so the additive migration alone does not block the prior Mini binary during the rollback window. The synthetic Mini migration test explicitly checked that the previous 5/31 binary contract can open the migrated schema. Actual rollback after a new reconciliation effect still needs its own runtime rehearsal and runbook boundary.

Migration 41 widens the history checkpoint constraint to include `stop-proved` and `lock-proved` while retaining the same 5/31 compatibility floor. The failed successor is eligible only while it still owns the source row and runtime fence at one of those proved checkpoints. The new operation freshly observes the exact stopped job, and also the locked/drained account for `lock-proved`, before another atomic transfer. Every predecessor remains inspectable through its signed proof chain. `stage-intended`, `publish-intended`, and later ambiguous effects still require separate recovery designs; this change does not authorize redoing a dump or object publication.

The full `go test ./store -count=1` suite passed against a fresh disposable PostgreSQL 16 instance after the migration shape was finalized. The exact reconciliation test also passed after the final replay fence check. The disposable containers were removed. The first full-suite rerun reused a fixture that had applied an earlier draft of migration 40 and correctly failed its checksum; it was discarded before the clean run.

## Observed boundary

`source` accepts and claims a one-attempt `database.mysql-source-snapshot` operation. `StopClaimedMySQLSourceJob` writes `stop-intended` before the Nomad CAS stop; `LockClaimedMySQLSourceAccount` writes `lock-intended` before altering the MySQL account. A lost response leaves the physical source reserved and the runtime mutation fence held. `inspect-source --observe-external` can report the exact signed job stopped and the catalog-bound account locked, but those booleans are advisory and do not advance the durable source state.

An in-place retry is not a complete recovery design. The CLI only claims a queued source operation, admission fixes `MaxAttempts=1`, effect functions require a live exact claim and `status=running`, and the source reservation prevents a second independent snapshot from silently replacing it. An expired or failed predecessor therefore needs a separately accepted successor. Simply promoting `stop-intended` or `lock-intended` on the predecessor would leave staging and retention without a valid claim.

## Successor contract

1. Accept a private, one-attempt source reconciliation operation with an explicit predecessor operation ID, predecessor acceptance digest, expected source identity, catalog revision, exact Nomad job revision, intended checkpoint, and stable request key. It must have no HTTP route or queue worker that selects a predecessor implicitly.
2. Verify the predecessor's signed acceptance and one-way intent, its failed/manual-recovery state, the exact source reservation, and the still-held runtime fence. Deny a successful, canceled, changed, or already reconciled predecessor. Verify the active catalog still resolves the same physical source and maintenance identity.
3. Under the successor's renewed claim, independently observe the exact stopped Nomad job for `stop-intended`. For `lock-intended`, require the stopped-job observation plus a fresh read proving the exact runtime account locked and sessions drained. A negative, indeterminate, or timed-out observation leaves all control state unchanged and the fence held. Never repeat a Nomad stop or MySQL account mutation to resolve a lost response.
4. In one catalog-gated PostgreSQL transaction, recheck the successor claim, predecessor status and signed identity, source row and checkpoint, active catalog, runtime fence epoch/owner, and any runtime-launch reservation. Persist the observed checkpoint proof and successor link, then transfer the source reservation and fence ownership to the successor without a release interval. Record the predecessor as reconciled while leaving its original failed status and evidence intact. A repeated successor key returns its existing result. A different successor is rejected while the first is active or after transfer; a failed successor that transferred nothing can be replaced under a new explicit signed request key.
5. The successor runs only the effects after the proved checkpoint: account lock after stop proof, or staging after account-lock proof. Staging, retention, and terminal success use the successor's signed identity and receipts. A failed successor at `stop-proved` or `lock-proved` can itself be named by a new signed successor after fresh observation. A later ambiguous stage or publication effect remains fenced for separate recovery design; no automatic retry crosses an external effect boundary.
6. Source-to-restore references must point to the successful successor's signed retention receipt. The predecessor's failed or partly observed artifact never qualifies restore. The source and account remain fenced until the separately accepted restore/recovery path releases them.

## Qualification cases

- PostgreSQL-backed lost Nomad response with job observed stopped; wrong job revision, active allocation, timeout, changed catalog, stolen claim, or changed fence must leave `stop-intended` and the reservation intact.
- Lost MySQL lock response with stopped job and locked/drained account; unlocked account, surviving session, changed maintenance identity, or failed secret read must leave `lock-intended` fenced.
- Two reconciliation contenders, old-claim return after transfer, lost transaction response, same-key replay, changed-key replay, and expired successor claim must never produce two owners or repeat the ambiguous effect.
- The compiled CLI fixture exercises `stop-intended` with the Nomad stop effect completed and its response deliberately lost, then signs a successor, proves the stopped job, transfers the fence, locks the account, retains the artifact, restores it, and serves WordPress from the restored target. PostgreSQL qualification injects a failure during the transfer after the source-intent update and verifies rollback of intent, fence, and proof at both checkpoints; same-claim replay after a committed transfer uses durable proof without another external observation. PostgreSQL tests also expire a transferred successor at `stop-proved` and `lock-proved`, then verify a second signed transfer and inspection of both predecessors. The compiled second-successor fixture exercises the stop-proved branch through restore. An actual process-kill rehearsal, plus separate-node/provider and managed-MySQL rehearsal, remain release gates.

## Private operator command

Inspect the failed predecessor with `inspect-source` and keep its exact ID. Use a new stable request key for one explicit successor. The operator supplies the same signed source database and `mysqldump` binary used by the predecessor; the command rejects a mismatch before accepting a successor. It never repeats the ambiguous Nomad stop or MySQL account mutation. A successful replay returns the successor's signed retained result.

```sh
./norn-mysql-maintenance reconcile-source \
  --database-url-file /private/control-url --audit-key-file /private/audit-key \
  --authority CONTROL_AUTHORITY_UUID --schema public \
  --prior-source-operation-id FAILED_SOURCE_OPERATION_ID \
  --source-database EXPECTED_SOURCE_DATABASE \
  --actor-issuer OPERATOR_ISSUER --actor-subject OPERATOR_SUBJECT \
  --request-key STABLE_SUCCESSOR_REQUEST_KEY \
  --secrets-dir /private/mysql-secrets --nomad-url https://nomad.example.invalid:4646 \
  --stage-dir /private/sql-stage --dump-tool-path /usr/local/bin/mysqldump \
  --s3-endpoint s3.example.invalid:443 --s3-bucket IMMUTABLE_BUCKET \
  --s3-prefix MYSQL_PREFIX --s3-region REGION \
  --s3-access-key-file /private/s3-access-key --s3-secret-key-file /private/s3-secret-key \
  --s3-spool-dir /private/s3-spool --s3-spool-capacity BYTES
```

For the disposable numeric loopback S3 emulator only, add `--s3-loopback-http`. The command keeps the source fenced on any indeterminate observation or later effect failure; inspect the exact predecessor and successor before deciding the next action. If a transferred successor fails at `stop-proved` or `lock-proved`, name that failed operation as `--prior-source-operation-id` and use a new request key. Do not point the new command at its older, already reconciled predecessor.

This contract is deliberately stricter than the read-only inspection output. Observing a true boolean is evidence for a prospective reconciliation transaction; it is not itself a durable proof or permission to mutate the source.
