# M8 release evidence ledger

Status: exact-current evidence checkpoint collected 2026-10-02. This ledger
does **not** sign M8, promote a candidate, or prove a live Fleet. The
machine-readable index is
[`m8-release-evidence-ledger.json`](m8-release-evidence-ledger.json).

## Version and capability matrix

| Surface | Exact version | Evidence class | Capability established | Boundary / open gate |
| --- | --- | --- | --- | --- |
| Norn signed candidate | `847c81741dd5b095d6833368ee8bb72dea6873d9`; tag `platform-847c81741dd5b095d6833368ee8bb72dea6873d9` | Immutable [release run 36936156456](https://github.com/antiartificial/norn/actions/runs/36936156456) and independent [2026-10-02 asset receipt](m8-platform-asset-verification-2026-10-02.md) | The receipt downloaded all 16 assets, matched each GitHub SHA-256 digest, verified each manifest's exact commit/tag and archive/SBOM digests, and passed all four Ed25519 manifest signatures. A subsequent read-only check matched Mini's public trust anchor and `require-signed` policy. | The receipt does not import/preflight or promote this candidate on Mini, exercise an upgrade/rollback, run protected Fleet plan/apply, or qualify a Fleet. |
| Norn protected source | `847c81741dd5b095d6833368ee8bb72dea6873d9` | Protected `master`, exact immutable release target | The release target and protected source are the same full commit. | A protected source and immutable release do not prove Mini promotion or a live Fleet. |
| Mini live runtime | API `v2.20.0-platform-30-ga5da8ef`; source identity `a5da8ef15d12e9eca7561e90b90d96f6dc652a21` | Read-only [2026-10-02 Mini observation](m8-mini-live-readback-2026-10-02.md) | Health/doctor/smoke passed; 46 services, zero active operations, and 20/20 recent deploys passed. | Installed binary/manifest digest was not rehashed. Mini is not running `847c817`; production readiness remains blocked separately. Upgrade, rollback, and post-upgrade preservation remain open. |
| Fleet protected source | `5ead17eb7574e2c3336220ad99e56eef68715f26` | Exact-main [validation 37071072390](https://github.com/antiartificial/norn-fleet/actions/runs/37071072390) and hosted [pre-release qualification 37071170456](https://github.com/antiartificial/norn-fleet/actions/runs/37071170456) passed for this commit. The owner-only `pilot261002d` packet retains the qualification receipt and complete precreate inventories. | Protected source and provider-free qualification are available for planning. No runtime pins or installed Fleet were proved. |
| Fleet live runtime | no version observed | Unobserved | Complete precreate inventories in the `pilot261002d` HOLD packet found zero resources matching this run; unrelated account resources exist. | Precreate zero is not post-create provider, backend-state, or installed-state proof. Protected plan/apply, exact installed versions, three-member etcd health, protected attempt/reconciliation, soak/fault, and retirement evidence remain open. |
| NornUI | proposed source `3252851ec74647e73a8d84935101694d6606de3a` in [PR #20](https://github.com/antiartificial/NornUI/pull/20), based on protected main `742445c491cd58c3e8992dab2ccafb212d938fab`; no published/installed version recorded | Open PR with successful exact-head [macOS build and unit tests 36827216694](https://github.com/antiartificial/NornUI/actions/runs/36827216694) and a separate [private development-signed Release-build receipt](m8-nornui-release-build-2026-10-01.md) | The source-derived signed-8a fixture decodes. An opt-in local test also passed the real Norn capabilities handler and bearer middleware response through the actual Swift `NornClient` over loopback, including wrong-bearer rejection. Release controls require the complete advertised feature/route contract, old event streams remain supported, and same-profile Fleet capability withdrawal clears gated caches. The local build passed 237 tests with zero failures and one standalone opt-in skip; the separate cross-client run executed and passed that opt-in test. A clean exact-source arm64 Release app passed strict signature and private receipt verification. The earlier `b59afc6` app launched in fixture mode showing sample data. | PR is unmerged and no published or installed NornUI release exists. The Apple Development signature and local HMAC receipt are pilot evidence only. Loopback capability negotiation does not cover all endpoints or live Mini/Fleet behavior; real etcd Fleet client behavior and M8 sign-off remain open. |

The 2026-10-02 authenticated [Mini readback](m8-mini-live-readback-2026-10-02.md) reports the legacy API identity
above, passing API health, host doctor, and smoke checks; 46 services; zero
active operations; and 20/20 recent deploys passed. Production readiness is
still blocked separately. The earlier signed private-copy shadow is historical
M5 rehearsal evidence for `8a291142`, not qualification of `847c817`.

## Open PR and pilot audit — 2026-10-02

This is a source-review snapshot, captured after the checkpoint. Strict branch
protection requires the named status contexts and linear history, but no
approvals at these bases. Passing PR checks establish only the checked source
at its listed head.

| Surface | Head and current check state | Release boundary |
| --- | --- | --- |
| [Norn #98](https://github.com/antiartificial/norn/pull/98) | Before this refresh, the local worktree was at unpushed `d0f4fdea30f2e2034e12d51cb3b804551ca38336`; the remote head was `8834682ebe0cb71037af05e0d7b9a19421875191`, behind protected `master`. This refresh had no checks at its new head at the time of observation. | Neither local review work nor prior green CI promotes `847c817` on Mini or creates a Fleet. |
| [Norn #102](https://github.com/antiartificial/norn/pull/102) | Open stacked review against `codex/m6-m7-cutover-recovery` at `8365f00e39df0a5b59ac9ab97f31c6caa09db509`; exact-head required checks passed after the bounded etcd leader-election wait was exercised in the Fleet pilot workload job. | It is not against protected `master`; green stacked CI is not cutover, Fleet, or release evidence. |
| [Fleet #209](https://github.com/antiartificial/norn-fleet/pull/209) | Open against protected `main` at `a20d5f8c260e919f088b4fe351fd6189fd52df94`; GitHub currently reports it behind `main`. Its earlier `contract` passed at this head. | A local drain qualification PR does not establish a plan, protected runner attempt, provider inventory, or installed Fleet. |
| [NornUI #20](https://github.com/antiartificial/NornUI/pull/20) | Open against protected `main` at `3252851ec74647e73a8d84935101694d6606de3a`; GitHub reported a clean merge state at this observation. Required macOS build and unit-test check passed. | It is the development-signed build source, not a published or installed NornUI release. |

`pilot261001a` is retired. Its $10 incremental ceiling and four-hour lifespan
were specific to that run and have expired. Management precreate reached
partial provider infrastructure, then a reviewed abort removed it. A
2026-10-01T17:24Z handoff recorded all 13 exact management provider IDs absent,
both scoped Spaces key IDs absent, both state backends deleted, 14 Tailscale
auth keys revoked/invalid, and zero pilot devices. Fleet itself was never
created, and production was not switched. The temporary Tailscale tag-owner
delegation was subsequently restored to the original owners. The status of
the two historical OAuth clients is not reasserted by this checkpoint. The
[pilot precreate receipt](fleet-pilot-approval-preflight-2026-10-01.md)
is historical authorization evidence, not approval for another run or live
Fleet fitness evidence. `pilot261002d` is an offline HOLD packet: complete
precreate provider readbacks record zero resources matching that exact run;
unrelated account resources exist, and no billable pilot resources were created.
It is not a protected plan, provider apply, installed Fleet, or M8 sign-off.

A read-only 2026-10-01T17:43Z price/availability refresh found NYC3
`s-2vcpu-4gb` at $0.03571/hour and `s-1vcpu-2gb` at $0.01786/hour in the
DigitalOcean sizes API; the database options API offered NYC3 two-node
PostgreSQL and MySQL using `db-s-2vcpu-4gb`. DigitalOcean's
[managed database table](https://www.digitalocean.com/pricing/managed-databases)
lists that node at $0.09063/hour, and [regional load balancers](https://www.digitalocean.com/products/load-balancers)
start at $12/month. For the prior paired shape (five larger droplets, two
smaller droplets, six database nodes and one load balancer), that is about
$0.776/hour or $3.10 for four hours before a possible $5 monthly Spaces base
charge and usage. This is a planning estimate, not a new approval or saved
provider plan; a future run must bind fresh quotes and its own cost ceiling.

## Proposed post-checkpoint qualification changes

These changes are reviewable branches, not protected-source or installed-state
evidence. [Norn #101](https://github.com/antiartificial/norn/pull/101)
at `4cb0c209dbcfba42dc1287d926e889c1202c8c60` records the real signed-staging
capabilities handler output as a canonical corpus and requires all three
release features plus all five release routes in the web client. Local Go,
Vitest and web-build checks passed. [NornUI #22](https://github.com/antiartificial/NornUI/pull/22)
at `4ea9dfbfe707e627e005a44cb14f2d4f9954112c` is stacked on #20; it
replaces the manually reconstructed fixture with the same canonical bytes,
pins their SHA-256, and tests the actual Swift request, bearer header,
decoding and missing-route/feature behavior. Its local 235-test suite passed.
Both proposed sources must be reconciled to their final protected commits
before claiming cross-client release parity.
Their exact open-PR CI checks passed on 2026-10-01; those checks do not
replace a published NornUI artifact or live Mini/Fleet client exercise.

[NornUI #21](https://github.com/antiartificial/NornUI/pull/21) at
`44386772555c60c4d3aacf13e83e9826a9a1ce2e` adds a protected-main-only
Developer ID signing and Apple notarization workflow. The
`release-qualification` environment now requires `antiartificial` review and
protected branches and has the Apple team variable, but no signing secrets.
Its credentialed job has not run; no notarized NornUI artifact exists.
The PR's ordinary macOS build and unit-test check passed; it does not run
the protected signing workflow.

[Norn #100](https://github.com/antiartificial/norn/pull/100) at
`7eae814515b7a897161351ec832ba92459753f81` is stacked on the M6 signed
phase-proof contract in #96. It adds an offline FinalSync-to-Activate
coordinator and crash/lost-response reconciliation tests, but the PostgreSQL
and etcd claimed journal adapters still refuse activation. No real Fleet
external authority, consumer switch, provider effect, traffic cutover or
Mini-to-Fleet mobility was exercised.
Its exact open-PR CI checks passed; this is local coordinator qualification,
not a live activation receipt.

## Pinned inputs at this checkpoint

The Norn source gates pin Go `1.26.6`, Node `24.19.0`, pnpm `10.32.1`,
`etcdctl` `3.5.17` with archive SHA-256
`eff6ac621d41711085d0f38fab17d8fa3705f6326c3ff11301a1f5a71fc94edd`, and
the etcd image digest
`sha256:a055da833a7c013b836ed0822e8ec1f99b059658be255ad8d0fcd31b635ae3d6`.
Fleet exact-main validation pins OpenTofu `1.10.6` for normal environments and
`1.12.6` for disposable pilots. These are build and qualification inputs; they
do not identify the versions installed on an uncreated Fleet. The current
protected Fleet source requires inputs to pin the Norn archive and digest, NornUI bundle and
digest, Nomad and Consul package versions, and etcd/etcdctl/etcdutl sources and
digests. No reviewed plan has supplied those values, so the ledger records
them as unresolved rather than borrowing versions from local CI.

Run `python3 v2/scripts/lint-m8-release-evidence.py` after changing this ledger
or the pinned Norn CI inputs. The offline linter checks the exact open-gate
vocabulary, semantic boundaries, hash shapes and current Norn workflow pins.
It does not authenticate GitHub runs, release assets, or live observations.

## Gates still missing

M8 has an immutable signed candidate at the current protected source with
independently verified published release assets, but remains open pending exact
NornUI release/build and remaining client parity, a private Mini signed
upgrade/rollback rehearsal followed by live preservation evidence, the M6
application-database cutover and recovery gate, the M7 complete Mini-to-Fleet
application mobility rehearsal, and a fresh provider-backed Fleet
qualification. The Fleet evidence must bind exact
installed Norn/Fleet/upstream versions to protected plan/apply, three-member
etcd health, independent app databases, client behavior, soak/fault recovery,
request/error/latency results, operation/effect reconciliation, retention
growth/restore, retirement and named operator sign-off.
M9 adoption remains a separate decision.
