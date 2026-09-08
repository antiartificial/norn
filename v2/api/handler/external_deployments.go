package handler

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"norn/v2/api/model"
	"norn/v2/api/store"
)

// externalFleetReceiptSchema is a deliberately narrow bridge for the pilot
// hello-norn-mysql job. It does not make deploy:false apps deployable through
// the normal pipeline and it is unavailable without an injected live verifier.
const externalFleetReceiptSchema = "norn.external-fleet-deployment-receipt/v3"
const externalFleetReceiptSchemaV4 = "norn.external-fleet-deployment-receipt/v4"
const externalFleetLogicalIdentitySchemaV4 = "norn.external-fleet-logical-identity/v4"
const externalFleetAdmissionNonceTTL = 10 * time.Minute

var sha256HexPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var externalNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

type externalDeploymentRequest struct {
	Action  string                          `json:"action,omitempty"`
	Receipt *ExternalFleetDeploymentReceipt `json:"receipt,omitempty"`
}

type externalFleetAdmissionBeginRequest struct {
	LogicalIdentity ExternalFleetLogicalIdentity `json:"logicalIdentity"`
}

type externalFleetAdmissionResponse struct {
	SchemaVersion string    `json:"schemaVersion"`
	AdmissionID   string    `json:"admissionId"`
	Nonce         string    `json:"nonce,omitempty"`
	ExpiresAt     time.Time `json:"expiresAt,omitempty"`
	State         string    `json:"state"`
	OperationID   string    `json:"operationId,omitempty"`
}

type externalFleetAdmissionContextResponse struct {
	AdmissionID  string   `json:"admissionId"`
	State        string   `json:"state"`
	OperationID  string   `json:"operationId,omitempty"`
	CleanupState string   `json:"cleanupState"`
	RetryLineage []string `json:"retryLineage"`
	Checkpoints  []string `json:"checkpoints"`
}

// ExternalFleetLogicalIdentity is the retry-stable part of an admission. It
// deliberately excludes run/attempt/JTI, nonce, and Nomad observations.
type ExternalFleetLogicalIdentity struct {
	SchemaVersion string                                `json:"schemaVersion"`
	SourceSHA     string                                `json:"sourceSha"`
	Artifact      string                                `json:"artifact"`
	Candidate     model.ReleaseCandidate                `json:"candidate"`
	Fleet         ExternalFleetLogicalExecutionIdentity `json:"fleet"`
}

type ExternalFleetLogicalExecutionIdentity struct {
	Namespace     string `json:"namespace"`
	PlanID        string `json:"planId"`
	PlanSHA256    string `json:"planSha256"`
	RootAttemptID string `json:"rootAttemptId"`
	FleetCommit   string `json:"fleetCommit"`
}

// externalFleetDeploymentReceipt contains identifiers and immutable pointers,
// never passwords, environment files, inline HCL, or self-asserted health
// booleans. The independent verifier below must obtain every claimed runtime
// fact from Nomad/Consul/ingress/GitHub before this is persisted.
type ExternalFleetDeploymentReceipt struct {
	SchemaVersion           string                        `json:"schemaVersion"`
	AdmissionID             string                        `json:"admissionId,omitempty"`
	Nonce                   string                        `json:"nonce"`
	App                     string                        `json:"app"`
	SourceSHA               string                        `json:"sourceSha"`
	Artifact                string                        `json:"artifact"`
	Candidate               model.ReleaseCandidate        `json:"candidate"`
	AttestationBundleSHA256 string                        `json:"attestationBundleSha256"`
	SBOMBundleSHA256        string                        `json:"sbomBundleSha256"`
	Fleet                   ExternalFleetExecutionProof   `json:"fleet"`
	Chronology              []ExternalFleetChronologyStep `json:"chronology"`
}

type ExternalFleetExecutionProof struct {
	Namespace        string                     `json:"namespace"`
	Migration        ExternalFleetNomadJobProof `json:"migration"`
	Runtime          ExternalFleetNomadJobProof `json:"runtime"`
	PlanID           string                     `json:"planId"`
	ApplyRunID       string                     `json:"applyRunId"`
	ApplyRunAttempt  string                     `json:"applyRunAttempt"`
	PlanSHA256       string                     `json:"planSha256"`
	RunnerAttemptID  string                     `json:"runnerAttemptId"`
	RootAttemptID    string                     `json:"rootAttemptId"`
	FleetCommit      string                     `json:"fleetCommit,omitempty"`
	NonceEvidenceRef string                     `json:"nonceEvidenceRef"`
}

// ExternalFleetNomadJobProof reflects Nomad's v2 JobRegisterResponse: a
// submitted job is bound by job ID, evaluation ID, and returned modify index.
// It deliberately does not invent a "submission ID" that Nomad does not emit.
type ExternalFleetNomadJobProof struct {
	JobID              string   `json:"jobId"`
	HCLSHA256          string   `json:"hclSha256"`
	EvalID             string   `json:"evalId"`
	EvalCreateIndex    uint64   `json:"evalCreateIndex,omitempty"`
	EvalJobModifyIndex uint64   `json:"evalJobModifyIndex,omitempty"`
	JobCreateIndex     uint64   `json:"jobCreateIndex,omitempty"`
	JobModifyIndex     uint64   `json:"jobModifyIndex"`
	JobVersion         uint64   `json:"jobVersion,omitempty"`
	CurrentSpecSHA256  string   `json:"currentSpecSha256,omitempty"`
	SubmissionSHA256   string   `json:"submissionSha256,omitempty"`
	EvaluationChainIDs []string `json:"evaluationChainIds,omitempty"`
	CheckpointID       string   `json:"checkpointId"`
}

type ExternalFleetChronologyStep struct {
	Phase       string    `json:"phase"`
	OccurredAt  time.Time `json:"occurredAt"`
	EvidenceRef string    `json:"evidenceRef"`
}

// ExternalFleetDeploymentVerifier is intentionally dependency-inverted. The
// platform currently has no safe generic runtime client for this direct Nomad
// companion; accepting receipt booleans would weaken release admission. A
// Fleet-aware adapter must instead read live Nomad submission/allocation state,
// Consul health, HTTPS probes, artifact attestations/SBOM, and nonce variable
// write/read evidence. Nil is a fail-closed configuration.
type ExternalFleetDeploymentVerifier interface {
	VerifyExternalFleetDeployment(context.Context, ExternalFleetDeploymentVerificationRequest) (*ExternalFleetDeploymentVerification, error)
}

type ExternalFleetDeploymentVerificationRequest struct {
	Receipt ExternalFleetDeploymentReceipt
	CI      CIIdentity
	Config  ExternalFleetAdmissionConfig
}

type ExternalFleetAdmissionConfig struct {
	App                string
	Namespace          string
	MigrationJobID     string
	MigrationHCLSHA256 string
	RuntimeJobID       string
	RuntimeHCLSHA256   string
	BootstrapSignerRef string
}

