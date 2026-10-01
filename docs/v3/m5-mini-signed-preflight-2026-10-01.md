# Mini signed candidate preflight — 2026-10-01

Status: blocked before candidate health; live Mini unchanged. This receipt is
the operator transcript for the exact Norn source
`8a291142677a7b00cc8606e61ac0a6952fff3e5c`, not an upgrade or M5 pass.

The protected [release run](https://github.com/antiartificial/norn/actions/runs/36813207019)
completed successfully and published
[`platform-8a291142677a7b00cc8606e61ac0a6952fff3e5c`](https://github.com/antiartificial/norn/releases/tag/platform-8a291142677a7b00cc8606e61ac0a6952fff3e5c)
with 16 assets, four each for darwin/linux and amd64/arm64. The independently
downloaded darwin arm64 archive SHA-256 was
`82c91b87bafef6b55064bfa474fce80e135cc6caffd70a6aade98ecca7aa9ed4`,
matching GitHub's asset digest. Mini fetched the full-SHA release into its
owner-only immutable release root. Neither preflight changed its active release.

The first preflight used the persistent managed `platform-upgrade` and its
adjacent manifest helper, with `NORN_DRAIN_MODE=fail`, the exact source SHA,
and authenticated environment loaded in-process. It stopped at strict local
manifest verification: `release binary set differs from required runtime
files`. The persistent helper expects nine older binaries; the newly signed
release contains twelve, adding `norn-effect-runner`,
`norn-ingress-observer`, and `norn-ingress-publisher`. No permissions or release
files were changed to bypass this check.

The command shape was identical for both attempts, changing only `--script`:

```text
NORN_DRAIN_MODE=fail norn platform env -- norn platform preflight \
  8a291142677a7b00cc8606e61ac0a6952fff3e5c \
  --repo /Users/0xadb/projects/norn \
  --script /Users/0xadb/.config/norn/host/bin/platform-upgrade
```

The second preflight used the privately staged release tooling from signed
source `47eb704445d793c64e9f3f289cd82de9a6caee95`, while still targeting
the new `8a291142…` release. Its script SHA-256 was
`44328171aba2e52a99cd0d91d313fc9424b480ba77aaa35ad6b52c7109efc6f6`;
its manifest-helper SHA-256 was
`95a94343a08909d540b6f2d7022a49e2db4498cb4a45fe4a2633024517c44a72`.
Both staged files matched the corresponding source blobs byte-for-byte. The
run reported `verified release 8a291142… (signed)` twice, then attempted a
passive candidate API on `127.0.0.1:18800`. Candidate startup reported:

```text
--script /Users/0xadb/.local/state/norn/m5-transition-47eb704-20261001/platform-upgrade
```

```text
schema: schema metadata is absent: migration ledger and compatibility row do not exist
[platform] error: candidate API did not become healthy
```

The follow-up authenticated inventory at `2026-10-01T04:12:02Z` reported
`/api/health` OK, host status `ok`, 29 apps, 46 services, zero active
operations, `fleet_configured=false`, and production readiness `blocked`.
There was no listener on `127.0.0.1:18800` after preflight. This proves a
clean stop and preserved observed live state, not schema migration, signed
promotion, rollback, or Fleet fitness. M5 requires the reviewed private
legacy-to-v3 transition, fresh backup and restore proof, drain, signed
promotion, smoke, host assurance, and rollback/re-promotion in a maintenance
window.
