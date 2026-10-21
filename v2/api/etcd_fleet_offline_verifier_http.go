package main

// A read-only view for the independently credentialed work-staging verifier.
// This endpoint never invokes any effect transition or returns runner identity.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"norn/v2/api/config"
	"norn/v2/api/etcdstore"
	"norn/v2/api/fleet/lifecycle"
	"norn/v2/api/fleet/offline"
	"norn/v2/api/handler"
)

const offlineVerifierSubject = "fleet-offline-verifier:work-staging"
const offlineVerifierAudience = "work-staging"

type offlineVerifierStateStore interface {
	GetOfflineDispatchReservation(context.Context, string) (offline.Binding, error)
	GetOfflineAttempt(context.Context, string) (offline.Attempt, error)
	GetOfflineEffectGrantV2(context.Context, string) (offline.EffectGrantV2, error)
	FleetAuthorityEpoch(context.Context) (int64, error)
	GetFleetTargetFence(context.Context, string) (lifecycle.FenceFacts, int64, error)
}

type offlineVerifierBinding struct {
	SchemaVersion       string                 `json:"schemaVersion"`
	PlanID              string                 `json:"planId"`
	TargetID            string                 `json:"targetId"`
	Resource            string                 `json:"resource"`
	SourceCommit        string                 `json:"sourceCommit"`
	SavedPlanSHA256     string                 `json:"savedPlanSHA256"`
	EffectSHA256        string                 `json:"effectSHA256"`
	CandidateSHA256     string                 `json:"candidateSHA256"`
	ProviderNodes       []offline.ProviderNode `json:"providerNodes"`
	Backend             offline.Backend        `json:"backend"`
	ProviderAccountUUID string                 `json:"providerAccountUUID"`
	NodeName            string                 `json:"nodeName"`
	DropletID           string                 `json:"dropletId"`
	FenceGeneration     uint64                 `json:"fenceGeneration"`
	CreatedAt           time.Time              `json:"createdAt"`
	ExpiresAt           time.Time              `json:"expiresAt"`
}

type offlineVerifierAttempt struct {
	ID              string `json:"id"`
	PlanID          string `json:"planId"`
	BindingSHA256   string `json:"bindingSHA256"`
	TargetID        string `json:"targetId"`
	FenceGeneration uint64 `json:"fenceGeneration"`
	Status          string `json:"status"`
	CurrentPhase    string `json:"currentPhase"`
}

type offlineVerifierGrant struct {
	ID                    string                      `json:"id"`
	AttemptID             string                      `json:"attemptId"`
	BindingSHA256         string                      `json:"bindingSHA256"`
	TargetID              string                      `json:"targetId"`
	TargetFenceGeneration uint64                      `json:"targetFenceGeneration"`
	AuthorityEpoch        int64                       `json:"authorityEpoch"`
	WriterFence           offline.WriterFence         `json:"writerFence"`
	Phase                 string                      `json:"phase"`
	ExpiresAt             time.Time                   `json:"expiresAt"`
	EffectEvidence        offline.EffectEvidenceV2    `json:"effectEvidence"`
	DeleteRequestSHA256   string                      `json:"deleteRequestSHA256"`
	Commitment            *offline.DeleteCommitmentV2 `json:"commitment,omitempty"`
	Dispatch              *offline.DeleteDispatchV2   `json:"dispatch,omitempty"`
	Terminal              *offline.TerminalEvidenceV2 `json:"terminal,omitempty"`
}

func registerEtcdFleetOfflineVerifierRoute(router chi.Router, cfg *config.Config, identities *etcdstore.AuthStore, state offlineVerifierStateStore) {
	if cfg == nil || !cfg.FleetOfflineVerifierReadEnabled {
		return
	}
	auth := etcdManagedTokenAuth(cfg, identities, handler.ScopeFleetOfflineVerify)
	router.With(offlineEffectNoStore, auth).Get("/api/v1/fleet/offline/verifier/plans/{planID}/attempts/{attemptID}", etcdOfflineVerifierRead(state))
}

