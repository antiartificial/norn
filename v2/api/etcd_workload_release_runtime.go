package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/google/uuid"

	"norn/v2/api/effect"
	"norn/v2/api/etcdstore"
	"norn/v2/api/fleetdeploy"
	"norn/v2/api/ingress"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/pipeline"
	"norn/v2/api/store"
	workerruntime "norn/v2/api/worker"
)

type etcdWorkloadReleaseExecutor struct {
	operations   *etcdstore.V3OperationStore
	stageEffects *etcdstore.V3CanaryEffectReservations
	nomad        *nomad.Client
	route        fleetdeploy.ClaimedFleetRouteTransport
	appsDir      string
	inputKeys    *store.WorkloadReleaseInputKeyRing
	runtimeID    string
	target       *pipeline.WorkloadReleaseTargetHealthController
	stop         *pipeline.WorkloadReleaseStopEffects
}

func newEtcdWorkloadReleaseExecutor(operations *etcdstore.V3OperationStore, nomadClient *nomad.Client, route fleetdeploy.ClaimedFleetRouteTransport,
	appsDir string, inputKeys *store.WorkloadReleaseInputKeyRing) (*etcdWorkloadReleaseExecutor, error) {
	if operations == nil || nomadClient == nil || route.Listen == nil {
		return nil, fmt.Errorf("etcd workload release executor dependencies are incomplete")
	}
	effects, err := etcdstore.NewV3WorkloadReleaseEffectReservations(operations)
	if err != nil {
		return nil, err
	}
	target, err := pipeline.NewWorkloadReleaseTargetHealthController(operations, nomadClient)
	if err != nil {
		return nil, err
	}
	stop, err := pipeline.NewWorkloadReleaseStopEffectsWithStore(effects, operations, nomadClient)
	if err != nil {
		return nil, err
	}
	return &etcdWorkloadReleaseExecutor{operations: operations, stageEffects: effects, nomad: nomadClient, route: route,
		appsDir: appsDir, inputKeys: inputKeys, runtimeID: "workload-release-controller:" + uuid.NewString(), target: target, stop: stop}, nil
}

func (e *etcdWorkloadReleaseExecutor) ExecuteOperation(context.Context, *model.Operation, store.OperationClaim) (*pipeline.OperationResult, error) {
	return nil, fmt.Errorf("workload release requires the claimed app lock")
}

func (e *etcdWorkloadReleaseExecutor) ExecuteOperationWithAppLock(ctx context.Context, op *model.Operation, claim store.OperationClaim, lock store.AppOperationLock) (*pipeline.OperationResult, error) {
	if e == nil || e.operations == nil || op == nil || op.Kind != "app.workload-release" || claim.OperationID() != op.ID || lock == nil || lock.Context().Err() != nil {
		return nil, fmt.Errorf("claimed workload release executor is unavailable")
	}
	return reconcileWorkloadRelease(ctx, claim, lock, e)
}

// workloadReleaseDriver is the narrow seam between the deterministic
// lifecycle reconciler and its durable/runtime effects. Tests can inject a
// lost response after any completed boundary while retaining the same
// reconciler used by the etcd worker.
type workloadReleaseDriver interface {
	Verify(context.Context, store.OperationClaim, store.AppOperationLock) (pipeline.WorkloadReleaseIntent, error)
	EnsureTargetBound(context.Context, store.OperationClaim, store.AppOperationLock, pipeline.WorkloadReleaseIntent) error
	Load(context.Context, pipeline.WorkloadReleaseIntent) (*store.WorkloadReleaseReceiptRecord, pipeline.WorkloadReleaseReceipt, error)
	Initialize(context.Context, store.OperationClaim, pipeline.WorkloadReleaseIntent) error
	Advance(context.Context, store.OperationClaim, pipeline.WorkloadReleaseIntent, string, pipeline.WorkloadReleaseReceipt) (store.WorkloadReleaseReceiptRecord, error)
	ObserveHealth(context.Context, store.OperationClaim, pipeline.WorkloadReleaseIntent) error
	PublishRouteStep(context.Context, store.OperationClaim, store.AppOperationLock, pipeline.WorkloadReleaseIntent, int) error
	StopAndReadback(context.Context, store.OperationClaim, pipeline.WorkloadReleaseIntent) error
	Complete(context.Context, store.OperationClaim, pipeline.WorkloadReleaseIntent) (store.WorkloadReleaseReceiptRecord, error)
}