// ExternalFleetDeploymentVerification carries only independently observed
// facts. It intentionally has no `healthy`/`approved` flag: every field must
// match the canonical request and verifier adapters must fail on missing proof.
type ExternalFleetDeploymentVerification struct {
	SourceSHA               string                        `json:"sourceSha"`
	Artifact                string                        `json:"artifact"`
	AttestationBundleSHA256 string                        `json:"attestationBundleSha256"`
	SBOMBundleSHA256        string                        `json:"sbomBundleSha256"`
	Namespace               string                        `json:"namespace"`
	Migration               ExternalFleetNomadJobProof    `json:"migration"`
	Runtime                 ExternalFleetNomadJobProof    `json:"runtime"`
	PlanID                  string                        `json:"planId"`
	ApplyRunID              string                        `json:"applyRunId"`
	ApplyRunAttempt         string                        `json:"applyRunAttempt"`
	PlanSHA256              string                        `json:"planSha256"`
	RunnerAttemptID         string                        `json:"runnerAttemptId"`
	FleetCommit             string                        `json:"fleetCommit"`
	NonceEvidenceRef        string                        `json:"nonceEvidenceRef"`
	Regions                 []ExternalFleetRegionProof    `json:"regions"`
	IngressNodeIDs          []string                      `json:"ingressNodeIds"`
	PublicHTTPSVersion      string                        `json:"publicHttpsVersion"`
	PrivateReadiness        ExternalFleetPrivateReadiness `json:"privateReadiness"`
	Chronology              []ExternalFleetChronologyStep `json:"chronology"`
}

// ExternalFleetPrivateReadiness preserves the independently observed private
// /readyz proof without misrepresenting the pilot's deliberately private
// readiness endpoint as public ingress evidence.
type ExternalFleetPrivateReadiness struct {
	Endpoint      string    `json:"endpoint"`
	AllocationIDs []string  `json:"allocationIds"`
	CheckedAt     time.Time `json:"checkedAt"`
}

type ExternalFleetRegionProof struct {
	Region        string `json:"region"`
	NomadRegion   string `json:"nomadRegion"`
	EvalID        string `json:"evalId"`
	DesiredWeight int    `json:"desiredWeight"`
	ActiveWeight  int    `json:"activeWeight"`
}

func (h *Handler) ConfigureExternalFleetDeploymentVerifier(verifier ExternalFleetDeploymentVerifier) {
	h.externalFleetDeploymentVerifier = verifier
}

// BeginExternalFleetDeploymentAdmission establishes the durable, retry-stable
// admission record and registers a hash-only nonce with the Norn-owned
// evidence service before disclosing the raw value to Fleet.
func (h *Handler) BeginExternalFleetDeploymentAdmission(w http.ResponseWriter, r *http.Request) {
	preventSensitiveResponseCaching(w)
	appID := chi.URLParam(r, "id")
	principal, ok := requireExternalFleetAdmissionScope(w, r, appID)
	if !ok {
		return
	}
	configured, err := h.externalFleetAdmissionConfig(appID)
	if err != nil {
		WriteControlProblem(w, r, http.StatusConflict, "external_deployment_unavailable", err.Error())
		return
	}
	registrar, ok := h.externalFleetDeploymentVerifier.(ExternalFleetEvidenceRegistrationClient)
	if h.db == nil || !ok {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "external_deployment_registration_unavailable", "Norn-owned evidence nonce registration is not configured")
		return
	}
	var request externalFleetAdmissionBeginRequest
	if err := decodeControlJSONLimit(w, r, &request, maxReleaseEvidenceJSONBody); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_external_deployment_request", err.Error())
		return
	}
	if err := h.validateExternalFleetLogicalIdentity(appID, configured, request.LogicalIdentity); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_external_deployment_identity", err.Error())
		return
	}
	key, digest, ok := externalFleetAdmissionIdentityIdempotency(w, r, principal, appID, request.LogicalIdentity)
	if !ok {
		return
	}
	admission, err := h.db.BeginExternalDeploymentAdmission(r.Context(), uuid.NewString(), key, digest, appID, principal.Environment, principal.CI.Repository)
	if errors.Is(err, store.ErrExternalDeploymentIdempotencyConflict) {
		WriteControlProblem(w, r, http.StatusConflict, "idempotency_key_reused", "Idempotency-Key was already used for a different external admission")
		return
	}
	if err != nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "external_deployment_store_unavailable", "durable external admission could not be started")
		return
	}
	if admission.State == store.ExternalDeploymentAdmissionCommitted || admission.State == store.ExternalDeploymentAdmissionCleanupPending || admission.State == store.ExternalDeploymentAdmissionComplete {
		writeJSON(w, externalFleetAdmissionResponse{SchemaVersion: "norn.external-fleet-admission/v4", AdmissionID: admission.ID, State: string(admission.State), OperationID: admission.OperationID})
		return
	}
	if admission.State == store.ExternalDeploymentAdmissionEvidenceClaimed || admission.State == store.ExternalDeploymentAdmissionExpired {
		WriteControlProblem(w, r, http.StatusConflict, "external_deployment_admission_unavailable", "admission cannot issue another nonce in its current state")
		return
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "external_deployment_nonce_unavailable", "secure nonce generation failed")
		return
	}
	nonce := externalAdmissionNonce{ID: uuid.NewString(), Secret: hex.EncodeToString(raw)}
	generation := admission.NonceGeneration + 1
	expiresAt := time.Now().UTC().Add(externalFleetAdmissionNonceTTL)
	registrationRef := admission.ID + ":" + fmt.Sprint(generation)
	stored := externalNonceStoreRecord(nonce, appID, h.cfg.EnvironmentID(), *principal.CI)
	stored.ExpiresAt, stored.AdmissionID, stored.RegistrationGeneration, stored.RegistrationRef = expiresAt, admission.ID, generation, registrationRef
	stored.IssuerSubject, stored.IssuerTokenID = principal.Subject, principal.TokenID
	stored.RegistrationMetadata = map[string]string{"logicalDigest": digest}
	if err := h.db.IssueExternalDeploymentNonce(r.Context(), stored); err != nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "external_deployment_nonce_unavailable", "durable nonce registration could not be started")
		return
	}
	registration := ExternalFleetEvidenceRegistration{AdmissionID: admission.ID, LogicalDigest: digest, NonceSHA256: nonce.sha256(), Generation: generation, ExpiresAt: expiresAt}
	if err := registrar.RegisterExternalFleetNonce(r.Context(), registration); err != nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "external_deployment_registration_unavailable", "evidence nonce registration failed before disclosure")
		return
	}
	if err := h.db.MarkExternalDeploymentNonceReady(r.Context(), admission.ID, nonce.ID, generation, registrationRef); err != nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "external_deployment_nonce_unavailable", "durable nonce registration could not be completed")
		return
	}
	writeJSONStatus(w, http.StatusCreated, externalFleetAdmissionResponse{SchemaVersion: "norn.external-fleet-admission/v4", AdmissionID: admission.ID, Nonce: nonce.String(), ExpiresAt: expiresAt, State: string(store.ExternalDeploymentAdmissionNonceReady)})
}

// AdmitExternalFleetDeploymentV4 is deliberately a separate endpoint even
// though it reuses the verifier's receipt path; v4 requires a prior durable
// registration and cannot fall back to v3 nonce issuance.
func (h *Handler) AdmitExternalFleetDeploymentV4(w http.ResponseWriter, r *http.Request) {
	h.AdmitExternalFleetDeployment(w, r)
}

