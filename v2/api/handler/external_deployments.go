package handler

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
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
	Action      string                          `json:"action,omitempty"`
	AdmissionID string                          `json:"admissionId,omitempty"`
	Receipt     *ExternalFleetDeploymentReceipt `json:"receipt,omitempty"`
}

type externalFleetAdmissionBeginRequest struct {
	LogicalIdentity ExternalFleetLogicalIdentity `json:"logicalIdentity"`
}

type externalFleetAdmissionResumeRequest struct {
	AdmissionID     string                       `json:"admissionId"`
	LogicalIdentity ExternalFleetLogicalIdentity `json:"logicalIdentity"`
}

type externalFleetAdmissionResponse struct {
	SchemaVersion   string    `json:"schemaVersion"`
	AdmissionID     string    `json:"admissionId"`
	Nonce           string    `json:"nonce,omitempty"`
	ExpiresAt       time.Time `json:"expiresAt,omitempty"`
	State           string    `json:"state"`
	NonceGeneration int64     `json:"nonceGeneration,omitempty"`
	OperationID     string    `json:"operationId,omitempty"`
	CleanupState    string    `json:"cleanupState,omitempty"`
}

type externalFleetAdmissionContextResponse struct {
	SchemaVersion   string                       `json:"schemaVersion"`
	AdmissionID     string                       `json:"admissionId"`
	State           string                       `json:"state"`
	LogicalIdentity ExternalFleetLogicalIdentity `json:"logicalIdentity"`
	LogicalDigest   string                       `json:"logicalDigest"`
	NonceGeneration int64                        `json:"nonceGeneration"`
	OperationID     string                       `json:"operationId,omitempty"`
	CleanupState    string                       `json:"cleanupState"`
	RetryLineage    []string                     `json:"retryLineage"`
	Checkpoints     []ExternalFleetCheckpointRef `json:"checkpoints"`
}

type externalFleetAdmissionCleanupRequest struct {
	AdmissionID         string `json:"admissionId"`
	OperationID         string `json:"operationId"`
	ReceiptDigest       string `json:"receiptDigest"`
	CleanupIntentDigest string `json:"cleanupIntentDigest"`
	AbsenceProofDigest  string `json:"absenceProofDigest"`
}

type externalFleetAdmissionReconcileRequest struct {
	AdmissionID string `json:"admissionId"`
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
	Receipt             ExternalFleetDeploymentReceipt
	CI                  CIIdentity
	Config              ExternalFleetAdmissionConfig
	AdmissionGeneration int64
	// ServiceSnapshot is the exact hash-validated claim response persisted by
	// the admission saga. Supplying it prevents a second status read from
	// substituting evidence between claim and verification.
	ServiceSnapshot *ExternalFleetEvidenceSnapshot
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
	if h.db == nil {
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
		// Terminal idempotent replays are entirely local. A failed remote commit
		// is represented durably by cleanup_pending and may be reconciled by an
		// authenticated later request; never manufacture a weaker commit binding.
		cleanupState := "pending"
		if admission.State == store.ExternalDeploymentAdmissionComplete {
			cleanupState = "complete"
		}
		writeJSON(w, externalFleetAdmissionResponse{SchemaVersion: "norn.external-fleet-admission/v4", AdmissionID: admission.ID, State: string(admission.State), NonceGeneration: admission.NonceGeneration, OperationID: admission.OperationID, CleanupState: cleanupState})
		return
	}
	registrar, ok := h.externalFleetDeploymentVerifier.(ExternalFleetEvidenceRegistrationClient)
	if !ok {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "external_deployment_registration_unavailable", "Norn-owned evidence nonce registration is not configured")
		return
	}
	if admission.State == store.ExternalDeploymentAdmissionEvidenceClaimed || admission.State == store.ExternalDeploymentAdmissionExpired {
		WriteControlProblem(w, r, http.StatusConflict, "external_deployment_admission_unavailable", "admission cannot issue another nonce in its current state")
		return
	}
	// A response lost after registration may be recovered only by replacing an
	// unclaimed generation.  The service CAS is chained to its returned
	// revision, so an old generation can never be silently reactivated.
	expectedRegistrationRevision := int64(0)
	if admission.NonceID != "" {
		previous, previousErr := h.db.GetExternalDeploymentNonceRegistration(r.Context(), admission.ID, admission.NonceID)
		if previousErr != nil || (previous.State != "registering" && previous.State != "ready") {
			WriteControlProblem(w, r, http.StatusConflict, "external_deployment_admission_unavailable", "admission nonce generation cannot be safely superseded")
			return
		}
		if previous.ServiceRevision < 1 {
			var previousIdentity ExternalFleetLogicalIdentity
			expected, parseErr := strconv.ParseInt(previous.RegistrationMetadata["expectedRevision"], 10, 64)
			issued, issuedErr := time.Parse(time.RFC3339Nano, previous.RegistrationMetadata["issuedAt"])
			if parseErr != nil || expected < 0 || issuedErr != nil || json.Unmarshal([]byte(previous.RegistrationMetadata["logicalIdentity"]), &previousIdentity) != nil || !reflect.DeepEqual(previousIdentity, request.LogicalIdentity) {
				WriteControlProblem(w, r, http.StatusConflict, "external_deployment_registration_unavailable", "pending registration context is not exact")
				return
			}
			// A crash after the local registering write is healed by replaying the
			// exact original PUT, not by inventing a successor generation.
			pending := ExternalFleetEvidenceRegistration{SchemaVersion: "norn.external-fleet-admission-callback/v4", AdmissionID: admission.ID, LogicalDigest: digest, LogicalIdentity: previousIdentity, AdmissionContextDigest: strings.TrimPrefix(digest, "sha256:"), CurrentAttemptID: previous.CIRunID + ":" + previous.CIRunAttempt, CIRepository: previous.CIRepository, CIRunID: previous.CIRunID, CIRunAttempt: previous.CIRunAttempt, NonceSHA256: previous.NonceSHA256, Generation: previous.Generation, ExpectedRevision: expected, IssuedAt: issued, ExpiresAt: previous.ExpiresAt}
			status, statusErr := registrar.RegisterExternalFleetNonce(r.Context(), pending)
			if statusErr != nil {
				status, statusErr = registrar.GetExternalFleetAdmissionStatus(r.Context(), admission.ID, previous.Generation)
			}
			if statusErr != nil || !externalRegistrationStatusMatches(status, pending) {
				WriteControlProblem(w, r, http.StatusServiceUnavailable, "external_deployment_registration_unavailable", "registration outcome cannot be reconciled before nonce rotation")
				return
			}
			if previous.State == "registering" {
				if err := h.db.MarkExternalDeploymentNonceReady(r.Context(), admission.ID, previous.NonceID, previous.Generation, previous.RegistrationRef, status.Revision); err != nil {
					WriteControlProblem(w, r, http.StatusServiceUnavailable, "external_deployment_registration_unavailable", "registration outcome could not be persisted")
					return
				}
			}
			previous.ServiceRevision = status.Revision
		}
		expectedRegistrationRevision = previous.ServiceRevision
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "external_deployment_nonce_unavailable", "secure nonce generation failed")
		return
	}
	nonce := externalAdmissionNonce{ID: uuid.NewString(), Secret: hex.EncodeToString(raw)}
	generation := admission.NonceGeneration + 1
	issuedAt := time.Now().UTC()
	expiresAt := time.Now().UTC().Add(externalFleetAdmissionNonceTTL)
	registrationRef := admission.ID + ":" + fmt.Sprint(generation)
	stored := externalNonceStoreRecord(nonce, appID, h.cfg.EnvironmentID(), *principal.CI)
	stored.ExpiresAt, stored.AdmissionID, stored.RegistrationGeneration, stored.RegistrationRef = expiresAt, admission.ID, generation, registrationRef
	stored.IssuerSubject, stored.IssuerTokenID = principal.Subject, principal.TokenID
	identityJSON, _ := json.Marshal(request.LogicalIdentity)
	stored.RegistrationMetadata = map[string]string{"logicalDigest": digest, "logicalIdentity": string(identityJSON), "expectedRevision": strconv.FormatInt(expectedRegistrationRevision, 10), "issuedAt": issuedAt.Format(time.RFC3339Nano)}
	if err := h.db.IssueExternalDeploymentNonce(r.Context(), stored); err != nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "external_deployment_nonce_unavailable", "durable nonce registration could not be started")
		return
	}
	// A first generation starts at revision zero. A replacement generation is
	// chained to the exact persisted unclaimed registration revision.
	registration := ExternalFleetEvidenceRegistration{SchemaVersion: "norn.external-fleet-admission-callback/v4", AdmissionID: admission.ID, LogicalDigest: digest, LogicalIdentity: request.LogicalIdentity, AdmissionContextDigest: strings.TrimPrefix(digest, "sha256:"), CurrentAttemptID: principal.CI.RunID + ":" + principal.CI.RunAttempt, CIRepository: principal.CI.Repository, CIRunID: principal.CI.RunID, CIRunAttempt: principal.CI.RunAttempt, NonceSHA256: nonce.sha256(), Generation: generation, ExpectedRevision: expectedRegistrationRevision, IssuedAt: issuedAt, ExpiresAt: expiresAt}
	registered, registerErr := registrar.RegisterExternalFleetNonce(r.Context(), registration)
	if registerErr != nil {
		registered, registerErr = registrar.GetExternalFleetAdmissionStatus(r.Context(), admission.ID, generation)
	}
	if registerErr != nil || !externalRegistrationStatusMatches(registered, registration) {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "external_deployment_registration_unavailable", "evidence nonce registration failed before disclosure")
		return
	}
	if err := h.db.MarkExternalDeploymentNonceReady(r.Context(), admission.ID, nonce.ID, generation, registrationRef, registered.Revision); err != nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "external_deployment_nonce_unavailable", "durable nonce registration could not be completed")
		return
	}
	writeJSONStatus(w, http.StatusCreated, externalFleetAdmissionResponse{SchemaVersion: "norn.external-fleet-admission/v4", AdmissionID: admission.ID, Nonce: nonce.String(), ExpiresAt: expiresAt, State: string(store.ExternalDeploymentAdmissionNonceReady), NonceGeneration: generation})
}

