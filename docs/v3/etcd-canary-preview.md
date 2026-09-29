# Etcd canary promotion preview

The normal etcd runtime leaves app mutation routes unavailable by default. For
controlled qualification, set both `NORN_ETCD_CANARY_WORKER=true` and
`NORN_ETCD_CANARY_HTTP_PREVIEW=true`. The HTTP flag without the worker fails
startup. Only `POST /api/v1/apps/{id}/promote` and reads of its operation
receipt are added; other app mutations remain unavailable. The capabilities
response advertises the preview route only when both flags are enabled.

Promotion requires a managed, non-CI `api:write` token and an
`Idempotency-Key`. Acceptance resolves the root of the token rotation lineage,
signs the app, region, Nomad region, and exact deployment ID, then queues an
`app.canary-promote` operation. A duplicate key from a rotated credential
replays the original receipt before reading current app intent or Nomad state.
The worker holds an etcd operation claim and app lock while reserving the
external effect. An ambiguous Nomad response leaves the effect for exact
deployment reconciliation; a successor claim does not submit a second
promotion while the original effect is unresolved.

The disposable etcd test path is:

```sh
cd v2/api
NORN_TEST_ETCD_ENDPOINTS=http://127.0.0.1:2379 go test . ./worker ./etcdstore ./pipeline -count=1
```

These tests use fake Nomad HTTP responses. Before enabling the preview in a
release, qualify it against a real Nomad deployment and a three-member etcd
cluster, including process death after reservation and after Nomad submission,
owner-lease expiry, quorum loss, credential rotation, and restore. The two
flags are a qualification gate, not release signoff.
