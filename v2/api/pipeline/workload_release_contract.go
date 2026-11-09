package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"norn/v2/api/ingress"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
)

// WorkloadReleaseIntent binds the immutable identities and route sequence for
// a stateless, mixed-version application release. It deliberately contains no
// provider credentials or database/schema fields. Signature verification and
// durable execution belong to the controller adapters that consume this intent.
type WorkloadReleaseIntent struct {
	ID                     string                     `json:"id"`
	App                    string                     `json:"app"`
	Region                 string                     `json:"region"`
	NomadRegion            string                     `json:"nomadRegion"`
	Namespace              string                     `json:"namespace"`
	ControlEnvironment     string                     `json:"controlEnvironment"`
	FleetCluster           string                     `json:"fleetCluster"`
	FleetEnvironment       string                     `json:"fleetEnvironment"`
	FleetDatacenters       []string                   `json:"fleetDatacenters"`
	FleetTargetGeneration  uint64                     `json:"fleetTargetGeneration"`
	Process                string                     `json:"process"`
	Endpoint               string                     `json:"endpoint"`
	PublicPaths            []string                   `json:"publicPaths"`
	RetryGETPaths          []string                   `json:"retryGetPaths"`
	PrivateReadinessPath   string                     `json:"privateReadinessPath"`
	TrafficProbePath       string                     `json:"trafficProbePath"`
	TrafficProbeBodySHA256 string                     `json:"trafficProbeBodySha256"`
	SpecDigest             string                     `json:"specDigest"`
	SourceRevision         string                     `json:"sourceRevision"`
	OldDeploymentID        string                     `json:"oldDeploymentId"`
	OldImageDigest         string                     `json:"oldImageDigest"`
	OldSpecDigest          string                     `json:"oldSpecDigest"`
	OldJobID               string                     `json:"oldJobId"`
	OldJobDigest           string                     `json:"oldJobDigest"`
	OldJobVersion          uint64                     `json:"oldJobVersion"`
	OldJobModifyIndex      uint64                     `json:"oldJobModifyIndex"`
	TargetDeploymentID     string                     `json:"targetDeploymentId"`
	TargetImage            string                     `json:"targetImage"`
	TargetImageDigest      string                     `json:"targetImageDigest"`
	TargetJobID            string                     `json:"targetJobId"`
	TargetJobDigest        string                     `json:"targetJobDigest"`
	TargetSpecDigest       string                     `json:"targetSpecDigest"`
	TargetOperationID      string                     `json:"targetOperationId"`
	TargetExecutionID      string                     `json:"targetExecutionId"`
	OldAllocationIDs       []string                   `json:"oldAllocationIds"`
	TargetJobVersion       uint64                     `json:"targetJobVersion"`
	TargetJobModifyIndex   uint64                     `json:"targetJobModifyIndex"`
	TargetReplicas         int                        `json:"targetReplicas"`
	SurgeLimit             int                        `json:"surgeLimit"`
	PolicyApprovalID       string                     `json:"policyApprovalId"`
	PolicyDigest           string                     `json:"policyDigest"`
	FirstRouteGeneration   uint64                     `json:"firstRouteGeneration"`
	RouteSteps             []WorkloadReleaseRouteStep `json:"routeSteps"`
}

// WorkloadReleaseRouteStep is an explicitly admitted traffic transition.
// Weight values are supplied by the admission policy; this type does not
// choose a canary percentage.
type WorkloadReleaseRouteStep struct {
	OldWeight    int `json:"oldWeight"`
	TargetWeight int `json:"targetWeight"`
}

// WorkloadReleaseReceipt records only verified progress. Route hashes and
// generations must come from ingress readback, and drained IDs from scheduler
// observation; a command acknowledgement alone is not sufficient evidence.
type WorkloadReleaseReceipt struct {
	IntentID                 string                                 `json:"intentId"`
	Phase                    string                                 `json:"phase"`
	TargetAllocations        []WorkloadReleaseAllocationObservation `json:"targetAllocations"`
	RouteStep                int                                    `json:"routeStep"`
	RouteOldWeight           int                                    `json:"routeOldWeight"`
	RouteTargetWeight        int                                    `json:"routeTargetWeight"`
	RouteGeneration          uint64                                 `json:"routeGeneration"`
	RouteSHA256              string                                 `json:"routeSha256"`
	OldJobStoppedVersion     uint64                                 `json:"oldJobStoppedVersion"`
	OldJobStoppedModifyIndex uint64                                 `json:"oldJobStoppedModifyIndex"`
	DrainedOldIDs            []string                               `json:"drainedOldIds"`
	HeldReason               string                                 `json:"heldReason,omitempty"`
}

