# Norn v3 / Fleet release handoff — 2026-09-28

This is the current resume point for the pragmatic v3 release. Recheck the
repositories, PR checks, Mini, DigitalOcean account and provider prices before
acting; the identities below are a dated snapshot. The authoritative release
contract and gate details remain in the [M0–M9 execution milestones](execution-milestones.md).

## Release target and progress

Initial v3 has two required paths: upgrade the existing single-machine Mini
from v2/PG to v3/PG while preserving workloads and data, and bootstrap an
empty DigitalOcean Fleet with three etcd control members and independent app
databases. A representative Mini-to-Fleet app move is separately rehearsed;
moving every app is not an initial release gate. M8 qualification precedes
the separately controlled M9 adoptions.

Owner decision on 2026-09-28: prioritize deployment of the empty DO Fleet.
For the development Mini, a fresh backup in a local directory is acceptable
for the v3 upgrade transition; Mini host-loss recovery and a bounded RPO/RTO
are outside this release gate. Production control-plane recovery gets a
separate off-host, measured qualification. This decision changes the release
contract, not the evidence already collected.

Later owner decision on 2026-09-28: DigitalOcean release tests may incur
temporary spend, but every test resource must be torn down and verified absent
after the exercise. A short ongoing test may remain up while it is actively
used; it must not sit for hours. Use the disposable pilot roots and their
reviewed retirement/final-zero path for a full Fleet test. The persistent
staging and management roots have `prevent_destroy` and no sanctioned
whole-root teardown, so they do not satisfy this test contract. Record an
exact run deadline, deletion owner, provider inventory, and final billing
review before creating any test resource. This is authorization for bounded
test spending, not evidence that the disposable path is ready to apply.

The 2026-09-28 implementation estimates are M0 **60%**, M1 **65%**, M2
**55%**, M3 **50%**, M4 **40%**, M5 **25%**, M6 **10%**, M7 **5%**, M8
**10%**, M9 **0%**. Their equal-weight mean is about **32%**. These are
judgment estimates, not elapsed-time forecasts. **0 of 10 release gates are
signed**. Recent local rehearsals and green CI did not change the estimates
or sign a gate. The revised Mini backup scope does not itself raise any
percentage or sign M0/M5.

## Exact source and review state