func reconcileWorkloadRelease(ctx context.Context, claim store.OperationClaim, lock store.AppOperationLock, driver workloadReleaseDriver) (*pipeline.OperationResult, error) {
	intent, err := driver.Verify(ctx, claim, lock)
	if err != nil {
		return nil, err
	}
	record, _, err := driver.Load(ctx, intent)
	if err != nil {
		return nil, err
	}
	if record == nil {
		if err := driver.EnsureTargetBound(ctx, claim, lock, intent); err != nil {
			return nil, err
		}
		if err := driver.Initialize(ctx, claim, intent); err != nil {
			return nil, err
		}
	}
	for remaining := len(intent.RouteSteps) + 6; remaining > 0; remaining-- {
		if ctx.Err() != nil || lock.Context().Err() != nil {
			return nil, store.ErrOperationOwnershipLost
		}
		intent, err = driver.Verify(ctx, claim, lock)
		if err != nil {
			return nil, err
		}
		record, receipt, err := driver.Load(ctx, intent)
		if err != nil || record == nil || record.OperationID != claim.OperationID() {
			return nil, fmt.Errorf("claimed workload receipt is unavailable: %v", err)
		}
		if receipt.Phase == pipeline.WorkloadReleaseComplete {
			return &pipeline.OperationResult{Claim: claim, Status: model.OperationSucceeded, Message: "workload release verified", Metadata: map[string]interface{}{"intentId": intent.ID, "receiptDigest": record.ReceiptDigest}}, nil
		}
		if receipt.Phase == pipeline.WorkloadReleaseHeld {
			return &pipeline.OperationResult{Claim: claim, Status: model.OperationFailed, Message: "workload release held: " + receipt.HeldReason,
				Metadata: map[string]interface{}{"manualRecoveryRequired": true, "workloadReleaseHeld": true, "intentId": intent.ID}}, nil
		}
		if receipt.Phase == pipeline.WorkloadReleasePlanned {
			if err := driver.EnsureTargetBound(ctx, claim, lock, intent); err != nil {
				return nil, err
			}
			warming := receipt
			warming.Phase = pipeline.WorkloadReleaseWarming
			if _, err := driver.Advance(ctx, claim, intent, record.ReceiptDigest, warming); err != nil {
				return nil, err
			}
			continue
		}
		if receipt.Phase != pipeline.WorkloadReleaseWarming && receipt.Phase != pipeline.WorkloadReleaseRouting && receipt.Phase != pipeline.WorkloadReleaseDraining {
			return nil, fmt.Errorf("unknown workload release phase %q", receipt.Phase)
		}
		if err := driver.ObserveHealth(ctx, claim, intent); err != nil {
			return nil, err
		}
		record, receipt, err = driver.Load(ctx, intent)
		if err != nil || record == nil {
			return nil, fmt.Errorf("workload health receipt is unavailable: %v", err)
		}
		if receipt.Phase == pipeline.WorkloadReleaseHeld {
			continue
		}
		if healthyWorkloadTargets(receipt) != intent.TargetReplicas {
			return nil, &effect.PendingError{Resource: workloadReleaseResource(intent), Reason: "target does not have full healthy replica capacity"}
		}
		switch receipt.Phase {
		case pipeline.WorkloadReleaseWarming, pipeline.WorkloadReleaseRouting:
			step := 0
			if receipt.RouteGeneration > 0 {
				step = receipt.RouteStep + 1
			}
			if step >= len(intent.RouteSteps) {
				return nil, fmt.Errorf("workload release route sequence is exhausted before drain completion")
			}
			if err := driver.PublishRouteStep(ctx, claim, lock, intent, step); err != nil {
				return nil, err
			}
		case pipeline.WorkloadReleaseDraining:
			if len(receipt.DrainedOldIDs) != len(intent.OldAllocationIDs) {
				if err := driver.StopAndReadback(ctx, claim, intent); err != nil {
					return nil, err
				}
				continue
			}
			completed, err := driver.Complete(ctx, claim, intent)
			if err != nil {
				return nil, err
			}
			return &pipeline.OperationResult{Claim: claim, Status: model.OperationSucceeded, Message: "workload release route, target health, and old allocation drain verified",
				Metadata: map[string]interface{}{"intentId": intent.ID, "receiptDigest": completed.ReceiptDigest, "routeGeneration": receipt.RouteGeneration}}, nil
		}
	}
	return nil, &effect.PendingError{Resource: workloadReleaseResource(intent), Reason: "workload release exceeded its bounded reconciliation steps"}
}

