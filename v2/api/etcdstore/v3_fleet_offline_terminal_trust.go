package etcdstore

// Terminal observer trust is provisioned on the target Fleet authority. It is
// deliberately immutable: replacing a verifier key requires a separately
// reviewed rotation protocol, never a silent overwrite of active trust.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"

	clientv3 "go.etcd.io/etcd/client/v3"
)

// ProvisionOfflineTerminalObserverTrust creates the one target-side observer
// trust record. An exact retry is safe after a lost response; any changed
// identity, key, or generation is refused. The private key stays off target.
func (s *V3OperationStore) ProvisionOfflineTerminalObserverTrust(ctx context.Context, generation uint64,
	observerSubject, keyID string, publicKey ed25519.PublicKey) error {
	if len(publicKey) != ed25519.PublicKeySize {
		return ErrOfflineAttemptCorrupt
	}
	want := terminalObserverTrust{SchemaVersion: terminalObserverTrustSchema, Generation: generation,
		ObserverSubject: observerSubject, KeyID: keyID,
		PublicKey: base64.RawURLEncoding.EncodeToString(publicKey)}
	encoded, err := json.Marshal(want)
	if err != nil {
		return err
	}
	if _, err := decodeTerminalObserverTrust(encoded); err != nil {
		return err
	}
	key := s.offlineTerminalObserverTrustKey()
	response, err := s.kv.Txn(ctx).If(clientv3.Compare(clientv3.CreateRevision(key), "=", 0)).
		Then(clientv3.OpPut(key, string(encoded))).Commit()
	if err != nil {
		// A lost commit response is recovered by an exact readback only.
		if readErr := s.VerifyOfflineTerminalObserverTrust(ctx, generation, observerSubject, keyID, publicKey); readErr == nil {
			return nil
		}
		return err
	}
	if response.Succeeded {
		return nil
	}
	return s.VerifyOfflineTerminalObserverTrust(ctx, generation, observerSubject, keyID, publicKey)
}

// VerifyOfflineTerminalObserverTrust requires a canonical record matching the
// exact expected generation, identity, and public key. It never accepts an
// older generation or a different key as an implicit rotation.
func (s *V3OperationStore) VerifyOfflineTerminalObserverTrust(ctx context.Context, generation uint64,
	observerSubject, keyID string, publicKey ed25519.PublicKey) error {
	if generation == 0 || len(publicKey) != ed25519.PublicKeySize || observerSubject == "" || keyID == "" {
		return ErrOfflineAttemptCorrupt
	}
	response, err := s.kv.Get(ctx, s.offlineTerminalObserverTrustKey())
	if err != nil {
		return err
	}
	if len(response.Kvs) == 0 {
		return ErrNotFound
	}
	if len(response.Kvs) != 1 {
		return ErrOfflineAttemptCorrupt
	}
	trust, err := decodeTerminalObserverTrust(response.Kvs[0].Value)
	if err != nil {
		return err
	}
	if trust.Generation != generation || trust.ObserverSubject != observerSubject || trust.KeyID != keyID {
		return ErrOfflineTerminalChallengeConflict
	}
	stored, err := base64.RawURLEncoding.Strict().DecodeString(trust.PublicKey)
	if err != nil || !bytes.Equal(stored, publicKey) {
		return ErrOfflineTerminalChallengeConflict
	}
	return nil
}
