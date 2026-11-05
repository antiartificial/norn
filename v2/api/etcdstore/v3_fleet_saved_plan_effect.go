package etcdstore

// Durable, one-shot saved-plan apply authority. This is intentionally separate
// from app-delete permits: it never accepts a raw provider request and grants
// only the exact reviewed OpenTofu saved-plan command.

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/fleet"
	"norn/v2/api/fleet/executor"
	"norn/v2/api/fleet/savedplaneffect"
)

var ErrSavedPlanEffectConflict = errors.New("saved plan effect changed or its authority is stale")

// SavedPlanEffectProjectionCandidate is discovery only. It is deliberately
// not signed apply input: Fleet must first prove the exact queued GitHub job,
// create PlanBinding and DispatchBinding, then request the signed projection.
type SavedPlanEffectProjectionCandidate struct {
	PlanID              string `json:"planId"`
	PlanSHA256          string `json:"planSha256"`
	ProtectedCommit     string `json:"protectedCommit"`
	GitHubRunID         int64  `json:"githubRunId"`
	PlanRunID           int64  `json:"planRunId"`
	PlanArtifactSHA256  string `json:"planArtifactSha256"`
	DispatchNonceSHA256 string `json:"dispatchNonceSha256"`
}

type savedPlanEffectRecord struct {
	Claim       savedplaneffect.Claim                     `json:"claim"`
	ApplyGrant  *savedplaneffect.ApplyGrant               `json:"applyGrant,omitempty"`
	RefreshOnly *savedplaneffect.RefreshOnlyAuthorization `json:"refreshOnly,omitempty"`
}

func (r savedPlanEffectRecord) validate() error {
	if r.Claim.Validate() != nil {
		return savedplaneffect.ErrInvalid
	}
	if r.ApplyGrant != nil && r.ApplyGrant.Validate(r.Claim) != nil {
		return savedplaneffect.ErrInvalid
	}
	if r.RefreshOnly != nil && (r.ApplyGrant == nil || r.RefreshOnly.Validate(r.Claim) != nil) {
		return savedplaneffect.ErrInvalid
	}
	return nil
}

func (s *V3OperationStore) savedPlanEffectKey(planID string) string {
	return s.prefix + "/v3/fleet-saved-plan-effect/" + planID
}

func (s *V3OperationStore) savedPlanEffectPlanBindingKey(planID string) string {
	return s.prefix + "/v3/fleet-saved-plan-effect-plan-bindings/" + planID
}

func (s *V3OperationStore) savedPlanEffectDispatchBindingKey(planID string) string {
	return s.prefix + "/v3/fleet-saved-plan-effect-dispatch-bindings/" + planID
}

func (s *V3OperationStore) savedPlanEffectProjectionKey(planID string) string {
	return s.prefix + "/v3/fleet-saved-plan-effect-projections/" + planID
}

// SetSavedPlanEffectProjectionSigner is startup-only configuration for the
// separate asymmetric signer. It is deliberately not the operation/audit HMAC
// signer and must be set before the default-off projection route is mounted.
func (s *V3OperationStore) SetSavedPlanEffectProjectionSigner(signer *savedplaneffect.ProjectionSigner) error {
	if s == nil || signer == nil {
		return savedplaneffect.ErrInvalid
	}
	s.projectionSigner = signer
	return nil
}