// GetExternalFleetDeploymentAdmissionContext exposes server-owned progress;
// it never accepts a receipt echo as evidence of lineage or chronology.
func (h *Handler) GetExternalFleetDeploymentAdmissionContext(w http.ResponseWriter, r *http.Request) {
	preventSensitiveResponseCaching(w)
	appID := chi.URLParam(r, "id")
	if _, ok := requireExternalFleetAdmissionScope(w, r, appID); !ok {
		return
	}
	if h.db == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "external_deployment_store_unavailable", "durable external admission storage is unavailable")
		return
	}
	admissionID := chi.URLParam(r, "admissionId")
	if uuid.Validate(admissionID) != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_external_deployment_admission", "admissionId must be a UUID")
		return
	}
	var state, operationID string
	err := h.db.Pool.QueryRow(r.Context(), `SELECT state, COALESCE(operation_id,'') FROM external_deployment_admissions WHERE id=$1 AND app=$2`, admissionID, appID).Scan(&state, &operationID)
	if err != nil {
		WriteControlProblem(w, r, http.StatusConflict, "external_deployment_admission_missing", "server-owned admission context is unavailable")
		return
	}
	checkpoints := []string{}
	rows, queryErr := h.db.Pool.Query(r.Context(), `SELECT checkpoint_id FROM external_deployment_admission_checkpoints WHERE admission_id=$1 ORDER BY phase`, admissionID)
	if queryErr != nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "external_deployment_context_unavailable", "server-owned checkpoint context is unavailable")
		return
	}
	for rows.Next() {
		var checkpoint string
		if err := rows.Scan(&checkpoint); err != nil {
			rows.Close()
			WriteControlProblem(w, r, http.StatusServiceUnavailable, "external_deployment_context_unavailable", "server-owned checkpoint context is invalid")
			return
		}
		checkpoints = append(checkpoints, checkpoint)
	}
	rows.Close()
	// The response is deliberately derived from durable references, never a
	// caller-supplied receipt. A missing or duplicate external phase is a
	// conflict rather than an inferred successful recovery.
	if (state == string(store.ExternalDeploymentAdmissionCommitted) || state == string(store.ExternalDeploymentAdmissionCleanupPending) || state == string(store.ExternalDeploymentAdmissionComplete)) && len(checkpoints) != 2 {
		WriteControlProblem(w, r, http.StatusConflict, "external_deployment_context_drift", "durable external admission checkpoint lineage is incomplete")
		return
	}
	cleanup := "pending"
	if state == string(store.ExternalDeploymentAdmissionComplete) {
		cleanup = "complete"
	}
	writeJSON(w, externalFleetAdmissionContextResponse{AdmissionID: admissionID, State: state, OperationID: operationID, CleanupState: cleanup, RetryLineage: []string{}, Checkpoints: checkpoints})
}

