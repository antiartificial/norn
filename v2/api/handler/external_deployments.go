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
	"unicode/utf8"

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

type externalFleetRecoveredNonce struct {
	ID     string
	SHA256 string
}

type externalFleetRecoveredNonceContextKey struct{}

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
	JobID              string `json:"jobId"`
	HCLSHA256          string `json:"hclSha256"`
	EvalID             string `json:"evalId"`
	EvalCreateIndex    uint64 `json:"evalCreateIndex,omitempty"`
	EvalJobModifyIndex uint64 `json:"evalJobModifyIndex,omitempty"`
	JobCreateIndex     uint64 `json:"jobCreateIndex,omitempty"`
	JobModifyIndex     uint64 `json:"jobModifyIndex"`
	// JobVersion is required even when Nomad's valid initial version is zero.
	JobVersion uint64 `json:"jobVersion"`
	// CurrentSpec is the exact JSON returned by Nomad job inspect. Norn hashes
	// a narrowly documented projection rather than trusting a Fleet-supplied
	// digest: only the eight volatile root fields below are removed.
	CurrentSpec       json.RawMessage `json:"currentSpec,omitempty"`
	CurrentSpecSHA256 string          `json:"currentSpecSha256,omitempty"`
	// Submission is the exact JSON returned by Nomad's versioned submission
	// endpoint. Its full canonical object is bound, including source and
	// variables; no fields are stripped from this evidence.
	Submission         json.RawMessage `json:"submission,omitempty"`
	SubmissionSHA256   string          `json:"submissionSha256,omitempty"`
	EvaluationChainIDs []string        `json:"evaluationChainIds,omitempty"`
	CheckpointID       string          `json:"checkpointId"`
	// jobVersionPresent is intentionally not serialized. JSON number zero is a
	// valid Nomad version, so v4 validation must distinguish it from omission.
	jobVersionPresent bool
}

// UnmarshalJSON preserves the required-field distinction that Go's uint64
// decoder otherwise erases: missing and JSON null are not version zero. V3
// receipts remain parseable, but v4 validation requires this bit.
func (proof *ExternalFleetNomadJobProof) UnmarshalJSON(raw []byte) error {
	type wire ExternalFleetNomadJobProof
	var value wire
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&fields); err != nil {
		return err
	}
	versionRaw, present := fields["jobVersion"]
	if present {
		var number json.Number
		versionDecoder := json.NewDecoder(bytes.NewReader(versionRaw))
		versionDecoder.UseNumber()
		if err := versionDecoder.Decode(&number); err != nil || !validExternalFleetCanonicalInteger(number.String()) {
			return errors.New("jobVersion must be an unsigned decimal integer")
		}
		version, err := strconv.ParseUint(number.String(), 10, 64)
		if err != nil {
			return errors.New("jobVersion is outside uint64")
		}
		value.JobVersion = version
	}
	*proof = ExternalFleetNomadJobProof(value)
	proof.jobVersionPresent = present
	return nil
}