// CreateSavedPlanEffectPreparation derives the non-authorizing, pre-run inbox
// item. The verifier submits no material; later prepared-plan facts are not
// present and cannot be authorized here.
func (s *V3OperationStore) CreateSavedPlanEffectPreparation(ctx context.Context, planID, principal string, now time.Time) (savedplaneffect.SignedPreparationProjection, error) {
	if s == nil || s.projectionSigner == nil || uuid.Validate(planID) != nil || principal != "fleet-saved-plan-effect-verifier:work-staging" {
		return savedplaneffect.SignedPreparationProjection{}, savedplaneffect.ErrInvalid
	}
	planKey, dispatchBindingKey, projectionKey := s.opKey(planID), s.savedPlanEffectDispatchBindingKey(planID), s.savedPlanEffectProjectionKey(planID)
	response, err := s.kv.Txn(ctx).Then(clientv3.OpGet(planKey), clientv3.OpGet(dispatchBindingKey), clientv3.OpGet(projectionKey)).Commit()
	if err != nil {
		return savedplaneffect.SignedPreparationProjection{}, err
	}
	if len(response.Responses) != 3 || len(response.Responses[0].GetResponseRange().Kvs) != 1 || len(response.Responses[1].GetResponseRange().Kvs) != 1 {
		return savedplaneffect.SignedPreparationProjection{}, savedplaneffect.ErrInvalid
	}
	if len(response.Responses[2].GetResponseRange().Kvs) != 0 {
		return savedplaneffect.SignedPreparationProjection{}, ErrSavedPlanEffectConflict
	}
	planRow, dispatchBindingRow := response.Responses[0].GetResponseRange().Kvs[0], response.Responses[1].GetResponseRange().Kvs[0]
	var plan v3Record
	var dispatch savedplaneffect.DispatchBinding
	if decodeV3Record(planRow.Value, &plan) != nil || json.Unmarshal(dispatchBindingRow.Value, &dispatch) != nil || dispatch.Validate() != nil {
		return savedplaneffect.SignedPreparationProjection{}, savedplaneffect.ErrInvalid
	}
	converge, digest, cluster := parseFleetConvergePlanPayload(plan.Operation.Payload)
	targetID, empty, _, targetErr := s.ResolveFleetTargetForPlan(ctx, cluster, "staging/nyc3")
	if plan.Operation.Kind != "fleet.capacity-plan" || converge == nil || converge.Resource == "" || targetErr != nil || empty || targetID == "" || dispatch.PlanID != planID || strings.TrimPrefix(digest, "sha256:") != dispatch.PlanSHA256 || converge.CommitSHA != dispatch.ProtectedCommit {
		return savedplaneffect.SignedPreparationProjection{}, savedplaneffect.ErrInvalid
	}
	projection, err := savedplaneffect.NewPreparationProjection(planID, dispatch.PlanSHA256, dispatch.ProtectedCommit, dispatch, now.UTC())
	if err != nil {
		return savedplaneffect.SignedPreparationProjection{}, err
	}
	signed, err := s.projectionSigner.SignPreparation(projection)
	if err != nil || signed.Validate() != nil {
		return savedplaneffect.SignedPreparationProjection{}, savedplaneffect.ErrInvalid
	}
	raw, err := json.Marshal(signed)
	if err != nil {
		return savedplaneffect.SignedPreparationProjection{}, err
	}
	result, err := s.kv.Txn(ctx).If(
		clientv3.Compare(clientv3.ModRevision(planKey), "=", planRow.ModRevision),
		clientv3.Compare(clientv3.ModRevision(dispatchBindingKey), "=", dispatchBindingRow.ModRevision),
		clientv3.Compare(clientv3.CreateRevision(projectionKey), "=", 0),
	).Then(clientv3.OpPut(projectionKey, string(raw))).Commit()
	if err != nil {
		return savedplaneffect.SignedPreparationProjection{}, err
	}
	if !result.Succeeded {
		return savedplaneffect.SignedPreparationProjection{}, ErrSavedPlanEffectConflict
	}
	return signed, nil
}

// GetSavedPlanEffectProjection is read-only reconciliation. It intentionally
// returns expired bytes too: expiration holds execution but never causes a
// re-sign or replacement of a durable projection.
func (s *V3OperationStore) GetSavedPlanEffectPreparation(ctx context.Context, planID, principal string) (savedplaneffect.SignedPreparationProjection, error) {
	if s == nil || uuid.Validate(planID) != nil || principal != "fleet-saved-plan-effect-verifier:work-staging" {
		return savedplaneffect.SignedPreparationProjection{}, savedplaneffect.ErrInvalid
	}
	response, err := s.kv.Get(ctx, s.savedPlanEffectProjectionKey(planID))
	if err != nil {
		return savedplaneffect.SignedPreparationProjection{}, err
	}
	if len(response.Kvs) != 1 {
		return savedplaneffect.SignedPreparationProjection{}, ErrNotFound
	}
	var signed savedplaneffect.SignedPreparationProjection
	if json.Unmarshal(response.Kvs[0].Value, &signed) != nil || signed.Validate() != nil {
		return savedplaneffect.SignedPreparationProjection{}, savedplaneffect.ErrInvalid
	}
	return signed, nil
}

// ListSavedPlanEffectProjectionCandidates gives the verifier a bounded,
// deterministic non-authorizing discovery view. It never accepts a plan ID
// from a workflow or operator and omits plans once their projection exists.
func (s *V3OperationStore) ListSavedPlanEffectProjectionCandidates(ctx context.Context, principal string) ([]SavedPlanEffectProjectionCandidate, error) {
	if s == nil || principal != "fleet-saved-plan-effect-verifier:work-staging" {
		return nil, savedplaneffect.ErrInvalid
	}
	plans, err := s.ListOperationsByKind(ctx, "fleet.capacity-plan", 100)
	if err != nil {
		return nil, err
	}
	result := make([]SavedPlanEffectProjectionCandidate, 0, len(plans))
	for _, plan := range plans {
		converge, digest, cluster := parseFleetConvergePlanPayload(plan.Payload)
		if plan.Status != "succeeded" || converge == nil || converge.Resource == "" || converge.Repository != projectionFleetRepository || strings.TrimPrefix(digest, "sha256:") == "" {
			continue
		}
		targetID, empty, _, targetErr := s.ResolveFleetTargetForPlan(ctx, cluster, "staging/nyc3")
		if targetErr != nil || empty || targetID == "" {
			continue
		}
		dispatch, dispatchErr := s.GetFleetRunnerDispatchBinding(ctx, plan.ID)
		prepared, prepErr := s.GetFleetGitHubDispatchPreparation(ctx, plan.ID)
		if dispatchErr != nil || prepErr != nil || s.VerifyFleetGitHubDispatchCompletion(ctx, dispatch) != nil || s.VerifyFleetGitHubDispatchReservation(ctx, prepared) != nil {
			continue
		}
		if dispatch.PlanID != plan.ID || dispatch.PlanSHA256 != strings.TrimPrefix(digest, "sha256:") || dispatch.ApprovedHeadSHA != converge.CommitSHA || prepared.PlanID != plan.ID || prepared.PlanRunID < 1 || prepared.PlanSHA256 != dispatch.PlanSHA256 || prepared.ApprovedHeadSHA != dispatch.ApprovedHeadSHA || prepared.DispatchNonceSHA256 != dispatch.DispatchNonceSHA256 {
			continue
		}
		if existing, readErr := s.kv.Get(ctx, s.savedPlanEffectProjectionKey(plan.ID)); readErr != nil {
			return nil, readErr
		} else if len(existing.Kvs) != 0 {
			continue
		}
		result = append(result, SavedPlanEffectProjectionCandidate{PlanID: plan.ID, PlanSHA256: dispatch.PlanSHA256, ProtectedCommit: dispatch.ApprovedHeadSHA, GitHubRunID: dispatch.RunID, PlanRunID: prepared.PlanRunID, PlanArtifactSHA256: prepared.PlanSHA256, DispatchNonceSHA256: dispatch.DispatchNonceSHA256})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].PlanID < result[j].PlanID })
	return result, nil
}

