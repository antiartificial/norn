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

The 2026-09-28 implementation estimates are M0 **60%**, M1 **65%**, M2
**55%**, M3 **50%**, M4 **40%**, M5 **25%**, M6 **10%**, M7 **5%**, M8
**10%**, M9 **0%**. Their equal-weight mean is about **32%**. These are
judgment estimates, not elapsed-time forecasts. **0 of 10 release gates are
signed**. Recent local rehearsals and green CI did not change the estimates
or sign a gate. The revised Mini backup scope does not itself raise any
percentage or sign M0/M5.

## Exact source and review state

- Norn integration checkout: `/Users/arti/Documents/Codex/2026-09-26/i-d/work/v3-master-integration`, branch `codex/v3-master-integration`. Draft [PR #77](https://github.com/antiartificial/norn/pull/77) targets `master`; [Repository CI](https://github.com/antiartificial/norn/actions/runs/36474675281) and [Norn CI](https://github.com/antiartificial/norn/actions/runs/36474675167) both passed at `4e4d56595da0ea30ce785b1ae430f776c5b94c78`. Only this current session handoff remains checked in; earlier session and topic handoffs are retained in Git history. Recheck the exact PR head and checks after this documentation revision. This is review and local qualification, not an installed release.
- Fleet host-etcd draft [PR #176](https://github.com/antiartificial/norn-fleet/pull/176) head `6267655052b209b22dc8b3421cb9339f797af9bc`, based on `main`. Ingress/readback draft [PR #177](https://github.com/antiartificial/norn-fleet/pull/177) head `c66fdae05d683781b890ca1827335fb8c18f55e8`, based on #176's branch. Both exact-head GitHub `contract` checks passed on the documented repository-scoped ephemeral Linux x64 fallback ([#176 run](https://github.com/antiartificial/norn-fleet/actions/runs/36285145209), [#177 run](https://github.com/antiartificial/norn-fleet/actions/runs/36477152422)). The temporary runner auto-removed and the repository switch was deleted. Normal GitHub-hosted execution still reports an account billing/spending-limit block. PR #177 declares the staging project, VPC/droplet/load-balancer membership, separate two-node application PostgreSQL and pilot MySQL, ingress-tag database firewalls and the runner SSH-key preflight. Staging rejects example SSH fingerprints and documentation CIDRs before a plan. Both first-create roots reject IPv6-only SSH sources because the Droplets have no IPv6 bootstrap route; the management offline preflight rejects an IPv6 executor before backend initialization. Isolated empty-state plans proposed 21 staging and 19 management creates, zero changes or destroys, using review-only values and no live provider token. The management tag-targeted bootstrap firewall precedes both hosts, and the staging firewall precedes its nodes. No provider change occurred. CI and local rehearsals do not establish protected-host etcd, database or ingress qualification.
- `mail-indexer` draft [PR #1](https://github.com/antiartificial/mail-indexer/pull/1) head `0a217e2a890900151de4ca298cb7630dfd25ee8f` and `mail-mcp` draft [PR #1](https://github.com/antiartificial/mail-mcp/pull/1) head `eec3db157a8cbcebcbde48bd30491256ce90c4bd` are based on their GitHub masters. Both local full Go suites passed; each hosted `test` job failed before runner steps from the same account billing block. Neither app PR is merged or deployed. Mini's app source checkouts contain unrelated dirty work; preserve them.

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

1. **Fleet deployment priority:** review and integrate PR #176 before stacked #177, then settle the exact account, complete cost ceiling, management authority, runner, state, secrets and protected plan in the [Fleet readiness review](https://github.com/antiartificial/norn-fleet/blob/codex/fleet-ingress-readback/docs/runbooks/staging-v3-protected-rehearsal-readiness-2026-09-28.md). The configured `doctl` default is the personal account; the spending ceiling remains an owner input. The provisional staging, management and two-application-database subtotal is $521.40/month before backup, monitoring, transfer and temporary capacity, and requires a live provider quote. Restore normal GitHub-hosted execution by resolving the billing block, while retaining the passing protected fallback receipts above. Rehearse three separate Linux hosts, etcd restore/faults and ingress/2→3→2 load behavior before M3/M4 sign-off. No DO Fleet resource was provisioned by this work.
2. **Mini upgrade backup:** the [revised decision](m0-mini-control-recovery-decision.md) permits an on-host directory backup for this development machine. The active URL/key binding and one protected remote restore are now observed. Recreate a fresh proof for the exact maintenance window, then qualify the signed candidate, operation drain, legacy binary fence, maintenance transition and rollback decision before M5 sign-off or M9 Mini adoption. Off-host Mini backup, disaster-recovery RPO and RTO are not v3 gates. The live Mini remains on v2 at the last read-only observation; recheck it.
3. **App role transition:** reconcile dirty deployed `mail-indexer` and `mail-mcp` images with source; review both app PRs and exact Nomad specs; inventory every database writer; establish fresh, privately restored local app backup and recoverable rollback artifacts. Only then schedule the [two-consumer role cutover](m2-mini-mailindexer-protected-role-cutover-plan-2026-09-28.md) and activate a catalog that matches observed live identities.
4. **Production recovery:** assign a separate production RPO/RTO and off-host restore qualification when that environment is defined. The former ≤15-minute backup-only RPO and ≤30-minute RTO remain draft targets, not Mini or Fleet guarantees.
5. **Later release path:** implement and qualify the M6 provider-backed app database cutover and rolling upgrade, then the M7 representative app/data/work/traffic move. M8 needs a signed candidate, version matrix, client parity, soak/fault and operator evidence. M9 empty Fleet, Mini and selected-app adoptions remain separate operator changes.

Preserve the original dirty checkouts, keep local/private-copy evidence distinct
from protected runtime proof, and re-estimate milestones only when the exit
evidence materially changes.