// WorkloadReleaseAllocationObservation is a redacted scheduler identity
// projection. A Nomad adapter must populate it from exact job/allocation
// readback, including the job version pinned in the intent.
type WorkloadReleaseAllocationObservation struct {
	AllocationID   string `json:"allocationId"`
	NodeID         string `json:"nodeId"`
	JobID          string `json:"jobId"`
	Namespace      string `json:"namespace"`
	TaskGroup      string `json:"taskGroup"`
	JobVersion     uint64 `json:"jobVersion"`
	JobModifyIndex uint64 `json:"jobModifyIndex"`
	ImageDigest    string `json:"imageDigest"`
	Healthy        bool   `json:"healthy"`
}

// CanonicalDigest returns a stable receipt digest suitable for compare-and-
// swap storage. The digest is not an authorization signature; callers must
// still bind it to the workload intent and current operation claim.
func (r WorkloadReleaseReceipt) CanonicalDigest(i WorkloadReleaseIntent) (string, error) {
	if err := r.ValidateAgainst(i); err != nil {
		return "", err
	}
	copy := r
	copy.TargetAllocations = append([]WorkloadReleaseAllocationObservation(nil), r.TargetAllocations...)
	sort.Slice(copy.TargetAllocations, func(left, right int) bool {
		return copy.TargetAllocations[left].AllocationID < copy.TargetAllocations[right].AllocationID
	})
	copy.DrainedOldIDs = append([]string(nil), r.DrainedOldIDs...)
	sort.Strings(copy.DrainedOldIDs)
	encoded, err := json.Marshal(copy)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

const (
	WorkloadReleasePlanned  = "planned"
	WorkloadReleaseWarming  = "warming"
	WorkloadReleaseRouting  = "routing"
	WorkloadReleaseDraining = "draining"
	WorkloadReleaseComplete = "complete"
	WorkloadReleaseHeld     = "held"
)

// Validate checks the immutable stateless-release contract before any
// controller adapter can use it. Route steps must begin and end at the exact
// old/target versions and every step must conserve 100 percent traffic.
func (i WorkloadReleaseIntent) Validate() error {
	if strings.TrimSpace(i.ID) == "" || strings.TrimSpace(i.App) == "" || strings.TrimSpace(i.Region) == "" || strings.TrimSpace(i.NomadRegion) == "" || strings.TrimSpace(i.Namespace) == "" ||
		strings.TrimSpace(i.ControlEnvironment) == "" || strings.TrimSpace(i.FleetCluster) == "" || strings.TrimSpace(i.FleetEnvironment) == "" || len(i.FleetDatacenters) == 0 || i.FleetTargetGeneration == 0 || strings.TrimSpace(i.Process) == "" || strings.TrimSpace(i.Endpoint) == "" ||
		!validReleaseDigest(i.SpecDigest) || strings.TrimSpace(i.SourceRevision) == "" ||
		strings.TrimSpace(i.OldDeploymentID) == "" || !validReleaseDigest(i.OldImageDigest) || !validReleaseDigest(i.OldSpecDigest) ||
		strings.TrimSpace(i.OldJobID) == "" || !validReleaseDigest(i.OldJobDigest) || i.OldJobModifyIndex == 0 ||
		strings.TrimSpace(i.TargetDeploymentID) == "" || i.TargetDeploymentID == i.OldDeploymentID || !validReleaseDigest(i.TargetImageDigest) || !contentAddressedImageMatches(i.TargetImage, i.TargetImageDigest) ||
		strings.TrimSpace(i.TargetJobID) == "" || i.TargetJobID == i.OldJobID || !validReleaseDigest(i.TargetJobDigest) || !validReleaseDigest(i.TargetSpecDigest) || strings.TrimSpace(i.TargetOperationID) == "" || strings.TrimSpace(i.TargetExecutionID) == "" ||
		i.TargetReplicas < 1 || i.SurgeLimit < 1 || strings.TrimSpace(i.PolicyApprovalID) == "" || !validReleaseDigest(i.PolicyDigest) || i.TargetJobModifyIndex == 0 || i.FirstRouteGeneration == 0 || len(i.OldAllocationIDs) == 0 || len(i.RouteSteps) < 3 {
		return fmt.Errorf("workload release intent is incomplete")
	}
	if uint64(len(i.RouteSteps)-1) > ^uint64(0)-i.FirstRouteGeneration {
		return fmt.Errorf("workload release route generations overflow")
	}
	seen := make(map[string]struct{}, len(i.OldAllocationIDs))
	seenDatacenters := make(map[string]struct{}, len(i.FleetDatacenters))
	for _, datacenter := range i.FleetDatacenters {
		if strings.TrimSpace(datacenter) == "" {
			return fmt.Errorf("workload release has an empty Fleet datacenter")
		}
		if _, exists := seenDatacenters[datacenter]; exists {
			return fmt.Errorf("workload release repeats a Fleet datacenter")
		}
		seenDatacenters[datacenter] = struct{}{}
	}
	for _, id := range i.OldAllocationIDs {
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("workload release has an empty old allocation identity")
		}
		if _, exists := seen[id]; exists {
			return fmt.Errorf("workload release repeats an old allocation identity")
		}
		seen[id] = struct{}{}
	}
	first, last := i.RouteSteps[0], i.RouteSteps[len(i.RouteSteps)-1]
	if first.OldWeight != 100 || first.TargetWeight != 0 || last.OldWeight != 0 || last.TargetWeight != 100 {
		return fmt.Errorf("workload release route sequence must begin old-only and end target-only")
	}
	hasMixedStep := false
	previousTargetWeight := -1
	for index, step := range i.RouteSteps {
		if step.OldWeight < 0 || step.TargetWeight < 0 || step.OldWeight+step.TargetWeight != 100 {
			return fmt.Errorf("workload release route weights must total 100")
		}
		if step.TargetWeight < previousTargetWeight {
			return fmt.Errorf("workload release route sequence cannot move traffic backward")
		}
		if index > 0 && (step.TargetWeight <= previousTargetWeight || step.OldWeight == 0 && step.TargetWeight != 100) {
			return fmt.Errorf("workload release route sequence must make strict progress through mixed steps to target-only")
		}
		previousTargetWeight = step.TargetWeight
		if step.OldWeight > 0 && step.TargetWeight > 0 {
			hasMixedStep = true
		}
	}
	if !hasMixedStep {
		return fmt.Errorf("workload release route sequence requires a mixed-version step")
	}
	probe := &model.TrafficProbeSpec{Path: i.TrafficProbePath, BodySHA256: i.TrafficProbeBodySHA256}
	if err := model.ValidateTrafficProbe(probe); err != nil {
		return fmt.Errorf("workload release traffic probe is invalid: %w", err)
	}
	probeIsPublic := false
	for _, path := range i.PublicPaths {
		if path == i.TrafficProbePath {
			probeIsPublic = true
			break
		}
	}
	if !probeIsPublic {
		return fmt.Errorf("workload release traffic probe must be an admitted public path")
	}
	return nil
}

