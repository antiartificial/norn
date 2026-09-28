# M4 staging HTTP release to disposable Nomad

The opt-in `TestEtcdFleetStagingReleaseHTTPToDisposableNomad` passed locally on
2026-09-27 against disposable loopback etcd, Consul 2.0.4, and Nomad 2.0.7.
It accepted a staging release through the normal HTTP handler, claimed its
signed `app.deploy` operation, staged managed job inputs, observed a healthy
real Docker allocation, and confirmed that the operation remained nonterminal
without ingress proof. The allocation served the InfraSpec's `/ready` response
through its assigned local host port; the test checked HTTP 200 and the exact
`ready` body. By default, artifact verification in this test is synthetic.
The fixture submits the job to real Nomad, discards the successful submit
response, then defers the original claim. A successor claim reconstructs the
same signed job plan and observes the original job through allocation health.
The wrapper counts one registration attempt and Nomad's job version remains
zero. This simulates a lost response with a controlled claim handoff; a real
process crash and its timing window remain unqualified.

Run from the Norn repository root on a Docker-capable Mac with `etcd`,
`consul`, `nomad`, `docker`, `go`, and Python 3 available:

```sh
docker pull docker.io/library/busybox@sha256:73aaf090f3d85aa34ee199857f03fa3a95c8ede2ffd4cc2cdb5b94e566b11662
python3 v2/api/scripts/qualify-v3-http-nomad.py
```

For the two-ingress extension, provide a Traefik binary and its exact SHA-256
pin. The 2026-09-27 run used Traefik 3.7.13 Darwin/arm64 extracted from an
archive that matched the published release checksum; the extracted binary had
the following digest:

```sh
NORN_TEST_TRAEFIK_BINARY=/absolute/path/to/traefik \
NORN_TEST_TRAEFIK_SHA256=97511eb5f2b4edd7a7bd78d4401d8e988e41628be5aac1e0a73c15ae9722160d \
python3 v2/api/scripts/qualify-v3-http-nomad.py
```

Set `NORN_TEST_VERIFY_REGISTRY=1` on that command to use Norn's actual
`docker buildx imagetools inspect` digest check for the public BusyBox image.
The combined registry, Nomad, and two-ingress rehearsal passed locally on
2026-09-27. Signature and vulnerability checks remain synthetic in this
fixture, so this is not signed release-artifact qualification.

The script checks that the exact image is cached, starts the three local
agents on free loopback ports, runs the named-database HTTP admission test and
the opt-in Nomad test, and stops the agents.
The Nomad test uses a unique app and etcd prefix, then purges its job. Docker
Desktop may delay release of a root-owned Nomad allocation log directory; if
the script prints a disposable-state cleanup path, remove only that path after
the script exits.

With `NORN_TEST_TRAEFIK_BINARY` set, the script starts two independent
loopback Traefik processes, each with a watched route directory and a
disposable TLS certificate trusted by the test. The accepted deployment's
Nomad service supplies the revision-specific backend through local Consul.
Publishing the generation-one route on only the first ingress must fail the
two-node observation; after publishing the same route on the second, both
Traefik readbacks and both HTTPS `/ready` probes must pass. Withdrawing
generation two on only the first ingress must fail the two-node withdrawal
observation; after withdrawing on the second, both readbacks pass and the
public host returns HTTPS 404 through each local ingress. The disposable
etcd, Consul, Nomad, Docker, and two-Traefik run passed on 2026-09-27.

This result qualifies the normal local admission-to-Nomad path and a direct
app endpoint response. The optional extension also qualifies a local
two-ingress route from that real allocation and rejects partial publication
and withdrawal.
It does not verify a real signed artifact, use the authenticated host publisher
or durable route authority, probe a public load balancer, prove weighted
traffic, or qualify protected Fleet hosts. Those remain M4 release gates.

First Fleet release admission now requires an exact traffic probe in the
checked-out InfraSpec. This prevents an accepted release from reaching the
worker without the signed path and response digest needed for terminal
traffic proof. The local Nomad fixture carries that probe declaration and
checks the direct allocation response. The optional two-ingress extension
also probes that response through both local Traefik processes.
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

`TestTwoNodePublisherTransportPartialRetry` exercises the exported publisher
preflight and publication clients over real mutual TLS listeners on two
loopback node addresses. A client with another verified identity fails
preflight. When the second node refuses authority, the first returns a file
receipt and the second retains no route; after authority is restored, a retry
leaves both node files at generation one with the expected route digest.
This closes the isolated client-to-publisher transport rehearsal. It does not
join the accepted HTTP operation, completed Fleet inventory, Nomad effect,
Traefik readback or public endpoint into one normal executor run.

The joined fixture needs two distinct private IPv4 addresses: the production
Fleet inventory parser rejects loopback, IPv6, duplicate IPs and public IPs.
The current macOS runner has only one locally bindable private IPv4 address;
its two loopback TLS listeners therefore cannot be substituted for a completed
Fleet inventory. Use an isolated Linux private network with two addressable
ingress nodes for the next normal-executor rehearsal, keeping the same
inventory validator and exact-node identity checks. This is a fixture
requirement, not permission to provision or change a protected Fleet.
