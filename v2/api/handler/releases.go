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
	"norn/v2/api/privateattestation"
	"norn/v2/api/store"
)

const releaseQualificationSchema = "norn.release-qualification/v2"
const releaseQualificationPayloadType = "application/vnd.norn.release-qualification.v2+json"
const maxQualificationListReceipts = 3

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

type privateAttestationRequest struct {
	SourceSHA string          `json:"sourceSha"`
	Artifact  string          `json:"artifact"`
	SBOM      json.RawMessage `json:"sbom"`
}

// CreatePrivateReleaseAttestation turns a GitHub OIDC-bound build assertion
// into server-canonical DSSE provenance and SPDX evidence. The Actions job
// never receives the Norn or KMS signing key.
func (h *Handler) CreatePrivateReleaseAttestation(w http.ResponseWriter, r *http.Request) {
	preventSensitiveResponseCaching(w)
	appID := chi.URLParam(r, "id")
	principal, ok := requireReleaseControlScope(w, r, ScopeReleaseAttest, appID)
	if !ok {
		return
	}
	if h.cfg == nil || h.cfg.EnvironmentID() != "staging" || h.cfg.ReleaseAttestationTrustMode != "norn-signed-private" || h.privateReleaseSigner == nil {
		WriteControlProblem(w, r, http.StatusConflict, "private_attestation_unavailable", "Norn-signed private release attestations are not configured on this staging control plane")
		return
	}
	if principal.CI == nil || (principal.CI.RepositoryVisibility != "private" && principal.CI.RepositoryVisibility != "internal") {
		WriteControlProblem(w, r, http.StatusForbidden, "private_attestation_identity_invalid", "a verified private GitHub Actions identity is required")
		return
	}
	protectedBranchRef := "refs/heads/" + strings.TrimSpace(h.cfg.GitHubActionsDefaultBranch)
	if principal.Environment != "staging" || principal.CI.Environment != "staging" || principal.CI.Intent != "attest" || !principal.CI.RefProtected || principal.CI.Ref != protectedBranchRef || principal.CI.EventName != "push" {
		WriteControlProblem(w, r, http.StatusForbidden, "private_attestation_lane_invalid", "private evidence may only be minted by the protected staging default-branch build identity")
		return
	}
	var request privateAttestationRequest
	if err := decodeControlJSONLimit(w, r, &request, maxPrivateAttestationJSONBody); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_private_attestation_request", err.Error())
		return
	}
	if request.SourceSHA != principal.CI.SHA || !validReleaseProvenance(request.SourceSHA, request.Artifact) || !model.IsContentAddressedImage(request.Artifact) {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_private_attestation_request", "source and immutable artifact must match the verified GitHub identity")
		return
	}
	spec := h.findSpec(appID)
	if spec == nil || h.pipeline == nil {
		WriteControlProblem(w, r, http.StatusNotFound, "app_not_found", "app was not found or has no release pipeline")
		return
	}
	candidate := releaseCandidateFromCI(*principal.CI, request.SourceSHA, request.Artifact, h.cfg.ReleaseAttestationTrustMode)
	if err := validateReleaseSpecBinding(spec, candidate, request.Artifact, h.pipeline.RegistryURL); err != nil {
		WriteControlProblem(w, r, http.StatusForbidden, "release_binding_mismatch", err.Error())
		return
	}
	if h.db == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "private_attestation_store_unavailable", "durable private release evidence storage is unavailable")
		return
	}
	key, digest, ok := appOperationIdempotency(w, r, principal, appID, "release.attestation", request)
	if !ok {
		return
	}
	if existing, handled := h.resolveAppOperationIdempotency(w, r, key, digest, "release.attestation", appID); handled {
		if existing != nil {
			writeJSON(w, existing.Payload)
		}
		return
	}
	bundle, err := privateattestation.Issue(r.Context(), h.privateReleaseSigner, appID, request.SourceSHA, request.Artifact, request.SBOM, candidate)
	if err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "private_attestation_failed", err.Error())
		return
	}
	candidate.Attestation.Bundle = bundle
	now := time.Now().UTC()
	payload := map[string]interface{}{"schemaVersion": model.NornPrivateAttestationSchema, "candidate": candidate}
	op := &model.Operation{ID: uuid.NewString(), Kind: "release.attestation", App: appID, Ref: request.SourceSHA, Status: model.OperationSucceeded, Risk: "private release evidence", Source: "release-control-api", Message: "private release evidence signed", Payload: payload, Metadata: map[string]interface{}{"idempotencyKey": key, "requestDigest": digest, "principal": principal.Subject, "principalTokenId": principal.TokenID, "environment": h.cfg.EnvironmentID(), "requestCI": principal.CI}, StartedAt: now, FinishedAt: &now, MaxAttempts: 1}
	if err := h.db.InsertCompletedOperation(r.Context(), op); err != nil {
		if existing, lookupErr := h.db.GetOperationByIdempotencyKey(r.Context(), key); lookupErr == nil {
			storedDigest, _ := existing.Metadata["requestDigest"].(string)
			if existing.Kind == "release.attestation" && existing.App == appID && storedDigest == digest {
				writeJSON(w, existing.Payload)
				return
			}
		}
		WriteControlProblem(w, r, http.StatusInternalServerError, "private_attestation_store_failed", "failed to durably store private release evidence")
		return
	}
	writeJSONStatus(w, http.StatusCreated, payload)
}