func contentAddressedImageMatches(image, digest string) bool {
	separator := strings.LastIndex(image, "@sha256:")
	if separator <= 0 || len(digest) != len("sha256:")+64 || !strings.HasPrefix(digest, "sha256:") {
		return false
	}
	return image[separator+1:] == digest
}

// RenderRouteStep uses the production ingress renderer for an admitted route
// transition. It does not authorize or publish the route; a durable route
// intent and generation-fenced node publication are still required.
func (i WorkloadReleaseIntent) RenderRouteStep(index int) (ingress.RenderedRoute, error) {
	if err := i.Validate(); err != nil {
		return ingress.RenderedRoute{}, err
	}
	if index < 0 || index >= len(i.RouteSteps) {
		return ingress.RenderedRoute{}, fmt.Errorf("workload release route step is outside the intent")
	}
	step := i.RouteSteps[index]
	backends := make([]ingress.WeightedBackend, 0, 2)
	if step.OldWeight > 0 {
		backends = append(backends, ingress.WeightedBackend{DeploymentID: i.OldDeploymentID, Weight: step.OldWeight})
	}
	if step.TargetWeight > 0 {
		backends = append(backends, ingress.WeightedBackend{DeploymentID: i.TargetDeploymentID, Weight: step.TargetWeight})
	}
	route, err := ingress.RenderWeightedRoute(ingress.WeightedRoute{
		App: i.App, Process: i.Process, Region: i.Region, Endpoint: i.Endpoint,
		Backends:    backends,
		PublicPaths: i.PublicPaths, RetryGETPaths: i.RetryGETPaths,
		PrivateReadinessPath: i.PrivateReadinessPath,
	})
	if err != nil {
		return ingress.RenderedRoute{}, fmt.Errorf("render admitted workload route step: %w", err)
	}
	if err := ingress.RequireTLSRenderedRoute(route); err != nil {
		return ingress.RenderedRoute{}, err
	}
	return route, nil
}