func (e *etcdWorkloadReleaseExecutor) Verify(ctx context.Context, claim store.OperationClaim, lock store.AppOperationLock) (pipeline.WorkloadReleaseIntent, error) {
	operation, err := e.operations.GetOperation(ctx, claim.OperationID())
	if err != nil {
		return pipeline.WorkloadReleaseIntent{}, err
	}
	if _, planned := operation.Payload["workloadReleasePlan"]; planned {
		return e.verifyAndStageWorkloadReleasePlan(ctx, *operation, claim, lock)
	}
	intent, _, err := e.operations.VerifyClaimedWorkloadRelease(ctx, claim, lock)
	return intent, err
}

func (e *etcdWorkloadReleaseExecutor) verifyAndStageWorkloadReleasePlan(ctx context.Context, operation model.Operation, claim store.OperationClaim,
	lock store.AppOperationLock) (pipeline.WorkloadReleaseIntent, error) {
	plan, accepted, err := e.operations.LoadClaimedWorkloadReleasePlan(ctx, claim, lock)
	if err != nil {
		return pipeline.WorkloadReleaseIntent{}, err
	}
	if accepted.Operation.ID != operation.ID || accepted.Operation.App != operation.App || operation.ID != claim.OperationID() {
		return pipeline.WorkloadReleaseIntent{}, store.ErrOperationOwnershipLost
	}
	reservation, err := e.operations.WorkloadReleaseStageTargetReservation(ctx, claim, lock)
	if err != nil {
		return pipeline.WorkloadReleaseIntent{}, err
	}
	reserved, err := e.stageEffects.Reserve(ctx, reservation)
	if err != nil {
		return pipeline.WorkloadReleaseIntent{}, err
	}
	if reserved.Record.Token.EffectID == "" || reserved.Record.Reservation.InputDigest != reservation.InputDigest ||
		reserved.Record.Reservation.OperationClaim.OperationID != claim.OperationID() {
		return pipeline.WorkloadReleaseIntent{}, fmt.Errorf("workload release target reservation differs from signed plan")
	}
	if reserved.Record.Lifecycle == effect.LifecycleCompleted {
		intent, _, _, err := e.operations.ResolveClaimedWorkloadReleasePlan(ctx, claim, lock, reserved.Record.Token.EffectID)
		return intent, err
	}
	if reserved.Record.Lifecycle != effect.LifecycleReserved && reserved.Record.Lifecycle != effect.LifecycleLaunched {
		return pipeline.WorkloadReleaseIntent{}, fmt.Errorf("workload release target effect is not recoverable")
	}
	if e.appsDir == "" || e.inputKeys == nil {
		return pipeline.WorkloadReleaseIntent{}, fmt.Errorf("workload release private input capability is disabled")
	}
	spec, err := workloadReleaseSourceSpec(e.appsDir, operation.App)
	if err != nil {
		return pipeline.WorkloadReleaseIntent{}, err
	}
	inputs, err := workerruntime.PrepareWorkloadReleaseTargetInputs(ctx, operation, plan, spec, e.operations, e.inputKeys)
	if err != nil {
		return pipeline.WorkloadReleaseIntent{}, err
	}
	if err := stageAndBindWorkloadReleaseTarget(ctx, e.nomad, e.operations, claim, lock, reserved.Record.Token.EffectID,
		e.runtimeID, plan.Intent, &inputs); err != nil {
		return pipeline.WorkloadReleaseIntent{}, err
	}
	intent, _, _, err := e.operations.ResolveClaimedWorkloadReleasePlan(ctx, claim, lock, reserved.Record.Token.EffectID)
	return intent, err
}

type workloadReleaseTargetNomad interface {
	StageManagedJobSecretInputs(string, nomad.ManagedJobInputRequirements, map[string]string) error
	RegisterDeploymentJobCAS(context.Context, nomad.CASDeploymentJobRequest) (string, error)
	LookupDeploymentJobRevision(context.Context, nomad.CASDeploymentJobRequest) (nomad.DeploymentJobObservation, error)
}