func (proof *ExternalFleetNomadJobProof) markJobVersionPresent() {
	proof.jobVersionPresent = true
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
	// NonceSHA256 is populated only by protected receipt-free recovery after
	// loading the original digest from Norn's nonce row. Normal Actions calls
	// leave it empty and derive the digest from the one-use raw receipt nonce.
	NonceSHA256 string
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
	if h.db == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "external_deployment_registration_unavailable", "Norn-owned evidence nonce registration is not configured")
		return
	}
	var request externalFleetAdmissionBeginRequest
	if err := decodeControlJSONLimit(w, r, &request, maxReleaseEvidenceJSONBody); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_external_deployment_request", err.Error())
		return
	}
	key, digest, ok := externalFleetAdmissionIdentityIdempotency(w, r, principal, appID, request.LogicalIdentity)
	if !ok {
		return
	}
	if existing, lookupErr := h.db.FindExternalDeploymentAdmissionByIdempotency(r.Context(), key, digest, appID, principal.Environment, principal.CI.Repository); lookupErr == nil && externalDeploymentAdmissionTerminal(existing.State) {
		h.writeExternalFleetTerminalAdmissionReplay(w, existing)
		return
	} else if errors.Is(lookupErr, store.ErrExternalDeploymentIdempotencyConflict) {
		WriteControlProblem(w, r, http.StatusConflict, "idempotency_key_reused", "Idempotency-Key was already used for a different external admission")
		return
	}
	configured, err := h.externalFleetAdmissionConfig(appID)
	if err != nil {
		WriteControlProblem(w, r, http.StatusConflict, "external_deployment_unavailable", err.Error())
		return
	}
	if err := h.validateExternalFleetLogicalIdentity(appID, configured, request.LogicalIdentity); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_external_deployment_identity", err.Error())
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
	if externalDeploymentAdmissionTerminal(admission.State) {
		h.writeExternalFleetTerminalAdmissionReplay(w, admission)
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

func externalDeploymentAdmissionTerminal(state store.ExternalDeploymentAdmissionState) bool {
	return state == store.ExternalDeploymentAdmissionCommitted || state == store.ExternalDeploymentAdmissionCleanupPending || state == store.ExternalDeploymentAdmissionComplete
}

func (h *Handler) writeExternalFleetTerminalAdmissionReplay(w http.ResponseWriter, admission *store.ExternalDeploymentAdmissionLifecycle) {
	cleanupState := "pending"
	if admission.State == store.ExternalDeploymentAdmissionComplete {
		cleanupState = "complete"
	}
	writeJSON(w, externalFleetAdmissionResponse{SchemaVersion: "norn.external-fleet-admission/v4", AdmissionID: admission.ID, State: string(admission.State), NonceGeneration: admission.NonceGeneration, OperationID: admission.OperationID, CleanupState: cleanupState})
}

// AdmitExternalFleetDeploymentV4 accepts only the v4 receipt envelope. No
// request can issue an unregistered nonce or submit a v3 receipt.
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
	if h.db == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "external_deployment_store_unavailable", "durable external admission storage is unavailable")
		return
	}
	var request externalFleetAdmissionResumeRequest
	if err := decodeControlJSONLimit(w, r, &request, maxReleaseEvidenceJSONBody); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_external_deployment_resume", err.Error())
		return
	}
	if uuid.Validate(request.AdmissionID) != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_external_deployment_resume", "resume requires a valid admissionId and unchanged logical identity")
		return
	}
	key, digest, valid := externalFleetAdmissionIdentityIdempotency(w, r, principal, appID, request.LogicalIdentity)
	if !valid {
		return
	}
	admission, err := h.db.GetExternalDeploymentAdmission(r.Context(), request.AdmissionID, appID, principal.Environment, principal.CI.Repository)
	if err != nil || admission.RequestDigest != digest || admission.IdempotencyKey != key {
		WriteControlProblem(w, r, http.StatusConflict, "external_deployment_admission_conflict", "resume identity does not match the server-owned admission")
		return
	}
	if externalDeploymentAdmissionTerminal(admission.State) {
		h.writeExternalFleetTerminalAdmissionReplay(w, admission)
		return
	}
	configured, err := h.externalFleetAdmissionConfig(appID)
	if err != nil || h.validateExternalFleetLogicalIdentity(appID, configured, request.LogicalIdentity) != nil {
		WriteControlProblem(w, r, http.StatusConflict, "external_deployment_unavailable", "resume requires currently valid external admission configuration")
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
	if !ok {
		return
	}
	if key := strings.TrimSpace(r.Header.Get("Idempotency-Key")); key == "" || len(key) > 200 {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_idempotency_key", "cleanup requires the original Idempotency-Key")
		return
	}
	if h.db == nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "external_deployment_store_unavailable", "durable external admission storage is unavailable")
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
	// Cleanup is receipt-free, so scope the supplied client key exactly as begin
	// did and compare that durable identity before any terminal replay or remote
	// cleanup status lookup.
	scopedKey, scopedKeyOK := externalFleetAdmissionScopedIdempotency(r, principal, appID)
	if !scopedKeyOK {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_idempotency_key", "cleanup requires the original scoped Idempotency-Key")
		return
	}
	var state, operationID, receiptDigest, proofDigest, expectedIntent, expectedAbsence, snapshotID, snapshotRef, snapshotSHA string
	var nonceGeneration, claimRevision, commitRevision, cleanupRevision int64
	var persistedKey string
	err := h.db.Pool.QueryRow(r.Context(), `SELECT a.state, COALESCE(a.operation_id,''), a.idempotency_key, a.nonce_generation, COALESCE(o.metadata->>'externalFleetProofSHA256',''), COALESCE(a.service_proof_sha256,''), COALESCE(a.cleanup_intent_sha256,''), COALESCE(a.absence_proof_sha256,''), COALESCE(a.service_snapshot_id,''), COALESCE(a.service_snapshot_ref,''), COALESCE(a.service_snapshot_sha256,''), COALESCE(a.service_claim_revision,0), COALESCE(a.service_commit_revision,0), COALESCE(a.service_cleanup_revision,0) FROM external_deployment_admissions a LEFT JOIN operations o ON o.id=a.operation_id WHERE a.id=$1 AND a.app=$2 AND a.environment=$3 AND a.ci_repository=$4`, request.AdmissionID, appID, principal.Environment, principal.CI.Repository).Scan(&state, &operationID, &persistedKey, &nonceGeneration, &receiptDigest, &proofDigest, &expectedIntent, &expectedAbsence, &snapshotID, &snapshotRef, &snapshotSHA, &claimRevision, &commitRevision, &cleanupRevision)
	if err != nil || persistedKey != scopedKey || operationID != request.OperationID || receiptDigest != request.ReceiptDigest {
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
		writeJSON(w, externalFleetAdmissionResponse{SchemaVersion: "norn.external-fleet-admission/v4", AdmissionID: request.AdmissionID, State: state, NonceGeneration: nonceGeneration, OperationID: operationID})
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
	writeJSON(w, externalFleetAdmissionResponse{SchemaVersion: "norn.external-fleet-admission/v4", AdmissionID: request.AdmissionID, State: string(store.ExternalDeploymentAdmissionComplete), NonceGeneration: nonceGeneration, OperationID: operationID})
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
	if err != nil {
		WriteControlProblem(w, r, http.StatusConflict, "external_deployment_reconcile_conflict", "admission is not a durable pending remote commit")
		return
	}
	scopedKey, scopedKeyOK := externalFleetAdmissionScopedIdempotency(r, principal, appID)
	if !scopedKeyOK || admission.IdempotencyKey != scopedKey {
		WriteControlProblem(w, r, http.StatusConflict, "external_deployment_reconcile_conflict", "reconcile does not match the original scoped Idempotency-Key")
		return
	}
	if admission.State == store.ExternalDeploymentAdmissionNonceReady || admission.State == store.ExternalDeploymentAdmissionEvidenceClaimed {
		h.recoverClaimedExternalFleetAdmission(w, r, admission)
		return
	}
	if (admission.State != store.ExternalDeploymentAdmissionCommitted && admission.State != store.ExternalDeploymentAdmissionCleanupPending) || admission.OperationID == "" {
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
		writeJSON(w, externalFleetAdmissionResponse{SchemaVersion: "norn.external-fleet-admission/v4", AdmissionID: admission.ID, State: string(store.ExternalDeploymentAdmissionCleanupPending), NonceGeneration: admission.NonceGeneration, OperationID: admission.OperationID, CleanupState: "pending"})
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
			writeJSON(w, externalFleetAdmissionResponse{SchemaVersion: "norn.external-fleet-admission/v4", AdmissionID: admission.ID, State: string(store.ExternalDeploymentAdmissionCleanupPending), NonceGeneration: admission.NonceGeneration, OperationID: admission.OperationID, CleanupState: "pending"})
			return
		}
	}
	committed, commitErr := registrar.CommitExternalFleetNonce(r.Context(), claim)
	if commitErr != nil || !externalCommitStatusMatches(committed, claim, snapshot) || h.db.RecordExternalDeploymentCommitBinding(r.Context(), store.ExternalDeploymentServiceSnapshot{AdmissionID: admission.ID, CommitRevision: committed.Revision, CleanupIntentSHA256: cleanupIntent}) != nil || h.db.MarkExternalDeploymentAdmissionCleanupPending(r.Context(), admission.ID) != nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "external_deployment_reconcile_unavailable", "remote commit remains pending")
		return
	}
	writeJSON(w, externalFleetAdmissionResponse{SchemaVersion: "norn.external-fleet-admission/v4", AdmissionID: admission.ID, State: string(store.ExternalDeploymentAdmissionCleanupPending), NonceGeneration: admission.NonceGeneration, OperationID: admission.OperationID, CleanupState: "pending"})
}

