# Norn v3 launch state

## Mini direct v3 upgrade completed (2026-11-07)

The Mini has completed its controller-mediated direct upgrade as an independent
node. It is not a Fleet member. The DigitalOcean deployment remains the only
Fleet in this work and is a lower-resource staging testbed intended to resemble
production.

- The exact signed target is `bba290db041e9f497c7161543c679d9d348b742d`,
  published from protected `master` in [release run 42408397039](https://github.com/antiartificial/norn/actions/runs/42408397039)
  after full qualification run [42408083511](https://github.com/antiartificial/norn/actions/runs/42408083511).
- Current source `e117d5645f7764ac6ba6bc316aa19fa241521655` was verified as a
  signed descendant of the retained adoption source. A fresh protected backup
  and exact-target signed shadow passed before transition.
- The controller created generation
  `gen-ac83feb81071c9f0e096d9df96d53983`, transition
  `mini-v3-20261107-bba290db-r1`. Its journal is `candidate-promoted`, and its
  signed preservation receipt reports `preservationMatched=true`.
- The migration completed at schema 51 with reader floor 5 and writer floor
  33. API health is `ok`, the runtime mutation fence is inactive, and the API
  serves the exact target SHA. Platform smoke passed with zero active Mini
  operations; host doctor/status report Nomad, Consul, API, host agent and
  cloudflared up.
- Seven queued `fleet.github.apply-dispatch` receipts were preserved through
  the Mini-only drain. They are not Fleet membership or evidence of running
  provider mutations.
- A PID-reuse race interrupted host-agent fencing and required a distinct
  signed repair controller from `d4dc88e0c37003a1013caed444983b2eeb50fbcb`
  (published in [release run 42410387388](https://github.com/antiartificial/norn/actions/runs/42410387388)).
  The target candidate and its evidence remained bound to `bba290d` throughout
  repair. The regression fix is PR [#298](https://github.com/antiartificial/norn/pull/298).

This proves the Mini direct adoption and a controller-recovered schema-compatible
forward transition. It does not prove uninterrupted API availability, a
general online migration protocol, rollback across future incompatible schema
changes, production admission, or the separate DigitalOcean Fleet gates. The
older dated Mini sections below are historical snapshots and do not describe
the current runtime.

## Fresh Mini inventory (2026-11-07 21:07 UTC)

Authenticated read-only inventory reconfirmed source
`bba290db041e9f497c7161543c679d9d348b742d`
(`v3.0.0-rc.1-platform-251-gbba290db`), API/host status `ok`, 30 app records
and 46 service records. Mini is not a DO Fleet member. Its API does report
`fleet_configured=true`: [`loadFleetInventory`](../../v2/api/handler/fleet.go#L116)
defines that flag as a Fleet config path being set, and the loaded document is
the separate DigitalOcean staging target `norn-disposable-nyc3` in `nyc3`, with
`control` and `ingress` pools. This is
target configuration on the Mini control plane, not Mini membership or proof
of live provider state.

The API still reports seven queued `fleet.github.apply-dispatch` operations
dated 2026-10-08 through 2026-10-10. They are durable dispatch receipts, not
running provider mutations or Mini workloads; classify them before any new
dispatch. The Mini has 25 open incident groups (22 latest-critical and 3
latest-warning), and its production-readiness endpoint is blocked by the
compatibility development profile and single-node substrate. These findings
do not invalidate the successful v3 API/host postflight, but host `ok` alone
does not mean every application is healthy or that Mini is production-ready.
A read-only `norn events reconcile --dry-run` at 21:01 UTC scanned 200 recent
events: 80 matched deterministic reconciliation rules and 120 still need
review. It acknowledged nothing; the incident groups remain open until a
separate evidence-backed reconciliation.

## Post-M8 protected evidence (2026-11-07)

The protected work-staging release-state readback
([run 42345172234](https://github.com/antiartificial/norn-fleet/actions/runs/42345172234))
passed at 06:03 UTC and verified the retained signed state on all three control
nodes. A read-only application catalog readback
([run 42341905690](https://github.com/antiartificial/norn-fleet/actions/runs/42341905690))
passed at 05:27 UTC: all three controls selected the same immutable catalog and
Norn remained active. This proves readback only; no promotion or rollback
evidence is established by that run.

Grafana observability credentials passed protected lifecycle checks, but the
two latest signed rollout attempts failed on `control-01` while confirming
etcd health after service changes. The newest
[rollout run 42348418095](https://github.com/antiartificial/norn-fleet/actions/runs/42348418095)
stopped after 20 retries; its secret-safe diagnostic returned
`unclassified-etcdctl-error` (exit 1). The exact etcd failure is therefore
unknown; do not retry or continue the serial rollout until a protected,
secret-safe diagnosis identifies the cause and fresh cluster-health evidence
is available. The catalog readback above does not prove observability ingest.

The later protected DO staging Genesis readback
([run 42398114376](https://github.com/antiartificial/norn-fleet/actions/runs/42398114376))
stopped in its local runner preflight before contacting the management host:
`ansible-inventory` was absent from `PATH`. It did not change Fleet state. The
follow-up fix in Fleet PR [#937](https://github.com/antiartificial/norn-fleet/pull/937)
adds the pinned runner Ansible virtualenv to `GITHUB_PATH` and passed contract
validation ([run 42399790472](https://github.com/antiartificial/norn-fleet/actions/runs/42399790472)),
but the repository owner closed that PR without merging it; protected `main`
remains at `22f0d58`. A new readback also needs fresh one-use runner
authorization and a valid owner-signed controller intent. Do not treat either
the failed run or the unmerged fix as Genesis readback evidence.

Replacement Norn Fleet PR [#938](https://github.com/antiartificial/norn-fleet/pull/938)
is open at `9402e83d34503bcaad9de7d7035dcf3d9f65d97d` against that `main` and
contains only the pinned Ansible path fix. Its protected validation run
[42413105058](https://github.com/antiartificial/norn-fleet/actions/runs/42413105058)
passed at 21:04 UTC. PR #938 remains open and unmerged. No Fleet readback has
been redispatched. The prior owner-signed intent expired at 18:03:26 UTC and
its run is terminal; a fresh intent and one-use authorization will be needed
after the workflow fix is reviewed and merged.

## Mini in-place upgrade checkpoint (historical observation, 2026-11-07)

PR [#289](https://github.com/antiartificial/norn/pull/289) and PR
[#290](https://github.com/antiartificial/norn/pull/290) are merged; the latter
is `e8c8ddd37f6d0db5d411ec0e3ddd7ab98114abee`, published as a signed prerelease
in [run 42358665477](https://github.com/antiartificial/norn/actions/runs/42358665477)
and imported on the Mini. The active pointer still resolves to
`3602e7e3d5fe5c0ef333b4041580d48ec15b24c5`; API health is `ok`, schema is 49,
and writer floor is 32. The candidate requires schema 51 and writer floor 33.

The prior recovered-forward transition from `3da50fe…` to `3602e7e…` has now
been finalized and verified with its exact signed controller. The original
adoption receipt was retained by digest; a successor receipt is now bound to
the running `3602e7e…` source. A fresh protected backup and exact-candidate
signed shadow for `e8c8ddd…` passed: 65 tables and 398,805 rows matched, both
migration passes reached schema 51, passive health passed, and the source was
read-only. Seven queued `fleet.github.apply-dispatch` receipts remain
preserved.

Ordinary upgrade correctly refused the writer-floor change because it cannot
prove all writers are stopped. The recovery suite exposed two generation
continuity cases: a prepared predecessor must remain supersedable without a
receipt, and an already promoted generation may need a signed-descendant
catch-up when the running source advanced later. The local fix retains old
signed journals and receipts, binds catch-up to both externally verified
release signatures and Git ancestry, and rejects unrelated or unverified
sources. Focused tests now pass: 15 transition, 22 generation preparation, and
13 resolver tests. At the time of this observation, the change was not merged
or released, and no migration or candidate promotion had occurred. See
the dated [Mini observation](m6-mini-v3-forward-upgrade-observation-2026-11-07.md)
and [online-upgrade capability proposal](online-upgrade-lessons-2026-11-07.md).

## Fleet running-upgrade checkpoint (2026-11-02 00:10 UTC)

The first protected staging canary release remains live on public HTTPS. A second protected release from source `be5ca5d969ccf3509e62ce04a833516bb5c204db` built and attested image `ghcr.io/antiartificial/norn-fleet-canary@sha256:33265daecc36215776c2029b7e0cd16463570e559c28678d3a94e249bd43ba52`, but [run 41364735627](https://github.com/antiartificial/norn/actions/runs/41364735627) failed during deployment completion. Its new Nomad job has two healthy allocations on app-03 and app-04. The previous job also has two healthy allocations, and the public `/version` route still serves the first release source `182b719bb8f5b84f8c7272e715f83553a00acb4d`. The deployment operation `6d96db37-a54e-4231-b1b8-e3e616beb3ab` is failed with manual recovery required; its durable Nomad effect says completed. The one-use runner was disabled and unregistered.

The proposed [held Fleet deployment recovery procedure](fleet-held-deployment-recovery.md) describes the checks and operator endpoint; it requires the reviewed recovery code to be deployed first.

The immediate release blocker is the route handoff for a second generation: the current initial route path rejects an already active route after creating the healthy replacement job. A fenced N+1 handoff and recovery of this failed operation are being implemented and tested. Keep both jobs for evidence and availability until a verified route switch and separate old-job retirement. Do not count the second release as deployed. The earlier replica scale and app-node drain proofs remain valid; running upgrade, rollback, loaded node-count contraction, HA fault/restore, soak, and production promotion remain open. No reliable production release date is established.

## Fleet staging canary checkpoint (2026-11-01 23:49 UTC)

The first protected Fleet canary release completed in [run 41359827250](https://github.com/antiartificial/norn/actions/runs/41359827250)
from source `182b719bb8f5b84f8c7272e715f83553a00acb4d`. Norn accepted the
signed, attested, content-addressed image
`ghcr.io/antiartificial/norn-fleet-canary@sha256:1321f05a19f2e3af0bd3bd2df4b6c2c0878d0d38d0866a3d98f7f9f15b321f63`.
Deployment `1584a4b1-03c8-43be-86ef-b00b943431a4` and operation
`ff94b712-d5d6-4933-9b19-2a228db98c9f` finished successfully. Nomad
reported two healthy, running `web` allocations on distinct ready app-pool
nodes `app-01` and `app-02`. Public HTTPS returned 200 for `/route-proof` with
the signed body digest
`75790fc8d9898d5a12787ac491b6f471619cde0efc851be9f26db608b6fbb37c`
and for `/version` with the released source SHA; TLS validation passed. The
one-use private runner stopped, consumed its authorization, and unregistered.

Norn then accepted an explicit `web` scale from two to three
(operation `2ae52865-3834-49bb-9f56-6c7bc9b3f03b`) and back to two
(operation `4e9b6a39-e8a9-4f5e-a0e2-5825f8e34cc3`). Both operations
finished `succeeded`. At three, the scale-status API reported three desired,
running and healthy allocations on distinct ready app nodes `app-01`,
`app-02` and `app-04`; 20 public `/version` requests reached all three
allocation IDs with the released source SHA. After downscale, scale-status
reported two desired, running and healthy allocations, and 12 public requests
reached both remaining allocation IDs. The public `/route-proof` body still
matched the signed digest during the three-replica phase.

A bounded app-node drain exercise then moved the live `web` allocation off
`app-01` to `app-03`, and a second drain moved the `app-02` allocation to
`app-04`. Each drain was disabled afterward; all four app nodes returned to
ready with no drain strategy. During the second transition, 180 consecutive
public HTTPS `/version` requests succeeded through `curl` certificate
validation and returned the released source SHA. Requests reached the old
`app-02` allocation and both replacement allocations. Final Norn scale-status
reported desired, placed, running and healthy counts of two. The public
`/route-proof` body still matched the signed digest. This proves scheduler
replacement and sampled public continuity during sequential drains; it does
not prove node retirement, fault recovery, a sustained load budget or Norn's
Fleet generation executor.

This is a staging release and live two-host workload proof. It advances the
fresh-Fleet and direct-deploy slices of M3, M4 and M7; it does not complete
their full gates. The live 2→3→2 **replica** scale cycle and bounded app-node
drain are proven, but loaded node-count 2→3→2 replacement, worker drain,
leader failover during
route mutation, running upgrade, rollback, whole-node restore, soak, and
production promotion remain unproved. The ingress publishers currently name
the elected control03 address `10.43.0.2` for route authority. An API leader
change requires authority reconciliation before another route publication;
the current successful route remains served. The Mini transition described
below is independent of this Fleet checkpoint.

## Mini adoption evidence checkpoint (historical, 2026-11-01 05:33 UTC)

Pinned-address SSH and authenticated loopback inventory verify that the Mini has
completed its guarded PostgreSQL v2-to-v3 adoption as an independent
development Norn. The active API reports exact source
`3602e7e3d5fe5c0ef333b4041580d48ec15b24c5` and version
`v3.0.0-rc.1-platform-34-g3602e7e3`; `~/norn/current` resolves to that immutable
release. The original `com.norn.api` LaunchAgent is running through the managed
launcher in active/check mode. Health is `ok`, the runtime mutation fence is
inactive, and Consul, Nomad, PostgreSQL, S3 and SOPS report up. The selected
recovered-forward generation remains recorded as `candidate-promoted`, with its
preservation receipt retained. Current inventory found 30 app records, 46
services and host status `ok`.

This confirms the Mini live-adoption portion of M9 and provides transition
evidence for M5; M5's copied-environment rehearsal gate remains a separate
assessment. It does not qualify a repeat without operator
intervention, a current-`master` v3-to-v3 refresh, production admission, Fleet
etcd or HA, or full M9 adoption. The completed transition required bounded
operator intervention to correct the owner-only release-key path, fill one
exact preservation digest under the promotion lock, and temporarily accommodate
host-agent startup timing; those facts remain part of the evidence rather than
an autonomous-repeat claim. The Mini checkout and current `master` may advance
independently of the immutable runtime source, so neither is runtime provenance.

The DigitalOcean staging Fleet is the only Fleet in this work: a separate
testbed intended to resemble production with lower resource use. Mini is not a
member. The Mini API's configured DO target and queued Fleet dispatch receipts
are control-plane records, not evidence that Mini is a Fleet node or that
provider work is running. The DO testbed retains its own gates for directly
deployed representative workloads and data, placement
and loaded node-count 2-to-3-to-2 scaling with safe drain, running-version upgrade under
traffic, whole-node fault/restore and HA, external admission, soak, and
operational handoff. Classify the seven queued dispatch receipts before any
new Fleet dispatch.
Production readiness is still blocked. No Mini transition should be replayed or
made dependent on the separate Fleet work.

## Operating lanes and API retirement boundary (historical pre-upgrade snapshot, 2026-11-07)

The release has distinct execution lanes across independent environments:
(1) Direct Mini for its independent, individually controlled node, (2) Fleet
bootstrap for the DigitalOcean staging testbed with the default two
application node pools, (3) Fleet upgrade for an existing Fleet with workloads,
and (4) capacity changes as a separate, workload-aware operation. The direct
Mini upgrade did not require Fleet signing authority or Fleet connectivity,
and still used a verified signed release, protected backup and shadow evidence,
controller-mediated fencing, health checks, and a recoverable journal. Mini's
control API may hold configuration for the separate DO staging target; that
does not make Mini a Fleet member. Fleet bootstrap is not the Fleet
running-upgrade gate; online rollout should preserve availability where proven,
with a controlled offline window as an explicit fallback.

The Mini's adoption and post-upgrade verification are now complete. A source
review found no `/api/v2` route namespace: the production router exposes the
current `/api/v1` contract and older unversioned `/api` handlers. The `v2/api`
Go import path is the repository's package layout, not evidence of an HTTP API
version. Tests mentioning “legacy”, “V2”, or “Postgres” therefore cannot be
removed by name alone; many protect durable state recovery, signed Fleet
protocol revisions, or Postgres conformance. Before retiring any old
unversioned handler, trace supported callers and replace its behavioral
coverage at the current contract boundary. Keep migration/recovery and
rollback-read compatibility coverage until the corresponding stored state is
no longer supported. No test deletion is justified by the route/name search
alone.

This is a correction to the pre-upgrade terminology in this historical
snapshot. PR [#295](https://github.com/antiartificial/norn/pull/295) merged the
Mini generation catch-up changes as `3f51e35ad246c68a835977d1f1e3b8fdf0a8511c`.
The signed prerelease for `0701add87b34d0de7ead5031579b3d74cd829bd7` was
published in [run 42406296055](https://github.com/antiartificial/norn/actions/runs/42406296055).
The generation catch-up logic preserves older signed journals/receipts and
binds a completed generation to the exact current source through external
signature verification and Git ancestry; it does not authorize migration or
candidate promotion by itself.

Fresh authenticated Mini inventory on 2026-11-07 reports source
`e117d5645f7764ac6ba6bc316aa19fa241521655`, version
`v3.0.0-rc.1-platform-248-ge117d564`, API health `ok`, and
`runtimeMutationFence=inactive`. `~/norn/current` resolves to that source. The
active recovered-forward generation is `gen-1d647aa65bac5b2b5cdda675fdb8cb85`,
transition `mini-v3-20261107-38fcec83`, candidate
`38fcec83380c7ba989d46e11db6cc449510fad86`; its journal says
`candidate-promoted`. Git confirms that candidate is an ancestor of the running
source. This confirms a healthy V3 runtime and an earlier promoted candidate;
it does not prove the pending e117-based schema migration. The signed
`0701add8…` release was published, but Mini preflight stopped before candidate
preparation because its legacy adoption guard accepts only the original
adoption source or exact journaled candidate. A
follow-up fix allows evidence preparation and recovered-forward control only
after both source releases verify externally and Git proves ancestry; ordinary
upgrade remains gated on the completed recovered-forward journal. That fix is
still unreleased. No candidate import, database migration, or candidate
promotion was verified in this checkpoint.

This checkpoint supersedes older present-tense Mini source, schema, and
generation statements below. Preserve those sections as historical evidence.

## Track boundaries (2026-10-11)

The v3 launch has two independent environments. **Mini is an independent,
single-node Norn installation and is not part of a Fleet.** It retains its
unmanaged PostgreSQL, and its startup and upgrade do not require Fleet
connectivity, Fleet etcd, or Fleet management. Separately, the only Fleet in
this work is the DigitalOcean staging testbed, intended to resemble production
with lower resource use. Its bootstrap and upgrade qualification are distinct
Fleet gates, not Mini prerequisites.

The Mini track is: publish and verify the exact signed platform release, take
and verify a fresh Mini-local recovery copy, run the protected Mini
transition/upgrade, then verify API version,
health, database access, preserved applications, and host assurance. Fleet
deployment, etcd quorum, ingress, workload/scale testing, and teardown remain
separate Fleet acceptance milestones; none is a prerequisite for upgrading the
Mini.

As of 2026-10-11, the Mini drain corrections in PRs
[#137](https://github.com/antiartificial/norn/pull/137) and
[#138](https://github.com/antiartificial/norn/pull/138) are merged. The signed
rehearsal correction in PR
[#140](https://github.com/antiartificial/norn/pull/140) is also merged as
`a059f2e5b42e8df30c3fe4460cbd6455a0ea4722`. Its immutable signed prerelease is
[`platform-a059f2e5b42e8df30c3fe4460cbd6455a0ea4722`](https://github.com/antiartificial/norn/releases/tag/platform-a059f2e5b42e8df30c3fe4460cbd6455a0ea4722).
The Mini imported and signature-verified that exact release. A fresh Mini-local
backup and a signed private-database/LaunchAgent shadow rehearsal passed. The
live upgrade then stopped before migration or promotion: the candidate raises
the PostgreSQL writer floor from 31 to 32, and the ordinary restart lane cannot
prove that every old writer is fenced. A prior transition is recorded as
`candidate-postflight-failed`; its signed adoption receipt supports forward
work but does not authorize replay of the old one-way procedure or prove live
writer quiescence. Mini remains on its prior signed release and schema 47.
The next Mini-specific engineering gate is a separately journaled,
interruption-tested recovered-forward transition that proves all writers are
quiesced before migration and only moves forward. Fleet deployment, Fleet
management nodes, and Fleet etcd are not prerequisites for this Mini update.

Historical architecture note: Mini is the independent single-node development
platform and uses its existing unmanaged PostgreSQL service. It does not host
Fleet nodes or require Fleet management nodes or an etcd quorum. Earlier
proposals considered making Mini a Fleet member; that is not the current
topology. The DigitalOcean Fleet is a separate staging testbed. The Mini's
current API target configuration for DO is recorded separately above.

At 2026-10-11 19:42 UTC, protected signed release
`798c82847c04156d99a9876c0bb0d6d4b76f901b` was published and independently
verified; it is a v3.0.0-rc.1 prerelease, not a stable v3.0.0 release. Direct
SSH and loopback API checks reconfirmed Mini still serves
`26147c39a554b73b6761a371edee5a9591c81d3f` (`v2.20.0-platform-72-g26147c39`),
schema 47/writer floor 31, health `ok`, and single-node PostgreSQL/Consul/Nomad.
No candidate was promoted.

The failed transition evidence binds historical candidate
`e7301f779041cd22f1872090323d3b8ed5c4611b`, while the current signed runtime is
`26147c39a554b73b6761a371edee5a9591c81d3f`. Its reconciliation records
`matchesBefore=false`; that is an explicit loss of proof for the old baseline,
not evidence that current data should be discarded. The local correction now
models these as separate identities (`sourceSHA` and `incidentCandidateSHA`),
retains the v1 receipt and all historical files, and captures a fresh current
Mini workload digest for the new forward transition. It still requires a fresh
verified database backup and exact-candidate signed shadow restore before any
live migration. This correction is not yet merged or released, and no live
Mini state has been changed.

Observed 2026-10-11. This is the single current handoff for the
pragmatic deployment objective. The [milestone contract](execution-milestones.md)
defines the exit criteria; percentages below are engineering estimates, not
owner sign-offs. Recheck external state before any new mutation.

## Latest Mini transition checkpoint (2026-10-11 21:00 UTC)

The Mini is being upgraded as its own single-node Norn instance. It uses its
existing unmanaged PostgreSQL and does not need Fleet nodes, Fleet management
nodes, or Fleet etcd. Fleet availability is not an upgrade prerequisite. Mini
is not a Fleet member; its control API's configured DO staging target is
separate from the Mini host and its workload topology.

Norn PR [#144](https://github.com/antiartificial/norn/pull/144) merged as
`85fb2830ac18a711b2e3e20de45c1adcbbd4935d`; the protected signed prerelease
[`platform-85fb2830ac18a711b2e3e20de45c1adcbbd4935d`](https://github.com/antiartificial/norn/releases/tag/platform-85fb2830ac18a711b2e3e20de45c1adcbbd4935d)
was published for that exact SHA. The Mini imported and verified it. A fresh
local backup and exact-candidate shadow rehearsal passed. The live forward
transition `mini-v3-20261011-85fb2830` is paused in `legacy-fenced`: both
`com.norn.api` and `com.norn.host-agent` are fenced with exit 78, `current`
still points to `26147c39a554b73b6761a371edee5a9591c81d3f`, and PostgreSQL
remains at schema 47. No v3 migration or candidate promotion has occurred.

The immediate gate is seven queued `fleet.github.apply-dispatch` receipts for
pilot `pilot261006c`. Their bound GitHub apply runs are terminal (five failed,
two cancelled); the `Apply verified plan` step is skipped for each. The
migration quiescence contract currently blocks on every nonterminal operation,
while Mini's own preflight explicitly preserves these queued Fleet dispatch
receipts because they do not mutate Mini. The fix is to align those contracts
narrowly: allow only queued `fleet.github.apply-dispatch` receipts to remain
unchanged through migration, while continuing to block every other active
operation and any additional database client. Do not edit operation rows
directly, bypass the guard, or alter the journal's candidate binding. Then
resume the same signed transition through a supported guarded candidate
retarget path, if its journal and schema checks still validate. Mini's
independent upgrade must not be held for provisioning or starting a Fleet
cluster.

## Current decision

**Norn v3 is not deployed on the DigitalOcean staging Fleet** (status at this
historical checkpoint). Mini's single-node upgrade is independent of Fleet
deployment. Mini was fenced during its single-node upgrade at that time;
it is not serving its normal API until the recovered-forward transition
completes. Its development profile and preserved applications do not qualify
Fleet or complete the release. A Fleet attempt remains a separate acceptance run and
requires fresh credentials, a repaired partial-apply/recovery lane, and a
complete workload and failure proof. Do not reuse `pilot261004b` state, runner,
keys or approvals.

At 21:29 UTC, Norn PR [#123](https://github.com/antiartificial/norn/pull/123)
merged to protected `master` as `1ca28934034c48f28aed4b97b6d4971d69b642e3`.
Norn CI [37376169196](https://github.com/antiartificial/norn/actions/runs/37376169196),
Repository CI [37376169212](https://github.com/antiartificial/norn/actions/runs/37376169212),
and docs deployment [37376169180](https://github.com/antiartificial/norn/actions/runs/37376169180)
all succeeded on that exact SHA. The Norn observer/status controller is now in
protected master; this is not a published platform release or live Fleet
deployment.

At 21:34 UTC, Fleet PR [#258](https://github.com/antiartificial/norn-fleet/pull/258)
merged as `e4756190670aa3a8afa6d3cfdc6344a531dd4376`. The staging doctor now
accepts the verified GitHub squash-commit identity. It still reports three
missing input groups. `staging-plan` is missing `ADMIN_CIDRS_JSON`,
`DIGITALOCEAN_TOKEN`, and `STATE_ACCESS_KEY_ID`, `STATE_BUCKET`,
`STATE_ENDPOINT`, `STATE_REGION`, and `STATE_SECRET_ACCESS_KEY`. `staging` is
missing `DIGITALOCEAN_TOKEN`, `NORN_API_URL`, and the same five state-store
values. The protected runner configuration is missing
`NORN_FLEET_BOOTSTRAP_VARS_FILE`, `NORN_FLEET_KNOWN_HOSTS_FILE`,
`NORN_FLEET_PRIVATE_INTERFACE`, `NORN_FLEET_SSH_PRIVATE_KEY_FILE`, and
`NORN_FLEET_SSH_USER`. No provider operation has been run from staging.

At 00:10 UTC, the protected `platform-release` environment secret
`NORN_RELEASE_IMMUTABILITY_CHECK_TOKEN` was updated after the release-check PAT
was regenerated. At 00:12 UTC, workflow run
[37392793546](https://github.com/antiartificial/norn/actions/runs/37392793546)
was dispatched for exact merged SHA
`1ca28934034c48f28aed4b97b6d4971d69b642e3`. Authorization, pinned UI build,
and all four Linux/macOS amd64/arm64 bundle jobs passed. The `publish` job is
waiting for review in the protected `platform-release` environment; no
immutable release has been published yet.

During browser inspection, the newly generated release-check PAT value appeared
in an accessibility snapshot. Treat it as exposed and revoke/replace it before
approving publication. The environment secret must then be refreshed with the
replacement. The running publish job may fail if it reaches the immutability
check after revocation; rerun the same exact SHA after rotation if needed. No
token value is recorded here.

The current staging doctor still reports missing staging-plan secrets
`ADMIN_CIDRS_JSON`, `DIGITALOCEAN_TOKEN`, `STATE_ACCESS_KEY_ID`, `STATE_BUCKET`,
`STATE_ENDPOINT`, `STATE_REGION`, and `STATE_SECRET_ACCESS_KEY`; staging is
missing `DIGITALOCEAN_TOKEN`, `NORN_API_URL`, and the five `STATE_*` values.
Protected runner variables remain missing:
`NORN_FLEET_BOOTSTRAP_VARS_FILE`, `NORN_FLEET_KNOWN_HOSTS_FILE`,
`NORN_FLEET_PRIVATE_INTERFACE`, `NORN_FLEET_SSH_PRIVATE_KEY_FILE`, and
`NORN_FLEET_SSH_USER`. No provider operation has been run from staging.

At 00:22 UTC, the newest authenticated Mini inventory reports source
`26147c39a554b73b6761a371edee5a9591c81d3f`
(`v2.20.0-platform-72-g26147c39`), host `ok`, Fleet unconfigured with zero
pools, and production readiness blocked (6 pass, 19 fail, 1 warning). It found
28 apps, 45 services, zero active operations, and 25 active incidents. Evidence
is at
`/var/folders/c8/d4bdjwzn2qgfmg3hqz7qj7ch0000gn/T/norn-inventory-20261006T002155Z`.
The merged Norn SHA has not been released or promoted to Mini.

At 19:34 UTC, a fresh authenticated Mini inventory again reported host status
`ok`, but `fleet_configured=false`, zero node pools, and production readiness
`blocked`. It found 28 apps, 45 services, zero active operations, and 25 active
incidents. The detailed safe inventory is at
`/tmp/norn-inventory-20261005T1934Z`; this confirms the Mini is online on its
v3 API source but does not establish Fleet deployment readiness.

At 20:01 UTC, a new authenticated read-only Mini inventory again reported
source commit `26147c39a554b73b6761a371edee5a9591c81d3f`
(`v2.20.0-platform-72-g26147c39`), host status `ok`, Fleet
`configured=false` with zero node pools, and production readiness `blocked`.
It observed 28 app entries, 45 service entries, zero active operations, and
25 incidents. Local observation files are at
`/var/folders/c8/d4bdjwzn2qgfmg3hqz7qj7ch0000gn/T/norn-inventory-20261005T200109Z`.
This confirms the Mini v3 API is online; it does not show a Fleet deployment.

At 20:30 UTC, a fresh authenticated read-only inventory remained unchanged:
source `26147c39a554b73b6761a371edee5a9591c81d3f`, host `ok`, Fleet
unconfigured with zero pools, and production readiness blocked (6 pass, 19
fail, 1 warn). It reported 28 app entries, 45 service entries, zero active
operations, and 25 incidents. Evidence is at
`/var/folders/c8/d4bdjwzn2qgfmg3hqz7qj7ch0000gn/T/norn-inventory-20261005T203015Z`.

At 20:11 UTC, the next authenticated read-only Mini inventory was unchanged:
source `26147c39a554b73b6761a371edee5a9591c81d3f`, host `ok`, Fleet
unconfigured with zero pools, and production readiness blocked (6 pass, 19
fail, 1 warn). It again reported 28 app entries, 45 services, zero active
operations, and 25 active incidents. Evidence is in
`/var/folders/c8/d4bdjwzn2qgfmg3hqz7qj7ch0000gn/T/norn-inventory-20261005T201102Z`.

At 21:01 UTC, focused local tests for the separate disposable-retirement
controller worktree passed (78 tests across the coordinator, delegation,
deadline proposal, root retirement, backend cleanup, and manifest builder).
These use injected test adapters and fixtures. The worktree still contains
uncommitted changes based on Fleet `main` `5210307`; the controller has no real
executor/Spaces adapters, fresh deadline-time fence approval producer or
renewal, or restart supervisor. Its runbook still classifies deadline
retirement as not implemented and unrehearsed. This work was independent of the
Norn observer-controller integration and remains a prerequisite to safely
running another billable disposable Fleet pilot.

At 17:42 UTC, Fleet PR #256 merged to `main` as `5210307`. Merged-main plan
`37350374522` and contract validation `37350374566` both succeeded. The change
binds resumable paired backend cleanup to the exact locked run and adds a
validation-only deadline descriptor checker. It does not implement or qualify
an unattended deadline controller. Fleet PR #255 remains merged at `abb0b00`;
its merged-main validation (`37336272528`) and planning (`37336272298`) passed.
Preceding commit `1fcaeb7` passed
contract validation run `37343349906`, including the full Python unittest
suite, local retirement qualification, workflow validation and OpenTofu checks.
Commit `abaabc4` clarifies retirement CLI help text; validation run
`37344577776` passed the same contract suite at 17:01 UTC. Commit `d4eee3e`
adds a regression test proving an already-started backend cleanup can resume
after its short approval expires, while a new cleanup cannot start after expiry.
Full validation run `37346380888` passed at 17:15:58 UTC, including the full
Python unittest suite, local retirement qualification, workflow checks,
OpenTofu validation and disposable pilot validation. Review then found a race
where a replaced cleanup scope could select another run after the shared lock
was acquired. Commit `3f41503` binds the reopened scope and canonical ledger path
to the locked run; its focused regression test and full validation run
`37347927438` passed. PR #256 also
corrects the disposable README's stale `pilot261004b`
label to the actual `pilot261005a` budget-gate ID, adds a non-executable
descriptor checker, and makes an already-authorized backend cleanup retryable
after its approval expires. It records deadline-controller design requirements
but does not provide a scheduler or make a pilot safe to start. The Fleet
account has no
matching `pilot261005a` or `pilot261005c` droplets, databases, load balancers,
projects, VPCs, volumes, firewalls, reserved IPs, snapshots, or runner labels at
16:20 UTC; this is still not Spaces bucket-zero proof. Norn launch-state PR #120
was a documentation-only handoff and merged to protected `master` at 17:31 UTC
as `d67b8bf01346ef8e54f25e1119c7ffe532e05e1d`. Its `Repository CI` run
`37348527855` and `Norn CI` run `37348527835` both passed, including the required
API/CLI, Fleet pilot workload, OpenAPI, workflow lint, Web UI, M4 durable replica,
and release-bundle checks. These workflows had been manually disabled; both are
now active. This verifies the current Norn branch gates, not a Fleet deployment.
Fleet issuer PR #210 is conflicting
with main; lifecycle PR #175 has a failed contract check. Neither is a current
deployment gate result.

The approved `pilot261004b` window was $20 maximum, four hours active plus one
hour for teardown, ending **2026-10-05 08:30 UTC**. The protected
[apply run 37273763546](https://github.com/antiartificial/norn-fleet/actions/runs/37273763546)
at Fleet `main` `ca52148d69eecd70a843938b72c2bc882a066c5d` stopped during provider
creation at 06:50:30 UTC with exit 143. The runner was online and idle after
termination; the signal sender is unproven. Norn attempt
`eb349686-c1ac-439d-b7b6-cf1fc2443d9f` was later marked abandoned after its
heartbeat expired. The remote Fleet state persisted at serial 3, lineage
`ffe289b7-a711-4ca6-b84d-7887c67f4d5c`, with five droplets, load balancer,
VPC, firewalls, project and tags. It did **not** contain either managed database,
although DigitalOcean had created both. No bootstrap, public ingress, etcd
quorum, application workload or scale proof followed. Automatic
[recovery run 37274493490](https://github.com/antiartificial/norn-fleet/actions/runs/37274493490)
failed at source-run name validation; the repository recovery inventory would
also hold on the two provider-only databases. Plain apply replay was unsafe.

At 13:47 UTC, well after the teardown deadline, a fresh DigitalOcean inventory
still showed seven pilot droplets, three managed databases, the Fleet load
balancer, four firewalls, two VPCs and two projects. This was an **overrun
incident**. Exact-ID emergency deletion began after preserving private state
and provider evidence. By 13:58 UTC, fresh DigitalOcean list calls showed zero
`pilot261004b` droplets, databases, load balancers, firewalls, VPCs, projects,
tags, volumes, snapshots, reserved IPs and SSH keys. Both pilot Spaces state
buckets, every retained object version and both scoped Spaces keys were removed;
a new full-access Spaces inventory key found zero pilot buckets and was revoked.
The exact GitHub pilot runner and the staging secrets/variables created for
this pilot were removed. DNS A lookup for
`pilot-pilot261004b.betabung.com` returned no address. These are technical
absence observations after **emergency unprotected cleanup**, not a signed
protected final-zero receipt. The final pilot-specific DigitalOcean bill is not
yet available. The account's month-to-date balance cannot be attributed to
this pilot, so compliance with the $20 cap remains unproven.

## Controller and executor state

There are three distinct controller/executor workstreams; none alone proves
an autonomous bounded pilot or a live deployment:

* The Norn Fleet observer/controller implementation began on
  `feature/v3-fleet-controller` over the weekend and was integrated by PR #123
  into protected master on Oct 5. It is merged, tested in post-merge CI, and
  present in source; it derives and stores Fleet target, observation and status.
  It does not provision provider infrastructure. Release publication, Mini
  promotion and Fleet deployment remain separate gates.
* Fleet executor PR [#257](https://github.com/antiartificial/norn-fleet/pull/257)
  remains a draft on `feature/fleet-controller-executor` at `64cb7b0`, based on
  Fleet main `5210307`; its contract check passed. This is separate newer work,
  not the weekend Norn observer/controller integration. The PR explicitly
  supports only the legacy PostgreSQL no-drain lifecycle, refuses v3/etcd before
  provider calls, and does not ship the Norn executor API route. It is not a v3
  deployment path and must not be installed on a v3/etcd staging root. No
  provider-backed E2E was run.
* The checkout `norn-fleet-deadline-retirement` is still on merged Fleet
  `main` commit `5210307` with uncommitted edits, not on an independent commit
  branch. Those edits add delegated owner authority/journaling at existing
  root/backend retirement boundaries and a post-create root approval producer.
  New `scripts/disposable_retirement_controller.py` provides a local-only
  persisted coordinator with injected stage adapters, separate controller
  locking, HMAC/hash-chained intents, proof checks, restart recovery hooks, and
  T0+5 breach-and-continue behavior. The full Fleet Python unittest suite
  passed at 20:06 UTC after the latest coordinator proof-validation changes:
  1,643 tests, four skipped, in 258.317 seconds. The earlier coordinator run
  had 1,641 tests, four skipped, in 278.771 seconds. This suite includes
  existing executor and fixture tests; it does not qualify a live provider
  adapter, unattended restartable orchestration, or deployment. Subsequent
  local changes now enforce the full root-zero and
  backend-cleanup receipt field sets, digest shapes, delegated run/root scope,
  independent provider-observation hashes, bucket bindings, and prior-stage
  receipt digests; 11 focused tests, `py_compile` and `git diff --check` pass.
  This is structural binding only: the coordinator still does not reopen and
  validate retained state exports, executor ledgers, or external provider
  evidence. The existing per-stage CLI executors do not yet have an adapter or
  launchd supervisor. The complete
  1,641 tests, four skipped, in 278.771 seconds on 2026-10-05. Both Fleet and
  launch-state `git diff --check` pass. It has no real
  root/backend command, approval, fence-renewal, or Spaces adapter and no CLI or
  launchd supervisor, and has not passed sleep/reboot/partial-apply rehearsal.

The three workstreams cover separate boundaries (Norn desired-state
observation, executor-host execution, and disposable-pilot retirement). They
need an explicit integration/release path; do not infer they compose safely
because their local tests pass.

After merged Fleet PR #256, a separate local Fleet checkout
`norn-fleet-deadline-retirement` on branch
`codex/executable-deadline-controller` contains new, unmerged work. The
`scripts/disposable_retirement_delegation.py` module validates an owner-HMAC
run delegation and maintains an owner-only hash-chained retirement journal.
The journal records and fsyncs a T0+5 breach marker before later cleanup can
continue. Both the root executor and paired-backend `apply` now have opt-in
delegation checks at their existing shared-lock mutation boundaries, with
account, budget, activation, source, evidence, lock, state/root, provider-scope,
and ordering bindings. New `disposable_deadline_approval.py` and manifest
builder support derive a short-lived HMAC approval from the owner delegation
and fresh post-create root output, state, provider scope, retention, fence
approval digest, and MySQL receipt. The root executor verifies that approval
against the exact signed delegation. Focused tests after these edits pass: 8
manifest-builder, 26 root-retirement, 19 backend-cleanup, 11 delegation/journal,
  and two deadline-proposal tests. The earlier Python suite passed 1,632 tests
  with four skipped in 300.372 seconds at 19:14 UTC, before the coordinator
  module was added. The subsequent full run including the coordinator also
  passed: 1,641 tests with four skipped in 278.771 seconds. A refreshed Mini
  inventory at 19:34 UTC again reports Fleet unconfigured and production
  readiness blocked; the controller suite pass does not alter the deployment
  gate.
  The deadline
proposal validator accepts the complete delegation CLI
bundle but remains validation-only. There is still no fully wired end-to-end
controller, fresh fence-approval producer/renewal, restart supervisor, or
sleep/reboot/partial-apply rehearsal. Do not use this branch to authorize or
start a billable pilot.

The two offline `pilot261004b` Tailscale management records (100.120.209.89
and 100.122.147.29) were removed after action-time user confirmation; the
console then showed 14 machines and no `pilot261004b` machines. The five
one-use Fleet auth keys are invalidated. The broad OAuth client that issued
them was previously revoked; a replacement scoped client was requested but
not generated. The `pilot261004b` DigitalOcean API token was revoked through
DigitalOcean’s token revocation endpoint, and a follow-up account request
returned HTTP 401. Its local token file was removed after a hash-only receipt
was saved. Technical external cleanup is now observed, but the final pilot
bill and a signed protected final-zero receipt remain unavailable.

**Reusable DigitalOcean capability:** The authenticated `doctl` context and
DigitalOcean API can eliminate repeated dashboard sign-ins for supported
account, resource, and token-management operations. An already-issued pilot
personal access token can revoke itself through DigitalOcean’s API, so cleanup
of that token does not require a second dashboard sign-in when its local token
file is still available. For `pilot261004b`, `POST` to DigitalOcean’s
[OAuth revocation endpoint](https://docs.digitalocean.com/reference/api/oauth/)
with that token as both Bearer authorization and the `token` form parameter
returned HTTP 200; a subsequent `/v2/account` call with the same token returned
401. This can eliminate a dashboard sign-in specifically for revoking an
existing token; it does not create a token, restore expired access, or prove
provider resources are gone. Record only a hash and response statuses, then
remove the local file. DigitalOcean documents the endpoint for OAuth tokens;
support for a dashboard-issued personal access token is a live observation to
recheck before relying on it in another pilot.

At 17:12 UTC, the signed-in local `doctl` default context authenticated
successfully (a fresh `doctl account get`) to
the intended `theartificial@hotmail.com` account (UUID
`05fe610a42b6d50c65344dd87bd842ce35ab036d`). Supported DigitalOcean API
inspection and operations can use this existing CLI session without a new
dashboard sign-in, including later token revocation when the active CLI context
has the required account authority. It does not supply fresh run-scoped
credentials or approval, and `doctl` cannot enumerate Spaces buckets in this
workflow; retain the separate Spaces API/S3 inventory for that proof. Read-only CLI checks found no
`pilot261005a` name matches among droplets, databases, load balancers, projects,
or VPCs. At 17:44 UTC, corrected read-only `doctl` name checks also found no
`pilot261004b`, `pilot261005a`, or `pilot261005c` matches among droplets,
databases, load balancers, firewalls, VPCs, or projects. These name checks are
not complete provider-zero evidence and did not inspect Spaces buckets. A
fresh read-only check at 18:14 UTC again authenticated to the intended account
and found no `pilot261005a` or `pilot261005c` matches in droplets, databases,
load balancers, firewalls, VPCs, or projects. This is still only a name-collision
check; volumes, snapshots, reserved IPs, SSH keys, project resources, DNS, and
Spaces buckets were not checked in this observation. No provider mutation was
made. A
plain Mini `norn fleet pools` CLI request at 17:45 returned HTTP 401
`authenticated_principal_required`; the documented owner-host SOPS inventory
path then authenticated successfully at 17:50. It confirmed Fleet
`configured=false`, zero node pools and zero plans. No Fleet plan or provider
mutation was made in this turn.

**Operational follow-up:** Use the authenticated `doctl` context first for
supported DigitalOcean account, resource, and token operations. This avoids
re-entering the dashboard for routine API-capable tasks and may allow a known
active token to be revoked through the API during cleanup. Use the dashboard
only for actions that require interactive UI authentication or are not exposed
by the API. Before a future pilot, verify the selected context/account and
required scopes with a read-only request; this convenience does not replace a
fresh run-scoped credential, bounded approval, or a separate Spaces inventory.

## Mini and source

On 2026-10-05 at 17:50 UTC, a fresh authenticated owner-host inventory showed
the Mini loopback API healthy on source `26147c39a554b73b6761a371edee5a9591c81d3f`,
displayed as `v2.20.0-platform-72-g26147c39`. The platform version label's
source includes v3 work. Authenticated production-readiness returned
**blocked**: 6 of 26 checks
passed, 19 failed, and 1 warned. Current failures include the development
profile and compatibility auth, single-member Nomad/Consul, disabled ACLs and
incomplete TLS, local control database without PITR/replica, application
deployment provenance, snapshot/restore posture, and missing recovery drills.
The host status is `ok`; this does not override production readiness.

At 19:15 UTC, authenticated owner-host inventory was repeated using the
Norn-platform inventory script. It again reported healthy Mini API source
`26147c39a554b73b6761a371edee5a9591c81d3f` (`v2.20.0-platform-72-g26147c39`),
host status `ok`, Fleet `configured=false`, zero node pools, zero plans, and
production readiness **blocked** with 6 pass, 19 fail, and 1 warn. It observed
28 app entries, 45 service entries (18 passing, 27 unknown), zero active
operations, and 25 active incidents. Snapshot-retention warnings remain for
field-harbor and turnkey-offer-intake; mail-mcp and signal-sideband still need
secret attention. This read-only inventory made no provider or control-plane
mutation. The local observation files are in
`/var/folders/c8/d4bdjwzn2qgfmg3hqz7qj7ch0000gn/T/norn-inventory-20261005T191507Z`.

At 20:38 UTC, a new authenticated owner-host inventory again found source
`26147c39a554b73b6761a371edee5a9591c81d3f`, host status `ok`, Fleet
`configured=false` with zero pools, zero active operations, 25 active incidents,
and production readiness **blocked** at 6 pass, 19 fail, and 1 warn. It listed
28 app entries and 45 services. The snapshot is in
`/var/folders/c8/d4bdjwzn2qgfmg3hqz7qj7ch0000gn/T/norn-inventory-20261005T203815Z`.

The authenticated `doctl` context still resolves to the intended DigitalOcean
account. Fresh read-only name-filtered lists found no `pilot261004b`,
`pilot261005a`, `pilot261005c`, `norn-staging`, or `norn-management` matches in
Droplets, managed databases, load balancers, firewalls, VPCs, volumes,
snapshots, reserved IPs, or projects. `doctl` does not inventory Spaces buckets;
this is not complete account-zero evidence. Staging Fleet's offline
`scripts/sanity.py` validation passed. Its setup doctor still reports missing
staging provider/state secrets and runner configuration. The protected squash
metadata policy is now merged and passes staging doctor. The three remaining
groups are staging-plan secrets, staging secrets and protected runner
configuration. No cloud resource was created or changed.

The exact `pilot261004b` protected apply log confirms its pre-issued public
ingress TLS check passed before provider apply. The apply then remained in
managed database creation until exit 143 at 06:50:30 UTC. The signal sender is
unproven; do not attribute this interruption to the TLS gate.

These app and service counts include discovered and inactive entries; they do
not prove identity preservation through the earlier one-way transition or
health for every app.

Norn [PR #115](https://github.com/antiartificial/norn/pull/115) merged the bridge
title correction as `c56fa0c`. The signed
[platform-c56fa0c release](https://github.com/antiartificial/norn/releases/tag/platform-c56fa0cbef322d44abf905a423bea72df26292b2)
contains four verified bundles. Publication is not Mini promotion or Fleet
fitness. The local `codex/v3-release-integration` branch remains unmerged and
must not be treated as protected release authority. The main Norn checkout has
unrelated local changes that must be preserved.

## Pilot lineage

| Pilot | Most recent useful evidence | Disposition |
| --- | --- | --- |
| `pilot261003c` / `pilot261003d` | `c` and `d` exposed earlier gaps in provider evidence, TLS handoff and emergency cleanup. The `d` Fleet apply stopped before provider mutation; management was retired by emergency unprotected cleanup. | Historical failure evidence, not launch proof. Old `c`/`d` access items were retired in the later owner observation. |
| `pilot261004a` | Exact-SAN TLS and replacement GitHub App key supported complete management handoff and protected Fleet planning. Its apply stopped at the pre-provider retained-bucket inventory gate. Management and both backends were retired before its approved deadline. | Clean cloud/backend abort; two Tailscale device records and replacement OAuth client were still pending at the last observation. Recheck them separately. |
| `pilot261004b` | Protected plan `37273380169`, Norn plan `a25b25fa-c0b0-47e0-b9ec-836ce6cde3ed`, and apply `37273763546` reached live provider creation. State missed both databases. | Fleet never bootstrapped; emergency technical teardown occurred after the 08:30 UTC deadline. Technical access cleanup is observed; billing closure remains open. |

Pilot `a` was the first complete management-to-Fleet handoff, but `b` was the
most recent and exposed the material partial-apply failure. A fresh pilot
should be based on the repaired contracts, not resumed from `a` or `b`.

`pilot261005a` is a **proposed** fresh run ID, not an approved or active pilot.
At 16:20 UTC on 2026-10-05, read-only checks against the intended
`theartificial@hotmail.com` account found no matching droplets, databases,
projects, load balancers or VPCs; no registered GitHub runner carries its Fleet
label. The broader name checks also found no matching c-suffixed droplet,
database, project, load balancer, VPC, volume, firewall, reserved-IP or snapshot
names. These are name-collision checks, not a full provider-zero or
Spaces-bucket proof. The canonical owner-only `pilot261005a` directory, its
budget contract/activation files, and its DigitalOcean token file do not exist;
staging aggregate-budget variables still point to the retired `b` paths and
digest. Fleet [PR #255](https://github.com/antiartificial/norn-fleet/pull/255)
merged the signed c budget, plan, apply, recovery and exact topology gates as
`abb0b00`; it rejects reuse of retired `pilot261004b`. The PR contract check
and merge-main plan and validation passed (`37335410031`, `37336272298`,
`37336272528`). This is code validation, not live proof. Read-only checks at
16:20 found no c-named DigitalOcean resources in the categories above and no
registered c runner. The independent deadline retirement path remains
unimplemented. Existing approvals require post-create state and expire within
an hour, so signed budget expiry alone cannot guarantee teardown. The existing
owner-Mac retire-by watchdog performs only a local finalizer; it has no
provider, GitHub, Spaces or token-exchange capability. Fleet PR #256 contains a
validation-only descriptor checker, not a controller. Its runbook records that
root destroy plans must be built after resources exist. Fleet PR #256 commit
`1fcaeb7` adds a same-lock, ledgered backend cleanup start: a retry can
continue after the short approval expires only if the first start event bound
the complete exact authority while that approval was live. Full contract
validation passed on both `1fcaeb7` and `abaabc4`. This does not authorize a
new start after expiry or provide
Fleet/management deadline-time approvals. A read-only workflow audit confirmed
no existing workflow can mint post-create retirement approvals or execute the
ordered retirement chain. There is no installed or wired
deadline-retirement executable. The separate branch's coordinator and
delegation/journal primitives alone do not close this gate. Do not start a new
billable run until a
restartable, run-scoped owner-Mac controller
can fence the runner, perform Fleet → management → backend cleanup automatically
from T0+4, retain each stage's receipts, and continue retrying through and beyond
T0+5 until final zero. A timer alone is not a hard spend cap when the owner host
or provider access is unavailable.

At 19:20 UTC, the authenticated `doctl` context again identified the intended
DigitalOcean account. Name-filtered lists contained no `pilot261004b`,
`pilot261005a`, or `pilot261005c` matches: 2 droplets, 0 managed databases,
0 load balancers, 0 firewalls, 6 VPCs, 1 volume, 2 snapshots, 0 reserved IPs,
and 3 projects were listed. This is a scoped resource-name collision check;
Spaces buckets/object versions, DNS, GitHub runners, Tailscale devices, and
provider resources without these names were not proven absent. No mutation was
made.

## Milestone estimates

No M0–M9 gate has recorded owner sign-off. These rounded estimates measure work
against each full exit criterion; they are not schedule forecasts or an overall
launch percentage. The `b` pilot adds real provider-create evidence but no
running Fleet qualification.

| Gate | Estimated complete | Finding and next proof |
| --- | ---: | --- |
| M0 baseline | 55% | Mini source and service counts observed; historical before/after identity equality and current owner baseline remain. |
| M1 control semantics | 65% | Signed v3 code runs on Mini; protected cross-process and downstream-effect recovery proof remains. |
| M2 profiles and retention | 50% | Mini uses development/PostgreSQL; separate-node restore, provider retention and legacy binding review remain. |
| M3 etcd Fleet | 45% | Local three-member rehearsals and live five-node provider creation exist; no bootstrapped etcd membership, quorum, fault/restore, alarm, rotation or soak proof. |
| M4 capacity and ingress | 40% | Local admission and two-ingress fixtures exist; no live route, placement, loaded 2→3→2 or drain proof. |
| M5 isolated Mac rehearsal | 55% | Controller-managed host preparation and signed preflight evidence exist; the exact latest guest-create attempt ended ambiguous, and a complete isolated guest rehearsal is not proven. The user-selected Mini upgrade is a separate M6 path. |
| M6 online upgrades and app DB | 35% | Mini direct v3 adoption is complete and journal/preservation/host assurance passed. Continuous API availability, rollback under an incompatible schema change, Fleet running upgrades and application DB lifecycle remain unqualified. |
| M7 mobility | 30% | Isolated Mini fixture web/worker/tick and private restore comparison passed. No Fleet target or complete move exists. |
| M8 release qualification | Complete for soak only | Protected current-main audit completed the 72h35m soak with 4,254 hash-chained samples and zero alarms, gaps or invalid observations. Other release gates, promotion evidence and owner sign-off remain open. |
| M9 adoption | 20% | Mini's independent direct v3 upgrade and postflight are complete. The DigitalOcean staging Fleet remains a separate testbed with incomplete Fleet qualification; controlled workload adoption and production promotion remain open. |

## Next actions

1. Measure and qualify low-interruption upgrade behavior. The direct Mini
   transition used a brief API/host-agent fence; do not describe it as
   uninterrupted service. Extend the controller's durable phase recovery and
   outage measurement before promising online upgrades.
2. Keep schema migration controller-owned and writer-fenced. Mini is now at
   migration 51 with writer floor 33; preserve that compatibility invariant.
   Qualify any bridge needed by a future target from its actual source schema
   rather than weakening the floor.
3. Add migration compatibility lint and bounded expand/backfill/contract
   semantics. Stage the candidate passively, implement owner/traffic handoff
   and phase-aware recovery, and measure API outage, write pause and job delay.
4. Keep the completed Mini transition as the direct-lane recovery example:
   retain its exact backup, shadow, journal, preservation receipt and signed
   repair-controller evidence. Full Tart IPSW restore was not required for this
   database/platform path; resolve the ambiguous guest-create outcome before
   any Tart retry or cleanup.
5. Continue DO staging Fleet work independently: obtain fresh protected state
   and review host-key, inventory, catalog, artifact and continuity evidence
   before its readback/promotion/rollback gates; finish Grafana Cloud observability
   and its separate 1–2 hour telemetry qualification; then complete M5's
   isolated Mac rehearsal through protected executor workflows and one-use
   runners. Refresh M0–M4/M7/M9 evidence before claiming full v3 readiness.

## Evidence locations

- Owner-only `pilot261004b` handoff, raw Fleet and management state snapshots,
  two complete pre-cleanup inventories, run logs and exact-ID deletion records:
  `/Users/arti/.config/norn/fleet-next-full-e2e/successor-pilot261004b/fleet-protected-handoff/`.
- Protected [Fleet apply](https://github.com/antiartificial/norn-fleet/actions/runs/37273763546)
  and [automatic recovery](https://github.com/antiartificial/norn-fleet/actions/runs/37274493490).
- Exact dry-run incident planner and tests in isolated Fleet checkout
  `/Users/arti/.config/norn/fleet-next-full-e2e/worktrees/incident-fleet-orphan-cleanup`
  at `eb7db92e5cb7f136472c5f43c2c4a8e5c9d4bed0`. It was not the executor
  used for the later emergency teardown and does not provide protected proof.
- `pilot261004a` abort and backend receipts under
  `/Users/arti/.config/norn/fleet-next-full-e2e/successor-pilot261004b/` and
  older pilot evidence under `/Users/arti/.config/norn/fleet-next-full-e2e/`.

The final invoice remains independent of the current technical zero
observation. The emergency cleanup did not produce a protected final-zero
receipt.