type workloadReleaseTargetStageStore interface {
	MarkWorkloadReleaseStageTargetAttempt(context.Context, store.OperationClaim, store.AppOperationLock, string, string) (bool, error)
	BindClaimedWorkloadReleaseTarget(context.Context, store.OperationClaim, store.AppOperationLock, string, interface {
		LookupDeploymentJobRevision(context.Context, nomad.CASDeploymentJobRequest) (nomad.DeploymentJobObservation, error)
	}) (pipeline.WorkloadReleaseStagedTarget, error)
}

func stageAndBindWorkloadReleaseTarget(ctx context.Context, scheduler workloadReleaseTargetNomad, operations workloadReleaseTargetStageStore,
	claim store.OperationClaim, lock store.AppOperationLock, effectID, runtimeID string, intent pipeline.WorkloadReleaseIntent,
	inputs *workerruntime.WorkloadReleaseTargetInputs) error {
	if scheduler == nil || operations == nil || lock == nil || inputs == nil || inputs.JobPlan.Inputs == nil ||
		strings.TrimSpace(effectID) == "" || strings.TrimSpace(runtimeID) == "" {
		return fmt.Errorf("workload release target stage dependencies are incomplete")
	}
	if lock.Context().Err() != nil || ctx.Err() != nil {
		clear(inputs.Secrets)
		return store.ErrOperationOwnershipLost
	}
	if err := scheduler.StageManagedJobSecretInputs(intent.NomadRegion, *inputs.JobPlan.Inputs, inputs.Secrets); err != nil {
		clear(inputs.Secrets)
		return &effect.PendingError{Resource: workloadReleaseResource(intent), Reason: "private target inputs could not be staged", Cause: err}
	}
	clear(inputs.Secrets)
	if lock.Context().Err() != nil || ctx.Err() != nil {
		return store.ErrOperationOwnershipLost
	}
	allowed, err := operations.MarkWorkloadReleaseStageTargetAttempt(ctx, claim, lock, effectID, runtimeID)
	if err != nil {
		return err
	}
	if allowed {
		if lock.Context().Err() != nil || ctx.Err() != nil {
			return store.ErrOperationOwnershipLost
		}
		// A registration response, successful or ambiguous, is never evidence.
		// Only the exact no-diff source readback below may bind the target.
		_, _ = scheduler.RegisterDeploymentJobCAS(ctx, inputs.JobPlan.Request)
	}
	_, err = operations.BindClaimedWorkloadReleaseTarget(ctx, claim, lock, effectID, scheduler)
	if errors.Is(err, etcdstore.ErrWorkloadReleaseTargetNotFound) || errors.Is(err, etcdstore.ErrWorkloadReleaseTargetReadbackUnavailable) {
		return &effect.PendingError{EffectID: effectID, Resource: workloadReleaseResource(intent),
			Reason: "target registration requires exact Nomad readback before release", Cause: err}
	}
	return err
}

func workloadReleaseSourceSpec(appsDir, app string) (*model.InfraSpec, error) {
	if strings.TrimSpace(appsDir) == "" || strings.TrimSpace(app) == "" {
		return nil, fmt.Errorf("workload release source is unavailable")
	}
	specs, err := model.DiscoverApps(appsDir)
	if err != nil {
		return nil, fmt.Errorf("discover workload release source")
	}
	var match *model.InfraSpec
	for _, spec := range specs {
		if spec.App != app {
			continue
		}
		if match != nil {
			return nil, fmt.Errorf("workload release source is ambiguous")
		}
		match = spec
	}
	if match == nil {
		return nil, fmt.Errorf("workload release source is unavailable")
	}
	return match, nil
}

// EnsureTargetBound re-reads the exact submitted revision before the durable
// release receipt can enter warming. The strict intent supplies the pinned
// scheduler coordinates; the observer verifies submission source and a
// no-diff Nomad plan without mutating the target.
func (e *etcdWorkloadReleaseExecutor) EnsureTargetBound(ctx context.Context, claim store.OperationClaim, lock store.AppOperationLock, intent pipeline.WorkloadReleaseIntent) error {
	if e == nil || e.nomad == nil || lock == nil || lock.Context().Err() != nil || claim.OperationID() == "" {
		return store.ErrOperationOwnershipLost
	}
	if err := intent.Validate(); err != nil {
		return fmt.Errorf("workload release target binding is invalid: %w", err)
	}
	request := nomad.CASDeploymentJobRequest{
		App: intent.TargetJobID, Region: intent.NomadRegion, Namespace: intent.Namespace,
		ExpectedJobModifyIndex: intent.TargetJobModifyIndex - 1,
		DeploymentID:           intent.TargetDeploymentID, SpecDigest: intent.TargetSpecDigest,
		OperationID: intent.TargetOperationID, ExecutionID: intent.TargetExecutionID,
		JobDigest: strings.TrimPrefix(intent.TargetJobDigest, "sha256:"), ImageTag: intent.TargetImage,
	}
	observed, err := e.nomad.LookupDeploymentJobRevision(ctx, request)
	if err != nil || observed.State != nomad.DeploymentJobFound || observed.Version != intent.TargetJobVersion || observed.JobModifyIndex != intent.TargetJobModifyIndex {
		return &effect.PendingError{Resource: workloadReleaseResource(intent), Reason: "exact pinned target revision is not bound by Nomad readback", Cause: err}
	}
	if lock.Context().Err() != nil {
		return store.ErrOperationOwnershipLost
	}
	return nil
}

