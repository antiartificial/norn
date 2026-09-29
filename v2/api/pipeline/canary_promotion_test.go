package pipeline

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	nomadapi "github.com/hashicorp/nomad/api"

	"norn/v2/api/effect"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/store"
)

func TestCanaryPromotionRechecksExactDeploymentBeforeReservationAndNomadWrite(t *testing.T) {
	var healthy atomic.Bool
	healthy.Store(false)
	var reads, writes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/deployment/deployment-accepted" || r.Method != http.MethodGet {
			writes.Add(1)
			http.Error(w, "unexpected request", http.StatusNotFound)
			return
		}
		reads.Add(1)
		state := &nomadapi.DeploymentState{DesiredCanaries: 2, PlacedCanaries: []string{"alloc-1"}, HealthyAllocs: 1}
		if healthy.Load() {
			state.PlacedCanaries = append(state.PlacedCanaries, "alloc-2")
			state.HealthyAllocs = 2
		}
		_ = json.NewEncoder(w).Encode(&nomadapi.Deployment{ID: "deployment-accepted", JobID: "widgets", Status: "running", TaskGroups: map[string]*nomadapi.DeploymentState{"web": state}})
	}))
	defer server.Close()
	client, err := nomad.NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	supervisor := &nomadCanaryPromotionSupervisor{client: client}
	payload, _ := json.Marshal(canaryPromotionRequest{App: "widgets", Region: "us-central", NomadRegion: "global", DeploymentID: "deployment-accepted"})
	r := effect.Reservation{Authority: "authority", Resource: "app/widgets/canary-promote/us-central", Stage: nomadCanaryPromotionStage, Supervisor: "nomad-canary-promotion", SupervisorExecutionID: "execution-1", LaunchPayload: payload}
	if err := supervisor.Prepare(context.Background(), r); err == nil {
		t.Fatal("underplaced canary passed pre-reservation check")
	}
	healthy.Store(true)
	if err := supervisor.Prepare(context.Background(), r); err != nil {
		t.Fatalf("ready exact deployment refused: %v", err)
	}
	// Health can fall again after the reservation; never submit the PUT for
	// a deployment whose desired canaries are no longer placed and healthy.
	healthy.Store(false)
	if _, err := supervisor.Launch(context.Background(), r, effect.LaunchMaterial{}); err == nil {
		t.Fatal("unready canary promoted after reservation")
	}
	if reads.Load() != 3 || writes.Load() != 0 {
		t.Fatalf("Nomad requests: reads=%d writes=%d", reads.Load(), writes.Load())
	}
}

func TestCanaryPromotionRejectsMissingDurableEffectBoundary(t *testing.T) {
	if _, err := NewNomadCanaryPromotionEffectsWithStore(nil, &nomad.Client{}); err == nil {
		t.Fatal("accepted a Nomad canary writer without an atomic effect store")
	}
	var typedNil *store.PGEffectStore
	if _, err := NewNomadCanaryPromotionEffectsWithStore(typedNil, &nomad.Client{}); err == nil {
		t.Fatal("accepted a typed-nil effect store")
	}
	if (&Pipeline{}).CanaryPromotionAvailable() {
		t.Fatal("admitted canary promotion without a durable effect boundary")
	}
}

func TestCanaryPromotionEvidenceExcludesMutableDescription(t *testing.T) {
	request := canaryPromotionRequest{App: "widgets", Region: "us-central", NomadRegion: "global", DeploymentID: "deployment-123"}
	info := &nomad.DeploymentInfo{ID: request.DeploymentID, Status: "successful", CanaryPromoted: true, StatusDesc: "initial"}
	first, err := canaryPromotionEvidenceOutput(request, info)
	if err != nil {
		t.Fatal(err)
	}
	info.StatusDesc = "updated operator description"
	second, err := canaryPromotionEvidenceOutput(request, info)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) || effect.DigestInput(first) != effect.DigestInput(second) {
		t.Fatal("mutable Nomad description changed a completed canary effect result digest")
	}
}

func TestCanaryPromotionRequestBindsLogicalAndNomadRegionsAndDeployment(t *testing.T) {
	op := &model.Operation{App: "widgets", Payload: map[string]interface{}{"region": "us-central", "nomadRegion": "global", "deploymentId": "deployment-123"}}
	request, err := canaryPromotionRequestFromOperation(op)
	if err != nil {
		t.Fatal(err)
	}
	if request.App != "widgets" || request.Region != "us-central" || request.NomadRegion != "global" || request.DeploymentID != "deployment-123" {
		t.Fatalf("request = %#v", request)
	}
	if got, want := canaryPromotionResource(request), "app/widgets/canary-promote/us-central"; got != want {
		t.Fatalf("resource = %q, want %q", got, want)
	}
	request.DeploymentID = "deployment-newer"
	if got, want := canaryPromotionResource(request), "app/widgets/canary-promote/us-central"; got != want {
		t.Fatalf("changed deployment must share region gate: resource = %q, want %q", got, want)
	}
}

