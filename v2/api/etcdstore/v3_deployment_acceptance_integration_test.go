package etcdstore

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/model"
	"norn/v2/api/store"
)

func deploymentAdmissionRequest(t *testing.T, authority string) store.OperationAcceptance {
	t.Helper()
	deploymentID, sagaID := uuid.NewString(), uuid.NewString()
	a := store.OperationAcceptance{
		Identity:   store.OperationRequestIdentity{Authority: authority, Actor: store.OperationActor{Issuer: "test", Subject: "operator"}, Kind: "app.deploy", Resource: "app/demo", Key: "deploy-once"},
		Operation:  model.Operation{ID: uuid.NewString(), Kind: "app.deploy", App: "demo", SagaID: sagaID, Status: model.OperationQueued, Source: "release-control-api", Payload: map[string]interface{}{"deploymentId": deploymentID}},
		Deployment: &model.Deployment{ID: deploymentID, App: "demo", SagaID: sagaID, Status: model.StatusQueued, CommitSHA: "0123456789abcdef", ImageTag: "example@sha256:abcdef", SourceKind: "release", SourceRef: "main"},
		Regions:    []model.ResolvedRegion{{Name: "west", NomadRegion: "global", Datacenters: []string{"dc2", "dc1"}, TrafficWeight: 100}},
		Audit:      store.AcceptanceAuditContext{Source: "test"},
		Admission:  store.OperationAdmissionPolicy{OneActiveMutablePerApp: true},
	}
	var err error
	a.Fingerprint, err = store.CanonicalOperationRequestFingerprint(a)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestV3PrivateDeploymentAggregateAtomicReplayEtcd(t *testing.T) {
	adapter, client, prefix := privateInvocationEtcdStore(t)
	ctx := context.Background()
	request := deploymentAdmissionRequest(t, adapter.authority)
	if _, err := adapter.Accept(ctx, request); !errors.Is(err, store.ErrAcceptanceInvalid) {
		t.Fatalf("public deployment admission was enabled: %v", err)
	}
	before, err := client.Get(ctx, prefix+"/v3/", clientv3.WithPrefix())
	if err != nil || len(before.Kvs) != 0 {
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