// AdmitExternalFleetDeploymentV4 accepts only the v4 receipt envelope. The
// legacy endpoint below shares this implementation for an unreleased bridge,
// but no request can issue an unregistered nonce or submit a v3 receipt.
func (h *Handler) AdmitExternalFleetDeploymentV4(w http.ResponseWriter, r *http.Request) {
	h.AdmitExternalFleetDeployment(w, r)
}

// ResumeExternalFleetDeploymentAdmission rotates an undisclosed nonce only
// after proving the same authenticated logical identity and idempotency key.
// It deliberately reuses begin's registration-before-disclosure barrier.
func (h *Handler) ResumeExternalFleetDeploymentAdmission(w http.ResponseWriter, r *http.Request) {
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
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "external_deployment_store_unavailable", "durable external admission storage is unavailable")
		return
	}
	var request externalFleetAdmissionResumeRequest
	if err := decodeControlJSONLimit(w, r, &request, maxReleaseEvidenceJSONBody); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_external_deployment_resume", err.Error())
		return
	}
	if uuid.Validate(request.AdmissionID) != nil || h.validateExternalFleetLogicalIdentity(appID, configured, request.LogicalIdentity) != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_external_deployment_resume", "resume requires a valid admissionId and unchanged logical identity")
		return
	}
	_, digest, valid := externalFleetAdmissionIdentityIdempotency(w, r, principal, appID, request.LogicalIdentity)
	if !valid {
		return
	}
	admission, err := h.db.GetExternalDeploymentAdmission(r.Context(), request.AdmissionID, appID, principal.Environment, principal.CI.Repository)
	if err != nil || admission.RequestDigest != digest {
		WriteControlProblem(w, r, http.StatusConflict, "external_deployment_admission_conflict", "resume identity does not match the server-owned admission")
		return
	}
	// Reuse the single begin implementation rather than creating a second
	// nonce issuer with subtly different disclosure or expiry semantics.
	body, _ := json.Marshal(externalFleetAdmissionBeginRequest{LogicalIdentity: request.LogicalIdentity})
	r.Body = io.NopCloser(bytes.NewReader(body))
	h.BeginExternalFleetDeploymentAdmission(w, r)
}