const projectionFleetRepository = "antiartificial/norn-fleet"

// RecordSavedPlanEffectPlanBinding makes the pre-job saved-plan facts
// immutable against the already accepted capacity-plan identity.
func (s *V3OperationStore) RecordSavedPlanEffectPlanBinding(ctx context.Context, binding savedplaneffect.PlanBinding, principal string) error {
	if s == nil || binding.Validate() != nil || principal != "fleet-saved-plan-effect-verifier:work-staging" {
		return savedplaneffect.ErrInvalid
	}
	planKey, preparationKey, bindingKey := s.opKey(binding.PlanID), s.savedPlanEffectProjectionKey(binding.PlanID), s.savedPlanEffectPlanBindingKey(binding.PlanID)
	response, err := s.kv.Txn(ctx).Then(clientv3.OpGet(planKey), clientv3.OpGet(preparationKey), clientv3.OpGet(bindingKey)).Commit()
	if err != nil {
		return err
	}
	if len(response.Responses) != 3 || len(response.Responses[0].GetResponseRange().Kvs) != 1 || len(response.Responses[1].GetResponseRange().Kvs) != 1 || len(response.Responses[2].GetResponseRange().Kvs) != 0 {
		return savedplaneffect.ErrInvalid
	}
	planRow, preparationRow := response.Responses[0].GetResponseRange().Kvs[0], response.Responses[1].GetResponseRange().Kvs[0]
	var plan v3Record
	var preparation savedplaneffect.SignedPreparationProjection
	if decodeV3Record(planRow.Value, &plan) != nil || json.Unmarshal(preparationRow.Value, &preparation) != nil || preparation.Validate() != nil || preparation.ProjectionSHA256 != binding.PreparationProjectionSHA256 {
		return savedplaneffect.ErrInvalid
	}
	converge, digest, cluster := parseFleetConvergePlanPayload(plan.Operation.Payload)
	targetID, empty, _, targetErr := s.ResolveFleetTargetForPlan(ctx, cluster, "staging/nyc3")
	if plan.Operation.Kind != "fleet.capacity-plan" || converge == nil || converge.Resource == "" || targetErr != nil || empty || targetID == "" || strings.TrimPrefix(digest, "sha256:") != binding.PlanSHA256 || converge.CommitSHA != binding.ProtectedCommit {
		return savedplaneffect.ErrInvalid
	}
	raw, err := json.Marshal(binding)
	if err != nil {
		return err
	}
	result, err := s.kv.Txn(ctx).If(clientv3.Compare(clientv3.ModRevision(planKey), "=", planRow.ModRevision), clientv3.Compare(clientv3.ModRevision(preparationKey), "=", preparationRow.ModRevision), clientv3.Compare(clientv3.CreateRevision(bindingKey), "=", 0)).Then(clientv3.OpPut(bindingKey, string(raw))).Commit()
	if err != nil {
		return err
	}
	if !result.Succeeded {
		return ErrSavedPlanEffectConflict
	}
	return nil
}

func (s *V3OperationStore) GetSavedPlanEffectPlanBinding(ctx context.Context, planID string) (savedplaneffect.PlanBinding, error) {
	if s == nil || uuid.Validate(planID) != nil {
		return savedplaneffect.PlanBinding{}, savedplaneffect.ErrInvalid
	}
	response, err := s.kv.Get(ctx, s.savedPlanEffectPlanBindingKey(planID))
	if err != nil {
		return savedplaneffect.PlanBinding{}, err
	}
	if len(response.Kvs) != 1 {
		return savedplaneffect.PlanBinding{}, ErrNotFound
	}
	var binding savedplaneffect.PlanBinding
	if json.Unmarshal(response.Kvs[0].Value, &binding) != nil || binding.Validate() != nil || binding.PlanID != planID {
		return savedplaneffect.PlanBinding{}, savedplaneffect.ErrInvalid
	}
	return binding, nil
}

