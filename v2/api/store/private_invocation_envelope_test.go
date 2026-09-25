package store

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestPrivateInvocationEnvelopeRotationAndBinding(t *testing.T) {
	oldKey := bytes.Repeat([]byte{0x31}, 32)
	newKey := bytes.Repeat([]byte{0x42}, 32)
	oldRing, err := NewPrivateInvocationKeyRing("old", map[string][]byte{"old": oldKey})
	if err != nil {
		t.Fatal(err)
	}
	// A caller cannot change the key ring after construction.
	clear(oldKey)
	binding := PrivateInvocationBinding{Authority: "authority-1", OperationID: "operation-1", App: "demo", Process: "resize"}
	input := PrivateInvocationInput{Body: "private-body-canary\nsecond line", Method: "POST", Path: "/upload?q=private-path-canary"}
	sealed, err := oldRing.Seal(binding, input)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(sealed)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("private-body-canary")) || bytes.Contains(encoded, []byte("private-path-canary")) {
		t.Fatalf("private bytes leaked into envelope: %s", encoded)
	}
	if digest, err := PrivateInvocationDigest(sealed); err != nil || !strings.HasPrefix(digest, "sha256:") {
		t.Fatalf("digest=%q err=%v", digest, err)
	}
	if _, err := NewPrivateInvocationKeyRing("new", map[string][]byte{"new": newKey}); err != nil {
		t.Fatal(err)
	}
	rotated, err := NewPrivateInvocationKeyRing("new", map[string][]byte{"old": bytes.Repeat([]byte{0x31}, 32), "new": newKey})
	if err != nil {
		t.Fatal(err)
	}
	opened, err := rotated.Open(binding, sealed)
	if err != nil || opened != input {
		t.Fatalf("opened=%+v err=%v", opened, err)
	}
	if err := rotated.RequirePrivateInvocationKeys([]string{"old", "new"}); err != nil {
		t.Fatal(err)
	}
	newOnly, _ := NewPrivateInvocationKeyRing("new", map[string][]byte{"new": newKey})
	if err := newOnly.RequirePrivateInvocationKeys([]string{"old"}); err == nil {
		t.Fatal("restore accepted a missing historical key")
	}
	if _, err := newOnly.Open(binding, sealed); err == nil {
		t.Fatal("opened record without its historical key")
	}
	other := binding
	other.OperationID = "operation-2"
	if _, err := rotated.Open(other, sealed); err == nil {
		t.Fatal("opened record under another operation")
	}
	modified := sealed
	modified.Ciphertext = bytes.Clone(sealed.Ciphertext)
	modified.Ciphertext[0] ^= 1
	if _, err := rotated.Open(binding, modified); err == nil {
		t.Fatal("opened tampered ciphertext")
	}
	modified = sealed
	modified.WrapNonce = []byte{1}
	if _, err := rotated.Open(binding, modified); err == nil {
		t.Fatal("accepted malformed nonce")
	}
}

func TestPrivateInvocationKeyRingAndEnvelopeBounds(t *testing.T) {
	if _, err := NewPrivateInvocationKeyRing("audit:key", map[string][]byte{"audit:key": bytes.Repeat([]byte{1}, 32)}); err == nil {
		t.Fatal("accepted invalid key ID")
	}
	if _, err := NewPrivateInvocationKeyRing("one", map[string][]byte{"one": []byte("short")}); err == nil {
		t.Fatal("accepted short key")
	}
	ring, _ := NewPrivateInvocationKeyRing("one", map[string][]byte{"one": bytes.Repeat([]byte{1}, 32)})
	if _, err := ring.Seal(PrivateInvocationBinding{}, PrivateInvocationInput{}); err == nil {
		t.Fatal("accepted incomplete binding")
	}
	binding := PrivateInvocationBinding{Authority: "authority", OperationID: "operation", App: "demo", Process: "fn"}
	if _, err := ring.Seal(binding, PrivateInvocationInput{Body: strings.Repeat("x", 64<<10)}); err == nil {
		t.Fatal("accepted oversized request material")
	}
}

func TestPrivateInvocationPublicInputRejectsPrivateFieldsAndValues(t *testing.T) {
	private := PrivateInvocationInput{Body: "secret-body-canary", Method: "PATCH", Path: "/private-path-canary"}
	base := OperationAcceptance{}
	base.Operation.Payload = map[string]interface{}{"process": "resize"}
	if err := validatePrivateInvocationPublicInput(base, private); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*OperationAcceptance){
		func(a *OperationAcceptance) { a.Operation.Payload["body"] = private.Body },
		func(a *OperationAcceptance) {
			a.Operation.Metadata = map[string]interface{}{"requestMethod": private.Method}
		},
		func(a *OperationAcceptance) {
			a.Semantics = map[string]interface{}{"nested": []interface{}{map[string]interface{}{"NORN_REQUEST_PATH": private.Path}}}
		},
		func(a *OperationAcceptance) { a.Operation.Message = private.Body },
		func(a *OperationAcceptance) { a.Audit.RequestID = private.Path },
	} {
		candidate := base
		candidate.Operation.Payload = map[string]interface{}{"process": "resize"}
		mutate(&candidate)
		if err := validatePrivateInvocationPublicInput(candidate, private); err == nil {
			t.Fatal("accepted private request bytes in public intent")
		}
	}
}
