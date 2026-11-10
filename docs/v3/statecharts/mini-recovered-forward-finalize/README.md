# Mini recovered-forward finalization model

This bounded TLA+ model starts from the Mini's observed
`candidate-preservation-pending` phase. It covers the separate finalization
controller path: verification of the prior signed API launcher binding,
signed ancestry and candidate evidence gates, exact preservation receipt,
Mini database-fence release, receipt-bound API plist and sidecar rebind,
normal API restart and authenticated health/source/schema readback,
host-agent restart intent and restart, and terminal receipt. The A8 launcher
may remain bound while preservation runs; the finalizer controller replaces
that binding only after the write fence is released and before normal restart.

The source ranks abstract a signed Git ancestry proof:
`AdoptionRank <= TransitionSourceRank <= CandidateRank`. The positive case uses
`0 <= 1 <= 2`, so an older adopted baseline can validly precede the journal
source and candidate. `InvalidAdoption.cfg` and `InvalidCandidate.cfg` break the
two ancestry edges independently and must hold before releasing the Mini write
fence. `NormalHealthUnavailable.cfg` allows the finalization request and
database fence release, then holds before host-agent restart if the normal API
does not pass health verification.

TLC results:

- Valid lineage, trusted prior controller and healthy normal API: 69 states / 151 transitions.
- Invalid adoption ancestry: 6 states / 12 transitions.
- Invalid transition-to-candidate ancestry: 6 states / 12 transitions.
- Normal API health unavailable: 42 states / 100 transitions.
- Untrusted prior controller binding: 3 states / 5 transitions.

All seven configured invariants and deadlock checks passed in all five cases.
Charts and reports are checked in beside each configuration. The chart keeps
one representative process-stop/recovery path; TLC checks the other crash
points too, including the plist-written/sidecar-old and rebound-sidecar/old
maintenance-process seams. The same journaled transition resumes from each
durable phase.

The model abstracts signature cryptography and concrete release SHAs, API token
custody, the generation digest and one-use controller authorization, exact
launchd path/process identities, SQL transaction durability, workload
processes, and exact HTTP response schemas. It distinguishes prior versus
finalizer controller ownership and requires the verified pair before normal
activation.
The code regression
`test_signed_descendant_can_finalize_when_journal_source_is_a_later_ancestor`
checks the real signed-ancestor finalizer gate. The model does not establish
live service health or authorize a Mini action. See the current Mini status in
[`LAUNCH-STATE.md`](../../LAUNCH-STATE.md) and the observed transition in
[`LAUNCH-HISTORY.md`](../../LAUNCH-HISTORY.md).

Reproduce the valid case with:

```sh
bun /Users/arti/.agents/skills/statechart/scripts/statechart.ts \
  "$PWD/RecoveredForwardFinalize.tla" \
  --config "$PWD/Valid.cfg" \
  --view "$PWD/view.ts" \
  --out "$PWD/reproduced-valid"
```

Run the same command with `InvalidAdoption.cfg`, `InvalidCandidate.cfg`,
`NormalHealthUnavailable.cfg`, or `UntrustedPriorController.cfg` to reproduce
each hold scenario. Choose a new empty output directory for each run.