// RecordSavedPlanEffectDispatchBinding persists the host-verifier's exact
// post-dispatch GitHub identity. The verifier is a protected host service;
// the one-use workflow job never receives this credential.
func (s *V3OperationStore) RecordSavedPlanEffectDispatchBinding(ctx context.Context, binding savedplaneffect.DispatchBinding, principal string) error {
	if s == nil || binding.Validate() != nil || principal != "fleet-saved-plan-effect-verifier:work-staging" {
		return savedplaneffect.ErrInvalid
	}
	planKey, dispatchKey, preparationKey, bindingKey := s.opKey(binding.PlanID), s.fleetRunnerDispatchKey(binding.PlanID), s.fleetGitHubDispatchPreparationKey(binding.PlanID), s.savedPlanEffectDispatchBindingKey(binding.PlanID)
	response, err := s.kv.Txn(ctx).Then(clientv3.OpGet(planKey), clientv3.OpGet(dispatchKey), clientv3.OpGet(preparationKey), clientv3.OpGet(bindingKey)).Commit()
	if err != nil {
		return err
	}
	if len(response.Responses) != 4 || len(response.Responses[0].GetResponseRange().Kvs) != 1 || len(response.Responses[1].GetResponseRange().Kvs) != 1 || len(response.Responses[2].GetResponseRange().Kvs) != 1 || len(response.Responses[3].GetResponseRange().Kvs) != 0 {
		return savedplaneffect.ErrInvalid
	}
	planRow, dispatchRow, preparationRow := response.Responses[0].GetResponseRange().Kvs[0], response.Responses[1].GetResponseRange().Kvs[0], response.Responses[2].GetResponseRange().Kvs[0]
	var plan v3Record
	var dispatch v3FleetRunnerDispatch
	var prepared v3FleetGitHubDispatchPreparation
	if decodeV3Record(planRow.Value, &plan) != nil || decodeV3Record(dispatchRow.Value, &dispatch) != nil || decodeV3Record(preparationRow.Value, &prepared) != nil {
		return savedplaneffect.ErrInvalid
	}
	_, digest, _ := parseFleetConvergePlanPayload(plan.Operation.Payload)
	if plan.Operation.Kind != "fleet.capacity-plan" || strings.TrimPrefix(digest, "sha256:") != binding.PlanSHA256 || dispatch.PlanID != binding.PlanID || dispatch.PlanSHA256 != binding.PlanSHA256 || dispatch.ApprovedHeadSHA != binding.ProtectedCommit || dispatch.RunID != binding.GitHubRunID || dispatch.DispatchNonceSHA256 != binding.DispatchNonceSHA256 || prepared.PlanID != binding.PlanID || prepared.PlanRunID != binding.PlanRunID || prepared.PlanSHA256 != binding.PlanSHA256 || prepared.ApprovedHeadSHA != binding.ProtectedCommit || prepared.DispatchNonceSHA256 != binding.DispatchNonceSHA256 {
		return savedplaneffect.ErrInvalid
	}
	raw, err := json.Marshal(binding)
	if err != nil {
		return err
	}
	result, err := s.kv.Txn(ctx).If(clientv3.Compare(clientv3.ModRevision(planKey), "=", planRow.ModRevision), clientv3.Compare(clientv3.ModRevision(dispatchKey), "=", dispatchRow.ModRevision), clientv3.Compare(clientv3.ModRevision(preparationKey), "=", preparationRow.ModRevision), clientv3.Compare(clientv3.CreateRevision(bindingKey), "=", 0)).Then(clientv3.OpPut(bindingKey, string(raw))).Commit()
	if err != nil {
		return err
	}
	if !result.Succeeded {
		return ErrSavedPlanEffectConflict
	}
	return nil
}

func (s *V3OperationStore) GetSavedPlanEffectDispatchBinding(ctx context.Context, planID string) (savedplaneffect.DispatchBinding, error) {
	if s == nil || uuid.Validate(planID) != nil {
		return savedplaneffect.DispatchBinding{}, savedplaneffect.ErrInvalid
	}
	response, err := s.kv.Get(ctx, s.savedPlanEffectDispatchBindingKey(planID))
	if err != nil {
		return savedplaneffect.DispatchBinding{}, err
	}
	if len(response.Kvs) != 1 {
		return savedplaneffect.DispatchBinding{}, ErrNotFound
	}
	var binding savedplaneffect.DispatchBinding
	if json.Unmarshal(response.Kvs[0].Value, &binding) != nil || binding.Validate() != nil || binding.PlanID != planID {
		return savedplaneffect.DispatchBinding{}, savedplaneffect.ErrInvalid
	}
	return binding, nil
}

