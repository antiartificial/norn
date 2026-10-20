package etcdstore

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"sync"
	"testing"

	clientv3 "go.etcd.io/etcd/client/v3"
)

func TestOfflineTerminalObserverTrustProvisionReplayAndReadback(t *testing.T) {
	h := newEtcdExecutorHarness(t)
	key := ed25519.PublicKey(make([]byte, ed25519.PublicKeySize))
	if err := h.s.ProvisionOfflineTerminalObserverTrust(h.ctx(), 1, "observer", "key-1", key); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := h.s.ProvisionOfflineTerminalObserverTrust(h.ctx(), 1, "observer", "key-1", key); err != nil {
			t.Fatalf("exact replay %d: %v", i, err)
		}
		if err := h.s.VerifyOfflineTerminalObserverTrust(h.ctx(), 1, "observer", "key-1", key); err != nil {
			t.Fatalf("readback %d: %v", i, err)
		}
	}
	other := ed25519.PublicKey(make([]byte, ed25519.PublicKeySize))
	other[0] = 1
	for _, tc := range []struct {
		generation     uint64
		subject, keyID string
		key            ed25519.PublicKey
	}{
		{2, "observer", "key-1", key}, {1, "other", "key-1", key},
		{1, "observer", "key-2", key}, {1, "observer", "key-1", other},
		{0, "observer", "key-1", key},
	} {
		if err := h.s.ProvisionOfflineTerminalObserverTrust(h.ctx(), tc.generation, tc.subject, tc.keyID, tc.key); err == nil {
			t.Fatalf("changed provisioning accepted: %+v", tc)
		}
		if err := h.s.VerifyOfflineTerminalObserverTrust(h.ctx(), tc.generation, tc.subject, tc.keyID, tc.key); err == nil {
			t.Fatalf("changed readback accepted: %+v", tc)
		}
	}
	if err := h.s.VerifyOfflineTerminalObserverTrust(h.ctx(), 1, "observer", "key-1", key); err != nil {
		t.Fatalf("changed attempts overwrote trust: %v", err)
	}
}

func TestOfflineTerminalObserverTrustConcurrentDifferentProvision(t *testing.T) {
	h := newEtcdExecutorHarness(t)
	keys := [2]ed25519.PublicKey{make([]byte, ed25519.PublicKeySize), make([]byte, ed25519.PublicKeySize)}
	keys[1][0] = 1
	var got [2]error
	var wg sync.WaitGroup
	for i := range got {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got[i] = h.s.ProvisionOfflineTerminalObserverTrust(h.ctx(), 1, "observer", "key", keys[i])
		}(i)
	}
	wg.Wait()
	if (got[0] == nil) == (got[1] == nil) {
		t.Fatalf("want exactly one winner, got %v / %v", got[0], got[1])
	}
	winner := 0
	if got[0] != nil {
		winner = 1
	}
	if err := h.s.VerifyOfflineTerminalObserverTrust(h.ctx(), 1, "observer", "key", keys[winner]); err != nil {
		t.Fatalf("winner readback: %v", err)
	}
	if err := h.s.VerifyOfflineTerminalObserverTrust(h.ctx(), 1, "observer", "key", keys[1-winner]); !errors.Is(err, ErrOfflineTerminalChallengeConflict) {
		t.Fatalf("loser readback: %v", err)
	}
}

func TestOfflineTerminalObserverTrustCorruptionFailsClosed(t *testing.T) {
	h := newEtcdExecutorHarness(t)
	key := ed25519.PublicKey(make([]byte, ed25519.PublicKeySize))
	if err := h.s.VerifyOfflineTerminalObserverTrust(h.ctx(), 1, "observer", "key", key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing trust: %v", err)
	}
	for _, raw := range []string{
		`{"schemaVersion":"norn.fleet-offline-terminal-observer-trust/v1","generation":1}`,
		`{"generation":1,"schemaVersion":"norn.fleet-offline-terminal-observer-trust/v1","observerSubject":"observer","keyId":"key","publicKey":"` + base64.RawURLEncoding.EncodeToString(key) + `"}`,
		`{"schemaVersion":"norn.fleet-offline-terminal-observer-trust/v1","generation":1,"observerSubject":"observer","keyId":"key","publicKey":"bad"}`,
		`{"schemaVersion":"norn.fleet-offline-terminal-observer-trust/v1","generation":1,"observerSubject":"observer","keyId":"key","publicKey":"` + base64.RawURLEncoding.EncodeToString(key) + `","extra":true}`,
	} {
		if _, err := h.s.kv.Put(h.ctx(), h.s.offlineTerminalObserverTrustKey(), raw); err != nil {
			t.Fatal(err)
		}
		if err := h.s.VerifyOfflineTerminalObserverTrust(h.ctx(), 1, "observer", "key", key); !errors.Is(err, ErrOfflineAttemptCorrupt) {
			t.Fatalf("corrupt readback %q: %v", raw, err)
		}
		if err := h.s.ProvisionOfflineTerminalObserverTrust(h.ctx(), 1, "observer", "key", key); !errors.Is(err, ErrOfflineAttemptCorrupt) {
			t.Fatalf("corrupt provision %q: %v", raw, err)
		}
	}
	response, err := h.s.kv.Get(h.ctx(), h.s.offlineTerminalObserverTrustKey(), clientv3.WithCountOnly())
	if err != nil || response.Count != 1 {
		t.Fatalf("trust key count: %d %v", response.Count, err)
	}
}
