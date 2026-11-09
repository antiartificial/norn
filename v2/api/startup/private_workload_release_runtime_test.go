package startup

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestWorkloadReleaseInputKeyRingFromRuntimeConfig(t *testing.T) {
	key := strings.Repeat("k", 32)
	encoded := base64.StdEncoding.EncodeToString([]byte(key))
	ring, err := WorkloadReleaseInputKeyRingFromRuntimeConfig(true, "release-current", `{"release-current":"`+encoded+`"}`)
	if err != nil || ring == nil {
		t.Fatalf("key ring=%v err=%v", ring, err)
	}
	if disabled, err := WorkloadReleaseInputKeyRingFromRuntimeConfig(false, "", ""); err != nil || disabled != nil {
		t.Fatalf("disabled key ring=%v err=%v", disabled, err)
	}
	if _, err := WorkloadReleaseInputKeyRingFromRuntimeConfig(true, "release-current", `{"release-current":"not-base64"}`); err == nil || strings.Contains(err.Error(), "not-base64") {
		t.Fatalf("invalid key material was not rejected safely: %v", err)
	}
}