func releaseCandidateFromCI(ci CIIdentity, sourceSHA, artifact, trustMode string) model.ReleaseCandidate {
	return model.ReleaseCandidate{
		Provider: ci.Provider, Repository: ci.Repository, RepositoryID: ci.RepositoryID, OwnerID: ci.RepositoryOwnerID,
		RepositoryVisibility: ci.RepositoryVisibility, RunID: ci.RunID, RunAttempt: ci.RunAttempt, WorkflowRef: ci.WorkflowRef,
		WorkflowSHA: ci.WorkflowSHA, SignerWorkflowRef: ci.JobWorkflowRef, SignerWorkflowSHA: ci.JobWorkflowSHA, Ref: ci.Ref,
		Attestation: model.ReleaseAttestationIdentity{Mode: releaseAttestationMode(ci.RepositoryVisibility, trustMode), Verifier: releaseAttestationVerifier(ci.RepositoryVisibility, trustMode), Issuer: githubActionsOIDCIssuer, SubjectDigest: artifactDigest(artifact), MaterialSHA: sourceSHA},
	}
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
	preventSensitiveResponseCaching(w)
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
	if err := decodeControlJSONLimit(w, r, &request, maxReleaseEvidenceJSONBody); err != nil {
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
	key, digest, ok := appOperationIdempotency(w, r, principal, appID, "app.rollback", request)
	if !ok {
		return
	}
	if existing, handled := h.resolveAppOperationIdempotency(w, r, key, digest, "app.rollback", appID); handled {
		if existing != nil {
			existing.AttachReceipt()
			writeJSON(w, existing)
		}
		return
	}
	if !h.requireNoActiveAppOperation(w, r, appID) {
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
	if err := validateReleaseSpecBinding(spec, qualification.Candidate, target.ImageTag, h.pipeline.RegistryURL); err != nil {
		WriteControlProblem(w, r, http.StatusConflict, "rollback_target_unadmitted", "rollback target no longer matches the app source and artifact binding: "+err.Error())
		return
	}
	if err := h.verifyRollbackReleaseArtifact(r.Context(), spec, target, qualification.Candidate); err != nil {
		WriteControlProblem(w, r, http.StatusForbidden, "rollback_target_unadmitted", "rollback target no longer satisfies production artifact admission: "+err.Error())
		return
	}
	current, err := h.db.LatestSuccessfulDeployment(r.Context(), appID, "production")
	if err != nil || current == nil || current.ID == target.ID {
		WriteControlProblem(w, r, http.StatusConflict, "rollback_current_deployment_missing", "rollback target must differ from the current deployment")
		return
	}
	_, operationID, err := h.pipeline.RollbackRegionsOperationContext(r.Context(), spec, *current, target, nil, map[string]interface{}{"idempotencyKey": key, "requestDigest": digest, "principal": principal.Subject, "principalTokenId": principal.TokenID, "requestCI": principal.CI, "environment": h.cfg.EnvironmentID(), "releaseRollback": true, "sourceSha": target.CommitSHA, "artifact": target.ImageTag, "targetDeploymentId": target.ID})
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "rollback_queue_failed", "failed to queue exact release rollback")
		return
	}
	op, err := h.db.GetOperation(r.Context(), operationID)
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "rollback_queue_failed", "failed to read release rollback operation")
		return
	}
	w.Header().Set("Location", "/api/v1/operations/"+op.ID)
	writeJSONStatus(w, http.StatusAccepted, op)
}