// TargetAllocationObservations converts the Nomad client's verified job and
// allocation readback into the release receipt projection. Pending health is
// retained as evidence; indeterminate provenance is rejected.
func (i WorkloadReleaseIntent) TargetAllocationObservations(observation nomad.DeploymentJobHealthObservation) ([]WorkloadReleaseAllocationObservation, error) {
	if err := i.Validate(); err != nil {
		return nil, err
	}
	if (observation.State != nomad.DeploymentJobHealthPending && observation.State != nomad.DeploymentJobHealthReady) ||
		observation.JobVersion != i.TargetJobVersion || observation.JobModifyIndex != i.TargetJobModifyIndex {
		return nil, fmt.Errorf("Nomad target readback differs from the pinned workload release job")
	}
	allocations := make([]WorkloadReleaseAllocationObservation, 0, len(observation.Allocations))
	healthyIDs := make(map[string]struct{}, len(observation.AllocationIDs))
	for _, id := range observation.AllocationIDs {
		if id == "" {
			return nil, fmt.Errorf("Nomad target readback contains an empty healthy allocation identity")
		}
		if _, duplicate := healthyIDs[id]; duplicate {
			return nil, fmt.Errorf("Nomad target readback repeats a healthy allocation identity")
		}
		healthyIDs[id] = struct{}{}
	}
	seenHealthy := make(map[string]struct{}, len(healthyIDs))
	for _, allocation := range observation.Allocations {
		if allocation.JobID != i.TargetJobID || allocation.Namespace != i.Namespace || allocation.TaskGroup != i.Process ||
			allocation.JobVersion != i.TargetJobVersion || allocation.JobModifyIndex != i.TargetJobModifyIndex || allocation.ImageDigest != i.TargetImageDigest {
			return nil, fmt.Errorf("Nomad target allocation readback differs from the release intent")
		}
		_, aggregateHealthy := healthyIDs[allocation.AllocationID]
		if aggregateHealthy != allocation.Healthy {
			return nil, fmt.Errorf("Nomad aggregate and per-allocation health readbacks disagree")
		}
		if allocation.Healthy {
			seenHealthy[allocation.AllocationID] = struct{}{}
		}
		allocations = append(allocations, WorkloadReleaseAllocationObservation{
			AllocationID: allocation.AllocationID, NodeID: allocation.NodeID, JobID: allocation.JobID,
			Namespace: allocation.Namespace, TaskGroup: allocation.TaskGroup, JobVersion: allocation.JobVersion,
			JobModifyIndex: allocation.JobModifyIndex, ImageDigest: allocation.ImageDigest, Healthy: allocation.Healthy,
		})
	}
	if len(seenHealthy) != len(healthyIDs) || (observation.State == nomad.DeploymentJobHealthReady && len(seenHealthy) != i.TargetReplicas) {
		return nil, fmt.Errorf("Nomad aggregate health does not match the target allocation observations")
	}
	return allocations, nil
}

