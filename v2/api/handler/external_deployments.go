package handler

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
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
const externalFleetReceiptSchema = "norn.external-fleet-deployment-receipt/v1"
const externalFleetAdmissionNonceTTL = 10 * time.Minute

var sha256HexPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var externalNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

type externalDeploymentRequest struct {
	Action  string                          `json:"action,omitempty"`
	Receipt *ExternalFleetDeploymentReceipt `json:"receipt,omitempty"`
}

// externalFleetDeploymentReceipt contains identifiers and immutable pointers,
// never passwords, environment files, inline HCL, or self-asserted health
// booleans. The independent verifier below must obtain every claimed runtime
// fact from Nomad/Consul/ingress/GitHub before this is persisted.
type ExternalFleetDeploymentReceipt struct {
	SchemaVersion  string                        `json:"schemaVersion"`
	Nonce          string                        `json:"nonce"`
	App            string                        `json:"app"`
	SourceSHA      string                        `json:"sourceSha"`
	Artifact       string                        `json:"artifact"`
	Candidate      model.ReleaseCandidate        `json:"candidate"`
	HCLSHA256      string                        `json:"hclSha256"`
	AttestationURI string                        `json:"attestationUri"`
	SBOMURI        string                        `json:"sbomUri"`
	Fleet          ExternalFleetExecutionProof   `json:"fleet"`
	Chronology     []ExternalFleetChronologyStep `json:"chronology"`
}

type ExternalFleetExecutionProof struct {
	Namespace         string `json:"namespace"`
	JobID             string `json:"jobId"`
	NomadSubmissionID string `json:"nomadSubmissionId"`
	ApplyRunID        string `json:"applyRunId"`
	ApplyRunAttempt   string `json:"applyRunAttempt"`
	PlanSHA256        string `json:"planSha256"`
	RunnerAttemptID   string `json:"runnerAttemptId"`
	CheckpointID      string `json:"checkpointId"`
	NonceEvidenceRef  string `json:"nonceEvidenceRef"`
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
	App       string
	Namespace string
	JobID     string
	HCLSHA256 string
}

// ExternalFleetDeploymentVerification carries only independently observed
// facts. It intentionally has no `healthy`/`approved` flag: every field must
// match the canonical request and verifier adapters must fail on missing proof.
type ExternalFleetDeploymentVerification struct {
	SourceSHA          string
	Artifact           string
	AttestationURI     string
	SBOMURI            string
	HCLSHA256          string
	Namespace          string
	JobID              string
	NomadSubmissionID  string
	ApplyRunID         string
	ApplyRunAttempt    string
	PlanSHA256         string
	RunnerAttemptID    string
	CheckpointID       string
	NonceEvidenceRef   string
	IngressNodeIDs     []string
	PublicHTTPSVersion string
	PublicHTTPSReady   string
	Chronology         []ExternalFleetChronologyStep
}

