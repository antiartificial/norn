package handler

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"norn/v2/api/model"
	"norn/v2/api/pipeline"
	"norn/v2/api/store"
)

const releaseQualificationSchema = "norn.release-qualification/v2"
const releaseQualificationPayloadType = "application/vnd.norn.release-qualification.v2+json"

var fullSourceSHAPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

type releaseRequest struct {
	SourceSHA string                 `json:"sourceSha"`
	Artifact  string                 `json:"artifact,omitempty"`
	Candidate model.ReleaseCandidate `json:"candidate"`
}

type qualificationRequest struct {
	DeploymentID string `json:"deploymentId"`
}

type promotionRequest struct {
	SourceSHA     string                     `json:"sourceSha"`
	Artifact      string                     `json:"artifact"`
	Qualification model.ReleaseQualification `json:"qualification"`
}

type releaseRollbackRequest struct {
	DeploymentID string `json:"deploymentId"`
	SourceSHA    string `json:"sourceSha"`
	Artifact     string `json:"artifact"`
	Confirm      bool   `json:"confirm"`
}

func (h *Handler) QueueReleasePreflight(w http.ResponseWriter, r *http.Request) {
	h.queueRelease(w, r, true, nil)
}

func (h *Handler) QueueReleaseDeployment(w http.ResponseWriter, r *http.Request) {
	h.queueRelease(w, r, false, nil)
}

// QueueReleaseRollback restores an earlier, already admitted immutable
// deployment. It never rebuilds or accepts a mutable target selector.
func (h *Handler) QueueReleaseRollback(w http.ResponseWriter, r *http.Request) {
	appID := chi.URLParam(r, "id")
	principal, ok := requireReleaseControlScope(w, r, ScopeReleaseRollback, appID)
	if !ok {
		return
	}
	if h.cfg == nil || h.cfg.EnvironmentID() != "production" {
		WriteControlProblem(w, r, http.StatusConflict, "rollback_environment_invalid", "release rollback is production-only")
		return
	}
	var request releaseRollbackRequest
	if err := decodeControlJSON(w, r, &request); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_release_rollback", err.Error())
		return
	}
	if !request.Confirm || !validReleaseProvenance(request.SourceSHA, request.Artifact) {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_release_rollback", "confirm and immutable provenance are required")
		return
	}
	if _, err := uuid.Parse(strings.TrimSpace(request.DeploymentID)); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_release_rollback", "deploymentId must be a UUID")
		return
	}
	spec := h.findSpec(appID)
	if spec == nil {
		WriteControlProblem(w, r, http.StatusNotFound, "app_not_found", "app was not found or deployment is disabled")
		return
	}
	target, err := h.db.GetDeployment(r.Context(), request.DeploymentID)
	if err != nil || target.App != appID || target.Environment != "production" || target.Status != model.StatusDeployed || target.CommitSHA != request.SourceSHA || target.ImageTag != request.Artifact {
		WriteControlProblem(w, r, http.StatusNotFound, "rollback_target_missing", "exact previously admitted successful production deployment was not found")
		return
	}
	promotion, err := h.db.GetPromotionOperationByDeploymentID(r.Context(), target.ID)
	if err != nil {
		WriteControlProblem(w, r, http.StatusConflict, "rollback_target_unadmitted", "rollback target has no durable production promotion evidence")
		return
	}
	rawQualification, ok := promotion.Metadata["promotionQualification"]
	if !ok {
		WriteControlProblem(w, r, http.StatusConflict, "rollback_target_unadmitted", "rollback target qualification is missing")
		return
	}
	encodedQualification, _ := json.Marshal(rawQualification)
	var qualification model.ReleaseQualification
	if json.Unmarshal(encodedQualification, &qualification) != nil || verifyReleaseQualificationSignature(h.cfg.TrustedQualificationSigningKeys, qualification) != nil || qualification.SourceSHA != target.CommitSHA || qualification.Artifact != target.ImageTag {
		WriteControlProblem(w, r, http.StatusConflict, "rollback_target_unadmitted", "rollback target qualification is invalid")
		return
	}
	if err := h.pipeline.VerifyReleaseArtifact(r.Context(), target.CommitSHA, target.ImageTag, qualification.Candidate); err != nil {
		WriteControlProblem(w, r, http.StatusForbidden, "rollback_target_unadmitted", "rollback target no longer satisfies production artifact admission: "+err.Error())
		return
	}
	enqueue, ok := h.pipelineEnqueueRequest(w, r, r.Header.Get("Idempotency-Key"), map[string]interface{}{"action": "release-rollback", "app": appID, "request": request, "targetDeploymentId": target.ID, "promotionOperationId": promotion.ID})
	if !ok {
		return
	}
	enqueue.Admission.OneActiveMutablePerApp = true
	enqueue = pipeline.DerivedChildRequest(enqueue, "rollback-route", "release", enqueue.Semantics)
	if replayed, found, resolveErr := h.resolveRollbackReplay(r.Context(), enqueue, appID, nil, target.ID); resolveErr != nil {
		writeOperationAcceptanceError(w, r, resolveErr)
		return
	} else if found {
		w.Header().Set("Location", "/api/v1/operations/"+replayed.Operation.ID)
		writeJSON(w, replayed.Operation)
		return
	}
	current, err := h.db.LatestSuccessfulDeployment(r.Context(), appID, "production")
	if err != nil || current == nil || current.ID == target.ID {
		WriteControlProblem(w, r, http.StatusConflict, "rollback_current_deployment_missing", "rollback target must differ from the current deployment")
		return
	}
	accepted, err := h.pipeline.QueueRollback(r.Context(), spec, *current, target, nil, enqueue, map[string]interface{}{"requestCI": principal.CI, "environment": h.cfg.EnvironmentID(), "releaseRollback": true, "sourceSha": target.CommitSHA, "artifact": target.ImageTag, "targetDeploymentId": target.ID})
	if err != nil {
		if replayed, found, _ := h.resolveRollbackReplay(r.Context(), enqueue, appID, nil, target.ID); found {
			w.Header().Set("Location", "/api/v1/operations/"+replayed.Operation.ID)
			writeJSON(w, replayed.Operation)
			return
		}
		writeOperationAcceptanceError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/operations/"+accepted.Operation.ID)
	if accepted.Replayed {
		writeJSON(w, accepted.Operation)
		return
	}
	writeJSONStatus(w, http.StatusAccepted, accepted.Operation)
}

