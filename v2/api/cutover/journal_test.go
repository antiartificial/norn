package cutover

import (
	"errors"
	"strings"
	"testing"

	"norn/v2/api/database"
)

func testIntent() Intent {
	return Intent{SchemaVersion: "norn.database-cutover/v2", OperationID: "op-1", App: "fixture", LogicalDatabase: "appdb", CandidateRelease: "sha256:release", CatalogRevision: 1, CatalogDigest: strings.Repeat("f", 64), SourceProfileID: "mini", TargetProfileID: "fleet", AuthorityGeneration: 7, WriterInventorySHA256: strings.Repeat("a", 64),
		Source: database.TargetIdentity{ServiceID: "mini", ServiceGeneration: 1, BindingID: "old", BindingGeneration: 2, Engine: database.EnginePostgreSQL, Database: "appdb", Role: "runtime"},
		Target: database.TargetIdentity{ServiceID: "fleet", ServiceGeneration: 1, BindingID: "new", BindingGeneration: 1, Engine: database.EnginePostgreSQL, Database: "appdb", Role: "runtime"}}
}

func TestVerifyCatalogRequiresExactLogicalBindingsAndRevision(t *testing.T) {
	intent := testIntent()
	service := func(id string, capabilities []database.Capability) database.DatabaseService {
		return database.DatabaseService{APIVersion: database.APIVersion, ID: id, Generation: 1, Purpose: database.PurposeApplication, Engine: database.EnginePostgreSQL, EngineVersion: "16", ProviderRef: "local:" + id,
			Endpoint: database.DatabaseEndpoint{Host: "127.0.0.1", Port: 5432}, Topology: database.DatabaseTopology{Mode: database.TopologyLocalShared, AvailabilityClass: database.AvailabilitySingleHost},
			TLS: database.DatabaseTLSPolicy{MinimumMode: database.TLSDisabled}, Recovery: database.RecoveryPolicy{Capabilities: capabilities}}
	}
	catalog := database.Catalog{APIVersion: database.APIVersion,
		Services: []database.DatabaseService{service("mini", []database.Capability{database.CapabilityRuntime, database.CapabilitySnapshot}), service("fleet", []database.Capability{database.CapabilityRuntime, database.CapabilityRestore})},
		Bindings: []database.DatabaseBinding{
			{APIVersion: database.APIVersion, ID: "old", ServiceID: "mini", Database: "appdb", Role: "runtime", Generation: 2, CredentialRef: "secret:old", TLS: database.DatabaseTLS{Mode: database.TLSDisabled}},
			{APIVersion: database.APIVersion, ID: "new", ServiceID: "fleet", Database: "appdb", Role: "runtime", Generation: 1, CredentialRef: "secret:new", TLS: database.DatabaseTLS{Mode: database.TLSDisabled}},
		},
		Profiles: []database.DeploymentProfile{
			{APIVersion: database.APIVersion, ID: "mini", Topology: database.DeploymentTopologyLocal, AvailabilityClass: database.AvailabilitySingleHost, DatabaseBindings: map[string]string{"appdb": "old"}},
			{APIVersion: database.APIVersion, ID: "fleet", Topology: database.DeploymentTopologyLocal, AvailabilityClass: database.AvailabilitySingleHost, DatabaseBindings: map[string]string{"appdb": "new"}},
		},
	}
	check := func(i Intent, revision int64, c database.Catalog, want bool) {
		t.Helper()
		err := VerifyCatalog(i, revision, intent.CatalogDigest, c)
		if (err == nil) != want {
			t.Fatalf("VerifyCatalog success=%v want=%v err=%v", err == nil, want, err)
		}
	}
	check(intent, 1, catalog, true)
	check(intent, 2, catalog, false)
	retargeted := catalog
	retargeted.Profiles = append([]database.DeploymentProfile(nil), catalog.Profiles...)
	retargeted.Profiles[1].DatabaseBindings = map[string]string{"appdb": "old"}
	check(intent, 1, retargeted, false)
	missingRestore := catalog
	missingRestore.Services = append([]database.DatabaseService(nil), catalog.Services...)
	missingRestore.Services[1].Recovery.Capabilities = []database.Capability{database.CapabilityRuntime}
	check(intent, 1, missingRestore, false)
}

