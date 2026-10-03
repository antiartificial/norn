# M5 protected Mini maintenance transition

The signed shadow rehearsal proves that an exact candidate can read and check
a privately restored copy of the Mini control database with recovery, workers,
and the Nomad watcher disabled. It does not authorize or execute the live
transition.

`v2/scripts/mini-protected-transition-rehearsal` composes the next scheduled
maintenance action. It requires a fresh legacy backup proof, the exact backup
artifact, and a successful version-2 signed-shadow receipt for the same bytes
and exact candidate SHA. The wrapper delegates backup identity, artifact,
future-skew, and age validation to `mini-verify-protected-backup`; its maximum
age is one hour unless the already reviewed `NORN_LEGACY_BACKUP_MAX_AGE_SECONDS`
configuration sets a different positive bound. Both producer timestamps must
be parseable UTC RFC3339 values, and the successful shadow receipt must finish
after the backup was created. The protected backup verifier requires the
owner-supplied `NORN_DATABASE_URL` and `NORN_AUDIT_SIGNING_KEY` values used by
the backup producer; load them from the owner-only runtime secret source
without placing them in the command line or ledger. Before invoking
`platform-upgrade legacy-baseline`, the wrapper
requires healthy Nomad and Consul observations, then captures a stable digest
of every app spec plus allocation identities, routes, secret-reference names,
service state, and periodic schedules from the live API and service manifest.
Credential-bearing endpoint URLs are refused rather than persisted.

The composer writes an owner-only proof-consumption record and ledger before
the irreversible command. The consumption record is derived from the full
backup proof and shadow receipt, so the same pair cannot be reused with a new
ledger filename in the same owner-only directory. Its
state changes to `candidate-recovery-required` before the legacy fence can be
installed. An interrupted or failed invocation must not be replayed: inspect
that ledger and the legacy fence state, then repair or roll forward with the
contract-aware candidate. A completed upgrade is accepted only when a second
read-only capture exactly matches the original stable view. The terminal
receipt records both digests and the compared snapshot without secret values.

Run only in an approved Mini maintenance window after independently reviewing
the backup restore and shadow receipts:

```sh
NORN_M5_PROTECTED_TRANSITION=1 \
NORN_DRAIN_MODE=fail \
v2/scripts/mini-protected-transition-rehearsal \
  --candidate-ref <exact-40-character-sha> \
  --candidate-sha <exact-40-character-sha> \
  --legacy-release <exact-40-character-sha> \
  --backup-proof /private/change/backup-proof.json \
  --backup-artifact /private/change/control.dump \
  --shadow-receipt /private/change/shadow-receipt.json \
  --ledger /private/change/transition-ledger.json \
  --receipt /private/change/transition-receipt.json
```

The wrapper does not make an old-binary rollback safe. After the fence, any
failure retains `candidate-recovery-required`; the operator must follow the
legacy-baseline roll-forward boundary. A passing hermetic test or receipt does
not by itself sign M5. Gate evidence still requires the scheduled on-host run,
application-availability observation, and operator review of the result.