func (h *Handler) ConfigureExternalFleetDeploymentVerifier(verifier ExternalFleetDeploymentVerifier) {
	h.externalFleetDeploymentVerifier = verifier
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
	if err := validateExternalFleetReceipt(receipt, configured, appID); err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "invalid_external_deployment_receipt", err.Error())
		return
	}
	spec := h.findExternalAdmissionSpec(appID)
	if spec == nil || spec.Deploy || spec.Repo == nil {
		WriteControlProblem(w, r, http.StatusConflict, "external_deployment_app_invalid", "external admission requires the configured deploy:false app with a server-owned repository")
		return
	}
	if err := validateReleaseSpecBinding(spec, receipt.Candidate, receipt.Artifact, h.pipelineRegistryURL()); err != nil || receipt.Candidate.Attestation.MaterialSHA != receipt.SourceSHA || receipt.Candidate.Attestation.ProvenanceURI != receipt.AttestationURI || receipt.Candidate.Attestation.SBOMURI != receipt.SBOMURI || !validReleaseCandidateForTrust(receipt.Candidate, receipt.SourceSHA, receipt.Artifact, h.releaseTrustMode()) {
		WriteControlProblem(w, r, http.StatusForbidden, "external_deployment_binding_mismatch", "external receipt source, artifact, and candidate do not match the server-owned app binding")
		return
	}
	key, digest, ok := appOperationIdempotency(w, r, principal, appID, "app.deploy", receipt)
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
	nonce, err := externalAdmissionNonceFromReceipt(receipt.Nonce)
	if err != nil {
		WriteControlProblem(w, r, http.StatusBadRequest, "external_deployment_nonce_invalid", err.Error())
		return
	}
	verification, err := h.externalFleetDeploymentVerifier.VerifyExternalFleetDeployment(r.Context(), ExternalFleetDeploymentVerificationRequest{Receipt: receipt, CI: *principal.CI, Config: configured})
	if err != nil || verification == nil {
		message := "independent Fleet runtime verification failed"
		if err != nil {
			message += ": " + safeExternalVerificationError(err)
		}
		WriteControlProblem(w, r, http.StatusForbidden, "external_deployment_verification_failed", message)
		return
	}
	if err := verificationMatchesExternalReceipt(*verification, receipt, configured); err != nil {
		WriteControlProblem(w, r, http.StatusForbidden, "external_deployment_verification_failed", err.Error())
		return
	}
	consumed, err := h.db.ConsumeExternalDeploymentNonce(r.Context(), externalNonceStoreRecord(nonce, appID, h.cfg.EnvironmentID(), *principal.CI))
	if err != nil {
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "external_deployment_nonce_unavailable", "durable Norn nonce consumption is unavailable")
		return
	}
	if !consumed {
		WriteControlProblem(w, r, http.StatusConflict, "external_deployment_nonce_consumed", "Norn nonce is expired, belongs to another protected Fleet run, or was already consumed")
		return
	}
	now := time.Now().UTC()
	deploymentID := uuid.NewString()
	deployment := &model.Deployment{ID: deploymentID, App: appID, CommitSHA: receipt.SourceSHA, ImageTag: receipt.Artifact, Environment: "staging", SagaID: "external-fleet:" + nonce.ID, Status: model.StatusDeployed, SourceKind: "external-fleet", SourceRef: receipt.SourceSHA, StartedAt: now, FinishedAt: &now}
	canonicalReceipt, canonicalErr := externalReceiptCanonicalJSON(receipt)
	if canonicalErr != nil {
		WriteControlProblem(w, r, http.StatusInternalServerError, "external_deployment_store_failed", "failed to canonicalize verified external deployment evidence")
		return
	}
	receiptHash := sha256.Sum256(canonicalReceipt)
	metadata := map[string]interface{}{"idempotencyKey": key, "requestDigest": digest, "principal": principal.Subject, "principalTokenId": principal.TokenID, "requestCI": principal.CI, "environment": "staging", "candidate": receipt.Candidate, "externalFleetReceipt": receipt, "externalFleetReceiptSHA256": hex.EncodeToString(receiptHash[:]), "externalFleetVerification": verification, "nonceID": nonce.ID}
	op := &model.Operation{ID: uuid.NewString(), Kind: "app.deploy", App: appID, SagaID: deployment.SagaID, Ref: receipt.SourceSHA, Status: model.OperationSucceeded, Risk: "externally executed staging workload", Source: "external-fleet-admission", Message: "independently verified external Fleet deployment admitted", Payload: map[string]interface{}{"deploymentId": deploymentID, "app": appID, "sourceSha": receipt.SourceSHA, "artifact": receipt.Artifact, "candidate": receipt.Candidate}, Metadata: metadata, StartedAt: now, FinishedAt: &now, MaxAttempts: 1}
	if err := h.db.InsertDeploymentOperation(r.Context(), deployment, spec.ResolvedRegions(), op); err != nil {
		if existing, lookupErr := h.db.GetOperationByIdempotencyKey(r.Context(), key); lookupErr == nil && existing.Kind == "app.deploy" && existing.App == appID {
			storedDigest, _ := existing.Metadata["requestDigest"].(string)
			if storedDigest == digest {
				existing.AttachReceipt()
				writeJSON(w, existing)
				return
			}
		}
		WriteControlProblem(w, r, http.StatusInternalServerError, "external_deployment_store_failed", "Norn consumed the nonce but could not persist the deployment receipt; issue a new nonce and inspect the durable operation store")
		return
	}
	op.AttachReceipt()
	w.Header().Set("Location", "/api/v1/operations/"+op.ID)
	writeJSONStatus(w, http.StatusCreated, op)
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
		WriteControlProblem(w, r, http.StatusServiceUnavailable, "external_deployment_nonce_unavailable", "failed to durably issue the Norn nonce")
		return
	}
	writeJSONStatus(w, http.StatusCreated, map[string]interface{}{"schemaVersion": "norn.external-fleet-admission-nonce/v1", "nonce": nonce.String(), "expiresAt": expiresAt.Format(time.RFC3339), "app": appID, "environment": h.cfg.EnvironmentID()})
}

