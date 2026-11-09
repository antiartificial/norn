package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"norn/v2/api/config"
	"norn/v2/api/etcdstore"
	"norn/v2/api/handler"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/pipeline"
	"norn/v2/api/secrets"
	"norn/v2/api/store"
)

const workloadReleaseAdmissionBodyLimit = 16 << 10

type workloadReleaseAdmissionStore interface {
	store.PrivateWorkloadReleaseStore
	Authority(context.Context) (string, error)
	CurrentFleetAppTarget(context.Context, string, string) (etcdstore.FleetAppTarget, int64, error)
	CurrentWorkloadReleasePolicy(context.Context, string, string) (store.WorkloadReleasePolicy, int64, error)
	CurrentWorkloadReleaseServingRoute(context.Context, string, string, string) (etcdstore.WorkloadReleaseServingRoute, error)
	ResolveWorkloadReleaseQualification(context.Context, string, string, []string, time.Time) (model.ReleaseQualification, error)
	GetDeployment(context.Context, string) (*model.Deployment, error)
}

// Only stable caller-selected values are accepted. The API derives the plan,
// serving lineage, policy, and encrypted SOPS snapshot from trusted state.
type workloadReleaseAdmissionRequest struct {
	QualificationID string `json:"qualificationId"`
	TargetReplicas  int    `json:"targetReplicas"`
}

