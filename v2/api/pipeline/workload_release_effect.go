package pipeline

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"norn/v2/api/effect"
	"norn/v2/api/nomad"
	"norn/v2/api/store"
)

const workloadReleaseStopStage = "app.workload-release.stop-old.nomad"

// WorkloadReleaseEffectStore is the durable single-authority boundary for
// stopping the old generation. Reserve must validate the operation claim and
// acquire the app/region workload-release gate atomically.
type WorkloadReleaseEffectStore interface {
	effect.Store
	effect.RecoveryStore
	Authority(context.Context) (string, error)
}

type WorkloadReleaseReceiptCAS interface {
	LoadWorkloadReleaseReceipt(context.Context, string) (*store.WorkloadReleaseReceiptRecord, error)
	CompareAndSwapWorkloadReleaseReceipt(context.Context, store.OperationClaim, string, string, json.RawMessage) (store.WorkloadReleaseReceiptRecord, error)
}

var _ WorkloadReleaseEffectStore = (*store.PGEffectStore)(nil)

type WorkloadReleaseStopEffects struct {
	store    WorkloadReleaseEffectStore
	receipts *WorkloadReleaseReceiptRepository
	executor *effect.Executor
	client   *nomad.Client
}

type workloadReleaseStopDescriptor struct {
	Intent WorkloadReleaseIntent `json:"intent"`
}

type workloadReleaseStopResult struct {
	JobID                string   `json:"jobId"`
	DeploymentID         string   `json:"deploymentId"`
	OperationID          string   `json:"operationId"`
	IntentDigest         string   `json:"intentDigest"`
	StoppedJobVersion    uint64   `json:"stoppedJobVersion"`
	StoppedModifyIndex   uint64   `json:"stoppedModifyIndex"`
	DrainedAllocationIDs []string `json:"drainedAllocationIds"`
}

func NewWorkloadReleaseStopEffects(db *store.DB, client *nomad.Client) (*WorkloadReleaseStopEffects, error) {
	if client == nil {
		return nil, fmt.Errorf("Nomad client is unavailable")
	}
	effectStore, err := store.NewPGEffectStore(db)
	if err != nil {
		return nil, err
	}
	return NewWorkloadReleaseStopEffectsWithStore(effectStore, db, client)
}

func NewWorkloadReleaseStopEffectsWithStore(effectStore WorkloadReleaseEffectStore, receiptStore WorkloadReleaseReceiptCAS, client *nomad.Client) (*WorkloadReleaseStopEffects, error) {
	if effectStore == nil || receiptStore == nil || client == nil {
		return nil, fmt.Errorf("workload release stop requires durable effect and receipt stores plus Nomad client")
	}
	value := reflect.ValueOf(effectStore)
	if value.Kind() == reflect.Ptr && value.IsNil() {
		return nil, fmt.Errorf("workload release stop requires durable effect and receipt stores plus Nomad client")
	}
	receiptValue := reflect.ValueOf(receiptStore)
	if receiptValue.Kind() == reflect.Ptr && receiptValue.IsNil() {
		return nil, fmt.Errorf("workload release stop requires durable effect and receipt stores plus Nomad client")
	}
	supervisor := &nomadWorkloadReleaseStopSupervisor{client: client}
	return &WorkloadReleaseStopEffects{store: effectStore, executor: &effect.Executor{
		Store: effectStore, Supervisor: supervisor, Verifier: workloadReleaseStopVerifier{},
	}, receipts: &WorkloadReleaseReceiptRepository{store: receiptStore}, client: client}, nil
}

func (e *WorkloadReleaseStopEffects) Available() bool {
	return e != nil && e.store != nil && e.executor != nil && e.receipts != nil
}

// AdvanceReceipt applies the intent's forward-only transition contract and
// persists it with claim-fenced compare-and-swap.
func (e *WorkloadReleaseStopEffects) AdvanceReceipt(ctx context.Context, claim store.OperationClaim, intent WorkloadReleaseIntent, expectedDigest string, next WorkloadReleaseReceipt) (store.WorkloadReleaseReceiptRecord, error) {
	if !e.Available() {
		return store.WorkloadReleaseReceiptRecord{}, fmt.Errorf("durable workload release receipt store is unavailable")
	}
	return e.receipts.Advance(ctx, claim, intent, expectedDigest, next)
}