// AdmitExternalFleetDeployment either issues a one-time Norn nonce or admits
// one independently verified staging receipt. It is a separate route/scope so
// normal DiscoverApps and release deployment behavior stay unchanged.
func (h *Handler) AdmitExternalFleetDeployment(w http.ResponseWriter, r *http.Request) {
	preventSensitiveResponseCaching(w)
	appID := chi.URLParam(r, "id")
	principal, ok := requireExternalFleetAdmissionScope(w, r, appID)
	if !ok {
		return
	}
	configured, err := h.externalFleetAdmissionConfig(appID)
	if err != nil {
		WriteControlProblem(w, r, http.StatusConflict, "external_deployment_unavailable", err.Error())
		return
	}
	if h.db == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "external_deployment_store_unavailable", "durable external deployment admission storage is unavailable")
		return
	}
	// A nonce is authority to submit a receipt, so issuance is not allowed
	// until the same independent verifier that will consume it is live.
	if h.externalFleetDeploymentVerifier == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "external_deployment_verifier_unavailable", "independent Fleet runtime verification is not configured; nonce issuance is disabled")
		return
	}
	var request externalDeploymentRequest
	if err := decodeControlJSONLimit(w, r, &request, maxReleaseEvidenceJSONBody); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_external_deployment_request", err.Error())
		return
	}
	if strings.TrimSpace(request.Action) == "issue-nonce" && request.Receipt == nil {
		h.issueExternalFleetAdmissionNonce(w, r, appID, principal)
		return
	}
	if request.Action != "" || request.Receipt == nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_external_deployment_request", "use action issue-nonce without a receipt, or submit exactly one receipt")
		return
	}
	if h.externalFleetDeploymentVerifier == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "external_deployment_verifier_unavailable", "independent Fleet runtime verification is not configured; receipt assertions are not accepted")
		return
	}
	receipt := *request.Receipt
	isV4 := receipt.SchemaVersion == externalFleetReceiptSchemaV4
	if isV4 {
		identity := ExternalFleetLogicalIdentity{SchemaVersion: externalFleetLogicalIdentitySchemaV4, SourceSHA: receipt.SourceSHA, Artifact: receipt.Artifact, Candidate: receipt.Candidate, Fleet: ExternalFleetLogicalExecutionIdentity{Namespace: receipt.Fleet.Namespace, PlanID: receipt.Fleet.PlanID, PlanSHA256: receipt.Fleet.PlanSHA256, RootAttemptID: receipt.Fleet.RootAttemptID, FleetCommit: receipt.Fleet.FleetCommit}}
		if err := h.validateExternalFleetLogicalIdentity(appID, configured, identity); err != nil || !validExternalFleetReceiptV4(receipt, configured, appID) {
			WriteControlProblem(w, r, http.StatusBadRequest, "invalid_external_deployment_receipt", "v4 receipt has invalid immutable identity, proof, or chronology")
			return
		}
	} else if err := validateExternalFleetReceipt(receipt, configured, appID); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_external_deployment_receipt", err.Error())
		return
	}
	spec := h.findExternalAdmissionSpec(appID)
	if spec == nil || spec.Deploy || spec.Repo == nil {
		WriteControlProblem(w, r, http.StatusConflict, "external_deployment_app_invalid", "external admission requires the configured deploy:false app with a server-owned repository")
		return
	}
	if err := validateReleaseSpecBinding(spec, receipt.Candidate, receipt.Artifact, h.pipelineRegistryURL()); err != nil || receipt.Candidate.Attestation.MaterialSHA != receipt.SourceSHA || !validExternalBootstrapCandidate(receipt.Candidate, receipt.SourceSHA, receipt.Artifact, h.releaseTrustMode(), configured) {
		WriteControlProblem(w, r, http.StatusForbidden, "external_deployment_binding_mismatch", "external receipt source, artifact, and candidate do not match the server-owned app binding")
		return
	}
	nonce, err := externalAdmissionNonceFromReceipt(receipt.Nonce)
	if err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "external_deployment_nonce_invalid", err.Error())
		return
	}
	if externalValueContainsNonce(redactExternalFleetReceipt(receipt), nonce) {
		WriteControlProblem(w, r, http.StatusBadRequest, "external_deployment_nonce_leak", "receipt fields must not contain the raw Norn nonce or its secret")
		return
	}
	if !externalReceiptMatchesCI(receipt, *principal.CI) {
		WriteControlProblem(w, r, http.StatusForbidden, "external_deployment_identity_denied", "receipt apply run and attempt must match the authenticated Fleet identity")
		return
	}
	key, digest, ok := externalFleetAdmissionIdempotency(w, r, principal, appID, receipt)
	if !ok {
		return
	}
	if existing, handled := h.resolveAppOperationIdempotency(w, r, key, digest, "app.deploy", appID); handled {
		if existing != nil {
			existing.AttachReceipt()
			writeJSON(w, existing)
		}
		return
	}
	var admission *store.ExternalDeploymentAdmissionLifecycle
	if isV4 {
		var beginErr error
		admission, beginErr = h.db.BeginExternalDeploymentAdmission(r.Context(), receipt.AdmissionID, key, digest, appID, principal.Environment, principal.CI.Repository)
		if errors.Is(beginErr, store.ErrExternalDeploymentIdempotencyConflict) {
			WriteControlProblem(w, r, http.StatusConflict, "external_deployment_admission_conflict", "receipt admission does not match the server-owned logical identity")
			return
		}
		if beginErr != nil || admission == nil {
			WriteControlProblem(w, r, http.StatusServiceUnavailable, "external_deployment_store_unavailable", "durable external admission is unavailable")
			return
		}
		if admission.ID != receipt.AdmissionID {
			WriteControlProblem(w, r, http.StatusConflict, "external_deployment_admission_conflict", "receipt admission does not match the server-owned logical identity")
			return
		}
		if admission.State == store.ExternalDeploymentAdmissionCommitted || admission.State == store.ExternalDeploymentAdmissionCleanupPending || admission.State == store.ExternalDeploymentAdmissionComplete {
			if operation, err := h.db.GetOperation(r.Context(), admission.OperationID); err == nil {
				operation.AttachReceipt()
				writeJSON(w, operation)
				return
			}
			WriteControlProblem(w, r, http.StatusConflict, "external_deployment_admission_conflict", "terminal admission operation is unavailable")
			return
		}
		registrar, registrationOK := h.externalFleetDeploymentVerifier.(ExternalFleetEvidenceRegistrationClient)
		if !registrationOK || admission.State != store.ExternalDeploymentAdmissionNonceReady || admission.NonceID != nonce.ID || admission.NonceGeneration < 1 {
			WriteControlProblem(w, r, http.StatusConflict, "external_deployment_admission_unavailable", "receipt nonce is not a ready server-registered admission nonce")
			return
		}
		if err := h.db.ClaimExternalDeploymentAdmissionEvidence(r.Context(), admission.ID, nonce.ID); err != nil {
			WriteControlProblem(w, r, http.StatusConflict, "external_deployment_nonce_consumed", "server-registered nonce is already claimed or unavailable")
			return
		}
		if err := registrar.ClaimExternalFleetNonce(r.Context(), ExternalFleetEvidenceClaim{AdmissionID: admission.ID, NonceSHA256: nonce.sha256(), Generation: admission.NonceGeneration}); err != nil {
			WriteControlProblem(w, r, http.StatusServiceUnavailable, "external_deployment_registration_unavailable", "evidence nonce claim failed")
			return
		}
	}
	verification, err := h.externalFleetDeploymentVerifier.VerifyExternalFleetDeployment(r.Context(), ExternalFleetDeploymentVerificationRequest{Receipt: receipt, CI: *principal.CI, Config: configured})
	if err != nil || verification == nil {
		message := "independent Fleet runtime verification failed"
		if err != nil {
			message += ": " + safeExternalVerificationError(err, nonce)
		}
		WriteControlProblem(w, r, http.StatusForbidden, "external_deployment_verification_failed", message)
		return
	}
	if externalValueContainsNonce(*verification, nonce) {
		WriteControlProblem(w, r, http.StatusForbidden, "external_deployment_verification_failed", "independent verification returned raw nonce material")
		return
	}
	if err := verificationMatchesExternalReceipt(*verification, receipt, configured); err != nil {
		WriteControlProblem(w, r, http.StatusForbidden, "external_deployment_verification_failed", err.Error())
		return
	}
	now := time.Now().UTC()
	deploymentID := uuid.NewString()
	deployment := &model.Deployment{ID: deploymentID, App: appID, CommitSHA: receipt.SourceSHA, ImageTag: receipt.Artifact, Environment: "staging", SagaID: "external-fleet:" + nonce.ID, Status: model.StatusDeployed, SourceKind: "external-fleet", SourceRef: receipt.SourceSHA, StartedAt: now, FinishedAt: &now}
	redactedReceipt := redactExternalFleetReceipt(receipt)
	canonicalReceipt, canonicalErr := externalReceiptCanonicalJSON(receipt)
	if canonicalErr != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "external_deployment_store_failed", "failed to canonicalize verified external deployment evidence")
		return
	}
	receiptHash := sha256.Sum256(canonicalReceipt)
	metadata := map[string]interface{}{"idempotencyKey": key, "requestDigest": digest, "principal": principal.Subject, "principalTokenId": principal.TokenID, "requestCI": principal.CI, "environment": "staging", "candidate": receipt.Candidate, "externalFleetProof": redactedReceipt, "externalFleetProofSHA256": hex.EncodeToString(receiptHash[:]), "externalFleetVerification": verification, "nonceID": nonce.ID, "nonceSHA256": nonce.sha256()}
	op := &model.Operation{ID: uuid.NewString(), Kind: "app.deploy", App: appID, SagaID: deployment.SagaID, Ref: receipt.SourceSHA, Status: model.OperationSucceeded, Risk: "externally executed staging workload", Source: "external-fleet-admission", Message: "independently verified external Fleet deployment admitted", Payload: map[string]interface{}{"deploymentId": deploymentID, "app": appID, "sourceSha": receipt.SourceSHA, "artifact": receipt.Artifact, "candidate": receipt.Candidate}, Metadata: metadata, StartedAt: now, FinishedAt: &now, MaxAttempts: 1}
	regions, err := externalDeploymentRegions(spec.ResolvedRegions(), verification.Regions)
	if err != nil {
		WriteControlProblem(w, r, http.StatusForbidden, "external_deployment_verification_failed", err.Error())
		return
	}
	storedNonce := externalNonceStoreRecord(nonce, appID, h.cfg.EnvironmentID(), *principal.CI)
	if admission != nil {
		storedNonce.AdmissionID, storedNonce.RegistrationGeneration = admission.ID, admission.NonceGeneration
	}
	result, err := h.db.AdmitExternalDeployment(r.Context(), store.ExternalDeploymentAdmission{Nonce: storedNonce, Deployment: deployment, Regions: regions, Operation: op, IdempotencyKey: key, RequestDigest: digest, AdmissionID: func() string {
		if admission != nil {
			return admission.ID
		}
		return ""
	}(), NonceGeneration: func() int64 {
		if admission != nil {
			return admission.NonceGeneration
		}
		return 0
	}(), CheckpointRefs: externalAdmissionCheckpointRefs(receipt)})
	if err != nil {
		switch {
		case errors.Is(err, store.ErrExternalDeploymentNonceConsumed):
			WriteControlProblem(w, r, http.StatusConflict, "external_deployment_nonce_consumed", "Norn nonce is expired, belongs to another protected Fleet run, or was already consumed")
		case errors.Is(err, store.ErrExternalDeploymentIdempotencyConflict):
			WriteControlProblem(w, r, http.StatusConflict, "idempotency_key_reused", "Idempotency-Key was already used for a different app operation")
		default:
			WriteControlProblem(w, r, http.StatusServiceUnavailable, "external_deployment_store_unavailable", "durable external deployment admission could not be committed")
		}
		return
	}
	if result.Replayed {
		result.Operation.AttachReceipt()
		writeJSON(w, result.Operation)
		return
	}
	if admission != nil {
		registrar := h.externalFleetDeploymentVerifier.(ExternalFleetEvidenceRegistrationClient)
		claim := ExternalFleetEvidenceClaim{AdmissionID: admission.ID, NonceSHA256: nonce.sha256(), Generation: admission.NonceGeneration}
		if err := registrar.CommitExternalFleetNonce(r.Context(), claim); err != nil {
			_ = h.db.MarkExternalDeploymentAdmissionCleanupPending(r.Context(), admission.ID)
			WriteControlProblem(w, r, http.StatusServiceUnavailable, "external_deployment_cleanup_pending", "deployment committed but evidence nonce cleanup is pending; retry the exact request")
			return
		}
		_ = h.db.CompleteExternalDeploymentAdmission(r.Context(), admission.ID)
	}
	op.AttachReceipt()
	w.Header().Set("Location", "/api/v1/operations/"+op.ID)
	writeJSONStatus(w, http.StatusCreated, op)
}