- Norn integration checkout: `/Users/arti/Documents/Codex/2026-09-26/i-d/work/v3-master-integration`, branch `codex/v3-master-integration`. Draft [PR #77](https://github.com/antiartificial/norn/pull/77) targets `master`; all 19 checks passed at exact head `96556225ee3c3d77e07903158690ac15d8f79e98`. No signed release exists for that v3 head; protected `master` remains at `0c21b661923e3ec5a896ce9aa737c87a697749f9`. Only this current session handoff remains checked in; earlier session and M0–M3 status checkpoints are retained in Git history. Recheck the exact PR head and checks after this documentation revision. This is review and local qualification, not an installed release.
- Fleet host-etcd [PR #176](https://github.com/antiartificial/norn-fleet/pull/176) was squash-merged into `main` as `73f7cff6decd5ce3ebcda0c6e936c8881ccee067` with the same source tree as reviewed head `6267655052b209b22dc8b3421cb9339f797af9bc`. Ingress/readback [PR #177](https://github.com/antiartificial/norn-fleet/pull/177) was rebased onto that tree, passed its exact-head [contract check](https://github.com/antiartificial/norn-fleet/actions/runs/36491479218) at `b84eb4a52eacab8b19532f98c4896e5b5d255c5c`, and was squash-merged into `main` as `283e27050c028488da0e6877e68ed890023c0e60`. The merge tree exactly matches the reviewed head. The temporary runner auto-removed and the repository switch was deleted. Normal GitHub-hosted execution still reports an account billing/spending-limit block. PR #177 declares the persistent staging project, VPC/droplet/load-balancer membership, separate two-node application PostgreSQL and pilot MySQL, ingress-tag database firewalls and the runner SSH-key preflight. Staging rejects example SSH fingerprints and documentation CIDRs before a plan. Both first-create roots reject IPv6-only SSH sources because the Droplets have no IPv6 bootstrap route; the management offline preflight rejects an IPv6 executor before backend initialization. The management backend example, preflight and protected workflows now include the documented Spaces S3 checksum setting; this has not been tested against a live Space. Management SSH and retirement expiry now fail a plan when already expired and are rechecked at apply; an isolated expired-input OpenTofu plan proved the plan refusal, while future dates retained the 19-create shape. Management preflight also rejects symlinked, improperly owned or group/world-readable private inputs and checks the Tailscale and retirement evidence files. Isolated empty-state plans proposed 21 staging and 19 management creates, zero changes or destroys, using review-only values and no live provider token. The management tag-targeted bootstrap firewall precedes both hosts, and the staging firewall precedes its nodes. CI and local rehearsals do not establish protected-host etcd, database or ingress qualification.
- `mail-indexer` draft [PR #1](https://github.com/antiartificial/mail-indexer/pull/1) head `0a217e2a890900151de4ca298cb7630dfd25ee8f` and `mail-mcp` draft [PR #1](https://github.com/antiartificial/mail-mcp/pull/1) head `eec3db157a8cbcebcbde48bd30491256ce90c4bd` are based on their GitHub masters. Both local full Go suites passed; each hosted `test` job failed before runner steps from the same account billing block. Neither app PR is merged or deployed. Mini's app source checkouts contain unrelated dirty work; preserve them.

## Disposable DigitalOcean host exercise

On 2026-09-28, a supervised three-Droplet etcd exercise ran in the personal
DigitalOcean account (`theartificial@hotmail.com`) in the existing NYC3 VPC.
It used the exact etcd template, systemd unit and PKI generator from the
merged #176 source tree (`6267655052b209b22dc8b3421cb9339f797af9bc`,
tree-identical to merge `73f7cff6decd5ce3ebcda0c6e936c8881ccee067`).
Run `26092822382a8e` created Droplets `604485849`, `604485947` and
`604486027`, each `s-2vcpu-4gb`, with a tag-targeted firewall and a 45-minute
watchdog deadline. All three members formed a healthy cluster; serial
systemd restarts, a full three-member snapshot restore and a write with one
member stopped passed. The run started at 22:38:50 UTC and deleted the three
Droplets, firewall and tag by 22:42:15 UTC. Independent provider inventory
returned zero matching Droplets, firewalls and tags. No test resource remains.

This exercises three real hosts and the etcd recovery path. It does not
exercise the protected Fleet executor, trusted host-key enrollment, five-node
staging shape, app databases, ingress, a signed Norn candidate or the
disposable pilot retirement chain. It does not sign M3, M8 or M9. The two
pre-existing unrelated account Droplets and the default VPC were untouched.

After #177 merged, the owner requested a full disposable pilot create and
retirement. The 2026-09-29 UTC precreate audit found the personal DigitalOcean
account active with only the same two unrelated Droplets, zero firewalls and
zero load balancers. No full-pilot resource was created. The current Norn v3
head is still a draft PR with no exact signed platform release, and no fresh
run-specific cost, backend, management/fleet create, execution-fence,
retirement and final-zero approval/evidence set has been prepared. The paired
root runbook requires bootstrap and retirement review before creation. The
prior three-host etcd test is not that chain. Keep the full pilot at precreate
until the exact released candidate and both execution and teardown paths are
ready for a short supervised window.

## Mini evidence and limits

- On 2026-09-28 the reviewed inactive Mini SOPS candidate was installed as
  the active API environment after confirming it added only the exact control
  database URL and first audit-signing key. The prior ciphertext is retained
  owner-only for rollback. The installed v2 API restarted healthy with no
  active operations. A fresh `norn.legacy-control-backup/v1` artifact and
  proof under that active URL/key passed the read-only doctor; its remaining
  blocks were the reviewed script and exact signed v3 candidate release.
  The protected pair was copied to a temporary personal DigitalOcean Space,
  downloaded on a separate Mac, and restored into isolated PostgreSQL 17:
  28 public tables, 61,125 audit rows, 483 operations and 483 deployments.
  The Space retains `control/` for seven days, so this copy is not a durable
  release retention policy. The current Mini backup and health jobs also
  completed consecutive scheduled remote runs; their 15-minute freshness
  threshold is a development monitor, not an accepted release RPO. The five
  scripts now in PR #77 match the running Mini copies by SHA-256.
- The latest [control private-copy receipt](m5-mini-private-copy-d71359e8-2026-09-28.md) used a read-only Mini `pg_dump` and a socket-only disposable Postgres.app 17 cluster. Candidate code head `d71359e8` preserved 28 original tables and 262,660 rows through schema 47, passed second migration and passive startup, and matched before/after Nomad base-job and cloudflared fingerprints. This is not an off-host protected restore or live upgrade.
- The [mailindexer private-copy rehearsal](m2-mailindexer-private-shared-role-copy-2026-09-28.md) ran **locally on Mini**, not DO. It preserved 101,534 messages and both consumers' interaction writes through a copied role split and reversal. The [protected cutover plan](m2-mini-mailindexer-protected-role-cutover-plan-2026-09-28.md) remains a proposal. Running dirty image IDs and executable hashes were inventoried, but neither binary embeds a VCS revision; exact source-to-image reconciliation, recoverable local image/backup custody, complete writer inventory and both job credential changes remain.
- The installed Mini release `a5da8ef15d12e9eca7561e90b90d96f6dc652a21` lacks the v3 startup/schema contract. A direct flag probe on 2026-09-28 briefly entered its normal startup path and logged a worker start before port 8800 prevented a second listener. Follow-up found no operation or mutation-audit rows in that window and the inspected Nomad job versions unchanged; external effects cannot be excluded by those checks. The [rollback-boundary record](m5-mini-installed-legacy-rollback-boundary-2026-09-28.md) preserves the incident and the new early marker guard. Use the reviewed legacy-baseline fence, not an assumed old-binary restart after migration.

## Critical decisions and next sequence

1. **Fleet deployment priority:** #177 is merged. Qualify and publish the exact signed Norn v3 candidate, then establish the bounded test cost and retirement contract, independent authority, protected runner, state, secrets and plan for the [disposable pilot path](https://github.com/antiartificial/norn-fleet/blob/main/docs/runbooks/disposable-fleet-retirement-and-cost.md). The configured `doctl` default is the personal account. Temporary spending is owner-authorized only for a test that is actively supervised and fully torn down; a live quote and explicit run deadline are still required for the full pilot. The previous $521.40/month steady and $538.40/month peak figures model the persistent roots, not the disposable pilot pair. Obtain a live disposable quote and cap the run with a reviewed retirement deadline; a spend alert does not cap charges. Restore normal GitHub-hosted execution by resolving the billing block, while retaining the passing protected fallback receipts above. The three-host etcd exercise above passed and was torn down. Ingress/2→3→2 load behavior and the protected five-node Fleet still require rehearsal before M3/M4 sign-off. No full DO Fleet was provisioned by this work.
2. **Mini upgrade backup:** the [revised decision](m0-mini-control-recovery-decision.md) permits an on-host directory backup for this development machine. The active URL/key binding and one protected remote restore are now observed. Recreate a fresh proof for the exact maintenance window, then qualify the signed candidate, operation drain, legacy binary fence, maintenance transition and rollback decision before M5 sign-off or M9 Mini adoption. Off-host Mini backup, disaster-recovery RPO and RTO are not v3 gates. The live Mini remains on v2 at the last read-only observation; recheck it.
3. **App role transition:** reconcile dirty deployed `mail-indexer` and `mail-mcp` images with source; review both app PRs and exact Nomad specs; inventory every database writer; establish fresh, privately restored local app backup and recoverable rollback artifacts. Only then schedule the [two-consumer role cutover](m2-mini-mailindexer-protected-role-cutover-plan-2026-09-28.md) and activate a catalog that matches observed live identities.
4. **Production recovery:** assign a separate production RPO/RTO and off-host restore qualification when that environment is defined. The former ≤15-minute backup-only RPO and ≤30-minute RTO remain draft targets, not Mini or Fleet guarantees.
5. **Later release path:** implement and qualify the M6 provider-backed app database cutover and rolling upgrade, then the M7 representative app/data/work/traffic move. M8 needs a signed candidate, version matrix, client parity, soak/fault and operator evidence. M9 empty Fleet, Mini and selected-app adoptions remain separate operator changes.

Preserve the original dirty checkouts, keep local/private-copy evidence distinct
from protected runtime proof, and re-estimate milestones only when the exit
evidence materially changes.
