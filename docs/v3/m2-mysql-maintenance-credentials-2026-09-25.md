# MySQL restore maintenance credentials (private M2 slice)

An application MySQL binding may now carry an optional `mysqlMaintenance`
record. It contains a positive maintenance generation, the exact runtime,
restore and fence account hosts, and separate restore and fence roles and
credential references.
The catalog rejects missing hosts, invalid roles or references, and any reuse
of the runtime role or credential, or reuse between restore and fence. Changes
to this identity require the enclosing binding generation to increase.

The private restore runner resolves the immutable catalog revision recorded by
the accepted operation. The signed restore payload copies the complete
maintenance identity, including references but never credential values, and
the prepare and begin checkpoints reject a mismatch. Both prepare preflights,
including an identical replay while runtime access is locked, use the restore
identity. `mysql` itself connects
with the restore role and credential reference; it verifies `CURRENT_USER()`
against the signed restore account before import and cannot silently fall back
to the application runtime credential. Post-import expectation verification
also uses this restore identity, so the application runtime account may remain
locked throughout restore.

Migration 27 connects the private runtime-account lock primitive to the
claimed runner. It commits an exact `lock-intended` checkpoint before
`ALTER USER`, verifies the lock and two zero-session observations, then commits
`verified-lock` before SQL can begin. The maintenance fence survives import
success and every uncertain lock or SQL outcome. No worker unlocks the runtime
account automatically. A distinct signed resume and source-quiescence protocol
remain required before this lane can be exposed. There is no public route or
capability for either primitive.