// savedPlanEffectLiveComparisons is the Norn-owned freshness check shared by
// apply consumption and refresh-only continuation. It verifies the immutable
// plan projection plus the currently held attempt/fence/epoch and returns the
// compares that make a subsequent write fail if any one changed.
func (s *V3OperationStore) savedPlanEffectLiveComparisons(ctx context.Context, claim savedplaneffect.Claim, now time.Time) ([]clientv3.Cmp, error) {
	planKey, attemptKey, fenceKey, epochKey := s.opKey(claim.PlanID), s.fleetRunnerAttemptKey(claim.PlanID, claim.AttemptID), s.fleetTargetFenceKey(claim.TargetID), s.fleetAuthorityEpochKey()
	dispatchKey, preparationKey, planBindingKey, effectDispatchKey := s.fleetRunnerDispatchKey(claim.PlanID), s.fleetGitHubDispatchPreparationKey(claim.PlanID), s.savedPlanEffectPlanBindingKey(claim.PlanID), s.savedPlanEffectDispatchBindingKey(claim.PlanID)
	response, err := s.kv.Txn(ctx).Then(clientv3.OpGet(planKey), clientv3.OpGet(attemptKey), clientv3.OpGet(fenceKey), clientv3.OpGet(epochKey), clientv3.OpGet(dispatchKey), clientv3.OpGet(preparationKey), clientv3.OpGet(planBindingKey), clientv3.OpGet(effectDispatchKey)).Commit()
	if err != nil {
		return nil, err
	}
	if len(response.Responses) != 8 || len(response.Responses[0].GetResponseRange().Kvs) != 1 || len(response.Responses[1].GetResponseRange().Kvs) != 1 || len(response.Responses[2].GetResponseRange().Kvs) != 1 || len(response.Responses[3].GetResponseRange().Kvs) != 1 || len(response.Responses[4].GetResponseRange().Kvs) != 1 || len(response.Responses[5].GetResponseRange().Kvs) != 1 || len(response.Responses[6].GetResponseRange().Kvs) != 1 || len(response.Responses[7].GetResponseRange().Kvs) != 1 {
		return nil, savedplaneffect.ErrInvalid
	}
	planRow, attemptRow, fenceRow, epochRow, dispatchRow, preparationRow, planBindingRow, effectDispatchRow := response.Responses[0].GetResponseRange().Kvs[0], response.Responses[1].GetResponseRange().Kvs[0], response.Responses[2].GetResponseRange().Kvs[0], response.Responses[3].GetResponseRange().Kvs[0], response.Responses[4].GetResponseRange().Kvs[0], response.Responses[5].GetResponseRange().Kvs[0], response.Responses[6].GetResponseRange().Kvs[0], response.Responses[7].GetResponseRange().Kvs[0]
	var plan v3Record
	var attempt fleet.RunnerAttempt
	var fence v3FleetTargetFence
	var dispatch v3FleetRunnerDispatch
	var prepared v3FleetGitHubDispatchPreparation
	var planBinding savedplaneffect.PlanBinding
	var effectDispatch savedplaneffect.DispatchBinding
	epoch, epochErr := strconv.ParseInt(strings.TrimSpace(string(epochRow.Value)), 10, 64)
	if epochErr != nil || decodeV3Record(planRow.Value, &plan) != nil || decodeV3Record(attemptRow.Value, &attempt) != nil || decodeV3Record(fenceRow.Value, &fence) != nil || decodeV3Record(dispatchRow.Value, &dispatch) != nil || decodeV3Record(preparationRow.Value, &prepared) != nil || json.Unmarshal(planBindingRow.Value, &planBinding) != nil || planBinding.Validate() != nil || json.Unmarshal(effectDispatchRow.Value, &effectDispatch) != nil || effectDispatch.Validate() != nil {
		return nil, savedplaneffect.ErrInvalid
	}
	converge, digest, cluster := parseFleetConvergePlanPayload(plan.Operation.Payload)
	targetID, empty, _, targetErr := s.ResolveFleetTargetForPlan(ctx, cluster, "")
	if !claim.MatchesPlanBinding(planBinding) || !claim.MatchesDispatchBinding(effectDispatch) || plan.Operation.Kind != "fleet.capacity-plan" || converge == nil || strings.TrimPrefix(digest, "sha256:") != claim.PlanSHA256 || converge.CommitSHA != claim.ProtectedCommit ||
		dispatch.PlanSHA256 != claim.PlanSHA256 || dispatch.ApprovedHeadSHA != claim.ProtectedCommit || dispatch.DispatchNonceSHA256 != claim.DispatchNonceSHA256 || dispatch.RunID != claim.GitHubRunID || dispatch.WorkflowURL != attempt.WorkflowURL ||
		prepared.PlanID != claim.PlanID || prepared.PlanRunID != claim.PlanRunID || prepared.PlanSHA256 != claim.PlanSHA256 || prepared.ApprovedHeadSHA != claim.ProtectedCommit || prepared.DispatchNonceSHA256 != claim.DispatchNonceSHA256 ||
		attempt.ID != claim.AttemptID || attempt.PlanID != claim.PlanID || attempt.CommitSHA != claim.ProtectedCommit || attempt.PlanSHA256 != claim.PlanSHA256 || attempt.Status != "running" || attempt.CurrentPhase != "provider_applying" || !attempt.HeartbeatExpiresAt.After(now) ||
		targetErr != nil || empty || targetID != claim.TargetID || !fence.Held || fence.HolderPlanID != claim.PlanID || fence.HolderNonceSHA256 != executor.HolderNonceSHA256(claim.PlanID) || fence.Generation != claim.TargetFenceGeneration || fence.AuthorityEpoch != claim.AuthorityEpoch || epoch != claim.AuthorityEpoch {
		return nil, savedplaneffect.ErrInvalid
	}
	return []clientv3.Cmp{clientv3.Compare(clientv3.ModRevision(planKey), "=", planRow.ModRevision), clientv3.Compare(clientv3.ModRevision(attemptKey), "=", attemptRow.ModRevision), clientv3.Compare(clientv3.ModRevision(fenceKey), "=", fenceRow.ModRevision), clientv3.Compare(clientv3.ModRevision(epochKey), "=", epochRow.ModRevision), clientv3.Compare(clientv3.ModRevision(dispatchKey), "=", dispatchRow.ModRevision), clientv3.Compare(clientv3.ModRevision(preparationKey), "=", preparationRow.ModRevision), clientv3.Compare(clientv3.ModRevision(planBindingKey), "=", planBindingRow.ModRevision), clientv3.Compare(clientv3.ModRevision(effectDispatchKey), "=", effectDispatchRow.ModRevision)}, nil
}

