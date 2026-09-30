# Bounded Fleet fitness pilot matrix — 2026-09-29

This matrix defines the minimum useful live evidence for the next disposable
run after `pilot260929g`. That epoch stopped before compute and cannot execute
the matrix. Bind the matrix to the fresh run's signed cutoff and retire-by, and
stop optional testing early enough to preserve a conservative retirement
margin.

The next run uses the existing paired disposable management/Fleet roots and
normal bootstrap described in the [operator plan](fleet-next-epoch-operator-sequence-2026-09-29.md).
Three cloud control nodes run etcd, Norn, Consul and Nomad; two ingress nodes
run Nomad clients. Pin Norn to the signed v3 candidate `8ac21f8e`. The abandoned
external-Mac topology did not run cloud Norn or etcd and cannot supply HA
credit for this matrix.

Prior-run result: `pilot260929g` stopped before compute because its signed
GitHub App evidence path was noncanonical. Its state backend, five node keys,
local controller/routes/watchdog and offline runner are now closed; signed
partial-precreate final-zero verified at 23:50:32Z, and the original Tailscale
policy was restored. No compute, load balancer, managed database or live Fleet
was created. This cleanup contributes no fitness result to the rows below.

| Area | Minimum exercise | Required evidence |
| --- | --- | --- |
| Protected execution | Complete the exact reviewed plan/apply from protected Fleet `main` and its bound Norn attempt. | Terminal workflow, numbered attempt and successful phase checkpoints agree with the reviewed commit, plan digest, state and provider inventory. |
| Norn HA control store | Form the exact three-member mTLS/RBAC etcd cluster, prove authenticated Norn reads/capabilities and a separately labeled etcd CAS marker, stop one control member, prove continued quorum and Norn API service, then restore healthy membership. | Exact signed Norn and etcd versions, endpoint/member health, empty alarms, bootstrap-role removal, runtime-role access and read/API and etcd marker receipts before/during/after interruption. Direct etcd CAS is not Norn application-write evidence. |
| Scheduler and discovery quorum | Form three TLS/ACL Consul servers and three TLS/ACL Nomad servers on the `control` Droplets. Stop one server at a time, prove both quorums remain healthy, then restore it and prove healthy rejoin. | Consul and Nomad Raft membership, leader/quorum health and exact node identities before, during and after the interruption. This is not etcd or Norn control-store evidence. |
| Runtime placement | Enroll all five declared nodes; verify the two ingress Nomad clients are ready in the `ingress` pool and Docker is healthy. | Provider inventory, generated inventory, Tailscale enrollment receipts, Nomad node status and Consul membership reconcile. Cloud Norn attempt/checkpoints must bind the protected execution and reconcile with the etcd control-store evidence. |
| Ingress | Probe the trusted public HTTPS hostname through both ingress members; withdraw or stop one ingress member and verify continued service, then restore it. | TLS identity, load-balancer health, per-ingress publication/readback and public response evidence. |
| Representative workload | Deploy the approved digest-pinned `hello-norn-mysql` fixture, run its migration once, place exactly two healthy allocations on the reviewed ingress nodes, and prove routed write/read. Restart one allocation or node and confirm service and data continuity without a duplicate migration. | Artifact/attestation bindings, job and allocation identities, database result, public/private probes and absence of duplicate migration effects. |
| Retirement readiness | Remove the fixture and its run-scoped Nomad variable, token, policy and namespace; retain the companion cleanup receipt before infrastructure retirement. | Workload absence and exact identity revocation bound into the retirement inputs. |
| Final zero | Fence execution, retire Fleet before management, clean retained backends and collect the final typed inventories. | Root/state/provider zero, runner and workflow closure, DNS absence, Tailscale device/key absence, restored policy digest, final-zero receipt and separate billing review. |

The paired disposable Fleet root fixes ingress at two nodes, so this run does
not prove the broader M4 `2→3→2` capacity gate. Snapshot creation is optional if
time remains; destructive restore, credential rotation, rolling upgrades and
soak remain separate M3 qualification. A failed readiness or evidence check
stops fitness expansion and moves the run to its reviewed recovery or
retirement path. Reserve the final hour for retirement.