func (h *Handler) queueRelease(w http.ResponseWriter, r *http.Request, preflight bool, promotion *model.ReleaseQualification) {
	requiredScope := ScopeReleaseStage
	if promotion != nil {
		requiredScope = ScopeReleasePromote
	}
	principal, ok := requireReleaseControlScope(w, r, requiredScope, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	if h.cfg == nil || !releaseActionEnvironmentAllowed(h.cfg.EnvironmentID(), promotion != nil) {
		WriteControlProblem(w, r, http.StatusConflict, "release_environment_invalid", "direct release preflight and deployment actions are staging-only; production requires a signed promotion")
		return
	}
	if h.db == nil || h.pipeline == nil || h.cfg == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "operation_store_unavailable", "durable release operations are unavailable")
		return
	}
	var request releaseRequest
	if err := decodeControlJSON(w, r, &request); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_release_request", err.Error())
		return
	}
	if promotion != nil {
		request.SourceSHA, request.Artifact, request.Candidate = promotion.SourceSHA, promotion.Artifact, promotion.Candidate
	}
	// The signer identity is derived solely from the verified workload token;
	// callers cannot claim a different reusable workflow in release JSON.
	if promotion == nil && principal.CI != nil {
		request.Candidate.SignerWorkflowRef = principal.CI.JobWorkflowRef
		request.Candidate.SignerWorkflowSHA = principal.CI.JobWorkflowSHA
	}
	privilegedCompatibility := principal.Legacy || principal.Allows(ScopeAdmin)
	principalMatches := releaseCandidateMatchesPrincipal(request.Candidate, principal)
	if promotion != nil {
		principalMatches = releaseSourceMatchesPrincipal(request.SourceSHA, principal)
	}
	if !validReleaseProvenance(request.SourceSHA, request.Artifact) || (!privilegedCompatibility && (!validReleaseCandidate(request.Candidate, request.SourceSHA, request.Artifact) || !principalMatches)) || (privilegedCompatibility && !releaseCandidateEmpty(request.Candidate) && !validReleaseCandidate(request.Candidate, request.SourceSHA, request.Artifact)) {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_release_provenance", "source, OCI digest, candidate attestation, and verified CI identity must agree")
		return
	}
	appID := chi.URLParam(r, "id")
	spec := h.findSpec(appID)
	if spec == nil || spec.Repo == nil {
		WriteControlProblem(w, r, http.StatusNotFound, "app_not_found", "app was not found, is disabled, or has no repository source")
		return
	}
	kind := "app.deploy"
	if preflight {
		kind = "app.preflight"
	}
	enqueue, ok := h.pipelineEnqueueRequest(w, r, r.Header.Get("Idempotency-Key"), map[string]interface{}{"action": kind, "app": appID, "release": releaseIdempotencyPayload(request, promotion), "environment": h.cfg.EnvironmentID()})
	if !ok {
		return
	}
	if !preflight {
		enqueue.Admission.OneActiveMutablePerApp = true
	}
	if resolved, resolveErr := h.pipeline.ResolveEnqueue(r.Context(), enqueue, kind, appID); resolveErr == nil {
		if !acceptedReleaseMatches(resolved, enqueue) {
			writeOperationAcceptanceError(w, r, &store.AcceptanceConflictError{})
			return
		}
		writeJSON(w, resolved.Operation)
		return
	} else if !errors.Is(resolveErr, store.ErrAcceptanceNotFound) {
		writeOperationAcceptanceError(w, r, resolveErr)
		return
	}
	if promotion != nil {
		if _, err := h.db.GetOperationByPromotionQualificationID(r.Context(), promotion.ID); err == nil {
			if resolved, resolveErr := h.pipeline.ResolveEnqueue(r.Context(), enqueue, kind, appID); resolveErr == nil && acceptedReleaseMatches(resolved, enqueue) {
				writeJSON(w, resolved.Operation)
				return
			}
			WriteControlProblem(w, r, http.StatusConflict, "qualification_already_consumed", "this staging qualification has already been consumed by a production promotion; issue a fresh qualification for an intentional re-promotion")
			return
		} else if err != pgx.ErrNoRows {
			WriteControlProblem(w, r, http.StatusInternalServerError, "operation_lookup_failed", "failed to inspect prior promotion consumption")
			return
		}
	}
	metadata := map[string]interface{}{"candidate": request.Candidate, "requestCI": principal.CI}
	if promotion != nil {
		metadata["promotionQualification"] = qualificationToMap(*promotion)
	}
	var accepted store.AcceptedOperation
	var err error
	if preflight {
		accepted, err = h.pipeline.QueueReleasePreflight(r.Context(), spec, request.SourceSHA, request.Artifact, h.cfg.EnvironmentID(), metadata, enqueue)
	} else {
		accepted, err = h.pipeline.QueueReleaseDeployment(r.Context(), spec, request.SourceSHA, request.Artifact, h.cfg.EnvironmentID(), metadata, enqueue)
	}
	if err != nil {
		if pgErr, duplicate := err.(*pgconn.PgError); duplicate && pgErr.Code == "23505" {
			if promotion != nil {
				if _, lookupErr := h.db.GetOperationByPromotionQualificationID(r.Context(), promotion.ID); lookupErr == nil {
					if resolved, resolveErr := h.pipeline.ResolveEnqueue(r.Context(), enqueue, kind, appID); resolveErr == nil && acceptedReleaseMatches(resolved, enqueue) {
						writeJSON(w, resolved.Operation)
						return
					}
					WriteControlProblem(w, r, http.StatusConflict, "qualification_already_consumed", "this staging qualification has already been consumed by a production promotion; issue a fresh qualification for an intentional re-promotion")
					return
				}
			}
		}
		writeOperationAcceptanceError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/operations/"+accepted.Operation.ID)
	if accepted.Replayed {
		writeJSON(w, accepted.Operation)
		return
	}
	writeJSONStatus(w, http.StatusAccepted, accepted.Operation)
}

