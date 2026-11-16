# Upgrade an existing Fleet online

The qualified lane updates Norn on compatible control nodes while an existing
workload remains available. It does not resize application pools or upgrade
the whole scheduler, host OS, or application database stack.

Use the exact-source [Fleet release runbook](https://github.com/antiartificial/norn-fleet/blob/main/docs/runbooks/work-staging-norn-release.md)
for commands and protected inputs. This page explains the operator sequence
and its acceptance boundary.

## Before admitting effects

1. Pin the old/new signed releases, executor/workflow source, Fleet state,
   provider/host identities, and owner authority.
2. Verify the assembled inputs with production consumers. Check the installed
   module closure, process environments, directory ownership, and every
   actor's real network route.
3. Read control-store/component health and prove exactly one active API owner.
4. Start independent workload/API observation with explicit outage and sample
   gap limits. Retain evidence outside the Fleet.
5. Admit a fresh signed operation only after these checks pass.

A published artifact, green CI, and an HTTP health response each prove narrower
facts. They do not replace live admission or continuity evidence.

## Rotate the controls

1. Update a passive control and verify its target artifact and health.
2. Repeat for the other passive control while preserving quorum and service.
3. Update the former active API owner last and verify a single healthy owner
   after handoff.
4. Validate all target pins and the observation policy before accepting success.

The 2026-11-16 pilot rotated Control 01 and Control 02 before Control 03.
API ownership moved from Control 03 to Control 01. Application allocations
continued serving; application nodes were not rotated in this operation.

## Finish the operation

Retain the authenticated terminal artifact and receipt. Prove terminal replay,
clear the completed active journal, retire one-use authority and runner
registration, and stop the operation's observer. Preserve the inputs and
historical receipts needed for future inspection.

If a response is lost or a step is ambiguous, follow [recovery](recovery.md).
Do not dispatch a new operation to conceal an unfinished one.

## Qualified result and limits

[Live run 43852466595](https://github.com/antiartificial/norn-fleet/actions/runs/43852466595)
ended `online-qualified` for signed `fa301fa5` to `cdf2081e`. Its accepted window
contains 399 official observer samples and 673 separate successful public
probes. Nine later public-probe timeouts occurred outside that window during
the longer monitor. The result is measured evidence for this operation, not a
promise of zero downtime under every failure.

The pilot is now retired. Future runs need a live Fleet and fresh admission;
this procedure does not recreate it.