func externalReceiptMatchesCI(receipt ExternalFleetDeploymentReceipt, ci CIIdentity) bool {
	return receipt.Fleet.ApplyRunID == ci.RunID && receipt.Fleet.ApplyRunAttempt == ci.RunAttempt
}

// externalFleetAdmissionIdempotency deliberately excludes token JTI, raw nonce,
// and the current GitHub run. A lost terminal response can therefore be replayed
// after an OIDC/token/nonce rotation, while a changed logical deployment cannot.
func externalFleetAdmissionIdempotency(w http.ResponseWriter, r *http.Request, principal AccessPrincipal, appID string, receipt ExternalFleetDeploymentReceipt) (string, string, bool) {
	identity := ExternalFleetLogicalIdentity{SchemaVersion: externalFleetLogicalIdentitySchemaV4, SourceSHA: receipt.SourceSHA, Artifact: receipt.Artifact, Candidate: receipt.Candidate, Fleet: ExternalFleetLogicalExecutionIdentity{Namespace: receipt.Fleet.Namespace, PlanID: receipt.Fleet.PlanID, PlanSHA256: receipt.Fleet.PlanSHA256, RootAttemptID: receipt.Fleet.RootAttemptID, FleetCommit: receipt.Fleet.FleetCommit}}
	return externalFleetAdmissionIdentityIdempotency(w, r, principal, appID, identity)
}

func externalFleetAdmissionIdentityIdempotency(w http.ResponseWriter, r *http.Request, principal AccessPrincipal, appID string, identity ExternalFleetLogicalIdentity) (string, string, bool) {
	clientKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if clientKey == "" || len(clientKey) > 200 || principal.CI == nil || principal.CI.Repository == "" || principal.Environment == "" {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_idempotency_key", "a stable Idempotency-Key and authorized CI repository/environment are required")
		return "", "", false
	}
	keySum := sha256.Sum256([]byte("external-fleet-admission\x00" + principal.CI.Repository + "\x00" + principal.Environment + "\x00" + appID + "\x00" + clientKey))
	logical := struct {
		Repository    string                        `json:"repository"`
		Environment   string                        `json:"environment"`
		App           string                        `json:"app"`
		Candidate     externalFleetLogicalCandidate `json:"candidate"`
		SourceSHA     string                        `json:"sourceSha"`
		Artifact      string                        `json:"artifact"`
		Namespace     string                        `json:"namespace"`
		PlanID        string                        `json:"planId"`
		PlanSHA256    string                        `json:"planSha256"`
		RootAttemptID string                        `json:"rootAttemptId"`
		FleetCommit   string                        `json:"fleetCommit"`
	}{principal.CI.Repository, principal.Environment, appID, externalFleetReplayCandidate(identity.Candidate), identity.SourceSHA, identity.Artifact, identity.Fleet.Namespace, identity.Fleet.PlanID, identity.Fleet.PlanSHA256, identity.Fleet.RootAttemptID, identity.Fleet.FleetCommit}
	canonical, err := json.Marshal(logical)
	if err != nil {
		return "", "", false
	}
	digest := sha256.Sum256(canonical)
	return "app.deploy:" + hex.EncodeToString(keySum[:]), "sha256:" + hex.EncodeToString(digest[:]), true
}

func (h *Handler) validateExternalFleetLogicalIdentity(appID string, configured ExternalFleetAdmissionConfig, identity ExternalFleetLogicalIdentity) error {
	if identity.SchemaVersion != externalFleetLogicalIdentitySchemaV4 || !fullSourceSHAPattern.MatchString(identity.SourceSHA) || !model.IsContentAddressedImage(identity.Artifact) || identity.Fleet.Namespace != configured.Namespace || !externalNamePattern.MatchString(identity.Fleet.PlanID) || !sha256HexPattern.MatchString(identity.Fleet.PlanSHA256) || !externalNamePattern.MatchString(identity.Fleet.RootAttemptID) || !fullSourceSHAPattern.MatchString(identity.Fleet.FleetCommit) {
		return fmt.Errorf("logical identity is incomplete or has invalid immutable Fleet fields")
	}
	spec := h.findExternalAdmissionSpec(appID)
	if spec == nil || spec.Deploy || spec.Repo == nil {
		return fmt.Errorf("external admission requires the configured deploy:false app with a server-owned repository")
	}
	if err := validateReleaseSpecBinding(spec, identity.Candidate, identity.Artifact, h.pipelineRegistryURL()); err != nil || identity.Candidate.Attestation.MaterialSHA != identity.SourceSHA || !validExternalBootstrapCandidate(identity.Candidate, identity.SourceSHA, identity.Artifact, h.releaseTrustMode(), configured) {
		return fmt.Errorf("logical identity source, artifact, and candidate do not match the server-owned app binding")
	}
	return nil
}

// externalFleetLogicalCandidate deliberately excludes the ephemeral GitHub
// run/attempt and attestation URLs. Those are re-authorized evidence for a
// retry, not the immutable deployment identity used to recover a lost result.
type externalFleetLogicalCandidate struct {
	Provider, Repository, RepositoryID, OwnerID, RepositoryVisibility   string
	WorkflowRef, WorkflowSHA, SignerWorkflowRef, SignerWorkflowSHA, Ref string
	Mode, Issuer, SubjectDigest, MaterialSHA                            string
}

func externalFleetReplayCandidate(c model.ReleaseCandidate) externalFleetLogicalCandidate {
	return externalFleetLogicalCandidate{c.Provider, c.Repository, c.RepositoryID, c.OwnerID, c.RepositoryVisibility, c.WorkflowRef, c.WorkflowSHA, c.SignerWorkflowRef, c.SignerWorkflowSHA, c.Ref, c.Attestation.Mode, c.Attestation.Issuer, c.Attestation.SubjectDigest, c.Attestation.MaterialSHA}
}