func releaseIdempotencyPayload(request releaseRequest, promotion *model.ReleaseQualification) interface{} {
	if promotion == nil {
		return request
	}
	return promotionRequest{SourceSHA: request.SourceSHA, Artifact: request.Artifact, Qualification: *promotion}
}

func acceptedReleaseMatches(accepted store.AcceptedOperation, request pipeline.EnqueueRequest) bool {
	return acceptedRequestMatches(accepted, request)
}

func releaseActionEnvironmentAllowed(environment string, promoted bool) bool {
	if promoted {
		return environment == "production"
	}
	return environment == "staging"
}

func (h *Handler) productionRequiresSignedPromotion() bool {
	return h != nil && h.cfg != nil && h.cfg.EnvironmentID() == "production"
}

func (h *Handler) ListReleaseQualifications(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireControlScope(w, r, ScopeAPIRead); !ok {
		return
	}
	if h.db == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "operation_store_unavailable", "release qualification store is unavailable")
		return
	}
	appID := chi.URLParam(r, "id")
	ops, err := h.db.ListOperations(r.Context(), store.OperationFilter{App: appID, Kind: "release.qualification", Limit: 100})
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "qualification_list_failed", "failed to list release qualifications")
		return
	}
	qualifications := make([]model.ReleaseQualification, 0, len(ops))
	for _, op := range ops {
		if receipt, ok := qualificationFromOperation(op); ok {
			qualifications = append(qualifications, receipt)
		}
	}
	writeJSON(w, map[string]interface{}{"schemaVersion": "norn.release-qualifications/v2", "qualifications": qualifications, "count": len(qualifications)})
}