// GetExternalFleetDeploymentAdmissionContext exposes server-owned progress;
// it never accepts a receipt echo as evidence of lineage or chronology.
func (h *Handler) GetExternalFleetDeploymentAdmissionContext(w http.ResponseWriter, r *http.Request) {
	preventSensitiveResponseCaching(w)
	appID := chi.URLParam(r, "id")
	principal, ok := requireExternalFleetAdmissionScope(w, r, appID)
	if !ok {
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
	admission, err := h.db.GetExternalDeploymentAdmission(r.Context(), admissionID, appID, principal.Environment, principal.CI.Repository)
	if err != nil {
		WriteControlProblem(w, r, http.StatusConflict, "external_deployment_admission_missing", "server-owned admission context is unavailable")
		return
	}
	checkpoints := []ExternalFleetCheckpointRef{}
	rows, queryErr := h.db.Pool.Query(r.Context(), `SELECT phase, checkpoint_id, attempt_id, evidence_ref, evidence_sha256 FROM external_deployment_admission_checkpoints WHERE admission_id=$1 ORDER BY created_at, phase`, admissionID)
	if queryErr != nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "external_deployment_context_unavailable", "server-owned checkpoint context is unavailable")
		return
	}
	for rows.Next() {
		var checkpoint ExternalFleetCheckpointRef
		if err := rows.Scan(&checkpoint.Phase, &checkpoint.CheckpointID, &checkpoint.AttemptID, &checkpoint.EvidenceRef, &checkpoint.EvidenceSHA256); err != nil {
			rows.Close()
			WriteControlProblem(w, r, http.StatusServiceUnavailable, "external_deployment_context_unavailable", "server-owned checkpoint context is invalid")
			return
		}
		checkpoints = append(checkpoints, checkpoint)
	}
	rows.Close()
	var lineageJSON []byte
	if err := h.db.Pool.QueryRow(r.Context(), `SELECT service_retry_lineage FROM external_deployment_admissions WHERE id=$1`, admissionID).Scan(&lineageJSON); err != nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "external_deployment_context_unavailable", "server-owned retry lineage is unavailable")
		return
	}
	lineage := []string{}
	if err := json.Unmarshal(lineageJSON, &lineage); err != nil || (len(lineage) > 0 && !validExternalRetryLineage(lineage)) {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "external_deployment_context_unavailable", "server-owned retry lineage is invalid")
		return
	}
	// The response is deliberately derived from durable references, never a
	// caller-supplied receipt. Before cleanup only external_admission exists;
	// completion atomically adds exactly the service-owned external_cleanup.
	terminal := admission.State == store.ExternalDeploymentAdmissionCommitted || admission.State == store.ExternalDeploymentAdmissionCleanupPending || admission.State == store.ExternalDeploymentAdmissionComplete
	expectedCheckpoints := 0
	if terminal {
		expectedCheckpoints = 1
	}
	if admission.State == store.ExternalDeploymentAdmissionComplete {
		expectedCheckpoints = 2
	}
	if len(checkpoints) != expectedCheckpoints || (expectedCheckpoints >= 1 && checkpoints[0].Phase != "external_admission") || (expectedCheckpoints == 2 && checkpoints[1].Phase != "external_cleanup") {
		WriteControlProblem(w, r, http.StatusConflict, "external_deployment_context_drift", "durable external admission checkpoint lineage is incomplete")
		return
	}
	if terminal && !validExternalRetryLineage(lineage) {
		WriteControlProblem(w, r, http.StatusConflict, "external_deployment_context_drift", "durable retry lineage is incomplete")
		return
	}
	cleanup := "pending"
	if admission.State == store.ExternalDeploymentAdmissionComplete {
		cleanup = "complete"
	}
	identity := ExternalFleetLogicalIdentity{}
	if admission.NonceID != "" {
		registration, registrationErr := h.db.GetExternalDeploymentNonceRegistration(r.Context(), admission.ID, admission.NonceID)
		if registrationErr != nil || json.Unmarshal([]byte(registration.RegistrationMetadata["logicalIdentity"]), &identity) != nil || identity.SchemaVersion != externalFleetLogicalIdentitySchemaV4 {
			WriteControlProblem(w, r, http.StatusConflict, "external_deployment_context_drift", "durable logical identity is missing or invalid")
			return
		}
	}
	writeJSON(w, externalFleetAdmissionContextResponse{SchemaVersion: "norn.external-fleet-admission-context/v4", AdmissionID: admissionID, State: string(admission.State), LogicalIdentity: identity, LogicalDigest: admission.RequestDigest, NonceGeneration: admission.NonceGeneration, OperationID: admission.OperationID, CleanupState: cleanup, RetryLineage: lineage, Checkpoints: checkpoints})
}

