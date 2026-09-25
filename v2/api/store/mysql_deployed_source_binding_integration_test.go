package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"norn/v2/api/database"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
)

type mysqlSourceObserverFunc func(context.Context, nomad.MySQLSourceJobObservationRequest) (nomad.MySQLSourceJobObservation, error)

func (f mysqlSourceObserverFunc) ObserveMySQLSourceJob(ctx context.Context, request nomad.MySQLSourceJobObservationRequest) (nomad.MySQLSourceJobObservation, error) {
	return f(ctx, request)
}

func TestVerifySignedDeployedMySQLSourceBinding(t *testing.T) {
	stores, dbs := acceptanceIntegrationStores(t, 1)
	acceptedStore, db := stores[0], dbs[0]
	ctx := context.Background()
	specDigest := "sha256:" + strings.Repeat("a", 64)
	source := database.TargetIdentity{ServiceID: "mysql-primary", ServiceGeneration: 3, BindingID: "wordpress-primary", BindingGeneration: 5, Engine: database.EngineMySQL, Database: "wordpress", Role: "wordpress_runtime"}
	maintenance := database.MySQLMaintenanceCredentials{Generation: 1, RuntimeAccountHost: "%", SnapshotRole: "snapshot", SnapshotAccountHost: "%", SnapshotCredentialRef: "secret:snapshot",
		RestoreRole: "restore", RestoreAccountHost: "%", RestoreCredentialRef: "secret:restore", FenceRole: "fence", FenceAccountHost: "%", FenceCredentialRef: "secret:fence"}
	catalog := storeTestCatalog()
	catalog.Services = append(catalog.Services, database.DatabaseService{APIVersion: database.APIVersion, ID: source.ServiceID, Generation: source.ServiceGeneration,
		Purpose: database.PurposeApplication, Engine: database.EngineMySQL, EngineVersion: "8.4", ProviderRef: "local:mysql-primary",
		Endpoint: database.DatabaseEndpoint{Host: "127.0.0.1", Port: 3306}, Topology: database.DatabaseTopology{Mode: database.TopologyLocalShared, AvailabilityClass: database.AvailabilitySingleHost},
		TLS: database.DatabaseTLSPolicy{MinimumMode: database.TLSDisabled}, Recovery: database.RecoveryPolicy{Capabilities: []database.Capability{database.CapabilitySnapshot, database.CapabilityRestore}}})
	catalog.Bindings = append(catalog.Bindings, database.DatabaseBinding{APIVersion: database.APIVersion, ID: source.BindingID, ServiceID: source.ServiceID,
		Database: source.Database, Role: source.Role, Generation: source.BindingGeneration, CredentialRef: "secret:runtime", TLS: database.DatabaseTLS{Mode: database.TLSDisabled}, MySQLMaintenance: &maintenance})
	catalog.Profiles[0].DatabaseBindings["primary"] = source.BindingID
	active, err := db.ActivateDatabaseCatalog(ctx, 0, catalog, "source-admission-test")
	if err != nil {
		t.Fatal(err)
	}
	targetSet := mysqlDeployedTargetSet{Schema: mysqlDeployedDatabaseTargetsSchema, ProfileID: "mini", CatalogRevision: active.Revision,
		Targets: []mysqlDeployedNamedTarget{{Name: "primary", Target: source}}}
	encoded, err := json.Marshal(targetSet)
	if err != nil {
		t.Fatal(err)
	}
	input := newAcceptance(t, acceptedStore, "mysql-deployed-source", "operator", "wordpress", true)
	input.Deployment.SpecDigest = specDigest
	input.Operation.Payload["specDigest"] = specDigest
	input.Operation.Payload["databaseTargets"] = string(encoded)
	input.Fingerprint, err = CanonicalOperationRequestFingerprint(input)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := acceptedStore.Accept(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE operations SET status='succeeded',finished_at=clock_timestamp() WHERE id=$1`, accepted.Operation.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE deployments SET status='deployed',spec_digest=$2,finished_at=clock_timestamp() WHERE id=$1`, accepted.Deployment.ID, specDigest); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE deployment_regions SET status='deployed',active_weight=desired_weight WHERE deployment_id=$1 AND region='west' AND nomad_region='global'`, accepted.Deployment.ID); err != nil {
		t.Fatal(err)
	}
	request := MySQLDeployedSourceBindingRequest{DeploymentID: accepted.Deployment.ID, App: "wordpress", SpecDigest: specDigest,
		Region: "west", NomadRegion: "global", ProfileID: "mini", LogicalID: "primary", CatalogRevision: active.Revision, Source: source}
	binding, err := db.VerifySignedDeployedMySQLSourceBinding(ctx, acceptedStore, request)
	if err != nil {
		t.Fatal(err)
	}
	wantDigest := sha256.Sum256([]byte(mysqlDeployedDatabaseTargetsSchema + "\x00" + string(encoded)))
	if binding.OperationID != accepted.Operation.ID || binding.AcceptanceIntentID != accepted.AcceptanceIntentID || binding.DeploymentID != accepted.Deployment.ID ||
		binding.DatabaseBindingSchema != mysqlDeployedDatabaseTargetsSchema || binding.DatabaseBindingSHA256 != hex.EncodeToString(wantDigest[:]) || binding.Source != source {
		t.Fatalf("binding=%+v", binding)
	}
	observer := mysqlSourceObserverFunc(func(_ context.Context, want nomad.MySQLSourceJobObservationRequest) (nomad.MySQLSourceJobObservation, error) {
		return nomad.MySQLSourceJobObservation{App: want.App, DeploymentID: want.DeploymentID, SpecDigest: want.SpecDigest, Region: want.Region,
			NomadRegion: want.NomadRegion, JobID: want.App, JobVersion: "8", JobModifyIndex: "44", AllocationIDs: []string{"alloc-1"},
			DatabaseBindingSchema: want.DatabaseBindingSchema, DatabaseBindingSHA256: want.DatabaseBindingSHA256, DatabaseCatalogRevision: want.DatabaseCatalogRevision}, nil
	})
	snapshot, err := db.BuildMySQLSourceSnapshotRequest(ctx, acceptedStore, observer, MySQLSourceSnapshotAdmissionRequest{Binding: request, Maintenance: maintenance, DumpToolSHA256: strings.Repeat("d", 64)})
	if err != nil || snapshot.JobIdentity.DeploymentID != accepted.Deployment.ID || snapshot.JobIdentity.JobVersion != "8" || snapshot.JobIdentity.JobModifyIndex != "44" {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	wrongMaintenance := maintenance
	wrongMaintenance.SnapshotCredentialRef = "secret:unbound-snapshot"
	noObservationForInvalidMaintenance := mysqlSourceObserverFunc(func(context.Context, nomad.MySQLSourceJobObservationRequest) (nomad.MySQLSourceJobObservation, error) {
		t.Fatal("unbound maintenance identity reached Nomad observation")
		return nomad.MySQLSourceJobObservation{}, nil
	})
	if _, err := db.BuildMySQLSourceSnapshotRequest(ctx, acceptedStore, noObservationForInvalidMaintenance,
		MySQLSourceSnapshotAdmissionRequest{Binding: request, Maintenance: wrongMaintenance, DumpToolSHA256: strings.Repeat("d", 64)}); !errors.Is(err, ErrMySQLSourceSnapshotFence) {
		t.Fatalf("unbound snapshot credential was accepted: %v", err)
	}
	snapshotAcceptanceInput := MySQLSourceSnapshotAcceptanceInput{
		Selection: MySQLSourceSnapshotAdmissionRequest{Binding: request, Maintenance: maintenance, DumpToolSHA256: strings.Repeat("d", 64)},
		Actor:     OperationActor{Issuer: "test-issuer", Subject: "operator"}, Key: "source-snapshot-one",
		Audit: AcceptanceAuditContext{Source: "private-test", RequestID: "snapshot-request-one"},
	}
	acceptedSnapshot, err := db.AcceptPrivateMySQLSourceSnapshot(ctx, acceptedStore, observer, snapshotAcceptanceInput)
	if err != nil || acceptedSnapshot.Operation.Status != model.OperationQueued || acceptedSnapshot.Operation.MaxAttempts != 1 ||
		!sameMySQLSourceSnapshotPayload(acceptedSnapshot.Operation.Payload, snapshot) {
		t.Fatalf("signed source snapshot acceptance=%+v err=%v", acceptedSnapshot, err)
	}
	noObservation := mysqlSourceObserverFunc(func(context.Context, nomad.MySQLSourceJobObservationRequest) (nomad.MySQLSourceJobObservation, error) {
		t.Fatal("exact replay must not observe a newer Nomad job")
		return nomad.MySQLSourceJobObservation{}, nil
	})
	replay, err := db.AcceptPrivateMySQLSourceSnapshot(ctx, acceptedStore, noObservation, snapshotAcceptanceInput)
	if err != nil || replay.Operation.ID != acceptedSnapshot.Operation.ID || !replay.Replayed {
		t.Fatalf("source snapshot replay=%+v err=%v", replay, err)
	}
	conflicting := snapshotAcceptanceInput
	conflicting.Selection.DumpToolSHA256 = strings.Repeat("e", 64)
	if _, err := db.AcceptPrivateMySQLSourceSnapshot(ctx, acceptedStore, noObservation, conflicting); !errors.Is(err, ErrAcceptanceConflict) {
		t.Fatalf("changed snapshot intent reused request identity: %v", err)
	}
	wrongObserver := mysqlSourceObserverFunc(func(_ context.Context, want nomad.MySQLSourceJobObservationRequest) (nomad.MySQLSourceJobObservation, error) {
		observed, _ := observer.ObserveMySQLSourceJob(ctx, want)
		observed.DeploymentID = "another-deployment"
		return observed, nil
	})
	if _, err := db.BuildMySQLSourceSnapshotRequest(ctx, acceptedStore, wrongObserver, MySQLSourceSnapshotAdmissionRequest{Binding: request, Maintenance: maintenance, DumpToolSHA256: strings.Repeat("d", 64)}); !errors.Is(err, ErrMySQLSourceSnapshotFence) {
		t.Fatalf("mismatched observation accepted: %v", err)
	}

	tests := []struct {
		name   string
		change func(*MySQLDeployedSourceBindingRequest)
	}{
		{name: "wrong app", change: func(r *MySQLDeployedSourceBindingRequest) { r.App = "another-app" }},
		{name: "wrong logical", change: func(r *MySQLDeployedSourceBindingRequest) { r.LogicalID = "analytics" }},
		{name: "wrong source", change: func(r *MySQLDeployedSourceBindingRequest) { r.Source.Role = "another_role" }},
		{name: "wrong catalog", change: func(r *MySQLDeployedSourceBindingRequest) { r.CatalogRevision++ }},
		{name: "wrong deployment", change: func(r *MySQLDeployedSourceBindingRequest) { r.DeploymentID = uuid.NewString() }},
		{name: "wrong spec", change: func(r *MySQLDeployedSourceBindingRequest) { r.SpecDigest = "sha256:" + strings.Repeat("b", 64) }},
		{name: "wrong region", change: func(r *MySQLDeployedSourceBindingRequest) { r.Region = "east" }},
		{name: "wrong Nomad region", change: func(r *MySQLDeployedSourceBindingRequest) { r.NomadRegion = "other" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := request
			test.change(&changed)
			if _, err := db.VerifySignedDeployedMySQLSourceBinding(ctx, acceptedStore, changed); !errors.Is(err, ErrMySQLDeployedSourceBinding) {
				t.Fatalf("unsafe binding err=%v", err)
			}
		})
	}
	if _, err := db.ActivateDatabaseCatalog(ctx, active.Revision, catalog, "source-admission-rotate"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.BuildMySQLSourceSnapshotRequest(ctx, acceptedStore, noObservationForInvalidMaintenance,
		MySQLSourceSnapshotAdmissionRequest{Binding: request, Maintenance: maintenance, DumpToolSHA256: strings.Repeat("d", 64)}); !errors.Is(err, ErrMySQLSourceSnapshotFence) {
		t.Fatalf("stale catalog revision was accepted for source snapshot: %v", err)
	}
}

func TestVerifySignedDeployedMySQLSourceBindingRejectsMultiRegionDeployment(t *testing.T) {
	stores, dbs := acceptanceIntegrationStores(t, 1)
	acceptedStore, db := stores[0], dbs[0]
	ctx := context.Background()
	specDigest := "sha256:" + strings.Repeat("e", 64)
	source := database.TargetIdentity{ServiceID: "mysql", ServiceGeneration: 1, BindingID: "app", BindingGeneration: 1, Engine: database.EngineMySQL, Database: "app", Role: "app"}
	encoded, _ := json.Marshal(mysqlDeployedTargetSet{Schema: mysqlDeployedDatabaseTargetsSchema, ProfileID: "mini", CatalogRevision: 4, Targets: []mysqlDeployedNamedTarget{{Name: "primary", Target: source}}})
	input := newAcceptance(t, acceptedStore, "mysql-deployed-multi-region", "operator", "app", true)
	input.Regions = append(input.Regions, model.ResolvedRegion{Name: "east", NomadRegion: "east", Datacenters: []string{"dc2"}, TrafficWeight: 0})
	input.Deployment.SpecDigest = specDigest
	input.Operation.Payload["specDigest"], input.Operation.Payload["databaseTargets"] = specDigest, string(encoded)
	input.Fingerprint, _ = CanonicalOperationRequestFingerprint(input)
	accepted, err := acceptedStore.Accept(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE operations SET status='succeeded',finished_at=clock_timestamp() WHERE id=$1`, accepted.Operation.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE deployments SET status='deployed',spec_digest=$2,finished_at=clock_timestamp() WHERE id=$1`, accepted.Deployment.ID, specDigest); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE deployment_regions SET status='deployed' WHERE deployment_id=$1`, accepted.Deployment.ID); err != nil {
		t.Fatal(err)
	}
	request := MySQLDeployedSourceBindingRequest{DeploymentID: accepted.Deployment.ID, App: "app", SpecDigest: specDigest, Region: "west", NomadRegion: "global", ProfileID: "mini", LogicalID: "primary", CatalogRevision: 4, Source: source}
	if _, err := db.VerifySignedDeployedMySQLSourceBinding(ctx, acceptedStore, request); !errors.Is(err, ErrMySQLDeployedSourceBinding) {
		t.Fatalf("multi-region source accepted: %v", err)
	}
}

