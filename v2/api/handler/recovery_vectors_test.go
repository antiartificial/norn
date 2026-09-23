package handler

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"norn/v2/api/model"
	"norn/v2/api/store"
)

// These vectors are consumed by controlrecovery, which independently
// reimplements verification. Regenerate only for an intentional producer
// format change: go test ./handler -run RecoveryKnownVector -args -update-recovery-vectors
var updateRecoveryVectors = flag.Bool("update-recovery-vectors", false, "rewrite controlrecovery producer known vectors")

const recoveryVectorDirectory = "../controlrecovery/testdata"

type qualificationRecoveryVector struct {
	PublicKey string          `json:"publicKey"`
	Receipt   json.RawMessage `json:"receipt"`
}

type auditRecoveryVectorEvent struct {
	ID               string    `json:"id"`
	RequestID        string    `json:"requestId"`
	PrincipalSubject string    `json:"principalSubject"`
	TokenID          string    `json:"tokenId"`
	DeviceID         string    `json:"deviceId"`
	KeyID            string    `json:"keyId"`
	Scopes           []string  `json:"scopes"`
	Method           string    `json:"method"`
	Path             string    `json:"path"`
	ClientIP         string    `json:"clientIp"`
	UserAgent        string    `json:"userAgent"`
	Status           int       `json:"status"`
	Outcome          string    `json:"outcome"`
	StartedAt        time.Time `json:"startedAt"`
	FinishedAt       time.Time `json:"finishedAt"`
	DurationMs       int64     `json:"durationMs"`
}

type auditRecoveryVectorIncident struct {
	ID             string    `json:"id"`
	AuditEventID   string    `json:"auditEventId"`
	ReasonCode     string    `json:"reasonCode"`
	Explanation    string    `json:"explanation"`
	AcknowledgedBy string    `json:"acknowledgedBy"`
	AcknowledgedAt time.Time `json:"acknowledgedAt"`
	KeyID          string    `json:"keyId"`
}

type auditRecoveryVector struct {
	Key            string                      `json:"key"`
	Event          auditRecoveryVectorEvent    `json:"event"`
	Digest         string                      `json:"digest"`
	Incident       auditRecoveryVectorIncident `json:"incident"`
	IncidentDigest string                      `json:"incidentDigest"`
}

func TestReleaseQualificationProducerMatchesRecoveryKnownVector(t *testing.T) {
	seed := bytes.Repeat([]byte{'q'}, ed25519.SeedSize)
	issued := time.Date(2026, 9, 22, 12, 0, 0, 123456789, time.UTC)
	sourceSHA := "0123456789abcdef0123456789abcdef01234567"
	workflowSHA := strings.Repeat("c", 40)
	signerSHA := strings.Repeat("e", 40)
	artifact := "registry.example.test/demo@sha256:" + strings.Repeat("b", 64)
	receipt := model.ReleaseQualification{
		SchemaVersion: releaseQualificationSchema, ID: "6f1c2c1e-4d0b-4c55-9a7e-3b7b1f0f5a01", App: "demo", Environment: "staging",
		DeploymentID: "0b8f5d1e-2a3c-4e5f-8a9b-1c2d3e4f5a6b", SourceSHA: sourceSHA, Artifact: artifact,
		IssuedAt: issued, ExpiresAt: issued.Add(7 * 24 * time.Hour),
		Candidate: model.ReleaseCandidate{
			Provider: "github-actions", Repository: "owner/demo", RepositoryID: "101", OwnerID: "202", RunID: "303", RunAttempt: "1",
			WorkflowRef: "owner/demo/.github/workflows/release.yml@refs/heads/main", WorkflowSHA: workflowSHA,
			SignerWorkflowRef: "owner/signing/.github/workflows/sign.yml@" + signerSHA, SignerWorkflowSHA: signerSHA, Ref: "refs/heads/main",
			Attestation: model.ReleaseAttestationIdentity{Issuer: "https://token.actions.githubusercontent.com", SubjectDigest: "sha256:" + strings.Repeat("b", 64), MaterialSHA: sourceSHA},
		},
	}
	if err := signReleaseQualification(base64.RawStdEncoding.EncodeToString(seed), &receipt); err != nil {
		t.Fatal(err)
	}
	public := base64.RawStdEncoding.EncodeToString(ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey))
	if err := verifyReleaseQualificationSignature([]string{public}, receipt); err != nil {
		t.Fatalf("producer vector does not satisfy handler verification: %v", err)
	}
	// The stored operation payload is the producer's map projection.
	encoded, err := json.Marshal(qualificationToMap(receipt))
	if err != nil {
		t.Fatal(err)
	}
	compareRecoveryVector(t, "release-qualification-v2.json", qualificationRecoveryVector{PublicKey: public, Receipt: encoded})
}

