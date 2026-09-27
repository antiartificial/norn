# Next Claude batch: execution recovery review

Source review checkpoint, 2026-09-22. Do not start this batch concurrently with the database/recovery batch. This supplements effect-recovery-handoff.md; it is not acceptance evidence.

## Confirmed in current source

- `runner_protocol.go`: the helper closes result.bin but does not sync it before publishing durable terminal status. The signed status binds neither result length nor digest. Output goes directly to an unbounded file. Decode errors are wrapped verbatim and request decoding does not require EOF.
- `cgroup_linux.go`: RetrieveResult performs Stat then ReadFile, so its 16 MiB check does not actually bound the read of a changed file. It does not authenticate output against terminal status. Observe then accepts this output when the cgroup is empty. Implement bounded single-open reads and signed terminal result integrity; test replacement, truncation, oversized output, sync/write failure, and terminal publication ordering.
- The cgroup parser now rejects populated values other than 0 or 1. Preserve this fix; add malformed/missing/duplicate-field fixtures rather than treating the previously reported invalid-value bug as still unfixed.
- Missing cgroup or missing status in an empty group currently returns Unknown, not NeverLaunched. Preserve that fail-closed boundary.
- Parent and Wait goroutine both close runner.log. Review descriptor ownership, but do not conflate closing the parent's descriptor after spawn with closing the child's inherited descriptor. Test actual lifecycle behavior before claiming output loss.

## Required coherent implementation outcome

Complete supervisor integrity, then wire one explicitly supported pipeline effect end-to-end with durable deferral and exact original execution recovery. Include the real PostgreSQL claim-1 pending -> claim-2 resume -> exactly one terminal publication test, plus conflicting operation/resource blocking. Pending must not first mark the deployment/saga failed. A contained stopped process is not proof that its remote effects can safely be repeated. Installation/configuration and protocol compatibility must be explicit; no direct-exec fallback that bypasses fencing.

Add loss/tamper/concurrent-initialization tests for the root registration ledger and exact Execute replay after a launched execution's journal is deleted. Recreating a missing journal must never produce NeverLaunched for prior execution. Keep Darwin containment unqualified and Docker/BuildKit outside this adapter until their downstream effects have a supported contract.

Linux cross-compilation proves compilation only. Real cgroup-v2 runtime acceptance requires a scoped environment; do not modify host cgroups or launch privileged containers without a separately authorized scope.
