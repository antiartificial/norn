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
age is one hour unless the reviewed `NORN_LEGACY_BACKUP_MAX_AGE_SECONDS`
configuration sets a different positive bound. Both producer timestamps must
be parseable UTC RFC3339 values, and the successful shadow receipt must finish
after the backup was created. The verifier requires the owner-supplied
`NORN_DATABASE_URL` and `NORN_AUDIT_SIGNING_KEY` used by the backup producer;
load them from the owner-only runtime secret source without placing them in the
command line or ledger. Before invoking `platform-upgrade legacy-baseline`, it
requires healthy Nomad and Consul observations, then captures a stable digest
of every app spec plus allocation identities, routes, secret-reference names,
service state, and periodic schedules from the live API and service manifest.
Credential-bearing endpoint URLs are refused rather than persisted.

The legacy-baseline operation drain is scoped to Mini-affecting work. Queued
Fleet GitHub dispatch receipts remain in the shared PostgreSQL control store
and do not block the Mini-only upgrade; running dispatches and Mini operations
still block. This preserves receipt evidence without claiming Fleet/provider
reconciliation or requiring a Fleet cluster to be online.

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

## Re-adopting a later signed source after a failed transition

If Mini has since returned to a different signed release, do not relabel or
rewrite the earlier failed-transition ledger, reconciliation, or v1 adoption
receipt. The v2 recovered-baseline adoption contract keeps the two release
identities separate: `sourceSHA` is the currently serving signed Mini release;
`incidentCandidateSHA` is the candidate named by the historical failed ledger
and reconciliation. It writes a new owner-only
`.norn-recovered-baseline-adoption-v2.json`, leaving the v1 receipt untouched.
The historical `matchesBefore=false` remains explicit and preservation of the
pre-failure application state stays unproven.

After that adoption is recorded, the recovered-forward wrapper takes a fresh
protected database backup and exact-candidate signed shadow restore, then
captures the current Mini workload digest as the new preservation baseline.
It separately binds the historical incident digest for audit. The new
transition can therefore prove preservation from the accepted current source
without claiming that the earlier failed attempt preserved its older baseline.
While the failed fence remains, ordinary `platform-upgrade upgrade` is
prohibited until the recovered-forward journal reaches `candidate-promoted`.
After promotion, a later protected forward upgrade is allowed only when the
owner-only journal and adoption receipt still verify, the exact source and
candidate signatures and signed transition evidence verify, the preservation
receipt matches its journaled digest, the current symlink still points to that
candidate, and the live API/schema and host agent prove the same active release.
The retained failed-fence file remains unchanged as the incident record. The
eligibility check does not authorize rollback, legacy-baseline replay, proxy
changes, or an `env-exec` escape from the canonical platform-upgrade path.

When preparing a new immutable recovered-forward generation after audit-key
rotation, the active `NORN_AUDIT_SIGNING_KEY` continues to verify the current
adoption receipt and sign new evidence, generation manifests, and pointers.
If the completed predecessor's preservation receipt and source evidence were
signed with the retained prior key, provide that value through the optional
`NORN_AUDIT_HISTORICAL_SIGNING_KEY` protected environment input for this
preparation. The predecessor's adoption receipt is resolved separately: the
generation preparer uses the fixed current receipt when its raw SHA-256 matches
the predecessor evidence, otherwise it searches only owner-only archived
siblings and accepts a receipt only when its raw digest matches that evidence.
An archived receipt is verified with the predecessor evidence key; the current
receipt and all new evidence remain verified with the active key. Identical
archived copies are selected in deterministic filename order and are never
rewritten. Missing, unsafe, digest-mismatched, or incorrectly signed receipts
fail before a new generation pointer is activated. The historical key is used
only for predecessor verification; it is never used to sign new artifacts or
passed to mutation/finalization children. If omitted, predecessor verification
uses the active key. Neither key belongs on a command line or in logs, and
historical receipts remain byte-for-byte unchanged.
