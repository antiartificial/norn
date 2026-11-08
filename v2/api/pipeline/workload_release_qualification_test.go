package pipeline

import (
	"crypto/ed25519"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"norn/v2/api/model"
)

func TestWorkloadReleaseQualificationBindsSignedCandidateAndPlan(t *testing.T) {
	intent := workloadReleaseFixture()
	intent.SourceRevision = strings.Repeat("a", 40)
	intent.TargetJobVersion, intent.TargetJobModifyIndex = 0, 0
	plan := WorkloadReleasePlan{SchemaVersion: WorkloadReleasePlanSchema, Intent: intent}
	now := time.Date(2026, 11, 8, 18, 0, 0, 0, time.UTC)
	qualification, trustedKey := workloadReleaseQualificationFixture(t, intent, now)
	if err := plan.ValidateWorkloadReleaseQualification(qualification, []string{trustedKey}, now); err != nil {
		t.Fatalf("valid qualification rejected: %v", err)
	}

	if err := plan.ValidateWorkloadReleaseQualification(qualification, []string{qualificationPublicKey('x')}, now); err == nil {
		t.Fatal("qualification signed by an untrusted key was accepted")
	}
	for name, mutate := range map[string]func(*WorkloadReleasePlan, *model.ReleaseQualification){
		"app": func(_ *WorkloadReleasePlan, q *model.ReleaseQualification) { q.App = "other-app" },
		"source": func(p *WorkloadReleasePlan, _ *model.ReleaseQualification) {
			p.Intent.SourceRevision = strings.Repeat("b", 40)
		},
		"artifact": func(p *WorkloadReleasePlan, _ *model.ReleaseQualification) {
			p.Intent.TargetImageDigest = "sha256:" + strings.Repeat("9", 64)
			p.Intent.TargetImage = "registry.example.test/norn@" + p.Intent.TargetImageDigest
		},
		"subject digest": func(p *WorkloadReleasePlan, q *model.ReleaseQualification) {
			q.Candidate.Attestation.SubjectDigest = "sha256:" + strings.Repeat("9", 64)
		},
		"material digest": func(p *WorkloadReleasePlan, q *model.ReleaseQualification) {
			q.Candidate.Attestation.MaterialSHA = strings.Repeat("b", 40)
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidatePlan := plan
			candidateQualification := qualification
			mutate(&candidatePlan, &candidateQualification)
			if name == "app" || name == "subject digest" || name == "material digest" {
				resignWorkloadReleaseQualification(t, &candidateQualification, 'q')
			}
			if err := candidatePlan.ValidateWorkloadReleaseQualification(candidateQualification, []string{trustedKey}, now); err == nil {
				t.Fatal("mismatched qualification and plan were accepted")
			}
		})
	}

	for _, name := range []string{"expired", "issued in future"} {
		t.Run(name, func(t *testing.T) {
			candidate := qualification
			if name == "expired" {
				candidate.ExpiresAt = now
			} else {
				candidate.IssuedAt = now.Add(time.Second)
			}
			resignWorkloadReleaseQualification(t, &candidate, 'q')
			if err := plan.ValidateWorkloadReleaseQualification(candidate, []string{trustedKey}, now); err == nil {
				t.Fatal("qualification outside its validity window was accepted")
			}
		})
	}
}

func workloadReleaseQualificationFixture(t *testing.T, intent WorkloadReleaseIntent, now time.Time) (model.ReleaseQualification, string) {
	t.Helper()
	qualification := model.ReleaseQualification{
		SchemaVersion: model.ReleaseQualificationSchema,
		ID:            "workload-release-qualification",
		App:           intent.App,
		Environment:   "staging",
		DeploymentID:  "qualified-deployment",
		SourceSHA:     intent.SourceRevision,
		Artifact:      intent.TargetImage,
		IssuedAt:      now.Add(-time.Minute),
		ExpiresAt:     now.Add(time.Hour),
		Candidate: model.ReleaseCandidate{Attestation: model.ReleaseAttestationIdentity{
			MaterialSHA: intent.SourceRevision, SubjectDigest: intent.TargetImageDigest,
		}},
	}
	resignWorkloadReleaseQualification(t, &qualification, 'q')
	return qualification, qualificationPublicKey('q')
}

func resignWorkloadReleaseQualification(t *testing.T, qualification *model.ReleaseQualification, keyByte byte) {
	t.Helper()
	privateKey := ed25519.NewKeyFromSeed(bytesOf(keyByte, ed25519.SeedSize))
	payload := []byte(model.CanonicalReleaseQualificationPayload(*qualification))
	keyID := model.ReleaseQualificationKeyID(privateKey.Public().(ed25519.PublicKey))
	signature := base64.RawStdEncoding.EncodeToString(ed25519.Sign(privateKey, model.DSSEPAE(model.ReleaseQualificationPayloadType, payload)))
	qualification.KeyID, qualification.Signature = keyID, signature
	qualification.DSSE = model.DSSEEnvelope{
		PayloadType: model.ReleaseQualificationPayloadType,
		Payload:     base64.RawStdEncoding.EncodeToString(payload),
		Signatures:  []model.DSSESignature{{KeyID: keyID, Sig: signature}},
	}
}