// SavedPlanEffectRecovery is deliberately non-authorizing, including after an
// apply response was lost. The executor must obtain provider absence evidence
// through the dedicated verifier before it can continue read-only.
type SavedPlanEffectRecovery struct {
	Claim           savedplaneffect.Claim                     `json:"claim"`
	ClaimSHA256     string                                    `json:"claimSha256"`
	ApplyConsumed   bool                                      `json:"applyConsumed"`
	RefreshOnly     *savedplaneffect.RefreshOnlyAuthorization `json:"refreshOnly,omitempty"`
	AuthorizesApply bool                                      `json:"authorizesApply"`
}

// SavedPlanEffectClaimRead is a verifier-only reconciliation view. It never
// authorizes apply and is safe after an uncertain claim POST.
type SavedPlanEffectClaimRead struct {
	Claim           savedplaneffect.Claim `json:"claim"`
	ClaimSHA256     string                `json:"claimSha256"`
	ApplyConsumed   bool                  `json:"applyConsumed"`
	AuthorizesApply bool                  `json:"authorizesApply"`
}

func (s *V3OperationStore) GetSavedPlanEffectClaim(ctx context.Context, planID, attemptID, principal string) (SavedPlanEffectClaimRead, error) {
	record, _, err := s.readSavedPlanEffect(ctx, planID)
	if err != nil {
		return SavedPlanEffectClaimRead{}, err
	}
	if principal != "fleet-saved-plan-effect-verifier:work-staging" || record.Claim.AttemptID != attemptID {
		return SavedPlanEffectClaimRead{}, savedplaneffect.ErrInvalid
	}
	digest, err := record.Claim.Digest()
	if err != nil {
		return SavedPlanEffectClaimRead{}, savedplaneffect.ErrInvalid
	}
	return SavedPlanEffectClaimRead{Claim: record.Claim, ClaimSHA256: digest, ApplyConsumed: record.ApplyGrant != nil, AuthorizesApply: false}, nil
}

func (s *V3OperationStore) RecordSavedPlanEffectClaim(ctx context.Context, claim savedplaneffect.Claim, principal string, now time.Time) error {
	if s == nil || claim.Validate() != nil || principal != "fleet-saved-plan-effect-verifier:work-staging" || now.Before(claim.CreatedAt) || !now.Before(claim.ExpiresAt) {
		return savedplaneffect.ErrInvalid
	}
	compares, err := s.savedPlanEffectLiveComparisons(ctx, claim, now)
	if err != nil {
		return err
	}
	recordKey := s.savedPlanEffectKey(claim.PlanID)
	raw, err := json.Marshal(savedPlanEffectRecord{Claim: claim})
	if err != nil {
		return err
	}
	compares = append(compares, clientv3.Compare(clientv3.CreateRevision(recordKey), "=", 0))
	result, err := s.kv.Txn(ctx).If(compares...).Then(clientv3.OpPut(recordKey, string(raw))).Commit()
	if err != nil {
		return err
	}
	if !result.Succeeded {
		return ErrSavedPlanEffectConflict
	}
	return nil
}

func (s *V3OperationStore) readSavedPlanEffect(ctx context.Context, planID string) (savedPlanEffectRecord, int64, error) {
	if s == nil || uuid.Validate(planID) != nil {
		return savedPlanEffectRecord{}, 0, savedplaneffect.ErrInvalid
	}
	response, err := s.kv.Get(ctx, s.savedPlanEffectKey(planID))
	if err != nil {
		return savedPlanEffectRecord{}, 0, err
	}
	if len(response.Kvs) != 1 {
		return savedPlanEffectRecord{}, 0, ErrNotFound
	}
	var record savedPlanEffectRecord
	if json.Unmarshal(response.Kvs[0].Value, &record) != nil || record.validate() != nil {
		return savedPlanEffectRecord{}, 0, savedplaneffect.ErrInvalid
	}
	return record, response.Kvs[0].ModRevision, nil
}