func (h *Handler) CreateReleaseQualification(w http.ResponseWriter, r *http.Request) {
	principal, ok := requireReleaseControlScope(w, r, ScopeReleaseQualify, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	if h.cfg == nil || h.cfg.EnvironmentID() != "staging" {
		WriteControlProblem(w, r, http.StatusConflict, "qualification_environment_invalid", "qualifications may only be issued by a staging control plane")
		return
	}
	if h.db == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "qualification_signing_unavailable", "staging qualification signing is not configured")
		return
	}
	var request qualificationRequest
	if err := decodeControlJSON(w, r, &request); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_qualification_request", err.Error())
		return
	}
	request.DeploymentID = strings.TrimSpace(request.DeploymentID)
	if _, err := uuid.Parse(request.DeploymentID); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_qualification_request", "deploymentId must be a UUID")
		return
	}
	appID := chi.URLParam(r, "id")
	enqueue, ok := h.pipelineEnqueueRequest(w, r, r.Header.Get("Idempotency-Key"), map[string]interface{}{
		"action": "release-qualification", "app": appID, "environment": "staging", "deploymentId": request.DeploymentID,
	})
	if !ok {
		return
	}
	if replayed, found, resolveErr := h.resolveReleaseQualificationReplay(r.Context(), enqueue, appID, request.DeploymentID); resolveErr != nil {
		writeOperationAcceptanceError(w, r, resolveErr)
		return
	} else if found {
		if permitted, reason := qualificationIntentPermitsCandidate(principal, replayed.Candidate); !permitted {
			WriteControlProblem(w, r, http.StatusForbidden, reason, "workload token may not replay this staging qualification")
			return
		}
		writeJSON(w, replayed)
		return
	}
	deployment, err := h.db.GetDeployment(r.Context(), request.DeploymentID)
	if err == pgx.ErrNoRows || (err == nil && deployment.App != appID) {
		WriteControlProblem(w, r, http.StatusNotFound, "deployment_not_found", "successful deployment was not found for this app")
		return
	}
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "deployment_lookup_failed", "failed to load deployment")
		return
	}
	if deployment.Status != model.StatusDeployed || deployment.Environment != "staging" || !validReleaseProvenance(deployment.CommitSHA, deployment.ImageTag) || !model.IsContentAddressedImage(deployment.ImageTag) {
		WriteControlProblem(w, r, http.StatusConflict, "deployment_not_qualifiable", "only a successful immutable staging release deployment may be qualified")
		return
	}
	candidate, err := h.releaseCandidateForDeployment(r, deployment.ID)
	if err != nil {
		WriteControlProblem(w, r, http.StatusConflict, "deployment_not_qualifiable", err.Error())
		return
	}
	// A fresh CI qualification is only for the exact immutable deployment the
	// current protected staging run created. Historical evidence is deliberately
	// limited to the separately allowlisted requalify workflow intent.
	if permitted, reason := qualificationIntentPermitsCandidate(principal, candidate); !permitted {
		WriteControlProblem(w, r, http.StatusForbidden, reason, "workload token may not issue this staging qualification")
		return
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	receipt := model.ReleaseQualification{SchemaVersion: releaseQualificationSchema, ID: uuid.NewString(), App: appID, Environment: "staging", DeploymentID: deployment.ID, SourceSHA: deployment.CommitSHA, Artifact: deployment.ImageTag, IssuedAt: now, ExpiresAt: now.Add(7 * 24 * time.Hour), Candidate: candidate}
	if err := signReleaseQualification(h.cfg.QualificationSigningKey, &receipt); err != nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "qualification_signing_unavailable", err.Error())
		return
	}
	op := model.Operation{ID: receipt.ID, Kind: "release.qualification", App: appID, Ref: receipt.SourceSHA, Status: model.OperationSucceeded, Risk: "staging release evidence", Source: "release-control-api", Message: "staging deployment qualified", Payload: qualificationToMap(receipt), Metadata: map[string]interface{}{"deploymentId": deployment.ID, "environment": h.cfg.EnvironmentID(), "requestCI": principal.CI}, StartedAt: now, FinishedAt: &now, MaxAttempts: 1}
	accepted, err := h.pipeline.QueueOperation(r.Context(), op, enqueue)
	if err != nil {
		if replayed, found, _ := h.resolveReleaseQualificationReplay(r.Context(), enqueue, appID, request.DeploymentID); found {
			if permitted, reason := qualificationIntentPermitsCandidate(principal, replayed.Candidate); !permitted {
				WriteControlProblem(w, r, http.StatusForbidden, reason, "workload token may not replay this staging qualification")
				return
			}
			writeJSON(w, replayed)
			return
		}
		writeOperationAcceptanceError(w, r, err)
		return
	}
	acceptedReceipt, valid := qualificationFromOperation(accepted.Operation)
	if !valid {
		WriteControlProblem(w, r, http.StatusInternalServerError, "qualification_store_failed", "accepted staging qualification is malformed")
		return
	}
	if accepted.Replayed {
		writeJSON(w, acceptedReceipt)
		return
	}
	writeJSONStatus(w, http.StatusCreated, acceptedReceipt)
}

