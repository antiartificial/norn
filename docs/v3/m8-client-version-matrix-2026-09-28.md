# M8 release evidence ledger

Status: exact-current evidence checkpoint collected 2026-10-01. This ledger
does **not** sign M8, promote a candidate, or prove a live Fleet. The
machine-readable index is
[`m8-release-evidence-ledger.json`](m8-release-evidence-ledger.json).

## Version and capability matrix

| Surface | Exact version | Evidence class | Capability established | Boundary / open gate |
| --- | --- | --- | --- | --- |
| Norn signed candidate | `47eb704445d793c64e9f3f289cd82de9a6caee95`; tag `platform-47eb704445d793c64e9f3f289cd82de9a6caee95` | Published release with signed assets; release run [36802095059](https://github.com/antiartificial/norn/actions/runs/36802095059) | The release page exposed darwin/linux, amd64/arm64 archives with manifests, signatures and SBOMs. This checkpoint independently records only the darwin arm64 archive SHA-256: `78dab1cbe2888a3d59ba9d5a42c1948bf87c9748b02f0033804eaeef7c4e6aa7`. | This is the runtime candidate. It predates the later M4 and Fleet control-store CI additions on current source. Complete asset, signer and immutable-release receipts remain open; publication is not M8 sign-off or live qualification. |
| Norn protected source | `8a291142677a7b00cc8606e61ac0a6952fff3e5c` | CI-only source; successful exact-master [Repository CI 36807641901](https://github.com/antiartificial/norn/actions/runs/36807641901), [Norn CI 36807641851](https://github.com/antiartificial/norn/actions/runs/36807641851), and [docs 36807641929](https://github.com/antiartificial/norn/actions/runs/36807641929) | Source includes later three-client durable-replica and etcd failure qualifications. | No signed release or live runtime is identified at this SHA. CI must not be attributed to the `47eb704` candidate. |
| Mini live runtime | API `v2.20.0-platform-30-ga5da8ef`; source identity `a5da8ef15d12e9eca7561e90b90d96f6dc652a21` | Read-only live observation in [`m0-mini-baseline-refresh-2026-09-30.md`](m0-mini-baseline-refresh-2026-09-30.md) | Existing PostgreSQL control plane and workloads remain observable. | The installed binary/manifest digest was not rehashed. Mini reports `fleet_configured=false`; this is neither `47eb704` nor `8a291142`. Signed upgrade, rollback and post-upgrade preservation remain open. |
| Fleet protected source / desired pilot | `39b7b3c817ae818014455eead12be4991dc39698` | Desired source plus successful exact-main [validate 36809166429](https://github.com/antiartificial/norn-fleet/actions/runs/36809166429) and credential-free [qualification 36809196735](https://github.com/antiartificial/norn-fleet/actions/runs/36809196735) | Static contracts and local three-client scheduling/drain/recovery qualification passed on exact main. | No provider resource, protected runner, or live Fleet was created by those runs. Desired source is not installed-state evidence. |
| Fleet live runtime | no version observed | Unobserved | The Mini readback reports zero Fleet node pools; that establishes only that the Mini is not configured for Fleet. | Complete provider, backend and installed-state inventory remains absent. Fresh protected plan/apply, exact installed versions, three-member etcd health, protected attempt/reconciliation, soak/fault and retirement evidence remain open. |
| NornUI | last source observation `742445c491cd58c3e8992dab2ccafb212d938fab`; no signed/installed version recorded | Historical source observation only | Source review described `fleet-v1` capability handling and closed unsupported route behavior. | Compatibility with the signed candidate and a real etcd Fleet is unknown. NornUI is unsupported for M8 until an exact released or installed build completes decode/refresh and negative-capability checks. |

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

M8 remains open pending one coherent candidate that includes the intended
current source, an exact NornUI release/build, a private Mini signed
upgrade/rollback rehearsal followed by live preservation evidence, and a fresh
provider-backed Fleet qualification. The Fleet evidence must bind exact
installed Norn/Fleet/upstream versions to protected plan/apply, three-member
etcd health, independent app databases, client behavior, soak/fault recovery,
request/error/latency results, operation/effect reconciliation, retention
growth/restore, retirement and named operator sign-off.
M9 adoption remains a separate decision.
