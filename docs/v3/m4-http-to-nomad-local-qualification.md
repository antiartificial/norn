# M4 staging HTTP release to disposable Nomad

The opt-in `TestEtcdFleetStagingReleaseHTTPToDisposableNomad` passed locally on
2026-09-27 against disposable loopback etcd, Consul 2.0.4, and Nomad 2.0.7.
It accepted a staging release through the normal HTTP handler, claimed its
signed `app.deploy` operation, staged managed job inputs, observed a healthy
real Docker allocation, and confirmed that the operation remained nonterminal
without ingress proof. The allocation served the InfraSpec's `/ready` response
through its assigned local host port; the test checked HTTP 200 and the exact
`ready` body. The artifact verifier in this test is synthetic.

Run from the Norn repository root on a Docker-capable Mac with `etcd`,
`consul`, `nomad`, `docker`, `go`, and Python 3 available:

```sh
docker pull docker.io/library/busybox@sha256:73aaf090f3d85aa34ee199857f03fa3a95c8ede2ffd4cc2cdb5b94e566b11662
python3 v2/api/scripts/qualify-v3-http-nomad.py
```

The script checks that the exact image is cached, starts the three local
agents on free loopback ports, runs the named-database HTTP admission test and
the opt-in Nomad test, and stops the agents.
The Nomad test uses a unique app and etcd prefix, then purges its job. Docker
Desktop may delay release of a root-owned Nomad allocation log directory; if
the script prints a disposable-state cleanup path, remove only that path after
the script exits.

This result qualifies the normal local admission-to-Nomad path and a direct
app endpoint response. It does not verify a real signed artifact, publish to
ingress nodes, probe the public endpoint, prove weighted traffic, or qualify
protected Fleet hosts. Those remain M4 release gates.

First Fleet release admission now requires an exact traffic probe in the
checked-out InfraSpec. This prevents an accepted release from reaching the
worker without the signed path and response digest needed for terminal
traffic proof. The local Nomad fixture carries that probe declaration and
checks the direct allocation response; it does not probe through ingress.
The claimed worker also rechecks this requirement before preparing Nomad
inputs, including for operations accepted before the admission rule changed.
The normal executor now requires a completed active Fleet ingress inventory
and an exact match between its node IDs and configured private certificate
identities before database resolution, Nomad input staging, or job submission.
The route intent still rechecks inventory after Nomad health. This preflight
has unit coverage for missing, extra, repeated, and wrong node identities.
The HTTP-to-Nomad fixture now also passes its claimed HTTP-accepted operation
into the normal executor with no completed inventory: it refuses the release
before opening the route listener. The fixture then directly exercises the
Nomad effect to keep the allocation-health gate qualified. A completed
inventory and protected ingress nodes have not been exercised through this
normal executor path.
