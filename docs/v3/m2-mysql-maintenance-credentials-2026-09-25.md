# MySQL restore maintenance credentials (private M2 slice)

An application MySQL binding may now carry an optional `mysqlMaintenance`
record. It contains a positive maintenance generation, the exact runtime
account host, and separate restore and fence roles and credential references.
The catalog rejects missing hosts, invalid roles or references, and any reuse
of the runtime role or credential, or reuse between restore and fence. Changes
to this identity require the enclosing binding generation to increase.

The private restore runner resolves the immutable catalog revision recorded by
the accepted operation. The signed restore payload copies the complete
maintenance identity, including references but never credential values, and
the prepare and begin checkpoints reject a mismatch. `mysql` itself connects
with the restore role and credential reference; it cannot silently fall back
to the application runtime credential.

The existing private runtime-account lock primitive remains disconnected.
It issues `ALTER USER ... ACCOUNT LOCK` and kills sessions, but this branch
does not yet have a durable checkpoint that atomically records the intended
account identity before that external effect, records a verified locked state
after it, and keeps the restore maintenance fence active on crash or claim
loss. Wiring it into this runner without those checkpoints could leave an
account locked with no recoverable receipt, or could incorrectly continue a
restore after an unknown lock outcome. A later slice must add those durable
pre-lock and verified-lock checkpoints, bind them to this signed maintenance
identity and operation claim, then make restore begin depend on the verified
lock checkpoint. There is no public route or capability for either primitive.