func (e *etcdWorkloadReleaseExecutor) Load(ctx context.Context, intent pipeline.WorkloadReleaseIntent) (*store.WorkloadReleaseReceiptRecord, pipeline.WorkloadReleaseReceipt, error) {
	return e.loadReceipt(ctx, intent)
}

func (e *etcdWorkloadReleaseExecutor) Initialize(ctx context.Context, claim store.OperationClaim, intent pipeline.WorkloadReleaseIntent) error {
	planned, err := json.Marshal(pipeline.WorkloadReleaseReceipt{IntentID: intent.ID, Phase: pipeline.WorkloadReleasePlanned})
	if err != nil {
		return err
	}
	_, err = e.operations.CompareAndSwapWorkloadReleaseReceipt(ctx, claim, intent.ID, "", planned)
	return err
}

func (e *etcdWorkloadReleaseExecutor) Advance(ctx context.Context, claim store.OperationClaim, intent pipeline.WorkloadReleaseIntent, expected string, receipt pipeline.WorkloadReleaseReceipt) (store.WorkloadReleaseReceiptRecord, error) {
	return e.advanceReceipt(ctx, claim, intent, expected, receipt)
}

func (e *etcdWorkloadReleaseExecutor) ObserveHealth(ctx context.Context, claim store.OperationClaim, intent pipeline.WorkloadReleaseIntent) error {
	if _, err := e.target.ObserveTargetHealth(ctx, claim, intent); err != nil {
		return &effect.PendingError{Resource: workloadReleaseResource(intent), Reason: "fresh target allocation health is unavailable", Cause: err}
	}
	return nil
}

func (e *etcdWorkloadReleaseExecutor) PublishRouteStep(ctx context.Context, claim store.OperationClaim, lock store.AppOperationLock, intent pipeline.WorkloadReleaseIntent, step int) error {
	return e.publishRouteStep(ctx, claim, lock, intent, step)
}

func (e *etcdWorkloadReleaseExecutor) StopAndReadback(ctx context.Context, claim store.OperationClaim, intent pipeline.WorkloadReleaseIntent) error {
	result, err := e.stop.ExecuteStop(ctx, claim, intent)
	if err != nil {
		return err
	}
	if _, err := e.stop.RecordDrainReadback(ctx, claim, intent, result); err != nil {
		return &effect.PendingError{EffectID: result.EffectID, Resource: workloadReleaseResource(intent), Reason: "old allocation drain readback requires same-intent reconciliation", Cause: err}
	}
	return nil
}

func (e *etcdWorkloadReleaseExecutor) Complete(ctx context.Context, claim store.OperationClaim, intent pipeline.WorkloadReleaseIntent) (store.WorkloadReleaseReceiptRecord, error) {
	return e.stop.MarkComplete(ctx, claim, intent)
}

func (e *etcdWorkloadReleaseExecutor) loadReceipt(ctx context.Context, intent pipeline.WorkloadReleaseIntent) (*store.WorkloadReleaseReceiptRecord, pipeline.WorkloadReleaseReceipt, error) {
	record, err := e.operations.LoadWorkloadReleaseReceipt(ctx, intent.ID)
	if err != nil || record == nil {
		return record, pipeline.WorkloadReleaseReceipt{}, err
	}
	var receipt pipeline.WorkloadReleaseReceipt
	if err := json.Unmarshal(record.Receipt, &receipt); err != nil || receipt.ValidateAgainst(intent) != nil {
		return nil, pipeline.WorkloadReleaseReceipt{}, fmt.Errorf("workload release receipt failed validation")
	}
	return record, receipt, nil
}