// CompleteExternalFleetDeploymentCleanup accepts only a typed absence-proof
// digest bound to the already committed server operation. It performs no
// cleanup itself: Fleet owns migration-only cleanup and Norn records its
// durable completion after validating the binding.
func (h *Handler) CompleteExternalFleetDeploymentCleanup(w http.ResponseWriter, r *http.Request) {
	preventSensitiveResponseCaching(w)
	appID := chi.URLParam(r, "id")
	principal, ok := requireExternalFleetAdmissionScope(w, r, appID)
	if !ok || h.db == nil {
		if ok {
			WriteControlProblem(w, r, http.StatusServiceUnavailable, "external_deployment_store_unavailable", "durable external admission storage is unavailable")
		}
		return
	}
	var request externalFleetAdmissionCleanupRequest
	if err := decodeControlJSONLimit(w, r, &request, maxReleaseEvidenceJSONBody); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_external_deployment_cleanup", err.Error())
		return
	}
	if uuid.Validate(request.AdmissionID) != nil || uuid.Validate(request.OperationID) != nil || !sha256HexPattern.MatchString(request.ReceiptDigest) || !sha256HexPattern.MatchString(request.CleanupIntentDigest) || !sha256HexPattern.MatchString(request.AbsenceProofDigest) {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_external_deployment_cleanup", "cleanup requires admission, operation, receipt, intent, and absence-proof bindings")
		return
	}
	var state, operationID, receiptDigest, proofDigest, expectedIntent, expectedAbsence, snapshotID, snapshotRef, snapshotSHA string
	var claimRevision, commitRevision, cleanupRevision int64
	err := h.db.Pool.QueryRow(r.Context(), `SELECT a.state, COALESCE(a.operation_id,''), COALESCE(o.metadata->>'externalFleetProofSHA256',''), COALESCE(a.service_proof_sha256,''), COALESCE(a.cleanup_intent_sha256,''), COALESCE(a.absence_proof_sha256,''), COALESCE(a.service_snapshot_id,''), COALESCE(a.service_snapshot_ref,''), COALESCE(a.service_snapshot_sha256,''), COALESCE(a.service_claim_revision,0), COALESCE(a.service_commit_revision,0), COALESCE(a.service_cleanup_revision,0) FROM external_deployment_admissions a LEFT JOIN operations o ON o.id=a.operation_id WHERE a.id=$1 AND a.app=$2 AND a.environment=$3 AND a.ci_repository=$4`, request.AdmissionID, appID, principal.Environment, principal.CI.Repository).Scan(&state, &operationID, &receiptDigest, &proofDigest, &expectedIntent, &expectedAbsence, &snapshotID, &snapshotRef, &snapshotSHA, &claimRevision, &commitRevision, &cleanupRevision)
	if err != nil || operationID != request.OperationID || receiptDigest != request.ReceiptDigest {
		WriteControlProblem(w, r, http.StatusConflict, "external_deployment_cleanup_conflict", "cleanup binding does not match the committed server admission")
		return
	}
	if state == string(store.ExternalDeploymentAdmissionComplete) {
		if expectedIntent == "" || expectedAbsence == "" || cleanupRevision <= commitRevision || expectedIntent != request.CleanupIntentDigest || expectedAbsence != request.AbsenceProofDigest {
			WriteControlProblem(w, r, http.StatusConflict, "external_deployment_cleanup_conflict", "completed admission does not match the exact cleanup bindings")
			return
		}
		var checkpointDigest string
		if err := h.db.Pool.QueryRow(r.Context(), `SELECT evidence_sha256 FROM external_deployment_admission_checkpoints WHERE admission_id=$1 AND phase='external_cleanup'`, request.AdmissionID).Scan(&checkpointDigest); err != nil || checkpointDigest != expectedAbsence {
			WriteControlProblem(w, r, http.StatusConflict, "external_deployment_cleanup_conflict", "completed admission lacks its exact cleanup checkpoint")
			return
		}
		writeJSON(w, externalFleetAdmissionResponse{SchemaVersion: "norn.external-fleet-admission/v4", AdmissionID: request.AdmissionID, State: state, OperationID: operationID})
		return
	}
	if state != string(store.ExternalDeploymentAdmissionCleanupPending) {
		WriteControlProblem(w, r, http.StatusConflict, "external_deployment_cleanup_conflict", "admission is not awaiting cleanup")
		return
	}
	registrar, registrationOK := h.externalFleetDeploymentVerifier.(ExternalFleetEvidenceRegistrationClient)
	admission, admissionErr := h.db.GetExternalDeploymentAdmission(r.Context(), request.AdmissionID, appID, principal.Environment, principal.CI.Repository)
	if !registrationOK || admissionErr != nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "external_deployment_cleanup_unavailable", "service-owned cleanup status is unavailable")
		return
	}
	status, statusErr := registrar.GetExternalFleetAdmissionStatus(r.Context(), admission.ID, admission.NonceGeneration)
	registration, registrationErr := h.db.GetExternalDeploymentNonceRegistration(r.Context(), admission.ID, admission.NonceID)
	if statusErr != nil || registrationErr != nil || status == nil || status.State != "cleanup_ready" || status.LogicalDigest != admission.RequestDigest || status.AdmissionContextDigest != strings.TrimPrefix(admission.RequestDigest, "sha256:") || status.NonceSHA256 != registration.NonceSHA256 || status.ProofDigest != proofDigest || !sha256HexPattern.MatchString(status.CleanupIntentDigest) || !sha256HexPattern.MatchString(status.AbsenceProofDigest) {
		WriteControlProblem(w, r, http.StatusConflict, "external_deployment_cleanup_conflict", "service-owned cleanup status does not exactly match the committed admission")
		return
	}
	op, opErr := h.db.GetOperation(r.Context(), operationID)
	opDigest, opDigestErr := externalOperationDigest(op)
	if opErr != nil || opDigestErr != nil || status.AdmissionID != admission.ID || status.Generation != admission.NonceGeneration || status.OperationID != operationID || status.OperationDigest != opDigest || status.ReceiptDigest != receiptDigest || status.Snapshot == nil || status.Snapshot.ID != snapshotID || status.Snapshot.Ref != snapshotRef || status.Snapshot.SHA256 != snapshotSHA || status.Revision != commitRevision+1 || claimRevision < 1 || expectedIntent == "" || expectedIntent != status.CleanupIntentDigest {
		WriteControlProblem(w, r, http.StatusConflict, "external_deployment_cleanup_conflict", "service-owned cleanup status has drifted from the committed admission")
		return
	}
	if expectedAbsence == "" {
		if err := h.db.RecordExternalDeploymentCleanupBindings(r.Context(), store.ExternalDeploymentServiceSnapshot{AdmissionID: admission.ID, CommitRevision: commitRevision, CleanupRevision: status.Revision, CleanupIntentSHA256: expectedIntent, AbsenceProofSHA256: status.AbsenceProofDigest}); err != nil {
			WriteControlProblem(w, r, http.StatusServiceUnavailable, "external_deployment_cleanup_unavailable", "service-owned cleanup bindings could not be persisted")
			return
		}
		expectedAbsence, cleanupRevision = status.AbsenceProofDigest, status.Revision
	}
	if expectedIntent != status.CleanupIntentDigest || expectedAbsence != status.AbsenceProofDigest || expectedIntent != request.CleanupIntentDigest || expectedAbsence != request.AbsenceProofDigest || cleanupRevision != status.Revision {
		WriteControlProblem(w, r, http.StatusConflict, "external_deployment_cleanup_conflict", "cleanup request does not match persisted service-owned bindings")
		return
	}
	var lineageJSON []byte
	if err := h.db.Pool.QueryRow(r.Context(), `SELECT service_retry_lineage FROM external_deployment_admissions WHERE id=$1`, admission.ID).Scan(&lineageJSON); err != nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "external_deployment_cleanup_unavailable", "persisted retry lineage is unavailable")
		return
	}
	var lineage []string
	checkpoint := status.CleanupCheckpoint
	if json.Unmarshal(lineageJSON, &lineage) != nil || !validExternalRetryLineage(lineage) || checkpoint == nil || checkpoint.Phase != "external_cleanup" || checkpoint.AttemptID != lineage[len(lineage)-1] || checkpoint.EvidenceSHA256 != status.AbsenceProofDigest || checkpoint.CheckpointID == "" || checkpoint.EvidenceRef == "" {
		WriteControlProblem(w, r, http.StatusConflict, "external_deployment_cleanup_conflict", "cleanup-ready status lacks the exact service-owned final checkpoint")
		return
	}
	if err := h.db.CompleteExternalDeploymentAdmissionWithCleanupCheckpoint(r.Context(), request.AdmissionID, store.ExternalDeploymentCheckpointRef{Phase: checkpoint.Phase, CheckpointID: checkpoint.CheckpointID, AttemptID: checkpoint.AttemptID, EvidenceRef: checkpoint.EvidenceRef, EvidenceSHA256: checkpoint.EvidenceSHA256}); err != nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "external_deployment_cleanup_unavailable", "durable cleanup completion could not be recorded")
		return
	}
	writeJSON(w, externalFleetAdmissionResponse{SchemaVersion: "norn.external-fleet-admission/v4", AdmissionID: request.AdmissionID, State: string(store.ExternalDeploymentAdmissionComplete), OperationID: operationID})
}

