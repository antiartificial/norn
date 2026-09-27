# Independent review: Claude batch one

Interim source review while implementation is still running; these findings must be rechecked against the completed batch before acceptance.

## R1: deletion and recreation can erase generation history

The initial `database.ValidateTransition` permits removing a profile, a logical mapping, or a legacy default. It also permits removing unreferenced bindings/services. Across multiple accepted transitions, this permits dropping references, deleting resources, then recreating their IDs at generation 1. A queued Expected identity from the original generation can then match a different provider target. Comparing only adjacent catalogs does not preserve historical generation authority.

For this first pure contract, fail closed on removals of identity-bearing services, bindings, profiles, mappings and legacy defaults unless an explicit persistent retirement/tombstone mechanism preserves identity history. Do not solve this only by rejecting removal and recreation in the same transition. Add a multi-transition regression: old target -> remove references -> remove resource -> recreate same IDs/generations with changed provider -> stale Expected. At least one transition must be rejected, or the final stale identity must fail using durable history.

This review does not request runtime/parser changes and does not establish an implemented tombstone protocol.

Independent reproduction: `go test ./database -run '^TestReviewRetirementCannotResetTargetGeneration$' -count=1` FAILS in review_generation_history_test.go: all four transitions accepted and the original Expected tuple resolves to a changed provider reference. The initial existing resolver race suite passed (1.864s), so its two-phase-removal success assertions miss this multi-transition identity reuse case. Preserve the regression, repair the invariant, and revise the incompatible removal expectations rather than removing the test.

Recheck: Claude added permanent retirement tombstones and the original regression now passes; complete database race suite passed independently in 2.194s before the following new regression.

## R2: legacy identity comparisons are incorrectly scoped to profile IDs

`TestReviewLegacyIdentityGenerationSurvivesProfileMove` currently FAILS: renaming a profile while retaining its MappingID permits changing TLS target policy without incrementing the mapping generation. ValidateTransition checks retained mapping existence globally but checks its fields only inside profiles with unchanged IDs. Compare legacy mappings by their identity across the entire old/new catalogs, and reject conflicting duplicate legacy MappingIDs across profiles (or require identical definitions). Preserve retirement history regardless of profile relocation. This must not be fixed just by weakening the expected target contract or deleting the regression.

## Independent verification checkpoint

Both R1 and R2 regressions now pass after Claude's corrections. Full API `go test -race ./... -skip '^TestSampleDarwinHostMetrics$' -count=1 -timeout=120s` passed with both disposable source and recovery-target database URLs set. Notable results: CLI recovery 7.260s, controlrecovery 8.388s, database 3.731s, startup 29.198s, store 8.525s. This includes actual CLI encrypted-key passive restore, not just a skipped no-database invocation. The exact existing Darwin host sampler is the sole explicit exclusion. Claude was still adding store evidence tests during this run, so rerun the final changed packages once its batch ends. This is a local verification checkpoint, not full M1/M2 or release qualification.