func (e *etcdWorkloadReleaseExecutor) advanceReceipt(ctx context.Context, claim store.OperationClaim, intent pipeline.WorkloadReleaseIntent, expected string, receipt pipeline.WorkloadReleaseReceipt) (store.WorkloadReleaseReceiptRecord, error) {
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return store.WorkloadReleaseReceiptRecord{}, err
	}
	return e.operations.CompareAndSwapWorkloadReleaseReceipt(ctx, claim, intent.ID, expected, encoded)
}

func (e *etcdWorkloadReleaseExecutor) publishRouteStep(ctx context.Context, claim store.OperationClaim, lock store.AppOperationLock, intent pipeline.WorkloadReleaseIntent, step int) error {
	listener, err := e.route.Listen()
	if err != nil {
		return &effect.PendingError{Resource: workloadReleaseResource(intent), Reason: "private workload route authority listener is unavailable", Cause: err}
	}
	defer listener.Close()
	authority, err := ingress.NewControlRouteAuthorityHandler(e.route.NodeURIs, func(ctx context.Context, intentID, nodeID string) (*ingress.AuthorizedRoutePublication, error) {
		return e.operations.AuthorizeClaimedWorkloadReleaseRouteForNode(ctx, claim, lock, intentID, nodeID, e.route.ObserverPort)
	})
	if err != nil {
		return err
	}
	serveCtx, stopServing := context.WithCancel(ctx)
	served := make(chan error, 1)
	go func() {
		served <- ingress.ServePrivateControlRouteAuthority(serveCtx, lock.Context(), listener, authority,
			e.route.AuthorityCertPEM, e.route.AuthorityKeyPEM, e.route.NodeCAPEM)
	}()
	_, publishErr := e.operations.PublishProveCommitClaimedWorkloadReleaseRouteStep(ctx, claim, lock, step,
		e.route.ObserverPort, e.route.PublisherPort, e.route.EndpointPort,
		e.route.PublisherCAPEM, e.route.PublisherCertPEM, e.route.PublisherKeyPEM,
		e.route.ObserverCAPEM, e.route.ObserverCertPEM, e.route.ObserverKeyPEM, e.route.PublicRoots)
	stopServing()
	_ = listener.Close()
	serveErr := <-served
	if publishErr != nil {
		return publishErr
	}
	if serveErr != nil && !errors.Is(serveErr, context.Canceled) {
		log.Printf("workload route authority shutdown after route commit: %v", serveErr)
	}
	return nil
}

func healthyWorkloadTargets(receipt pipeline.WorkloadReleaseReceipt) int {
	count := 0
	for _, allocation := range receipt.TargetAllocations {
		if allocation.Healthy {
			count++
		}
	}
	return count
}

func workloadReleaseResource(intent pipeline.WorkloadReleaseIntent) string {
	return "app/" + intent.App + "/workload-release/" + intent.Region
}

// etcdFleetReleaseOperationExecutor serializes deploy and workload route
// writers because both use the same private ingress authority bind address.
type etcdFleetReleaseOperationExecutor struct {
	mu       sync.Mutex
	deploy   *fleetdeploy.ClaimedFleetDeploymentExecutor
	workload *etcdWorkloadReleaseExecutor
}

func (e *etcdFleetReleaseOperationExecutor) ExecuteOperation(ctx context.Context, op *model.Operation, claim store.OperationClaim) (*pipeline.OperationResult, error) {
	if op != nil && op.Kind == "app.deploy" {
		return e.deploy.ExecuteOperation(ctx, op, claim)
	}
	if op == nil || op.Kind != "app.workload-release" {
		return nil, fmt.Errorf("unsupported Fleet release operation")
	}
	return e.workload.ExecuteOperation(ctx, op, claim)
}

func (e *etcdFleetReleaseOperationExecutor) ExecuteOperationWithAppLock(ctx context.Context, op *model.Operation, claim store.OperationClaim, lock store.AppOperationLock) (*pipeline.OperationResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if op != nil && op.Kind == "app.deploy" {
		return e.deploy.ExecuteOperationWithAppLock(ctx, op, claim, lock)
	}
	if op == nil || op.Kind != "app.workload-release" {
		return nil, fmt.Errorf("unsupported Fleet release operation")
	}
	return e.workload.ExecuteOperationWithAppLock(ctx, op, claim, lock)
}