// ReconcileExternalFleetDeploymentAdmission completes an explicitly durable
// remote commit after a network loss. It is intentionally receipt-free: every
// CAS field is reloaded from Norn's persisted claim and operation projection.
func (h *Handler) ReconcileExternalFleetDeploymentAdmission(w http.ResponseWriter, r *http.Request) {
	preventSensitiveResponseCaching(w)
	appID := chi.URLParam(r, "id")
	principal, ok := requireExternalFleetAdmissionScope(w, r, appID)
	if !ok || h.db == nil {
		if ok {
			WriteControlProblem(w, r, http.StatusServiceUnavailable, "external_deployment_store_unavailable", "durable external admission storage is unavailable")
		}
		return
	}
	var request externalFleetAdmissionReconcileRequest
	if err := decodeControlJSONLimit(w, r, &request, maxReleaseEvidenceJSONBody); err != nil || uuid.Validate(request.AdmissionID) != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_external_deployment_reconcile", "reconcile requires an admissionId")
		return
	}
	admission, err := h.db.GetExternalDeploymentAdmission(r.Context(), request.AdmissionID, appID, principal.Environment, principal.CI.Repository)
	if err != nil || (admission.State != store.ExternalDeploymentAdmissionCommitted && admission.State != store.ExternalDeploymentAdmissionCleanupPending) || admission.OperationID == "" {
		WriteControlProblem(w, r, http.StatusConflict, "external_deployment_reconcile_conflict", "admission is not a durable pending remote commit")
		return
	}
	var snapshotID, snapshotRef, snapshotSHA, receiptDigest, proofDigest, cleanupIntent string
	var claimRevision, commitRevision int64
	err = h.db.Pool.QueryRow(r.Context(), `SELECT service_snapshot_id, service_snapshot_ref, service_snapshot_sha256, service_receipt_sha256, service_proof_sha256, cleanup_intent_sha256, service_claim_revision, service_commit_revision FROM external_deployment_admissions WHERE id=$1`, admission.ID).Scan(&snapshotID, &snapshotRef, &snapshotSHA, &receiptDigest, &proofDigest, &cleanupIntent, &claimRevision, &commitRevision)
	if err != nil || snapshotID == "" || snapshotRef == "" || !sha256HexPattern.MatchString(snapshotSHA) || !sha256HexPattern.MatchString(receiptDigest) || !sha256HexPattern.MatchString(proofDigest) || !sha256HexPattern.MatchString(cleanupIntent) || claimRevision < 1 {
		WriteControlProblem(w, r, http.StatusConflict, "external_deployment_reconcile_conflict", "persisted remote commit bindings are incomplete")
		return
	}
	if commitRevision > 0 {
		if err := h.db.MarkExternalDeploymentAdmissionCleanupPending(r.Context(), admission.ID); err != nil {
			WriteControlProblem(w, r, http.StatusServiceUnavailable, "external_deployment_reconcile_unavailable", "committed remote admission could not advance to cleanup")
			return
		}
		writeJSON(w, externalFleetAdmissionResponse{SchemaVersion: "norn.external-fleet-admission/v4", AdmissionID: admission.ID, State: string(store.ExternalDeploymentAdmissionCleanupPending), OperationID: admission.OperationID, CleanupState: "pending"})
		return
	}
	registration, err := h.db.GetExternalDeploymentNonceRegistration(r.Context(), admission.ID, admission.NonceID)
	op, opErr := h.db.GetOperation(r.Context(), admission.OperationID)
	opDigest, digestErr := externalOperationDigest(op)
	registrar, registered := h.externalFleetDeploymentVerifier.(ExternalFleetEvidenceRegistrationClient)
	if err != nil || opErr != nil || digestErr != nil || !registered || registration.Generation != admission.NonceGeneration || registration.ServiceRevision < 1 {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "external_deployment_reconcile_unavailable", "remote commit cannot be reconstructed")
		return
	}
	status, statusErr := registrar.GetExternalFleetAdmissionStatus(r.Context(), admission.ID, admission.NonceGeneration)
	snapshot := &ExternalFleetEvidenceSnapshot{ID: snapshotID, Ref: snapshotRef, SHA256: snapshotSHA}
	claim := ExternalFleetEvidenceClaim{SchemaVersion: "norn.external-fleet-admission-callback/v4", AdmissionID: admission.ID, LogicalDigest: admission.RequestDigest, AdmissionContextDigest: strings.TrimPrefix(admission.RequestDigest, "sha256:"), ReceiptDigest: receiptDigest, ProofDigest: proofDigest, NonceSHA256: registration.NonceSHA256, Generation: admission.NonceGeneration, ExpectedRevision: claimRevision, OperationID: admission.OperationID, OperationDigest: opDigest, CleanupIntentDigest: cleanupIntent}
	if statusErr == nil && externalCommitStatusMatches(status, claim, snapshot) {
		if err := h.db.RecordExternalDeploymentCommitBinding(r.Context(), store.ExternalDeploymentServiceSnapshot{AdmissionID: admission.ID, CommitRevision: status.Revision, CleanupIntentSHA256: cleanupIntent}); err == nil {
			_ = h.db.MarkExternalDeploymentAdmissionCleanupPending(r.Context(), admission.ID)
			writeJSON(w, externalFleetAdmissionResponse{SchemaVersion: "norn.external-fleet-admission/v4", AdmissionID: admission.ID, State: string(store.ExternalDeploymentAdmissionCleanupPending), OperationID: admission.OperationID, CleanupState: "pending"})
			return
		}
	}
	committed, commitErr := registrar.CommitExternalFleetNonce(r.Context(), claim)
	if commitErr != nil || !externalCommitStatusMatches(committed, claim, snapshot) || h.db.RecordExternalDeploymentCommitBinding(r.Context(), store.ExternalDeploymentServiceSnapshot{AdmissionID: admission.ID, CommitRevision: committed.Revision, CleanupIntentSHA256: cleanupIntent}) != nil || h.db.MarkExternalDeploymentAdmissionCleanupPending(r.Context(), admission.ID) != nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "external_deployment_reconcile_unavailable", "remote commit remains pending")
		return
	}
	writeJSON(w, externalFleetAdmissionResponse{SchemaVersion: "norn.external-fleet-admission/v4", AdmissionID: admission.ID, State: string(store.ExternalDeploymentAdmissionCleanupPending), OperationID: admission.OperationID, CleanupState: "pending"})
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
	if h.db == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "external_deployment_store_unavailable", "durable external deployment admission storage is unavailable")
		return
	}
	// A nonce is authority to submit a receipt, so admission is unavailable
	// until the independently configured verifier is live.
	if h.externalFleetDeploymentVerifier == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "external_deployment_verifier_unavailable", "independent Fleet runtime verification is not configured; nonce issuance is disabled")
		return
	}
	var request externalDeploymentRequest
	if err := decodeControlJSONLimit(w, r, &request, maxReleaseEvidenceJSONBody); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_external_deployment_request", err.Error())
		return
	}
	// Receipt-free terminal replay is intentionally before current config,
	// receipt, nonce, GitHub, or freshness checks. The admission ID plus the
	// scoped idempotency key selects only one durable server operation.
	if request.AdmissionID != "" && request.Receipt == nil && request.Action == "" {
		if uuid.Validate(request.AdmissionID) != nil {
			WriteControlProblem(w, r, http.StatusBadRequest, "invalid_external_deployment_request", "admissionId must be a UUID")
			return
		}
		clientKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
		keySum := sha256.Sum256([]byte("external-fleet-admission\x00" + principal.CI.Repository + "\x00" + principal.Environment + "\x00" + appID + "\x00" + clientKey))
		admission, lookupErr := h.db.GetExternalDeploymentAdmission(r.Context(), request.AdmissionID, appID, principal.Environment, principal.CI.Repository)
		if clientKey == "" || lookupErr != nil || admission.IdempotencyKey != "app.deploy:"+hex.EncodeToString(keySum[:]) || (admission.State != store.ExternalDeploymentAdmissionCommitted && admission.State != store.ExternalDeploymentAdmissionCleanupPending && admission.State != store.ExternalDeploymentAdmissionComplete) {
			WriteControlProblem(w, r, http.StatusConflict, "external_deployment_admission_conflict", "receipt-free replay does not match a terminal admission")
			return
		}
		if op, err := h.db.GetOperation(r.Context(), admission.OperationID); err == nil {
			op.AttachReceipt()
			writeJSON(w, op)
			return
		}
		WriteControlProblem(w, r, http.StatusConflict, "external_deployment_admission_conflict", "terminal admission operation is unavailable")
		return
	}
	configured, err := h.externalFleetAdmissionConfig(appID)
	if err != nil {
		WriteControlProblem(w, r, http.StatusConflict, "external_deployment_unavailable", err.Error())
		return
	}
	if request.Action != "" || request.Receipt == nil || request.Receipt.SchemaVersion != externalFleetReceiptSchemaV4 {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_external_deployment_request", "external deployment admission accepts exactly one v4 receipt; begin must register the nonce first")
		return
	}
	if h.externalFleetDeploymentVerifier == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "external_deployment_verifier_unavailable", "independent Fleet runtime verification is not configured; receipt assertions are not accepted")
		return
	}
	receipt := *request.Receipt
	isV4 := true
	if isV4 {
		identity := ExternalFleetLogicalIdentity{SchemaVersion: externalFleetLogicalIdentitySchemaV4, SourceSHA: receipt.SourceSHA, Artifact: receipt.Artifact, Candidate: receipt.Candidate, Fleet: ExternalFleetLogicalExecutionIdentity{Namespace: receipt.Fleet.Namespace, PlanID: receipt.Fleet.PlanID, PlanSHA256: receipt.Fleet.PlanSHA256, RootAttemptID: receipt.Fleet.RootAttemptID, FleetCommit: receipt.Fleet.FleetCommit}}
		if err := h.validateExternalFleetLogicalIdentity(appID, configured, identity); err != nil || !validExternalFleetReceiptV4(receipt, configured, appID) {
			WriteControlProblem(w, r, http.StatusBadRequest, "invalid_external_deployment_receipt", "v4 receipt has invalid immutable identity, proof, or chronology")
			return
		}
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
	var serviceCheckpointRefs []store.ExternalDeploymentCheckpointRef
	var serviceSnapshot *ExternalFleetEvidenceSnapshot
	var serviceClaimRevision int64
	var serviceProofDigest string
	var serviceReceiptDigest string
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
		if !registrationOK || (admission.State != store.ExternalDeploymentAdmissionNonceReady && admission.State != store.ExternalDeploymentAdmissionEvidenceClaimed) || admission.NonceID != nonce.ID || admission.NonceGeneration < 1 {
			WriteControlProblem(w, r, http.StatusConflict, "external_deployment_admission_unavailable", "receipt nonce is not a ready server-registered admission nonce")
			return
		}
		registrationState, registrationErr := h.db.GetExternalDeploymentNonceRegistration(r.Context(), admission.ID, nonce.ID)
		if registrationErr != nil || registrationState.NonceSHA256 != nonce.sha256() || registrationState.ExpiresAt.Before(time.Now().UTC()) || registrationState.ServiceRevision < 1 {
			WriteControlProblem(w, r, http.StatusConflict, "external_deployment_admission_unavailable", "server-registered nonce is expired or its durable registration drifted")
			return
		}
		// Claim remotely first. A network timeout can be reconciled through the
		// service's idempotent hash/generation claim rather than leaving a local
		// claim stranded without the independently observed snapshot.
		receiptBytes, _ := externalReceiptCanonicalJSON(receipt)
		receiptDigest := sha256.Sum256(receiptBytes)
		proofDigest, proofErr := externalAdmissionProofDigest(receipt)
		if proofErr != nil {
			WriteControlProblem(w, r, http.StatusInternalServerError, "external_deployment_store_failed", "failed to canonicalize external deployment proof")
			return
		}
		claimRequest := ExternalFleetEvidenceClaim{SchemaVersion: "norn.external-fleet-admission-callback/v4", AdmissionID: admission.ID, LogicalDigest: digest, AdmissionContextDigest: strings.TrimPrefix(digest, "sha256:"), ReceiptDigest: hex.EncodeToString(receiptDigest[:]), ProofDigest: proofDigest, NonceSHA256: nonce.sha256(), Generation: admission.NonceGeneration, ExpectedRevision: registrationState.ServiceRevision}
		claimStatus, claimErr := registrar.ClaimExternalFleetNonce(r.Context(), claimRequest)
		if claimErr != nil {
			claimStatus, claimErr = registrar.GetExternalFleetAdmissionStatus(r.Context(), admission.ID, admission.NonceGeneration)
		}
		if claimErr != nil || !externalClaimStatusMatches(claimStatus, claimRequest) {
			WriteControlProblem(w, r, http.StatusServiceUnavailable, "external_deployment_registration_unavailable", "evidence nonce claim failed")
			return
		}
		// Store the complete immutable service response before making the local
		// nonce claim visible. This leaves a timeout/crash recoverable solely by
		// GET status, never by a second mutable evidence collection.
		status := claimStatus
		serviceSnapshot, serviceClaimRevision, serviceProofDigest, serviceReceiptDigest = status.Snapshot, status.Revision, proofDigest, claimRequest.ReceiptDigest
		if err := h.db.RecordExternalDeploymentServiceSnapshot(r.Context(), store.ExternalDeploymentServiceSnapshot{AdmissionID: admission.ID, SnapshotID: status.Snapshot.ID, SnapshotRef: status.Snapshot.Ref, SnapshotSHA256: status.Snapshot.SHA256, RetryLineage: status.Snapshot.RetryLineage, ReceiptDigest: claimRequest.ReceiptDigest, ProofDigest: claimRequest.ProofDigest, ClaimRevision: status.Revision}); err != nil {
			WriteControlProblem(w, r, http.StatusConflict, "external_deployment_snapshot_conflict", "immutable service snapshot conflicts with durable admission state")
			return
		}
		serviceCheckpointRefs = externalServiceCheckpointRefs(status.Snapshot.CheckpointRefs)
		if len(serviceCheckpointRefs) != 1 || serviceCheckpointRefs[0].Phase != "external_admission" || serviceCheckpointRefs[0].AttemptID != status.Snapshot.RetryLineage[len(status.Snapshot.RetryLineage)-1] {
			WriteControlProblem(w, r, http.StatusConflict, "external_deployment_snapshot_conflict", "service snapshot has no durable checkpoint references")
			return
		}
		if err := h.db.ClaimExternalDeploymentAdmissionEvidence(r.Context(), admission.ID, nonce.ID); err != nil {
			WriteControlProblem(w, r, http.StatusConflict, "external_deployment_nonce_consumed", "server-registered nonce is already claimed or unavailable")
			return
		}
	}
	verification, err := h.externalFleetDeploymentVerifier.VerifyExternalFleetDeployment(r.Context(), ExternalFleetDeploymentVerificationRequest{Receipt: receipt, CI: *principal.CI, Config: configured, AdmissionGeneration: admission.NonceGeneration, ServiceSnapshot: serviceSnapshot})
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
	// Persist the terminal operation and its exact remote-commit intent in the
	// same transaction. A crash after this point is receipt-free recoverable.
	opDigest, digestErr := externalOperationDigest(op)
	if digestErr != nil || serviceSnapshot == nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "external_deployment_store_failed", "failed to bind admitted operation")
		return
	}
	cleanupDigest, cleanupErr := externalCleanupIntentDigest(admission.ID, op.ID, opDigest, hex.EncodeToString(receiptHash[:]), serviceSnapshot.ID, serviceSnapshot.Ref, serviceSnapshot.SHA256)
	if cleanupErr != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "external_deployment_store_failed", "failed to bind cleanup intent")
		return
	}
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
	}(), CheckpointRefs: serviceCheckpointRefs, ServiceSnapshot: store.ExternalDeploymentServiceSnapshot{AdmissionID: admission.ID, SnapshotID: serviceSnapshot.ID, SnapshotRef: serviceSnapshot.Ref, SnapshotSHA256: serviceSnapshot.SHA256, RetryLineage: serviceSnapshot.RetryLineage, ReceiptDigest: serviceReceiptDigest, ProofDigest: serviceProofDigest, ClaimRevision: serviceClaimRevision, CleanupIntentSHA256: cleanupDigest}})
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
		if serviceSnapshot == nil || serviceClaimRevision < 1 || serviceProofDigest == "" {
			WriteControlProblem(w, r, http.StatusServiceUnavailable, "external_deployment_snapshot_unavailable", "claimed immutable service snapshot is unavailable")
			return
		}
		claim := ExternalFleetEvidenceClaim{SchemaVersion: "norn.external-fleet-admission-callback/v4", AdmissionID: admission.ID, LogicalDigest: digest, AdmissionContextDigest: strings.TrimPrefix(digest, "sha256:"), ReceiptDigest: hex.EncodeToString(receiptHash[:]), ProofDigest: serviceProofDigest, NonceSHA256: nonce.sha256(), Generation: admission.NonceGeneration, ExpectedRevision: serviceClaimRevision, OperationID: result.Operation.ID, OperationDigest: opDigest, CleanupIntentDigest: cleanupDigest}
		if err := h.db.RecordExternalDeploymentCommitPending(r.Context(), admission.ID, cleanupDigest); err != nil {
			WriteControlProblem(w, r, http.StatusServiceUnavailable, "external_deployment_cleanup_pending", "deployment committed but durable remote commit recovery could not be recorded")
			return
		}
		commitStatus, commitErr := registrar.CommitExternalFleetNonce(r.Context(), claim)
		if commitErr != nil {
			commitStatus, commitErr = registrar.GetExternalFleetAdmissionStatus(r.Context(), admission.ID, admission.NonceGeneration)
		}
		if commitErr != nil || !externalCommitStatusMatches(commitStatus, claim, serviceSnapshot) {
			_ = h.db.MarkExternalDeploymentAdmissionCleanupPending(r.Context(), admission.ID)
			WriteControlProblem(w, r, http.StatusServiceUnavailable, "external_deployment_cleanup_pending", "deployment committed but evidence nonce cleanup is pending; retry the exact request")
			return
		}
		if err := h.db.RecordExternalDeploymentCommitBinding(r.Context(), store.ExternalDeploymentServiceSnapshot{AdmissionID: admission.ID, CommitRevision: commitStatus.Revision, CleanupIntentSHA256: cleanupDigest}); err != nil {
			WriteControlProblem(w, r, http.StatusServiceUnavailable, "external_deployment_cleanup_pending", "deployment committed but service commit binding could not be recorded")
			return
		}
		// Remote nonce commit is not proof that Fleet's migration-only cleanup
		// has completed. Keep this server-owned admission in cleanup_pending
		// until the dedicated absence-proof endpoint records that final phase.
		if err := h.db.MarkExternalDeploymentAdmissionCleanupPending(r.Context(), admission.ID); err != nil {
			WriteControlProblem(w, r, http.StatusServiceUnavailable, "external_deployment_cleanup_pending", "deployment committed but cleanup state could not be recorded; retry the exact request")
			return
		}
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
	if !validDistinctIngressNodes(verified.IngressNodeIDs) || !validHTTPSVersion(verified.PublicHTTPSVersion) || !validPrivateReadinessAt(verified.PrivateReadiness, now) || !validExternalChronologyAt(verified.Chronology, now) {
		return fmt.Errorf("independent verifier did not prove two distinct ingress nodes, public HTTPS version, private readiness, and server chronology")
	}
	return nil
}