func (h *Handler) verifyRollbackReleaseArtifact(ctx context.Context, spec *model.InfraSpec, target *model.Deployment, candidate model.ReleaseCandidate) error {
	if h == nil || h.pipeline == nil || target == nil {
		return fmt.Errorf("rollback release verification is unavailable")
	}
	return h.pipeline.VerifyReleaseArtifact(ctx, spec, target.CommitSHA, target.ImageTag, candidate)
}

func (h *Handler) queueRelease(w http.ResponseWriter, r *http.Request, preflight bool, promotion *model.ReleaseQualification) {
	preventSensitiveResponseCaching(w)
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
	if err := decodeControlJSONLimit(w, r, &request, maxReleaseEvidenceJSONBody); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_release_request", err.Error())
		return
	}
	if promotion != nil {
		request.SourceSHA, request.Artifact, request.Candidate = promotion.SourceSHA, promotion.Artifact, promotion.Candidate
	}
	// The signer identity is derived solely from the verified workload token;
	// callers cannot claim a different reusable workflow in release JSON.
	if promotion == nil && principal.CI != nil {
		suppliedEvidence := request.Candidate.Attestation
		request.Candidate = releaseCandidateFromCI(*principal.CI, request.SourceSHA, request.Artifact, h.cfg.ReleaseAttestationTrustMode)
		request.Candidate.Attestation.Bundle = suppliedEvidence.Bundle
		request.Candidate.Attestation.ProvenanceURI = suppliedEvidence.ProvenanceURI
		request.Candidate.Attestation.SBOMURI = suppliedEvidence.SBOMURI
	}
	privilegedCompatibility := principal.Legacy || principal.Allows(ScopeAdmin)
	principalMatches := releaseCandidateMatchesPrincipal(request.Candidate, principal)
	if promotion != nil {
		principalMatches = releasePromotionMatchesPrincipal(request.Candidate, principal)
		if h.externalBootstrapCandidateForApp(chi.URLParam(r, "id"), request.Candidate) && releaseRepositoryMatchesPrincipal(request.Candidate, principal) {
			principalMatches = true
		}
	}
	trustedCandidate := validReleaseCandidateForTrust(request.Candidate, request.SourceSHA, request.Artifact, h.cfg.ReleaseAttestationTrustMode) || (promotion != nil && h.externalBootstrapCandidateForApp(chi.URLParam(r, "id"), request.Candidate))
	if !validReleaseProvenance(request.SourceSHA, request.Artifact) || (!privilegedCompatibility && (!trustedCandidate || !principalMatches)) || (privilegedCompatibility && !releaseCandidateEmpty(request.Candidate) && !trustedCandidate) {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_release_provenance", "source, OCI digest, candidate attestation, and verified CI identity must agree")
		return
	}
	appID := chi.URLParam(r, "id")
	spec := h.findSpec(appID)
	if spec == nil || spec.Repo == nil {
		WriteControlProblem(w, r, http.StatusNotFound, "app_not_found", "app was not found, is disabled, or has no repository source")
		return
	}
	if err := validateReleaseSpecBinding(spec, request.Candidate, request.Artifact, h.pipeline.RegistryURL); err != nil {
		WriteControlProblem(w, r, http.StatusForbidden, "release_binding_mismatch", err.Error())
		return
	}
	kind := "app.deploy"
	if preflight {
		kind = "app.preflight"
	}
	key, digest, ok := appOperationIdempotency(w, r, principal, appID, kind, releaseIdempotencyPayload(request, promotion))
	if !ok {
		return
	}
	if existing, handled := h.resolveAppOperationIdempotency(w, r, key, digest, kind, appID); handled {
		if existing != nil {
			existing.AttachReceipt()
			writeJSON(w, existing)
		}
		return
	}
	if promotion != nil {
		if _, err := h.db.GetOperationByPromotionQualificationID(r.Context(), promotion.ID); err == nil {
			WriteControlProblem(w, r, http.StatusConflict, "qualification_already_consumed", "this staging qualification has already been consumed by a production promotion; issue a fresh qualification for an intentional re-promotion")
			return
		} else if err != pgx.ErrNoRows {
			WriteControlProblem(w, r, http.StatusInternalServerError, "operation_lookup_failed", "failed to inspect prior promotion consumption")
			return
		}
	}
	if !preflight && !h.requireNoActiveAppOperation(w, r, appID) {
		return
	}
	metadata := map[string]interface{}{"idempotencyKey": key, "requestDigest": digest, "principal": principal.Subject, "principalTokenId": principal.TokenID, "candidate": request.Candidate, "requestCI": principal.CI}
	if promotion != nil {
		metadata["promotionQualification"] = qualificationToMap(*promotion)
	}
	var op *model.Operation
	var err error
	if preflight {
		op, err = h.pipeline.QueueReleasePreflight(r.Context(), spec, request.SourceSHA, request.Artifact, h.cfg.EnvironmentID(), metadata)
	} else {
		op, err = h.pipeline.QueueReleaseDeployment(r.Context(), spec, request.SourceSHA, request.Artifact, h.cfg.EnvironmentID(), metadata)
	}
	if err != nil {
		if pgErr, duplicate := err.(*pgconn.PgError); duplicate && pgErr.Code == "23505" {
			if existing, lookupErr := h.db.GetOperationByIdempotencyKey(r.Context(), key); lookupErr == nil && existing.Kind == kind && existing.App == appID {
				storedDigest, _ := existing.Metadata["requestDigest"].(string)
				if storedDigest == digest {
					existing.AttachReceipt()
					writeJSON(w, existing)
					return
				}
			}
			if promotion != nil {
				if _, lookupErr := h.db.GetOperationByPromotionQualificationID(r.Context(), promotion.ID); lookupErr == nil {
					WriteControlProblem(w, r, http.StatusConflict, "qualification_already_consumed", "this staging qualification has already been consumed by a production promotion; issue a fresh qualification for an intentional re-promotion")
					return
				}
			}
		}
		WriteControlProblem(w, r, http.StatusInternalServerError, "operation_create_failed", "failed to durably queue release operation")
		return
	}
	w.Header().Set("Location", "/api/v1/operations/"+op.ID)
	writeJSONStatus(w, http.StatusAccepted, op)
}

