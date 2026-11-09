package etcdstore

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"norn/v2/api/ingress"
	"norn/v2/api/model"
	"norn/v2/api/pipeline"
	"norn/v2/api/store"
)

func TestV3WorkloadRouteIntentFencesAndRecoversSameStepEtcd(t *testing.T) {
	adapter, client, _ := functionInvocationEffectAttemptStore(t)
	ctx := context.Background()
	activeFleetIngressForRouteIntent(t, adapter, 2)
	const app, environment, region = "widgets", "staging", "nyc3"
	target := FleetAppTarget{SchemaVersion: fleetAppTargetSchema, App: app, ControlEnvironment: environment,
		Cluster: "norn-staging", FleetEnvironment: "staging/nyc3", Region: region, NomadRegion: "global",
		NomadNamespace: "default", Datacenters: []string{"nyc3"}, Generation: 1}
	if _, err := adapter.ConfigureFleetAppTarget(ctx, target, 0); err != nil {
		t.Fatal(err)
	}
	intent := pipeline.WorkloadReleaseIntent{
		ID: "workload-route-etcd", App: app, Region: region, NomadRegion: target.NomadRegion, Namespace: target.NomadNamespace,
		ControlEnvironment: environment, FleetCluster: target.Cluster, FleetEnvironment: target.FleetEnvironment,
		FleetDatacenters: append([]string(nil), target.Datacenters...), FleetTargetGeneration: target.Generation,
		Process: "web", Endpoint: "https://widgets.example.test", PublicPaths: []string{"/proof"}, RetryGETPaths: []string{"/proof"},
		PrivateReadinessPath: "/ready", TrafficProbePath: "/proof", TrafficProbeBodySHA256: strings.Repeat("a", 64),
		SpecDigest: "sha256:" + strings.Repeat("1", 64), SourceRevision: "source-1",
		OldDeploymentID: "old-deploy", OldImageDigest: "sha256:" + strings.Repeat("2", 64), OldSpecDigest: "sha256:" + strings.Repeat("3", 64),
		OldJobID: "widgets-web-old", OldJobDigest: "sha256:" + strings.Repeat("4", 64), OldJobVersion: 1, OldJobModifyIndex: 2,
		TargetDeploymentID: "new-deploy", TargetImage: "registry.example.test/widgets@sha256:" + strings.Repeat("5", 64), TargetImageDigest: "sha256:" + strings.Repeat("5", 64),
		TargetJobID: "widgets-web-new", TargetJobDigest: "sha256:" + strings.Repeat("6", 64), TargetSpecDigest: "sha256:" + strings.Repeat("7", 64),
		TargetOperationID: "target-operation", TargetExecutionID: "target-execution", OldAllocationIDs: []string{"old-allocation"},
		TargetJobVersion: 2, TargetJobModifyIndex: 3, TargetReplicas: 1, SurgeLimit: 1, FirstRouteGeneration: 10,
		RouteSteps: []pipeline.WorkloadReleaseRouteStep{{OldWeight: 100}, {OldWeight: 90, TargetWeight: 10}, {TargetWeight: 100}},
	}
	intentBytes, err := json.Marshal(intent)
	if err != nil {
		t.Fatal(err)
	}
	operation := model.Operation{ID: uuid.NewString(), Kind: "app.workload-release", App: app, Status: model.OperationQueued,
		MaxAttempts: 3, Source: "test", Payload: map[string]interface{}{"workloadReleaseIntent": string(intentBytes)}}
	acceptance := store.OperationAcceptance{
		Identity: store.OperationRequestIdentity{Authority: adapter.authority, Actor: store.OperationActor{Issuer: "workload-route-test", Subject: "operator"},
			Kind: operation.Kind, Resource: "app/" + app, Key: uuid.NewString()},
		Operation: operation, Audit: store.AcceptanceAuditContext{Source: "workload-route-test"}, Semantics: map[string]interface{}{"intentId": intent.ID},
	}
	acceptance.Fingerprint, err = store.CanonicalOperationRequestFingerprint(acceptance)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Accept(ctx, acceptance); err != nil {
		t.Fatal(err)
	}
	claimed, claim, err := adapter.ClaimNextOperation(ctx, "workload-route-worker", time.Minute, []string{operation.Kind})
	if err != nil || claimed == nil || claimed.ID != operation.ID {
		t.Fatalf("claim=%+v err=%v", claimed, err)
	}
	lock, acquired, err := adapter.AcquireAppOperationLock(ctx, app)
	if err != nil || !acquired {
		t.Fatalf("lock=%t err=%v", acquired, err)
	}
	defer lock.Release()
	targetAllocation := pipeline.WorkloadReleaseAllocationObservation{AllocationID: "target-allocation", NodeID: "app-01", JobID: intent.TargetJobID,
		Namespace: intent.Namespace, TaskGroup: intent.Process, JobVersion: intent.TargetJobVersion, JobModifyIndex: intent.TargetJobModifyIndex,
		ImageDigest: intent.TargetImageDigest, Healthy: true}
	receipt := pipeline.WorkloadReleaseReceipt{IntentID: intent.ID, Phase: pipeline.WorkloadReleaseWarming, TargetAllocations: []pipeline.WorkloadReleaseAllocationObservation{targetAllocation}}
	if err := receipt.ValidateAgainst(intent); err != nil {
		t.Fatal(err)
	}
	receiptBytes, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	storedReceipt, err := adapter.CompareAndSwapWorkloadReleaseReceipt(ctx, claim, intent.ID, "", receiptBytes)
	if err != nil {
		t.Fatal(err)
	}
	firstRoute, err := intent.RenderRouteStep(0)
	if err != nil {
		t.Fatal(err)
	}
	prior := activeFleetRoute{IntentID: "prior-route", DeploymentID: intent.OldDeploymentID, Generation: intent.FirstRouteGeneration - 1, RouteSHA256: firstRoute.SHA256}
	put := func(key string, value interface{}) {
		t.Helper()
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.Put(ctx, key, string(encoded)); err != nil {
			t.Fatal(err)
		}
	}
	activeKey := adapter.fleetActiveRouteKey(app, environment, region)
	reservationKey := adapter.fleetRouteReservationKey(app, environment, region)
	put(activeKey, prior)
	put(reservationKey, initialFleetRouteReservation{IntentID: prior.IntentID, Generation: prior.Generation})
	activeBefore, err := client.Get(ctx, activeKey)
	if err != nil || len(activeBefore.Kvs) != 1 {
		t.Fatalf("initial active route=%+v err=%v", activeBefore, err)
	}
	loadInventory := func(ctx context.Context, cluster, fleetEnvironment string, port int) (*FleetIngressInventoryEvidence, error) {
		return adapter.CurrentActiveFleetIngressInventory(ctx, cluster, fleetEnvironment, port)
	}
	var predecessorReadbacks int
	observe := func(_ context.Context, cluster, fleetEnvironment string, _ int, _ []byte, _ []byte, _ []byte, desired ingress.RenderedRoute, generation uint64) (*FleetIngressRouteObservation, error) {
		predecessorReadbacks++
		inventory, err := adapter.CurrentActiveFleetIngressInventory(ctx, cluster, fleetEnvironment, 18082)
		if err != nil {
			return nil, err
		}
		nodes := make([]ingress.NodeObservation, 0, len(inventory.Nodes))
		for _, node := range inventory.Nodes {
			nodes = append(nodes, ingress.NodeObservation{NodeID: node.ID, MatchedDesiredRouteSHA256: desired.SHA256, PublishedGeneration: generation})
		}
		return &FleetIngressRouteObservation{Inventory: *inventory, RouteSHA256: desired.SHA256, Generation: generation, Nodes: nodes}, nil
	}
	first, err := adapter.intendClaimedWorkloadReleaseRouteStep(ctx, claim, lock, 0, 18082, []byte("ca"), []byte("cert"), []byte("key"), loadInventory, observe)
	if err != nil || first == nil || first.Generation != intent.FirstRouteGeneration || first.RenderedRoute.SHA256 != firstRoute.SHA256 {
		t.Fatalf("workload route intent=%+v err=%v", first, err)
	}
	var reservation initialFleetRouteReservation
	response, err := client.Get(ctx, reservationKey)
	if err != nil || len(response.Kvs) != 1 || decodeV3Record(response.Kvs[0].Value, &reservation) != nil || reservation.IntentID != intent.ID || reservation.Generation != first.Generation {
		t.Fatalf("pending route did not fence scale readers with its reservation: %+v err=%v", reservation, err)
	}
	second, err := adapter.intendClaimedWorkloadReleaseRouteStep(ctx, claim, lock, 0, 18082, []byte("ca"), []byte("cert"), []byte("key"), loadInventory, func(context.Context, string, string, int, []byte, []byte, []byte, ingress.RenderedRoute, uint64) (*FleetIngressRouteObservation, error) {
		t.Fatal("same-intent recovery incorrectly required every ingress node to remain on the predecessor after partial publication")
		return nil, nil
	})
	if err != nil || second == nil || second.ID != first.ID || predecessorReadbacks != 1 {
		t.Fatalf("same-intent route recovery=%+v readbacks=%d err=%v", second, predecessorReadbacks, err)
	}
	decision, err := adapter.AuthorizeClaimedWorkloadReleaseRouteForNode(ctx, claim, lock, first.ID, "ingress-01", 18082)
	if err != nil || decision == nil || decision.IntentID != first.ID || decision.Generation != first.Generation || decision.Route.SHA256 != first.RenderedRoute.SHA256 || decision.Expected.Generation != prior.Generation {
		t.Fatalf("node route authorization=%+v err=%v", decision, err)
	}
	if _, err := adapter.AuthorizeClaimedWorkloadReleaseRouteForNode(ctx, claim, lock, first.ID, "unlisted-ingress", 18082); err == nil {
		t.Fatal("workload route authorization accepted an ingress outside the current inventory")
	}
	if storedReceipt.ReceiptDigest == "" {
		t.Fatal("workload receipt was not durably stored")
	}
	traffic := func(context.Context, WorkloadReleaseRouteIntent) (*FleetIngressTrafficObservation, error) {
		inventory, err := adapter.CurrentActiveFleetIngressInventory(ctx, target.Cluster, target.FleetEnvironment, 18082)
		if err != nil {
			return nil, err
		}
		nodes := make([]ingress.NodeObservation, 0, len(inventory.Nodes))
		endpoints := make([]ingress.NodeEndpointProbe, 0, len(inventory.Nodes))
		for _, node := range inventory.Nodes {
			nodes = append(nodes, ingress.NodeObservation{NodeID: node.ID, MatchedDesiredRouteSHA256: first.RenderedRoute.SHA256, PublishedGeneration: first.Generation})
			endpoints = append(endpoints, ingress.NodeEndpointProbe{NodeID: node.ID, BodySHA256: intent.TrafficProbeBodySHA256})
		}
		return &FleetIngressTrafficObservation{Route: FleetIngressRouteObservation{Inventory: *inventory, RouteSHA256: first.RenderedRoute.SHA256, Generation: first.Generation, Nodes: nodes},
			ProbePath: intent.TrafficProbePath, EndpointBodySHA256: intent.TrafficProbeBodySHA256, NodeEndpoints: endpoints, PublicMatched: true}, nil
	}
	if _, err := adapter.commitClaimedWorkloadReleaseRouteStep(ctx, claim, lock, first.ID, 18082, func(ctx context.Context, route WorkloadReleaseRouteIntent) (*FleetIngressTrafficObservation, error) {
		observation, err := traffic(ctx, route)
		if err == nil {
			observation.PublicMatched = false
		}
		return observation, err
	}); err == nil {
		t.Fatal("workload route committed without an exact public traffic match")
	}
	unchanged, err := adapter.LoadWorkloadReleaseReceipt(ctx, intent.ID)
	if err != nil || unchanged == nil || unchanged.ReceiptDigest != storedReceipt.ReceiptDigest {
		t.Fatalf("failed public proof mutated workload receipt: %+v err=%v", unchanged, err)
	}
	unchangedActive, err := client.Get(ctx, activeKey)
	if err != nil || len(unchangedActive.Kvs) != 1 || unchangedActive.Kvs[0].ModRevision != activeBefore.Kvs[0].ModRevision {
		t.Fatalf("failed public proof mutated active route: %+v err=%v", unchangedActive, err)
	}
	published, err := adapter.publishProveCommitClaimedWorkloadReleaseRouteStep(ctx, claim, lock, 0, 18082, 443, 18083,
		[]byte("ca"), []byte("cert"), []byte("key"), loadInventory, observe,
		func(_ context.Context, nodes []ingress.IngressNode, intentID string, generation uint64, routeSHA string) ([]ingress.NodePublicationReceipt, error) {
			receipts := make([]ingress.NodePublicationReceipt, 0, len(nodes))
			for _, node := range nodes {
				receipts = append(receipts, ingress.NodePublicationReceipt{NodeID: node.ID, Generation: generation, RouteSHA256: routeSHA})
			}
			return receipts, nil
		}, traffic)
	if err != nil || published == nil || published.Receipt == nil || published.Receipt.Revision != storedReceipt.Revision+1 {
		t.Fatalf("publish/prove/commit workload route=%+v err=%v", published, err)
	}
	committed := published.Receipt
	var activeAfter activeFleetRoute
	activeResponse, err := client.Get(ctx, activeKey)
	if err != nil || len(activeResponse.Kvs) != 1 || decodeV3Record(activeResponse.Kvs[0].Value, &activeAfter) != nil ||
		activeAfter.IntentID != intent.ID || activeAfter.Generation != first.Generation || activeAfter.RouteSHA256 != first.RenderedRoute.SHA256 {
		t.Fatalf("active route did not advance with receipt: %+v err=%v", activeAfter, err)
	}
	retried, err := adapter.commitClaimedWorkloadReleaseRouteStep(ctx, claim, lock, first.ID, 18082, traffic)
	if err != nil || retried == nil || retried.ReceiptDigest != committed.ReceiptDigest || retried.Revision != committed.Revision {
		t.Fatalf("lost-response workload route recovery=%+v err=%v", retried, err)
	}
	replayDecision, err := adapter.AuthorizeClaimedWorkloadReleaseRouteForNode(ctx, claim, lock, first.ID, "ingress-01", 18082)
	if err != nil || replayDecision == nil || replayDecision.Generation != first.Generation || replayDecision.Route.SHA256 != first.RenderedRoute.SHA256 {
		t.Fatalf("same-generation node publication retry=%+v err=%v", replayDecision, err)
	}
}
