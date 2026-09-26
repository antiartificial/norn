# Disposable three-member etcd Fleet qualification

Run `v2/scripts/test-etcd-three-member-fleet` from a machine with Docker,
OpenSSL, etcdctl, `timeout`, and Go. It uses the pinned
`quay.io/coreos/etcd:v3.5.17` image already present locally, creates a private
Docker network and temporary certificates, and removes all of its containers,
network, snapshot, credentials, and test data on exit. It does not connect to
Mini, Fleet, a provider, or an existing etcd endpoint.

The script creates three TLS client/peer members, enables etcd authentication,
and grants a non-root API principal write access only to the test prefix. It
runs `TestEtcdFleetRuntimeProductionTLSRBACProcess` against the three-member
cluster, then repeats it while one member is detached from the Docker network
and again after stopping one member. The detached member is reconnected and
all endpoints pass health before the stopped-member stage. That test builds and starts
the normal Norn API binary in production mode with both an absent and a
poisoned PostgreSQL DSN; bootstraps a managed token; accepts and replays a
signed Fleet plan; checks the operation receipt; rejects a Fleet runner's
broader reads and an out-of-prefix etcd principal; and verifies unsupported
app routes remain unavailable.

The script stops a second member and requires a linearizable etcd read and
write to fail without quorum. After bringing the cluster back, it saves a
snapshot, discards all three member data directories, restores each member
with `etcdutl` from that snapshot, verifies an authenticated sentinel survived, and reruns
the normal API process test against the restored group.

On 2026-09-26, this disposable run passed twice on PR #76 head `c718de3`,
including the isolated-member partition and rejoin, the API process test
before loss, with one member down, and after full snapshot restore. The first
run left an empty disposable Docker network after the containers stopped; it
was removed. The cleanup now retries network removal and preserves a failed
test's exit status. The second run left no qualification containers or network.
This is evidence for the local transport, auth, quorum, and restore path. It
does not qualify host-supervised Fleet membership changes, asymmetric or
multi-host partitions, disk/full-quota behavior, certificate rotation,
one-member-at-a-time upgrades, soak, or a live Fleet restore. Those remain
M3 release gates.