func releaseIdempotencyPayload(request releaseRequest, promotion *model.ReleaseQualification) interface{} {
	if promotion == nil {
		return request
	}
	return promotionRequest{SourceSHA: request.SourceSHA, Artifact: request.Artifact, Qualification: *promotion}
}

func releaseActionEnvironmentAllowed(environment string, promoted bool) bool {
	if promoted {
		return environment == "production"
	}
	return environment == "staging"
}

// validateReleaseSpecBinding binds CI evidence to the server-owned app spec.
// A valid GitHub OIDC identity and digest alone are not enough: otherwise an
// allowed application repository could deploy a different app's source or OCI
// namespace merely by choosing it in the reusable workflow inputs.
func validateReleaseSpecBinding(spec *model.InfraSpec, candidate model.ReleaseCandidate, artifact, registryURL string) error {
	return model.ValidateReleaseSpecBinding(spec, candidate, artifact, registryURL)
}

func (h *Handler) productionRequiresSignedPromotion() bool {
	return h != nil && h.cfg != nil && h.cfg.EnvironmentID() == "production"
}

func (h *Handler) ListReleaseQualifications(w http.ResponseWriter, r *http.Request) {
	preventSensitiveResponseCaching(w)
	if _, ok := requireControlScope(w, r, ScopeAPIRead); !ok {
		return
	}
	if h.db == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "operation_store_unavailable", "release qualification store is unavailable")
		return
	}
	appID := chi.URLParam(r, "id")
	if h.cfg != nil && h.cfg.EnvironmentID() == "production" {
		receipts, err := h.db.ListPromotionQualifications(r.Context(), appID, maxQualificationListReceipts)
		if err != nil {
			WriteControlProblem(w, r, http.StatusInternalServerError, "qualification_list_failed", "failed to list promoted release qualifications")
			return
		}
		qualifications := trustedProductionQualifications(receipts, appID, h.cfg.ReleaseAttestationTrustMode, h.cfg.TrustedQualificationSigningKeys)
		writeJSON(w, map[string]interface{}{"schemaVersion": "norn.release-qualifications/v2", "qualifications": qualifications, "count": len(qualifications)})
		return
	}
	ops, err := h.db.ListOperations(r.Context(), releaseQualificationListFilter(appID))
	if err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "qualification_list_failed", "failed to list release qualifications")
		return
	}
	qualifications := recentUnexpiredQualifications(ops, time.Now().UTC())
	writeJSON(w, map[string]interface{}{"schemaVersion": "norn.release-qualifications/v2", "qualifications": qualifications, "count": len(qualifications)})
}

