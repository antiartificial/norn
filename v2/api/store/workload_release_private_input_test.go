package store

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"norn/v2/api/model"
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

func TestWorkloadReleasePinnedMaterialRoundTripsExactSpecAndCiphertext(t *testing.T) {
	spec := &model.InfraSpec{App: "demo", Env: map[string]string{"PRIVATE_SETTING": "source-secret"},
		Processes: map[string]model.Process{"web": {Port: 8080, Resources: &model.Resources{CPU: 100, Memory: 128},
			Env: map[string]string{"PROCESS_SETTING": "process-secret"}}}}
	digest, err := model.InfraSpecDigest(spec)
	if err != nil {
		t.Fatal(err)
	}
	encryptedSnapshot := []byte("encrypted-sops-snapshot")
	encoded, err := EncodeWorkloadReleasePinnedMaterial(spec, digest, encryptedSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, encryptedSnapshot) {
		t.Fatal("SOPS ciphertext was not encoded as part of pinned material")
	}
	opened, snapshot, err := DecodeWorkloadReleasePinnedMaterial(encoded, "demo", digest)
	if err != nil || opened.Env["PRIVATE_SETTING"] != "source-secret" || opened.Processes["web"].Env["PROCESS_SETTING"] != "process-secret" ||
		!bytes.Equal(snapshot, encryptedSnapshot) {
		t.Fatalf("pinned material round trip spec=%+v snapshot=%q err=%v", opened, snapshot, err)
	}
	clear(snapshot)
	if _, _, err := DecodeWorkloadReleasePinnedMaterial(encoded, "other", digest); err == nil {
		t.Fatal("pinned source was accepted for another app")
	}
	if _, _, err := DecodeWorkloadReleasePinnedMaterial(encoded, "demo", "sha256:"+strings.Repeat("1", 64)); err == nil {
		t.Fatal("pinned source was accepted with another target digest")
	}
	if _, err := EncodeWorkloadReleasePinnedMaterial(spec, "sha256:"+strings.Repeat("1", 64), encryptedSnapshot); err == nil {
		t.Fatal("source spec was pinned under a digest that did not match its content")
	}
}