func (h *Handler) resolveReleaseQualificationReplay(ctx context.Context, request pipeline.EnqueueRequest, appID, deploymentID string) (model.ReleaseQualification, bool, error) {
	accepted, err := h.pipeline.ResolveEnqueue(ctx, request, "release.qualification", appID)
	if errors.Is(err, store.ErrAcceptanceNotFound) {
		return model.ReleaseQualification{}, false, nil
	}
	if err != nil {
		return model.ReleaseQualification{}, false, err
	}
	if !acceptedRequestMatches(accepted, request) {
		return model.ReleaseQualification{}, false, &store.AcceptanceConflictError{Identity: store.OperationRequestIdentity{Kind: "release.qualification", Resource: appID}}
	}
	receipt, valid := qualificationFromOperation(accepted.Operation)
	if !valid || receipt.App != appID || receipt.DeploymentID != deploymentID {
		return model.ReleaseQualification{}, false, &store.AcceptanceSignatureError{Err: fmt.Errorf("accepted release qualification does not match request")}
	}
	if err := verifyReleaseQualificationSignature(h.releaseQualificationVerificationKeys(), receipt); err != nil {
		return model.ReleaseQualification{}, false, &store.AcceptanceSignatureError{Err: err}
	}
	return receipt, true, nil
}

func (h *Handler) releaseQualificationVerificationKeys() []string {
	if h == nil || h.cfg == nil {
		return nil
	}
	keys := append([]string(nil), h.cfg.TrustedQualificationSigningKeys...)
	if private, err := parseEd25519PrivateKey(h.cfg.QualificationSigningKey); err == nil {
		keys = append(keys, base64.RawStdEncoding.EncodeToString(private.Public().(ed25519.PublicKey)))
	}
	return keys
}

func qualificationIntentPermitsCandidate(principal AccessPrincipal, candidate model.ReleaseCandidate) (bool, string) {
	if principal.CI == nil {
		return true, ""
	}
	switch principal.CI.Intent {
	case "qualify":
		if releaseCandidateMatchesPrincipal(candidate, principal) {
			return true, ""
		}
		return false, "qualification_identity_mismatch"
	case "requalify":
		return true, ""
	default:
		return false, "qualification_intent_invalid"
	}
}

func (h *Handler) QueueReleasePromotion(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireReleaseControlScope(w, r, ScopeReleasePromote, chi.URLParam(r, "id")); !ok {
		return
	}
	if h.cfg == nil || h.cfg.EnvironmentID() != "production" {
		WriteControlProblem(w, r, http.StatusConflict, "promotion_environment_invalid", "promotions may only be queued by a production control plane")
		return
	}
	var request promotionRequest
	if err := decodeControlJSON(w, r, &request); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_promotion_request", err.Error())
		return
	}
	if request.SourceSHA != request.Qualification.SourceSHA || request.Artifact != request.Qualification.Artifact || !validReleaseProvenance(request.SourceSHA, request.Artifact) || !model.IsContentAddressedImage(request.Artifact) {
		WriteControlProblem(w, r, http.StatusBadRequest, "promotion_provenance_mismatch", "promotion sourceSha and artifact must exactly match the signed qualification")
		return
	}
	if !promotionQualificationMatchesApp(chi.URLParam(r, "id"), request.Qualification) {
		WriteControlProblem(w, r, http.StatusBadRequest, "promotion_provenance_mismatch", "qualification app must match the promoted app")
		return
	}
	if err := verifyReleaseQualification(h.cfg.TrustedQualificationSigningKeys, request.Qualification); err != nil {
		WriteControlProblem(w, r, http.StatusForbidden, "qualification_untrusted", err.Error())
		return
	}
	// The normal release queue re-decodes this payload into its narrower request.
	encoded, _ := json.Marshal(releaseRequest{SourceSHA: request.SourceSHA, Artifact: request.Artifact})
	r.Body = io.NopCloser(bytes.NewReader(encoded))
	h.queueRelease(w, r, false, &request.Qualification)
}