func etcdOfflineVerifierRead(state offlineVerifierStateStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := handler.AccessPrincipalFromRequest(r)
		if !ok || p.Source != handler.AccessPrincipalSourceManagedToken || p.Legacy || p.CI != nil ||
			p.DeviceID != "" || p.Subject != offlineVerifierSubject || p.TokenID == "" ||
			len(p.Scopes) != 1 || p.Scopes[0] != handler.ScopeFleetOfflineVerify ||
			p.ExpiresAt.IsZero() || !time.Now().Before(p.ExpiresAt) {
			handler.WriteControlProblem(w, r, http.StatusForbidden, "offline_verifier_required", "a dedicated work-staging verifier token is required")
			return
		}
		planID, attemptID := chi.URLParam(r, "planID"), chi.URLParam(r, "attemptID")
		query := r.URL.Query()
		epoch, epochErr := strconv.ParseInt(query.Get("authorityEpoch"), 10, 64)
		if uuid.Validate(planID) != nil || uuid.Validate(attemptID) != nil || len(query["audience"]) != 1 || query.Get("audience") != offlineVerifierAudience ||
			len(query["authorityEpoch"]) != 1 || epochErr != nil || epoch <= 0 || len(query) != 2 {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "invalid_offline_verifier_request", "exact work-staging audience, authority epoch, plan and attempt are required")
			return
		}
		if state == nil {
			handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "offline_verifier_unavailable", "offline verifier state is unavailable")
			return
		}
		binding, err := state.GetOfflineDispatchReservation(r.Context(), planID)
		if errors.Is(err, etcdstore.ErrNotFound) {
			handler.WriteControlProblem(w, r, http.StatusNotFound, "offline_verifier_state_not_found", "offline verifier state not found")
			return
		}
		if err != nil || binding.Validate() != nil || binding.SchemaVersion != offline.BindingSchemaV2 || binding.PlanID != planID ||
			binding.Resource != offline.WorkResource || binding.NodeName != offline.WorkNode || binding.DropletID != offline.WorkDropletID {
			handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "offline_verifier_state_invalid", "offline verifier binding is unavailable or invalid")
			return
		}
		attempt, err := state.GetOfflineAttempt(r.Context(), planID)
		if errors.Is(err, etcdstore.ErrNotFound) {
			handler.WriteControlProblem(w, r, http.StatusNotFound, "offline_verifier_state_not_found", "offline verifier state not found")
			return
		}
		if err != nil || attempt.ID != attemptID || attempt.PlanID != planID || attempt.TargetID != binding.TargetID ||
			attempt.FenceGeneration != binding.FenceGeneration {
			handler.WriteControlProblem(w, r, http.StatusConflict, "offline_verifier_attempt_mismatch", "offline attempt is missing or differs from the binding")
			return
		}
		bindingBytes, err := json.Marshal(binding)
		bindingDigest := sha256.Sum256(bindingBytes)
		if err != nil || attempt.BindingSHA256 != hex.EncodeToString(bindingDigest[:]) {
			handler.WriteControlProblem(w, r, http.StatusConflict, "offline_verifier_attempt_mismatch", "offline attempt binding digest differs from the reservation")
			return
		}
		currentEpoch, err := state.FleetAuthorityEpoch(r.Context())
		if err != nil || currentEpoch != epoch {
			handler.WriteControlProblem(w, r, http.StatusConflict, "offline_verifier_epoch_mismatch", "offline authority epoch changed")
			return
		}
		fence, _, err := state.GetFleetTargetFence(r.Context(), binding.TargetID)
		if err != nil || fence.TargetID != binding.TargetID || fence.Generation != int64(binding.FenceGeneration) ||
			fence.AuthorityEpoch != epoch || (attempt.Status == "queued" &&
			(!fence.Held || fence.HolderPlanID != planID || fence.HolderNonceSHA256 != binding.DispatchNonceSHA256)) {
			handler.WriteControlProblem(w, r, http.StatusConflict, "offline_verifier_fence_mismatch", "offline target fence differs from the bound attempt")
			return
		}
		var grantView *offlineVerifierGrant
		grant, err := state.GetOfflineEffectGrantV2(r.Context(), planID)
		if err != nil && !errors.Is(err, etcdstore.ErrNotFound) {
			handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "offline_verifier_grant_invalid", "offline effect grant state is unavailable or invalid")
			return
		}
		if err == nil {
			if grant.Validate() != nil || grant.PlanID != planID || grant.AttemptID != attemptID || grant.BindingSHA256 != attempt.BindingSHA256 ||
				grant.TargetID != binding.TargetID || grant.TargetFenceGeneration != binding.FenceGeneration || grant.AuthorityEpoch != epoch {
				handler.WriteControlProblem(w, r, http.StatusConflict, "offline_verifier_grant_mismatch", "offline effect grant differs from the bound attempt or epoch")
				return
			}
			grantView = &offlineVerifierGrant{ID: grant.ID, AttemptID: grant.AttemptID, BindingSHA256: grant.BindingSHA256,
				TargetID: grant.TargetID, TargetFenceGeneration: grant.TargetFenceGeneration, AuthorityEpoch: grant.AuthorityEpoch,
				WriterFence: grant.WriterFence, Phase: grant.Phase, ExpiresAt: grant.ExpiresAt, EffectEvidence: grant.EffectEvidence,
				DeleteRequestSHA256: grant.DeleteRequestSHA256, Commitment: grant.Commitment,
				Dispatch: grant.Dispatch, Terminal: grant.Terminal}
		}
		// An expired reservation cannot serve a new effect proof. Historical
		// dispatch and terminal readback remains available to resolve uncertainty.
		if attempt.Status == "queued" && !time.Now().Before(binding.ExpiresAt) &&
			(grantView == nil || (grantView.Phase != "delete_dispatched" && grantView.Phase != "terminal_verified")) {
			handler.WriteControlProblem(w, r, http.StatusConflict, "offline_verifier_binding_expired", "offline reservation expired")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			SchemaVersion    string                 `json:"schemaVersion"`
			Audience         string                 `json:"audience"`
			BindingSHA256    string                 `json:"bindingSHA256"`
			AuthorityEpoch   int64                  `json:"authorityEpoch"`
			ObservedAt       time.Time              `json:"observedAt"`
			Binding          offlineVerifierBinding `json:"binding"`
			Attempt          offlineVerifierAttempt `json:"attempt"`
			Grant            *offlineVerifierGrant  `json:"grant,omitempty"`
			AuthorizesEffect bool                   `json:"authorizesEffect"`
		}{"norn.fleet-offline-verifier-state/v1", offlineVerifierAudience, hex.EncodeToString(bindingDigest[:]), epoch, time.Now().UTC(),
			offlineVerifierBinding{binding.SchemaVersion, binding.PlanID, binding.TargetID, binding.Resource, binding.SourceCommit,
				binding.SavedPlanSHA256, binding.EffectSHA256, binding.CandidateSHA256, binding.ProviderNodes, binding.Backend,
				binding.ProviderAccountUUID, binding.NodeName, binding.DropletID, binding.FenceGeneration, binding.CreatedAt, binding.ExpiresAt},
			offlineVerifierAttempt{attempt.ID, attempt.PlanID, attempt.BindingSHA256, attempt.TargetID, attempt.FenceGeneration, attempt.Status, attempt.CurrentPhase},
			grantView, false})
	}
}
