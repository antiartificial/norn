package etcdstore

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"
	"norn/v2/api/fleet/offline"
)

const terminalAttestationProvenanceSchema = "norn.fleet-offline-terminal-provenance/v1"

// The public key is retained with the immutable receipt so a later trust-key
// rotation does not erase the ability to verify historical evidence.
type terminalAttestationProvenance struct {
	SchemaVersion     string          `json:"schemaVersion"`
	ChallengeID       string          `json:"challengeId"`
	Envelope          json.RawMessage `json:"envelope"`
	ObserverPublicKey string          `json:"observerPublicKey"`
}

func (s *V3OperationStore) offlineTerminalChallengeConsumedKey(planID string) string {
	return s.prefix + "/v3/fleet-offline-terminal-challenge-consumed/" + planID + "/00000000000000000001"
}

func (s *V3OperationStore) offlineTerminalAttestationProvenanceKey(planID string) string {
	return s.prefix + "/v3/fleet-offline-terminal-attestations/" + planID + "/00000000000000000001"
}

// SubmitOfflineTerminalAttestation accepts only the canonical signed envelope.
// The target Fleet authority owns every decision and writes all four result
// rows atomically. An exact retry recovers the receipt; a different envelope
// for the same one-use challenge cannot replace it. No fence is released.
func (s *V3OperationStore) SubmitOfflineTerminalAttestation(ctx context.Context, planID, attemptID, challengeID string,
	rawEnvelope []byte) (offline.TerminalEvidence, error) {
	return s.submitOfflineTerminalAttestationWithClock(ctx, planID, attemptID, challengeID, rawEnvelope, func() time.Time { return time.Now().UTC() })
}