// RecordDrainReadback binds the durable stop effect result to a fresh exact
// Nomad readback, then advances the receipt under the current claim.
func (e *WorkloadReleaseStopEffects) RecordDrainReadback(ctx context.Context, claim store.OperationClaim, intent WorkloadReleaseIntent, result effect.ExecuteResult) (store.WorkloadReleaseReceiptRecord, error) {
	if !e.Available() || e.client == nil || result.Outcome != effect.OutcomeSucceeded || len(result.Output) == 0 {
		return store.WorkloadReleaseReceiptRecord{}, fmt.Errorf("verified workload stop result is unavailable")
	}
	if err := intent.Validate(); err != nil {
		return store.WorkloadReleaseReceiptRecord{}, err
	}
	var stopped workloadReleaseStopResult
	if err := json.Unmarshal(result.Output, &stopped); err != nil || stopped.JobID != intent.OldJobID || stopped.DeploymentID != intent.OldDeploymentID || stopped.OperationID == "" || result.EffectID == "" {
		return store.WorkloadReleaseReceiptRecord{}, fmt.Errorf("verified workload stop result differs from intent")
	}
	reservation, err := e.stopReservation(ctx, claim, intent)
	if err != nil || stopped.OperationID != reservation.SupervisorExecutionID || effect.DigestInput(result.Output) != result.ResultDigest {
		return store.WorkloadReleaseReceiptRecord{}, fmt.Errorf("workload stop result differs from this operation's durable effect identity")
	}
	request, err := intent.OldDeploymentStopRequest(stopped.OperationID)
	if err != nil {
		return store.WorkloadReleaseReceiptRecord{}, err
	}
	request.StoppedJobVersion = stopped.StoppedJobVersion
	request.StoppedJobModifyIndex = stopped.StoppedModifyIndex
	observation, err := e.client.ObserveStoppedDeployment(ctx, request)
	if err != nil {
		return store.WorkloadReleaseReceiptRecord{}, err
	}
	record, receipt, err := e.receipts.Load(ctx, intent)
	if err != nil || record == nil || record.OperationID != claim.OperationID() || receipt.Phase != WorkloadReleaseDraining {
		return store.WorkloadReleaseReceiptRecord{}, fmt.Errorf("workload release is not ready to record old-generation drain")
	}
	next, err := receipt.WithOldDeploymentDrainReadback(intent, stopped.StoppedJobVersion, stopped.StoppedModifyIndex, observation)
	if err != nil {
		return store.WorkloadReleaseReceiptRecord{}, err
	}
	return e.receipts.Advance(ctx, claim, intent, record.ReceiptDigest, next)
}

// MarkComplete records the terminal receipt only after final-route proof and
// exact terminal readback for every pinned old allocation are already durable.
func (e *WorkloadReleaseStopEffects) MarkComplete(ctx context.Context, claim store.OperationClaim, intent WorkloadReleaseIntent) (store.WorkloadReleaseReceiptRecord, error) {
	if !e.Available() {
		return store.WorkloadReleaseReceiptRecord{}, fmt.Errorf("durable workload release receipt store is unavailable")
	}
	record, receipt, err := e.receipts.Load(ctx, intent)
	if err != nil || record == nil || record.OperationID != claim.OperationID() || receipt.Phase != WorkloadReleaseDraining ||
		receipt.RouteStep != len(intent.RouteSteps)-1 || receipt.RouteGeneration == 0 || len(receipt.DrainedOldIDs) != len(intent.OldAllocationIDs) || healthyTargetCount(receipt) != intent.TargetReplicas {
		return store.WorkloadReleaseReceiptRecord{}, fmt.Errorf("workload release lacks terminal route, health, or drain evidence")
	}
	next := receipt
	next.Phase = WorkloadReleaseComplete
	return e.receipts.Advance(ctx, claim, intent, record.ReceiptDigest, next)
}