// requireReleaseControlScope deliberately narrows new managed tokens. The
// direct static control token and pre-scope legacy JWTs retain compatibility;
// every new scoped token must name the exact release capability and app/env.
func requireReleaseControlScope(w http.ResponseWriter, r *http.Request, scope, app string) (AccessPrincipal, bool) {
	principal, exists := AccessPrincipalFromRequest(r)
	if !exists {
		return requireControlScope(w, r, scope)
	}
	if principal.Legacy || principal.Allows(ScopeAdmin) || (principal.CI == nil && principal.Allows(ScopeAPIWrite)) {
		return principal, true
	}
	if !principal.Allows(scope) {
		WriteControlProblem(w, r, http.StatusForbidden, "insufficient_scope", "token lacks required scope "+scope)
		return AccessPrincipal{}, false
	}
	if principal.App != app || principal.Environment == "" {
		WriteControlProblem(w, r, http.StatusForbidden, "release_token_binding_invalid", "release token is not bound to this app and environment")
		return AccessPrincipal{}, false
	}
	return principal, true
}

func validReleaseProvenance(sourceSHA, artifact string) bool {
	if !fullSourceSHAPattern.MatchString(strings.TrimSpace(sourceSHA)) {
		return false
	}
	return artifact == "" || model.IsContentAddressedImage(artifact)
}

func validReleaseCandidate(candidate model.ReleaseCandidate, sourceSHA, artifact string) bool {
	if candidate.Provider != "github-actions" || candidate.Repository == "" || candidate.RepositoryID == "" || candidate.OwnerID == "" || candidate.RunID == "" || candidate.WorkflowRef == "" || !fullSourceSHAPattern.MatchString(candidate.WorkflowSHA) || candidate.SignerWorkflowRef == "" || !fullSourceSHAPattern.MatchString(candidate.SignerWorkflowSHA) || !strings.HasSuffix(candidate.SignerWorkflowRef, "@"+candidate.SignerWorkflowSHA) || candidate.Ref == "" || candidate.Attestation.Issuer == "" || candidate.Attestation.MaterialSHA != sourceSHA {
		return false
	}
	return artifact == "" || candidate.Attestation.SubjectDigest == artifactDigest(artifact)
}

func artifactDigest(artifact string) string {
	index := strings.LastIndex(artifact, "@sha256:")
	if index < 0 {
		return ""
	}
	return artifact[index+1:]
}

func releaseCandidateEmpty(candidate model.ReleaseCandidate) bool {
	return candidate.Provider == "" && candidate.Repository == "" && candidate.RepositoryID == "" && candidate.RunID == "" && candidate.WorkflowRef == "" && candidate.SignerWorkflowRef == "" && candidate.Attestation.SubjectDigest == ""
}

func releaseCandidateMatchesPrincipal(candidate model.ReleaseCandidate, principal AccessPrincipal) bool {
	if principal.Legacy || principal.Allows(ScopeAdmin) {
		return true
	}
	ci := principal.CI
	return ci != nil && candidate.Provider == ci.Provider && candidate.Repository == ci.Repository && candidate.RepositoryID == ci.RepositoryID && candidate.OwnerID == ci.RepositoryOwnerID && candidate.RunID == ci.RunID && candidate.RunAttempt == ci.RunAttempt && candidate.WorkflowRef == ci.WorkflowRef && candidate.WorkflowSHA == ci.WorkflowSHA && candidate.SignerWorkflowRef == ci.JobWorkflowRef && candidate.SignerWorkflowSHA == ci.JobWorkflowSHA && candidate.Ref == ci.Ref && candidate.Attestation.MaterialSHA == ci.SHA
}

func releaseSourceMatchesPrincipal(sourceSHA string, principal AccessPrincipal) bool {
	return principal.Legacy || principal.Allows(ScopeAdmin) || (principal.CI != nil && principal.CI.SHA == sourceSHA)
}

