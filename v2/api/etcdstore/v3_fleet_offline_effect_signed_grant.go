package etcdstore

// Grant transitions here use the accepted signed observer transcript rather
// than a local callback or runner-supplied backend claim. No provider effect or
// HTTP route is part of these source-only methods.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"
	"norn/v2/api/fleet/offline"
)

const signedEffectGrantProofSchema = "norn.fleet-offline-signed-effect-grant-proof/v1"

type signedEffectGrantProof struct {
	SchemaVersion     string `json:"schemaVersion"`
	PlanID            string `json:"planId"`
	AttemptID         string `json:"attemptId"`
	ChallengeID       string `json:"challengeId"`
	AttestationSHA256 string `json:"attestationSHA256"`
	GrantID           string `json:"grantId"`
	IssuedGrantSHA256 string `json:"issuedGrantSHA256"`
}

func (s *V3OperationStore) offlineSignedEffectGrantProofKey(planID string) string {
	return s.prefix + "/v3/fleet-offline-signed-effect-grant-proofs/" + planID + "/00000000000000000001"
}

// This separate append-only high-watermark catches a selective rollback of
// grant head/event/proof while target etcd continuity survives. Total etcd
// rollback remains an external state-continuity and fence-recovery concern.
func (s *V3OperationStore) offlineSignedEffectGrantWatermarkKey(planID string) string {
	return s.prefix + "/v3/fleet-offline-signed-effect-grant-watermarks/" + planID
}

func effectAttemptMatchesIdentity(attempt offline.Attempt, identity OfflineRunnerIdentity) bool {
	return attempt.PrincipalSubject == identity.Subject && attempt.PrincipalTokenID == identity.TokenID &&
		attempt.PrincipalDeviceID == identity.DeviceID
}

func (s *V3OperationStore) GetOfflineSignedEffectGrant(ctx context.Context, planID, attemptID, challengeID string) (offline.EffectGrant, error) {
	if s == nil || uuid.Validate(planID) != nil || uuid.Validate(attemptID) != nil || uuid.Validate(challengeID) != nil {
		return offline.EffectGrant{}, offline.ErrEffectAttestation
	}
	response, err := s.kv.Txn(ctx).Then(
		clientv3.OpGet(s.offlineSignedEffectGrantProofKey(planID)),
		clientv3.OpGet(s.offlineSignedEffectGrantWatermarkKey(planID)),
		clientv3.OpGet(s.offlineEffectGrantKey(planID)),
		clientv3.OpGet(s.offlineEffectGrantEventKey(planID, 1)),
		clientv3.OpGet(s.offlineEffectGrantEventKey(planID, 2)),
	).Commit()
	if err != nil {
		return offline.EffectGrant{}, err
	}
	if len(response.Responses) != 5 {
		return offline.EffectGrant{}, ErrOfflineAttemptCorrupt
	}
	var raw [5][]byte
	for i, item := range response.Responses {
		rows := item.GetResponseRange().Kvs
		if len(rows) > 1 {
			return offline.EffectGrant{}, ErrOfflineAttemptCorrupt
		}
		if len(rows) == 1 {
			raw[i] = rows[0].Value
		}
	}
	if len(raw[0]) == 0 && len(raw[1]) == 0 && len(raw[2]) == 0 && len(raw[3]) == 0 && len(raw[4]) == 0 {
		return offline.EffectGrant{}, ErrNotFound
	}
	if len(raw[0]) == 0 && len(raw[1]) == 0 {
		return offline.EffectGrant{}, ErrOfflineEffectGrantConflict
	}
	if len(raw[0]) == 0 || !bytes.Equal(raw[0], raw[1]) {
		return offline.EffectGrant{}, ErrOfflineAttemptCorrupt
	}
	var proof signedEffectGrantProof
	if decodeV3Record(raw[0], &proof) != nil || proof.SchemaVersion != signedEffectGrantProofSchema || proof.PlanID != planID ||
		proof.AttemptID != attemptID || proof.ChallengeID != challengeID || uuid.Validate(proof.GrantID) != nil ||
		!offlineEvidenceDigest.MatchString(proof.AttestationSHA256) || !offlineEvidenceDigest.MatchString(proof.IssuedGrantSHA256) {
		return offline.EffectGrant{}, ErrOfflineAttemptCorrupt
	}
	canonical, err := json.Marshal(proof)
	if err != nil || !bytes.Equal(canonical, raw[0]) {
		return offline.EffectGrant{}, ErrOfflineAttemptCorrupt
	}
	grant, err := decodeOfflineEffectGrantPair(raw[2], raw[3], raw[4], planID)
	if err != nil {
		return offline.EffectGrant{}, err
	}
	issuedDigest := sha256.Sum256(raw[3])
	if grant.ID != proof.GrantID || grant.AttemptID != attemptID || hex.EncodeToString(issuedDigest[:]) != proof.IssuedGrantSHA256 {
		return offline.EffectGrant{}, ErrOfflineAttemptCorrupt
	}
	accepted, err := s.ReadAcceptedOfflineEffectAttestation(ctx, planID, offline.EffectIssuePurpose, challengeID)
	if err != nil || accepted.AttemptID != attemptID || accepted.AttestationSHA256 != proof.AttestationSHA256 {
		return offline.EffectGrant{}, ErrOfflineAttemptCorrupt
	}
	signed, err := offline.DecodeSignedEffectAttestationV1(accepted.Envelope)
	binding := accepted.Binding
	if err != nil || grant.IndependentlyObservedSHA256 != signed.Attestation.Observation.EvidenceSHA256 ||
		!offlineEvidenceDigest.MatchString(grant.PrechangeSHA256) ||
		grant.BindingSHA256 != accepted.Attempt.BindingSHA256 || grant.TargetID != binding.TargetID ||
		grant.FenceGeneration != binding.FenceGeneration || grant.AuthorityEpoch != accepted.Challenge.AuthorityEpoch ||
		grant.SourceCommit != binding.SourceCommit || grant.SavedPlanSHA256 != binding.SavedPlanSHA256 ||
		grant.EffectSHA256 != binding.EffectSHA256 || grant.Backend != binding.Backend ||
		grant.ProviderAccountUUID != binding.ProviderAccountUUID || grant.NodeName != binding.NodeName ||
		grant.DropletID != binding.DropletID {
		return offline.EffectGrant{}, ErrOfflineAttemptCorrupt
	}
	return grant, nil
}