func (h *Handler) issueExternalFleetAdmissionNonce(w http.ResponseWriter, r *http.Request, appID string, principal AccessPrincipal) {
	if h.db == nil || h.cfg == nil || principal.CI == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "external_deployment_nonce_unavailable", "durable Norn nonce issuance is unavailable")
		return
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "external_deployment_nonce_unavailable", "failed to generate a Norn nonce")
		return
	}
	nonce := externalAdmissionNonce{ID: uuid.NewString(), Secret: hex.EncodeToString(raw)}
	expiresAt := time.Now().UTC().Add(externalFleetAdmissionNonceTTL)
	if err := h.db.IssueExternalDeploymentNonce(r.Context(), store.ExternalDeploymentNonce{ID: nonce.ID, NonceSHA256: nonce.sha256(), App: appID, Environment: h.cfg.EnvironmentID(), CIRepository: principal.CI.Repository, CIRunID: principal.CI.RunID, CIRunAttempt: principal.CI.RunAttempt, ExpiresAt: expiresAt}); err != nil {
		if errors.Is(err, store.ErrExternalDeploymentNonceLimit) {
			WriteControlProblem(w, r, http.StatusConflict, "external_deployment_nonce_limit", "the protected Fleet run already has the maximum outstanding admission nonces")
			return
		}
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "external_deployment_nonce_unavailable", "failed to durably issue the Norn nonce")
		return
	}
	writeJSONStatus(w, http.StatusCreated, map[string]interface{}{"schemaVersion": "norn.external-fleet-admission-nonce/v1", "nonce": nonce.String(), "expiresAt": expiresAt.Format(time.RFC3339), "app": appID, "environment": h.cfg.EnvironmentID()})
}

func (h *Handler) externalFleetAdmissionConfig(appID string) (ExternalFleetAdmissionConfig, error) {
	if h == nil || h.cfg == nil || (h.cfg.EnvironmentID() != "staging" && h.cfg.EnvironmentID() != "production") || h.cfg.ExternalFleetAdmissionApp == "" || h.cfg.ExternalFleetAdmissionApp != appID || !externalNamePattern.MatchString(h.cfg.ExternalFleetAdmissionNamespace) || !externalNamePattern.MatchString(h.cfg.ExternalFleetAdmissionMigrationJobID) || !externalNamePattern.MatchString(h.cfg.ExternalFleetAdmissionRuntimeJobID) || h.cfg.ExternalFleetAdmissionMigrationJobID == h.cfg.ExternalFleetAdmissionRuntimeJobID || !sha256HexPattern.MatchString(h.cfg.ExternalFleetAdmissionMigrationHCLSHA256) || !sha256HexPattern.MatchString(h.cfg.ExternalFleetAdmissionRuntimeHCLSHA256) || h.cfg.ExternalFleetAdmissionMigrationHCLSHA256 == h.cfg.ExternalFleetAdmissionRuntimeHCLSHA256 || !validExternalBootstrapSignerRef(h.cfg.ExternalFleetAdmissionBootstrapSignerRef) || stringInSlice(h.cfg.ExternalFleetAdmissionBootstrapSignerRef, h.cfg.ReleaseAttestationWorkflowRefs) {
		return ExternalFleetAdmissionConfig{}, fmt.Errorf("external Fleet admission is disabled or its exact staging app/migration/runtime/bootstrap-signer binding is incomplete")
	}
	return ExternalFleetAdmissionConfig{App: appID, Namespace: h.cfg.ExternalFleetAdmissionNamespace, MigrationJobID: h.cfg.ExternalFleetAdmissionMigrationJobID, MigrationHCLSHA256: h.cfg.ExternalFleetAdmissionMigrationHCLSHA256, RuntimeJobID: h.cfg.ExternalFleetAdmissionRuntimeJobID, RuntimeHCLSHA256: h.cfg.ExternalFleetAdmissionRuntimeHCLSHA256, BootstrapSignerRef: h.cfg.ExternalFleetAdmissionBootstrapSignerRef}, nil
}

func validExternalBootstrapSignerRef(value string) bool {
	path, sha, found := strings.Cut(strings.TrimSpace(value), "@")
	return found && strings.HasSuffix(path, ".github/workflows/hello-norn-mysql-bootstrap-image.yml") && fullSourceSHAPattern.MatchString(sha)
}

func (h *Handler) findExternalAdmissionSpec(appID string) *model.InfraSpec {
	if h == nil || h.cfg == nil {
		return nil
	}
	specs, err := model.DiscoverAllApps(h.cfg.AppsDir)
	if err != nil {
		return nil
	}
	for _, spec := range specs {
		if spec.App == appID {
			return spec
		}
	}
	return nil
}

func (h *Handler) pipelineRegistryURL() string {
	if h != nil && h.pipeline != nil {
		return h.pipeline.RegistryURL
	}
	return ""
}

func (h *Handler) releaseTrustMode() string {
	if h != nil && h.cfg != nil {
		return h.cfg.ReleaseAttestationTrustMode
	}
	return ""
}

func requireExternalFleetAdmissionScope(w http.ResponseWriter, r *http.Request, app string) (AccessPrincipal, bool) {
	principal, exists := AccessPrincipalFromRequest(r)
	if !exists || principal.CI == nil || principal.Legacy || principal.Allows(ScopeAdmin) || !principal.Allows(ScopeFleetExternalAdmission) || principal.App != app || principal.Environment != "staging" || principal.CI.Environment != "staging" || !principal.CI.RefProtected || (principal.CI.Intent != "apply" && principal.CI.Intent != "recover") {
		WriteControlProblem(w, r, http.StatusForbidden, "external_deployment_identity_denied", "external deployment admission requires the exact scoped protected staging Fleet apply or recover identity")
		return AccessPrincipal{}, false
	}
	return principal, true
}

type externalAdmissionNonce struct{ ID, Secret string }

func (n externalAdmissionNonce) String() string { return n.ID + "." + n.Secret }
func (n externalAdmissionNonce) sha256() string {
	sum := sha256.Sum256([]byte(n.String()))
	return hex.EncodeToString(sum[:])
}

func externalAdmissionNonceFromReceipt(value string) (externalAdmissionNonce, error) {
	id, secret, ok := strings.Cut(strings.TrimSpace(value), ".")
	if !ok || uuid.Validate(id) != nil || len(secret) != 64 || !sha256HexPattern.MatchString(secret) {
		return externalAdmissionNonce{}, fmt.Errorf("nonce must be an issued UUID plus 32-byte lowercase secret")
	}
	return externalAdmissionNonce{ID: id, Secret: secret}, nil
}

func externalNonceStoreRecord(nonce externalAdmissionNonce, app, environment string, ci CIIdentity) store.ExternalDeploymentNonce {
	return store.ExternalDeploymentNonce{ID: nonce.ID, NonceSHA256: nonce.sha256(), App: app, Environment: environment, CIRepository: ci.Repository, CIRunID: ci.RunID, CIRunAttempt: ci.RunAttempt}
}

func validateExternalFleetReceipt(receipt ExternalFleetDeploymentReceipt, configured ExternalFleetAdmissionConfig, app string) error {
	return validateExternalFleetReceiptAt(receipt, configured, app, time.Now().UTC())
}