func (s *V3OperationStore) ConsumeSavedPlanEffectApply(ctx context.Context, planID, attemptID, principal string, now time.Time) (savedplaneffect.ApplyGrant, error) {
	record, revision, err := s.readSavedPlanEffect(ctx, planID)
	if err != nil {
		return savedplaneffect.ApplyGrant{}, err
	}
	if principal != "fleet-attempt:"+attemptID || record.Claim.AttemptID != attemptID || record.ApplyGrant != nil || !now.Before(record.Claim.ExpiresAt) {
		return savedplaneffect.ApplyGrant{}, savedplaneffect.ErrConsumed
	}
	// Re-read the live Norn-owned attempt and fence in the consuming CAS. The
	// verifier claim is a short-lived observation, not a lease that survives a
	// stopped attempt, a changed writer holder, or a replaced target fence.
	planKey, attemptKey, fenceKey, epochKey := s.opKey(planID), s.fleetRunnerAttemptKey(planID, attemptID), s.fleetTargetFenceKey(record.Claim.TargetID), s.fleetAuthorityEpochKey()
	state, err := s.kv.Txn(ctx).Then(clientv3.OpGet(planKey), clientv3.OpGet(attemptKey), clientv3.OpGet(fenceKey), clientv3.OpGet(epochKey)).Commit()
	if err != nil {
		return savedplaneffect.ApplyGrant{}, err
	}
	if len(state.Responses) != 4 || len(state.Responses[0].GetResponseRange().Kvs) != 1 || len(state.Responses[1].GetResponseRange().Kvs) != 1 || len(state.Responses[2].GetResponseRange().Kvs) != 1 || len(state.Responses[3].GetResponseRange().Kvs) != 1 {
		return savedplaneffect.ApplyGrant{}, savedplaneffect.ErrConsumed
	}
	planRow, attemptRow, fenceRow, epochRow := state.Responses[0].GetResponseRange().Kvs[0], state.Responses[1].GetResponseRange().Kvs[0], state.Responses[2].GetResponseRange().Kvs[0], state.Responses[3].GetResponseRange().Kvs[0]
	var plan v3Record
	var attempt fleet.RunnerAttempt
	var fence v3FleetTargetFence
	epoch, epochErr := strconv.ParseInt(strings.TrimSpace(string(epochRow.Value)), 10, 64)
	if epochErr != nil || decodeV3Record(planRow.Value, &plan) != nil || decodeV3Record(attemptRow.Value, &attempt) != nil || decodeV3Record(fenceRow.Value, &fence) != nil {
		return savedplaneffect.ApplyGrant{}, savedplaneffect.ErrConsumed
	}
	converge, planDigest, _ := parseFleetConvergePlanPayload(plan.Operation.Payload)
	if plan.Operation.Kind != "fleet.capacity-plan" || converge == nil || converge.CommitSHA != record.Claim.ProtectedCommit || strings.TrimPrefix(planDigest, "sha256:") != record.Claim.PlanSHA256 ||
		attempt.ID != attemptID || attempt.PlanID != planID || attempt.CommitSHA != record.Claim.ProtectedCommit || attempt.PlanSHA256 != record.Claim.PlanSHA256 || attempt.Status != "running" || attempt.CurrentPhase != "provider_applying" || !attempt.HeartbeatExpiresAt.After(now) ||
		!fence.Held || fence.HolderPlanID != planID || fence.HolderNonceSHA256 != executor.HolderNonceSHA256(planID) || fence.Generation != record.Claim.TargetFenceGeneration ||
		fence.AuthorityEpoch != record.Claim.AuthorityEpoch || epoch != record.Claim.AuthorityEpoch {
		return savedplaneffect.ApplyGrant{}, savedplaneffect.ErrConsumed
	}
	// Bind the single-use grant to the exact signed plan projection and the
	// protected dispatch/preparation rows as well as the local stale-state
	// checks above. A changed projection, workflow binding or fence loses the
	// consuming CAS rather than minting an apply capability.
	liveCompares, err := s.savedPlanEffectLiveComparisons(ctx, record.Claim, now)
	if err != nil {
		return savedplaneffect.ApplyGrant{}, savedplaneffect.ErrConsumed
	}
	digest, err := record.Claim.Digest()
	if err != nil {
		return savedplaneffect.ApplyGrant{}, err
	}
	expires := now.Add(savedplaneffect.MaxApplyGrantLifetime)
	if record.Claim.ExpiresAt.Before(expires) {
		expires = record.Claim.ExpiresAt
	}
	grant := savedplaneffect.ApplyGrant{SchemaVersion: savedplaneffect.GrantSchema, ID: uuid.NewString(), ClaimID: record.Claim.ID, ClaimSHA256: digest, PlanID: planID, AttemptID: attemptID, Command: savedplaneffect.ApplyCommand, AuthorizesApply: true, ConsumedAt: now.UTC(), ExpiresAt: expires.UTC()}
	if grant.Validate(record.Claim) != nil {
		return savedplaneffect.ApplyGrant{}, savedplaneffect.ErrInvalid
	}
	record.ApplyGrant = &grant
	raw, err := json.Marshal(record)
	if err != nil {
		return savedplaneffect.ApplyGrant{}, err
	}
	compares := append([]clientv3.Cmp{clientv3.Compare(clientv3.ModRevision(s.savedPlanEffectKey(planID)), "=", revision),
		clientv3.Compare(clientv3.ModRevision(planKey), "=", planRow.ModRevision), clientv3.Compare(clientv3.ModRevision(attemptKey), "=", attemptRow.ModRevision), clientv3.Compare(clientv3.ModRevision(fenceKey), "=", fenceRow.ModRevision),
		clientv3.Compare(clientv3.ModRevision(epochKey), "=", epochRow.ModRevision)}, liveCompares...)
	result, err := s.kv.Txn(ctx).If(compares...).Then(clientv3.OpPut(s.savedPlanEffectKey(planID), string(raw))).Commit()
	if err != nil {
		return savedplaneffect.ApplyGrant{}, err
	}
	if !result.Succeeded {
		return savedplaneffect.ApplyGrant{}, ErrSavedPlanEffectConflict
	}
	return grant, nil
}