func (s *V3OperationStore) submitOfflineTerminalAttestationWithClock(ctx context.Context, planID, attemptID, challengeID string,
	rawEnvelope []byte, clock func() time.Time) (offline.TerminalEvidence, error) {
	if s == nil || clock == nil || uuid.Validate(planID) != nil || uuid.Validate(attemptID) != nil || uuid.Validate(challengeID) != nil {
		return offline.TerminalEvidence{}, offline.ErrTerminalAttestation
	}
	envelope, err := offline.DecodeSignedTerminalAttestationV1(rawEnvelope)
	if err != nil || envelope.Attestation.PlanID != planID || envelope.Attestation.AttemptID != attemptID ||
		envelope.Attestation.ChallengeID != challengeID {
		return offline.TerminalEvidence{}, offline.ErrTerminalAttestation
	}
	// Recovery is deliberately available after expiry or a lost response, but
	// only when the exact canonical bytes were committed previously.
	if prior, err := s.recoverSubmittedTerminal(ctx, planID, attemptID, challengeID, rawEnvelope); err == nil {
		return prior, nil
	} else if err != ErrNotFound {
		return offline.TerminalEvidence{}, err
	}
	snapshot, err := s.readTerminalChallengeSnapshot(ctx, planID)
	if err != nil {
		return offline.TerminalEvidence{}, err
	}
	if !snapshot.hasChallenge || snapshot.challenge.ID != challengeID || snapshot.challenge.AttemptID != attemptID ||
		snapshot.challenge.GrantID != envelope.Attestation.GrantID {
		return offline.TerminalEvidence{}, offline.ErrTerminalAttestation
	}
	public, err := base64.RawURLEncoding.Strict().DecodeString(snapshot.trust.PublicKey)
	if err != nil {
		return offline.TerminalEvidence{}, ErrOfflineAttemptCorrupt
	}
	decisionTime := clock().UTC()
	verified, err := offline.VerifyTerminalAttestationV1(envelope, snapshot.challenge, snapshot.grant,
		snapshot.trust.ObserverSubject, snapshot.trust.KeyID, ed25519.PublicKey(public), decisionTime)
	if err != nil {
		return offline.TerminalEvidence{}, err
	}
	observation := envelope.Attestation.Observation
	receipt := offline.TerminalEvidence{SchemaVersion: offline.TerminalSchema, PlanID: planID,
		AttemptID: attemptID, GrantID: snapshot.grant.ID, BindingSHA256: snapshot.grant.BindingSHA256,
		TargetID: snapshot.grant.TargetID, FenceGeneration: snapshot.grant.FenceGeneration,
		AuthorityEpoch: snapshot.grant.AuthorityEpoch, ObservedAt: observation.ObservedAt.UTC(),
		RecordedAt: decisionTime, EvidenceSHA256: observation.EvidenceSHA256,
		ProviderEvidenceSHA256: observation.ProviderEvidenceSHA256, BackendEvidenceSHA256: observation.BackendEvidenceSHA256,
		WorkloadEvidenceSHA256: observation.WorkloadEvidenceSHA256, Backend: observation.Backend,
		ChallengeID: challengeID, AttestationSHA256: verified.AttestationSHA256(),
		ObserverSubject: envelope.Attestation.ObserverSubject, ObserverKeyID: envelope.KeyID,
		RawJournalSHA256: envelope.Attestation.RawJournalSHA256}
	receiptRaw, err := json.Marshal(receipt)
	if err != nil {
		return offline.TerminalEvidence{}, err
	}
	provenance := terminalAttestationProvenance{SchemaVersion: terminalAttestationProvenanceSchema,
		ChallengeID: challengeID, Envelope: append(json.RawMessage(nil), rawEnvelope...),
		ObserverPublicKey: snapshot.trust.PublicKey}
	provenanceRaw, err := json.Marshal(provenance)
	if err != nil {
		return offline.TerminalEvidence{}, err
	}
	// One authority clock controls freshness; sample again immediately before
	// CAS and revalidate the entire signed observation, not only challenge TTL.
	commitTime := clock().UTC()
	if _, err := offline.VerifyTerminalAttestationV1(envelope, snapshot.challenge, snapshot.grant,
		snapshot.trust.ObserverSubject, snapshot.trust.KeyID, ed25519.PublicKey(public), commitTime); err != nil {
		return offline.TerminalEvidence{}, err
	}
	receipt.RecordedAt = commitTime
	receiptRaw, err = json.Marshal(receipt)
	if err != nil {
		return offline.TerminalEvidence{}, err
	}
	compares := make([]clientv3.Cmp, 0, len(snapshot.keys)+2)
	for i, key := range snapshot.keys {
		if i == challengeRunnerAttempts {
			compares = append(compares, clientv3.Compare(clientv3.CreateRevision(key).WithPrefix(), "=", 0))
		} else {
			compares = append(compares, clientv3.Compare(clientv3.ModRevision(key), "=", snapshot.revisions[i]))
		}
	}
	compares = append(compares,
		clientv3.Compare(clientv3.CreateRevision(s.offlineTerminalChallengeConsumedKey(planID)), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(s.offlineTerminalAttestationProvenanceKey(planID)), "=", 0))
	result, err := s.kv.Txn(ctx).If(compares...).Then(
		clientv3.OpPut(s.offlineTerminalChallengeConsumedKey(planID), verified.AttestationSHA256()),
		clientv3.OpPut(s.offlineTerminalAttestationProvenanceKey(planID), string(provenanceRaw)),
		clientv3.OpPut(s.offlineTerminalKey(planID), string(receiptRaw)),
		clientv3.OpPut(s.offlineTerminalEventKey(planID), string(receiptRaw)),
	).Commit()
	if err != nil {
		return offline.TerminalEvidence{}, err
	}
	if !result.Succeeded {
		if prior, recoveryErr := s.recoverSubmittedTerminal(ctx, planID, attemptID, challengeID, rawEnvelope); recoveryErr == nil {
			return prior, nil
		} else if recoveryErr != ErrNotFound {
			return offline.TerminalEvidence{}, recoveryErr
		}
		return offline.TerminalEvidence{}, ErrOfflineTerminalConflict
	}
	return receipt, nil
}

func (s *V3OperationStore) recoverSubmittedTerminal(ctx context.Context, planID, attemptID, challengeID string,
	rawEnvelope []byte) (offline.TerminalEvidence, error) {
	response, err := s.kv.Txn(ctx).Then(clientv3.OpGet(s.offlineTerminalKey(planID)),
		clientv3.OpGet(s.offlineTerminalEventKey(planID)),
		clientv3.OpGet(s.offlineTerminalChallengeConsumedKey(planID)),
		clientv3.OpGet(s.offlineTerminalAttestationProvenanceKey(planID))).Commit()
	if err != nil {
		return offline.TerminalEvidence{}, err
	}
	if len(response.Responses) != 4 {
		return offline.TerminalEvidence{}, ErrOfflineAttemptCorrupt
	}
	var raw [4][]byte
	for i, row := range response.Responses {
		kvs := row.GetResponseRange().Kvs
		if len(kvs) > 1 {
			return offline.TerminalEvidence{}, ErrOfflineAttemptCorrupt
		}
		if len(kvs) == 1 {
			raw[i] = kvs[0].Value
		}
	}
	if len(raw[0]) == 0 && len(raw[1]) == 0 && len(raw[2]) == 0 && len(raw[3]) == 0 {
		return offline.TerminalEvidence{}, ErrNotFound
	}
	receipt, err := decodeOfflineTerminalPair(raw[0], raw[1], planID)
	if err != nil || len(raw[2]) == 0 || len(raw[3]) == 0 {
		return offline.TerminalEvidence{}, ErrOfflineAttemptCorrupt
	}
	var provenance terminalAttestationProvenance
	if decodeV3Record(raw[3], &provenance) != nil || provenance.SchemaVersion != terminalAttestationProvenanceSchema ||
		provenance.ChallengeID != challengeID || receipt.ChallengeID != challengeID || receipt.AttemptID != attemptID ||
		!bytes.Equal(provenance.Envelope, rawEnvelope) || raw[2] == nil {
		return offline.TerminalEvidence{}, ErrOfflineTerminalConflict
	}
	digest := sha256.Sum256(rawEnvelope)
	if receipt.AttestationSHA256 != hex.EncodeToString(digest[:]) || string(raw[2]) != receipt.AttestationSHA256 {
		return offline.TerminalEvidence{}, ErrOfflineAttemptCorrupt
	}
	// Readback also checks the live binding, consumed grant, fence and epoch.
	return s.GetOfflineTerminal(ctx, planID)
}

