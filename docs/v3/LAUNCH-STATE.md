# Norn v3 launch state

## Current DO Fleet status (2026-11-12 17:16 UTC)

The existing DigitalOcean staging Fleet is running the signed V3 platform at
source `fa301fa5f881b847a26f61a501092f9a9dd090fd`. The controller-mediated
all-stop upgrade completed on 2026-11-11; it verified the target release, one
healthy private API backed by etcd, and signed API/UI pins on all three
controls. The promotion interrupted service. Online platform upgrade remains
unqualified.

The explicit read-only `doctl --context work` account readback matched the
TurnKey Marketing work-staging account (UUID prefix `17f40050`). At 17:16 UTC
all eleven Norn staging Droplets were active: two management, three control,
two ingress, and four app nodes. Platform `/version` returned HTTP 200 in
0.266 seconds with allocation `de4645bf-25c0-93bd-124b-5e7f9c8cda05` and
version `af0f054358d9b9c7b68ea25132b52ee5eb099e12`. The separate canary's
`/route-proof` returned HTTP 200 in 0.256 seconds with body
`norn-fleet-canary`; `/version` returned HTTP 200 in 0.228 seconds with source
`be5ca5d969ccf3509e62ce04a833516bb5c204db`, node `515ddcea44b8`, and allocation
`6b4b9efa-ca9a-2dc6-3e0b-d7cc3e27bcf8`. These public point-in-time responses
do not prove allocation-to-host lineage, V3 signed admission, or continuous
health. The public-pilot verifier's 12/12 reads at 02:40:57 UTC remain the
latest recorded MySQL pilot check.