// validateExternalFleetReceiptAt keeps the nonce-window freshness decision on
// the same injected clock as the rest of external-admission verification.
func validateExternalFleetReceiptAt(receipt ExternalFleetDeploymentReceipt, configured ExternalFleetAdmissionConfig, app string, now time.Time) error {
	if receipt.SchemaVersion != externalFleetReceiptSchema || receipt.App != app || !fullSourceSHAPattern.MatchString(receipt.SourceSHA) || !model.IsContentAddressedImage(receipt.Artifact) || !sha256HexPattern.MatchString(receipt.AttestationBundleSHA256) || !sha256HexPattern.MatchString(receipt.SBOMBundleSHA256) || receipt.AttestationBundleSHA256 == receipt.SBOMBundleSHA256 {
		return fmt.Errorf("receipt schema, app, immutable source/artifact, or evidence references are invalid")
	}
	if receipt.Fleet.Namespace != configured.Namespace || !validExternalNomadJobProof(receipt.Fleet.Migration, configured.MigrationJobID, configured.MigrationHCLSHA256) || !validExternalNomadJobProof(receipt.Fleet.Runtime, configured.RuntimeJobID, configured.RuntimeHCLSHA256) || receipt.Fleet.Migration.EvalID == receipt.Fleet.Runtime.EvalID || receipt.Fleet.Migration.CheckpointID == receipt.Fleet.Runtime.CheckpointID || !externalNamePattern.MatchString(receipt.Fleet.PlanID) || !validGitHubNumericID(receipt.Fleet.ApplyRunID) || !validGitHubNumericID(receipt.Fleet.ApplyRunAttempt) || !sha256HexPattern.MatchString(receipt.Fleet.PlanSHA256) || !externalNamePattern.MatchString(receipt.Fleet.RunnerAttemptID) || !externalNamePattern.MatchString(receipt.Fleet.RootAttemptID) || !externalNamePattern.MatchString(receipt.Fleet.NonceEvidenceRef) {
		return fmt.Errorf("receipt Fleet namespace/migration/runtime/run/plan/attempt/nonce evidence binding is invalid")
	}
	if !validExternalChronologyAt(receipt.Chronology, now) {
		return fmt.Errorf("receipt must carry ordered prepare, migration, runtime, and exercise evidence")
	}
	return nil
}

func validExternalFleetReceiptV4(receipt ExternalFleetDeploymentReceipt, configured ExternalFleetAdmissionConfig, app string) bool {
	if receipt.SchemaVersion != externalFleetReceiptSchemaV4 || uuid.Validate(receipt.AdmissionID) != nil {
		return false
	}
	legacy := receipt
	legacy.SchemaVersion = externalFleetReceiptSchema
	if validateExternalFleetReceipt(legacy, configured, app) != nil || !fullSourceSHAPattern.MatchString(receipt.Fleet.FleetCommit) {
		return false
	}
	return validExternalNomadJobProofV4(receipt.Fleet.Migration, configured.MigrationJobID, configured.MigrationHCLSHA256) && validExternalNomadJobProofV4(receipt.Fleet.Runtime, configured.RuntimeJobID, configured.RuntimeHCLSHA256)
}

func verificationMatchesExternalReceipt(verified ExternalFleetDeploymentVerification, receipt ExternalFleetDeploymentReceipt, configured ExternalFleetAdmissionConfig) error {
	return verificationMatchesExternalReceiptAt(verified, receipt, configured, time.Now().UTC())
}

func verificationMatchesExternalReceiptAt(verified ExternalFleetDeploymentVerification, receipt ExternalFleetDeploymentReceipt, configured ExternalFleetAdmissionConfig, now time.Time) error {
	if verified.SourceSHA != receipt.SourceSHA || verified.Artifact != receipt.Artifact || verified.AttestationBundleSHA256 != receipt.AttestationBundleSHA256 || verified.SBOMBundleSHA256 != receipt.SBOMBundleSHA256 || verified.Namespace != configured.Namespace || !reflect.DeepEqual(verified.Migration, receipt.Fleet.Migration) || !reflect.DeepEqual(verified.Runtime, receipt.Fleet.Runtime) || verified.PlanID != receipt.Fleet.PlanID || verified.ApplyRunID != receipt.Fleet.ApplyRunID || verified.ApplyRunAttempt != receipt.Fleet.ApplyRunAttempt || verified.PlanSHA256 != receipt.Fleet.PlanSHA256 || verified.RunnerAttemptID != receipt.Fleet.RunnerAttemptID || verified.FleetCommit != receipt.Fleet.FleetCommit || verified.NonceEvidenceRef != receipt.Fleet.NonceEvidenceRef {
		return fmt.Errorf("independent verifier observations do not exactly match the receipt")
	}
	if !validDistinctIngressNodes(verified.IngressNodeIDs) || !validHTTPSVersion(verified.PublicHTTPSVersion) || !validPrivateReadinessAt(verified.PrivateReadiness, now) || !sameExternalChronologyAt(verified.Chronology, receipt.Chronology, now) {
		return fmt.Errorf("independent verifier did not prove two distinct ingress nodes, public HTTPS version, private readiness, and full chronology")
	}
	return nil
}

func validExternalNomadJobProof(proof ExternalFleetNomadJobProof, jobID, hclSHA256 string) bool {
	return proof.JobID == jobID && proof.HCLSHA256 == hclSHA256 && uuid.Validate(proof.EvalID) == nil && proof.JobModifyIndex > 0 && externalNamePattern.MatchString(proof.CheckpointID)
}

func validExternalNomadJobProofV4(proof ExternalFleetNomadJobProof, jobID, hclSHA256 string) bool {
	if !validExternalNomadJobProof(proof, jobID, hclSHA256) || proof.EvalCreateIndex == 0 || proof.EvalJobModifyIndex == 0 || proof.JobCreateIndex == 0 || proof.JobVersion == 0 || !sha256HexPattern.MatchString(proof.CurrentSpecSHA256) || !sha256HexPattern.MatchString(proof.SubmissionSHA256) || len(proof.EvaluationChainIDs) == 0 || len(proof.EvaluationChainIDs) > 32 {
		return false
	}
	seen := map[string]struct{}{}
	for _, id := range proof.EvaluationChainIDs {
		if uuid.Validate(id) != nil {
			return false
		}
		if _, duplicate := seen[id]; duplicate {
			return false
		}
		seen[id] = struct{}{}
	}
	return proof.EvaluationChainIDs[0] == proof.EvalID
}

func externalAdmissionCheckpointRefs(receipt ExternalFleetDeploymentReceipt) []store.ExternalDeploymentCheckpointRef {
	if receipt.SchemaVersion != externalFleetReceiptSchemaV4 {
		return nil
	}
	return []store.ExternalDeploymentCheckpointRef{
		{Phase: "external_admission", CheckpointID: receipt.Fleet.Migration.CheckpointID, AttemptID: receipt.Fleet.RootAttemptID, EvidenceRef: receipt.Fleet.NonceEvidenceRef, EvidenceSHA256: receipt.Fleet.Migration.SubmissionSHA256},
		{Phase: "external_cleanup", CheckpointID: receipt.Fleet.Runtime.CheckpointID, AttemptID: receipt.Fleet.RootAttemptID, EvidenceRef: receipt.Fleet.NonceEvidenceRef, EvidenceSHA256: receipt.Fleet.Runtime.SubmissionSHA256},
	}
}

