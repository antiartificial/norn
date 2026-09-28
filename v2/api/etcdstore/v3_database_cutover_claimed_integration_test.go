package etcdstore

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"norn/v2/api/cutover"
	"norn/v2/api/database"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

func TestEtcdClaimedCutoverJournalBindsSignedIntentClaimAndAppLock(t *testing.T) {
	adapter, _, _ := privateInvocationEtcdStore(t)
	ctx := context.Background()
	intent := etcdTestCutoverIntent()
	catalog := postgresCatalogFixture()
	catalog.Services[0].ID = "mini"
	catalog.Services[0].Recovery.Capabilities = []database.Capability{database.CapabilityRuntime, database.CapabilitySnapshot, database.CapabilityRestore}
	targetService := catalog.Services[0]
	targetService.ID, targetService.ProviderRef = "fleet", "local:fleet"
	catalog.Services = append(catalog.Services, targetService)
	catalog.Bindings[0].ID, catalog.Bindings[0].ServiceID, catalog.Bindings[0].Database, catalog.Bindings[0].Role = "old", "mini", "appdb", "runtime"
	targetBinding := catalog.Bindings[0]
	targetBinding.ID, targetBinding.ServiceID, targetBinding.CredentialRef = "new", "fleet", "secret:target"
	catalog.Bindings = append(catalog.Bindings, targetBinding)
	catalog.Profiles[0].ID = "mini"
	catalog.Profiles[0].DatabaseBindings = map[string]string{"appdb": "old"}
	catalog.Profiles = append(catalog.Profiles, database.DeploymentProfile{APIVersion: database.APIVersion, ID: "fleet", Topology: database.DeploymentTopologyLocal, AvailabilityClass: database.AvailabilitySingleHost, DatabaseBindings: map[string]string{"appdb": "new"}})
	activeCatalog, err := adapter.ActivatePostgresDatabaseCatalog(ctx, 0, catalog, "operator")
	if err != nil {
		t.Fatal(err)
	}
	intent.CatalogRevision, intent.CatalogDigest = activeCatalog.Revision, activeCatalog.Digest
	digest, err := cutover.IntentSHA256(intent)
	if err != nil {
		t.Fatal(err)
	}
	a := store.OperationAcceptance{
		Identity:  store.OperationRequestIdentity{Authority: adapter.authority, Actor: store.OperationActor{Issuer: "test", Subject: "operator"}, Kind: store.DatabaseCutoverOperationKind, Resource: "app/fixture/database/appdb", Key: "first-cutover"},
		Operation: model.Operation{ID: intent.OperationID, Kind: store.DatabaseCutoverOperationKind, App: intent.App, Ref: intent.CandidateRelease, Source: "test", Risk: "write", Payload: map[string]interface{}{"cutoverIntentSha256": digest}, MaxAttempts: 1},
		Audit:     store.AcceptanceAuditContext{Source: "test", Scopes: []string{"database:cutover"}},
		Admission: store.OperationAdmissionPolicy{OneActiveMutablePerApp: true},
	}
	a.Fingerprint, err = store.CanonicalOperationRequestFingerprint(a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Accept(ctx, a); err != nil {
		t.Fatal(err)
	}
	claimed, claim, err := adapter.ClaimNextOperation(ctx, "cutover-worker", time.Minute, []string{store.DatabaseCutoverOperationKind})
	if err != nil || claimed == nil || claimed.ID != intent.OperationID {
		t.Fatalf("claim %+v: %v", claimed, err)
	}
	lock, acquired, err := adapter.AcquireAppOperationLock(ctx, intent.App)
	if err != nil || !acquired {
		t.Fatalf("app lock acquired=%v err=%v", acquired, err)
	}
	defer lock.Release()
	j, err := adapter.PrepareClaimedDatabaseCutoverJournal(ctx, claim, lock, intent)
	if err != nil || j.Revision != 1 {
		t.Fatalf("prepare %+v: %v", j, err)
	}
	if _, err := adapter.PrepareClaimedDatabaseCutoverJournal(ctx, claim, lock, intent); err != nil {
		t.Fatalf("exact replay: %v", err)
	}
	changed := intent
	changed.Target.BindingGeneration++
	if _, err := adapter.PrepareClaimedDatabaseCutoverJournal(ctx, claim, lock, changed); !errors.Is(err, store.ErrOperationOwnershipLost) && !errors.Is(err, errEtcdCutoverJournalConflict) {
		t.Fatal("retargeted signed intent accepted")
	}
	stale, err := store.NewOperationClaim(claim.OperationID(), claim.OwnerID(), claim.Generation()+1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.AdvanceClaimedDatabaseCutoverJournal(ctx, stale, lock, 1, cutover.PhaseQuiesce, strings.Repeat("b", 64)); !errors.Is(err, store.ErrOperationOwnershipLost) {
		t.Fatal("stale claim advanced journal")
	}
	wrongLock := store.NewFencedAppOperationLock(ctx, "not-the-held-app-lock", nil)
	defer wrongLock.Release()
	if _, err := adapter.AdvanceClaimedDatabaseCutoverJournal(ctx, claim, wrongLock, 1, cutover.PhaseQuiesce, strings.Repeat("b", 64)); !errors.Is(err, errEtcdCutoverJournalConflict) {
		t.Fatal("unowned app lock advanced journal")
	}
	if _, err := adapter.AdvanceClaimedDatabaseCutoverJournal(ctx, claim, lock, 1, cutover.PhaseActivate, strings.Repeat("b", 64)); !errors.Is(err, errEtcdCutoverJournalConflict) {
		t.Fatal("skipped activation accepted")
	}
	advanced, err := adapter.AdvanceClaimedDatabaseCutoverJournal(ctx, claim, lock, 1, cutover.PhaseQuiesce, strings.Repeat("b", 64))
	if err != nil || advanced.Phase != cutover.PhaseQuiesce || advanced.Revision != 2 {
		t.Fatalf("advance %+v: %v", advanced, err)
	}
	if err := adapter.FinishClaimedOperationWithAppLock(ctx, claim, lock, model.OperationSucceeded, "done", nil); err == nil {
		t.Fatal("generic success bypassed cutover gate")
	}
}
