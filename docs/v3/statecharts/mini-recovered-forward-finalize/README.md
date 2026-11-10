# Mini recovered-forward finalization model

This bounded TLA+ model starts from the Mini's observed
`candidate-preservation-pending` phase. It covers the separate finalization
controller path: signed ancestry and candidate evidence gates, exact
preservation receipt, Mini database-fence release, normal API restart and
authenticated health/source/schema readback, host-agent restart intent and
restart, and terminal receipt.

The source ranks abstract a signed Git ancestry proof:
`AdoptionRank <= TransitionSourceRank <= CandidateRank`. The positive case uses
`0 <= 1 <= 2`, so an older adopted baseline can validly precede the journal
source and candidate. `InvalidAdoption.cfg` and `InvalidCandidate.cfg` break the
two ancestry edges independently and must hold before releasing the Mini write
fence. `NormalHealthUnavailable.cfg` allows the finalization request and
database fence release, then holds before host-agent restart if the normal API
does not pass health verification.

TLC results:

- Valid lineage and healthy normal API: 52 states / 96 transitions.
- Invalid adoption ancestry: 3 states / 5 transitions.
- Invalid transition-to-candidate ancestry: 3 states / 5 transitions.
- Normal API health unavailable: 25 states / 45 transitions.

All six configured invariants and deadlock checks passed in all four cases.
Charts and reports are checked in beside each configuration. The chart keeps
one representative process-stop/recovery path; TLC checks the other crash
points too. The same journaled transition resumes from each durable phase.

The model abstracts signature verification, API token custody, the generation
digest and one-use controller authorization, launchd process identity, SQL
transaction durability, workload processes, and exact HTTP response schemas.
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

Run the same command with `InvalidAdoption.cfg`, `InvalidCandidate.cfg`, or
`NormalHealthUnavailable.cfg` to reproduce each hold scenario. Choose a new
empty output directory for each run.