func etcdWorkloadReleaseAdmission(cfg *config.Config, operations workloadReleaseAdmissionStore,
	identities store.IdentityStore, keys *store.WorkloadReleaseInputKeyRing,
	lineage nomad.ActiveDeploymentLineageObserver, trustedQualificationKeys []string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		app := chi.URLParam(r, "id")
		principal, ok := handler.AccessPrincipalFromRequest(r)
		if cfg == nil || cfg.EnvironmentID() != "staging" || operations == nil || identities == nil || keys == nil || lineage == nil ||
			!etcdFleetName(app) || !ok || principal.Source != handler.AccessPrincipalSourceManagedToken || principal.TokenID == "" ||
			principal.Legacy || principal.CI != nil || !principal.Allows(handler.ScopeReleaseStage) ||
			(principal.App != "" && principal.App != app) || (principal.Environment != "" && principal.Environment != "staging") {
			handler.WriteControlProblem(w, r, http.StatusForbidden, "workload_release_identity_invalid", "a managed staging operator identity is required")
			return
		}
		var request workloadReleaseAdmissionRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, workloadReleaseAdmissionBodyLimit))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "invalid_workload_release_request", "request must contain one qualificationId and targetReplicas")
			return
		}
		key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
		if key == "" || len(key) > 512 {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "idempotency_key_required", "a bounded Idempotency-Key is required")
			return
		}
		requestDigest, err := pipeline.WorkloadReleaseRequestDigest(request.QualificationID, request.TargetReplicas)
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "invalid_workload_release_selection", "qualificationId and targetReplicas must identify the stable request selection")
			return
		}
		identity, err := workloadReleaseAdmissionIdentity(r.Context(), operations, identities, principal, app, key)
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "workload_release_identity_unavailable", "stable operator identity is unavailable")
			return
		}
		now := time.Now().UTC()
		operation := model.Operation{ID: uuid.NewString(), Kind: "app.workload-release", App: app, Status: model.OperationQueued,
			Risk: "approved mixed-version workload release", Source: "release-control-api", Message: "queued workload release",
			StartedAt: now, MaxAttempts: 3, Metadata: map[string]interface{}{"workloadReleaseRequestDigest": requestDigest}}
		if prior, replayErr := operations.ResolvePrivateWorkloadReleaseReplay(r.Context(), identity, operation); replayErr == nil {
			writeWorkloadReleaseAdmissionResult(w, http.StatusOK, prior, true)
			return
		} else if !errors.Is(replayErr, store.ErrAcceptanceNotFound) {
			writeWorkloadReleaseAdmissionError(w, r, replayErr)
			return
		}
		qualification, err := operations.ResolveWorkloadReleaseQualification(r.Context(), app, request.QualificationID,
			trustedQualificationKeys, now)
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusConflict, "workload_release_qualification_unavailable", "a current successful V3 staging qualification is required")
			return
		}
		target, _, err := operations.CurrentFleetAppTarget(r.Context(), app, "staging")
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusConflict, "workload_release_target_unavailable", "a current control-owned staging Fleet target is required")
			return
		}
		policy, _, err := operations.CurrentWorkloadReleasePolicy(r.Context(), app, "staging")
		if err != nil || request.TargetReplicas < policy.MinReplicas || request.TargetReplicas > policy.MaxReplicas {
			handler.WriteControlProblem(w, r, http.StatusConflict, "workload_release_policy_unavailable", "the requested replica count must fit the current approved policy")
			return
		}
		route, err := operations.CurrentWorkloadReleaseServingRoute(r.Context(), app, "staging", target.Region)
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusConflict, "workload_release_route_unavailable", "a verified serving route is required before workload release")
			return
		}
		spec, err := workloadReleaseSourceSpec(cfg.AppsDir, app)
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusConflict, "workload_release_source_unavailable", "the current pinned app source is unavailable")
			return
		}
		endpoint, err := model.ResolveFleetRouteEndpoint(spec, target.Region)
		if err != nil || endpoint.Process != route.Process {
			handler.WriteControlProblem(w, r, http.StatusConflict, "workload_release_source_ineligible", "the source does not declare the currently serving stateless service")
			return
		}
		deployment, err := operations.GetDeployment(r.Context(), qualification.DeploymentID)
		targetSpecDigest, digestErr := model.InfraSpecDigest(spec)
		if err != nil || deployment == nil || digestErr != nil || deployment.SpecDigest != targetSpecDigest {
			handler.WriteControlProblem(w, r, http.StatusConflict, "workload_release_source_mismatch", "the current app source must match the qualified immutable staging release")
			return
		}
		servingLineage, err := lineage.ObserveActiveDeploymentLineage(r.Context(), app, target.Region, target.NomadRegion,
			target.NomadNamespace, route.DeploymentID, route.OperationID, route.SpecDigest,
			strings.TrimPrefix(route.JobDigest, "sha256:"), route.ImageTag, route.Process)
		if err != nil || servingLineage.DeploymentID != route.DeploymentID || servingLineage.OperationID != route.OperationID ||
			servingLineage.JobDigest != strings.TrimPrefix(route.JobDigest, "sha256:") || servingLineage.SpecDigest != route.SpecDigest ||
			servingLineage.ImageTag != route.ImageTag || servingLineage.Process != route.Process || servingLineage.JobVersion == 0 || servingLineage.ModifyIndex == 0 {
			handler.WriteControlProblem(w, r, http.StatusConflict, "workload_release_lineage_unavailable", "exact healthy Nomad serving lineage could not be verified")
			return
		}
		policyDigest, err := etcdstore.WorkloadReleasePolicyDigest(policy)
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusConflict, "workload_release_policy_invalid", "the current overlap policy is invalid")
			return
		}
		plan, err := deriveWorkloadReleasePlan(target, policy, policyDigest, route, servingLineage, endpoint, qualification, request.TargetReplicas, spec)
		if err != nil || plan.ValidateWorkloadReleaseQualification(qualification, trustedQualificationKeys, now) != nil {
			handler.WriteControlProblem(w, r, http.StatusConflict, "workload_release_plan_unavailable", "current release inputs do not form an admissible workload plan")
			return
		}
		snapshot, err := secrets.NewManager(cfg.AppsDir).SnapshotEncrypted(app)
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusConflict, "workload_release_secret_snapshot_unavailable", "the protected encrypted app input snapshot is unavailable")
			return
		}
		defer clear(snapshot.Ciphertext)
		pinnedMaterial, err := store.EncodeWorkloadReleasePinnedMaterial(spec, plan.Intent.TargetSpecDigest, snapshot.Ciphertext)
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusConflict, "workload_release_source_snapshot_unavailable", "the exact job-driving app source could not be pinned")
			return
		}
		defer clear(pinnedMaterial)
		planBytes, err := json.Marshal(plan)
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "invalid_workload_release_plan", "derived plan could not be canonicalized")
			return
		}
		operation.Ref = qualification.ID
		operation.Payload = map[string]interface{}{"workloadReleasePlan": string(planBytes)}
		acceptance := store.OperationAcceptance{Identity: identity, Operation: operation,
			Admission: store.OperationAdmissionPolicy{OneActiveMutablePerApp: true},
			Audit: store.AcceptanceAuditContext{CredentialID: principal.TokenID, DeviceID: principal.DeviceID,
				Source: "etcd-workload-release-api", Scopes: append([]string(nil), principal.Scopes...)},
			Semantics: map[string]interface{}{"workloadReleaseRequestDigest": requestDigest}}
		accepted, err := operations.AcceptPrivateWorkloadRelease(r.Context(), acceptance, qualification,
			trustedQualificationKeys, pinnedMaterial, keys, lineage)
		if err != nil {
			writeWorkloadReleaseAdmissionError(w, r, err)
			return
		}
		status := http.StatusCreated
		if accepted.Replayed {
			status = http.StatusOK
		}
		writeWorkloadReleaseAdmissionResult(w, status, accepted, accepted.Replayed)
	}
}

