# Resizing the Norn HA fleet (node-pool grow / shrink)

How to safely grow and shrink capacity, what the ha-lab supports today, and what
still needs building. Written from the pilot on 2026-09-18.

## The core constraint: the ha-lab is flat

Every ha-lab droplet is simultaneously a **Consul server**, a **Nomad
server + client**, and a **Patroni Postgres** member (`scripts/inventory` puts
every host in `consul_servers`, `nomad_servers`, `nomad_clients`,
`postgres_cluster`). There is **no separate server vs. workload role**, so *every*
resize is a Raft-quorum change. Consequences:

- Valid sizes are **odd only, >= 3** (`terraform/variables.tf` enforces it): 3 -> 5 -> 7.
- You cannot add a single node, and you cannot grow workload capacity without
  also growing the quorum.

The `norn-fleet` **intent model** (`cluster.yaml`) does separate roles —
`control` (Raft quorum, `max 5`), `ingress`, and `app` (`max 8`, the pool
designed to scale) — with a `replacement` block (`strategy`,
`requireCapacityHeadroom`, `drainTimeout`, `requireReadiness`). Its drain program
(`norn-fleet-drain.sh`) **refuses to auto-drain a `control` node**. That is the
right long-term shape; the ha-lab cannot express it yet.

## Quorum-safety rules (apply to every resize)

- **Consul + Nomad are Raft.** Keep an **odd** voter count; a write quorum of
  `floor(N/2)+1` must survive every single step. **Never remove more than one
  voter at a time**, and never drop below 3.
- **Never touch a current leader.** Reuse the `scripts/lab replace-member` leader
  guard: read the Consul/Nomad/Patroni/Norn leaders first and skip them.
- **Patroni** = 1 primary + N replicas. Add/remove **replicas** freely; **never
  remove the primary** without a controlled `patronictl switchover` first.
- **Grow is the safe direction** (quorum only rises, autopilot promotes new
  voters). **Shrink is the dangerous direction** and must drain + remove-peer
  before destroy.

## Scale UP

### ha-lab today (works, manual)
1. Edit `terraform/terraform.tfvars`: `node_count` 3 -> **5** (odd).
2. `scripts/lab plan` — confirm the plan **only creates** `-04`/`-05` (+ LB /
   firewall / project membership), with **no** changes to `-01..-03`.
3. `scripts/lab apply`.
4. `scripts/pki issue-node <prefix>-04` and `-05` (per-node certs; see
   `scripts/replace-member`).
5. `scripts/lab converge` — new nodes join Consul/Nomad via `retry_join` +
   autopilot, bootstrap Patroni replicas, and `site.yml` asserts all counts == 5.
   `bootstrap_expect` re-renders to 5 (a no-op on already-bootstrapped servers).
6. `scripts/lab status` — expect 5 Consul members, 5 Nomad peers, 5 Patroni
   members, one leader.

### Intent model (the safe, hands-off path)
Raise the pool's `desired` in `cluster.yaml` -> PR -> `norn fleet plan` ->
protected `apply`. Grow is non-destructive, so the phase contract runs
`infrastructure_applied -> inventory_generated -> nodes_configured ->
nodes_enrolled -> readiness_verified -> complete`. `requireCapacityHeadroom` and
`requireReadiness` gate it. Prefer growing the **`app`/`ingress`** pools (no
quorum impact); grow `control` only 3->5, never by one.

## Scale DOWN / drain

### ha-lab today — DOES NOT EXIST. Do **not** `terraform apply` a lower node_count.
A naive `node_count` 5->3 destroys the highest-index droplets with **no drain, no
raft peer removal, no Patroni removal**, leaving dead voters and orphan members.
Until tooling exists, the **manual safe order** (one node at a time, highest
index first, never a leader):
1. `scripts/lab status` — identify all leaders; never pick one.
2. On the target: `nomad node drain -self -enable -deadline 15m`; wait for
   allocations to reschedule.
3. If it is the Patroni primary: `patronictl switchover`, confirm the new leader.
4. `consul operator raft remove-peer` **and** `nomad operator raft remove-peer`
   for the node; `patronictl remove` the member.
5. Verify the remaining (odd) quorum is healthy with a leader.
6. Only then lower `node_count` and `scripts/lab apply` — confirm the plan
   destroys **only** that node.

### Intent model (implemented contraction/drain lane)
Lower `desired` -> PR -> `norn fleet plan` (destructive; `contraction_policy.py`
binds the plan to exact existing node names and refuses anything else) ->
protected `apply` inserts `old_nodes_drained` before deletion. Per node,
`norn-fleet-drain.sh`: refuses `control`; withdraws ingress from the LB first
(`systemctl stop traefik`, wait ~40s for the LB to deselect); `nomad node drain
-deadline <drainTimeout>`; polls until drained; `trap rollback` re-enables on
failure. `require_exact_reviewed_surplus` proves capacity headroom before
draining; `readiness_verified` reruns before the SHA-bound delete. `recover.yml`
can resume **only while all reviewed nodes still exist**.

## Recommendation for the next pilot

1. **Exercise the safe direction first** on the intent-model pilot: grow then
   shrink the **`app`/`ingress`** pool (e.g. 2 -> 4 -> 2). This drives the full
   non-destructive expansion and the contraction/drain lane end to end **without
   ever touching quorum** — the lowest-risk demonstration of `drainTimeout` /
   `requireReadiness` / headroom.
2. **Keep control-pool resize manual and gated** until a blue/green
   quorum-voter generation model exists. Do not automate a 5->3 control shrink.
3. **For the ha-lab**, the highest-value additions (in order): (a) a
   `scripts/lab grow` wrapper — plan-then-converge an odd `node_count` bump with
   an "only creates new nodes" plan assertion (cheap; reuses converge); (b) a
   `scripts/lab shrink` — port the drain semantics (`nomad node drain -deadline`,
   `consul`/`nomad operator raft remove-peer`, `patronictl switchover`/`remove`,
   the leader guard, a single-node-delete plan assertion). The ha-lab already has
   every quorum-safety primitive it needs (leader detection, single-target plan
   assertion, `serial: 1` converge, stale-client reconciliation) — shrink is the
   one missing lane.