func trustedProductionQualifications(receipts []model.ReleaseQualification, appID, trustMode string, trustedKeys []string) []model.ReleaseQualification {
	qualifications := make([]model.ReleaseQualification, 0, maxQualificationListReceipts)
	for _, receipt := range receipts {
		if len(qualifications) == maxQualificationListReceipts {
			break
		}
		if receipt.App != appID || verifyReleaseQualification(trustedKeys, receipt) != nil || !validReleaseCandidateForTrust(receipt.Candidate, receipt.SourceSHA, receipt.Artifact, trustMode) {
			continue
		}
		qualifications = append(qualifications, receipt)
	}
	return qualifications
}

func releaseQualificationListFilter(appID string) store.OperationFilter {
	return store.OperationFilter{App: appID, Kind: "release.qualification", UnexpiredQualification: true, Limit: maxQualificationListReceipts}
}

func recentUnexpiredQualifications(operations []model.Operation, now time.Time) []model.ReleaseQualification {
	qualifications := make([]model.ReleaseQualification, 0, maxQualificationListReceipts)
	for _, operation := range operations {
		if len(qualifications) == maxQualificationListReceipts {
			break
		}
		if receipt, ok := qualificationFromOperation(operation); ok && receipt.ExpiresAt.After(now) {
			qualifications = append(qualifications, receipt)
		}
	}
	return qualifications
}

func (h *Handler) CreateReleaseQualification(w http.ResponseWriter, r *http.Request) {
	preventSensitiveResponseCaching(w)
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
	if err := decodeControlJSONLimit(w, r, &request, maxReleaseEvidenceJSONBody); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_qualification_request", err.Error())
		return
	}
	request.DeploymentID = strings.TrimSpace(request.DeploymentID)
	if _, err := uuid.Parse(request.DeploymentID); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_qualification_request", "deploymentId must be a UUID")
		return
	}
	appID := chi.URLParam(r, "id")
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
	key, digest, ok := appOperationIdempotency(w, r, principal, appID, "release.qualification", request)
	if !ok {
		return
	}
	if existing, handled := h.resolveAppOperationIdempotency(w, r, key, digest, "release.qualification", appID); handled {
		if existing != nil {
			if receipt, valid := qualificationFromOperation(*existing); valid {
				writeJSON(w, receipt)
			}
		}
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
	if permitted, reason := h.qualificationIntentPermitsCandidate(appID, principal, candidate); !permitted {
		WriteControlProblem(w, r, http.StatusForbidden, reason, "workload token may not issue this staging qualification")
		return
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	receipt := model.ReleaseQualification{SchemaVersion: releaseQualificationSchema, ID: uuid.NewString(), App: appID, Environment: "staging", DeploymentID: deployment.ID, SourceSHA: deployment.CommitSHA, Artifact: deployment.ImageTag, IssuedAt: now, ExpiresAt: now.Add(7 * 24 * time.Hour), Candidate: candidate}
	if err := signReleaseQualification(h.cfg.QualificationSigningKey, &receipt); err != nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "qualification_signing_unavailable", err.Error())
		return
	}
	op := &model.Operation{ID: receipt.ID, Kind: "release.qualification", App: appID, Ref: receipt.SourceSHA, Status: model.OperationSucceeded, Risk: "staging release evidence", Source: "release-control-api", Message: "staging deployment qualified", Payload: qualificationToMap(receipt), Metadata: map[string]interface{}{"idempotencyKey": key, "requestDigest": digest, "principal": principal.Subject, "principalTokenId": principal.TokenID, "deploymentId": deployment.ID, "environment": h.cfg.EnvironmentID(), "requestCI": principal.CI}, StartedAt: now, FinishedAt: &now, MaxAttempts: 1}
	if err := h.db.InsertCompletedOperation(r.Context(), op); err != nil {
		if existing, lookupErr := h.db.GetOperationByIdempotencyKey(r.Context(), key); lookupErr == nil {
			if !qualificationReplayMatches(existing, appID, digest) {
				WriteControlProblem(w, r, http.StatusConflict, "idempotency_key_reused", "Idempotency-Key was already used for a different app operation")
				return
			}
			if replay, valid := qualificationFromOperation(*existing); valid {
				writeJSON(w, replay)
				return
			}
		}
		WriteControlProblem(w, r, http.StatusInternalServerError, "qualification_store_failed", "failed to store staging qualification")
		return
	}
	writeJSONStatus(w, http.StatusCreated, receipt)
}

