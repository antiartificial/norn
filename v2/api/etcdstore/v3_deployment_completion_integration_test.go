package etcdstore

import (
	"context"
	"errors"
	clientv3 "go.etcd.io/etcd/client/v3"
	"strings"
	"testing"
	"time"

	"norn/v2/api/model"
	"norn/v2/api/store"
)

func TestV3PrivateDeploymentCompletionRequiresIngressProofEtcd(t *testing.T) {
	adapter, client, _ := deploymentEtcdStore(t)
	ctx := context.Background()
	request := deploymentAdmissionRequest(t, adapter.authority)
	accepted, err := adapter.acceptDeploymentAggregate(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	_, claim, err := adapter.ClaimNextOperation(ctx, "deploy-worker", time.Minute, []string{"app.deploy"})
	if err != nil {
		t.Fatal(err)
	}
	lock, acquired, err := adapter.AcquireAppOperationLock(ctx, "demo")
	if err != nil || !acquired {
		t.Fatalf("app lock acquired=%v err=%v", acquired, err)
	}
	defer lock.Release()
	result := *accepted.Deployment
	result.Status = model.StatusDeployed
	regions := []model.DeploymentRegion{{DeploymentID: result.ID, Region: "west", NomadRegion: "global", Status: model.StatusDeployed, DesiredWeight: 100, ActiveWeight: 100}}
	if err := adapter.finishClaimedDeployment(ctx, claim, lock, result, regions, model.OperationSucceeded, "deployed", nil); err == nil || !strings.Contains(err.Error(), "deployment-bound ingress proof") {
		t.Fatalf("positive active traffic bypassed ingress proof: %v", err)
	}
	operation, err := adapter.GetOperation(ctx, accepted.Operation.ID)
	if err != nil || operation.Status != model.OperationRunning {
		t.Fatalf("ingress refusal changed operation: operation=%+v err=%v", operation, err)
	}
	results, err := client.Get(ctx, adapter.deploymentRegionResultPrefix(result.ID), clientv3.WithPrefix())
	if err != nil || len(results.Kvs) != 0 {
		t.Fatalf("ingress refusal wrote region result: count=%d err=%v", len(results.Kvs), err)
	}
	active, err := client.Get(ctx, adapter.appAdmissionActiveKey("demo", accepted.Operation.ID))
	if err != nil || len(active.Kvs) != 1 {
		t.Fatalf("ingress refusal released app admission: count=%d err=%v", len(active.Kvs), err)
	}
}

func zeroTrafficDeploymentAdmissionRequest(t *testing.T, authority string) store.OperationAcceptance {
	t.Helper()
	request := deploymentAdmissionRequest(t, authority)
	request.Regions[0].TrafficWeight = 0
	var err error
	request.Fingerprint, err = store.CanonicalOperationRequestFingerprint(request)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func TestV3PrivateDeploymentCompletionFencesAndReleasesEtcd(t *testing.T) {
	adapter, client, _ := deploymentEtcdStore(t)
	ctx := context.Background()
	request := zeroTrafficDeploymentAdmissionRequest(t, adapter.authority)
	accepted, err := adapter.acceptDeploymentAggregate(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	claimed, claim, err := adapter.ClaimNextOperation(ctx, "deploy-worker", time.Minute, []string{"app.deploy"})
	if err != nil || claimed == nil || claimed.ID != accepted.Operation.ID {
		t.Fatalf("claim=%+v err=%v", claimed, err)
	}
	lock, acquired, err := adapter.AcquireAppOperationLock(ctx, "demo")
	if err != nil || !acquired {
		t.Fatalf("app lock acquired=%v err=%v", acquired, err)
	}
	defer lock.Release()
	if err := adapter.FinishClaimedOperationWithAppLock(ctx, claim, lock, model.OperationSucceeded, "generic finish", nil); err == nil {
		t.Fatal("generic operation finish bypassed deployment result")
	}
	result := *accepted.Deployment
	result.Status = model.StatusDeployed
	regions := []model.DeploymentRegion{{DeploymentID: result.ID, Region: "west", NomadRegion: "global", Status: model.StatusDeployed, DesiredWeight: 0, ActiveWeight: 0, EvalID: "eval-1"}}
	if err := adapter.finishClaimedDeployment(ctx, claim, lock, result, regions, model.OperationSucceeded, "deployed", nil); err != nil {
		t.Fatal(err)
	}
	operation, err := adapter.GetOperation(ctx, accepted.Operation.ID)
	if err != nil || operation.Status != model.OperationSucceeded || operation.FinishedAt == nil {
		t.Fatalf("terminal operation=%+v err=%v", operation, err)
	}
	deployment, resolved, err := adapter.loadAcceptedDeployment(ctx, result.ID)
	if err != nil || deployment.Status != model.StatusDeployed || len(resolved) != 1 {
		t.Fatalf("terminal deployment=%+v regions=%v err=%v", deployment, resolved, err)
	}
	lookup, err := adapter.GetDeployment(ctx, result.ID)
	if err != nil || lookup.Status != model.StatusDeployed || len(lookup.Regions) != 1 || lookup.Regions[0].Status != model.StatusDeployed || lookup.Regions[0].EvalID != "eval-1" {
		t.Fatalf("terminal deployment lookup=%+v err=%v", lookup, err)
	}
	regionResult, err := client.Get(ctx, adapter.deploymentRegionResultKey(result.ID, "west"))
	if err != nil || len(regionResult.Kvs) != 1 {
		t.Fatalf("region result count=%d err=%v", len(regionResult.Kvs), err)
	}
	active, err := client.Get(ctx, adapter.appAdmissionActiveKey("demo", operation.ID))
	if err != nil || len(active.Kvs) != 0 {
		t.Fatalf("terminal app gate count=%d err=%v", len(active.Kvs), err)
	}
	replayed, err := adapter.ResolveIdentity(ctx, request.Identity)
	if err != nil || replayed.Deployment.Status != model.StatusDeployed || replayed.Operation.Status != model.OperationSucceeded {
		t.Fatalf("completed deployment replay=%+v err=%v", replayed, err)
	}
	if _, err := client.Delete(ctx, adapter.deploymentRegionResultKey(result.ID, "west")); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.ResolveIdentity(ctx, request.Identity); !errors.Is(err, store.ErrAcceptanceSignature) {
		t.Fatalf("missing terminal region result did not fail replay: %v", err)
	}
}

func TestV3PrivateDeploymentCompletionRejectsLostAppLockEtcd(t *testing.T) {
	adapter, client, _ := deploymentEtcdStore(t)
	ctx := context.Background()
	request := deploymentAdmissionRequest(t, adapter.authority)
	accepted, err := adapter.acceptDeploymentAggregate(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	_, claim, err := adapter.ClaimNextOperation(ctx, "deploy-worker", time.Minute, []string{"app.deploy"})
	if err != nil {
		t.Fatal(err)
	}
	lock, acquired, err := adapter.AcquireAppOperationLock(ctx, "demo")
	if err != nil || !acquired {
		t.Fatalf("app lock acquired=%v err=%v", acquired, err)
	}
	lock.Release()
	result := *accepted.Deployment
	result.Status = model.StatusDeployed
	regions := []model.DeploymentRegion{{DeploymentID: result.ID, Region: "west", NomadRegion: "global", Status: model.StatusDeployed, DesiredWeight: 100, ActiveWeight: 100}}
	if err := adapter.finishClaimedDeployment(ctx, claim, lock, result, regions, model.OperationSucceeded, "deployed", nil); !errors.Is(err, store.ErrOperationOwnershipLost) {
		t.Fatalf("lost app lock completed deployment: %v", err)
	}
	operation, err := adapter.GetOperation(ctx, accepted.Operation.ID)
	if err != nil || operation.Status != model.OperationRunning {
		t.Fatalf("operation after lost lock=%+v err=%v", operation, err)
	}
	regionResult, err := client.Get(ctx, adapter.deploymentRegionResultKey(result.ID, "west"))
	if err != nil || len(regionResult.Kvs) != 0 {
		t.Fatalf("lost lock wrote %d region results: %v", len(regionResult.Kvs), err)
	}
}

func TestV3PrivateDeploymentCompletionRejectsStaleClaimEtcd(t *testing.T) {
	adapter, client, _ := deploymentEtcdStore(t)
	ctx := context.Background()
	request := zeroTrafficDeploymentAdmissionRequest(t, adapter.authority)
	accepted, err := adapter.acceptDeploymentAggregate(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	_, stale, err := adapter.ClaimNextOperation(ctx, "first-worker", time.Minute, []string{"app.deploy"})
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.DeferClaimedOperation(ctx, stale, "retry", time.Now().Add(-time.Second), nil); err != nil {
		t.Fatal(err)
	}
	_, current, err := adapter.ClaimNextOperation(ctx, "second-worker", time.Minute, []string{"app.deploy"})
	if err != nil || current.Generation() <= stale.Generation() {
		t.Fatalf("replacement claim=%+v err=%v", current, err)
	}
	lock, acquired, err := adapter.AcquireAppOperationLock(ctx, "demo")
	if err != nil || !acquired {
		t.Fatalf("app lock acquired=%v err=%v", acquired, err)
	}
	defer lock.Release()
	result := *accepted.Deployment
	result.Status = model.StatusDeployed
	regions := []model.DeploymentRegion{{DeploymentID: result.ID, Region: "west", NomadRegion: "global", Status: model.StatusDeployed, DesiredWeight: 0, ActiveWeight: 0}}
	if err := adapter.finishClaimedDeployment(ctx, stale, lock, result, regions, model.OperationSucceeded, "stale", nil); !errors.Is(err, store.ErrOperationOwnershipLost) {
		t.Fatalf("stale claim finished deployment: %v", err)
	}
	if resultRecord, err := client.Get(ctx, adapter.deploymentRegionResultKey(result.ID, "west")); err != nil || len(resultRecord.Kvs) != 0 {
		t.Fatalf("stale claim wrote region result: %d records, %v", len(resultRecord.Kvs), err)
	}
	if err := adapter.finishClaimedDeployment(ctx, current, lock, result, regions, model.OperationSucceeded, "current", nil); err != nil {
		t.Fatal(err)
	}
}

func TestV3PrivateDeploymentCompletionRetainsUnresolvedEffectHoldEtcd(t *testing.T) {
	adapter, client, _ := deploymentEtcdStore(t)
	ctx := context.Background()
	request := deploymentAdmissionRequest(t, adapter.authority)
	accepted, err := adapter.acceptDeploymentAggregate(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	_, claim, err := adapter.ClaimNextOperation(ctx, "deploy-worker", time.Minute, []string{"app.deploy"})
	if err != nil {
		t.Fatal(err)
	}
	lock, acquired, err := adapter.AcquireAppOperationLock(ctx, "demo")
	if err != nil || !acquired {
		t.Fatalf("app lock acquired=%v err=%v", acquired, err)
	}
	defer lock.Release()
	result := *accepted.Deployment
	result.Status = model.StatusFailed
	regions := []model.DeploymentRegion{{DeploymentID: result.ID, Region: "west", NomadRegion: "global", Status: model.StatusSubmitting, DesiredWeight: 100, ActiveWeight: 0, EvalID: "ambiguous-eval"}}
	if err := adapter.finishClaimedDeployment(ctx, claim, lock, result, regions, model.OperationFailed, "Nomad outcome unresolved", map[string]interface{}{"externalEffectRecoveryPending": true}); err != nil {
		t.Fatal(err)
	}
	active, err := client.Get(ctx, adapter.appAdmissionActiveKey("demo", accepted.Operation.ID))
	if err != nil || len(active.Kvs) != 1 {
		t.Fatalf("unresolved effect lost app hold: %d records, %v", len(active.Kvs), err)
	}
	if _, err := adapter.ResolveIdentity(ctx, request.Identity); err != nil {
		t.Fatalf("unresolved deployment replay: %v", err)
	}
	if _, err := client.Delete(ctx, adapter.deploymentRegionResultKey(result.ID, "west")); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.ResolveIdentity(ctx, request.Identity); !errors.Is(err, store.ErrAcceptanceSignature) {
		t.Fatalf("missing unresolved region result did not fail replay: %v", err)
	}
	second := deploymentAdmissionRequest(t, adapter.authority)
	second.Identity.Key = "deploy-two"
	second.Fingerprint, err = store.CanonicalOperationRequestFingerprint(second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.acceptDeploymentAggregate(ctx, second); !errors.Is(err, store.ErrAcceptanceAdmission) {
		t.Fatalf("unresolved effect allowed new deployment: %v", err)
	}
}
