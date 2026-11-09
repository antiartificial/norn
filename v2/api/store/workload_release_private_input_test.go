package store

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestWorkloadReleasePrivateInputEnvelopeRecoveryAndBinding(t *testing.T) {
	oldKey := bytes.Repeat([]byte{0x39}, 32)
	newKey := bytes.Repeat([]byte{0x71}, 32)
	ring, err := NewWorkloadReleaseInputKeyRing("release-old", map[string][]byte{"release-old": oldKey})
	if err != nil {
		t.Fatal(err)
	}
	clear(oldKey)
	binding := WorkloadReleaseInputBinding{
		Authority: "authority-1", Operation: "operation-1", App: "demo",
		PlanDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	input := []byte("encrypted-sops-document-canary")
	envelope, err := ring.Seal(binding, input)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, input) {
		t.Fatal("private SOPS snapshot appeared in its serialized envelope")
	}
	digest, err := WorkloadReleaseInputDigest(envelope)
	if err != nil || !strings.HasPrefix(digest, "sha256:") {
		t.Fatalf("digest=%q err=%v", digest, err)
	}
	rotated, err := NewWorkloadReleaseInputKeyRing("release-new", map[string][]byte{
		"release-old": bytes.Repeat([]byte{0x39}, 32), "release-new": newKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	opened, err := rotated.Open(binding, envelope)
	if err != nil || !bytes.Equal(opened, input) {
		t.Fatalf("opened private snapshot mismatch: err=%v", err)
	}
	clear(opened)
	if err := rotated.RequireWorkloadReleaseInputKeys([]string{"release-old", "release-new"}); err != nil {
		t.Fatal(err)
	}
	newOnly, _ := NewWorkloadReleaseInputKeyRing("release-new", map[string][]byte{"release-new": newKey})
	if err := newOnly.RequireWorkloadReleaseInputKeys([]string{"release-old"}); err == nil {
		t.Fatal("restore accepted a missing retained release-input key")
	}
	if _, err := newOnly.Open(binding, envelope); err == nil {
		t.Fatal("opened a release input without its historical key")
	}
	wrongPlan := binding
	wrongPlan.PlanDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if _, err := rotated.Open(wrongPlan, envelope); err == nil {
		t.Fatal("opened a release input under another plan")
	}
	wrongApp := binding
	wrongApp.App = "other"
	if _, err := rotated.Open(wrongApp, envelope); err == nil {
		t.Fatal("opened a release input under another app")
	}
	wrongOperation := binding
	wrongOperation.Operation = "operation-2"
	if _, err := rotated.Open(wrongOperation, envelope); err == nil {
		t.Fatal("opened a release input under another operation")
	}
	wrongAuthority := binding
	wrongAuthority.Authority = "authority-2"
	if _, err := rotated.Open(wrongAuthority, envelope); err == nil {
		t.Fatal("opened a release input under another authority")
	}
	tampered := envelope
	tampered.Ciphertext = bytes.Clone(envelope.Ciphertext)
	tampered.Ciphertext[0] ^= 0x80
	if _, err := rotated.Open(binding, tampered); err == nil {
		t.Fatal("opened a tampered release input")
	}
}

func TestWorkloadReleasePrivateInputBoundsAndDistinctPurpose(t *testing.T) {
	if _, err := NewWorkloadReleaseInputKeyRing("audit:key", map[string][]byte{"audit:key": bytes.Repeat([]byte{1}, 32)}); err == nil {
		t.Fatal("accepted invalid release-input key ID")
	}
	if _, err := NewWorkloadReleaseInputKeyRing("release-one", map[string][]byte{"release-one": []byte("short")}); err == nil {
		t.Fatal("accepted short release-input key")
	}
	ring, err := NewWorkloadReleaseInputKeyRing("release-one", map[string][]byte{"release-one": bytes.Repeat([]byte{1}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ring.Seal(WorkloadReleaseInputBinding{}, []byte("ciphertext")); err == nil {
		t.Fatal("accepted incomplete release-input binding")
	}
	binding := WorkloadReleaseInputBinding{
		Authority: "authority", Operation: "operation", App: "demo",
		PlanDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	if _, err := ring.Seal(binding, nil); err == nil {
		t.Fatal("accepted empty encrypted snapshot")
	}
	if _, err := ring.Seal(binding, bytes.Repeat([]byte("x"), maxWorkloadReleasePrivateInput+1)); err == nil {
		t.Fatal("accepted oversized encrypted snapshot")
	}
	oversizedEnvelope := WorkloadReleaseInputEnvelope{
		Schema: WorkloadReleasePrivateInputSchema, KeyID: "release-one",
		Ciphertext: bytes.Repeat([]byte{1}, maxWorkloadReleasePrivateInput+17),
	}
	if _, err := ring.Open(binding, oversizedEnvelope); err == nil {
		t.Fatal("attempted to decrypt an oversized envelope")
	}
	wrongPurpose := WorkloadReleaseInputEnvelope{Schema: PrivateInvocationEnvelopeSchema, KeyID: "release-one"}
	if _, err := ring.Open(binding, wrongPurpose); err == nil {
		t.Fatal("accepted an envelope from another private-data purpose")
	}
}