func TestVerifySignedDeployedMySQLSourceBindingRequiresSuccessfulDeploymentAndRegion(t *testing.T) {
	stores, dbs := acceptanceIntegrationStores(t, 1)
	acceptedStore, db := stores[0], dbs[0]
	ctx := context.Background()
	specDigest := "sha256:" + strings.Repeat("c", 64)
	source := database.TargetIdentity{ServiceID: "mysql", ServiceGeneration: 1, BindingID: "app", BindingGeneration: 1, Engine: database.EngineMySQL, Database: "app", Role: "app"}
	encoded, _ := json.Marshal(mysqlDeployedTargetSet{Schema: mysqlDeployedDatabaseTargetsSchema, ProfileID: "mini", CatalogRevision: 4, Targets: []mysqlDeployedNamedTarget{{Name: "primary", Target: source}}})
	input := newAcceptance(t, acceptedStore, "mysql-deployed-not-terminal", "operator", "app", true)
	input.Deployment.SpecDigest = specDigest
	input.Operation.Payload["specDigest"], input.Operation.Payload["databaseTargets"] = specDigest, string(encoded)
	input.Fingerprint, _ = CanonicalOperationRequestFingerprint(input)
	accepted, err := acceptedStore.Accept(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	request := MySQLDeployedSourceBindingRequest{DeploymentID: accepted.Deployment.ID, App: "app", SpecDigest: specDigest, Region: "west", NomadRegion: "global", ProfileID: "mini", LogicalID: "primary", CatalogRevision: 4, Source: source}
	if _, err := db.VerifySignedDeployedMySQLSourceBinding(ctx, acceptedStore, request); !errors.Is(err, ErrMySQLDeployedSourceBinding) {
		t.Fatalf("queued deployment accepted: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE operations SET status='succeeded' WHERE id=$1`, accepted.Operation.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE deployments SET status='deployed' WHERE id=$1`, accepted.Deployment.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.VerifySignedDeployedMySQLSourceBinding(ctx, acceptedStore, request); !errors.Is(err, ErrMySQLDeployedSourceBinding) {
		t.Fatalf("undeployed region accepted: %v", err)
	}
}