// OldDeploymentStopRequest constructs the CAS stop/readback input from the
// immutable intent and the durable effect's unique operation marker.
func (i WorkloadReleaseIntent) OldDeploymentStopRequest(stopOperationID string) (nomad.StoppedDeploymentRequest, error) {
	if err := i.Validate(); err != nil {
		return nomad.StoppedDeploymentRequest{}, err
	}
	if strings.TrimSpace(stopOperationID) == "" || i.OldJobVersion == ^uint64(0) {
		return nomad.StoppedDeploymentRequest{}, fmt.Errorf("old deployment stop operation identity is incomplete")
	}
	intentDigest, err := i.CanonicalDigest()
	if err != nil {
		return nomad.StoppedDeploymentRequest{}, err
	}
	return nomad.StoppedDeploymentRequest{
		JobID: i.OldJobID, Region: i.NomadRegion, Namespace: i.Namespace, DeploymentID: i.OldDeploymentID,
		StopOperationID: stopOperationID, IntentDigest: intentDigest,
		SpecDigest: i.OldSpecDigest, JobDigest: i.OldJobDigest, ImageDigest: i.OldImageDigest,
		TaskGroup: i.Process, OriginalJobVersion: i.OldJobVersion, OriginalJobModifyIndex: i.OldJobModifyIndex,
		AllocationIDs: append([]string(nil), i.OldAllocationIDs...),
	}, nil
}

// WithTargetAllocationReadback binds one verified Nomad observation to the
// receipt and validates the complete projection before returning it. Callers
// still need to persist it with a durable compare-and-swap before acting.
func (r WorkloadReleaseReceipt) WithTargetAllocationReadback(i WorkloadReleaseIntent, observation nomad.DeploymentJobHealthObservation) (WorkloadReleaseReceipt, error) {
	allocations, err := i.TargetAllocationObservations(observation)
	if err != nil {
		return WorkloadReleaseReceipt{}, err
	}
	next := r
	next.TargetAllocations = allocations
	if err := next.ValidateAgainst(i); err != nil {
		return WorkloadReleaseReceipt{}, fmt.Errorf("Nomad target receipt projection is invalid: %w", err)
	}
	return next, nil
}

// WithOldDeploymentDrainReadback binds the exact stop effect revision and
// terminal allocation set verified by the read-only Nomad observer.
func (r WorkloadReleaseReceipt) WithOldDeploymentDrainReadback(i WorkloadReleaseIntent, stoppedVersion, stoppedModifyIndex uint64, observation nomad.StoppedDeploymentObservation) (WorkloadReleaseReceipt, error) {
	if err := i.Validate(); err != nil {
		return WorkloadReleaseReceipt{}, err
	}
	ids := append([]string(nil), observation.DrainedAllocationIDs...)
	sort.Strings(ids)
	want := append([]string(nil), i.OldAllocationIDs...)
	sort.Strings(want)
	if i.OldJobVersion == ^uint64(0) || stoppedVersion != i.OldJobVersion+1 || stoppedModifyIndex <= i.OldJobModifyIndex ||
		observation.JobID != i.OldJobID || observation.DeploymentID != i.OldDeploymentID ||
		observation.JobVersion != stoppedVersion || observation.JobModifyIndex != stoppedModifyIndex || !equalReleaseStrings(ids, want) {
		return WorkloadReleaseReceipt{}, fmt.Errorf("Nomad old deployment drain readback differs from the pinned release intent")
	}
	next := r
	next.OldJobStoppedVersion = observation.JobVersion
	next.OldJobStoppedModifyIndex = observation.JobModifyIndex
	next.DrainedOldIDs = ids
	if err := next.ValidateAgainst(i); err != nil {
		return WorkloadReleaseReceipt{}, fmt.Errorf("Nomad old deployment receipt projection is invalid: %w", err)
	}
	return next, nil
}

func equalReleaseStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