// IssueOfflineEffectGrantFromAccepted is a one-shot target-owned transition.
// It reads a fresh accepted ISSUE transcript and atomically writes grant head,
// immutable issued event, and a proof pointer to the accepted attestation.
func (s *V3OperationStore) IssueOfflineEffectGrantFromAccepted(ctx context.Context, planID, attemptID, challengeID string, identity OfflineRunnerIdentity) (offline.EffectGrant, error) {
	return s.issueOfflineEffectGrantFromAcceptedWithClock(ctx, planID, attemptID, challengeID, identity, time.Now)
}
func (s *V3OperationStore) issueOfflineEffectGrantFromAcceptedWithClock(ctx context.Context, planID, attemptID, challengeID string, identity OfflineRunnerIdentity, clock func() time.Time) (offline.EffectGrant, error) {
	if s == nil || clock == nil || uuid.Validate(planID) != nil || uuid.Validate(attemptID) != nil || uuid.Validate(challengeID) != nil || !identity.valid() {
		return offline.EffectGrant{}, offline.ErrEffectProofMismatch
	}
	if prior, err := s.GetOfflineSignedEffectGrant(ctx, planID, attemptID, challengeID); err == nil {
		accepted, readErr := s.ReadAcceptedOfflineEffectAttestation(ctx, planID, offline.EffectIssuePurpose, challengeID)
		if readErr != nil || !effectAttemptMatchesIdentity(accepted.Attempt, identity) {
			return offline.EffectGrant{}, offline.ErrEffectProofMismatch
		}
		return prior, nil
	} else if !errors.Is(err, ErrNotFound) {
		return offline.EffectGrant{}, err
	}
	snapshot, err := s.readEffectChallengeSnapshot(ctx, planID, offline.EffectIssuePurpose)
	if err != nil {
		return offline.EffectGrant{}, err
	}
	if !snapshot.hasChallenge || snapshot.accepted == nil || snapshot.challenge.ID != challengeID || snapshot.attempt.ID != attemptID ||
		!effectAttemptMatchesIdentity(snapshot.attempt, identity) {
		return offline.EffectGrant{}, offline.ErrEffectProofMismatch
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
	if now.Before(accepted.AcceptedAt) || now.Sub(accepted.AcceptedAt) > 30*time.Second ||
		!now.Before(checkpoint.Prechange.ExpiresAt) || !now.Before(snapshot.binding.ExpiresAt) {
		return offline.EffectGrant{}, offline.ErrEffectProofMismatch
	}
	digest, err := offline.VerifyEffectAttestationV1(signed, snapshot.challenge, snapshot.binding, snapshot.attempt, nil, snapshot.epoch,
		snapshot.trust.ObserverSubject, snapshot.trust.KeyID, ed25519.PublicKey(public), now)
	if err != nil || digest != accepted.AttestationSHA256 {
		return offline.EffectGrant{}, offline.ErrEffectProofMismatch
	}
	expires := now.Add(time.Minute)
	if snapshot.binding.ExpiresAt.Before(expires) {
		expires = snapshot.binding.ExpiresAt
	}
	if checkpoint.Prechange.ExpiresAt.Before(expires) {
		expires = checkpoint.Prechange.ExpiresAt
	}
	if snapshot.challenge.ExpiresAt.Before(expires) {
		expires = snapshot.challenge.ExpiresAt
	}
	if !expires.After(now) {
		return offline.EffectGrant{}, offline.ErrEffectProofMismatch
	}
	observation := signed.Attestation.Observation
	grant := offline.EffectGrant{SchemaVersion: offline.EffectGrantSchema, ID: uuid.NewString(), PlanID: planID, AttemptID: attemptID,
		BindingSHA256: snapshot.attempt.BindingSHA256, TargetID: snapshot.binding.TargetID, FenceGeneration: snapshot.binding.FenceGeneration,
		AuthorityEpoch: snapshot.epoch, SourceCommit: snapshot.binding.SourceCommit, SavedPlanSHA256: snapshot.binding.SavedPlanSHA256,
		EffectSHA256: snapshot.binding.EffectSHA256, Backend: snapshot.binding.Backend, ProviderAccountUUID: snapshot.binding.ProviderAccountUUID,
		NodeName: snapshot.binding.NodeName, DropletID: snapshot.binding.DropletID, PrechangeSHA256: checkpoint.EvidenceSHA256,
		IndependentlyObservedSHA256: observation.EvidenceSHA256, Phase: "issued", IssuedAt: now, ExpiresAt: expires}
	grantRaw, err := json.Marshal(grant)
	if err != nil {
		return offline.EffectGrant{}, err
	}
	sum := sha256.Sum256(grantRaw)
	proof := signedEffectGrantProof{SchemaVersion: signedEffectGrantProofSchema, PlanID: planID, AttemptID: attemptID,
		ChallengeID: challengeID, AttestationSHA256: accepted.AttestationSHA256, GrantID: grant.ID, IssuedGrantSHA256: hex.EncodeToString(sum[:])}
	proofRaw, err := json.Marshal(proof)
	if err != nil {
		return offline.EffectGrant{}, err
	}
	// Revalidate after all reads so a delayed verifier cannot extend validity.
	decisionTime := clock().UTC()
	if decisionTime.Before(now) || !decisionTime.Before(expires) || decisionTime.Sub(accepted.AcceptedAt) > 30*time.Second {
		return offline.EffectGrant{}, offline.ErrEffectProofMismatch
	}
	if _, err := offline.VerifyEffectAttestationV1(signed, snapshot.challenge, snapshot.binding, snapshot.attempt, nil, snapshot.epoch,
		snapshot.trust.ObserverSubject, snapshot.trust.KeyID, ed25519.PublicKey(public), decisionTime); err != nil {
		return offline.EffectGrant{}, err
	}
	compares := make([]clientv3.Cmp, 0, len(snapshot.keys)+2)
	for i, key := range snapshot.keys {
		if i == effectChallengeRunnerAttempts {
			compares = append(compares, clientv3.Compare(clientv3.CreateRevision(key).WithPrefix(), "=", 0))
		} else {
			compares = append(compares, clientv3.Compare(clientv3.ModRevision(key), "=", snapshot.revisions[i]))
		}
	}
	compares = append(compares, clientv3.Compare(clientv3.CreateRevision(s.offlineSignedEffectGrantProofKey(planID)), "=", 0))
	compares = append(compares, clientv3.Compare(clientv3.CreateRevision(s.offlineSignedEffectGrantWatermarkKey(planID)), "=", 0))
	result, err := s.kv.Txn(ctx).If(compares...).Then(
		clientv3.OpPut(s.offlineEffectGrantKey(planID), string(grantRaw)),
		clientv3.OpPut(s.offlineEffectGrantEventKey(planID, 1), string(grantRaw)),
		clientv3.OpPut(s.offlineSignedEffectGrantProofKey(planID), string(proofRaw)),
		clientv3.OpPut(s.offlineSignedEffectGrantWatermarkKey(planID), string(proofRaw)),
	).Commit()
	if err != nil {
		if recovered, readErr := s.GetOfflineSignedEffectGrant(ctx, planID, attemptID, challengeID); readErr == nil {
			accepted, acceptedErr := s.ReadAcceptedOfflineEffectAttestation(ctx, planID, offline.EffectIssuePurpose, challengeID)
			if acceptedErr == nil && effectAttemptMatchesIdentity(accepted.Attempt, identity) {
				return recovered, nil
			}
		}
		return offline.EffectGrant{}, err
	}
	if !result.Succeeded {
		if recovered, readErr := s.GetOfflineSignedEffectGrant(ctx, planID, attemptID, challengeID); readErr == nil {
			accepted, acceptedErr := s.ReadAcceptedOfflineEffectAttestation(ctx, planID, offline.EffectIssuePurpose, challengeID)
			if acceptedErr == nil && effectAttemptMatchesIdentity(accepted.Attempt, identity) {
				return recovered, nil
			}
		}
		return offline.EffectGrant{}, ErrOfflineEffectGrantConflict
	}
	return grant, nil
}
