# M2 source ambiguity reconciliation contract — 2026-09-26

Status: private signed admission and exact source-kind claim are implemented in draft PR #76. No reconciliation command, fresh external proof, reservation/fence transfer, or successor source continuation is implemented or qualified yet.

The admission slice signs one exact failed/manual-recovery predecessor at `stop-intended` or `lock-intended`. The successor uses the existing `database.mysql-source-snapshot` kind and the original exact source payload, with the predecessor digest, checkpoint, and fence epoch/owner in signed metadata. This keeps the established staging/retention receipt contract usable after a proved transfer. A catalog-gated transaction rechecks the predecessor, source row, catalog binding, and held fence; a competing successor request key is rejected. A disposable PostgreSQL test exercised real claim expiry, signed admission, exact source-kind claim, replay before and after successor expiry, competing-key rejection, and missing-fence rejection. `go test ./store -count=1` passed against the disposable PostgreSQL instance; its container was removed. Admission and claim alone cannot execute a successor and must not be treated as a recovered source.

## Observed boundary

`source` accepts and claims a one-attempt `database.mysql-source-snapshot` operation. `StopClaimedMySQLSourceJob` writes `stop-intended` before the Nomad CAS stop; `LockClaimedMySQLSourceAccount` writes `lock-intended` before altering the MySQL account. A lost response leaves the physical source reserved and the runtime mutation fence held. `inspect-source --observe-external` can report the exact signed job stopped and the catalog-bound account locked, but those booleans are advisory and do not advance the durable source state.

An in-place retry is not a complete recovery design. The CLI only claims a queued source operation, admission fixes `MaxAttempts=1`, effect functions require a live exact claim and `status=running`, and the source reservation prevents a second independent snapshot from silently replacing it. An expired or failed predecessor therefore needs a separately accepted successor. Simply promoting `stop-intended` or `lock-intended` on the predecessor would leave staging and retention without a valid claim.

## Successor contract

1. Accept a private, one-attempt source reconciliation operation with an explicit predecessor operation ID, predecessor acceptance digest, expected source identity, catalog revision, exact Nomad job revision, intended checkpoint, and stable request key. It must have no HTTP route or queue worker that selects a predecessor implicitly.
2. Verify the predecessor's signed acceptance and one-way intent, its failed/manual-recovery state, the exact source reservation, and the still-held runtime fence. Deny a successful, canceled, changed, or already reconciled predecessor. Verify the active catalog still resolves the same physical source and maintenance identity.
3. Under the successor's renewed claim, independently observe the exact stopped Nomad job for `stop-intended`. For `lock-intended`, require the stopped-job observation plus a fresh read proving the exact runtime account locked and sessions drained. A negative, indeterminate, or timed-out observation leaves all control state unchanged and the fence held. Never repeat a Nomad stop or MySQL account mutation to resolve a lost response.
4. In one catalog-gated PostgreSQL transaction, recheck the successor claim, predecessor status and signed identity, source row and checkpoint, active catalog, runtime fence epoch/owner, and any runtime-launch reservation. Persist the observed checkpoint proof and successor link, then transfer the source reservation and fence ownership to the successor without a release interval. Record the predecessor as reconciled while leaving its original failed status and evidence intact. A repeated successor key returns its existing terminal result; a different successor is rejected.
5. The successor runs only the effects after the proved checkpoint: account lock after stop proof, or staging after account-lock proof. Staging, retention, and terminal success use the successor's signed identity and receipts. A later ambiguous effect again requires a new explicit successor; no automatic retry crosses an external effect boundary.
6. Source-to-restore references must point to the successful successor's signed retention receipt. The predecessor's failed or partly observed artifact never qualifies restore. The source and account remain fenced until the separately accepted restore/recovery path releases them.

## Qualification cases

- PostgreSQL-backed lost Nomad response with job observed stopped; wrong job revision, active allocation, timeout, changed catalog, stolen claim, or changed fence must leave `stop-intended` and the reservation intact.
- Lost MySQL lock response with stopped job and locked/drained account; unlocked account, surviving session, changed maintenance identity, or failed secret read must leave `lock-intended` fenced.
- Two reconciliation contenders, old-claim return after transfer, lost transaction response, same-key replay, changed-key replay, and expired successor claim must never produce two owners or repeat the ambiguous effect.
- A compiled CLI fixture must exercise the full successor through signed retained proof and restore, including interruption immediately before and after transactional transfer. A separate-node/provider and managed-MySQL rehearsal remain release gates.

This contract is deliberately stricter than the read-only inspection output. Observing a true boolean is evidence for a prospective reconciliation transaction; it is not itself a durable proof or permission to mutate the source.