// ExecuteStop reserves and executes exactly one old-generation stop under the
// caller's operation claim. Unknown outcomes remain pending and recoverable;
// callers must not create a replacement intent to retry them.
func (e *WorkloadReleaseStopEffects) ExecuteStop(ctx context.Context, claim store.OperationClaim, intent WorkloadReleaseIntent) (effect.ExecuteResult, error) {
	if !e.Available() || strings.TrimSpace(claim.OperationID()) == "" || strings.TrimSpace(claim.OwnerID()) == "" || claim.Generation() <= 0 {
		return effect.ExecuteResult{}, fmt.Errorf("durable workload release stop is unavailable or unclaimed")
	}
	if err := intent.Validate(); err != nil {
		return effect.ExecuteResult{}, err
	}
	stored, receipt, err := e.receipts.Load(ctx, intent)
	if err != nil {
		return effect.ExecuteResult{}, err
	}
	if stored == nil || stored.OperationID != claim.OperationID() {
		return effect.ExecuteResult{}, fmt.Errorf("workload release stop requires a durable receipt owned by this operation")
	}
	if err := validateWorkloadReleaseStopPrerequisite(intent, receipt); err != nil {
		return effect.ExecuteResult{}, err
	}
	reservation, err := e.stopReservation(ctx, claim, intent)
	if err != nil {
		return effect.ExecuteResult{}, err
	}
	authority := reservation.Authority
	result, err := e.executor.Execute(ctx, effect.ExecuteRequest{Reservation: reservation})
	if errors.Is(err, effect.ErrResourceBlocked) {
		blocking, found, lookupErr := e.store.UnresolvedForResource(ctx, authority, reservation.Resource)
		if lookupErr != nil {
			return effect.ExecuteResult{}, &effect.PendingError{Resource: reservation.Resource, Reason: "blocking workload release lookup failed", Cause: lookupErr}
		}
		if found {
			_, recoverErr := e.executor.Recover(ctx, blocking)
			if recoverErr != nil {
				return effect.ExecuteResult{}, recoverErr
			}
			return effect.ExecuteResult{}, &effect.PendingError{Resource: reservation.Resource, Reason: "prior workload release stop recovered; successor needs fresh claim"}
		}
	}
	return result, err
}

func (e *WorkloadReleaseStopEffects) stopReservation(ctx context.Context, claim store.OperationClaim, intent WorkloadReleaseIntent) (effect.Reservation, error) {
	authority, err := e.store.Authority(ctx)
	if err != nil {
		return effect.Reservation{}, err
	}
	payload, err := json.Marshal(workloadReleaseStopDescriptor{Intent: intent})
	if err != nil {
		return effect.Reservation{}, fmt.Errorf("encode workload release stop descriptor: %w", err)
	}
	reservation := effect.Reservation{Authority: authority, Resource: workloadReleaseStopResource(intent),
		OperationClaim: effect.OperationClaim{OperationID: claim.OperationID(), OwnerID: claim.OwnerID(), Generation: claim.Generation()},
		Stage:          workloadReleaseStopStage, Supervisor: "nomad-workload-release-stop", LaunchPayload: payload}
	reservation.InputDigest, err = effect.ComputeInputDigest(reservation)
	if err != nil {
		return effect.Reservation{}, err
	}
	reservation.SupervisorExecutionID = workloadReleaseStopExecutionID(reservation)
	return reservation, nil
}

// validateWorkloadReleaseStopPrerequisite prevents the irreversible old-job
// stop from running until a durable receipt proves full target health and an
// observed mixed route. The caller must load this receipt from the release
// controller's durable CAS store; a command response is not sufficient.
func validateWorkloadReleaseStopPrerequisite(intent WorkloadReleaseIntent, receipt WorkloadReleaseReceipt) error {
	if err := receipt.ValidateAgainst(intent); err != nil {
		return fmt.Errorf("old deployment stop prerequisite receipt is invalid: %w", err)
	}
	if receipt.Phase != WorkloadReleaseDraining || receipt.RouteStep < 1 || len(receipt.TargetAllocations) < intent.TargetReplicas || healthyTargetCount(receipt) != intent.TargetReplicas {
		return fmt.Errorf("old deployment stop requires full target health and mixed-route readback in the draining phase")
	}
	return nil
}

func (e *WorkloadReleaseStopEffects) Recover(ctx context.Context, record effect.Record) (effect.ExecuteResult, error) {
	if !e.Available() {
		return effect.ExecuteResult{}, fmt.Errorf("durable workload release stop recovery is unavailable")
	}
	return e.executor.Recover(ctx, record)
}

