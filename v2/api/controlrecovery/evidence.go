package controlrecovery

import (
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"norn/v2/api/fleet"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

// RecoveryKeyMaterial is independently recovered verification material. It is
// supplied to the CLI as the age-encrypted JSON document described by
// recoveryKeyDocument and never stored in a bundle or manifest.
type RecoveryKeyMaterial struct {
	HMACKeys                [][]byte
	QualificationPublicKeys []ed25519.PublicKey
}

// recoveryKeyDocument is the plaintext recovery-key JSON:
//
//	{"hmacKeys":["<unpadded standard base64 of the exact acceptance/audit HMAC key bytes, >=32 bytes>"],
//	 "qualificationPublicKeys":["<unpadded standard base64 Ed25519 public key, or PKIX PEM>"]}
//
// Unknown fields and trailing JSON are rejected. HMAC key IDs are derived as
// hex(sha256(key)[:8]) and qualification key IDs as the producer's
// "ed25519:" + base64url(sha256(public)[:12]).
type recoveryKeyDocument struct {
	HMACKeys                []string `json:"hmacKeys"`
	QualificationPublicKeys []string `json:"qualificationPublicKeys"`
}

func ParseRecoveryKeyMaterial(encoded []byte) (*RecoveryKeyMaterial, error) {
	var document recoveryKeyDocument
	if err := decodeExactJSON(encoded, &document); err != nil {
		return nil, fmt.Errorf("recovery key material is malformed")
	}
	material := &RecoveryKeyMaterial{}
	for _, value := range document.HMACKeys {
		key, err := base64.RawStdEncoding.DecodeString(value)
		if err != nil || len(key) < 32 {
			return nil, fmt.Errorf("recovery HMAC key material is invalid")
		}
		material.HMACKeys = append(material.HMACKeys, key)
	}
	for _, value := range document.QualificationPublicKeys {
		key, err := ParseManifestPublicKey([]byte(value))
		if err != nil {
			return nil, fmt.Errorf("recovery qualification key material is invalid")
		}
		material.QualificationPublicKeys = append(material.QualificationPublicKeys, key)
	}
	return material, nil
}

func (m *RecoveryKeyMaterial) KeyIDs() []string {
	var ids []string
	for _, key := range m.HMACKeys {
		ids = append(ids, hmacKeyID(key))
	}
	for _, key := range m.QualificationPublicKeys {
		ids = append(ids, manifestKeyID(key))
	}
	return uniqueSorted(ids)
}

// AcceptanceSigner verifies archived acceptance signatures with the
// recovered HMAC keys (no signing key is needed offline; any recovered key
// may be the one named by a record).
func (m *RecoveryKeyMaterial) AcceptanceSigner() (store.AcceptanceSigner, error) {
	if m == nil || len(m.HMACKeys) == 0 {
		return nil, fmt.Errorf("recovery key material has no acceptance keys")
	}
	retained := make([]string, 0, len(m.HMACKeys)-1)
	for _, key := range m.HMACKeys[1:] {
		retained = append(retained, string(key))
	}
	return store.NewHMACAcceptanceSigner(string(m.HMACKeys[0]), retained...)
}

func (m *RecoveryKeyMaterial) VerifyRestoredEvidence(ctx context.Context, tx pgx.Tx, schema string, required []string) error {
	if m == nil || missingRequiredKeys(required, m.KeyIDs()) {
		return fmt.Errorf("required recovery keys are unavailable")
	}
	hmacKeys := make(map[string][]byte, len(m.HMACKeys))
	for _, key := range m.HMACKeys {
		hmacKeys[hmacKeyID(key)] = key
	}
	if err := verifyAcceptanceEvidence(ctx, tx, schema, hmacKeys); err != nil {
		return err
	}
	if err := verifyAuditEvidence(ctx, tx, schema, hmacKeys); err != nil {
		return err
	}
	return verifyQualificationEvidence(ctx, tx, schema, m.QualificationPublicKeys)
}

type restoredAcceptance struct {
	evidence           store.AcceptanceEvidence
	acceptanceRequired bool
}

// verifyAcceptanceEvidence loads every restored acceptance record and applies
// the store's own invariant verifier after checking the HMAC with the
// retained key named by the record. Only loading is recovery-specific.
func verifyAcceptanceEvidence(ctx context.Context, tx pgx.Tx, schema string, keys map[string][]byte) error {
	q := func(table string) string { return pgx.Identifier{schema, table}.Sanitize() }
	rows, err := tx.Query(ctx, `SELECT
		(r.id IS NOT NULL AND o.id IS NOT NULL),
		coalesce(r.authority::text,''),coalesce(r.actor_issuer,''),coalesce(r.actor_subject,''),coalesce(r.kind,''),coalesce(r.resource,''),coalesce(r.request_key,''),
		coalesce(r.fingerprint_version,''),coalesce(r.fingerprint_digest,''),coalesce(r.operation_id,''),
		i.id,i.schema_version,i.request_identity_id,i.operation_id,coalesce(i.deployment_id,''),i.accepted_at,coalesce(i.request_receipt_id,''),i.request_id,i.credential_id,i.device_id,i.source,i.scopes,
		i.fingerprint_version,i.fingerprint_digest,i.request_canonical_bytes,i.canonical_bytes,i.canonical_digest,i.signing_algorithm,i.signing_key_id,i.signature,
		coalesce(o.id,''),coalesce(o.kind,''),coalesce(o.app,''),coalesce(o.saga_id,''),coalesce(o.ref,''),coalesce(o.status,''),coalesce(o.risk,''),coalesce(o.source,''),
		coalesce(o.payload,'{}'::jsonb),coalesce(o.metadata,'{}'::jsonb),coalesce(o.max_attempts,0),coalesce(o.acceptance_required,false)
		FROM `+q("operation_acceptance_intents")+` i
		LEFT JOIN `+q("operation_request_identities")+` r ON r.id=i.request_identity_id
		LEFT JOIN `+q("operations")+` o ON o.id=i.operation_id
		ORDER BY i.id`)
	if err != nil {
		return err
	}
	var records []restoredAcceptance
	for rows.Next() {
		var record restoredAcceptance
		var linked bool
		var scopes, payload, metadata []byte
		e := &record.evidence
		if err := rows.Scan(&linked,
			&e.Identity.Authority, &e.Identity.Actor.Issuer, &e.Identity.Actor.Subject, &e.Identity.Kind, &e.Identity.Resource, &e.Identity.Key,
			&e.IdentityFingerprint.Version, &e.IdentityFingerprint.Digest, &e.IdentityOperationID,
			&e.Intent.ID, &e.Intent.Schema, &e.Intent.RequestIdentityID, &e.Intent.OperationID, &e.Intent.DeploymentID, &e.Intent.AcceptedAt, &e.Intent.Audit.RequestReceiptID, &e.Intent.Audit.RequestID, &e.Intent.Audit.CredentialID, &e.Intent.Audit.DeviceID, &e.Intent.Audit.Source, &scopes,
			&e.Intent.Fingerprint.Version, &e.Intent.Fingerprint.Digest, &e.Intent.RequestCanonicalBytes, &e.Intent.CanonicalBytes, &e.Intent.CanonicalDigest, &e.Intent.Signature.Algorithm, &e.Intent.Signature.KeyID, &e.Intent.Signature.Value,
			&e.Operation.ID, &e.Operation.Kind, &e.Operation.App, &e.Operation.SagaID, &e.Operation.Ref, &e.Operation.Status, &e.Operation.Risk, &e.Operation.Source, &payload, &metadata, &e.Operation.MaxAttempts, &record.acceptanceRequired,
		); err != nil {
			rows.Close()
			return err
		}
		if !linked {
			rows.Close()
			return fmt.Errorf("acceptance intent references a missing identity or operation")
		}
		var decodeErr error
		if json.Unmarshal(scopes, &e.Intent.Audit.Scopes) != nil {
			decodeErr = fmt.Errorf("scopes")
		}
		if e.Operation.Payload, err = store.DecodeExactJSONObject(payload); err != nil {
			decodeErr = err
		}
		if e.Operation.Metadata, err = store.DecodeExactJSONObject(metadata); err != nil {
			decodeErr = err
		}
		if decodeErr != nil {
			rows.Close()
			return fmt.Errorf("acceptance durable JSON is invalid")
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, record := range records {
		intent := record.evidence.Intent
		key := keys[intent.Signature.KeyID]
		if intent.Signature.Algorithm != "hmac-sha256" || len(key) < 32 {
			return fmt.Errorf("acceptance verification key is unavailable")
		}
		mac := hmac.New(sha256.New, key)
		_, _ = mac.Write(intent.CanonicalBytes)
		if !hmac.Equal([]byte(hex.EncodeToString(mac.Sum(nil))), []byte(intent.Signature.Value)) {
			return fmt.Errorf("acceptance signature is invalid")
		}
		if !record.acceptanceRequired {
			return fmt.Errorf("accepted operation no longer requires acceptance evidence")
		}
		evidence := record.evidence
		if err := loadAcceptedDomain(ctx, tx, schema, &evidence); err != nil {
			return err
		}
		if err := store.VerifyAcceptanceEvidence(evidence); err != nil {
			return fmt.Errorf("acceptance evidence is invalid: %w", err)
		}
	}
	return nil
}

// loadAcceptedDomain mirrors PGOperationStore.loadAcceptance against a
// schema-qualified restored copy: deployment/regions for deployment intents
// and the accepted runner attempt plus its root/predecessor lineage.
func loadAcceptedDomain(ctx context.Context, tx pgx.Tx, schema string, evidence *store.AcceptanceEvidence) error {
	q := func(table string) string { return pgx.Identifier{schema, table}.Sanitize() }
	if id := evidence.Intent.DeploymentID; id != "" {
		var d model.Deployment
		var changes []byte
		if err := tx.QueryRow(ctx, `SELECT id,app,commit_sha,image_tag,environment,saga_id,status,source_kind,source_ref,source_dirty,source_changes,started_at,finished_at FROM `+q("deployments")+` WHERE id=$1`, id).
			Scan(&d.ID, &d.App, &d.CommitSHA, &d.ImageTag, &d.Environment, &d.SagaID, &d.Status, &d.SourceKind, &d.SourceRef, &d.SourceDirty, &changes, &d.StartedAt, &d.FinishedAt); err != nil {
			return fmt.Errorf("accepted deployment is unavailable")
		}
		if json.Unmarshal(changes, &d.SourceChanges) != nil {
			return fmt.Errorf("accepted deployment source changes are invalid")
		}
		rows, err := tx.Query(ctx, `SELECT region,nomad_region,desired_weight,datacenters FROM `+q("deployment_regions")+` WHERE deployment_id=$1 ORDER BY region`, id)
		if err != nil {
			return err
		}
		regions := []model.ResolvedRegion{}
		for rows.Next() {
			var region model.ResolvedRegion
			var datacenters []byte
			if rows.Scan(&region.Name, &region.NomadRegion, &region.TrafficWeight, &datacenters) != nil || json.Unmarshal(datacenters, &region.Datacenters) != nil {
				rows.Close()
				return fmt.Errorf("accepted deployment region is unreadable")
			}
			regions = append(regions, region)
		}
		rows.Close()
		if rows.Err() != nil {
			return fmt.Errorf("accepted deployment regions are unreadable")
		}
		evidence.Deployment, evidence.Regions = &d, regions
	}

	var request struct {
		FleetRunnerAttempt *struct {
			PlanID string `json:"planId"`
		} `json:"fleetRunnerAttempt"`
	}
	var envelope struct {
		FleetRunnerAttempt *struct {
			ID string `json:"id"`
		} `json:"fleetRunnerAttempt"`
	}
	if json.Unmarshal(evidence.Intent.RequestCanonicalBytes, &request) != nil || json.Unmarshal(evidence.Intent.CanonicalBytes, &envelope) != nil {
		return fmt.Errorf("acceptance canonical bytes are malformed")
	}
	if request.FleetRunnerAttempt == nil || envelope.FleetRunnerAttempt == nil {
		// The shared verifier rejects a one-sided runner link.
		return nil
	}
	var attempt fleet.RunnerAttempt
	if err := tx.QueryRow(ctx, `SELECT id,plan_id,attempt,runner_attempt_id,commit_sha,plan_sha256,workflow_url,status,current_phase,root_attempt_id,retry_of,heartbeat_timeout_seconds FROM `+q("fleet_runner_attempts")+` WHERE plan_id=$1 AND id=$2`,
		request.FleetRunnerAttempt.PlanID, envelope.FleetRunnerAttempt.ID).
		Scan(&attempt.ID, &attempt.PlanID, &attempt.Attempt, &attempt.RunnerAttemptID, &attempt.CommitSHA, &attempt.PlanSHA256, &attempt.WorkflowURL, &attempt.Status, &attempt.CurrentPhase, &attempt.RootAttemptID, &attempt.RetryOf, &attempt.HeartbeatTimeoutSeconds); err != nil {
		return fmt.Errorf("accepted runner attempt is unavailable")
	}
	evidence.FleetRunnerAttempt = &attempt
	if attempt.Attempt == 1 {
		evidence.FleetRunnerLineageValid = attempt.RootAttemptID == attempt.ID && attempt.RetryOf == ""
		return nil
	}
	if err := tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM `+q("fleet_runner_attempts")+` root
		JOIN `+q("fleet_runner_attempts")+` predecessor ON predecessor.id=$3 AND predecessor.plan_id=$1 AND predecessor.attempt=$4
		WHERE root.id=$2 AND root.plan_id=$1 AND root.attempt=1)`, attempt.PlanID, attempt.RootAttemptID, attempt.RetryOf, attempt.Attempt-1).Scan(&evidence.FleetRunnerLineageValid); err != nil {
		return fmt.Errorf("accepted runner lineage is unreadable")
	}
	return nil
}

type auditRecord struct {
	ID, RequestID, PrincipalSubject, TokenID, DeviceID, Method, Path, ClientIP, UserAgent string
	Scopes                                                                                []string
	Status                                                                                int
	Outcome                                                                               string
	StartedAt                                                                             time.Time
	FinishedAt                                                                            *time.Time
	DurationMs                                                                            int64
	Digest, KeyID                                                                         string
}

func verifyAuditEvidence(ctx context.Context, tx pgx.Tx, schema string, keys map[string][]byte) error {
	rows, err := tx.Query(ctx, `SELECT id,request_id,principal_subject,token_id,device_id,scopes,method,path,client_ip,user_agent,status,outcome,started_at,finished_at,duration_ms,record_digest,key_id FROM `+pgx.Identifier{schema, "mutation_audit_events"}.Sanitize())
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var record auditRecord
		var scopes []byte
		if err := rows.Scan(&record.ID, &record.RequestID, &record.PrincipalSubject, &record.TokenID, &record.DeviceID, &scopes, &record.Method, &record.Path, &record.ClientIP, &record.UserAgent, &record.Status, &record.Outcome, &record.StartedAt, &record.FinishedAt, &record.DurationMs, &record.Digest, &record.KeyID); err != nil {
			return err
		}
		if err := json.Unmarshal(scopes, &record.Scopes); err != nil {
			return err
		}
		if record.Outcome == "started" && record.FinishedAt == nil {
			continue
		}
		key := keys[record.KeyID]
		if record.Digest == "" || len(key) < 32 || !hmac.Equal([]byte(signAudit(key, record)), []byte(record.Digest)) {
			return fmt.Errorf("mutation audit signature is invalid")
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	incidentRows, err := tx.Query(ctx, `SELECT id,audit_event_id,reason_code,explanation,acknowledged_by,acknowledged_at,key_id,record_digest FROM `+pgx.Identifier{schema, "mutation_audit_incidents"}.Sanitize())
	if err != nil {
		return err
	}
	defer incidentRows.Close()
	for incidentRows.Next() {
		var id, eventID, reason, explanation, acknowledgedBy, keyID, digest string
		var acknowledgedAt time.Time
		if err := incidentRows.Scan(&id, &eventID, &reason, &explanation, &acknowledgedBy, &acknowledgedAt, &keyID, &digest); err != nil {
			return err
		}
		key := keys[keyID]
		if digest == "" || len(key) < 32 || !hmac.Equal([]byte(signAuditIncident(key, id, eventID, reason, explanation, acknowledgedBy, keyID, acknowledgedAt)), []byte(digest)) {
			return fmt.Errorf("mutation audit incident signature is invalid")
		}
	}
	return incidentRows.Err()
}

// signAudit independently reimplements the handler's norn.mutation-audit/v1
// canonical form. Compatibility is pinned by a producer-generated known vector
// shared with the handler tests (testdata/mutation-audit-v1.json).
func signAudit(key []byte, record auditRecord) string {
	finished := ""
	if record.FinishedAt != nil {
		finished = canonicalAuditTime(*record.FinishedAt)
	}
	canonical := struct {
		Schema, ID, RequestID, PrincipalSubject, TokenID, DeviceID, KeyID string
		Scopes                                                            []string
		Method, Path, ClientIP, UserAgent, StartedAt, FinishedAt          string
		Status                                                            int
		Outcome                                                           string
		DurationMs                                                        int64
	}{"norn.mutation-audit/v1", record.ID, record.RequestID, record.PrincipalSubject, record.TokenID, record.DeviceID, record.KeyID, record.Scopes, record.Method, record.Path, record.ClientIP, record.UserAgent, canonicalAuditTime(record.StartedAt), finished, record.Status, record.Outcome, record.DurationMs}
	encoded, _ := json.Marshal(canonical)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(encoded)
	return hex.EncodeToString(mac.Sum(nil))
}

func signAuditIncident(key []byte, id, eventID, reason, explanation, acknowledgedBy, keyID string, acknowledgedAt time.Time) string {
	canonical := struct{ Schema, ID, AuditEventID, ReasonCode, Explanation, AcknowledgedBy, AcknowledgedAt, KeyID string }{
		"norn.mutation-audit-incident/v1", id, eventID, reason, explanation, acknowledgedBy, canonicalAuditTime(acknowledgedAt), keyID,
	}
	encoded, _ := json.Marshal(canonical)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(encoded)
	return hex.EncodeToString(mac.Sum(nil))
}

const (
	releaseQualificationSchema      = "norn.release-qualification/v2"
	releaseQualificationPayloadType = "application/vnd.norn.release-qualification.v2+json"
)

var fullSourceSHAPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// verifyQualificationEvidence applies the handler's historical-authenticity
// checks (verifyReleaseQualificationSignature) and additionally binds the
// signed receipt to its outer operation row and qualified deployment.
func verifyQualificationEvidence(ctx context.Context, tx pgx.Tx, schema string, keys []ed25519.PublicKey) error {
	q := func(table string) string { return pgx.Identifier{schema, table}.Sanitize() }
	type qualificationRow struct {
		id, app, ref, status, source string
		maxAttempts                  int
		payload, metadata            []byte
	}
	rows, err := tx.Query(ctx, `SELECT id,app,ref,status,source,max_attempts,payload,metadata FROM `+q("operations")+` WHERE kind='release.qualification' ORDER BY id`)
	if err != nil {
		return err
	}
	var records []qualificationRow
	for rows.Next() {
		var row qualificationRow
		if err := rows.Scan(&row.id, &row.app, &row.ref, &row.status, &row.source, &row.maxAttempts, &row.payload, &row.metadata); err != nil {
			rows.Close()
			return err
		}
		records = append(records, row)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	trusted := make(map[string]ed25519.PublicKey, len(keys))
	for _, key := range keys {
		if len(key) == ed25519.PublicKeySize {
			trusted[manifestKeyID(key)] = key
		}
	}
	for _, row := range records {
		var receipt model.ReleaseQualification
		if err := decodeExactJSON(row.payload, &receipt); err != nil {
			return fmt.Errorf("release qualification receipt is malformed")
		}
		if err := verifyQualificationReceipt(receipt, trusted); err != nil {
			return err
		}
		var metadata struct {
			DeploymentID string `json:"deploymentId"`
			Environment  string `json:"environment"`
		}
		if json.Unmarshal(row.metadata, &metadata) != nil {
			return fmt.Errorf("release qualification operation metadata is malformed")
		}
		if row.id != receipt.ID || row.app != receipt.App || row.ref != receipt.SourceSHA || row.status != string(model.OperationSucceeded) ||
			row.source != "release-control-api" || row.maxAttempts != 1 || metadata.DeploymentID != receipt.DeploymentID || metadata.Environment != receipt.Environment {
			return fmt.Errorf("release qualification operation differs from its signed receipt")
		}
		var app, environment, commitSHA, imageTag string
		if err := tx.QueryRow(ctx, `SELECT app,environment,commit_sha,image_tag FROM `+q("deployments")+` WHERE id=$1`, receipt.DeploymentID).Scan(&app, &environment, &commitSHA, &imageTag); err != nil {
			return fmt.Errorf("release qualification deployment is unavailable")
		}
		if app != receipt.App || environment != receipt.Environment || commitSHA != receipt.SourceSHA || imageTag != receipt.Artifact {
			return fmt.Errorf("release qualification deployment differs from its signed receipt")
		}
	}
	return nil
}

func verifyQualificationReceipt(receipt model.ReleaseQualification, trusted map[string]ed25519.PublicKey) error {
	if receipt.SchemaVersion != releaseQualificationSchema || receipt.Environment != "staging" || receipt.ID == "" || receipt.App == "" || receipt.DeploymentID == "" ||
		!fullSourceSHAPattern.MatchString(receipt.SourceSHA) || !model.IsContentAddressedImage(receipt.Artifact) || !validQualificationCandidate(receipt.Candidate, receipt.SourceSHA, receipt.Artifact) {
		return fmt.Errorf("release qualification receipt is structurally invalid")
	}
	if receipt.DSSE.PayloadType != releaseQualificationPayloadType || len(receipt.DSSE.Signatures) != 1 || receipt.DSSE.Payload == "" ||
		receipt.KeyID != receipt.DSSE.Signatures[0].KeyID || receipt.Signature != receipt.DSSE.Signatures[0].Sig {
		return fmt.Errorf("release qualification signature envelope is invalid")
	}
	payload, payloadErr := base64.RawStdEncoding.DecodeString(receipt.DSSE.Payload)
	signature, signatureErr := base64.RawStdEncoding.DecodeString(receipt.Signature)
	public := trusted[receipt.KeyID]
	if payloadErr != nil || string(payload) != releaseQualificationCanonical(receipt) || signatureErr != nil || len(public) != ed25519.PublicKeySize ||
		!ed25519.Verify(public, dssePAE(receipt.DSSE.PayloadType, payload), signature) {
		return fmt.Errorf("release qualification signature is invalid")
	}
	return nil
}

// validQualificationCandidate mirrors handler.validReleaseCandidate.
func validQualificationCandidate(candidate model.ReleaseCandidate, sourceSHA, artifact string) bool {
	if candidate.Provider != "github-actions" || candidate.Repository == "" || candidate.RepositoryID == "" || candidate.OwnerID == "" || candidate.RunID == "" || candidate.WorkflowRef == "" ||
		!fullSourceSHAPattern.MatchString(candidate.WorkflowSHA) || candidate.SignerWorkflowRef == "" || !fullSourceSHAPattern.MatchString(candidate.SignerWorkflowSHA) ||
		!strings.HasSuffix(candidate.SignerWorkflowRef, "@"+candidate.SignerWorkflowSHA) || candidate.Ref == "" || candidate.Attestation.Issuer == "" || candidate.Attestation.MaterialSHA != sourceSHA {
		return false
	}
	index := strings.LastIndex(artifact, "@sha256:")
	return index >= 0 && candidate.Attestation.SubjectDigest == artifact[index+1:]
}

func releaseQualificationCanonical(receipt model.ReleaseQualification) string {
	canonical := struct {
		SchemaVersion, ID, App, Environment, DeploymentID, SourceSHA, Artifact, IssuedAt, ExpiresAt string
		Candidate                                                                                   model.ReleaseCandidate
	}{releaseQualificationSchema, receipt.ID, receipt.App, receipt.Environment, receipt.DeploymentID, receipt.SourceSHA, receipt.Artifact, canonicalAuditTime(receipt.IssuedAt), canonicalAuditTime(receipt.ExpiresAt), receipt.Candidate}
	encoded, _ := json.Marshal(canonical)
	return string(encoded)
}

func hmacKeyID(key []byte) string {
	digest := sha256.Sum256(key)
	return hex.EncodeToString(digest[:8])
}

func canonicalAuditTime(value time.Time) string {
	return value.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano)
}