// ValidateAgainst prevents receipts from another intent or from claiming
// progress unsupported by route and allocation identities in this intent.
func (r WorkloadReleaseReceipt) ValidateAgainst(i WorkloadReleaseIntent) error {
	if err := i.Validate(); err != nil {
		return err
	}
	if r.IntentID != i.ID || !knownReleasePhase(r.Phase) || r.RouteStep < 0 || r.RouteStep >= len(i.RouteSteps) {
		return fmt.Errorf("workload release receipt identity or phase is invalid")
	}
	oldSet := make(map[string]struct{}, len(i.OldAllocationIDs))
	for _, id := range i.OldAllocationIDs {
		oldSet[id] = struct{}{}
	}
	targetSet := make(map[string]struct{}, len(r.TargetAllocations))
	targetNodes := make(map[string]struct{}, len(r.TargetAllocations))
	healthySet := make(map[string]struct{}, len(r.TargetAllocations))
	for _, allocation := range r.TargetAllocations {
		if allocation.AllocationID == "" || allocation.NodeID == "" ||
			allocation.JobID != i.TargetJobID || allocation.Namespace != i.Namespace || allocation.TaskGroup != i.Process ||
			allocation.ImageDigest != i.TargetImageDigest {
			return fmt.Errorf("workload release receipt contains an incomplete target allocation identity")
		}
		if allocation.JobVersion != i.TargetJobVersion || allocation.JobModifyIndex != i.TargetJobModifyIndex {
			return fmt.Errorf("target allocation belongs to an unexpected Nomad job version")
		}
		if _, duplicate := targetSet[allocation.AllocationID]; duplicate {
			return fmt.Errorf("workload release receipt repeats a target allocation identity")
		}
		if _, oldIdentity := oldSet[allocation.AllocationID]; oldIdentity {
			return fmt.Errorf("target allocation reuses an old allocation identity")
		}
		if _, duplicate := targetNodes[allocation.NodeID]; duplicate {
			return fmt.Errorf("target allocations violate distinct-host placement")
		}
		targetSet[allocation.AllocationID] = struct{}{}
		targetNodes[allocation.NodeID] = struct{}{}
		if allocation.Healthy {
			healthySet[allocation.AllocationID] = struct{}{}
		}
	}
	drained := make(map[string]struct{}, len(r.DrainedOldIDs))
	for _, id := range r.DrainedOldIDs {
		if _, known := oldSet[id]; !known {
			return fmt.Errorf("workload release receipt drains an allocation outside the intent")
		}
		if _, duplicate := drained[id]; duplicate {
			return fmt.Errorf("workload release receipt repeats a drain acknowledgement")
		}
		drained[id] = struct{}{}
	}
	if r.OldJobStoppedVersion == 0 || r.OldJobStoppedModifyIndex == 0 {
		if r.OldJobStoppedVersion != 0 || r.OldJobStoppedModifyIndex != 0 || len(drained) != 0 {
			return fmt.Errorf("workload release has incomplete old job stop evidence")
		}
	} else if i.OldJobVersion == ^uint64(0) || r.OldJobStoppedVersion != i.OldJobVersion+1 || r.OldJobStoppedModifyIndex <= i.OldJobModifyIndex {
		return fmt.Errorf("workload release old job stop revision differs from the pinned effect")
	}
	if len(drained) > 0 && (r.RouteStep != len(i.RouteSteps)-1 || r.RouteOldWeight != 0 || r.RouteTargetWeight != 100) {
		return fmt.Errorf("workload release cannot drain before target-only route proof")
	}
	if len(targetSet) > i.TargetReplicas+i.SurgeLimit {
		return fmt.Errorf("workload release receipt exceeds the admitted target surge")
	}
	if r.RouteGeneration == 0 {
		if r.RouteSHA256 != "" || r.RouteStep != 0 || r.RouteOldWeight != 0 || r.RouteTargetWeight != 0 || len(drained) != 0 {
			return fmt.Errorf("workload release receipt has progress without route readback")
		}
	} else {
		if uint64(r.RouteStep) > ^uint64(0)-i.FirstRouteGeneration || r.RouteGeneration != i.FirstRouteGeneration+uint64(r.RouteStep) || !validReleaseDigest(r.RouteSHA256) {
			return fmt.Errorf("workload release route readback identity is invalid")
		}
		admittedStep := i.RouteSteps[r.RouteStep]
		if r.RouteOldWeight != admittedStep.OldWeight || r.RouteTargetWeight != admittedStep.TargetWeight {
			return fmt.Errorf("workload release route readback differs from the admitted route step")
		}
		rendered, err := i.RenderRouteStep(r.RouteStep)
		if err != nil || r.RouteSHA256 != "sha256:"+rendered.SHA256 {
			return fmt.Errorf("workload release route digest differs from the rendered intent")
		}
	}
	if r.Phase != WorkloadReleaseHeld && r.RouteStep > 0 && len(healthySet) == 0 {
		return fmt.Errorf("workload release cannot route traffic without a healthy target allocation")
	}
	if len(drained) > 0 && len(healthySet) != i.TargetReplicas {
		return fmt.Errorf("workload release cannot drain old allocations before full target health")
	}
	if r.Phase == WorkloadReleaseComplete && (r.RouteStep != len(i.RouteSteps)-1 || len(drained) != len(i.OldAllocationIDs) || len(targetSet) != i.TargetReplicas || len(healthySet) != i.TargetReplicas) {
		return fmt.Errorf("workload release completion lacks terminal route or allocation evidence")
	}
	if r.Phase == WorkloadReleaseHeld && strings.TrimSpace(r.HeldReason) == "" {
		return fmt.Errorf("held workload release receipt requires a reason")
	}
	if r.Phase == WorkloadReleasePlanned && (r.RouteGeneration != 0 || len(targetSet) != 0 || len(drained) != 0) {
		return fmt.Errorf("planned workload release receipt contains execution progress")
	}
	return nil
}