// recoverClaimedExternalFleetAdmission resumes a pre-terminal claim after any
// crash boundary. The database supplies the exact redacted receipt and nonce
// hash; the owner-only callback replays/gets the immutable claim snapshot.
// This path never reconstructs, logs, or asks Actions to resubmit raw nonce
// material, and it deliberately delegates to the normal verifier/terminal
// transaction rather than creating a weaker recovery-only admission path.
func (h *Handler) recoverClaimedExternalFleetAdmission(w http.ResponseWriter, r *http.Request, admission *store.ExternalDeploymentAdmissionLifecycle) {
	var claimedReceipt []byte
	var receiptDigest, proofDigest, nonceSHA256 string
	err := h.db.Pool.QueryRow(r.Context(), `SELECT claimed_receipt, service_receipt_sha256, service_proof_sha256, nonce.nonce_sha256
		FROM external_deployment_admissions admission JOIN external_deployment_nonces nonce ON nonce.id=admission.nonce_id
		WHERE admission.id=$1 AND admission.nonce_generation=nonce.registration_generation`, admission.ID).Scan(&claimedReceipt, &receiptDigest, &proofDigest, &nonceSHA256)
	if err != nil || len(claimedReceipt) == 0 || !sha256HexPattern.MatchString(receiptDigest) || !sha256HexPattern.MatchString(proofDigest) || !sha256HexPattern.MatchString(nonceSHA256) {
		WriteControlProblem(w, r, http.StatusConflict, "external_deployment_reconcile_conflict", "claimed admission has no exact durable recovery envelope")
		return
	}
	var receipt ExternalFleetDeploymentReceipt
	if err := json.Unmarshal(claimedReceipt, &receipt); err != nil || receipt.Nonce != "" || receipt.AdmissionID != admission.ID {
		WriteControlProblem(w, r, http.StatusConflict, "external_deployment_reconcile_conflict", "claimed admission recovery envelope is invalid")
		return
	}
	canonical, canonicalErr := externalReceiptCanonicalJSON(receipt)
	proof, proofErr := externalAdmissionProofDigest(receipt)
	if canonicalErr != nil || proofErr != nil || externalSHA256(canonical) != receiptDigest || proof != proofDigest {
		WriteControlProblem(w, r, http.StatusConflict, "external_deployment_reconcile_conflict", "claimed admission recovery digests drifted")
		return
	}
	registrar, registered := h.externalFleetDeploymentVerifier.(ExternalFleetEvidenceRegistrationClient)
	registration, registrationErr := h.db.GetExternalDeploymentNonceRegistration(r.Context(), admission.ID, admission.NonceID)
	if !registered || registrationErr != nil || registration.Generation != admission.NonceGeneration || registration.NonceSHA256 != nonceSHA256 || registration.ServiceRevision < 1 {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "external_deployment_reconcile_unavailable", "claimed admission registration cannot be reconstructed")
		return
	}
	claim := ExternalFleetEvidenceClaim{SchemaVersion: "norn.external-fleet-admission-callback/v4", AdmissionID: admission.ID, LogicalDigest: admission.RequestDigest, AdmissionContextDigest: strings.TrimPrefix(admission.RequestDigest, "sha256:"), ReceiptDigest: receiptDigest, ProofDigest: proofDigest, NonceSHA256: nonceSHA256, Generation: admission.NonceGeneration, ExpectedRevision: registration.ServiceRevision}
	status, statusErr := registrar.GetExternalFleetAdmissionStatus(r.Context(), admission.ID, admission.NonceGeneration)
	if statusErr == nil && status.State == "registered" {
		status, statusErr = registrar.ClaimExternalFleetNonce(r.Context(), claim)
		if statusErr != nil {
			status, statusErr = registrar.GetExternalFleetAdmissionStatus(r.Context(), admission.ID, admission.NonceGeneration)
		}
	}
	if statusErr != nil || !externalClaimStatusMatches(status, claim) {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "external_deployment_reconcile_unavailable", "claimed admission snapshot cannot be reconciled")
		return
	}
	if err := h.db.RecordExternalDeploymentServiceSnapshot(r.Context(), store.ExternalDeploymentServiceSnapshot{AdmissionID: admission.ID, SnapshotID: status.Snapshot.ID, SnapshotRef: status.Snapshot.Ref, SnapshotSHA256: status.Snapshot.SHA256, RetryLineage: status.Snapshot.RetryLineage, ReceiptDigest: receiptDigest, ProofDigest: proofDigest, ClaimRevision: status.Revision}); err != nil || h.db.ClaimExternalDeploymentAdmissionEvidence(r.Context(), admission.ID, admission.NonceID) != nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "external_deployment_reconcile_unavailable", "claimed admission state could not be durably reconciled")
		return
	}
	// The persisted envelope intentionally excluded raw Nomad transport bytes.
	// Rehydrate them only from the exact immutable owner snapshot so the normal
	// v4 validator can recompute its two canonical evidence digests.
	receipt.Fleet.Migration.CurrentSpec = status.Snapshot.Verification.Migration.CurrentSpec
	receipt.Fleet.Migration.Submission = status.Snapshot.Verification.Migration.Submission
	receipt.Fleet.Runtime.CurrentSpec = status.Snapshot.Verification.Runtime.CurrentSpec
	receipt.Fleet.Runtime.Submission = status.Snapshot.Verification.Runtime.Submission
	// The normal parser still requires a syntactically valid nonce envelope.
	// Its digest is overridden from the protected store context before any
	// binding check, so these zeros can never authorize or replace the nonce.
	receipt.Nonce = admission.NonceID + "." + strings.Repeat("0", 64)
	body, marshalErr := json.Marshal(externalDeploymentRequest{Receipt: &receipt})
	if marshalErr != nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "external_deployment_reconcile_unavailable", "claimed admission recovery could not be encoded")
		return
	}
	ctx := context.WithValue(r.Context(), externalFleetRecoveredNonceContextKey{}, externalFleetRecoveredNonce{ID: admission.NonceID, SHA256: nonceSHA256})
	replay := r.Clone(ctx)
	replay.Body = io.NopCloser(bytes.NewReader(body))
	replay.ContentLength = int64(len(body))
	h.AdmitExternalFleetDeployment(w, replay)
}