func (s *V3OperationStore) GetSavedPlanEffectRecovery(ctx context.Context, planID, attemptID, principal string) (SavedPlanEffectRecovery, error) {
	record, _, err := s.readSavedPlanEffect(ctx, planID)
	if err != nil {
		return SavedPlanEffectRecovery{}, err
	}
	if principal != "fleet-attempt:"+attemptID || record.Claim.AttemptID != attemptID {
		return SavedPlanEffectRecovery{}, savedplaneffect.ErrInvalid
	}
	digest, err := record.Claim.Digest()
	if err != nil {
		return SavedPlanEffectRecovery{}, savedplaneffect.ErrInvalid
	}
	return SavedPlanEffectRecovery{Claim: record.Claim, ClaimSHA256: digest, ApplyConsumed: record.ApplyGrant != nil, RefreshOnly: record.RefreshOnly, AuthorizesApply: false}, nil
}

func (s *V3OperationStore) AuthorizeSavedPlanRefreshOnly(ctx context.Context, planID, attemptID, principal, providerAbsenceSHA256 string, now time.Time) (savedplaneffect.RefreshOnlyAuthorization, error) {
	record, revision, err := s.readSavedPlanEffect(ctx, planID)
	if err != nil {
		return savedplaneffect.RefreshOnlyAuthorization{}, err
	}
	if principal != "fleet-saved-plan-effect-verifier:work-staging" || record.Claim.AttemptID != attemptID || record.ApplyGrant == nil || record.RefreshOnly != nil || !now.Before(record.Claim.ExpiresAt) || !now.Before(record.ApplyGrant.ExpiresAt) {
		return savedplaneffect.RefreshOnlyAuthorization{}, savedplaneffect.ErrInvalid
	}
	// A refresh-only continuation is read-only, but it is still a fresh
	// verifier decision over a live attempt, writer fence and epoch. It cannot
	// be issued from a stale consumed record or re-open apply authority.
	liveCompares, err := s.savedPlanEffectLiveComparisons(ctx, record.Claim, now)
	if err != nil {
		return savedplaneffect.RefreshOnlyAuthorization{}, savedplaneffect.ErrInvalid
	}
	refresh := savedplaneffect.RefreshOnlyAuthorization{SchemaVersion: savedplaneffect.RefreshOnlySchema, ID: uuid.NewString(), ClaimID: record.Claim.ID, PlanID: planID, AttemptID: attemptID, ProviderAbsenceSHA256: providerAbsenceSHA256, Command: savedplaneffect.RefreshOnlyCommand, AuthorizedAt: now.UTC()}
	if refresh.Validate(record.Claim) != nil {
		return savedplaneffect.RefreshOnlyAuthorization{}, savedplaneffect.ErrInvalid
	}
	record.RefreshOnly = &refresh
	raw, err := json.Marshal(record)
	if err != nil {
		return savedplaneffect.RefreshOnlyAuthorization{}, err
	}
	compares := append([]clientv3.Cmp{clientv3.Compare(clientv3.ModRevision(s.savedPlanEffectKey(planID)), "=", revision)}, liveCompares...)
	result, err := s.kv.Txn(ctx).If(compares...).Then(clientv3.OpPut(s.savedPlanEffectKey(planID), string(raw))).Commit()
	if err != nil {
		return savedplaneffect.RefreshOnlyAuthorization{}, err
	}
	if !result.Succeeded {
		return savedplaneffect.RefreshOnlyAuthorization{}, ErrSavedPlanEffectConflict
	}
	return refresh, nil
}