// ValidateSuccessor proves that next is a safe forward-only continuation of
// the durable receipt. It is intentionally a pure check: the caller must
// persist the new receipt with a compare-and-swap bound to its current digest.
func (next WorkloadReleaseReceipt) ValidateSuccessor(i WorkloadReleaseIntent, current WorkloadReleaseReceipt) error {
	if err := current.ValidateAgainst(i); err != nil {
		return fmt.Errorf("current workload release receipt is invalid: %w", err)
	}
	if err := next.ValidateAgainst(i); err != nil {
		return fmt.Errorf("next workload release receipt is invalid: %w", err)
	}
	if current.Phase == WorkloadReleaseComplete || current.Phase == WorkloadReleaseHeld {
		return fmt.Errorf("terminal workload release receipt cannot advance")
	}
	if current.OldJobStoppedVersion != 0 && (next.OldJobStoppedVersion != current.OldJobStoppedVersion || next.OldJobStoppedModifyIndex != current.OldJobStoppedModifyIndex) {
		return fmt.Errorf("old deployment stop effect receipt cannot be replaced")
	}
	if !validReleasePhaseTransition(current, next, i) {
		return fmt.Errorf("workload release phase transition lacks its required route, health, or drain evidence")
	}
	if next.RouteStep < current.RouteStep || next.RouteGeneration < current.RouteGeneration {
		return fmt.Errorf("workload release progress cannot move backward")
	}
	if len(next.DrainedOldIDs) < len(current.DrainedOldIDs) {
		return fmt.Errorf("workload release drain evidence cannot be removed")
	}
	currentDrained := make(map[string]struct{}, len(current.DrainedOldIDs))
	for _, id := range current.DrainedOldIDs {
		currentDrained[id] = struct{}{}
	}
	nextDrained := make(map[string]struct{}, len(next.DrainedOldIDs))
	for _, id := range next.DrainedOldIDs {
		nextDrained[id] = struct{}{}
		if _, retained := currentDrained[id]; !retained {
			if current.Phase != WorkloadReleaseDraining || current.RouteStep != len(i.RouteSteps)-1 || current.RouteOldWeight != 0 || current.RouteTargetWeight != 100 || healthyTargetCount(current) != i.TargetReplicas {
				return fmt.Errorf("new drain requires prior target-only route proof and full target health")
			}
		}
	}
	for id := range currentDrained {
		if _, retained := nextDrained[id]; !retained {
			return fmt.Errorf("drained allocation acknowledgement cannot be removed")
		}
	}
	nextHealthy := make(map[string]struct{}, len(next.TargetAllocations))
	for _, allocation := range next.TargetAllocations {
		if allocation.Healthy {
			nextHealthy[allocation.AllocationID] = struct{}{}
		}
	}
	for _, allocation := range current.TargetAllocations {
		if allocation.Healthy {
			_, retained := nextHealthy[allocation.AllocationID]
			if !retained && current.RouteStep < len(i.RouteSteps)-1 {
				// A health regression is valid new evidence, but it cannot accompany
				// route advancement or a newly acknowledged drain.
				if next.RouteStep > current.RouteStep || len(next.DrainedOldIDs) > len(current.DrainedOldIDs) {
					return fmt.Errorf("route or drain progress requires retained target health")
				}
			}
			if _, retained := nextDrained[allocation.AllocationID]; retained {
				return fmt.Errorf("target allocation cannot also be drained as old")
			}
		}
	}
	if current.RouteGeneration > 0 && next.RouteGeneration == current.RouteGeneration &&
		(next.RouteSHA256 != current.RouteSHA256 || next.RouteStep != current.RouteStep) {
		return fmt.Errorf("route content or step changed without a new generation")
	}
	if current.RouteGeneration > 0 && next.RouteGeneration > current.RouteGeneration && next.RouteGeneration != current.RouteGeneration+1 {
		return fmt.Errorf("route generation must advance exactly once per receipt")
	}
	if current.RouteGeneration > 0 && next.RouteGeneration > current.RouteGeneration && next.RouteStep != current.RouteStep+1 {
		return fmt.Errorf("new route generation must correspond to the next admitted route step")
	}
	if next.RouteStep > current.RouteStep && next.RouteStep != current.RouteStep+1 {
		return fmt.Errorf("route step must advance exactly once per receipt")
	}
	if next.RouteStep > current.RouteStep && next.RouteGeneration != current.RouteGeneration+1 {
		return fmt.Errorf("route step advancement requires exactly one new route generation")
	}
	if releasePhaseRank(next.Phase) < releasePhaseRank(current.Phase) {
		return fmt.Errorf("workload release phase cannot move backward")
	}
	return nil
}

