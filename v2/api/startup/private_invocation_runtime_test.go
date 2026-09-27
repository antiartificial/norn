package startup

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

func TestPrivateInvocationKeyRingFromRuntimeConfig(t *testing.T) {
	current := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x11}, 32))
	retired := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x22}, 32))
	ring, err := PrivateInvocationKeyRingFromRuntimeConfig(true, "current", `{"current":"`+current+`","retired":"`+retired+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := ring.RequirePrivateInvocationKeys([]string{"retired", "current"}); err != nil {
		t.Fatalf("configured ring did not retain both key IDs: %v", err)
	}
}

func TestPrivateInvocationKeyRingFromRuntimeConfigAcceptsRawBase64(t *testing.T) {
	key := base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{0x33}, 32))
	ring, err := PrivateInvocationKeyRingFromRuntimeConfig(true, "current", `{"current":"`+key+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := ring.RequirePrivateInvocationKeys([]string{"current"}); err != nil {
		t.Fatal(err)
	}
}

func TestPrivateInvocationKeyRingFromRuntimeConfigFailsClosedWithoutLeakingValues(t *testing.T) {
	const canary = "private-invocation-key-canary"
	for _, tt := range []struct {
		name    string
		enabled bool
		current string
		keys    string
		wantErr bool
	}{
		{name: "disabled ignores incomplete values", enabled: false, keys: canary},
		{name: "missing current key ID", enabled: true, keys: `{}`, wantErr: true},
		{name: "malformed key map", enabled: true, current: "current", keys: canary, wantErr: true},
		{name: "invalid key encoding", enabled: true, current: "current", keys: `{"current":"` + canary + `"}`, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := PrivateInvocationKeyRingFromRuntimeConfig(tt.enabled, tt.current, tt.keys)
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr %t", err, tt.wantErr)
			}
			if err != nil && strings.Contains(err.Error(), canary) {
				t.Fatalf("configuration error exposed private key material: %v", err)
			}
		})
	}
}
