# Offline effect evidence source v2

This adapter is a default-off trust boundary for the fixed work-staging
`app-03` offline lifecycle. The runner can trigger an observation, but it
cannot submit effect, terminal, or completion facts. Norn first reads the exact
v2 reservation, admitted attempt, and grant from its own etcd store. It then
performs an authenticated `GET` against a separately operated read-only
verifier and exact-compares the response with that server-owned state.

The verifier protocol uses these paths:

```text
GET /v2/offline-effects/{planID}/{attemptID}/effect
GET /v2/offline-effects/{planID}/{attemptID}/{grantID}/terminal
GET /v2/offline-effects/{planID}/{attemptID}/{grantID}/completion
```

Every response uses
`norn.fleet-offline-independent-effect-evidence/v2`. It carries the exact
binding, target fence, authority epoch, PostgreSQL writer fence, provider
account and droplet identity, reviewed backend object, latest backend object,
all-namespace workload facts, evidence digests, and observation times. Norn
requires:

- effect: the exact writer generation is held by the attempt; the exact
  provider target and backend target are still present; the workload target is
  absent after drain; the latest versioned backend identity is exactly the
  reviewed identity; surviving placement, headroom, and service checks pass;
- terminal: the same writer generation remains held; the provider and workload
  target remain absent; a newer object version in the same backend lineage is
  observed; all evidence digests are present;
- completion: the exact writer generation is released; the release receipt is
  bound to the recorded terminal evidence; provider absence, newer backend
  identity, and workload facts are observed again.

## Activation gate

The adapter remains nil unless all three settings are present:

```text
NORN_FLEET_OFFLINE_EFFECT_V2_ENABLED=true
NORN_FLEET_OFFLINE_EFFECT_V2_VERIFIER_URL=https://...
NORN_FLEET_OFFLINE_EFFECT_V2_TOKEN_FILE=/owner-only/path
```

The URL must be HTTPS, except for loopback test fixtures. The token file must
be a regular owner-only file and is independent from the reservation verifier
credential. Partial configuration fails startup. Enabling offline reservation
admission does not enable this effect source.

The repository does not include the upstream verifier service or live
PostgreSQL, DigitalOcean, versioned object-store, Nomad, and Consul connectors.
The adapter must therefore remain disabled in work staging until that service
has a read-only credential for each system, returns this exact protocol, and
passes a qualification that covers stale writer generations, wrong provider
accounts, backend lineage/version drift, partial namespace inspection, stale
release receipts, redirects, timeouts, and credential revocation. The included
HTTP fixtures establish Norn-side protocol and fail-closed behavior only; they
are not live infrastructure evidence.