func workloadReleaseStopResource(intent WorkloadReleaseIntent) string {
	return "app/" + intent.App + "/workload-release/" + intent.Region
}

func workloadReleaseStopExecutionID(r effect.Reservation) string {
	// The marker belongs to the immutable intent, not a lease generation. A
	// successor claim must rediscover the same Nomad stop after a lost response.
	sum := sha256.Sum256([]byte(r.Authority + "\x00" + r.OperationClaim.OperationID + "\x00" + r.InputDigest))
	return "nomad-workload-stop-" + hex.EncodeToString(sum[:16])
}

type nomadWorkloadReleaseStopSupervisor struct {
	client *nomad.Client
}

func (s *nomadWorkloadReleaseStopSupervisor) Prepare(_ context.Context, r effect.Reservation) error {
	_, err := workloadReleaseDescriptorFromReservation(r)
	return err
}

func (s *nomadWorkloadReleaseStopSupervisor) Launch(ctx context.Context, r effect.Reservation, _ effect.LaunchMaterial) (effect.ExecutionIdentity, error) {
	intent, err := workloadReleaseDescriptorFromReservation(r)
	if err != nil {
		return effect.ExecutionIdentity{}, err
	}
	request, err := intent.OldDeploymentStopRequest(r.SupervisorExecutionID)
	if err != nil {
		return effect.ExecutionIdentity{}, err
	}
	stop, err := s.client.StopDeploymentCAS(ctx, request)
	if err != nil {
		return effect.ExecutionIdentity{}, err
	}
	return effect.ExecutionIdentity{Supervisor: r.Supervisor, SupervisorExecutionID: r.SupervisorExecutionID,
		RuntimeInstanceID: stoppedDeploymentRuntimeID(stop.JobID, stop.JobVersion, stop.JobModifyIndex)}, nil
}

func (s *nomadWorkloadReleaseStopSupervisor) Query(ctx context.Context, r effect.Reservation, identity effect.ExecutionIdentity) (effect.Observation, error) {
	intent, err := workloadReleaseDescriptorFromReservation(r)
	if err != nil {
		return effect.Observation{}, err
	}
	request, err := intent.OldDeploymentStopRequest(r.SupervisorExecutionID)
	if err != nil {
		return effect.Observation{}, err
	}
	stop, err := s.client.LookupStoppedDeploymentByIntent(ctx, request)
	if err != nil {
		return effect.Observation{}, err
	}
	identity.Supervisor, identity.SupervisorExecutionID = r.Supervisor, r.SupervisorExecutionID
	identity.RuntimeInstanceID = stoppedDeploymentRuntimeID(stop.JobID, stop.JobVersion, stop.JobModifyIndex)
	terminal, err := s.client.ObserveStoppedDeployment(ctx, nomad.StoppedDeploymentRequest{
		JobID: request.JobID, Region: request.Region, Namespace: request.Namespace, DeploymentID: request.DeploymentID,
		StopOperationID: request.StopOperationID, IntentDigest: request.IntentDigest,
		SpecDigest: request.SpecDigest, JobDigest: request.JobDigest, ImageDigest: request.ImageDigest, TaskGroup: request.TaskGroup,
		OriginalJobVersion: request.OriginalJobVersion, OriginalJobModifyIndex: request.OriginalJobModifyIndex,
		StoppedJobVersion: stop.JobVersion, StoppedJobModifyIndex: stop.JobModifyIndex, AllocationIDs: request.AllocationIDs,
	})
	if err != nil {
		return effect.Observation{Identity: identity, Phase: effect.SupervisorRunning,
			Evidence: effect.RawEvidence{Source: "nomad.workload-release.stop", Reference: r.SupervisorExecutionID}}, nil
	}
	output, err := json.Marshal(workloadReleaseStopResult{JobID: terminal.JobID, DeploymentID: terminal.DeploymentID,
		OperationID: r.SupervisorExecutionID, IntentDigest: request.IntentDigest,
		StoppedJobVersion: terminal.JobVersion, StoppedModifyIndex: terminal.JobModifyIndex,
		DrainedAllocationIDs: terminal.DrainedAllocationIDs})
	if err != nil {
		return effect.Observation{}, err
	}
	return effect.Observation{Identity: identity, Phase: effect.SupervisorSucceeded, Output: output,
		Evidence: effect.RawEvidence{Source: "nomad.workload-release.stop", Reference: r.SupervisorExecutionID, Payload: output}}, nil
}