func workloadReleaseAdmissionIdentity(ctx context.Context, operations workloadReleaseAdmissionStore, identities store.IdentityStore,
	principal handler.AccessPrincipal, app, key string) (store.OperationRequestIdentity, error) {
	authority, err := operations.Authority(ctx)
	if err != nil || strings.TrimSpace(authority) == "" || principal.TokenID == "" || app == "" || key == "" {
		return store.OperationRequestIdentity{}, fmt.Errorf("workload release identity is incomplete")
	}
	root, err := identities.RootAccessToken(ctx, principal.TokenID)
	if err != nil || root == "" {
		return store.OperationRequestIdentity{}, fmt.Errorf("managed operator token lineage is unavailable")
	}
	return store.OperationRequestIdentity{Authority: authority, Actor: store.OperationActor{Issuer: "norn://managed-token", Subject: root},
		Kind: "app.workload-release", Resource: "app/" + app, Key: key}, nil
}

func writeWorkloadReleaseAdmissionResult(w http.ResponseWriter, status int, accepted store.AcceptedOperation, replayed bool) {
	writeEtcdSourceJSON(w, status, map[string]interface{}{"operationId": accepted.Operation.ID,
		"status": accepted.Operation.Status, "replayed": replayed, "acceptanceDigest": accepted.Intent.CanonicalDigest})
}

func writeWorkloadReleaseAdmissionError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message := http.StatusConflict, "workload_release_admission_failed", "workload release could not be admitted"
	switch {
	case errors.Is(err, store.ErrAcceptanceNotFound):
		status, code, message = http.StatusNotFound, "workload_release_replay_not_found", "prior workload release was not found"
	case errors.Is(err, store.ErrAcceptanceConflict):
		status, code, message = http.StatusConflict, "workload_release_idempotency_conflict", "idempotency key is bound to a different workload release selection"
	case errors.Is(err, store.ErrAcceptanceInvalid):
		status, code, message = http.StatusBadRequest, "invalid_workload_release_admission", "workload release evidence does not satisfy admission"
	case errors.Is(err, store.ErrAcceptanceIndeterminate):
		status, code, message = http.StatusServiceUnavailable, "workload_release_admission_indeterminate", "workload release acceptance outcome is indeterminate; retry with the same Idempotency-Key"
	case errors.Is(err, etcdstore.ErrWorkloadReleasePolicyNotFound), errors.Is(err, etcdstore.ErrWorkloadReleasePolicyStale):
		status, code, message = http.StatusConflict, "workload_release_policy_unavailable", "an approved policy for the current Fleet placement is required"
	}
	handler.WriteControlProblem(w, r, status, code, message)
}

