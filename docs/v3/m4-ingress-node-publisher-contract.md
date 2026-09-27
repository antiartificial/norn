# M4 ingress node publisher contract

Status: proposed implementation boundary, not a deployed service or a release
gate. This contract connects the existing canonical route renderer and local
generation-fenced file publisher to Fleet's loopback Traefik readback. The
normal etcd app deployment route stays disabled until the complete path is
qualified.

## Authority and host boundary

- Fleet must install one narrowly scoped publisher on every ingress host. It
  writes only `norn-route-<32 lowercase hex>.yaml` below
  `/etc/traefik/dynamic`; the existing TLS and readback files are outside its
  write scope. The service must run under an identity authorized to write this
  directory, with no provider, OpenTofu, or general Nomad credentials.
- Norn's accepted deployment and live operation claim are the source of route
  intent. A caller cannot supply an unbound hostname, backend, weight, route
  generation, or withdrawal. The publisher authenticates the caller and checks
  the accepted intent plus current operation authority before each local
  mutation. A bearer token, SSH access, or a file generation alone is not
  sufficient authority for app traffic changes.
- The transport between the control worker and every ingress host must be
  private and mutually authenticated. Fleet must pin each host identity and
  install a separate publisher credential; the existing loopback
  `/api/rawdata` router remains local to each ingress host. A remote caller
  cannot use that unauthenticated loopback route as a control endpoint.

## Publish transaction

1. Resolve the exact accepted deployment, route hostname, revision-specific
   Consul backend names, desired weights, and current route generation from
   durable control state. Compare the live claim and app lock before issuing
   a generation. Record the desired route digest and generation durably before
   fanout, so retry uses the same identity after an uncertain reply.
2. Send the same canonical route and generation to the complete ingress node
   set from one exact Fleet inventory revision. Each host invokes
   `PublishRenderedRoute` with its previously read local revision. A replay of
   the identical generation and digest is idempotent; a stale or divergent
   revision is a conflict. A withdrawal uses
   `WithdrawPublishedRoute` and retains its tombstone generation.
3. Read each host's published file revision and Traefik's effective
   `/api/rawdata`. Require the exact router, weighted service, TLS mode,
   enabled backends, and weights on every node. Probe the expected app endpoint
   through every ingress node and through normal public DNS. Include a
   withdrawal/public 404 check for rollback. A partial publish, unavailable
   host, competing public router, config error, probe mismatch, or unknown
   Fleet inventory member leaves active weight at zero and retains the app
   gate for recovery.
4. Persist one proof tied to deployment ID, region, accepted intent digest,
   route digest and generation, Fleet inventory revision and complete node IDs,
   per-node file/readback/probe results, and public probe result. The terminal
   deployment transaction must compare this proof's durable revision and
   authority before it can write a positive `ActiveWeight`. The current
   `finishClaimedDeployment` caller-supplied region value is insufficient.

## Recovery and qualification

After a lost response, reread durable intent and each node's file/effective
route before retrying the same generation. Never issue a newer generation or
release the app gate while an older publish may still be active. If nodes
disagree, repair or withdraw under a newer authorized generation, then repeat
all readback and probes. A process crash after the last node write but before
proof persistence must be reconciled by observation, not a blind second route
change.

The first protected test needs two separate ingress hosts, one route revision
at a time, old/new revision-specific backends, a public load balancer, an
induced one-node publish failure, and an interrupted publisher recovery. The
test must demonstrate that no positive `ActiveWeight` or terminal success is
recorded until every node and the public path agree. Local Traefik fixtures
and Fleet draft PR #177 establish only composition and loopback readback.