func TestMutationAuditProducerMatchesRecoveryKnownVector(t *testing.T) {
	key := "recovery-known-vector-audit-key-0123456789"
	finished := time.Date(2026, 9, 22, 12, 34, 56, 654321000, time.UTC)
	event := store.MutationAuditEvent{
		ID: "audit-vector-1", RequestID: "request-vector-1", PrincipalSubject: "operator@example.test", TokenID: "token-1", DeviceID: "device-1",
		KeyID: auditKeyID(key), Scopes: []string{"api:write", "platform:operate"}, Method: "POST", Path: "/api/v1/apps/{id}/deploy",
		ClientIP: "100.64.0.10", UserAgent: "NornUI/1", Status: 202, Outcome: "succeeded",
		// Nanoseconds prove the producer truncates to PostgreSQL precision.
		StartedAt: time.Date(2026, 9, 22, 12, 34, 55, 123456789, time.UTC), FinishedAt: &finished, DurationMs: 1530,
	}
	incident := store.MutationAuditIncident{
		ID: "incident-vector-1", AuditEventID: event.ID, ReasonCode: "legacy_timestamp_precision",
		Explanation: "PostgreSQL truncated nanoseconds before the canonicalization fix.", AcknowledgedBy: "operator@example.test",
		AcknowledgedAt: time.Date(2026, 9, 22, 13, 0, 0, 500000000, time.UTC), KeyID: auditKeyID(key),
	}
	vector := auditRecoveryVector{
		Key: key, Digest: signMutationAudit(key, event), IncidentDigest: signMutationAuditIncident(key, incident),
		Event: auditRecoveryVectorEvent{
			ID: event.ID, RequestID: event.RequestID, PrincipalSubject: event.PrincipalSubject, TokenID: event.TokenID, DeviceID: event.DeviceID,
			KeyID: event.KeyID, Scopes: event.Scopes, Method: event.Method, Path: event.Path, ClientIP: event.ClientIP, UserAgent: event.UserAgent,
			Status: event.Status, Outcome: event.Outcome, StartedAt: event.StartedAt, FinishedAt: finished, DurationMs: event.DurationMs,
		},
		Incident: auditRecoveryVectorIncident{
			ID: incident.ID, AuditEventID: incident.AuditEventID, ReasonCode: incident.ReasonCode, Explanation: incident.Explanation,
			AcknowledgedBy: incident.AcknowledgedBy, AcknowledgedAt: incident.AcknowledgedAt, KeyID: incident.KeyID,
		},
	}
	event.RecordDigest = vector.Digest
	if vector.Digest == "" || mutationAuditIntegrity(key, event) != "verified" {
		t.Fatal("producer audit vector does not verify")
	}
	compareRecoveryVector(t, "mutation-audit-v1.json", vector)
}

func compareRecoveryVector(t *testing.T, name string, vector any) {
	t.Helper()
	want, err := json.MarshalIndent(vector, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, '\n')
	path := filepath.Join(recoveryVectorDirectory, name)
	if *updateRecoveryVectors {
		if err := os.WriteFile(path, want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read recovery vector: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("producer output no longer matches recovery known vector %s; recovery verification must be updated deliberately\n got: %s\nwant: %s", name, got, want)
	}
}