func deriveWorkloadReleasePlan(target etcdstore.FleetAppTarget, policy store.WorkloadReleasePolicy, policyDigest string,
	route etcdstore.WorkloadReleaseServingRoute, lineage nomad.ActiveDeploymentLineage, endpoint model.FleetRouteEndpoint,
	qualification model.ReleaseQualification, replicas int, spec *model.InfraSpec) (pipeline.WorkloadReleasePlan, error) {
	if spec == nil || target.App != qualification.App || target.Region != lineage.Region || target.NomadRegion != lineage.NomadRegion ||
		target.NomadNamespace != lineage.Namespace || target.Generation == 0 || route.Generation == ^uint64(0) ||
		lineage.Replicas < 1 || len(lineage.AllocationIDs) != lineage.Replicas || replicas < policy.MinReplicas || replicas > policy.MaxReplicas ||
		policy.SurgeLimit < 1 || len(policy.RouteSteps) < 3 || endpoint.Process != route.Process {
		return pipeline.WorkloadReleasePlan{}, fmt.Errorf("workload release plan derivation inputs are incomplete")
	}
	oldImageDigest := ""
	if marker := strings.LastIndex(route.ImageTag, "@sha256:"); marker > 0 {
		oldImageDigest = route.ImageTag[marker+1:]
	}
	targetDeploymentID, targetOperationID := uuid.NewString(), uuid.NewString()
	targetJobID, err := nomad.ManagedDeploymentJobID(target.App, target.Region, targetDeploymentID)
	if err != nil {
		return pipeline.WorkloadReleasePlan{}, err
	}
	targetSpecDigest, err := model.InfraSpecDigest(spec)
	if err != nil {
		return pipeline.WorkloadReleasePlan{}, err
	}
	routeSteps := make([]pipeline.WorkloadReleaseRouteStep, len(policy.RouteSteps))
	for index, step := range policy.RouteSteps {
		routeSteps[index] = pipeline.WorkloadReleaseRouteStep{OldWeight: step.OldWeight, TargetWeight: step.TargetWeight}
	}
	intent := pipeline.WorkloadReleaseIntent{ID: uuid.NewString(), App: target.App, Region: target.Region, NomadRegion: target.NomadRegion,
		Namespace: target.NomadNamespace, ControlEnvironment: target.ControlEnvironment, FleetCluster: target.Cluster,
		FleetEnvironment: target.FleetEnvironment, FleetDatacenters: append([]string(nil), target.Datacenters...), FleetTargetGeneration: target.Generation,
		Process: endpoint.Process, Endpoint: endpoint.Origin, PublicPaths: append([]string(nil), endpoint.PublicPaths...),
		RetryGETPaths: append([]string(nil), endpoint.RetryGETPaths...), PrivateReadinessPath: endpoint.PrivateReadinessPath,
		TrafficProbePath: endpoint.ProbePath, TrafficProbeBodySHA256: endpoint.ProbeBodySHA256,
		SpecDigest: targetSpecDigest, SourceRevision: qualification.SourceSHA, OldDeploymentID: route.DeploymentID,
		OldImageDigest: oldImageDigest, OldSpecDigest: route.SpecDigest, OldJobID: lineage.JobID, OldJobDigest: "sha256:" + lineage.JobDigest,
		OldJobVersion: lineage.JobVersion, OldJobModifyIndex: lineage.ModifyIndex, TargetDeploymentID: targetDeploymentID,
		TargetImage: qualification.Artifact, TargetImageDigest: qualification.Candidate.Attestation.SubjectDigest,
		TargetJobID: targetJobID, TargetSpecDigest: targetSpecDigest, TargetOperationID: targetOperationID,
		TargetExecutionID: nomad.DeploymentEffectExecutionID(targetOperationID, target.Region, "pending"),
		OldAllocationIDs:  append([]string(nil), lineage.AllocationIDs...), TargetReplicas: replicas, SurgeLimit: policy.SurgeLimit,
		PolicyApprovalID: policy.ApprovalID, PolicyDigest: policyDigest, FirstRouteGeneration: route.Generation + 1, RouteSteps: routeSteps}
	if digest, err := pipeline.DeriveWorkloadReleaseTargetJobDigest(intent, spec); err != nil {
		return pipeline.WorkloadReleasePlan{}, err
	} else {
		intent.TargetJobDigest = digest
	}
	intent.TargetExecutionID = nomad.DeploymentEffectExecutionID(targetOperationID, target.Region, strings.TrimPrefix(intent.TargetJobDigest, "sha256:"))
	plan := pipeline.WorkloadReleasePlan{SchemaVersion: pipeline.WorkloadReleasePlanSchema, Intent: intent}
	if err := plan.Validate(); err != nil {
		return pipeline.WorkloadReleasePlan{}, err
	}
	return plan, nil
}