func (h *Handler) externalFleetAdmissionConfig(appID string) (ExternalFleetAdmissionConfig, error) {
	if h == nil || h.cfg == nil || h.cfg.EnvironmentID() != "staging" || h.cfg.ExternalFleetAdmissionApp == "" || h.cfg.ExternalFleetAdmissionApp != appID || !externalNamePattern.MatchString(h.cfg.ExternalFleetAdmissionNamespace) || !externalNamePattern.MatchString(h.cfg.ExternalFleetAdmissionJobID) || !sha256HexPattern.MatchString(h.cfg.ExternalFleetAdmissionHCLSHA256) {
		return ExternalFleetAdmissionConfig{}, fmt.Errorf("external Fleet admission is disabled or its exact staging app/namespace/job/HCL binding is incomplete")
	}
	return ExternalFleetAdmissionConfig{App: appID, Namespace: h.cfg.ExternalFleetAdmissionNamespace, JobID: h.cfg.ExternalFleetAdmissionJobID, HCLSHA256: h.cfg.ExternalFleetAdmissionHCLSHA256}, nil
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
	if receipt.SchemaVersion != externalFleetReceiptSchema || receipt.App != app || !fullSourceSHAPattern.MatchString(receipt.SourceSHA) || !model.IsContentAddressedImage(receipt.Artifact) || !sha256HexPattern.MatchString(receipt.HCLSHA256) || receipt.HCLSHA256 != configured.HCLSHA256 || !validExternalURI(receipt.AttestationURI) || !validExternalURI(receipt.SBOMURI) {
		return fmt.Errorf("receipt schema, app, immutable source/artifact/HCL digest, or evidence references are invalid")
	}
	if receipt.Fleet.Namespace != configured.Namespace || receipt.Fleet.JobID != configured.JobID || !externalNamePattern.MatchString(receipt.Fleet.NomadSubmissionID) || !validGitHubNumericID(receipt.Fleet.ApplyRunID) || !validGitHubNumericID(receipt.Fleet.ApplyRunAttempt) || !sha256HexPattern.MatchString(receipt.Fleet.PlanSHA256) || !externalNamePattern.MatchString(receipt.Fleet.RunnerAttemptID) || !externalNamePattern.MatchString(receipt.Fleet.CheckpointID) || !externalNamePattern.MatchString(receipt.Fleet.NonceEvidenceRef) {
		return fmt.Errorf("receipt Fleet namespace/job/run/plan/attempt/checkpoint/nonce evidence binding is invalid")
	}
	if !validExternalChronology(receipt.Chronology) {
		return fmt.Errorf("receipt must carry ordered prepare, migration, runtime, and exercise evidence")
	}
	return nil
}

func verificationMatchesExternalReceipt(verified ExternalFleetDeploymentVerification, receipt ExternalFleetDeploymentReceipt, configured ExternalFleetAdmissionConfig) error {
	if verified.SourceSHA != receipt.SourceSHA || verified.Artifact != receipt.Artifact || verified.AttestationURI != receipt.AttestationURI || verified.SBOMURI != receipt.SBOMURI || verified.HCLSHA256 != receipt.HCLSHA256 || verified.Namespace != configured.Namespace || verified.JobID != configured.JobID || verified.NomadSubmissionID != receipt.Fleet.NomadSubmissionID || verified.ApplyRunID != receipt.Fleet.ApplyRunID || verified.ApplyRunAttempt != receipt.Fleet.ApplyRunAttempt || verified.PlanSHA256 != receipt.Fleet.PlanSHA256 || verified.RunnerAttemptID != receipt.Fleet.RunnerAttemptID || verified.CheckpointID != receipt.Fleet.CheckpointID || verified.NonceEvidenceRef != receipt.Fleet.NonceEvidenceRef {
		return fmt.Errorf("independent verifier observations do not exactly match the receipt")
	}
	if !validDistinctIngressNodes(verified.IngressNodeIDs) || !validHTTPSEvidence(verified.PublicHTTPSVersion, verified.PublicHTTPSReady) || !sameExternalChronology(verified.Chronology, receipt.Chronology) {
		return fmt.Errorf("independent verifier did not prove two distinct ingress nodes, public HTTPS version/ready, and full chronology")
	}
	return nil
}

func validExternalChronology(steps []ExternalFleetChronologyStep) bool {
	if len(steps) != 4 {
		return false
	}
	for index, phase := range []string{"prepare", "migration", "runtime", "exercise"} {
		step := steps[index]
		if step.Phase != phase || step.OccurredAt.IsZero() || !validExternalURI(step.EvidenceRef) || (index > 0 && !step.OccurredAt.After(steps[index-1].OccurredAt)) {
			return false
		}
	}
	return true
}

func sameExternalChronology(left, right []ExternalFleetChronologyStep) bool {
	if !validExternalChronology(left) || !validExternalChronology(right) || len(left) != len(right) {
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

func validHTTPSEvidence(version, ready string) bool {
	return validExternalURI(version) && validExternalURI(ready) && strings.HasPrefix(version, "https://") && strings.HasPrefix(ready, "https://")
}

func validExternalURI(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) == 0 || len(value) > 2048 || strings.ContainsAny(value, "\r\n") {
		return false
	}
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme == "https" && parsed.Hostname() != "" && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == ""
}

func safeExternalVerificationError(err error) string {
	value := strings.TrimSpace(err.Error())
	if len(value) > 240 {
		return value[:240]
	}
	return value
}

// externalReceiptCanonicalJSON makes evidence digests stable for verifier
// adapters and tests; callers are never asked to supply a trusted digest.
func externalReceiptCanonicalJSON(receipt ExternalFleetDeploymentReceipt) ([]byte, error) {
	return json.Marshal(receipt)
}
