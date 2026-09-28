package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"norn/v2/api/cutover"
	"norn/v2/api/database"
	"norn/v2/api/model"
)

func TestClaimedDatabaseCutoverJournalRequiresSignedIntentAndLiveClaim(t *testing.T) {
	stores, dbs := acceptanceIntegrationStores(t, 1)
	ctx := context.Background()
	acceptance, db := stores[0], dbs[0]
	catalog := storeTestCatalog()
	catalog.Services[0].ID, catalog.Services[1].ID = "mini", "fleet"
	for i := range catalog.Services {
		catalog.Services[i].Recovery.Capabilities = []database.Capability{database.CapabilityRuntime, database.CapabilitySnapshot, database.CapabilityRestore}
	}
	catalog.Bindings = []database.DatabaseBinding{
		{APIVersion: database.APIVersion, ID: "old", ServiceID: "mini", Database: "appdb", Role: "runtime", Generation: 1, CredentialRef: "secret:old", TLS: database.DatabaseTLS{Mode: database.TLSDisabled}},
		{APIVersion: database.APIVersion, ID: "new", ServiceID: "fleet", Database: "appdb", Role: "runtime", Generation: 1, CredentialRef: "secret:new", TLS: database.DatabaseTLS{Mode: database.TLSDisabled}},
	}
	catalog.Profiles[0].DatabaseBindings = map[string]string{"appdb": "old"}
	catalog.Profiles[0].LegacyPostgres = nil
	catalog.Profiles = append(catalog.Profiles, database.DeploymentProfile{APIVersion: database.APIVersion, ID: "fleet", Topology: database.DeploymentTopologyLocal, AvailabilityClass: database.AvailabilitySingleHost, DatabaseBindings: map[string]string{"appdb": "new"}})
	activeCatalog, err := db.ActivateDatabaseCatalog(ctx, 0, catalog, "operator")
	if err != nil {
		t.Fatal(err)
	}
	a := newAcceptance(t, acceptance, "cutover-once", "operator", "fixture", false)
	a.Identity.Kind, a.Identity.Resource = DatabaseCutoverOperationKind, "app/fixture/database/appdb"
	a.Operation.Kind, a.Operation.Ref, a.Operation.MaxAttempts, a.Operation.Risk = DatabaseCutoverOperationKind, "release-1", 1, "write"
	intent := cutover.Intent{SchemaVersion: "norn.database-cutover/v2", OperationID: a.Operation.ID, App: "fixture", LogicalDatabase: "appdb", CandidateRelease: a.Operation.Ref, CatalogRevision: activeCatalog.Revision, CatalogDigest: activeCatalog.Digest, SourceProfileID: "mini", TargetProfileID: "fleet", AuthorityGeneration: 3, WriterInventorySHA256: strings.Repeat("a", 64),
		Source: database.TargetIdentity{ServiceID: "mini", ServiceGeneration: 1, BindingID: "old", BindingGeneration: 1, Engine: database.EnginePostgreSQL, Database: "appdb", Role: "runtime"},
		Target: database.TargetIdentity{ServiceID: "fleet", ServiceGeneration: 1, BindingID: "new", BindingGeneration: 1, Engine: database.EnginePostgreSQL, Database: "appdb", Role: "runtime"}}
	digest, err := cutover.IntentSHA256(intent)
	if err != nil {
		t.Fatal(err)
	}
	a.Operation.Payload = map[string]interface{}{"cutoverIntentSha256": digest}
	a.Admission.OneActiveMutablePerApp = true
	a.Fingerprint, err = CanonicalOperationRequestFingerprint(a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acceptance.Accept(ctx, a); err != nil {
		t.Fatal(err)
	}
	claimed, claim, err := db.ClaimNextOperation(ctx, "cutover-worker", time.Minute, []string{DatabaseCutoverOperationKind})
	if err != nil || claimed == nil || claimed.ID != intent.OperationID {
		t.Fatalf("claim %+v: %v", claimed, err)
	}
	j, err := db.PrepareClaimedDatabaseCutoverJournal(ctx, acceptance, claim, intent)
	if err != nil || j.Phase != cutover.PhasePrepare || j.Revision != 1 {
		t.Fatalf("prepare %+v: %v", j, err)
	}
	if _, err := db.PrepareClaimedDatabaseCutoverJournal(ctx, acceptance, claim, intent); err != nil {
		t.Fatalf("exact replay: %v", err)
	}
	changed := intent
	changed.Target.BindingGeneration++
	if _, err := db.PrepareClaimedDatabaseCutoverJournal(ctx, acceptance, claim, changed); !errors.Is(err, errDatabaseCutoverJournalConflict) {
		t.Fatal("changed signed intent accepted")
	}
	stale, err := NewOperationClaim(claim.OperationID(), claim.OwnerID(), claim.Generation()+1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.PrepareClaimedDatabaseCutoverJournal(ctx, acceptance, stale, intent); !errors.Is(err, ErrOperationOwnershipLost) {
		t.Fatal("stale claim accepted")
	}
	quiesceEvidence, err := cutover.NewPhaseEvidenceReference(j, cutover.PhaseQuiesce, strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	activateEvidence := quiesceEvidence
	activateEvidence.NextPhase = cutover.PhaseActivate
	wrongEvidence := quiesceEvidence
	wrongEvidence.IntentSHA256 = strings.Repeat("0", 64)
	if _, err := db.AdvanceClaimedDatabaseCutoverJournal(ctx, acceptance, claim, 1, wrongEvidence); !errors.Is(err, errDatabaseCutoverJournalConflict) {
		t.Fatal("evidence for a different intent advanced journal")
	}
	if _, err := db.AdvanceClaimedDatabaseCutoverJournal(ctx, acceptance, stale, 1, quiesceEvidence); !errors.Is(err, ErrOperationOwnershipLost) {
		t.Fatal("stale claim advanced journal")
	}
	if _, err := db.AdvanceClaimedDatabaseCutoverJournal(ctx, acceptance, claim, 1, activateEvidence); !errors.Is(err, errDatabaseCutoverJournalConflict) {
		t.Fatal("claimed operation skipped quiescence and final sync")
	}
	advanced, err := db.AdvanceClaimedDatabaseCutoverJournal(ctx, acceptance, claim, 1, quiesceEvidence)
	if err != nil || advanced.Phase != cutover.PhaseQuiesce || advanced.Revision != 2 {
		t.Fatalf("claimed advance %+v: %v", advanced, err)
	}
	readback, _, err := db.loadDatabaseCutoverJournal(ctx, intent.OperationID)
	if err != nil || readback.EvidenceReferences[cutover.PhaseQuiesce] != quiesceEvidence {
		t.Fatalf("claimed evidence reference was not retained: %+v err=%v", readback.EvidenceReferences, err)
	}
	if _, err := db.ActivateDatabaseCatalog(ctx, activeCatalog.Revision, catalog, "operator"); err != nil {
		t.Fatalf("activate newer catalog: %v", err)
	}
	finalEvidence, err := cutover.NewPhaseEvidenceReference(advanced, cutover.PhaseFinalSync, strings.Repeat("c", 64))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.AdvanceClaimedDatabaseCutoverJournal(ctx, acceptance, claim, 2, finalEvidence); !errors.Is(err, cutover.ErrTransition) {
		t.Fatalf("catalog drift advanced final sync: %v", err)
	}
	if err := db.FinishClaimedOperation(ctx, claim, model.OperationSucceeded, "cutover done", nil); err == nil {
		t.Fatal("generic success bypassed cutover gate")
	}
}
