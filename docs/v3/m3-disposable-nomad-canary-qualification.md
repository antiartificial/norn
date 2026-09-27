# Disposable Nomad canary promotion qualification

Run `TestEtcdCanaryHTTPPromotesRealNomadDeployment` only against a disposable
local Nomad agent and local etcd. The test rejects non-loopback endpoints,
registers a uniquely named `raw_exec` service job, waits for a healthy base
deployment, then registers a canary update and waits for the canary allocation
to be healthy. It sends the promotion through managed-token HTTP admission,
signed etcd acceptance, and the etcd operation worker. It verifies the exact
Nomad deployment was promoted and that an idempotent replay returns the same
operation. Cleanup purges the job and deletes only the test's etcd prefix.

Example local setup (Nomad `raw_exec` must be enabled and `/bin/sh` available):

```sh
nomad agent -dev -config=/path/to/disposable-agent.hcl
docker run -d --name norn-canary-qual-etcd -p 127.0.0.1:12379:2379 \
  quay.io/coreos/etcd:v3.5.17 /usr/local/bin/etcd \
  --listen-client-urls=http://0.0.0.0:2379 \
  --advertise-client-urls=http://127.0.0.1:2379
cd v2/api
NORN_TEST_NOMAD_ADDR=http://127.0.0.1:14646 \
NORN_TEST_ETCD_ENDPOINTS=http://127.0.0.1:12379 \
go test . -run '^TestEtcdCanaryHTTPPromotesRealNomadDeployment$' -count=1 -v
```

The 2026-09-24 run used local Nomad 2.0.7 and etcd 3.5.17. The test passed
with a real healthy canary allocation and a real Nomad promotion. An earlier
run sent the request before the canary allocation was healthy. Nomad rejected
the promotion with HTTP 500 (`0/1 healthy allocations`), and the accepted
Norn operation remained queued with `externalEffectRecoveryPending` for 45
seconds even after the canary became healthy. Admission and recovery for this
case motivated an admission guard: a new request is rejected until every
placed canary allocation is healthy. A canary can still lose health after
admission and before Nomad accepts the promotion; that race and its durable
effect recovery remain an open release gap. This is a local single-node qualification.
It does not prove process-crash windows,
three-member etcd recovery, live Nomad behavior, or release readiness.
