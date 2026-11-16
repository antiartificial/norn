# Recover and qualify controller operations

Recovery starts by identifying the operation and the last proven effect.
Process liveness, a queued job, or a lock file does not establish completion.

## Inspect before repeating effects

1. Locate the exact intent, workflow run/attempt, journal, and retained receipt.
2. Check the actual worker, provider/runtime identity, and readback evidence.
3. Classify the operation as unstarted, running, terminal, or ambiguous.
4. Resume only the same-intent path justified by that evidence. Hold ambiguous
   effects until readback establishes what happened.

Fresh admission and historical inspection have different time requirements.
Historical verification can accept an expired intent for inspection while
retaining its signature, digest, source, and identity checks. That inspection
does not grant authority for a new dispatch or mutation.

## Include cleanup in success

A controller is not fully qualified when its central mutation succeeds but its
terminal path remains untested. Verify successful cleanup through the actual
filesystem helpers, subprocess environments, retained artifact readers, and
runner deregistration. Exercise failure, pre-start cancellation, lost response,
expiry, and restart at the boundaries that the controller supports.

The live pilot needed terminal-only repairs after the accepted upgrade. These
preserved the original operation and evidence rather than replaying the upgrade.
Worker and observer cleanup completed before Fleet retirement.

## Reuse the lessons

Use the Fleet [qualification rubric](https://github.com/antiartificial/norn-fleet/blob/main/docs/runbooks/release-qualification.md)
and candidate proof record for new controllers. The useful sequence is:

1. Model state transitions and map each critical rule to a production check.
2. Compose the actual producer, transformations, signatures, installed
   consumers, journal, and terminal reader with synthetic external transport.
3. Reproduce real user IDs, legacy paths, clean environments, and process hops.
4. Inject restart and lost responses before an expensive live rehearsal.
5. Run live checks for the remaining provider, network, and continuity claims.

Put incident chronology in dated audits. Put reusable constraints in validators
or regressions, then rerun only the evidence affected by a change. A test count
or model that assumes valid inputs cannot qualify the actual assembled package.
