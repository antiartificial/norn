package etcdstore

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/database"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

func TestV3DeploymentAcceptancePinsActiveDatabaseCatalogEtcd(t *testing.T) {
	adapter, client, _ := deploymentEtcdStore(t)
	ctx := context.Background()
	active, err := adapter.ActivatePostgresDatabaseCatalog(ctx, 0, postgresCatalogFixture(), "operator")
	if err != nil || active.Revision != 1 {
		t.Fatalf("catalog activation=%+v err=%v", active, err)
	}
	resolver, err := database.NewResolver(active.Catalog)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := resolver.Resolve(database.ResolveRequest{DeploymentProfileID: "local", Purpose: database.PurposeApplication,
		LogicalResourceID: "primary", RequiredCapabilities: []database.Capability{database.CapabilityRuntime}})
	if err != nil {
		t.Fatal(err)
	}
	request := deploymentAdmissionRequest(t, adapter.authority)
	set, err := json.Marshal(map[string]interface{}{"schema": "norn.database-targets/v1", "profileId": "local", "catalogRevision": 1,
		"targets": []map[string]interface{}{{"name": "primary", "target": resolved.Target}}})
	if err != nil {
		t.Fatal(err)
	}
	request.Operation.Payload["databaseTargets"] = string(set)
	request.Fingerprint, err = store.CanonicalOperationRequestFingerprint(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.acceptDeploymentAggregate(ctx, request); err != nil {
		t.Fatalf("current catalog acceptance: %v", err)
	}
	comparison, err := adapter.deploymentDatabaseCatalogCompare(ctx, request.Operation)
	if err != nil || comparison == nil {
		t.Fatalf("catalog comparison=%v err=%v", comparison, err)
	}
	if _, err := client.Put(ctx, adapter.databaseCatalogActiveKey(), "2"); err != nil {
		t.Fatal(err)
	}
	txn, err := client.Txn(ctx).If(*comparison).Then(clientv3.OpPut(adapter.prefix+"/catalog-fence-test", "unsafe")).Commit()
	if err != nil || txn.Succeeded {
		t.Fatalf("changed catalog passed acceptance comparison: %+v err=%v", txn, err)
	}
	stale := deploymentAdmissionRequest(t, adapter.authority)
	stale.Identity.Key = "stale-catalog"
	stale.Operation.Payload["databaseTargets"] = string(set)
	stale.Fingerprint, err = store.CanonicalOperationRequestFingerprint(stale)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.acceptDeploymentAggregate(ctx, stale); err == nil {
		t.Fatal("stale catalog target set was accepted")
	}
}

func deploymentAdmissionRequest(t *testing.T, authority string) store.OperationAcceptance {
	t.Helper()
	deploymentID, sagaID := uuid.NewString(), uuid.NewString()
	a := store.OperationAcceptance{
		Identity:   store.OperationRequestIdentity{Authority: authority, Actor: store.OperationActor{Issuer: "test", Subject: "operator"}, Kind: "app.deploy", Resource: "app/demo", Key: "deploy-once"},
		Operation:  model.Operation{ID: uuid.NewString(), Kind: "app.deploy", App: "demo", SagaID: sagaID, Status: model.OperationQueued, Source: "release-control-api", Payload: map[string]interface{}{"deploymentId": deploymentID}},
		Deployment: &model.Deployment{ID: deploymentID, App: "demo", Environment: "staging", SagaID: sagaID, Status: model.StatusQueued, CommitSHA: "0123456789abcdef", ImageTag: "example@sha256:abcdef", SourceKind: "release", SourceRef: "main"},
		Regions:    []model.ResolvedRegion{{Name: "west", NomadRegion: "global", Datacenters: []string{"dc2", "dc1"}, TrafficWeight: 100}},
		Audit:      store.AcceptanceAuditContext{Source: "test"},
		Admission:  store.OperationAdmissionPolicy{OneActiveMutablePerApp: true},
		Semantics:  map[string]interface{}{"fleetAppTarget": FleetAppTarget{SchemaVersion: fleetAppTargetSchema, App: "demo", ControlEnvironment: "staging", Cluster: "norn-staging", FleetEnvironment: "staging/nyc3", Region: "west", NomadRegion: "global", Datacenters: []string{"dc1", "dc2"}, Generation: 1}},
	}
	var err error
	a.Fingerprint, err = store.CanonicalOperationRequestFingerprint(a)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func deploymentEtcdStore(t *testing.T) (*V3OperationStore, *clientv3.Client, string) {
	t.Helper()
	adapter, client, prefix := privateInvocationEtcdStore(t)
	target := FleetAppTarget{SchemaVersion: fleetAppTargetSchema, App: "demo", ControlEnvironment: "staging", Cluster: "norn-staging", FleetEnvironment: "staging/nyc3", Region: "west", NomadRegion: "global", Datacenters: []string{"dc1", "dc2"}, Generation: 1}
	if _, err := adapter.putFleetAppTarget(context.Background(), target, 0); err != nil {
		t.Fatal(err)
	}
	return adapter, client, prefix
}

func TestV3PrivateDeploymentAggregateAtomicReplayEtcd(t *testing.T) {
	adapter, client, prefix := deploymentEtcdStore(t)
	ctx := context.Background()
	request := deploymentAdmissionRequest(t, adapter.authority)
	if _, err := adapter.Accept(ctx, request); !errors.Is(err, store.ErrAcceptanceInvalid) {
		t.Fatalf("public deployment admission was enabled: %v", err)
	}
	before, err := client.Get(ctx, prefix+"/v3/", clientv3.WithPrefix())
	if err != nil || len(before.Kvs) != 1 || string(before.Kvs[0].Key) != adapter.fleetAppTargetKey("demo", "staging") {
		t.Fatalf("public refusal wrote %d records: %v", len(before.Kvs), err)
	}
	accepted, err := adapter.acceptDeploymentAggregate(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Intent.DeploymentID != request.Deployment.ID || accepted.Deployment == nil || len(accepted.Regions) != 1 {
		t.Fatalf("accepted deployment is incomplete: %+v", accepted)
	}
	queued, err := adapter.GetDeployment(ctx, request.Deployment.ID)
	if err != nil || queued.Status != model.StatusQueued || len(queued.Regions) != 1 || queued.Regions[0].Status != model.StatusQueued || queued.Regions[0].DesiredWeight != 100 {
		t.Fatalf("queued deployment lookup=%+v err=%v", queued, err)
	}
	replayed, err := adapter.acceptDeploymentAggregate(ctx, request)
	if err != nil || !replayed.Replayed || replayed.Operation.ID != accepted.Operation.ID || replayed.Deployment.ID != accepted.Deployment.ID {
		t.Fatalf("deployment replay=%+v err=%v", replayed, err)
	}
	regionKey := adapter.deploymentRegionKey(request.Deployment.ID, "west")
	region, err := client.Get(ctx, regionKey)
	if err != nil || len(region.Kvs) != 1 {
		t.Fatalf("accepted region unavailable: %v", err)
	}
	if _, err := client.Delete(ctx, regionKey); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.ResolveIdentity(ctx, request.Identity); !errors.Is(err, store.ErrAcceptanceSignature) {
		t.Fatalf("missing accepted region did not fail verification: %v", err)
	}
	if _, err := client.Put(ctx, regionKey, string(region.Kvs[0].Value)); err != nil {
		t.Fatal(err)
	}
	deploymentKey := adapter.deploymentKey(request.Deployment.ID)
	if _, err := client.Put(ctx, deploymentKey, `{"deployment":{"id":"`+request.Deployment.ID+`","app":"other","sagaId":"`+request.Operation.SagaID+`","status":"queued"}}`); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.ResolveIdentity(ctx, request.Identity); !errors.Is(err, store.ErrAcceptanceSignature) {
		t.Fatalf("changed deployment did not fail verification: %v", err)
	}
}

func TestV3PrivateDeploymentRejectsUnboundFleetTargetEtcd(t *testing.T) {
	adapter, client, _ := deploymentEtcdStore(t)
	ctx := context.Background()
	request := deploymentAdmissionRequest(t, adapter.authority)
	wrong := request
	wrong.Semantics = map[string]interface{}{"fleetAppTarget": FleetAppTarget{SchemaVersion: fleetAppTargetSchema, App: "demo", ControlEnvironment: "staging", Cluster: "other-cluster", FleetEnvironment: "staging/nyc3", Region: "west", NomadRegion: "global", Datacenters: []string{"dc1", "dc2"}, Generation: 1}}
	var err error
	wrong.Fingerprint, err = store.CanonicalOperationRequestFingerprint(wrong)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.acceptDeploymentAggregate(ctx, wrong); err == nil {
		t.Fatal("caller-selected Fleet cluster was admitted")
	}
	if op, err := client.Get(ctx, adapter.opKey(request.Operation.ID)); err != nil || len(op.Kvs) != 0 {
		t.Fatalf("unbound target wrote operation: count=%d err=%v", len(op.Kvs), err)
	}
	accepted, err := adapter.acceptDeploymentAggregate(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	target, revision, err := adapter.loadFleetAppTarget(ctx, "demo", "staging")
	if err != nil {
		t.Fatal(err)
	}
	target.Generation++
	target.Cluster = "replacement"
	if _, err := adapter.putFleetAppTarget(ctx, target, revision); err != nil {
		t.Fatal(err)
	}
	if replayed, err := adapter.ResolveIdentity(ctx, request.Identity); err != nil || !replayed.Replayed || replayed.Intent.ID != accepted.Intent.ID {
		t.Fatalf("target replacement changed signed replay: %+v err=%v", replayed, err)
	}
	acceptanceKey := adapter.acceptanceKey(request.Identity)
	stored, err := client.Get(ctx, acceptanceKey)
	if err != nil || len(stored.Kvs) != 1 {
		t.Fatal("accepted record is missing", err)
	}
	var record v3Acceptance
	if err := json.Unmarshal(stored.Kvs[0].Value, &record); err != nil {
		t.Fatal(err)
	}
	record.Accepted.FleetAppTarget.Cluster = "forged"
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Put(ctx, acceptanceKey, string(encoded)); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.ResolveIdentity(ctx, request.Identity); !errors.Is(err, store.ErrAcceptanceSignature) {
		t.Fatalf("tampered target snapshot did not fail replay: %v", err)
	}
}