Fleet PR #1027 merged as `27d7b33f` and derives protected readback file and
receipt paths from `NORN_FLEET_SECRET_DIR`; runbook refresh PR #1028 merged as
`11e4611c`. A 17:25 names-only audit against current main compared the
workflow's referenced names with repository and `work-staging` environment
variable and secret names: six of seven referenced variables are present,
with only `NORN_WORKLOAD_READBACK_ENABLED` missing; all three referenced
state-backend secrets are present. Main's workflow file matches the audited
copy. The audit read no values and did not inspect protected files, their
modes or contents, signed digests, credential provenance, or runner access.
The enable gate is absent, so the protected workload-readback workflow remains
disabled. A query at 17:25 of the latest 30 Fleet workflow runs found no
workload-readback dispatch; this bounded recent-history check is not a
complete archive audit. The 16:26 candidate-specific query found failed
candidate-branch push
[43117232561](https://github.com/antiartificial/norn-fleet/actions/runs/43117232561)
with zero jobs. The responding public canary is not evidence of a signed
admission receipt. The
first V3-admission-qualified workload launch date remains uncommitted, with no
calendar forecast; the earlier November 16 target is withdrawn.

At 17:15 UTC, the repeatable local restart qualification verified that the
composed etcd-backed stage/route/drain test replays the exact terminal receipt
and succeeded operation after client/store reconstruction. Route and drain
effects use a test driver. This does not prove terminal replay from the
protected readback workflow or recovery through a deployed controller process.

The next DO gates are owner review and verification of the exact protected
inputs and ACL credential provenance, a signed workload/ACL/network readback
(GET-only evidence reads plus explicitly authorized, expiring, run-scoped
Nomad and Consul reader-token issuance), replay of the protected terminal
artifact after an actual controller-process restart, full-executor recovery
through real Nomad and ingress effects, admission review, and durable
same-intent receipts binding the responding canary. Workload resize and online
platform upgrade remain follow-on Fleet capabilities; neither is required for
the first workload. The Mini is an independent direct lane. Fresh-Fleet
Genesis/bootstrap is separate from this existing-Fleet update.

## Managed MySQL stateful staging candidate (2026-11-12 03:41 UTC)

The source InfraSpec declares `deploy: true` and the distinct hostname
`norn-fleet-mysql.tkmkt-dev.com`. Protected catalog promotion
[42312866244](https://github.com/antiartificial/norn-fleet/actions/runs/42312866244)
and three-control readback
[42341905690](https://github.com/antiartificial/norn-fleet/actions/runs/42341905690)
completed successfully. DNS resolves to `159.89.243.220`.

At 03:41 UTC, the hostname still presented the Traefik default TLS certificate.
The protected TLS workflow has no run, and
`NORN_MANAGED_MYSQL_STAGING_RELEASE_ENABLED` is
`false`, and the release workflow has no `workflow_dispatch` run. No
Norn-owned version A or B deployment or rollback has been qualified. The
catalog and DNS evidence do not prove app runtime. See the
[managed MySQL activation runbook](m6-managed-mysql-staging-catalog.md) and
[rollback runbook](m6-staging-app-rollback.md). This stateful candidate is
separate from the first stateless controller-admitted DO canary.

## Current Mini status (2026-11-10 15:26 UTC)

The independent Mini runs immutable signed release
`34178b8b90d52a2fa3f138ffae2dfc63744197e1`, schema 52. The direct controller
upgrade completed with no schema change. The Mini meets the owner-stated direct
success criterion: V3 is active, the local API health endpoint returned HTTP
200, and one Norn-managed workload responded. At 15:26 UTC, `vigil-gateway` was
running in Nomad and its `/health` endpoint returned HTTP 200. Read-only host
status showed the API, host agent, Nomad, Consul, supervisor, and Cloudflare
agent up; the bundled V3 CLI reported `api ok` and its required services up.

This verifies that the Mini can host and serve a workload after the V3 direct
upgrade. It does not qualify every app or the Git push-to-deploy path. The
inventory still has mixed app health and seven old queued Fleet dispatch
receipts. Three queued platform-upgrade attempts also stopped before
promotion; diagnose that recovery path separately. Those broader conditions do
not negate the direct Mini success under the stated criterion.

PR #401 merged at `34178b8b`; exact-source qualification run
[42809544343](https://github.com/antiartificial/norn/actions/runs/42809544343)
passed all 12 jobs, and protected release run
[42810147654](https://github.com/antiartificial/norn/actions/runs/42810147654)
published the immutable signed release with 16 assets. On Mini, exact-release
passive preflight passed. Its fresh protected backup and signed shadow passed:
68 tables, 404,527 rows, migration ledger 1–52, 66 application tables
fingerprinted, two migrate-only passes, passive health, and no source mutations.

Three queued upgrades stopped before promotion: two against `1b2dc229` and a
fresh operation against `34178b8b`. The last exact receipt identified
`LaunchAgent wrapper has no child process`; the operation did not promote the
release or run a migration. The Mini was subsequently upgraded through the
documented signed direct controller lane, but that success does not resolve the
queued-upgrade failure. The offline statechart now includes an absent-agent
sample that resets the stability window; TLC checked 56 states / 138 steps and
five invariants plus deadlock. The existing checked-in platform-upgrade
integration suite passes all 24 tests. Neither result explains or fixes the
operation-window disappearance. The current implementation still requires the
LaunchAgent wrapper's direct child. Do not replay terminal operation IDs.

Fresh read-only checks at 12:29–12:40 UTC confirmed API health `ok`, exact
source `34178b8b90d52a2fa3f138ffae2dfc63744197e1`, schema 52, reader/writer
floors 5/33, and managed host-agent bytes matching the active release. A new
durable `platform.smoke` operation (`ead999e5-b0ac-4de8-ac61-14989e7760d6`)
succeeded through the host agent; its receipt says `platform.smoke complete`.
The direct smoke reported health, operation drain, releases, events, and
platform all `ok` with `activeOps=0`. A fresh `norn host doctor/status` passed.
Three one-second process observations also found the same live LaunchAgent
wrapper and exact managed host-agent child. This verifies queued smoke and
current host stability, but does not explain the earlier operation-window
disappearance or qualify queued upgrades.

The authenticated inventory still reports 30 app records and 46 services with
mixed workload health. It also shows seven old queued
`fleet.github.apply-dispatch` receipts (last updated October 8–10). The first
approved reconciliation lookup returned HTTP 409
`fleet_github_dispatch_binding_mismatch`; it made no Fleet change and left the
receipt queued. The other six were not submitted after that lane-binding
rejection. The platform smoke's `activeOps=0` shows these receipts were not
counted in the platform maintenance drain at this readback. Do not cancel or
rebind them outside the supported current-lane reconciliation path.

Queue-based upgrade, controller-mediated new workload admission, and workload
resize remain open gates. See [launch history](LAUNCH-HISTORY.md) for direct
upgrade evidence and the dated failure sequence.

## Prior DO V3 platform release and controller recovery (2026-11-11 03:16 UTC)

The protected state readback
[42943628535](https://github.com/antiartificial/norn-fleet/actions/runs/42943628535)
passed at lineage `9201526d-4ce8-c70b-6a70-7e94a68dc580`, serial 23. The
controller-mediated platform upgrade
[42945021070](https://github.com/antiartificial/norn-fleet/actions/runs/42945021070)
completed on merged Fleet source `2d168140c842d8614fe093c9ece28b458926eeaa`.
It promoted signed Norn release
`34178b8b90d52a2fa3f138ffae2dfc63744197e1` through the all-stop lane. Its
postflight verified exactly one healthy private API backed by etcd, the
target release, and durable API/UI pins on all three controls. The all-stop
promotion interrupted service. Online upgrade remains unqualified.

This proves the V3 platform release and its postflight at that run's time. It
does not prove that an application workload responded after the upgrade.
Controller-driven workload admission, mixed-version rollout and resize, and
stateful MySQL compatibility remain open. The Mini is an independent direct
lane and is not part of this Fleet.

Fleet PRs [#979](https://github.com/antiartificial/norn-fleet/pull/979),
[#980](https://github.com/antiartificial/norn-fleet/pull/980), and
[#981](https://github.com/antiartificial/norn-fleet/pull/981) fixed terminal
intent discovery, packaged receipt validation in the pinned discovery module,
and allowed a protected module refresh with an unchanged Linux gate digest.
Gate-install run
[42951258208](https://github.com/antiartificial/norn-fleet/actions/runs/42951258208)
installed the updated controller modules. Runner 1036 retired with a terminal
receipt, both controller journals cleared, and the installed controller read
the intent queue with zero eligible entries. The LaunchAgent restarted with
the reviewed modules. The remote runner service remained inactive and
disabled. This management-host install changed no Fleet node, workload, or
provider infrastructure.

## DO staging provider inventory (2026-11-10 14:58 UTC)

A read-only `doctl --context work` inventory at 14:53 UTC reported all eleven
staging Droplets active in `nyc3`: two management nodes, three controls, two
ingress nodes, and four app nodes. The `norn-pilot` context is a different DO
account and does not contain this Fleet. These checks prove provider instance
state only. They do not prove Norn API, etcd, Nomad, Consul, route, or workload
health.

## DO staging provider refresh (2026-11-10 16:02 UTC)

A fresh read-only `doctl --context work compute droplet list` returned all
eleven Norn staging Droplets active in `nyc3`: two management nodes, three
controls, two ingress nodes, and four app nodes. The `work` account is Turnkey
Marketing (`webdev@tkmkt.com`, team `Webdev`, account UUID
`17f40050233d1cd4962b44050e82a0dd01f8dff7`). The separate default
`norn-pilot` account is `theartificial@hotmail.com` (`theartificial`, team
`My Team`, UUID `05fe610a42b6d50c65344dd87bd842ce35ab036d`); it is not the
staging account.

This refresh confirms provider-reported VM state only. It does not check the
Norn API, etcd, Nomad, Consul, routes, or application response. Genesis
readback, platform release/postflight, and workload qualification remain open.

## DO bootstrap controller admission merged (2026-11-10 16:16 UTC)

Fleet PR [#962](https://github.com/antiartificial/norn-fleet/pull/962) merged
as `5400fd8a973266f6e211ce85ee6b3e0d7798cb16`. It adds owner-signed,
state-bound bootstrap intent admission through the one-use Linux runner
controller and remote job gate, with exact run-scoped runner labels. The
contract passed; focused local verification covered 54 tests and the
deterministic statechart passed nine safety invariants.

This advances code and offline protocol evidence only. The bootstrap feature
flag remains `false`; the merged controller has not been installed on the DO
management runner, and no bootstrap dispatch, Fleet install, or provider
mutation occurred. Next: stop the persistent effect runner, install and verify
the reviewed remote gate, verify fresh protected Linux runner readiness, and
then prepare a fresh reviewed intent. Do not replay canceled run
`42838769029`.

## DO management runner readiness (2026-11-10 16:20 UTC)

A read-only GitHub runner inventory for `antiartificial/norn-fleet` returned
one online self-hosted runner: `norn-external-mac-finalizer-xenon`, labeled
`norn-external-mac-finalizer`. No runner was registered with the required
`norn-work-management-bootstrap` label or the protected Linux
`norn-fleet-protected` effect-runner label. The external-Mac finalizer is a
separate purpose and must not be substituted. This inventory is an idle-time
snapshot: the management Mac runner is registered just in time for an exact
queued protected job, so its absence from the persistent runner list does not
by itself show that the install workflow is undispatchable.

The `work-staging-fleet-gate-install` workflow requires the protected macOS
management-bootstrap runner. Its local controller admits only exact protected
workflow jobs and creates a run-scoped ephemeral runner after one-use local
authorization. Readiness therefore depends on the controller, its protected
inputs, and a matching queued job; do not manually register the runner or reuse
the finalizer. Keep the platform-bootstrap flag false until the install
preflight passes.

## DO management Mac controller check (2026-11-10 16:41 UTC)

The owner-local `com.norn.work-management-mac-runner-controller` LaunchAgent
was running (PID 17405). Its protected registration metadata names ephemeral
runner ID 236 and records it offline; the one-use `allowed-job.json` and
controller state file were absent, consistent with an idle controller. The
controller source allowlists `work-staging-fleet-gate-install.yml` and uses a
run-scoped JIT registration after it finds an exact queued job. This confirms
the intended route exists locally; it does not prove that the controller's
GitHub credentials and all protected install inputs are currently usable. The
next gate is a read-only readiness check of those inputs and the remote gate
digest, followed by the reviewed gate install while the Linux effect runner is
stopped. No workflow was dispatched and no host or provider state was changed.

Genesis readback runs [42827741033](https://github.com/antiartificial/norn-fleet/actions/runs/42827741033)
and [42831291445](https://github.com/antiartificial/norn-fleet/actions/runs/42831291445)
verified exact backend bytes, the serial-22-to-23 route continuity, and
effective Ansible transport, then failed installed-source verification before
reading Genesis data. The management controller is still pinned to
`2de3cbd832688a06fc549ec68172145dca4404fc`; PR #957's bounded retry did not
fix the installed-source failure. Genesis units remain disabled. The protected
Genesis readback variable is `true`, but it does not bypass source verification.

Fleet PR [#959](https://github.com/antiartificial/norn-fleet/pull/959) merged
as `7f6433b06030d87726aa4055b7ff7b5bfa963f2b`. It adds exact route-continuity
proof and deterministic generation of the controller source archive and
private install vars. Bootstrap run
[42838769029](https://github.com/antiartificial/norn-fleet/actions/runs/42838769029)
was canceled while queued, before its first step, because no matching protected
Linux effect runner was registered. At that time the controller's
signed-effect allowlist did not admit the bootstrap workflow; Fleet PR #962
has since merged that admission path. The bootstrap enable flag is `false`; no
host installation occurred.

## DO gate install and controller postflight (2026-11-10 17:20 UTC)

Protected gate-install run
[42861703620](https://github.com/antiartificial/norn-fleet/actions/runs/42861703620)
completed successfully at 17:10 UTC. Its exact-source preflight passed; the
workflow installed the reviewed Linux gate and modules, installed and verified
the pinned Certbot 2.9.0 package set, masked and stopped `certbot.timer`, and
replaced the reviewed Mac hook/modules and owner public key. Postflight found
the persistent effect-runner service inactive and disabled, no `.runner`
registration or active job, and the expected installed file/package digests.
This changes management-host tooling only; it did not apply provider
infrastructure, bootstrap Norn, or read Genesis data.

The run created and retired its exact run-scoped Mac JIT runner. After
confirming the run and its resources were terminal, the local LaunchAgent was
restarted; at 17:20 UTC it was running as PID 85572. Fresh Python imports and
read-only GitHub queue-candidate checks for both management and effect
controllers succeeded and found no queued candidate. Post-install review found
the installer's Mac copy list omitted
`work_staging_norn_release_job_gate.py`; the exact source module was repaired
locally. Fleet PR
[#964](https://github.com/antiartificial/norn-fleet/pull/964) adds it to the
repeatable installer with a regression assertion, and its exact-head CI is
still running. Do not treat the repair as repeatable until that PR merges.
Fleet PR #963 merged the JIT install and controller runbook. Fleet PR
[#965](https://github.com/antiartificial/norn-fleet/pull/965) documents the
persistent Certbot package and timer effects; it remains open.

The bootstrap feature flag remains `false`. No fresh Genesis intent or
readback, platform release, workload deployment, or provider apply occurred.
The DO Fleet is not confirmed online by this management-host install. Obtain a
fresh protected private health and Genesis readback before making a service
health claim.

## DO protected state-object readback (2026-11-10 18:08 UTC)

Protected state-handoff workflow run
[42871361099](https://github.com/antiartificial/norn-fleet/actions/runs/42871361099)
passed in `readback` mode on merged Fleet `main`
`a09847fcd34d1f1df48355d6f07c2304e11b59bf`. The protected Spaces object
matched the owner-retained state byte for byte at lineage
`9201526d-4ce8-c70b-6a70-7e94a68dc580`, serial 23. The exact JIT Mac runner
retired successfully; GitHub returned only the separate persistent Mac
finalizer runner afterward, with no active authorization or run journal.

This confirms the remote state object still matches the reviewed owner copy.
It does not inspect the Genesis journal or prove current API, etcd, Nomad,
Consul, route, or workload health. The bootstrap flag remains `false`; no
Genesis install/readback, platform release, workload deployment, or provider
apply occurred. A fresh protected service and Genesis readback is still needed
before calling the DO Fleet online.

Fleet PR #964 fixed the Mac module copy list, and the exact-source dependency
is now present locally with imports and read-only queue checks passing. A
repeat gate install was not dispatched: the remote gate already matches
`main`, and the workflow rejects identical old/new gate digests. Fleet PR #965
documents the package/timer side effects; PR
[#966](https://github.com/antiartificial/norn-fleet/pull/966) documents this
same-gate recovery limit.

## DO Genesis readback follow-up (2026-11-10 20:01 UTC)

Fleet PR [#968](https://github.com/antiartificial/norn-fleet/pull/968)
merged as `31c200f3a7653485c4a48511c1c8ce1623c18fe4`. A fresh signed,
state- and host-key-bound readback intent was dispatched as run
[42890599944](https://github.com/antiartificial/norn-fleet/actions/runs/42890599944).
The exact-source checkout, derived private readback variables, pinned Ansible
environment, original protected-input checks, and live private management and
control SSH checks all passed. The playbook verified that the Genesis service
and timer were inactive and the timer disabled, and verified the installed
controller source pin. The final read-only journal task then failed closed:
`genesis.sqlite` had SQLite WAL sidecars, and the helper refuses to inspect a
journal with sidecars. No Genesis contents were reported, and the controller
recorded the one-use run terminal.

This is now the specific blocking issue, replacing the earlier private-variable
mismatch report. Do not remove or checkpoint the journal sidecars to force a
read. Fix the readback to inspect a stable copy of the database and any WAL
data without mutating the source, add deterministic fixture coverage, then use
a fresh one-use readback authorization. DO services, etcd quorum, Nomad,
Consul, routes, and workload response remain unverified; provider inventory
and SSH reachability are not service-health proof. Fleet PR #969's LaunchAgent
CA persistence fix passed exact-head validation and merged. Fleet PR #970's
stable-copy fix passed exact-head contract validation and merged; the
successful protected Genesis readback is recorded below.

## DO Genesis readback completed (2026-11-10 20:48 UTC)

Fleet PR [#970](https://github.com/antiartificial/norn-fleet/pull/970)
merged as `17ce50edeef75d0d2330bfd79ff6ed90786bc440`. Its protected,
state- and host-key-bound readback run
[42898054171](https://github.com/antiartificial/norn-fleet/actions/runs/42898054171)
passed. The controller checked out the exact merged source, validated the
original private inputs and management/control SSH transport, then read the
Genesis intent and dormant journal from a stable private SQLite copy including
the committed WAL sidecar. The Ansible recap was `changed=0`, with no failed or
unreachable hosts; the one-use runner was retired and the controller receipt
reconciled to terminal.

This closed the Genesis journal readback gate and confirmed that the journal
records an approved signed release intent. At that time, it did not prove that
the Genesis service was active or that Norn and workloads were healthy. The
later platform postflight is recorded above. It proved the target API and etcd
backend, but no application workload response has been checked. The Mini
remains an independent direct lane and is not part of this Fleet status.

## DO Norn release state revalidated (2026-11-10 20:53 UTC)

The protected Norn release state readback
[42899611733](https://github.com/antiartificial/norn-fleet/actions/runs/42899611733)
passed against the retained raw state at lineage
`9201526d-4ce8-c70b-6a70-7e94a68dc580`, serial 23. The backend object matched
the owner-retained bytes and reviewed SHA. A new private release-input
candidate was assembled and validated offline from that exact snapshot and
the reviewed one-hop host-key continuity receipt. At that time, the readback
proved input consistency only. The live source and health preflight had not
run, so no upgrade was dispatched. The follow-up controller-mediated release
completed on 2026-11-11 and is recorded above.

## Release timing

The DO V3 platform deployment date is 2026-11-11. The latest public platform
`/version` request at 2026-11-12 12:26 UTC and canary `/route-proof` and
`/version` probes returned HTTP 200. The canary response names one node and
allocation; the earlier five-sample check reached two allocations on two
nodes. The bounded public-pilot verifier passed 12/12 reads
at 02:40:57 UTC and observed two allocation IDs. These responses do not prove
the canary's V3 controller admission. The V3-admission-qualified workload date
is uncommitted and has no calendar forecast. The remaining gates are the
reviewed protected inputs and credential provenance, signed workload/ACL/
network readback (GET-only evidence plus explicitly authorized, expiring,
run-scoped Nomad and Consul reader-token issuance), terminal-artifact replay
after controller restart, full-executor recovery through real Nomad and ingress
effects, and binding the responding stateless canary to durable same-intent
receipts. See the
[release program milestones](release-program-milestones.md) for sequencing.

Mixed-version workload resize, online platform upgrade, and fresh-Fleet
Genesis/bootstrap are separate follow-on capabilities; they are not first-
workload launch-date gates. The independent Mini deployment meets the
owner-stated success criterion: the V3 API and `vigil-gateway` both responded.
Its queued-upgrade recovery and Git-driven workload admission remain separate
Mini follow-ups. Provider-active Droplets or a historical soak do not prove
current service or workload health.

## Historical snapshot (2026-11-10 10:50 UTC; superseded)

## Mini queued upgrade recovery-gate correction (2026-11-10 10:50 UTC)

Mini remains on signed release `2a2d712a` with schema 52. The read-only
inventory reports API health and host status `ok`; workload health remains
mixed. Signed release `d414b3ab` was published after PR #398 passed exact-source
qualification, but its first durable queued upgrade stopped before promotion
because the completed recovered-forward helper still required the current link
to equal the original recovery candidate. The release pointer and schema did
not change.

PR #399 corrected that helper call and passed full exact-SHA qualification. Its
signed successor `640b8b89` passed passive preflight and a fresh protected
backup plus signed-shadow rehearsal: 68 tables, 404,434 rows, migration ledger
1–52, full row fingerprints matched, two migrate-only passes, passive health,
and no source mutations. The next durable queued upgrade still stopped before
promotion. Its terminal diagnostic was `current Mini release is not the source
or exact journaled recovery candidate`, followed by `completed recovery
requires the exact signed adoption receipt`. Inspection found the signed-
descendant verifier’s mode allowlist omitted `upgrade`, although it already
allowed preflight, backup, and shadow. A follow-up adds `upgrade` plus a
regression assertion; a second offline statechart configuration now reproduces
the missing-mode hold in three transitions while keeping the valid configuration
green. The correction is not yet merged, qualified, or signed.

Both failed operations are terminal and must not be replayed. No promotion,
schema change, or workload mutation occurred. The next attempt needs a fresh
exact-candidate backup and shadow rehearsal after the follow-up is merged and
signed. Queue-based platform upgrade remains unqualified until a new durable
operation completes postflight and smoke. Workload admission and resizing are
separate qualification gates.

## Mini direct V3 platform upgrade completed (2026-11-10 09:25 UTC)

PR [#396](https://github.com/antiartificial/norn/pull/396) fixed recovered-
forward evidence verification when immutable predecessor evidence references
an archived adoption receipt. It merged as
`2a2d712a7484b0d7ee82036ccc8d077d1ffc8697`. Protected V3 qualification run
[42787969413](https://github.com/antiartificial/norn/actions/runs/42787969413)
passed all 12 jobs; protected release run
[42788567933](https://github.com/antiartificial/norn/actions/runs/42788567933)
published the immutable signed prerelease with all 16 expected assets.

The exact Mini ARM64 release passed external signature verification and
passive preflight on port 18800. The candidate reported schema 52, reader floor
5, writer floor 33, and disabled recovery/operation workers. The corrected
verifier resolved the archived receipt by its signed raw digest; the managed
host copy was backed up and atomically synced from that already-verified
release to break the first-upgrade helper cycle.

Two durable `platform.upgrade` attempts stopped before promotion at
`Mini host agent is not stably running from the exact promoted release`. After
confirming the API still served A4, the operation drain was clear, the signed
evidence verifier passed, and the host-agent passed its stability check while
idle, the operator ran the standard signed `platform-upgrade upgrade` controller
through the authenticated `env-exec` wrapper. It preserved the recovered-
forward journal and promoted only the exact signed `2a2d712a` release. The
schema remained at 52; the migration-only phase completed at that same version.
The API returned healthy on the exact new source, and authenticated
`norn platform smoke` passed health, drain, release, and event checks. The host
agent binary exactly matches the active release. A subsequent durable queued
`platform.smoke` also succeeded.

This verifies the Mini direct platform-upgrade lane and a working durable smoke
operation. It does **not** qualify queued platform upgrades: the two
pre-promotion failures at the host-agent stability check remain an open
controller issue. No Mini workload admission or workload-resize test was run.
Mini remains independent of the DO staging Fleet; the protected Fleet
readback limits recorded below are unchanged.

## Mini direct V3 generation finalized (2026-11-10 06:30 UTC)

The protected repair release for PR #391, exact SHA
`5bb691de07c3eb865c5b7f636d07e668d824510c`, was signed and published after
the user's exact-SHA approval, imported and verified on Mini, then used only as
the controller for the already-existing recovered-forward generation. The
signed repair runner validated the active pointer, generation manifest,
journal, source evidence, signed source/candidate/controller releases, and
prior API-launcher binding before action. It preserved the existing A4 target;
it did not create a generation or rerun the database migration.

The first finalizer attempt activated the normal candidate API but failed its
host-agent-running check. The journal remained recoverable at
`candidate-normal-active-pending-host-agent-restart`. After confirming the
exact candidate API was healthy and the host-agent launch was retryable, the
same signed runner resumed the same generation; the second attempt restarted
the candidate host-agent, restored the original API LaunchAgent, and returned
success. The active journal is now `candidate-promoted` for transition
`mini-v3-20261110T0021Z-a4b9c3b3`; `/Users/0xadb/norn/current` selects
`a4b9c3b313d65c8a054fb869b18455fde33d3a70`.

Post-finalization checks at 06:29 UTC returned `/api/health=status: ok`,
API version `v3.0.0-rc.1-platform-326-ga4b9c3b3` and the exact A4 source,
authenticated `norn platform smoke` passed health, operation drain, releases,
events, and platform checks, and host status was `ok`. The inventory returned
30 apps and 46 services, but includes stopped/unhealthy apps, unknown cron or
worker services, 25 active incidents, and conflicting active-operation counts
(0 in platform smoke versus 7 in inventory). This proves the V3 platform is
online and its controller smoke passed; it does not prove every workload is
healthy. Mini remains independent of the DO Fleet.

## Fresh Mini read-only postflight (2026-11-10 08:00–08:09 UTC)

From the operator workstation, a strict-host-key-checked Tailnet SSH session
read the Mini's loopback API and ran authenticated `norn platform smoke` via
its encrypted runtime environment. `/api/health` returned `status=ok` with
Consul, Nomad, Postgres, S3, SOPS, and the Nomad/Consul workload connector up;
the runtime mutation fence was inactive. `/api/version` still selected the
exact A4 source above. The smoke passed health, operation drain (`activeOps=0`),
release, and event queries, reporting 46 services, 161 releases, and ten
critical plus ten warning events in the queried recent-event window. These are
event-query results, not a count of active incidents or proof every workload
is healthy. At 08:08 UTC, the Tailnet HTTPS route returned 200 for `/`,
`/api/health`, and `/api/version`, resolving to the Mini's Tailnet address;
this verifies the Tailnet route, not public Internet ingress or app-specific
routes. The API listener itself remains loopback-bound. The Mini remains
separate from the DO Fleet.

At 08:09 UTC, a read-only Nomad status query found 21 service-type jobs: 18
running, two pending, and one dead. This confirms the Mini is hosting service
workloads, while also showing that not all are running. These Nomad job counts
are separate from the Norn API's app and service inventory. They do not prove
that a new workload manifest has been admitted through a release controller.

At the time of this snapshot, the first smoke invocation from the thin SSH shell could not find the `norn`
child command because the wrapper omitted `$HOME/go/bin` from `PATH`. The
read-only check passed after explicitly adding that directory. PR #395 merged
as `df105b3ba81f81a8189b4e0ade15dbb52e568835`, passed all 12 protected V3
qualification jobs, and its immutable signed release was published. The PATH
fix was later included in the signed `2a2d712a` release and verified by the
post-upgrade smoke recorded above.

## Historical: Mini forward-upgrade attempts exposed archived receipt lookup (2026-11-10 08:59 UTC)

Exact signed candidate `df105b3ba81f81a8189b4e0ade15dbb52e568835` passed
authenticated managed-script preflight on Mini (passive API on port 18800,
schema 52, reader floor 5, writer floor 33; no migration was run). Two
controller-queued upgrade attempts failed before promotion. The first reported
the accepted A4 baseline was not represented by the exact signed adoption
receipt. The operator preserved the old receipt byte-for-byte and created the
fresh A4-bound receipt while retaining the earlier preservation-unproven
incident record. The second then exposed a verifier bug: it read only the
current receipt although immutable predecessor evidence bound the archived
receipt's digest. No API restart, schema migration, or release promotion
occurred; Mini remains healthy on A4. The verifier fix and deterministic
regression were merged, qualified, signed, published, and successfully used in
the later Mini upgrade documented above. Those two A4 operation attempts remain
terminal failed history and did not change the active generation.

The Mini control plane's configured staging topology and 24 historical
read-only capacity plans (latest Oct 13) are not fresh provider or DO
API/workload evidence. The later raw state-object result and remaining online
health gates are recorded in the current DO evidence section below.

## Historical: Mini finalizer repair qualified; exact release awaiting protected publish (2026-11-10 06:07 UTC)

The following is the 06:07 snapshot, superseded by the completed finalization
record above.

PR [#391](https://github.com/antiartificial/norn/pull/391) merged to `master`
as `5bb691de07c3eb865c5b7f636d07e668d824510c`. Full protected V3
qualification run
[42765513323](https://github.com/antiartificial/norn/actions/runs/42765513323)
passed. Protected release run
[42765955784](https://github.com/antiartificial/norn/actions/runs/42765955784)
passed source authorization, pinned UI build, and all four platform bundle
builds, then stopped at the `platform-release` environment approval gate. The
exact SHA has not been signed or published; the run is waiting. The earlier
approval for source `2e8bd545` does not apply to this candidate.

The code change verifies the existing signed A8 API launcher record and loaded
LaunchAgent before preserving the current Mini generation, then stages the
finalizer controller binding at receipt-bound normal activation. It does not
change the A4 target or create another generation. This candidate has not been
imported to or activated on Mini. The latest Mini runtime observation remains
the authenticated 05:31 UTC read below; no later Mini action was performed.

## Mini finalizer controller mismatch found; same generation still pending (2026-11-10 05:31 UTC)

Owner approval was applied to the exact `platform-release` environment gate for
source `2e8bd545547f4bfa375c68fac9be9667954276de`. Protected release workflow
[42744929675](https://github.com/antiartificial/norn/actions/runs/42744929675)
completed successfully. GitHub reports the matching full-SHA tag as immutable
and published, with signed manifests and four platform bundles. At 05:17 UTC,
the Mini ARM64 bundle and installed release passed both the artifact verifier
and configured external signature verifier. This imported the signed
controller but did not activate it or resume the Mini.

At 05:31 UTC, authenticated read-only checks returned `/api/health=ok`, A4
source `a4b9c3b313d65c8a054fb869b18455fde33d3a70` from `/api/version`, and the
exact recovered-forward status for this generation with `finalized=false`,
`runtimeTopology=single-node-mini`, and no Fleet API or etcd consultation. The
05:10 inventory returned 30 app records (19 running and healthy, two pending,
one dead, and eight without Nomad status) and 46 services (19 passing, 27
unknown). Host, operation, incident, release, Fleet, and production-readiness
endpoints returned 404 in that inventory and remain unknown. None of this
proves full workload recovery.

The 05:17 read resolved `active-generation.json` to
`gen-1a769550b1051bc9358ce8e8758265e5`. Its journal SHA-256 is
`3eba3c4e83f025e96959604bf23215ab2434a2cf2b1434b58c44af7ba59e8749`; the
pointer SHA-256 is
`84cbb71c138afd08b60b6e9a1c9ea14bf3d2ebccd0f7c6611a9c06d61e4d8e8a`. The
journal remains `candidate-preservation-pending` for transition
`mini-v3-20261110T0021Z-a4b9c3b3`, source
`d4dc88e0c37003a1013caed444983b2eeb50fbcb`, and candidate
`a4b9c3b313d65c8a054fb869b18455fde33d3a70`; the current symlink still selects
that candidate. The preservation receipt exists and no finalization receipt
exists. These hashes identify the state observed; the signed helper must
revalidate the journal, pointer, releases, and evidence before authorizing any
repair. Do not reuse an authorization after a journal change.

The API LaunchAgent and its HMAC-signed maintenance record still name A8
`a8aa2860c0ecc9ac76b2d852b46cd1ccfb634d63`, and the loaded LaunchAgent program
is that release's signed API launcher. The imported finalizer helper from 2e8
rejects this record when asked to verify its own launcher because its
`controllerSHA` is A8. The ordinary `run` entry also checks the record before
preservation. Do not invoke the imported helper's `run` until the follow-on
signed repair is published and imported. PR #391 later merged and passed full
V3 qualification. Its protected publish job remains paused for exact-SHA
approval. It keeps the verified A8 launcher through preservation, then
rebinds the sidecar and plist to the finalizer controller only at receipt-bound
normal activation; it does not change A4, rerun migration, or create another
generation.

At 05:31 UTC the A8 API was authenticated and responsive, the API LaunchAgent
was loaded, and the host-agent LaunchAgent was not loaded. The active generation
remained `candidate-preservation-pending` with its preservation receipt present
and no finalization receipt. No live finalizer action was run.

## Current Mini recovery status (2026-11-10 03:48 UTC)

The protected A8 controller release
[`platform-a8aa2860c0ecc9ac76b2d852b46cd1ccfb634d63`](https://github.com/antiartificial/norn/releases/tag/platform-a8aa2860c0ecc9ac76b2d852b46cd1ccfb634d63)
is signed, published, imported on Mini, and verified. It resumed the existing
transition `mini-v3-20261110T0021Z-a4b9c3b3` without changing its target A4
release or generation. The database is at catalog schema 52, and the A4 API
serves the exact recovered-forward maintenance status with `finalized=false`,
`runtimeTopology=single-node-mini`, and no Fleet API or etcd consultation. The
preservation receipt records `preservationMatched=true`.

Normal activation is still incomplete. Norn PR #385 merged the finalizer's
signed-descendant ancestry fix as exact source
`2e8bd545547f4bfa375c68fac9be9667954276de`. Protected-master run
[42744444370](https://github.com/antiartificial/norn/actions/runs/42744444370)
passed all 12 qualification jobs. Release workflow
[42744929675](https://github.com/antiartificial/norn/actions/runs/42744929675)
verified the source and built the pinned UI plus all four platform bundles;
its protected signing job is waiting for owner approval. The release is not
yet signed or published, so no further Mini transition was run.

A fresh authenticated inventory at 03:47 UTC reported `/api/health` as `ok`
and `/api/version` source `a4b9c3b313d65c8a054fb869b18455fde33d3a70`; the
A8 controller API is answering on Mini. The inventory returned 30 app records
and a 46-service manifest, but it also contains unhealthy or unknown entries;
this is not proof that every workload is healthy or recovered. The inventory
client's unsupported endpoint probes returned 404, so use the available
health/version/apps/manifest evidence only.

A 03:48 read-only host inspection confirmed that the active generation remains
`gen-1a769550b1051bc9358ce8e8758265e5` at
`candidate-preservation-pending`; its preservation receipt says
`preservationMatched=true`, and no finalization receipt exists. The API
LaunchAgent is running with PID 93463. The `com.norn.host-agent` LaunchAgent
label is absent and the host supervisor has no running PID. Thus the Mini
remains under the recovered-forward write fence and host-agent service is not
restored. Do not report the Mini upgrade complete until the signed repair
controller finalizes the same generation, restores the original API and host
agent behavior, and authenticated API plus host/workload readback passes.

The `active-generation.json` pointer selects the generation above, whose
nested journal remains the `candidate-preservation-pending` transition. The
root-level `recovered-forward/transition.json` still records an unrelated
October transition; use the active-generation pointer and its journal, not
that stale root-level record. These process and journal reads do not establish
workload recovery or finalization.

## Mini partial authenticated inventory refresh (2026-11-10 04:56 UTC)

The authenticated read-only inventory rechecked `/api/health` as `ok` and
`/api/version` as source
`a4b9c3b313d65c8a054fb869b18455fde33d3a70`. `/api/apps` returned 30 records:
19 report running and healthy, two pending, one dead, and eight with no Nomad
status. `/api/services/manifest` returned 46 services, 19 passing and 27
unknown. This supports that the Mini API is serving and some workloads are
running; it does not prove full workload recovery.

The capabilities, active-operations, active-incidents, host-status, release,
Fleet, and production-readiness probes returned 404. Their state is unknown,
not empty or healthy; no current queue, incident, host-assurance, or Fleet
status claim follows from this inventory. The local inventory helper was fixed
to report missing endpoints as `unavailable` rather than zero. The active
generation and host-process state were last directly read at 03:48 UTC above;
this refresh did not reread those files or processes.

During this recovery, `NORN_PLATFORM_REPO` was also added to Mini's encrypted
API environment so the GUI LaunchAgent can verify the adopted source lineage.
The original encrypted environment was retained as a mode-0600 backup under
the recovered-forward directory. The authenticated Mini source and schema
readbacks were performed through the existing controller; no DO Fleet changes
were made.

## Current DO staging Fleet evidence (2026-11-10 09:25 UTC)

DO Norn/API, workload, route, and host-key health are still unverified. The
protected raw backend-object readback in Norn Fleet run
[42783948949](https://github.com/antiartificial/norn-fleet/actions/runs/42783948949)
passed on merged `norn-fleet` source `bf5428f01e6c77e12a4f9844a5b454341d5e4dbd`.
It verified retained state SHA-256
`8cf77ae4f5ea5729a8aecde21fe05f82ca36066f66663f2bb5fe3ef14b19060c`, lineage
`9201526d-4ce8-c70b-6a70-7e94a68dc580`, serial 23, and provider identity for
the three control Droplets. This is a read of retained backend bytes; it did
not prove current provider instance health or private service state.

The private Genesis/service inventory remains held: the protected
`NORN_WORK_PLATFORM_GENESIS_READBACK_ENABLED` setting is absent, current route
and host-key pins need refresh, and the only registered runner is the online
Mac finalizer rather than the required protected Linux
`norn-work-staging-fleet-effect` runner. Do not repurpose that Mac runner or
claim the DO Fleet is online. The Nov 10 provider plan
[42731843397](https://github.com/antiartificial/norn-fleet/actions/runs/42731843397)
was plan-only and made no provider changes. A fresh exact-bound private
inventory of controls, workloads, routes, and provider identities is still the
next live-health gate.

## Mini direct V3 upgrade status (baseline launched 2026-11-07)

## Historical V3 release readiness snapshot (2026-11-10 01:52 UTC)

Protected-master qualification for exact source
`a8aa2860c0ecc9ac76b2d852b46cd1ccfb634d63` passed all 12 jobs in
[run 42729857751](https://github.com/antiartificial/norn/actions/runs/42729857751).
It includes the source-lineage verifier fix needed for the Mini's signed
recovery path. Exact-source immutable release preparation is running as
[42730463793](https://github.com/antiartificial/norn/actions/runs/42730463793).
Authorization, pinned UI, and all four bundle jobs passed; the protected
signing/publish job is waiting for environment approval. It is not signed or
published. Publication still requires approval for this exact source. The
01:52 UTC authenticated read-only Mini inventory confirms the same current
source (`d4dc88e0c37003a1013caed444983b2eeb50fbcb`), API/host status `ok`,
runtime mutation fence `inactive`, and Consul, Nomad, PostgreSQL, S3, SOPS, and
the Nomad/Consul workload connector up. Nineteen of 30 app records are healthy;
seven queued operations remain the stale DO dispatch reservations, 25 incident
groups remain, and production readiness is blocked (18 failed, seven passed,
one warning). The host-status endpoint's latest assurance receipt is still
dated Sep 6, 2026, so the previous 01:16 statement that assurance had freshly
succeeded is not corroborated and must not be treated as current assurance.
This inventory performed no mutation. A new staging-only read-only DO plan completed at
[run 42731843397](https://github.com/antiartificial/norn-fleet/actions/runs/42731843397)
on `norn-fleet` main `35e1c893198efbdc9e407d708e5096a32b485f7d`. Its private
summary reports one create (`digitalocean_firewall.management_ingress_runtime[0]`),
28 no-op resources, and zero new nodes. The firewall attaches to the two
ingress Droplets and allows TCP 18082 (observer) and 18083 (publisher) from
the management runner's private `/32`. It was plan-only; no provider apply or Fleet change occurred.
This does not establish current DO Norn/API, control-store, or workload health.

The protected Genesis readback cannot use the earlier owner approval: the
current `work-staging` state snapshot input digest is
`8cf77ae4f5ea5729a8aecde21fe05f82ca36066f66663f2bb5fe3ef14b19060c`, while
that approval named `4c43bae56dc9e6c9d624f729dc4398d810043c7f4344df0975637a268c1a00cd`.
The member known-hosts digest is unchanged, but the Genesis readback enable
variable is absent and its workflow fails closed. No new intent or dispatch
was created. The configured input digest is not a live provider-state readback;
refresh and review the exact inputs before another one-use protected request.

The Mini recovered-forward controller repair merged as Norn PR #370 at exact
source `9d9d6208654b995cddc8b76e13d089c665c52fd6`. Full protected-master
qualification [42713422353](https://github.com/antiartificial/norn/actions/runs/42713422353)
passed all 12 jobs. Exact-source release workflow
[42714046058](https://github.com/antiartificial/norn/actions/runs/42714046058)
completed after explicit approval; the immutable signed release is published.
A fresh Mini backup and signed shadow passed, but preflight correctly stopped
at schema 51 versus required 52. The subsequent signed recovered-forward
attempt stopped before fencing or migration because the evidence verifier
rejected the older signed adoption source despite its valid ancestry to the
current source. PR #373 fixed that verifier and merged at
`a4b9c3b313d65c8a054fb869b18455fde33d3a70`. Full protected-master
qualification [42721291745](https://github.com/antiartificial/norn/actions/runs/42721291745)
passed all 12 jobs. Exact-source release workflow
[42721807811](https://github.com/antiartificial/norn/actions/runs/42721807811)
completed successfully after exact-SHA owner approval. Immutable signed
release
[`platform-a4b9c3b313d65c8a054fb869b18455fde33d3a70`](https://github.com/antiartificial/norn/releases/tag/platform-a4b9c3b313d65c8a054fb869b18455fde33d3a70)
is published and has been imported and verified on Mini. It has not been
promoted or deployed to DO. It supersedes `9d9d6208` for the recovered-forward
Mini attempt because it includes the verified source-lineage correction.

Norn PR #375 merged afterward at `66690f93` and narrows hash-bound dispatch
recovery to the GitHub App `actions:read` permission. Its full PR checks passed.
The Mini and DO do not run this new code yet. It does not fix the seven stale
receipts' current-lane binding mismatch, which still stops reconciliation
before GitHub lookup. The receipts remain queued. See the 00:01 UTC
[launch-history entry](LAUNCH-HISTORY.md#dispatch-recovery-now-requests-read-only-github-actions-access-2026-11-10-0001-utc).

At 00:33 UTC, a post-stop authenticated read-only inventory confirmed the Mini on
`d4dc88e0c37003a1013caed444983b2eeb50fbcb`, API health and host status `ok`,
and runtime mutation fence `inactive`. The exact release `a4b9c3b3` was then
imported and verified signed. A fresh protected backup and signed shadow both
passed for that candidate. The recovered-forward transition
`mini-v3-20261110T0021Z-a4b9c3b3` created generation
`gen-1a769550b1051bc9358ce8e8758265e5` and stopped at journal phase
`host-agent-fenced`, before API fencing or migration. The API still serves
source `d4dc88e0`, schema last observed at 51, health `ok`, and runtime
mutation fence `inactive`; candidate `a4b9c3b3` is not promoted. The host-agent
LaunchAgent is loaded but unable to start because its durable fence blocks
ordinary restart. A plain
retry was rejected because the resolver requires signed controller repair for
this phase. The distinct source controller's repair helper rejected the new
source-lineage evidence, so no repair authorization or database change was
made. The 00:33 inventory had 19 of 30 app records running and healthy, seven
queued `fleet.github.apply-dispatch` operations, 25 open incident groups, and
blocked production readiness. At that time, the latest host-assurance receipt
was stale. The 01:16 UTC inventory entry claimed a fresh assurance pass, but
the 01:52 UTC response still reports the latest durable `host.assure` receipt
dated Sep 6, 2026; that fresh-pass claim is uncorroborated. No new assurance
command was run.
DO was not changed.

The seven stale DO `fleet.github.apply-dispatch` receipts remain queued. A
fresh authenticated inventory confirmed the count. The current handler rejects
their old Fleet-lane bindings before invoking GitHub lookup, so reconciliation
cannot proceed on the current lane; it would require a new, exact-binding
read-only recovery path. No workflow dispatch, provider apply, or receipt
change occurred. PR #370 makes the recovered-forward Mini lane preserve these
receipts under signed transition evidence while `legacy-baseline` continues
to require them drained. See the [23:08 reconciliation finding](LAUNCH-HISTORY.md#seven-stale-fleet-dispatch-receipts-remain-unreconcilable-on-current-lane-2026-11-09-2308-utc).

All 27 checked-in statechart configurations were rerun from the release branch
and then merged to current `master` on 2026-11-10; their configured rules
passed, including four missing-evidence workload
admission cases and concurrent two-client admission. Deadlock checks passed
where enabled; the concurrent-admission model intentionally disables them for
terminal stutter states. Coverage includes Mini direct, existing DO release,
existing-Fleet Genesis and readback, fresh Fleet create (current 3+2+4
topology and future General + Stateful pools), proposed online Norn upgrade,
and workload admission, publication, stop recovery, private-input retention,
and mixed-version resize.
The bounded resize comparison still demonstrates that capacity 3 cannot
reach a target-only route and terminal receipt while retaining the old cohort;
capacity 4 reaches the modeled completion. These are protocol-model results,
not runtime or controller-refinement evidence. See the dated
[launch-history entry](LAUNCH-HISTORY.md#recovered-forward-repair-merged-exact-source-release-prepared-2026-11-09-2256-utc)
for per-model counts and limits.

The 22:47 UTC snapshot and the 00:33 UTC post-stop inventory above are
historical. The latest status is in the current Mini recovery section at the
top of this document: the active generation is at
`candidate-preservation-pending`, and the A8 release is already signed,
published, imported, and verified. The ancestry-fix source `2e8bd545` passed
qualification and bundle preparation, but its separate exact-source publish
job still requires owner approval. Do not retry with a new transition ID or
edit the journal. After the exact repair release is published, resume this same
generation through the signed controller path and verify the generation
receipt, API/schema, host-agent restoration, host assurance, and workload smoke.

## Historical Mini runtime evidence snapshot (2026-11-09 22:47 UTC)

The read-only inventory at 22:47 UTC reports `/api/health` and host status
`ok`, runtime mutation fence `inactive`, and exact current release/source
`d4dc88e0c37003a1013caed444983b2eeb50fbcb`. `/api/schema` reports migration
51 and reader/writer floors 5/33. The host-status read reports the latest
`host.assure` as `succeeded` with exit code 0. This is sufficient evidence
that the independent Mini is online and currently hosting workloads: 19 of 30
app records are running and healthy. The inventory also records two pending,
one dead, and eight without a Nomad status; it is not a claim that every app is
healthy.

Seven active operations remain, all queued `fleet.github.apply-dispatch`
receipts. The earlier lookup-only reconciliation returned
`fleet_github_dispatch_binding_mismatch` for all seven; no receipt was changed.
There are 25 active incident groups, four snapshot-retention warnings, and
production readiness remains blocked (7 checks passed, 1 warning, 18 failed).
These are separate from the evidence that the Mini API and running workloads
are online. The runbook lane is recovered-forward after release `9d9d6208` is
signed and published; the seven Fleet receipts will be preserved under that
lane's transition evidence.

The DO staging provider/runtime snapshot remains the dated 18:58–19:01 UTC
evidence below. It has not been freshly read in this turn. Provider credentials
remain in the protected Fleet runner; a fresh DO control-plane/runtime status
requires a new exact-bound readback intent and runner receipt. No local
provider query or DO mutation was made.

## Prior Mini/DO runtime evidence snapshot (2026-11-09 22:08 UTC)

The Mini direct adoption is complete, but V3 is not yet ready for general
release or hands-free Fleet upgrades. The Mini is independent of Fleet; DO is
the sole staging Fleet and remains a separate qualification target.

Protected-master qualification run
[42700530242](https://github.com/antiartificial/norn/actions/runs/42700530242)
passed at `e9a21d2095d60c8a96c1440e35e045dd0b6a7637`. Release run
[42701234927](https://github.com/antiartificial/norn/actions/runs/42701234927)
passed exact-source authorization and all five UI/platform bundle jobs. The
owner approved its protected `platform-release` job; immutable signed
prerelease `platform-e9a21d2095d60c8a96c1440e35e045dd0b6a7637` was published
at 21:28 UTC. The downloaded Darwin/arm64 bundle passed signature, manifest,
archive-inventory, SBOM, and exact-source verification.

At 21:31 UTC, Mini health remained `ok` on source
`d4dc88e0c37003a1013caed444983b2eeb50fbcb`, schema 51. The exact signed
`e9a21d20` candidate was imported and its passive controller preflight stopped
before any database migration or API promotion because the candidate requires
schema 52. The seven old DO `fleet.github.apply-dispatch` receipts were each
sent through the protected lookup-only reconcile endpoint. All seven returned
HTTP 409 `fleet_github_dispatch_binding_mismatch`: their stored bindings do not
match the current Fleet lane. No receipt was changed, and no GitHub workflow
dispatch or provider apply was requested.

The 22:08 UTC inventory reports Mini API and host status `ok` on source
`d4dc88e0c37003a1013caed444983b2eeb50fbcb`, schema 51, and reader/writer
floors 5/33. The signed `e9a21d20` candidate requires catalog migration 52 with
the same floors. Its passive preflight stopped before DDL as expected. A
protected backup created at 22:05 UTC matches its recorded SHA-256 and source
`d4dc…`; the signed shadow receipt completed at 22:05:49 UTC for exact target
`e9a21d20`, with private-restore and runtime claims passed.

The ordinary controller `upgrade` attempt then stopped before migration or
promotion because the retained recovery guard requires the current release to
match the completed recovered-forward candidate. The signed active generation
is complete for `bba290db` (source `e117d564`), while the live Mini is now on
signed descendant `d4dc88e0`. The current release has not been changed by this
refusal. The supported successor-generation path can bind this descendant only
after the exact signed candidate wrapper and managed controller agree on the
generation-input evidence path. Review found they do not in `e9a21d20`; a
follow-up fix is in this worktree and remains unsigned. Focused Go tests, the
21 controller integration tests, and the full `startup` package now pass. The
correct lane is `recovered-forward`, which can preserve the seven queued DO
receipts under signed transition evidence. Do not edit the prior generation
or try the ordinary upgrade again.

Keep the separate `legacy-baseline` lane distinct: only `recovered-forward`
may preserve queued Fleet dispatch receipts, because it binds their
preservation to signed transition evidence. Qualification run
[42706816151](https://github.com/antiartificial/norn/actions/runs/42706816151)
exposed an initial attempt to broaden that exception. The follow-up now refuses
`legacy-baseline` before fencing when any queued Fleet dispatch exists. That
guard is not yet merged or signed. Although reader/writer floors are
compatible, the retained failed-transition sentinel means the Mini cannot use
the ordinary `upgrade` path until a signed recovered-forward successor
generation is completed. No Mini migration or release promotion has occurred.
DO's latest provider inventory remains 18:58 UTC; its 19:01 UTC public canary
probe is not proof of control-plane health.

- **Mini direct (18:48 UTC inventory):** the independent Mini's completed Nov 7 controller-mediated
  direct adoption remains in place. At 18:48 UTC, authenticated reads reported API and host status `ok`,
  `runtimeMutationFence=inactive`, and Consul, Nomad, Postgres, S3, SOPS, and
  the Nomad/Consul workload connector up on signed V3 repair release
  `d4dc88e0c37003a1013caed444983b2eeb50fbcb`
  (`v3.0.0-rc.1-platform-252-gd4dc88e0`). The inventory contained 30 app
  records and 46 service records; 19 app records were running, two pending, one
  dead, and eight had no allocation. The latest `host.assure` receipt succeeded
  at 18:48:53 UTC (exit 0), covering 11 required services, 10 endpoint probes,
  and six public-route checks. Seven queued `fleet.github.apply-dispatch`
  receipts remain; no other active operations were reported. There are 25
  active incident groups, including 51 open entries in the host-assurance
  failure group, and four snapshot-retention warnings. The 18:48:55 UTC
  production-readiness report remains blocked (7 passed, 1 warning, 18 failed)
  by the separate production-admission contract; this is not evidence that the
  Mini cannot host its currently running workloads. A separate 17:53 UTC
  read-only Git-path check confirmed 12 `repo.autoDeploy` app records, a
  configured webhook secret in the running API process, and 25 recent GitHub
  delivery receipts (17 `deploying`, 8 `ignored`). Six active GitHub push hooks for
  mapped personal repositories target `norn-webhooks.slopistry.com`; their
  recent GitHub delivery lists were empty, and endpoint-to-Mini routing and
  delivery-to-operation outcomes were not correlated. At this Nov 9 read time,
  the Mini ran `d4dc88e0`, before the exact-push-SHA fix merged as `ccb49a56`.
  The later `2a2d712a` release includes that fix, but end-to-end webhook
  deployment remains unqualified. See the
  [Mini Git-to-controller workload path](release-lanes-and-workload-resizing.md#mini-git-to-controller-workload-path).
  Mini remains independent of Fleet.
- **DO staging:** read-only `doctl --context work` queries at 18:58 UTC found
  all nine `tk-staging-nyc3` Droplets active (3 control, 2 ingress, 4 app),
  both management Droplets active, the ingress load balancer active, and the
  management-authority PostgreSQL plus application PostgreSQL/MySQL databases
  online. A public HTTPS probe at 19:01 UTC returned
  `norn-fleet-canary` from `/route-proof`; five `/version` responses reported
  the same source SHA
  `be5ca5d969ccf3509e62ce04a833516bb5c204db` across two allocation IDs and
  two node IDs. The canary hostname returns 404 for `/api/health`. Current
  Provider resources and that earlier canary probe do not establish current
  Norn API health, etcd quorum, Nomad/Consul control-plane health, application
  catalog state, or the terminal receipt for the earlier failed deployment.
  Full Fleet runtime health remains unverified. Genesis readback remains
  unpassed: run 42418784411 used an installed verifier whose pinned source
  objects were absent, and later attempts also stopped before a successful
  readback. Those intents and one-use runners are terminal. Fleet PR #946
  merged the source-admission preflight, but no protected installer or
  readback has run against that merged source. A repeat needs a fresh
  authorized intent and one-use runner; do not reuse prior authorization.
- **Workload release/resize:** PR #302 is merged to Norn `master` at
  `c792564c9440805a467a832a4fcb6b433a1f68c3`; PR #307 is merged at
  `1a254466cacf37b636efd50cdecfdd7d6cf5e75f`; PR #311 is merged at
  `4c90aecfc47d4687cc3f39d433d8d5d182d61a7`, PR #314 at
  `db4623a1`, PR #315 at `fc7bb70f`, PR #319 at `877ad4ba`, and PR #324 at
  `3fcdc154e247d5b32a8ad025e4fb9690390b5022`.
  PR #311 returns the accepted operation before rereading mutable inputs;
  PR #314 binds the stable request digest to qualification and target size;
  PR #315 exposes replay-only lookup for admission producers; PR #319 adds an
  opt-in staging-only authenticated API that derives the plan from current
  qualified deployment, approved policy, Fleet target, route, Nomad lineage,
  app source and encrypted SOPS snapshot. The exact-head API, Fleet pilot, durable replica,
  CLI, OpenAPI, UI,
  qualification, workflow, and security checks passed. The offline stateless
  controller, durable receipt/recovery path, and statecharts are in source, but
  have not been deployed or live-qualified. TLC explored 458 states and 1,663
  steps: 18 safety invariants and deadlock checking passed. Four
  missing-evidence scenarios each explored 4 states and 6 steps and rejected
  incomplete admission. The API suite and focused etcd-backed integration
  coverage passed; they do not exercise a live Nomad/ingress driver or provider.
  PR #319 consumes the CAS-protected staging policy and binds its digest into
  the signed plan through private acceptance. Its request is limited to a
  qualification ID and replica count; exact retries resolve before mutable
  reads.
  PR #324 fixes a store-level race between two independent etcd clients
  submitting the same request: the losing caller rechecks and returns the
  winner's durable acceptance. Its two-client test passed ten consecutive
  local-etcd runs, the race detector, and API suite; the bounded regression
  model checks 14 states and 18 transitions. PR #335 exercises authenticated
  HTTP replay against real etcd after response loss using seeded acceptance;
  PR #337 reconstructs the handler and etcd adapters before replay; PR #339
  recovers the shared reconciler across an etcd-backed successor claim after a
  lost route-effect response, with controlled fake scheduler/route/health/drain
  effects. PR #351 (merged as `ed296c17`) pins the exact job-driving app spec
  and encrypted SOPS snapshot in the release key-ring envelope. Worker tests
  reconstruct target inputs without a current checkout, and a real-etcd
  acceptance/open integration test verifies persisted source and ciphertext.
  Fresh HTTP admission with real qualification, owned active-route lineage,
  and etcd private-input persistence in one joined fixture; whole production
  executor/worker restart recovery; retention/abandonment for permanently held
  operations; and DO mixed-version release remain open. PR #351's
  receipt-bound encrypted-input deletion and pending/held retention are
  implemented and store-tested, but not live-qualified. Stateful workloads
  still need separate writer/schema/data authorization.
- **Fresh Fleet:** separate from upgrading DO. General + Stateful are the
  selected future application pool roles, with stable pool IDs `general` and
  `stateful`. Protected `norn-fleet` main `35e1c893` now accepts that pair in
  topology validation, renders per-pool identity/role in inventory, and
  registers distinct Nomad pools before clients join. Three focused validator,
  inventory, and bootstrap tests passed against that exact main revision.
  This does not add a future provider profile: current staging remains the
  single `workload: app` pool, and sizing, cost, placement and stateful recovery
  are still open. Fleet PR #941 (offline war-game),
  #940 (release intent controller), #945 (provider scope contract), #947
  (bootstrap credential lifecycle), #942 (module-owned test collection), and
  #948 (attested DO token-scope admission) are merged. PR #948's contract and
  security checks passed at exact head `951ca701`. Its owner-supplied evidence
  binds the granted-scope record to the exact token fingerprint and account,
  and its offline checks pass. It does not query DO to prove the live token's
  grants. Merged-main plan run `42693143436` completed read-only and proposed
  one management-runner-only ingress firewall for ports 18082 and 18083,
  with no other changes or destroys. No apply occurred. PR #943 closed when its stacked base merged;
  Fleet PR #837 at head
  `eef00505d23e37c66cdaed75c5f4c5504a1cd22b` merged as
  `6335c667693d0333d4239f91edf2efb9772ab63a` after its exact-head contract
  rerun (42578247221) and both security checks passed. The rerun succeeded
  after the earlier transient provider download failure. This is source
  readiness only: it did not enroll a host, apply provider changes, or perform
  Genesis readback. Integrated controller recovery and credential lifecycle
  evidence remain gates before any provider mutation.

The current `norn-fleet` source has the General + Stateful role wiring; prior
documentation claiming that its validator and Ansible rejected the pair was
stale. This does not create a fresh-Fleet profile or alter current staging,
which remains one `workload: app` pool. The stable future IDs are now
`general` and `stateful`. See the 23:02 UTC
[source-verification entry](LAUNCH-HISTORY.md#fresh-fleet-general-stateful-role-path-verified-2026-11-09-2302-utc)
for the exact main revision, focused tests, and remaining profile/workflow
gates.

The milestone table near the end of this file is a historical estimate; use
the lane evidence above and the M5 protocol's dated status for current gates.

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

## Fresh Mini inventory (2026-11-07 21:38 UTC)

Authenticated read-only inventory reconfirmed source
`bba290db041e9f497c7161543c679d9d348b742d`
(`v3.0.0-rc.1-platform-251-gbba290db`), API/host status `ok`, 30 app records
and 46 service records. Mini is not a DO Fleet member. Its API does report
`fleet_configured=true`: [`loadFleetInventory`](https://github.com/antiartificial/norn/blob/877ad4ba/v2/api/handler/fleet.go#L116)
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

## Read-only Mini refresh (2026-11-08 15:41 UTC)

The authenticated inventory helper completed without mutation. API health and
host status reported `ok`; the host services `consul`, `nomad`, `postgres`,
and `workload-connector/nomad-consul` reported up, and the latest host
assurance reported succeeded. The active platform release source reported by
the API is
`d4dc88e0c37003a1013caed444983b2eeb50fbcb`
(`v3.0.0-rc.1-platform-252-gd4dc88e0`), which is the next controller release
after the original signed Mini adoption target
`bba290db041e9f497c7161543c679d9d348b742d`.
The latter remains in the installed release inventory as a prior release.
This reconciles the post-upgrade repair release with the adoption journal;
`bba290d` remains the target whose transition and preservation receipt were
verified.

The same point-in-time read reported 30 app records, 19 healthy, 25 open
incident groups, seven queued `fleet.github.apply-dispatch` receipts, and
production readiness `blocked`. The platform rollup also warned that snapshot
retention was over limit for four apps. Therefore API/host-upgrade success is
confirmed, but this inventory does not claim that every app is healthy or
that Mini is production-admitted. The Mini's `fleet_configured=true` API flag
still describes the separate DO staging target configuration, not Fleet
membership. This observation confirms only the Mini; it says nothing about
current DO runtime health.

## Read-only refresh (2026-11-08 18:12 UTC)

The Mini inventory helper completed remotely without mutation. The API and
host reported `ok`; latest host assurance was `succeeded`; Nomad, Consul,
Postgres, S3, SOPS, and the Nomad/Consul workload connector were up. The API
reported source `d4dc88e0c37003a1013caed444983b2eeb50fbcb`
(`v3.0.0-rc.1-platform-252-gd4dc88e0`), seven queued
`fleet.github.apply-dispatch` operations, and production readiness `blocked`
(7 passed, 1 warning, 18 failed). Four snapshot-retention warnings remain.
The API's `environment.id=staging` and configured `control`/`ingress` Fleet
document describe the Mini control plane's configured target, not Mini Fleet
membership and not current DO runtime state. Mini remains an independent
node.

Fleet PR [#939](https://github.com/antiartificial/norn-fleet/pull/939) merged
at `4451767624b12cbc41cc32e160a10d4b46006e0f`. The subsequent protected,
read-only Genesis readback [run 42418784411](https://github.com/antiartificial/norn-fleet/actions/runs/42418784411)
failed its source-capability check: it executed controller source
`2de3cbd832688a06fc549ec68172145dca4404fc`, and the reviewed capability or
target commit was absent from the source repository seen by that run. The run
made no reported changes. This does not establish that DO Genesis readback
passed or that the DO Fleet is currently online. A repeat requires a fresh
readback intent bound to the same verified state and host keys and an execution
that uses the merged verifier. The previously signed intent and one-use runner
authorization are terminal and must not be reused.

The offline recovery contract is modeled in
[`genesis-readback-source-gate`](statecharts/genesis-readback-source-gate/README.md):
it requires dormant units, a reviewed source install, and a fresh one-use intent
after the stale-source hold. TLC checked 18 states and 52 transitions with all
five invariants and the deadlock check passing. The live bootstrap gate remains
false or unset, so the modeled install is not a live-readiness claim.

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
Replacement Fleet PR
[#938](https://github.com/antiartificial/norn-fleet/pull/938) added the pinned
Ansible virtualenv to `GITHUB_PATH` and was squash-merged at 21:10 UTC as
`6f9b46886950f3109abe4c26eecead91f823965d` after its protected checks passed.

A fresh owner-signed read-only intent and one-use runner authorization then
dispatched protected run
[#42415121479](https://github.com/antiartificial/norn-fleet/actions/runs/42415121479).
Its runner preflight, checkout, input verification, and private management
transport all passed; the runner was retired afterward. The readback failed
when its final verifier ran Git as root against the protected bare source repo
owned by `norn-exec`. Git rejected that ownership as dubious. Both the pinned
capability commit `86ad17b6407bcbcd028f534f17a9db2253c5de6c` and signed target
commit `9e2e33ad9d2fd0d815e5c19157e1925d6a7e5a05` are present in the repository
when checked as `norn-exec`, and an invocation-scoped `safe.directory` check
finds both without changing global Git trust. The Genesis readback therefore
remains unpassed; the Genesis journal and Fleet state were unchanged.

Fleet PR [#939](https://github.com/antiartificial/norn-fleet/pull/939) merged
the exact-path Git ownership fix at `4451767624b12cbc41cc32e160a10d4b46006e0f`.
The first readback using that repository head still ran the installed
controller source pinned to `2de3cbd832688a06fc549ec68172145dca4404fc` and
failed because the reviewed source commits were absent. See the 2026-11-08
read-only refresh above; install/verify the merged verifier through its
protected path before attempting another readback.

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

## DO staging Fleet read-only refresh (2026-11-08 15:45 UTC)

The DigitalOcean `work` credential context returned nine droplets tagged for
`tk-staging-nyc3`, all `active`: three control, two ingress, and four app
nodes. The `tk-staging-nyc3-ingress` load balancer was `active`. These are
provider-inventory facts, not proof of Norn control-plane quorum or every
application's health.

The public canary endpoints passed read-only HTTPS probes with normal
certificate validation. `/route-proof` returned `norn-fleet-canary`; five
`/version` requests all returned source
`be5ca5d969ccf3509e62ce04a833516bb5c204db`, reporting two distinct allocation
IDs on two distinct node IDs. This confirms the canary route is serving that
source across two reported allocations. It does not verify the DO Norn API,
Nomad/Consul quorum, or the durable deployment operation receipt.

The November 2 record for operation
`6d96db37-a54e-4231-b1b8-e3e616beb3ab` still says failed/manual recovery
required. The public route now serves the same source SHA as that attempt, but
this read did not correlate the route to a terminal signed controller
receipt. Keep that operation unresolved until its durable record is read back.
The Fleet is provider-active and its canary is publicly reachable; its full
control-plane/runtime health and hands-free mixed-version recovery are not
established by this refresh.

## Fleet running-upgrade checkpoint (2026-11-02 00:10 UTC)

This is the point-in-time state from 2026-11-02. The later public-route
readback above reports source `be5ca5d…`; it does not provide a terminal
receipt for the failed operation recorded here.

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
version. A specific Fleet offline-effect subprotocol is still explicitly
versioned in the route `/api/v1/fleet/offline/attempts/{planID}/{attemptID}/effect-grants/v2`;
its tests protect one-use evidence and recovery behavior. It needs a caller and
protocol migration review before removal as part of V3-only support. Tests
mentioning “legacy”, “V2”, or “Postgres” therefore cannot be removed by name
alone; many protect durable state recovery, signed Fleet protocol revisions,
or Postgres conformance. Before retiring any old
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