func qualificationReplayMatches(existing *model.Operation, appID, requestDigest string) bool {
	if existing == nil {
		return false
	}
	storedDigest, _ := existing.Metadata["requestDigest"].(string)
	return existing.Kind == "release.qualification" && existing.App == appID && storedDigest == requestDigest
}

func (h *Handler) qualificationIntentPermitsCandidate(appID string, principal AccessPrincipal, candidate model.ReleaseCandidate) (bool, string) {
	if principal.CI == nil {
		return true, ""
	}
	// This is a narrow adoption lane for the separately pinned first-image
	// bootstrap signer. It preserves that historical signer in the signed
	// qualification; the current requalifier only proves same repository IDs.
	if principal.CI.Intent == "requalify" && h.externalBootstrapCandidateForApp(appID, candidate) && releaseRepositoryMatchesPrincipal(candidate, principal) {
		return true, ""
	}
	return qualificationIntentPermitsCandidate(principal, candidate)
}

// qualificationIntentPermitsCandidate is the normal managed-release policy.
// Keep it separate from the explicit external bootstrap adoption exception so
// ordinary qualification callers cannot accidentally inherit that exception.
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
		if releaseRequalificationMatchesPrincipal(candidate, principal) {
			return true, ""
		}
		return false, "qualification_identity_mismatch"
	default:
		return false, "qualification_intent_invalid"
	}
}