func TestCanaryPromotionVerifierRecordsTerminalNomadFailure(t *testing.T) {
	request := canaryPromotionRequest{App: "widgets", Region: "us-central", NomadRegion: "global", DeploymentID: "deployment-123"}
	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	reservation := effect.Reservation{Authority: "authority", Resource: canaryPromotionResource(request), Stage: nomadCanaryPromotionStage, Supervisor: "nomad-canary-promotion", LaunchPayload: payload, OperationClaim: effect.OperationClaim{OperationID: "operation", OwnerID: "worker", Generation: 1}}
	if reservation.InputDigest, err = effect.ComputeInputDigest(reservation); err != nil {
		t.Fatal(err)
	}
	reservation.SupervisorExecutionID = canaryPromotionExecutionID(reservation)
	output := []byte(`{"deploymentId":"deployment-123","status":"failed"}`)
	verification, err := (nomadCanaryPromotionVerifier{}).Verify(context.Background(), effect.Record{Reservation: reservation}, effect.Observation{Phase: effect.SupervisorFailed, Output: output, Identity: effect.ExecutionIdentity{Supervisor: reservation.Supervisor, SupervisorExecutionID: reservation.SupervisorExecutionID, RuntimeInstanceID: "nomad-deployment:deployment-123"}, Evidence: effect.RawEvidence{Source: "nomad.deployment", Reference: "deployment-123", Payload: output}})
	if err != nil || verification.Decision != effect.VerificationFailed || verification.ResultDigest != effect.DigestInput(output) {
		t.Fatalf("failure verification = %#v, %v", verification, err)
	}
}

func TestCanaryPromotionVerifierRequiresPromotedTaskGroupEvidence(t *testing.T) {
	request := canaryPromotionRequest{App: "widgets", Region: "us-central", NomadRegion: "global", DeploymentID: "deployment-123"}
	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	reservation := effect.Reservation{Authority: "authority", Resource: canaryPromotionResource(request), Stage: nomadCanaryPromotionStage, Supervisor: "nomad-canary-promotion", LaunchPayload: payload, OperationClaim: effect.OperationClaim{OperationID: "operation", OwnerID: "worker", Generation: 1}}
	if reservation.InputDigest, err = effect.ComputeInputDigest(reservation); err != nil {
		t.Fatal(err)
	}
	reservation.SupervisorExecutionID = canaryPromotionExecutionID(reservation)
	for _, output := range [][]byte{[]byte(`{"canaryPromoted":false}`), []byte(`{}`)} {
		_, err := (nomadCanaryPromotionVerifier{}).Verify(context.Background(), effect.Record{Reservation: reservation}, effect.Observation{Phase: effect.SupervisorSucceeded, Output: output, Identity: effect.ExecutionIdentity{Supervisor: reservation.Supervisor, SupervisorExecutionID: reservation.SupervisorExecutionID, RuntimeInstanceID: "nomad-deployment:deployment-123"}})
		if err == nil {
			t.Fatalf("accepted success evidence %s", output)
		}
	}
	output := []byte(`{"canaryPromoted":true}`)
	verification, err := (nomadCanaryPromotionVerifier{}).Verify(context.Background(), effect.Record{Reservation: reservation}, effect.Observation{Phase: effect.SupervisorSucceeded, Output: output, Identity: effect.ExecutionIdentity{Supervisor: reservation.Supervisor, SupervisorExecutionID: reservation.SupervisorExecutionID, RuntimeInstanceID: "nomad-deployment:deployment-123"}})
	if err != nil || verification.Decision != effect.VerificationSucceeded {
		t.Fatalf("verified success = %#v, %v", verification, err)
	}
}

func TestCanaryPromotionRejectsIncompleteAcceptedPayload(t *testing.T) {
	for _, payload := range []map[string]interface{}{
		{"region": "us-central", "nomadRegion": "global"},
		{"region": "us-central", "deploymentId": "deployment-123"},
		{"nomadRegion": "global", "deploymentId": "deployment-123"},
	} {
		if _, err := canaryPromotionRequestFromOperation(&model.Operation{App: "widgets", Payload: payload}); err == nil {
			t.Fatalf("incomplete payload accepted: %#v", payload)
		}
	}
}

func TestCanaryPromotionExecutionIdentityIncludesClaimGeneration(t *testing.T) {
	reservation := effect.Reservation{Authority: "authority", InputDigest: "sha256:input", OperationClaim: effect.OperationClaim{OperationID: "operation", Generation: 1}}
	first := canaryPromotionExecutionID(reservation)
	reservation.OperationClaim.Generation = 2
	if second := canaryPromotionExecutionID(reservation); first == second {
		t.Fatal("successor claim reused predecessor Nomad promotion identity")
	}
}
