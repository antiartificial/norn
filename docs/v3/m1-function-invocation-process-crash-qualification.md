# M1 function invocation process-crash qualification

`TestClaimedFunctionV3WorkerProcessCrashNomadPostgres` is an opt-in,
disposable PostgreSQL and loopback Nomad qualification for the claimed v3
function path.

The test accepts a signed private invocation through the HTTP handler, then
starts a separate claimed-function worker process through a local Nomad proxy.
The proxy forwards exactly one create-only `/v1/jobs` registration to Nomad and
holds the worker's first exact allocation read. At that point the operation's
variable and job effect attempts are durable, and the live one-shot job exists,
but no terminal receipt has been written. The parent kills that worker with
`SIGKILL`, expires only its disposable PostgreSQL claim lease, and starts two
replacement worker processes through the same proxy.

The proxy rejects a second registration before it can reach Nomad. The test
also requires one live job version and one-version history after recovery.
The winning successor must re-identify the durable job, observe its terminal
allocation, and publish the succeeded operation receipt. This is the normal
recovery route; it does not simulate an operation or use a fake remote effect.

Run it only against disposable services:

```sh
NORN_TEST_NOMAD_ADDR=http://127.0.0.1:4646 \
NORN_TEST_NOMAD_DOCKER=1 \
NORN_TEST_FUNCTION_IMAGE='busybox@sha256:<local-or-published-content-digest>' \
NORN_TEST_DATABASE_URL='postgres://postgres:<test-password>@127.0.0.1:<port>/norn_test?sslmode=disable' \
go test ./api -run '^TestClaimedFunctionV3WorkerProcessCrashNomadPostgres$' -count=1 -v
```

The test is not a full deployment qualification. It leaves protected GitHub,
OIDC, real multi-replica control APIs, and production backup/restore evidence
to their respective release gates.