func (h *Handler) releaseCandidateForDeployment(r *http.Request, deploymentID string) (model.ReleaseCandidate, error) {
	op, err := h.db.GetReleaseOperationByDeploymentID(r.Context(), deploymentID)
	if err != nil {
		return model.ReleaseCandidate{}, fmt.Errorf("successful deployment has no durable release candidate")
	}
	raw, found := op.Metadata["candidate"]
	if !found {
		raw = op.Payload["candidate"]
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return model.ReleaseCandidate{}, fmt.Errorf("successful deployment candidate cannot be decoded")
	}
	var candidate model.ReleaseCandidate
	if json.Unmarshal(encoded, &candidate) != nil || !validReleaseCandidate(candidate, stringFromOperation(op.Payload, "sourceSha"), stringFromOperation(op.Payload, "artifact")) {
		return model.ReleaseCandidate{}, fmt.Errorf("successful deployment candidate is incomplete")
	}
	return candidate, nil
}

func stringFromOperation(values map[string]interface{}, key string) string {
	if values == nil {
		return ""
	}
	value, _ := values[key].(string)
	return value
}

func promotionQualificationMatchesApp(appID string, receipt model.ReleaseQualification) bool {
	return appID != "" && receipt.App == appID
}

func qualificationToMap(receipt model.ReleaseQualification) map[string]interface{} {
	encoded, _ := json.Marshal(receipt)
	var result map[string]interface{}
	_ = json.Unmarshal(encoded, &result)
	return result
}

func qualificationFromOperation(op model.Operation) (model.ReleaseQualification, bool) {
	if op.Kind != "release.qualification" {
		return model.ReleaseQualification{}, false
	}
	encoded, err := json.Marshal(op.Payload)
	if err != nil {
		return model.ReleaseQualification{}, false
	}
	var receipt model.ReleaseQualification
	if json.Unmarshal(encoded, &receipt) != nil || receipt.SchemaVersion != releaseQualificationSchema {
		return model.ReleaseQualification{}, false
	}
	return receipt, true
}

func verifyReleaseQualification(keys []string, receipt model.ReleaseQualification) error {
	if err := verifyReleaseQualificationSignature(keys, receipt); err != nil {
		return err
	}
	now := time.Now().UTC()
	if receipt.IssuedAt.IsZero() || receipt.IssuedAt.After(now.Add(5*time.Minute)) || receipt.ExpiresAt.IsZero() || !receipt.ExpiresAt.After(now) || receipt.ExpiresAt.Sub(receipt.IssuedAt) > 7*24*time.Hour+time.Minute {
		return fmt.Errorf("qualification is expired or outside the allowed validity window")
	}
	return nil
}

// verifyReleaseQualificationSignature establishes historical authenticity.
// Rollback deliberately uses it without the promotion freshness window: the
// target is additionally constrained to a durable successful promotion and is
// re-admitted against current artifact policy.
func verifyReleaseQualificationSignature(keys []string, receipt model.ReleaseQualification) error {
	if receipt.SchemaVersion != releaseQualificationSchema || receipt.Environment != "staging" || receipt.ID == "" || receipt.App == "" || receipt.DeploymentID == "" || !validReleaseProvenance(receipt.SourceSHA, receipt.Artifact) || !model.IsContentAddressedImage(receipt.Artifact) || !validReleaseCandidate(receipt.Candidate, receipt.SourceSHA, receipt.Artifact) {
		return fmt.Errorf("qualification is malformed")
	}
	if receipt.DSSE.PayloadType != releaseQualificationPayloadType || len(receipt.DSSE.Signatures) != 1 || receipt.DSSE.Payload == "" {
		return fmt.Errorf("qualification DSSE envelope is malformed")
	}
	if receipt.KeyID != receipt.DSSE.Signatures[0].KeyID || receipt.Signature != receipt.DSSE.Signatures[0].Sig {
		return fmt.Errorf("qualification display signature does not match authenticated DSSE envelope")
	}
	payload, err := base64.RawStdEncoding.DecodeString(receipt.DSSE.Payload)
	if err != nil {
		return fmt.Errorf("qualification DSSE payload is malformed")
	}
	if string(payload) != releaseQualificationCanonical(receipt) {
		return fmt.Errorf("qualification DSSE payload does not match receipt")
	}
	for _, encodedKey := range keys {
		public, err := parseEd25519PublicKey(encodedKey)
		if err != nil {
			continue
		}
		keyID := releaseQualificationKeyID(public)
		for _, signature := range receipt.DSSE.Signatures {
			if signature.KeyID != keyID {
				continue
			}
			raw, err := base64.RawStdEncoding.DecodeString(signature.Sig)
			if err == nil && ed25519.Verify(public, dssePAE(receipt.DSSE.PayloadType, payload), raw) {
				return nil
			}
		}
	}
	return fmt.Errorf("qualification signature is not trusted by this production control plane")
}