func validateSignedTerminalProvenance(receipt offline.TerminalEvidence, grant offline.EffectGrant,
	challengeHead, challengeEvent, consumed, provenanceRaw []byte) error {
	if receipt.ChallengeID == "" {
		if receipt.AttestationSHA256 != "" || receipt.ObserverSubject != "" || receipt.ObserverKeyID != "" ||
			receipt.RawJournalSHA256 != "" || len(consumed) != 0 || len(provenanceRaw) != 0 {
			return ErrOfflineAttemptCorrupt
		}
		return nil
	}
	if len(challengeHead) == 0 || !bytes.Equal(challengeHead, challengeEvent) || len(consumed) == 0 ||
		len(provenanceRaw) == 0 || !offlineEvidenceDigest.MatchString(receipt.AttestationSHA256) ||
		!offlineEvidenceDigest.MatchString(receipt.RawJournalSHA256) {
		return ErrOfflineAttemptCorrupt
	}
	challenge, err := offline.DecodeTerminalChallengeV1(challengeHead)
	if err != nil || challenge.ID != receipt.ChallengeID || challenge.PlanID != receipt.PlanID ||
		challenge.AttemptID != receipt.AttemptID || challenge.GrantID != receipt.GrantID {
		return ErrOfflineAttemptCorrupt
	}
	var provenance terminalAttestationProvenance
	if decodeV3Record(provenanceRaw, &provenance) != nil || provenance.SchemaVersion != terminalAttestationProvenanceSchema ||
		provenance.ChallengeID != challenge.ID {
		return ErrOfflineAttemptCorrupt
	}
	canonical, err := json.Marshal(provenance)
	if err != nil || !bytes.Equal(canonical, provenanceRaw) {
		return ErrOfflineAttemptCorrupt
	}
	envelope, err := offline.DecodeSignedTerminalAttestationV1(provenance.Envelope)
	public, keyErr := base64.RawURLEncoding.Strict().DecodeString(provenance.ObserverPublicKey)
	if err != nil || keyErr != nil || len(public) != ed25519.PublicKeySize ||
		envelope.Attestation.ObserverSubject != receipt.ObserverSubject || envelope.KeyID != receipt.ObserverKeyID ||
		envelope.Attestation.RawJournalSHA256 != receipt.RawJournalSHA256 {
		return ErrOfflineAttemptCorrupt
	}
	verified, err := offline.VerifyTerminalAttestationV1(envelope, challenge, grant,
		receipt.ObserverSubject, receipt.ObserverKeyID, ed25519.PublicKey(public), receipt.RecordedAt)
	if err != nil || verified.AttestationSHA256() != receipt.AttestationSHA256 ||
		string(consumed) != receipt.AttestationSHA256 {
		return ErrOfflineAttemptCorrupt
	}
	observation := envelope.Attestation.Observation
	if !receipt.ObservedAt.Equal(observation.ObservedAt) || receipt.EvidenceSHA256 != observation.EvidenceSHA256 ||
		receipt.ProviderEvidenceSHA256 != observation.ProviderEvidenceSHA256 ||
		receipt.BackendEvidenceSHA256 != observation.BackendEvidenceSHA256 ||
		receipt.WorkloadEvidenceSHA256 != observation.WorkloadEvidenceSHA256 || receipt.Backend != observation.Backend {
		return ErrOfflineAttemptCorrupt
	}
	return nil
}
