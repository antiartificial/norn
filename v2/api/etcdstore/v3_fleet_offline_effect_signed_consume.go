package etcdstore

// This CAS only records that the one-use grant was consumed. It neither
// executes OpenTofu nor serializes external state writers; the separate
// backend writer lock remains mandatory at the executor boundary.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"
	"norn/v2/api/fleet/offline"
)

const signedEffectConsumeProofSchema = "norn.fleet-offline-signed-effect-consume-proof/v1"

type signedEffectConsumeProof struct {
	SchemaVersion     string    `json:"schemaVersion"`
	PlanID            string    `json:"planId"`
	AttemptID         string    `json:"attemptId"`
	ChallengeID       string    `json:"challengeId"`
	AttestationSHA256 string    `json:"attestationSHA256"`
	GrantID           string    `json:"grantId"`
	ConsumedAt        time.Time `json:"consumedAt"`
}

func (s *V3OperationStore) offlineSignedEffectConsumeProofKey(planID string) string {
	return s.prefix + "/v3/fleet-offline-signed-effect-consume-proofs/" + planID + "/00000000000000000001"
}
func (s *V3OperationStore) offlineSignedEffectConsumeWatermarkKey(planID string) string {
	return s.prefix + "/v3/fleet-offline-signed-effect-consume-watermarks/" + planID
}

func (s *V3OperationStore) GetOfflineSignedConsumedGrant(ctx context.Context, planID, attemptID, challengeID string) (offline.EffectGrant, error) {
	if s == nil || uuid.Validate(planID) != nil || uuid.Validate(attemptID) != nil || uuid.Validate(challengeID) != nil {
		return offline.EffectGrant{}, offline.ErrEffectAttestation
	}
	response, err := s.kv.Txn(ctx).Then(
		clientv3.OpGet(s.offlineSignedEffectConsumeProofKey(planID)),
		clientv3.OpGet(s.offlineSignedEffectConsumeWatermarkKey(planID)),
		clientv3.OpGet(s.offlineEffectGrantKey(planID)),
		clientv3.OpGet(s.offlineEffectGrantEventKey(planID, 2)),
	).Commit()
	if err != nil {
		return offline.EffectGrant{}, err
	}
	if len(response.Responses) != 4 {
		return offline.EffectGrant{}, ErrOfflineAttemptCorrupt
	}
	var raw [4][]byte
	for i, item := range response.Responses {
		rows := item.GetResponseRange().Kvs
		if len(rows) > 1 {
			return offline.EffectGrant{}, ErrOfflineAttemptCorrupt
		}
		if len(rows) == 1 {
			raw[i] = rows[0].Value
		}
	}
	if len(raw[0]) == 0 && len(raw[1]) == 0 && len(raw[3]) == 0 {
		if len(raw[2]) == 0 {
			return offline.EffectGrant{}, ErrNotFound
		}
		var current offline.EffectGrant
		if decodeV3Record(raw[2], &current) == nil && current.Phase == "issued" && current.ConsumedAt == nil {
			return offline.EffectGrant{}, ErrNotFound
		}
		return offline.EffectGrant{}, ErrOfflineAttemptCorrupt
	}
	if len(raw[0]) == 0 || !bytes.Equal(raw[0], raw[1]) || len(raw[2]) == 0 || !bytes.Equal(raw[2], raw[3]) {
		return offline.EffectGrant{}, ErrOfflineAttemptCorrupt
	}
	var proof signedEffectConsumeProof
	if decodeV3Record(raw[0], &proof) != nil || proof.SchemaVersion != signedEffectConsumeProofSchema || proof.PlanID != planID ||
		proof.AttemptID != attemptID || proof.ChallengeID != challengeID || uuid.Validate(proof.GrantID) != nil ||
		!offlineEvidenceDigest.MatchString(proof.AttestationSHA256) || proof.ConsumedAt.IsZero() {
		return offline.EffectGrant{}, ErrOfflineAttemptCorrupt
	}
	canonical, err := json.Marshal(proof)
	if err != nil || !bytes.Equal(canonical, raw[0]) {
		return offline.EffectGrant{}, ErrOfflineAttemptCorrupt
	}
	issuedProofRows, err := s.kv.Get(ctx, s.offlineSignedEffectGrantProofKey(planID))
	if err != nil || len(issuedProofRows.Kvs) != 1 {
		return offline.EffectGrant{}, ErrOfflineAttemptCorrupt
	}
	var issued signedEffectGrantProof
	if decodeV3Record(issuedProofRows.Kvs[0].Value, &issued) != nil || issued.GrantID != proof.GrantID || issued.AttemptID != attemptID {
		return offline.EffectGrant{}, ErrOfflineAttemptCorrupt
	}
	grant, err := s.GetOfflineSignedEffectGrant(ctx, planID, attemptID, issued.ChallengeID)
	if err != nil || grant.Phase != "consumed" || grant.ConsumedAt == nil || grant.ID != proof.GrantID ||
		!grant.ConsumedAt.Equal(proof.ConsumedAt) {
		return offline.EffectGrant{}, ErrOfflineAttemptCorrupt
	}
	accepted, err := s.ReadAcceptedOfflineEffectAttestation(ctx, planID, offline.EffectConsumePurpose, challengeID)
	if err != nil || accepted.AttemptID != attemptID || accepted.AttestationSHA256 != proof.AttestationSHA256 ||
		accepted.Grant == nil || accepted.Grant.ID != grant.ID {
		return offline.EffectGrant{}, ErrOfflineAttemptCorrupt
	}
	return grant, nil
}