// AdmitExternalFleetDeployment is retained as a deprecated internal alias for
// tests and old in-process callers. Routes use AdmitExternalFleetDeploymentV4.
// It accepts only a v4 receipt or a receipt-free terminal replay.
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
	var request externalDeploymentRequest
	if err := decodeControlJSONLimit(w, r, &request, maxReleaseEvidenceJSONBody); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_external_deployment_request", err.Error())
		return
	}
	// Receipt-free terminal replay is intentionally before current config,
	// receipt, nonce, GitHub, or freshness checks. The admission ID plus the
	// scoped idempotency key selects only one durable server operation.
	if request.Action != "" || (request.Receipt == nil) == (request.AdmissionID == "") {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_external_deployment_request", "external deployment admission requires exactly one of receipt or admissionId")
		return
	}
	if request.AdmissionID != "" {
		if uuid.Validate(request.AdmissionID) != nil {
			WriteControlProblem(w, r, http.StatusBadRequest, "invalid_external_deployment_request", "admissionId must be a UUID")
			return
		}
		scopedKey, scopedKeyOK := externalFleetAdmissionScopedIdempotency(r, principal, appID)
		admission, lookupErr := h.db.GetExternalDeploymentAdmission(r.Context(), request.AdmissionID, appID, principal.Environment, principal.CI.Repository)
		if !scopedKeyOK || lookupErr != nil || admission.IdempotencyKey != scopedKey || (admission.State != store.ExternalDeploymentAdmissionCommitted && admission.State != store.ExternalDeploymentAdmissionCleanupPending && admission.State != store.ExternalDeploymentAdmissionComplete) {
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
	if recovered, recoveredOK := r.Context().Value(externalFleetRecoveredNonceContextKey{}).(externalFleetRecoveredNonce); recoveredOK && receipt.Nonce != "" && uuid.Validate(recovered.ID) == nil && sha256HexPattern.MatchString(recovered.SHA256) {
		nonce = externalAdmissionNonce{ID: recovered.ID, Digest: recovered.SHA256}
		err = nil
	}
	if err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "external_deployment_nonce_invalid", err.Error())
		return
	}
	if externalValueContainsNonce(redactExternalFleetReceipt(receipt), nonce) {
		WriteControlProblem(w, r, http.StatusBadRequest, "external_deployment_nonce_leak", "receipt fields must not contain the raw Norn nonce or its secret")
		return
	}
	_, recoveredClaim := r.Context().Value(externalFleetRecoveredNonceContextKey{}).(externalFleetRecoveredNonce)
	if !externalReceiptMatchesCI(receipt, *principal.CI) && !recoveredClaim {
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
		// Persist the nonce-redacted canonical envelope before the remote CAS.
		// A process crash or lost response can then recover the exact claim with
		// the owner credential and nonce hash, without asking Actions for raw
		// one-use material a second time.
		if err := h.db.RecordExternalDeploymentClaimedEvidence(r.Context(), admission.ID, receiptBytes, claimRequest.ReceiptDigest, claimRequest.ProofDigest); err != nil {
			WriteControlProblem(w, r, http.StatusConflict, "external_deployment_snapshot_conflict", "claimed receipt conflicts with durable admission state")
			return
		}
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
	requestNonceSHA256 := ""
	if recovered, recoveredOK := r.Context().Value(externalFleetRecoveredNonceContextKey{}).(externalFleetRecoveredNonce); recoveredOK {
		requestNonceSHA256 = recovered.SHA256
	}
	verification, err := h.externalFleetDeploymentVerifier.VerifyExternalFleetDeployment(r.Context(), ExternalFleetDeploymentVerificationRequest{Receipt: receipt, CI: *principal.CI, Config: configured, AdmissionGeneration: admission.NonceGeneration, ServiceSnapshot: serviceSnapshot, NonceSHA256: requestNonceSHA256})
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
	key, ok := externalFleetAdmissionScopedIdempotency(r, principal, appID)
	if !ok {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_idempotency_key", "a stable Idempotency-Key and authorized CI repository/environment are required")
		return "", "", false
	}
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
	return key, "sha256:" + hex.EncodeToString(digest[:]), true
}