func releaseQualificationCanonical(receipt model.ReleaseQualification) string {
	canonical := struct {
		SchemaVersion, ID, App, Environment, DeploymentID, SourceSHA, Artifact, IssuedAt, ExpiresAt string
		Candidate                                                                                   model.ReleaseCandidate
	}{releaseQualificationSchema, receipt.ID, receipt.App, receipt.Environment, receipt.DeploymentID, receipt.SourceSHA, receipt.Artifact, canonicalAuditTime(receipt.IssuedAt), canonicalAuditTime(receipt.ExpiresAt), receipt.Candidate}
	encoded, _ := json.Marshal(canonical)
	return string(encoded)
}

func signReleaseQualification(encodedKey string, receipt *model.ReleaseQualification) error {
	private, err := parseEd25519PrivateKey(encodedKey)
	if err != nil {
		return fmt.Errorf("invalid Ed25519 staging signing key: %w", err)
	}
	payload := []byte(releaseQualificationCanonical(*receipt))
	public := private.Public().(ed25519.PublicKey)
	keyID := releaseQualificationKeyID(public)
	signature := base64.RawStdEncoding.EncodeToString(ed25519.Sign(private, dssePAE(releaseQualificationPayloadType, payload)))
	receipt.KeyID, receipt.Signature = keyID, signature
	receipt.DSSE = model.DSSEEnvelope{PayloadType: releaseQualificationPayloadType, Payload: base64.RawStdEncoding.EncodeToString(payload), Signatures: []model.DSSESignature{{KeyID: keyID, Sig: signature}}}
	return nil
}

func dssePAE(payloadType string, payload []byte) []byte {
	return []byte(fmt.Sprintf("DSSEv1 %d %s %d %s", len(payloadType), payloadType, len(payload), payload))
}
func releaseQualificationKeyID(public ed25519.PublicKey) string {
	sum := sha256.Sum256(public)
	return "ed25519:" + base64.RawURLEncoding.EncodeToString(sum[:12])
}
func parseEd25519PrivateKey(raw string) (ed25519.PrivateKey, error) {
	if block, _ := pem.Decode([]byte(raw)); block != nil {
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		private, ok := key.(ed25519.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("key is not Ed25519")
		}
		return private, nil
	}
	decoded, err := base64.RawStdEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil {
		return nil, err
	}
	if len(decoded) == ed25519.SeedSize {
		return ed25519.NewKeyFromSeed(decoded), nil
	}
	if len(decoded) == ed25519.PrivateKeySize {
		// The 64-byte representation is seed || public key. Do not trust its
		// caller-controlled public half: a malformed pair would otherwise pass
		// startup and later advertise a key ID that cannot verify its signatures.
		private := ed25519.NewKeyFromSeed(decoded[:ed25519.SeedSize])
		if subtle.ConstantTimeCompare(private, decoded) != 1 {
			return nil, fmt.Errorf("Ed25519 private key public half does not match its seed")
		}
		return private, nil
	}
	return nil, fmt.Errorf("key must be base64 Ed25519 seed or private key")
}
func parseEd25519PublicKey(raw string) (ed25519.PublicKey, error) {
	if block, _ := pem.Decode([]byte(raw)); block != nil {
		key, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		public, ok := key.(ed25519.PublicKey)
		if !ok {
			return nil, fmt.Errorf("key is not Ed25519")
		}
		return public, nil
	}
	decoded, err := base64.RawStdEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil || len(decoded) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("key must be base64 Ed25519 public key")
	}
	return ed25519.PublicKey(decoded), nil
}

// ValidateQualificationSigningConfiguration is called at control-plane
// startup, so a staging/production server never discovers malformed key
// material only after a release has been queued.
func ValidateQualificationSigningConfiguration(private string, trusted []string, requirePrivate bool) error {
	if requirePrivate {
		if _, err := parseEd25519PrivateKey(private); err != nil {
			return err
		}
	}
	for _, key := range trusted {
		if _, err := parseEd25519PublicKey(key); err != nil {
			return err
		}
	}
	return nil
}