func TestJournalRejectsStaleAndOutOfOrderPromotion(t *testing.T) {
	j, err := New(testIntent())
	if err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("b", 64)
	if _, err := j.Advance(1, PhaseActivate, digest); !errors.Is(err, ErrTransition) {
		t.Fatal("activation skipped source fence and final sync")
	}
	if _, err := j.Advance(0, PhaseQuiesce, digest); !errors.Is(err, ErrTransition) {
		t.Fatal("zero revision accepted")
	}
	for _, next := range []Phase{PhaseQuiesce, PhaseFinalSync, PhaseActivate, PhaseVerify, PhaseAccept} {
		old := j
		j, err = j.Advance(j.Revision, next, digest)
		if err != nil {
			t.Fatalf("advance %s: %v", next, err)
		}
		if _, err := old.Advance(j.Revision, next, digest); !errors.Is(err, ErrTransition) {
			t.Fatal("stale writer accepted")
		}
		if len(old.Receipts) != int(old.Revision-1) {
			t.Fatal("advance mutated prior journal")
		}
	}
	if _, err := j.Advance(j.Revision, PhaseAccept, digest); !errors.Is(err, ErrTransition) {
		t.Fatal("accepted journal advanced again")
	}
}

func TestJournalRejectsIdentityAndEvidenceDrift(t *testing.T) {
	intent := testIntent()
	intent.Target = intent.Source
	if _, err := New(intent); !errors.Is(err, ErrTransition) {
		t.Fatal("same target accepted")
	}
	intent = testIntent()
	intent.Target.Engine = database.EngineMySQL
	if _, err := New(intent); !errors.Is(err, ErrTransition) {
		t.Fatal("mixed engine accepted")
	}
	j, _ := New(testIntent())
	if _, err := j.Advance(1, PhaseQuiesce, "not-a-digest"); !errors.Is(err, ErrTransition) {
		t.Fatal("unbound evidence accepted")
	}
	j, _ = j.Advance(1, PhaseQuiesce, strings.Repeat("c", 64))
	delete(j.Receipts, PhaseQuiesce)
	if _, err := j.Advance(2, PhaseFinalSync, strings.Repeat("d", 64)); !errors.Is(err, ErrTransition) {
		t.Fatal("missing prior receipt accepted")
	}
}

func TestJournalReadbackRejectsForgedPhaseOrMissingHistory(t *testing.T) {
	j, _ := New(testIntent())
	if err := j.Validate(); err != nil {
		t.Fatal(err)
	}
	j.Phase = PhaseActivate
	j.Revision = 4
	if err := j.Validate(); !errors.Is(err, ErrTransition) {
		t.Fatal("activation without source fence or final sync receipts passed readback")
	}
	j, _ = New(testIntent())
	j, _ = j.Advance(1, PhaseQuiesce, strings.Repeat("d", 64))
	j.Receipts[PhasePrepare] = strings.Repeat("e", 64)
	if err := j.Validate(); !errors.Is(err, ErrTransition) {
		t.Fatal("extra receipt passed readback")
	}
}

func TestPhaseEvidenceReferenceBindsIntentRevisionPhaseAndPriorReceipt(t *testing.T) {
	j, err := New(testIntent())
	if err != nil {
		t.Fatal(err)
	}
	ref, err := NewPhaseEvidenceReference(j, PhaseQuiesce, strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*PhaseEvidenceReference){
		"intent":   func(r *PhaseEvidenceReference) { r.IntentSHA256 = strings.Repeat("b", 64) },
		"revision": func(r *PhaseEvidenceReference) { r.FromRevision++ },
		"phase":    func(r *PhaseEvidenceReference) { r.NextPhase = PhaseActivate },
		"evidence": func(r *PhaseEvidenceReference) { r.EvidenceSHA256 = "missing" },
		"prior":    func(r *PhaseEvidenceReference) { r.PriorReceiptSHA256 = strings.Repeat("c", 64) },
	} {
		changed := ref
		change(&changed)
		if _, err := j.AdvanceWithEvidence(changed); !errors.Is(err, ErrTransition) {
			t.Fatalf("%s evidence drift accepted: %v", name, err)
		}
	}
	j, err = j.AdvanceWithEvidence(ref)
	if err != nil {
		t.Fatal(err)
	}
	if j.Phase != PhaseQuiesce || j.Receipts[PhaseQuiesce] == ref.EvidenceSHA256 {
		t.Fatal("reference was not hashed into receipt")
	}
	next, err := NewPhaseEvidenceReference(j, PhaseFinalSync, strings.Repeat("d", 64))
	if err != nil {
		t.Fatal(err)
	}
	if next.PriorReceiptSHA256 != j.Receipts[PhaseQuiesce] {
		t.Fatal("prior receipt missing from chain")
	}
	if _, err := j.AdvanceWithEvidence(next); err != nil {
		t.Fatal(err)
	}
}