// externalFleetAdmissionScopedIdempotency is intentionally independent of
// mutable receipt/configuration data. Resume, cleanup, and terminal replay
// can therefore derive the exact original key from the authenticated route
// scope, while any changed caller key fails before creating or replaying work.
func externalFleetAdmissionScopedIdempotency(r *http.Request, principal AccessPrincipal, appID string) (string, bool) {
	clientKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if clientKey == "" || len(clientKey) > 200 || principal.CI == nil || principal.CI.Repository == "" || principal.Environment == "" || appID == "" {
		return "", false
	}
	keySum := sha256.Sum256([]byte("external-fleet-admission\x00" + principal.CI.Repository + "\x00" + principal.Environment + "\x00" + appID + "\x00" + clientKey))
	return "app.deploy:" + hex.EncodeToString(keySum[:]), true
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

type externalAdmissionNonce struct {
	ID     string
	Secret string
	// Digest is used only by receipt-free owner recovery. It is the durable
	// SHA-256 of the original raw nonce and never permits reconstructing it.
	Digest string
}

func (n externalAdmissionNonce) String() string { return n.ID + "." + n.Secret }
func (n externalAdmissionNonce) sha256() string {
	if n.Digest != "" {
		return n.Digest
	}
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
	return validExternalNomadJobProofV4(receipt.Fleet.Migration, configured.MigrationJobID, configured.MigrationHCLSHA256, receipt.Fleet.Namespace) && validExternalNomadJobProofV4(receipt.Fleet.Runtime, configured.RuntimeJobID, configured.RuntimeHCLSHA256, receipt.Fleet.Namespace)
}

func verificationMatchesExternalReceipt(verified ExternalFleetDeploymentVerification, receipt ExternalFleetDeploymentReceipt, configured ExternalFleetAdmissionConfig) error {
	return verificationMatchesExternalReceiptAt(verified, receipt, configured, time.Now().UTC())
}

func verificationMatchesExternalReceiptAt(verified ExternalFleetDeploymentVerification, receipt ExternalFleetDeploymentReceipt, configured ExternalFleetAdmissionConfig, now time.Time) error {
	if verified.SourceSHA != receipt.SourceSHA || verified.Artifact != receipt.Artifact || verified.AttestationBundleSHA256 != receipt.AttestationBundleSHA256 || verified.SBOMBundleSHA256 != receipt.SBOMBundleSHA256 || verified.Namespace != configured.Namespace || !externalNomadProofStableMatch(verified.Migration, receipt.Fleet.Migration) || !externalNomadProofStableMatch(verified.Runtime, receipt.Fleet.Runtime) || verified.PlanID != receipt.Fleet.PlanID || verified.ApplyRunID != receipt.Fleet.ApplyRunID || verified.ApplyRunAttempt != receipt.Fleet.ApplyRunAttempt || verified.PlanSHA256 != receipt.Fleet.PlanSHA256 || verified.RunnerAttemptID != receipt.Fleet.RunnerAttemptID || verified.FleetCommit != receipt.Fleet.FleetCommit || verified.NonceEvidenceRef != receipt.Fleet.NonceEvidenceRef {
		return fmt.Errorf("independent verifier observations do not exactly match the receipt")
	}
	if !validDistinctIngressNodes(verified.IngressNodeIDs) || !validHTTPSVersion(verified.PublicHTTPSVersion) || !validPrivateReadinessAt(verified.PrivateReadiness, now) || !validExternalChronologyAt(verified.Chronology, now) {
		return fmt.Errorf("independent verifier did not prove two distinct ingress nodes, public HTTPS version, private readiness, and server chronology")
	}
	return nil
}

// externalNomadProofStableMatch deliberately compares the complete stable
// evidence binding but not the raw JSON transport spelling. Nomad may alter
// volatile inspect fields between Fleet's observation and Norn's verification;
// CurrentSpecSHA256 and SubmissionSHA256 bind their documented canonical bytes.
func externalNomadProofStableMatch(observed, receipt ExternalFleetNomadJobProof) bool {
	return observed.JobID == receipt.JobID &&
		observed.HCLSHA256 == receipt.HCLSHA256 &&
		observed.EvalID == receipt.EvalID &&
		observed.EvalCreateIndex == receipt.EvalCreateIndex &&
		observed.EvalJobModifyIndex == receipt.EvalJobModifyIndex &&
		observed.JobCreateIndex == receipt.JobCreateIndex &&
		observed.JobModifyIndex == receipt.JobModifyIndex &&
		observed.JobVersion == receipt.JobVersion &&
		observed.CurrentSpecSHA256 == receipt.CurrentSpecSHA256 &&
		observed.SubmissionSHA256 == receipt.SubmissionSHA256 &&
		observed.CheckpointID == receipt.CheckpointID &&
		reflect.DeepEqual(observed.EvaluationChainIDs, receipt.EvaluationChainIDs)
}

func validExternalNomadJobProof(proof ExternalFleetNomadJobProof, jobID, hclSHA256 string) bool {
	return proof.JobID == jobID && proof.HCLSHA256 == hclSHA256 && uuid.Validate(proof.EvalID) == nil && proof.JobModifyIndex > 0 && externalNamePattern.MatchString(proof.CheckpointID)
}

func validExternalNomadJobProofV4(proof ExternalFleetNomadJobProof, jobID, hclSHA256, namespace string) bool {
	if !validExternalNomadJobProof(proof, jobID, hclSHA256) || !proof.jobVersionPresent || proof.EvalCreateIndex == 0 || proof.EvalJobModifyIndex != proof.JobModifyIndex || proof.JobCreateIndex == 0 || !sha256HexPattern.MatchString(proof.CurrentSpecSHA256) || !sha256HexPattern.MatchString(proof.SubmissionSHA256) || len(proof.EvaluationChainIDs) == 0 || len(proof.EvaluationChainIDs) > 32 {
		return false
	}
	currentSpec, err := externalNomadCurrentSpecCanonicalJSON(proof.CurrentSpec, proof.JobID, namespace, proof.JobCreateIndex, proof.JobModifyIndex, proof.JobVersion)
	if err != nil || externalSHA256(currentSpec) != proof.CurrentSpecSHA256 {
		return false
	}
	submission, err := externalNomadSubmissionCanonicalJSON(proof.Submission, proof.JobID, namespace, proof.JobVersion, proof.JobModifyIndex)
	if err != nil || externalSHA256(submission) != proof.SubmissionSHA256 {
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

const externalNomadEvidenceMaxBytes = 1 << 20

var externalNomadCurrentSpecVolatileFields = map[string]struct{}{
	"Status": {}, "StatusDescription": {}, "Stable": {}, "ModifyIndex": {},
	"CreateIndex": {}, "Version": {}, "JobModifyIndex": {}, "SubmitTime": {},
}

// externalNomadCurrentSpecCanonicalJSON is the v4 cross-runtime digest
// profile. It accepts one JSON object, rejects duplicate keys and non-integer
// numbers, verifies the live identity/index/version fields before projection,
// removes exactly Nomad's listed volatile root fields, and serializes sorted,
// compact UTF-8 JSON with HTML escaping disabled. Fleet's implementation must
// use this exact profile; the shared fixture documents representative bytes.
func externalNomadCurrentSpecCanonicalJSON(raw []byte, jobID, namespace string, createIndex, jobModifyIndex, version uint64) ([]byte, error) {
	value, err := externalNomadEvidenceObject(raw)
	if err != nil {
		return nil, err
	}
	if !externalNomadObjectIdentity(value, jobID, namespace, createIndex, jobModifyIndex, version) {
		return nil, errors.New("Nomad current job identity/index/version mismatch")
	}
	projected := make(map[string]any, len(value))
	for key, item := range value {
		if _, volatile := externalNomadCurrentSpecVolatileFields[key]; !volatile {
			projected[key] = item
		}
	}
	return externalNomadCanonicalJSON(projected)
}

// externalNomadSubmissionCanonicalJSON preserves the complete versioned Nomad
// /v1/job/:id/submission response. Nomad's actual response has JobID,
// Namespace, Version, and JobModifyIndex (or legacy JobIndex) at its root;
// Source, Variables, VariableFlags, and Format remain part of the digest.
// It deliberately does not expect a made-up nested Job object.
func externalNomadSubmissionCanonicalJSON(raw []byte, jobID, namespace string, version, modifyIndex uint64) ([]byte, error) {
	value, err := externalNomadEvidenceObject(raw)
	if err != nil {
		return nil, err
	}
	observedVersion, versionPresent := externalNomadUint(value, "Version")
	if externalNomadString(value, "JobID") != jobID || externalNomadString(value, "Namespace") != namespace || !versionPresent || observedVersion != version {
		return nil, errors.New("Nomad submission job identity/index/version mismatch")
	}
	jobIndex, jobIndexPresent := externalNomadUint(value, "JobModifyIndex")
	legacyJobIndex, legacyJobIndexPresent := externalNomadUint(value, "JobIndex")
	if jobIndexPresent == legacyJobIndexPresent {
		return nil, errors.New("Nomad submission requires exactly one job modify index")
	}
	if !jobIndexPresent {
		jobIndex = legacyJobIndex
	}
	if jobIndex != modifyIndex {
		return nil, errors.New("Nomad submission job modify index mismatch")
	}
	if _, ok := value["Source"].(string); !ok {
		return nil, errors.New("Nomad submission source missing")
	}
	if _, ok := value["Variables"].(string); !ok {
		return nil, errors.New("Nomad submission variables missing")
	}
	if _, ok := value["VariableFlags"].(map[string]any); !ok {
		return nil, errors.New("Nomad submission variable flags missing")
	}
	if _, ok := value["Format"].(string); !ok {
		return nil, errors.New("Nomad submission format missing")
	}
	return externalNomadCanonicalJSON(value)
}

func externalNomadEvidenceObject(raw []byte) (map[string]any, error) {
	if len(raw) == 0 || len(raw) > externalNomadEvidenceMaxBytes || !utf8.Valid(raw) {
		return nil, errors.New("invalid Nomad JSON evidence")
	}
	if err := rejectExternalFleetDuplicateBundleKeys(raw); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing Nomad JSON")
	}
	object, ok := value.(map[string]any)
	if !ok || !validExternalFleetCanonicalBundleValue(value) {
		return nil, errors.New("unsupported Nomad JSON representation")
	}
	return object, nil
}

func externalNomadObjectIdentity(object map[string]any, jobID, namespace string, createIndex, jobModifyIndex, version uint64) bool {
	observedCreateIndex, createIndexPresent := externalNomadUint(object, "CreateIndex")
	observedJobModifyIndex, jobModifyIndexPresent := externalNomadUint(object, "JobModifyIndex")
	observedVersion, versionPresent := externalNomadUint(object, "Version")
	// ModifyIndex is deliberately not compared here: it is a volatile inspect
	// field. The proof's JobModifyIndex is the stable Nomad registration index.
	if externalNomadString(object, "ID") != jobID || externalNomadString(object, "Namespace") != namespace || !createIndexPresent || !jobModifyIndexPresent || !versionPresent || observedJobModifyIndex != jobModifyIndex || observedVersion != version {
		return false
	}
	return createIndex == 0 || observedCreateIndex == createIndex
}

func externalNomadString(object map[string]any, field string) string {
	value, _ := object[field].(string)
	return value
}

func externalNomadUint(object map[string]any, field string) (uint64, bool) {
	number, ok := object[field].(json.Number)
	if !ok || !validExternalFleetCanonicalInteger(number.String()) {
		return 0, false
	}
	value, err := strconv.ParseUint(number.String(), 10, 64)
	if err != nil {
		return 0, false
	}
	return value, true
}

func externalNomadCanonicalJSON(value any) ([]byte, error) {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(encoded.Bytes(), []byte("\n")), nil
}

func externalSHA256(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
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
		if uuid.Validate(attempt) != nil {
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
		return (raw != "" && strings.Contains(value.String(), raw)) || (secret != "" && strings.Contains(value.String(), secret))
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

type externalFleetExecutionProofCanonical struct {
	Namespace        string                                    `json:"namespace"`
	Migration        externalFleetSnapshotNomadProofProjection `json:"migration"`
	Runtime          externalFleetSnapshotNomadProofProjection `json:"runtime"`
	PlanID           string                                    `json:"planId"`
	ApplyRunID       string                                    `json:"applyRunId"`
	ApplyRunAttempt  string                                    `json:"applyRunAttempt"`
	PlanSHA256       string                                    `json:"planSha256"`
	RunnerAttemptID  string                                    `json:"runnerAttemptId"`
	RootAttemptID    string                                    `json:"rootAttemptId"`
	FleetCommit      string                                    `json:"fleetCommit,omitempty"`
	NonceEvidenceRef string                                    `json:"nonceEvidenceRef"`
}

func externalCanonicalFleetExecutionProof(proof ExternalFleetExecutionProof) externalFleetExecutionProofCanonical {
	return externalFleetExecutionProofCanonical{Namespace: proof.Namespace, Migration: externalFleetSnapshotNomadProof(proof.Migration), Runtime: externalFleetSnapshotNomadProof(proof.Runtime), PlanID: proof.PlanID, ApplyRunID: proof.ApplyRunID, ApplyRunAttempt: proof.ApplyRunAttempt, PlanSHA256: proof.PlanSHA256, RunnerAttemptID: proof.RunnerAttemptID, RootAttemptID: proof.RootAttemptID, FleetCommit: proof.FleetCommit, NonceEvidenceRef: proof.NonceEvidenceRef}
}

// externalReceiptCanonicalJSON makes redacted evidence digests stable for
// verifier adapters and tests. It clears the one-use raw nonce and excludes
// raw Nomad JSON transport bytes: their typed canonical SHA-256 bindings are
// retained, so PostgreSQL JSONB key ordering cannot alter a durable digest.
func externalReceiptCanonicalJSON(receipt ExternalFleetDeploymentReceipt) ([]byte, error) {
	receipt = redactExternalFleetReceipt(receipt)
	chronology := make([]externalFleetReceiptChronologyProjection, len(receipt.Chronology))
	for i, step := range receipt.Chronology {
		occurredAt, err := externalFleetSnapshotTimestamp(step.OccurredAt)
		if err != nil {
			return nil, err
		}
		chronology[i] = externalFleetReceiptChronologyProjection{Phase: step.Phase, OccurredAt: occurredAt, EvidenceRef: step.EvidenceRef}
	}
	projection := struct {
		SchemaVersion           string                                     `json:"schemaVersion"`
		AdmissionID             string                                     `json:"admissionId,omitempty"`
		Nonce                   string                                     `json:"nonce"`
		App                     string                                     `json:"app"`
		SourceSHA               string                                     `json:"sourceSha"`
		Artifact                string                                     `json:"artifact"`
		Candidate               model.ReleaseCandidate                     `json:"candidate"`
		AttestationBundleSHA256 string                                     `json:"attestationBundleSha256"`
		SBOMBundleSHA256        string                                     `json:"sbomBundleSha256"`
		Fleet                   externalFleetExecutionProofCanonical       `json:"fleet"`
		Chronology              []externalFleetReceiptChronologyProjection `json:"chronology"`
	}{receipt.SchemaVersion, receipt.AdmissionID, receipt.Nonce, receipt.App, receipt.SourceSHA, receipt.Artifact, receipt.Candidate, receipt.AttestationBundleSHA256, receipt.SBOMBundleSHA256, externalCanonicalFleetExecutionProof(receipt.Fleet), chronology}
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	// This is a cross-language receipt-digest contract. Keep compact UTF-8
	// JSON and disable Go's HTML escaping to match the Fleet bridge.
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(projection); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(encoded.Bytes(), []byte("\n")), nil
}

// externalFleetReceiptChronologyProjection uses the shared millisecond UTC
// profile instead of time.Time's variable RFC3339 JSON representation. Fleet
// writes exactly three fractional digits, including .000, before it hashes.
type externalFleetReceiptChronologyProjection struct {
	Phase       string `json:"phase"`
	OccurredAt  string `json:"occurredAt"`
	EvidenceRef string `json:"evidenceRef"`
}

// externalAdmissionProofDigest deliberately has a different domain from the
// receipt digest.  It binds the signed/material deployment proof without
// treating a transport envelope (or its one-use nonce) as evidence content.
func externalAdmissionProofDigest(receipt ExternalFleetDeploymentReceipt) (string, error) {
	proof := struct {
		Schema      string                               `json:"schemaVersion"`
		Source      string                               `json:"sourceSha"`
		Artifact    string                               `json:"artifact"`
		Candidate   model.ReleaseCandidate               `json:"candidate"`
		Attestation string                               `json:"attestationBundleSha256"`
		SBOM        string                               `json:"sbomBundleSha256"`
		Fleet       externalFleetExecutionProofCanonical `json:"fleet"`
	}{externalFleetReceiptSchemaV4, receipt.SourceSHA, receipt.Artifact, receipt.Candidate, receipt.AttestationBundleSHA256, receipt.SBOMBundleSHA256, externalCanonicalFleetExecutionProof(receipt.Fleet)}
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

const externalFleetSnapshotTimestampLayout = "2006-01-02T15:04:05.000Z"

// externalFleetSnapshotTimestamp is the cross-language snapshot timestamp
// profile. Every instant is normalized to UTC and truncated (never rounded) to
// milliseconds, then written with exactly three fractional digits. Python's
// Fleet companion uses the same floor-to-millisecond RFC3339 form; in
// particular, .12Z is represented as .120Z rather than omitted or expanded.
func externalFleetSnapshotTimestamp(t time.Time) (string, error) {
	if t.IsZero() {
		// The digest profile preserves an absent optional timestamp as an empty
		// string. Callers that require a live observation separately reject it;
		// treating it as a fabricated instant would weaken that validation.
		return "", nil
	}
	return t.UTC().Truncate(time.Millisecond).Format(externalFleetSnapshotTimestampLayout), nil
}

type externalFleetSnapshotChronologyProjection struct {
	Phase       string `json:"phase"`
	OccurredAt  string `json:"occurredAt"`
	EvidenceRef string `json:"evidenceRef"`
}

// externalFleetSnapshotNomadProofProjection excludes raw transport JSON. Its
// two canonical digests retain the stable, independently validated Nomad
// binding without making whitespace or volatile inspect state hash-relevant.
type externalFleetSnapshotNomadProofProjection struct {
	JobID              string   `json:"jobId"`
	HCLSHA256          string   `json:"hclSha256"`
	EvalID             string   `json:"evalId"`
	EvalCreateIndex    uint64   `json:"evalCreateIndex,omitempty"`
	EvalJobModifyIndex uint64   `json:"evalJobModifyIndex,omitempty"`
	JobCreateIndex     uint64   `json:"jobCreateIndex,omitempty"`
	JobModifyIndex     uint64   `json:"jobModifyIndex"`
	JobVersion         uint64   `json:"jobVersion"`
	CurrentSpecSHA256  string   `json:"currentSpecSha256,omitempty"`
	SubmissionSHA256   string   `json:"submissionSha256,omitempty"`
	EvaluationChainIDs []string `json:"evaluationChainIds,omitempty"`
	CheckpointID       string   `json:"checkpointId"`
}

func externalFleetSnapshotNomadProof(proof ExternalFleetNomadJobProof) externalFleetSnapshotNomadProofProjection {
	return externalFleetSnapshotNomadProofProjection{
		JobID: proof.JobID, HCLSHA256: proof.HCLSHA256, EvalID: proof.EvalID, EvalCreateIndex: proof.EvalCreateIndex,
		EvalJobModifyIndex: proof.EvalJobModifyIndex, JobCreateIndex: proof.JobCreateIndex, JobModifyIndex: proof.JobModifyIndex,
		JobVersion: proof.JobVersion, CurrentSpecSHA256: proof.CurrentSpecSHA256, SubmissionSHA256: proof.SubmissionSHA256,
		EvaluationChainIDs: proof.EvaluationChainIDs, CheckpointID: proof.CheckpointID,
	}
}

type externalFleetSnapshotVerificationProjection struct {
	SourceSHA               string                                    `json:"sourceSha"`
	Artifact                string                                    `json:"artifact"`
	AttestationBundleSHA256 string                                    `json:"attestationBundleSha256"`
	SBOMBundleSHA256        string                                    `json:"sbomBundleSha256"`
	Namespace               string                                    `json:"namespace"`
	Migration               externalFleetSnapshotNomadProofProjection `json:"migration"`
	Runtime                 externalFleetSnapshotNomadProofProjection `json:"runtime"`
	PlanID                  string                                    `json:"planId"`
	ApplyRunID              string                                    `json:"applyRunId"`
	ApplyRunAttempt         string                                    `json:"applyRunAttempt"`
	PlanSHA256              string                                    `json:"planSha256"`
	RunnerAttemptID         string                                    `json:"runnerAttemptId"`
	FleetCommit             string                                    `json:"fleetCommit"`
	NonceEvidenceRef        string                                    `json:"nonceEvidenceRef"`
	Regions                 []ExternalFleetRegionProof                `json:"regions"`
	IngressNodeIDs          []string                                  `json:"ingressNodeIds"`
	PublicHTTPSVersion      string                                    `json:"publicHttpsVersion"`
	PrivateReadiness        struct {
		Endpoint      string   `json:"endpoint"`
		AllocationIDs []string `json:"allocationIds"`
		CheckedAt     string   `json:"checkedAt"`
	} `json:"privateReadiness"`
	Chronology []externalFleetSnapshotChronologyProjection `json:"chronology"`
}

func externalFleetSnapshotVerificationCanonical(v ExternalFleetDeploymentVerification) (externalFleetSnapshotVerificationProjection, error) {
	checkedAt, err := externalFleetSnapshotTimestamp(v.PrivateReadiness.CheckedAt)
	if err != nil {
		return externalFleetSnapshotVerificationProjection{}, err
	}
	chronology := make([]externalFleetSnapshotChronologyProjection, len(v.Chronology))
	for i, step := range v.Chronology {
		occurredAt, timestampErr := externalFleetSnapshotTimestamp(step.OccurredAt)
		if timestampErr != nil {
			return externalFleetSnapshotVerificationProjection{}, timestampErr
		}
		chronology[i] = externalFleetSnapshotChronologyProjection{Phase: step.Phase, OccurredAt: occurredAt, EvidenceRef: step.EvidenceRef}
	}
	result := externalFleetSnapshotVerificationProjection{
		SourceSHA: v.SourceSHA, Artifact: v.Artifact, AttestationBundleSHA256: v.AttestationBundleSHA256, SBOMBundleSHA256: v.SBOMBundleSHA256,
		Namespace: v.Namespace, Migration: externalFleetSnapshotNomadProof(v.Migration), Runtime: externalFleetSnapshotNomadProof(v.Runtime), PlanID: v.PlanID, ApplyRunID: v.ApplyRunID, ApplyRunAttempt: v.ApplyRunAttempt,
		PlanSHA256: v.PlanSHA256, RunnerAttemptID: v.RunnerAttemptID, FleetCommit: v.FleetCommit, NonceEvidenceRef: v.NonceEvidenceRef,
		Regions: v.Regions, IngressNodeIDs: v.IngressNodeIDs, PublicHTTPSVersion: v.PublicHTTPSVersion, Chronology: chronology,
	}
	result.PrivateReadiness.Endpoint = v.PrivateReadiness.Endpoint
	result.PrivateReadiness.AllocationIDs = v.PrivateReadiness.AllocationIDs
	result.PrivateReadiness.CheckedAt = checkedAt
	return result, nil
}

// externalFleetSnapshotDigest is the v4 snapshot hash profile shared with the
// evidence service. It hashes a compact typed projection, excluding only the
// digest field itself. Typed timestamp strings avoid Go RFC3339Nano and Python
// isoformat differences while preserving array order and every signed fact.
func externalFleetSnapshotDigest(snapshot *ExternalFleetEvidenceSnapshot) (string, error) {
	if snapshot == nil {
		return "", errors.New("missing evidence snapshot")
	}
	liveCheckedAt, err := externalFleetSnapshotTimestamp(snapshot.LiveCheckedAt)
	if err != nil {
		return "", err
	}
	nonceWrittenAt, err := externalFleetSnapshotTimestamp(snapshot.NonceWrittenAt)
	if err != nil {
		return "", err
	}
	nonceReadAt, err := externalFleetSnapshotTimestamp(snapshot.NonceReadAt)
	if err != nil {
		return "", err
	}
	verification, err := externalFleetSnapshotVerificationCanonical(snapshot.Verification)
	if err != nil {
		return "", err
	}
	projection := struct {
		ID             string                                      `json:"id"`
		Ref            string                                      `json:"ref"`
		LiveCheckedAt  string                                      `json:"liveCheckedAt"`
		NonceWrittenAt string                                      `json:"nonceWrittenAt"`
		NonceReadAt    string                                      `json:"nonceReadAt"`
		Verification   externalFleetSnapshotVerificationProjection `json:"verification"`
		RetryLineage   []string                                    `json:"retryLineage"`
		CheckpointRefs []ExternalFleetCheckpointRef                `json:"checkpointRefs"`
		Allocations    []externalFleetAllocationEvidence           `json:"allocations"`
	}{snapshot.ID, snapshot.Ref, liveCheckedAt, nonceWrittenAt, nonceReadAt, verification, snapshot.RetryLineage, snapshot.CheckpointRefs, snapshot.Allocations}
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	// Match the shared Nomad canonical profile: compact UTF-8 JSON with no Go
	// HTML escaping. The projection has no maps, so field order is explicit.
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(projection); err != nil {
		return "", err
	}
	b := bytes.TrimSuffix(encoded.Bytes(), []byte("\n"))
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
	return err == nil && s.ID != "" && s.Ref != "" && actual == s.SHA256 && !s.LiveCheckedAt.IsZero() && validExternalRetryLineage(s.RetryLineage) && len(s.CheckpointRefs) > 0
}

func externalCommitStatusMatches(status *ExternalFleetEvidenceAdmissionStatus, claim ExternalFleetEvidenceClaim, snapshot *ExternalFleetEvidenceSnapshot) bool {
	return status != nil && snapshot != nil && status.SchemaVersion == "norn.external-fleet-admission-status/v4" && status.AdmissionID == claim.AdmissionID && status.LogicalDigest == claim.LogicalDigest && status.AdmissionContextDigest == claim.AdmissionContextDigest && status.NonceSHA256 == claim.NonceSHA256 && status.Generation == claim.Generation && status.State == "committed" && status.Revision == claim.ExpectedRevision+1 && status.ReceiptDigest == claim.ReceiptDigest && status.ProofDigest == claim.ProofDigest && status.OperationID == claim.OperationID && status.OperationDigest == claim.OperationDigest && status.CleanupIntentDigest == claim.CleanupIntentDigest && status.Snapshot != nil && status.Snapshot.ID == snapshot.ID && status.Snapshot.Ref == snapshot.Ref && status.Snapshot.SHA256 == snapshot.SHA256
}