func (s *nomadWorkloadReleaseStopSupervisor) Revoke(ctx context.Context, r effect.Reservation, identity effect.ExecutionIdentity) (effect.Observation, error) {
	return s.Query(ctx, r, identity)
}

func (s *nomadWorkloadReleaseStopSupervisor) RetrieveResult(ctx context.Context, r effect.Reservation, identity effect.ExecutionIdentity, _ string) ([]byte, error) {
	observation, err := s.Query(ctx, r, identity)
	return observation.Output, err
}

func workloadReleaseDescriptorFromReservation(r effect.Reservation) (WorkloadReleaseIntent, error) {
	var descriptor workloadReleaseStopDescriptor
	if err := json.Unmarshal(r.LaunchPayload, &descriptor); err != nil || descriptor.Intent.Validate() != nil ||
		r.Resource != workloadReleaseStopResource(descriptor.Intent) || r.Stage != workloadReleaseStopStage || r.Supervisor != "nomad-workload-release-stop" {
		return WorkloadReleaseIntent{}, fmt.Errorf("workload release stop descriptor is invalid")
	}
	return descriptor.Intent, nil
}

func stoppedDeploymentRuntimeID(jobID string, version, modifyIndex uint64) string {
	return fmt.Sprintf("nomad-stopped-deployment:%s:%d:%d", jobID, version, modifyIndex)
}

type workloadReleaseStopVerifier struct{}

func (workloadReleaseStopVerifier) Verify(_ context.Context, record effect.Record, observation effect.Observation) (effect.Verification, error) {
	intent, err := workloadReleaseDescriptorFromReservation(record.Reservation)
	if err != nil {
		return effect.Verification{}, err
	}
	if observation.Phase != effect.SupervisorSucceeded || observation.Identity.Supervisor != record.Reservation.Supervisor ||
		observation.Identity.SupervisorExecutionID != record.Reservation.SupervisorExecutionID || observation.Identity.RuntimeInstanceID == "" ||
		observation.Evidence.Source != "nomad.workload-release.stop" || !bytes.Equal(observation.Evidence.Payload, observation.Output) {
		return effect.Verification{}, fmt.Errorf("old deployment stop observation identity or evidence is invalid")
	}
	var result workloadReleaseStopResult
	if err := json.Unmarshal(observation.Output, &result); err != nil {
		return effect.Verification{}, fmt.Errorf("decode old deployment stop result: %w", err)
	}
	expectedIntentDigest, err := intent.CanonicalDigest()
	if err != nil {
		return effect.Verification{}, err
	}
	if result.JobID != intent.OldJobID || result.DeploymentID != intent.OldDeploymentID || result.OperationID != record.Reservation.SupervisorExecutionID ||
		result.IntentDigest != expectedIntentDigest || result.StoppedJobVersion != intent.OldJobVersion+1 || result.StoppedModifyIndex <= intent.OldJobModifyIndex ||
		!equalReleaseStrings(sortedReleaseIDs(result.DrainedAllocationIDs), sortedReleaseIDs(intent.OldAllocationIDs)) ||
		observation.Identity.RuntimeInstanceID != stoppedDeploymentRuntimeID(result.JobID, result.StoppedJobVersion, result.StoppedModifyIndex) {
		return effect.Verification{}, fmt.Errorf("old deployment stop result differs from its exact intent")
	}
	return effect.Verification{Decision: effect.VerificationSucceeded, InputDigest: record.Reservation.InputDigest,
		ResultDigest: effect.DigestInput(observation.Output), ResultReference: observation.Evidence.Reference,
		SupervisorExecutionID: record.Reservation.SupervisorExecutionID, RuntimeInstanceID: observation.Identity.RuntimeInstanceID,
		EvidenceSource: observation.Evidence.Source, EvidenceReference: observation.Evidence.Reference, ObservedAt: time.Now().UTC()}, nil
}

func sortedReleaseIDs(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}