func validExternalNomadJobProof(proof ExternalFleetNomadJobProof, jobID, hclSHA256 string) bool {
	return proof.JobID == jobID && proof.HCLSHA256 == hclSHA256 && uuid.Validate(proof.EvalID) == nil && proof.JobModifyIndex > 0 && externalNamePattern.MatchString(proof.CheckpointID)
}

func validExternalNomadJobProofV4(proof ExternalFleetNomadJobProof, jobID, hclSHA256 string) bool {
	if !validExternalNomadJobProof(proof, jobID, hclSHA256) || proof.EvalCreateIndex == 0 || proof.EvalJobModifyIndex != proof.JobModifyIndex || proof.JobCreateIndex == 0 || !sha256HexPattern.MatchString(proof.CurrentSpecSHA256) || !sha256HexPattern.MatchString(proof.SubmissionSHA256) || len(proof.EvaluationChainIDs) == 0 || len(proof.EvaluationChainIDs) > 32 {
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

func externalServiceCheckpointRefs(refs []ExternalFleetCheckpointRef) []store.ExternalDeploymentCheckpointRef {
	if len(refs) == 0 || len(refs) > 32 {
		return nil
	}
	result := make([]store.ExternalDeploymentCheckpointRef, 0, len(refs))
	seen := map[string]struct{}{}
	allowed := map[string]struct{}{"prepare": {}, "migration": {}, "runtime": {}, "exercise": {}, "external_admission": {}}
	for _, ref := range refs {
		if _, permitted := allowed[ref.Phase]; !permitted || ref.CheckpointID == "" || ref.AttemptID == "" || ref.EvidenceRef == "" || !sha256HexPattern.MatchString(ref.EvidenceSHA256) {
			return nil
		}
		if _, duplicate := seen[ref.Phase]; duplicate {
			return nil
		}
		seen[ref.Phase] = struct{}{}
		result = append(result, store.ExternalDeploymentCheckpointRef{Phase: ref.Phase, CheckpointID: ref.CheckpointID, AttemptID: ref.AttemptID, EvidenceRef: ref.EvidenceRef, EvidenceSHA256: ref.EvidenceSHA256})
	}
	return result
}

func validExternalRetryLineage(lineage []string) bool {
	if len(lineage) == 0 || len(lineage) > 64 {
		return false
	}
	seen := map[string]struct{}{}
	for _, attempt := range lineage {
		if attempt == "" || len(attempt) > 256 {
			return false
		}
		if _, duplicate := seen[attempt]; duplicate {
			return false
		}
		seen[attempt] = struct{}{}
	}
	return true
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
		if step.Phase != phase || step.OccurredAt.IsZero() || step.OccurredAt.After(now) || !validExternalURI(step.EvidenceRef) || (index > 0 && !step.OccurredAt.After(steps[index-1].OccurredAt)) {
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

// externalAdmissionProofDigest deliberately has a different domain from the
// receipt digest.  It binds the signed/material deployment proof without
// treating a transport envelope (or its one-use nonce) as evidence content.
func externalAdmissionProofDigest(receipt ExternalFleetDeploymentReceipt) (string, error) {
	proof := struct {
		Schema      string                      `json:"schemaVersion"`
		Source      string                      `json:"sourceSha"`
		Artifact    string                      `json:"artifact"`
		Candidate   model.ReleaseCandidate      `json:"candidate"`
		Attestation string                      `json:"attestationBundleSha256"`
		SBOM        string                      `json:"sbomBundleSha256"`
		Fleet       ExternalFleetExecutionProof `json:"fleet"`
	}{externalFleetReceiptSchemaV4, receipt.SourceSHA, receipt.Artifact, receipt.Candidate, receipt.AttestationBundleSHA256, receipt.SBOMBundleSHA256, receipt.Fleet}
	b, err := json.Marshal(proof)
	if err != nil {
		return "", err
	}
	d := sha256.Sum256(b)
	return hex.EncodeToString(d[:]), nil
}

func externalOperationDigest(op *model.Operation) (string, error) {
	if op == nil {
		return "", errors.New("missing operation")
	}
	v := struct {
		ID, Kind, App, Ref, Source string
		Payload                    map[string]interface{} `json:"payload"`
	}{op.ID, op.Kind, op.App, op.Ref, op.Source, op.Payload}
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	d := sha256.Sum256(b)
	return hex.EncodeToString(d[:]), nil
}

func externalCleanupIntentDigest(admissionID, operationID, operationDigest, receiptDigest, snapshotID, snapshotRef, snapshotSHA string) (string, error) {
	v := struct{ Schema, AdmissionID, OperationID, OperationDigest, ReceiptDigest, SnapshotID, SnapshotRef, SnapshotSHA256 string }{
		"norn.external-fleet-cleanup-intent/v4", admissionID, operationID, operationDigest, receiptDigest, snapshotID, snapshotRef, snapshotSHA,
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	d := sha256.Sum256(b)
	return hex.EncodeToString(d[:]), nil
}

// externalFleetSnapshotDigest is the v4 snapshot hash profile shared with the
// evidence service. It hashes this compact JSON projection, excluding only the
// digest field itself. The projection has no maps and therefore makes nested
// field names and array order explicit across implementations.
func externalFleetSnapshotDigest(snapshot *ExternalFleetEvidenceSnapshot) (string, error) {
	if snapshot == nil {
		return "", errors.New("missing evidence snapshot")
	}
	projection := struct {
		ID             string                              `json:"id"`
		Ref            string                              `json:"ref"`
		LiveCheckedAt  time.Time                           `json:"liveCheckedAt"`
		NonceWrittenAt time.Time                           `json:"nonceWrittenAt"`
		NonceReadAt    time.Time                           `json:"nonceReadAt"`
		Verification   ExternalFleetDeploymentVerification `json:"verification"`
		RetryLineage   []string                            `json:"retryLineage"`
		CheckpointRefs []ExternalFleetCheckpointRef        `json:"checkpointRefs"`
		Allocations    []externalFleetAllocationEvidence   `json:"allocations"`
	}{snapshot.ID, snapshot.Ref, snapshot.LiveCheckedAt, snapshot.NonceWrittenAt, snapshot.NonceReadAt, snapshot.Verification, snapshot.RetryLineage, snapshot.CheckpointRefs, snapshot.Allocations}
	b, err := json.Marshal(projection)
	if err != nil {
		return "", err
	}
	d := sha256.Sum256(b)
	return hex.EncodeToString(d[:]), nil
}

func externalRegistrationStatusMatches(status *ExternalFleetEvidenceAdmissionStatus, registration ExternalFleetEvidenceRegistration) bool {
	return status != nil && status.SchemaVersion == "norn.external-fleet-admission-status/v4" &&
		status.AdmissionID == registration.AdmissionID && status.LogicalDigest == registration.LogicalDigest &&
		status.AdmissionContextDigest == registration.AdmissionContextDigest && status.NonceSHA256 == registration.NonceSHA256 &&
		status.Generation == registration.Generation && status.State == "registered" && status.Revision == registration.ExpectedRevision+1 &&
		status.Snapshot == nil
}

func externalClaimStatusMatches(status *ExternalFleetEvidenceAdmissionStatus, claim ExternalFleetEvidenceClaim) bool {
	if status == nil || status.SchemaVersion != "norn.external-fleet-admission-status/v4" || status.AdmissionID != claim.AdmissionID || status.LogicalDigest != claim.LogicalDigest || status.AdmissionContextDigest != claim.AdmissionContextDigest || status.NonceSHA256 != claim.NonceSHA256 || status.Generation != claim.Generation || status.State != "claimed" || status.Revision != claim.ExpectedRevision+1 || status.ReceiptDigest != claim.ReceiptDigest || status.ProofDigest != claim.ProofDigest || status.Snapshot == nil {
		return false
	}
	s := status.Snapshot
	actual, err := externalFleetSnapshotDigest(s)
	return err == nil && s.ID != "" && s.Ref != "" && actual == s.SHA256 && !s.LiveCheckedAt.IsZero() && len(s.RetryLineage) > 0 && len(s.CheckpointRefs) > 0
}

func externalCommitStatusMatches(status *ExternalFleetEvidenceAdmissionStatus, claim ExternalFleetEvidenceClaim, snapshot *ExternalFleetEvidenceSnapshot) bool {
	return status != nil && snapshot != nil && status.SchemaVersion == "norn.external-fleet-admission-status/v4" && status.AdmissionID == claim.AdmissionID && status.LogicalDigest == claim.LogicalDigest && status.AdmissionContextDigest == claim.AdmissionContextDigest && status.NonceSHA256 == claim.NonceSHA256 && status.Generation == claim.Generation && status.State == "committed" && status.Revision == claim.ExpectedRevision+1 && status.ReceiptDigest == claim.ReceiptDigest && status.ProofDigest == claim.ProofDigest && status.OperationID == claim.OperationID && status.OperationDigest == claim.OperationDigest && status.CleanupIntentDigest == claim.CleanupIntentDigest && status.Snapshot != nil && status.Snapshot.ID == snapshot.ID && status.Snapshot.Ref == snapshot.Ref && status.Snapshot.SHA256 == snapshot.SHA256
}