func (s *V3OperationStore) ConsumeOfflineEffectGrantFromAccepted(ctx context.Context, planID, attemptID, grantID, challengeID string, identity OfflineRunnerIdentity) (offline.EffectGrant, error) {
	return s.consumeOfflineEffectGrantFromAcceptedWithClock(ctx, planID, attemptID, grantID, challengeID, identity, time.Now)
}
func (s *V3OperationStore) consumeOfflineEffectGrantFromAcceptedWithClock(ctx context.Context, planID, attemptID, grantID, challengeID string, identity OfflineRunnerIdentity, clock func() time.Time) (offline.EffectGrant, error) {
	if s == nil || clock == nil || uuid.Validate(planID) != nil || uuid.Validate(attemptID) != nil || uuid.Validate(grantID) != nil ||
		uuid.Validate(challengeID) != nil || !identity.valid() {
		return offline.EffectGrant{}, offline.ErrEffectProofMismatch
	}
	if prior, err := s.GetOfflineSignedConsumedGrant(ctx, planID, attemptID, challengeID); err == nil {
		accepted, readErr := s.ReadAcceptedOfflineEffectAttestation(ctx, planID, offline.EffectConsumePurpose, challengeID)
		if readErr != nil || prior.ID != grantID || !effectAttemptMatchesIdentity(accepted.Attempt, identity) {
			return offline.EffectGrant{}, offline.ErrEffectProofMismatch
		}
		return prior, nil
	} else if !errors.Is(err, ErrNotFound) {
		return offline.EffectGrant{}, err
	}
	snapshot, err := s.readEffectChallengeSnapshot(ctx, planID, offline.EffectConsumePurpose)
	if err != nil {
		return offline.EffectGrant{}, err
	}
	if !snapshot.hasChallenge || snapshot.accepted == nil || snapshot.challenge.ID != challengeID || snapshot.attempt.ID != attemptID ||
		snapshot.grant.ID != grantID || !effectAttemptMatchesIdentity(snapshot.attempt, identity) {
		return offline.EffectGrant{}, offline.ErrEffectProofMismatch
	}
	// A legacy unsigned grant cannot be consumed through this signed lane.
	proofRows, err := s.kv.Get(ctx, s.offlineSignedEffectGrantProofKey(planID))
	if err != nil {
		return offline.EffectGrant{}, err
	}
	if len(proofRows.Kvs) != 1 {
		return offline.EffectGrant{}, ErrOfflineEffectGrantConflict
	}
	var issueProof signedEffectGrantProof
	if decodeV3Record(proofRows.Kvs[0].Value, &issueProof) != nil || issueProof.SchemaVersion != signedEffectGrantProofSchema ||
		issueProof.PlanID != planID || issueProof.AttemptID != attemptID || issueProof.GrantID != grantID || uuid.Validate(issueProof.ChallengeID) != nil {
		return offline.EffectGrant{}, ErrOfflineAttemptCorrupt
	}
	issued, err := s.GetOfflineSignedEffectGrant(ctx, planID, attemptID, issueProof.ChallengeID)
	if err != nil || issued.ID != snapshot.grant.ID || issued.Phase != "issued" {
		return offline.EffectGrant{}, ErrOfflineEffectGrantConflict
	}
	accepted := snapshot.accepted
	signed, err := offline.DecodeSignedEffectAttestationV1(accepted.Envelope)
	if err != nil {
		return offline.EffectGrant{}, ErrOfflineAttemptCorrupt
	}
	public, err := base64.RawURLEncoding.Strict().DecodeString(snapshot.trust.PublicKey)
	if err != nil || len(public) != ed25519.PublicKeySize {
		return offline.EffectGrant{}, ErrOfflineAttemptCorrupt
	}
	var checkpoint offline.Checkpoint
	if decodeV3Record(snapshot.raw[effectChallengeCheckpoint], &checkpoint) != nil || checkpoint.Prechange == nil {
		return offline.EffectGrant{}, ErrOfflineAttemptCorrupt
	}
	now := clock().UTC()
	if now.Before(accepted.AcceptedAt) || now.Sub(accepted.AcceptedAt) > 10*time.Second ||
		!now.Before(snapshot.grant.ExpiresAt) || !now.Before(snapshot.binding.ExpiresAt) || !now.Before(checkpoint.Prechange.ExpiresAt) {
		return offline.EffectGrant{}, offline.ErrEffectProofMismatch
	}
	digest, err := offline.VerifyEffectAttestationV1(signed, snapshot.challenge, snapshot.binding, snapshot.attempt, &snapshot.grant, snapshot.epoch,
		snapshot.trust.ObserverSubject, snapshot.trust.KeyID, ed25519.PublicKey(public), now)
	if err != nil || digest != accepted.AttestationSHA256 {
		return offline.EffectGrant{}, offline.ErrEffectProofMismatch
	}
	// Revalidate at the authority clock immediately before the one-use CAS.
	decisionTime := clock().UTC()
	if decisionTime.Before(now) || decisionTime.Sub(accepted.AcceptedAt) > 10*time.Second ||
		!decisionTime.Before(snapshot.grant.ExpiresAt) || !decisionTime.Before(checkpoint.Prechange.ExpiresAt) {
		return offline.EffectGrant{}, offline.ErrEffectProofMismatch
	}
	if _, err := offline.VerifyEffectAttestationV1(signed, snapshot.challenge, snapshot.binding, snapshot.attempt, &snapshot.grant, snapshot.epoch,
		snapshot.trust.ObserverSubject, snapshot.trust.KeyID, ed25519.PublicKey(public), decisionTime); err != nil {
		return offline.EffectGrant{}, err
	}
	consumed := snapshot.grant
	consumed.Phase, consumed.ConsumedAt = "consumed", &decisionTime
	consumedRaw, err := json.Marshal(consumed)
	if err != nil {
		return offline.EffectGrant{}, err
	}
	consumeProof := signedEffectConsumeProof{SchemaVersion: signedEffectConsumeProofSchema, PlanID: planID, AttemptID: attemptID,
		ChallengeID: challengeID, AttestationSHA256: accepted.AttestationSHA256, GrantID: grantID, ConsumedAt: decisionTime}
	consumeProofRaw, err := json.Marshal(consumeProof)
	if err != nil {
		return offline.EffectGrant{}, err
	}
	compares := make([]clientv3.Cmp, 0, len(snapshot.keys)+3)
	for i, key := range snapshot.keys {
		if i == effectChallengeRunnerAttempts {
			compares = append(compares, clientv3.Compare(clientv3.CreateRevision(key).WithPrefix(), "=", 0))
		} else {
			compares = append(compares, clientv3.Compare(clientv3.ModRevision(key), "=", snapshot.revisions[i]))
		}
	}
	compares = append(compares,
		clientv3.Compare(clientv3.ModRevision(s.offlineSignedEffectGrantProofKey(planID)), "=", proofRows.Kvs[0].ModRevision),
		clientv3.Compare(clientv3.CreateRevision(s.offlineSignedEffectConsumeProofKey(planID)), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(s.offlineSignedEffectConsumeWatermarkKey(planID)), "=", 0),
	)
	result, err := s.kv.Txn(ctx).If(compares...).Then(
		clientv3.OpPut(s.offlineEffectGrantKey(planID), string(consumedRaw)),
		clientv3.OpPut(s.offlineEffectGrantEventKey(planID, 2), string(consumedRaw)),
		clientv3.OpPut(s.offlineSignedEffectConsumeProofKey(planID), string(consumeProofRaw)),
		clientv3.OpPut(s.offlineSignedEffectConsumeWatermarkKey(planID), string(consumeProofRaw)),
	).Commit()
	if err != nil {
		reconcileCtx, cancel := signedEffectReconcileContext(ctx)
		defer cancel()
		if recovered, readErr := s.GetOfflineSignedConsumedGrant(reconcileCtx, planID, attemptID, challengeID); readErr == nil && recovered.ID == grantID {
			accepted, acceptedErr := s.ReadAcceptedOfflineEffectAttestation(reconcileCtx, planID, offline.EffectConsumePurpose, challengeID)
			if acceptedErr == nil && effectAttemptMatchesIdentity(accepted.Attempt, identity) {
				return recovered, nil
			}
		}
		return offline.EffectGrant{}, err
	}
	if !result.Succeeded {
		reconcileCtx, cancel := signedEffectReconcileContext(ctx)
		defer cancel()
		if recovered, readErr := s.GetOfflineSignedConsumedGrant(reconcileCtx, planID, attemptID, challengeID); readErr == nil && recovered.ID == grantID {
			accepted, acceptedErr := s.ReadAcceptedOfflineEffectAttestation(reconcileCtx, planID, offline.EffectConsumePurpose, challengeID)
			if acceptedErr == nil && effectAttemptMatchesIdentity(accepted.Attempt, identity) {
				return recovered, nil
			}
		}
		return offline.EffectGrant{}, ErrOfflineEffectGrantConflict
	}
	return consumed, nil
}