func validExternalBootstrapCandidate(candidate model.ReleaseCandidate, sourceSHA, artifact, trustMode string, configured ExternalFleetAdmissionConfig) bool {
	return validReleaseCandidateForTrust(candidate, sourceSHA, artifact, trustMode) && candidate.SignerWorkflowRef == configured.BootstrapSignerRef && candidate.SignerWorkflowSHA == configured.BootstrapSignerRef[strings.LastIndex(configured.BootstrapSignerRef, "@")+1:]
}

func redactExternalFleetReceipt(receipt ExternalFleetDeploymentReceipt) ExternalFleetDeploymentReceipt {
	receipt.Nonce = ""
	return receipt
}

// externalValueContainsNonce walks every caller-controlled nested string,
// including DSSE fields and evidence references, before any value can enter a
// durable operation or response. The nonce field itself is cleared first.
func externalValueContainsNonce(value interface{}, nonce externalAdmissionNonce) bool {
	return externalValueContainsNonceAt(reflect.ValueOf(value), nonce.String(), nonce.Secret, 0)
}

func externalValueContainsNonceAt(value reflect.Value, raw, secret string, depth int) bool {
	if !value.IsValid() || depth > 32 {
		return false
	}
	if value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer {
		return !value.IsNil() && externalValueContainsNonceAt(value.Elem(), raw, secret, depth+1)
	}
	switch value.Kind() {
	case reflect.String:
		return strings.Contains(value.String(), raw) || strings.Contains(value.String(), secret)
	case reflect.Struct:
		for index := 0; index < value.NumField(); index++ {
			if value.Type().Field(index).PkgPath == "" && externalValueContainsNonceAt(value.Field(index), raw, secret, depth+1) {
				return true
			}
		}
	case reflect.Array, reflect.Slice:
		for index := 0; index < value.Len(); index++ {
			if externalValueContainsNonceAt(value.Index(index), raw, secret, depth+1) {
				return true
			}
		}
	case reflect.Map:
		iterator := value.MapRange()
		for iterator.Next() {
			if externalValueContainsNonceAt(iterator.Key(), raw, secret, depth+1) || externalValueContainsNonceAt(iterator.Value(), raw, secret, depth+1) {
				return true
			}
		}
	}
	return false
}

func stringInSlice(value string, values []string) bool {
	for _, candidate := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func externalDeploymentRegions(expected []model.ResolvedRegion, observed []ExternalFleetRegionProof) ([]model.DeploymentRegion, error) {
	if len(expected) == 0 || len(observed) != len(expected) {
		return nil, fmt.Errorf("independent verifier did not return one region proof for every configured region")
	}
	byRegion := make(map[string]ExternalFleetRegionProof, len(observed))
	for _, region := range observed {
		if region.Region == "" || region.NomadRegion == "" || uuid.Validate(region.EvalID) != nil || region.DesiredWeight < 0 || region.ActiveWeight != region.DesiredWeight {
			return nil, fmt.Errorf("independent verifier returned an invalid region weight or evaluation binding")
		}
		if _, duplicate := byRegion[region.Region]; duplicate {
			return nil, fmt.Errorf("independent verifier returned duplicate region evidence")
		}
		byRegion[region.Region] = region
	}
	result := make([]model.DeploymentRegion, 0, len(expected))
	for _, configured := range expected {
		region, found := byRegion[configured.Name]
		if !found || region.NomadRegion != configured.NomadRegion || region.DesiredWeight != configured.TrafficWeight {
			return nil, fmt.Errorf("independent verifier region evidence does not match configured placement")
		}
		result = append(result, model.DeploymentRegion{Region: region.Region, NomadRegion: region.NomadRegion, Status: model.StatusDeployed, DesiredWeight: region.DesiredWeight, ActiveWeight: region.ActiveWeight, EvalID: region.EvalID})
	}
	return result, nil
}

func validExternalChronology(steps []ExternalFleetChronologyStep) bool {
	return validExternalChronologyAt(steps, time.Now().UTC())
}

func validExternalChronologyAt(steps []ExternalFleetChronologyStep, now time.Time) bool {
	if len(steps) != 4 {
		return false
	}
	for index, phase := range []string{"prepare", "migration", "runtime", "exercise"} {
		step := steps[index]
		if step.Phase != phase || step.OccurredAt.IsZero() || step.OccurredAt.After(now) || now.Sub(step.OccurredAt) > externalFleetAdmissionNonceTTL || !validExternalURI(step.EvidenceRef) || (index > 0 && !step.OccurredAt.After(steps[index-1].OccurredAt)) {
			return false
		}
	}
	return true
}

func sameExternalChronology(left, right []ExternalFleetChronologyStep) bool {
	return sameExternalChronologyAt(left, right, time.Now().UTC())
}

func sameExternalChronologyAt(left, right []ExternalFleetChronologyStep, now time.Time) bool {
	if !validExternalChronologyAt(left, now) || !validExternalChronologyAt(right, now) || len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func validDistinctIngressNodes(nodes []string) bool {
	if len(nodes) < 2 {
		return false
	}
	seen := map[string]bool{}
	for _, node := range nodes {
		if !externalNamePattern.MatchString(node) || seen[node] {
			return false
		}
		seen[node] = true
	}
	return len(seen) >= 2
}

func validHTTPSVersion(version string) bool {
	return validExternalURI(version) && strings.HasPrefix(version, "https://") && strings.HasSuffix(strings.TrimSuffix(version, "/"), "/version")
}

func validPrivateReadiness(readiness ExternalFleetPrivateReadiness) bool {
	return validPrivateReadinessAt(readiness, time.Now().UTC())
}

func validPrivateReadinessAt(readiness ExternalFleetPrivateReadiness, now time.Time) bool {
	if !validExternalURI(readiness.Endpoint) || !strings.HasSuffix(strings.TrimSuffix(readiness.Endpoint, "/"), "/readyz") || readiness.CheckedAt.IsZero() || readiness.CheckedAt.After(now) || now.Sub(readiness.CheckedAt) > externalFleetAdmissionNonceTTL || len(readiness.AllocationIDs) < 2 {
		return false
	}
	seen := map[string]bool{}
	for _, id := range readiness.AllocationIDs {
		if !externalNamePattern.MatchString(id) || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

func validExternalURI(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) == 0 || len(value) > 2048 || strings.ContainsAny(value, "\r\n") {
		return false
	}
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme == "https" && parsed.Hostname() != "" && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == ""
}

func safeExternalVerificationError(err error, nonce externalAdmissionNonce) string {
	// Verifiers can hold provider, Nomad, and registry responses. Only an error
	// explicitly marked safe is allowed across the control-plane boundary.
	type safe interface{ SafeExternalVerificationError() string }
	if typed, ok := err.(safe); ok {
		value := strings.TrimSpace(typed.SafeExternalVerificationError())
		if value != "" && len(value) <= 240 && !strings.ContainsAny(value, "\r\n") && !strings.Contains(value, nonce.String()) && !strings.Contains(value, nonce.Secret) {
			return value
		}
	}
	return "independent evidence was rejected"
}

// externalReceiptCanonicalJSON makes redacted evidence digests stable for
// verifier adapters and tests. It clears the one-use raw nonce itself so a
// future caller cannot accidentally make it part of durable evidence.
func externalReceiptCanonicalJSON(receipt ExternalFleetDeploymentReceipt) ([]byte, error) {
	return json.Marshal(redactExternalFleetReceipt(receipt))
}
