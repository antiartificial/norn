# Offline Fleet DELETE commitment boundary

The offline Fleet scale-in ledger stores one canonical commitment head and an
identical append-only event in a single etcd compare-and-swap. It binds the
admitted attempt, consumed effect grant, prechange checkpoint, target fence,
authority epoch, and supervised runtime identity. A lost ledger response is
recovered by reading those exact bytes; it never creates another commitment.
The source rows and commitment share the permanent etcd snapshot/restore
lifetime. Missing or divergent rows fail recovery closed.

**A commitment is historical pre-dispatch intent, not DELETE authorization.**
The local pre-CAS clock check rejects a grant already expired at that check,
but etcd has no server-time predicate in this transaction. The grant can expire
while the CAS request is in flight. The ledger therefore makes no claim that
the grant was fresh at the precise commit instant.

Before a future executor issues its single DigitalOcean DELETE, a separate
admission must independently verify the exact committed bytes and current
binding, consumed grant validity, owner authorization, held target fence,
authority epoch, and the identified supervised runtime. It must fail closed
when any check is unavailable, stale, or ambiguous. A DELETE response loss or
process crash after dispatch goes to reconciliation; it must never reissue
DELETE from the commitment alone. No production route or provider call is
wired to this ledger yet.