func validReleasePhaseTransition(current, next WorkloadReleaseReceipt, intent WorkloadReleaseIntent) bool {
	if current.Phase == next.Phase {
		return true
	}
	if next.Phase == WorkloadReleaseHeld {
		return true
	}
	switch current.Phase {
	case WorkloadReleasePlanned:
		return next.Phase == WorkloadReleaseWarming && next.RouteGeneration == 0 && len(next.TargetAllocations) == 0
	case WorkloadReleaseWarming:
		return next.Phase == WorkloadReleaseRouting && len(next.TargetAllocations) > 0 && healthyTargetCount(next) > 0 && next.RouteStep == 0
	case WorkloadReleaseRouting:
		return next.Phase == WorkloadReleaseDraining && next.RouteStep == len(intent.RouteSteps)-1 && next.RouteOldWeight == 0 && next.RouteTargetWeight == 100 && healthyTargetCount(current) == intent.TargetReplicas && healthyTargetCount(next) == intent.TargetReplicas
	case WorkloadReleaseDraining:
		return next.Phase == WorkloadReleaseComplete && next.RouteStep == len(intent.RouteSteps)-1 && healthyTargetCount(next) == intent.TargetReplicas && len(next.DrainedOldIDs) == len(intent.OldAllocationIDs)
	default:
		return false
	}
}

func healthyTargetCount(receipt WorkloadReleaseReceipt) int {
	count := 0
	for _, allocation := range receipt.TargetAllocations {
		if allocation.Healthy {
			count++
		}
	}
	return count
}

// CanonicalDigest returns a stable digest for binding this immutable contract
// into a future signed admission envelope.
func (i WorkloadReleaseIntent) CanonicalDigest() (string, error) {
	if err := i.Validate(); err != nil {
		return "", err
	}
	copy := i
	copy.OldAllocationIDs = append([]string(nil), i.OldAllocationIDs...)
	sort.Strings(copy.OldAllocationIDs)
	copy.PublicPaths = append([]string(nil), i.PublicPaths...)
	sort.Strings(copy.PublicPaths)
	copy.RetryGETPaths = append([]string(nil), i.RetryGETPaths...)
	sort.Strings(copy.RetryGETPaths)
	encoded, err := json.Marshal(copy)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func validReleaseDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	decoded, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil && len(decoded) == sha256.Size
}

func knownReleasePhase(phase string) bool {
	switch phase {
	case WorkloadReleasePlanned, WorkloadReleaseWarming, WorkloadReleaseRouting, WorkloadReleaseDraining, WorkloadReleaseComplete, WorkloadReleaseHeld:
		return true
	default:
		return false
	}
}

func releasePhaseRank(phase string) int {
	switch phase {
	case WorkloadReleasePlanned:
		return 0
	case WorkloadReleaseWarming:
		return 1
	case WorkloadReleaseRouting:
		return 2
	case WorkloadReleaseDraining:
		return 3
	case WorkloadReleaseComplete:
		return 4
	case WorkloadReleaseHeld:
		return 5
	default:
		return -1
	}
}
