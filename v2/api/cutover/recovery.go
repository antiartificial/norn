package cutover

// CrashState is intentionally advisory only: no state returned here authorizes
// a phase write. A coordinator must reconcile any non-committed observation
// through its durable journal and effect readbacks before retrying work.
type CrashState string

const (
	CrashStateEffectProven CrashState = "effect-proven"
	CrashStatePending      CrashState = "pending"
	CrashStateAmbiguous    CrashState = "ambiguous"
	CrashStateConflict     CrashState = "conflict"
)

type CatalogTuple struct {
	Revision int64
	Digest   string
}
type OperationTuple struct {
	OperationID      string
	App              string
	CandidateRelease string
	IntentSHA256     string
	AuthorityEpoch   uint64
	Claim            OperationClaimBinding
	Running          bool
}
type EffectObservation struct {
	Known   bool
	Applied bool
	Proof   *VerifiedPhaseProof
}
type CrashClassification struct {
	State     CrashState
	Reconcile bool
	Reason    string
}

// ClassifyCrashState compares independently recovered control-store tuple
// values. Unknown readback, a partial effect, a stale claim, or any tuple drift
// stays ambiguous/conflicting and therefore fail closed.
func ClassifyCrashState(j Journal, catalog CatalogTuple, operation OperationTuple, observation EffectObservation) CrashClassification {
	if j.Validate() != nil || catalog.Revision != j.Intent.CatalogRevision || catalog.Digest != j.Intent.CatalogDigest || operation.OperationID != j.Intent.OperationID || operation.App != j.Intent.App || operation.CandidateRelease != j.Intent.CandidateRelease || operation.AuthorityEpoch != j.Intent.AuthorityGeneration || operation.Claim.OperationID != j.Intent.OperationID || operation.Claim.OwnerID == "" || operation.Claim.Generation == 0 {
		return CrashClassification{CrashStateConflict, true, "journal, catalog, operation, or claim tuple differs"}
	}
	digest, err := IntentSHA256(j.Intent)
	if err != nil || operation.IntentSHA256 != digest || !operation.Running {
		return CrashClassification{CrashStateConflict, true, "accepted operation no longer binds the journal"}
	}
	if !observation.Known {
		return CrashClassification{CrashStateAmbiguous, true, "external effect readback is unknown"}
	}
	if observation.Applied && (!observation.Proof.Valid() || observation.Proof.JournalRevision() != j.Revision || observation.Proof.Proof().Claim != operation.Claim) {
		return CrashClassification{CrashStateAmbiguous, true, "external effect is partial or lacks a matching verified proof"}
	}
	if observation.Applied {
		return CrashClassification{CrashStateEffectProven, true, "verified proof and effect readback match the current journal edge"}
	}
	return CrashClassification{CrashStatePending, false, "readback proves the effect was not applied"}
}
