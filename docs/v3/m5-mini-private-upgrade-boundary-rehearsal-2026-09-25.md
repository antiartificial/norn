# Mini private upgrade and rollback boundary rehearsal — 2026-09-25

Status: the copied-data transition passed, but the installed Mini release has
no schema-contract capability. The protected upgrade lane therefore refuses
the transition before it can touch Mini's database. M5 remains open.

Read-only Mini inspection identified the running API artifact as the current
release binary recorded by release `a5da8ef15d12e9eca7561e90b90d96f6dc652a21`.
The local Mini checkout was clean but at an older source commit, so this
rehearsal built the release commit itself rather than treating the checkout as
the binary's source identity.

A fresh read-only custom PostgreSQL dump was restored into disposable
PostgreSQL 17.7 with `network=none` and no published ports. The copy had 28
original public tables and 247,189 rows. Ordered primary-key fingerprints and
row counts for every original table were recorded before each transition.
No Mini database, API process, worker, Nomad client, or application runtime
was changed.

The exact release-source binary started on the copy and served `/api/health`
with deployment and operation recovery disabled. Its legacy startup performs
baseline DDL but has no migration ledger or `norn.startup/v2` probe. Original
table counts and primary-key fingerprints remained unchanged. The current
candidate then ran `NORN_SCHEMA_MODE=migrate-only` through migration 23 twice,
ending at reader 4 and writer 19, and its passive/check API served health
without recovery, workers, or watchers.

The exact release-source binary also served health after migration 23. This
does not qualify rollback: the binary cannot read the new ledger or enforce the
reader/writer floor, so a direct restart would bypass the intended schema
fence. Original-row fingerprints still matched on this isolated copy, but that
only shows that the bounded startup did not alter those rows.

The current `platform-upgrade startup-contract` probe rejects the exact legacy
release before database access because it does not implement the bounded
`norn.startup/v2` capability. That refusal is the current safe boundary: a
normal protected upgrade cannot promote the candidate while this release is
its rollback target.

The private dump, temporary source and binaries, fingerprint files, logs, and
disposable container were removed after the rehearsal. Closing M5 requires a
reviewed legacy-to-contract baseline transition with a production storage
backup and restore proof, an explicit rollback or roll-forward boundary, and
a controlled Mini maintenance rehearsal. This copied-data result does not
authorize a Mini deployment.
