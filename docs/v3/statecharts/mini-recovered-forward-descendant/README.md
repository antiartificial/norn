# Mini recovered-forward descendant upgrade gate

This model checks that a completed recovered-forward receipt continues to gate
later platform upgrades after Mini moves from the recovery candidate to a
newer signed release. The accepted path requires retained recovery evidence,
a signed current release in the recovery candidate's Git ancestry, an admitted
operation mode, and API and host-agent identities equal to that exact active
release. A signed but unrelated release or a disallowed operation mode must be
held.

`LegacyBug.cfg` preserves the former exact-runtime assumption: it accepts only
when the API and host agent still run the original recovery candidate. Its
shortest path holds the next upgrade after the first signed descendant becomes
active. `UpgradeModeBug.cfg` models the later operation-mode omission: signature,
ancestry, API identity, and host-agent identity are valid, but the operation's
`upgrade` mode is missing from the allowlist. `Valid.cfg` admits the mode and
uses the signed-descendant rule.

The model abstracts cryptographic verification and Git ancestry into
`activeReleaseSigned`, `activeReleaseDescends`, and the finite release set.
The requested operation is `upgrade`; `UpgradeModeAllowed` is a configuration
input representing whether the actual mode allowlist admits that request. The
model abstracts string membership into `modeAccepted`. The shell integration test
`PlatformUpgradeIntegrationTests.test_recovery_gate_accepts_only_the_current_signed_recovery_descendant`
replays the Git-lineage boundary against the actual controller helper. It does
not prove runtime signatures, host process stability, database durability,
API health, or live Mini behavior.

Reproduce into scratch outputs with:

```sh
bun /Users/arti/.agents/skills/statechart/scripts/statechart.ts \
  "$PWD/RecoveryDescendant.tla" --config "$PWD/Valid.cfg" \
  --view "$PWD/view.ts" --out /tmp/norn-mini-recovery-descendant-valid

bun /Users/arti/.agents/skills/statechart/scripts/statechart.ts \
  "$PWD/RecoveryDescendant.tla" --config "$PWD/LegacyBug.cfg" \
  --view "$PWD/view.ts" --out /tmp/norn-mini-recovery-descendant-legacy-bug

bun /Users/arti/.agents/skills/statechart/scripts/statechart.ts \
  "$PWD/RecoveryDescendant.tla" --config "$PWD/UpgradeModeBug.cfg" \
  --view "$PWD/view.ts" --out /tmp/norn-mini-recovery-descendant-mode-bug
```
