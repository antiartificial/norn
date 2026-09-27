# Independent review: execution recovery batch

Interim review, recheck against final source before acceptance.

## Correction review checkpoint

The correction batch completed successfully. Independent review retained the
lock-wait expiry regression and verified the post-lock clock read. Independent
worker tests passed for ordinary-build reuse and dirty/non-git identity across
claims. The full API race suite against both disposable PostgreSQL databases
passed with only `TestSampleDarwinHostMetrics` explicitly excluded (store
10.249s, worker 8.089s, supervisor 14.292s, recovery 6.701s). `git diff --check`
also passed. The findings below are historical reproductions, now addressed
within this local slice.

Accepted as a local execution/checkpoint integration checkpoint, not M1
completion. Real Linux containment, Docker push/checkpoint crash reconciliation,
unknown-execution operator resolution, deferred checkout cleanup and multi-host
qualification remain open. Proceed to database-consumer integration rather
than treating helper tests as release qualification.

## Timeout identity and lifecycle

The initial runner `runContained` uses time.AfterFunc to issue raw `syscall.Kill(-pid, SIGKILL)`, then calls timer.Stop after command.Wait reaps the process. Stop does not join an already-running callback. A delayed callback can race with reaping and act on a reused process-group ID. Timeout termination must remain bound to the actual execution identity, not a reusable numeric PID/PGID. Prefer the independently owned execution cgroup or an equivalent lifetime-safe mechanism; do not claim process-group containment proves all descendants stopped. Add deterministic cancellation/callback lifecycle tests and confirm no timeout callback survives the execution lifecycle. Treat this separately from the existing cgroup-empty success proof.

Keep output publication ordering tests at the helper boundary: durable file before terminal status, signed length/digest, bounded capture and read, corruption and sync/write failure. A backend that merely hashes whatever file is present cannot authenticate historical output.

## Independent interim checks

Expanded `go test -race ./effect/supervisor -count=1 -timeout=60s` passed in 6.785s with runner_protocol_test.go and cgroup_test.go present. `GOOS=linux GOARCH=amd64 go test -c` also passed. These are helper/fake-filesystem and compile checks, not real Linux containment qualification or pipeline recovery acceptance. Recheck timeout lifecycle finding against final source.

## Pipeline retry must not replay preceding external stages

Initial pipeline integration restarts the full clone/admission/build/artifact-admission/test sequence on a deferred test claim. For an ordinary unbound deployment, build.go executes Docker build/push again before reaching recovery of the outstanding test. Successful stage status alone is not a complete checkpoint of its outputs; st.imageTag is only in memory at this point. The real integration test must assert the prior build invocation count remains one across pending -> new claim -> original test completion, not merely assert one test launch. Preserve/reconstruct verified prior outputs or provide an explicit supported prebound-artifact lane that rejects unsafe ordinary-build recovery without claiming general pipeline support. Also test unpinned/dirty sources: changing the subject per claim must not silently authorize repeating arbitrary test commands for the same accepted operation after an ambiguous execution.

Timeout correction checkpoint: `terminate.go` now observes leader exit without reaping, closes/joins the timeout guard, then reaps. The Linux command terminator uses an opened cgroup directory descriptor. Deterministic guard/lifecycle tests are present. Independent full supervisor race suite passed in 8.210s after these changes. This addresses the reviewed callback/reaping race at the local unit boundary; real Linux containment remains a separate qualification requirement. Pipeline replay findings remain open pending implementation and tests.

## Checkpoint ownership regression (confirmed)

`TestReviewCheckpointRejectsLeaseExpiredDuringLockWait` independently FAILS against real PG (1.14s): RecordOperationCheckpoint accepts a lease that expired while its SELECT FOR UPDATE waited. Its clock_timestamp() target expression is evaluated before the lock wait. Acquire the row lock first, then read database time in a separate statement, as the effect reservation adapter already does. Retain this deterministic test: it observes an actual waiter blocked by the holder, lets the lease expire without updating the row, and checks no checkpoint is written. Do not weaken the ownership condition or delete the test.
