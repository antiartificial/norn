# M8 release evidence ledger

Status: exact-current evidence checkpoint collected 2026-10-01. This ledger
does **not** sign M8, promote a candidate, or prove a live Fleet. The
machine-readable index is
[`m8-release-evidence-ledger.json`](m8-release-evidence-ledger.json).

## Version and capability matrix

| Surface | Exact version | Evidence class | Capability established | Boundary / open gate |
| --- | --- | --- | --- | --- |
| Norn signed candidate | `8a291142677a7b00cc8606e61ac0a6952fff3e5c`; tag `platform-8a291142677a7b00cc8606e61ac0a6952fff3e5c` | Published [release run 36813207019](https://github.com/antiartificial/norn/actions/runs/36813207019), after protected environment approval | Four OS/architecture bundles passed and the published release has 16 assets: one archive, manifest, signature and SBOM per platform. All four bundles were independently downloaded, passed the pinned Ed25519 bundle verifier, and all 16 local asset sizes and SHA-256 digests matched the GitHub release whose target commit is exact `8a291142` ([dated asset receipt](m8-platform-asset-verification-2026-10-01.md)). The release API reported `immutable: true`. The Linux amd64 archive SHA-256 is `6b3032b935c6d4d736313409f0a4a440c5f8c58f8104fa31d72762b0fce3c76e`. Mini imported and verified the signed artifact using the staged current manifest helper; its [signed private-copy shadow](m5-mini-signed-shadow-2026-10-01.md) passed against real Mini data. | This is a signed candidate containing M4 durable-replica code, not the live runtime. Live promotion, protected plan/apply and Fleet qualification remain open. Ordinary passive Mini preflight stops on absent legacy schema metadata; the protected private-copy shadow path passed. |
| Norn protected source | `8a291142677a7b00cc8606e61ac0a6952fff3e5c` | Signed exact-master source; successful [Repository CI 36807641901](https://github.com/antiartificial/norn/actions/runs/36807641901), [Norn CI 36807641851](https://github.com/antiartificial/norn/actions/runs/36807641851), and [docs 36807641929](https://github.com/antiartificial/norn/actions/runs/36807641929) | The signed source includes the three-client durable-replica and etcd failure qualifications. | CI and publication do not prove Mini promotion or a live Fleet. |
| Mini live runtime | API `v2.20.0-platform-30-ga5da8ef`; source identity `a5da8ef15d12e9eca7561e90b90d96f6dc652a21` | Read-only [2026-09-30 baseline](m0-mini-baseline-refresh-2026-09-30.md) and [2026-10-01 signed preflight/post-state receipt](m5-mini-signed-preflight-2026-10-01.md) | Existing PostgreSQL control plane and workloads remain observable; host healthy, zero active operations, 29 apps and 46 services after the failed passive candidate check. | Installed binary/manifest digest was not rehashed. Mini reports `fleet_configured=false`; it is not running `8a291142`. The persistent managed manifest helper has an older binary allowlist; the exact-source private-copy shadow path passed while ordinary preflight still finds no live migration ledger or compatibility row. Signed upgrade, rollback and post-upgrade preservation remain open. |
| Fleet protected source / desired pilot | `39b7b3c817ae818014455eead12be4991dc39698` | Desired source plus successful exact-main [validate 36809166429](https://github.com/antiartificial/norn-fleet/actions/runs/36809166429) and credential-free [qualification 36809196735](https://github.com/antiartificial/norn-fleet/actions/runs/36809196735) | Static contracts and local three-client scheduling/drain/recovery qualification passed on exact main. | No provider resource, protected runner, or live Fleet was created by those runs. Desired source is not installed-state evidence. |
| Fleet live runtime | no version observed | Unobserved | The Mini readback reports zero Fleet node pools; that establishes only that the Mini is not configured for Fleet. | Complete provider, backend and installed-state inventory remains absent. Fresh protected plan/apply, exact installed versions, three-member etcd health, protected attempt/reconciliation, soak/fault and retirement evidence remain open. |
| NornUI | proposed source `63a0b5e9a884fc09d9a491d145b4620acfe7f84e` in [PR #20](https://github.com/antiartificial/NornUI/pull/20), based on protected main `742445c491cd58c3e8992dab2ccafb212d938fab`; no signed/installed version recorded | Open PR with successful exact-head [macOS build and unit tests 36821630818](https://github.com/antiartificial/NornUI/actions/runs/36821630818) and a separate [unsigned Release-build checkpoint](m8-nornui-release-build-2026-10-01.md) | A source-derived signed-8a capability fixture decodes; release controls require their complete advertised feature/route contract, old event streams remain supported, and same-profile Fleet capability withdrawal clears gated caches. Local built bundle passed 235/235 tests, and arm64 Release configuration compiled without signing. | PR is unmerged and no NornUI release or installed build exists. The fixture is configuration-shaped, not a captured live response. Local signing did not complete. Full signed-candidate endpoint parity and real etcd Fleet client behavior remain unverified, so M8 NornUI sign-off stays open. |

## Pinned inputs at this checkpoint

The Norn source gates pin Go `1.26.6`, Node `24.19.0`, pnpm `10.32.1`,
`etcdctl` `3.5.17` with archive SHA-256
`eff6ac621d41711085d0f38fab17d8fa3705f6326c3ff11301a1f5a71fc94edd`, and
the etcd image digest
`sha256:a055da833a7c013b836ed0822e8ec1f99b059658be255ad8d0fcd31b635ae3d6`.
Fleet exact-main validation pins OpenTofu `1.10.6` for normal environments and
`1.12.6` for disposable pilots. These are build and qualification inputs; they
do not identify the versions installed on an uncreated Fleet. Fleet `39b7b3c8`
requires protected inputs to pin the Norn archive and digest, NornUI bundle and
digest, Nomad and Consul package versions, and etcd/etcdctl/etcdutl sources and
digests. No reviewed plan has supplied those values, so the ledger records
them as unresolved rather than borrowing versions from local CI.

Run `python3 v2/scripts/lint-m8-release-evidence.py` after changing this ledger
or the pinned Norn CI inputs. The offline linter checks the exact open-gate
vocabulary, semantic boundaries, hash shapes and current Norn workflow pins.
It does not authenticate GitHub runs, release assets, or live observations.

## Gates still missing

M8 has a signed candidate at the intended current source but remains open
pending an exact NornUI release/build and remaining client parity, a private Mini signed
upgrade/rollback rehearsal followed by live preservation evidence, the M6
application-database cutover and recovery gate, the M7 complete Mini-to-Fleet
application mobility rehearsal, and a fresh provider-backed Fleet
qualification. The Fleet evidence must bind exact
installed Norn/Fleet/upstream versions to protected plan/apply, three-member
etcd health, independent app databases, client behavior, soak/fault recovery,
request/error/latency results, operation/effect reconciliation, retention
growth/restore, retirement and named operator sign-off.
M9 adoption remains a separate decision.