func releaseRepositoryMatchesPrincipal(candidate model.ReleaseCandidate, principal AccessPrincipal) bool {
	ci := principal.CI
	return ci != nil && candidate.Provider == ci.Provider && candidate.Repository == ci.Repository && candidate.RepositoryID == ci.RepositoryID && candidate.OwnerID == ci.RepositoryOwnerID && candidate.RepositoryVisibility == ci.RepositoryVisibility
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
	if err := decodeControlJSONLimit(w, r, &request, maxReleaseEvidenceJSONBody); err != nil {
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
	mode, verifier := candidate.Attestation.Mode, candidate.Attestation.Verifier
	private := candidate.RepositoryVisibility == "private" || candidate.RepositoryVisibility == "internal"
	validMode := (candidate.RepositoryVisibility == "public" && mode == "github-public" && verifier == "Sigstore public-good") || (private && mode == "github-private" && verifier == "GitHub private Sigstore") || (private && mode == "norn-signed-private" && verifier == "Norn private DSSE")
	if candidate.Provider != "github-actions" || candidate.Repository == "" || candidate.RepositoryID == "" || candidate.OwnerID == "" || (candidate.RepositoryVisibility != "public" && !private) || candidate.RunID == "" || candidate.WorkflowRef == "" || !fullSourceSHAPattern.MatchString(candidate.WorkflowSHA) || candidate.SignerWorkflowRef == "" || !fullSourceSHAPattern.MatchString(candidate.SignerWorkflowSHA) || !strings.HasSuffix(candidate.SignerWorkflowRef, "@"+candidate.SignerWorkflowSHA) || candidate.Ref == "" || candidate.Attestation.Issuer == "" || !validMode || candidate.Attestation.MaterialSHA != sourceSHA {
		return false
	}
	return artifact == "" || candidate.Attestation.SubjectDigest == artifactDigest(artifact)
}

func validReleaseCandidateForTrust(candidate model.ReleaseCandidate, sourceSHA, artifact, trustMode string) bool {
	return validReleaseCandidate(candidate, sourceSHA, artifact) && candidate.Attestation.Mode == releaseAttestationMode(candidate.RepositoryVisibility, trustMode) && candidate.Attestation.Verifier == releaseAttestationVerifier(candidate.RepositoryVisibility, trustMode)
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
	return ci != nil && candidate.Provider == ci.Provider && candidate.Repository == ci.Repository && candidate.RepositoryID == ci.RepositoryID && candidate.OwnerID == ci.RepositoryOwnerID && candidate.RepositoryVisibility == ci.RepositoryVisibility && candidate.RunID == ci.RunID && candidate.RunAttempt == ci.RunAttempt && candidate.WorkflowRef == ci.WorkflowRef && candidate.WorkflowSHA == ci.WorkflowSHA && candidate.SignerWorkflowRef == ci.JobWorkflowRef && candidate.SignerWorkflowSHA == ci.JobWorkflowSHA && candidate.Ref == ci.Ref && candidate.Attestation.MaterialSHA == ci.SHA
}

func releaseSourceMatchesPrincipal(sourceSHA string, principal AccessPrincipal) bool {
	return principal.Legacy || principal.Allows(ScopeAdmin) || (principal.CI != nil && principal.CI.SHA == sourceSHA)
}

func releaseAttestationMode(visibility string, trustMode ...string) string {
	if visibility == "private" || visibility == "internal" {
		if len(trustMode) > 0 && trustMode[0] == "norn-signed-private" {
			return "norn-signed-private"
		}
		return "github-private"
	}
	return "github-public"
}

func releaseAttestationVerifier(visibility string, trustMode ...string) string {
	if visibility == "private" || visibility == "internal" {
		if len(trustMode) > 0 && trustMode[0] == "norn-signed-private" {
			return "Norn private DSSE"
		}
		return "GitHub private Sigstore"
	}
	return "Sigstore public-good"
}

// Promotion/requalification tokens can have a different run, ref, and event
// from staging evidence, but may never cross the repository, numeric owner, or
// reusable signer boundary. The exact app-to-repository binding prevents an
// unintended cross-product authorization policy.
func releasePromotionMatchesPrincipal(candidate model.ReleaseCandidate, principal AccessPrincipal) bool {
	if principal.Legacy || principal.Allows(ScopeAdmin) {
		return true
	}
	ci := principal.CI
	return ci != nil && ci.SHA == candidate.Attestation.MaterialSHA && candidate.Provider == ci.Provider && candidate.Repository == ci.Repository && candidate.RepositoryID == ci.RepositoryID && candidate.OwnerID == ci.RepositoryOwnerID && candidate.RepositoryVisibility == ci.RepositoryVisibility && candidate.SignerWorkflowRef == ci.JobWorkflowRef && candidate.SignerWorkflowSHA == ci.JobWorkflowSHA
}

// Requalification runs from the current protected staging branch but may
// intentionally renew evidence for an older durable staging deployment. It
// keeps the app/repository/signer boundary while not requiring dispatch HEAD
// to equal that historical artifact source SHA.
func releaseRequalificationMatchesPrincipal(candidate model.ReleaseCandidate, principal AccessPrincipal) bool {
	if principal.Legacy || principal.Allows(ScopeAdmin) {
		return true
	}
	ci := principal.CI
	return ci != nil && candidate.Provider == ci.Provider && candidate.Repository == ci.Repository && candidate.RepositoryID == ci.RepositoryID && candidate.OwnerID == ci.RepositoryOwnerID && candidate.RepositoryVisibility == ci.RepositoryVisibility && candidate.SignerWorkflowRef == ci.JobWorkflowRef && candidate.SignerWorkflowSHA == ci.JobWorkflowSHA
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
	if json.Unmarshal(encoded, &candidate) != nil || !(validReleaseCandidateForTrust(candidate, stringFromOperation(op.Payload, "sourceSha"), stringFromOperation(op.Payload, "artifact"), h.cfg.ReleaseAttestationTrustMode) || h.externalBootstrapCandidateForApp(op.App, candidate)) {
		return model.ReleaseCandidate{}, fmt.Errorf("successful deployment candidate is incomplete")
	}
	return candidate, nil
}

func (h *Handler) externalBootstrapCandidateForApp(appID string, candidate model.ReleaseCandidate) bool {
	configured, err := h.externalFleetAdmissionConfig(appID)
	if err != nil {
		return false
	}
	return validExternalBootstrapCandidate(candidate, candidate.Attestation.MaterialSHA, "", h.releaseTrustMode(), configured)
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
	return model.VerifyReleaseQualificationSignature(keys, receipt)
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
