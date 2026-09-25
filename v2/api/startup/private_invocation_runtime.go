package startup

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"norn/v2/api/store"
)

// PrivateInvocationKeyRingFromRuntimeConfig constructs the distinct envelope
// key ring for private function invocations. keysJSON is a JSON object mapping
// a versioned key ID to a base64-encoded 32-byte key-encryption key. Errors
// deliberately identify configuration fields or key IDs only; they never
// include configured key material.
func PrivateInvocationKeyRingFromRuntimeConfig(enabled bool, currentKeyID, keysJSON string) (*store.PrivateInvocationKeyRing, error) {
	if !enabled {
		return nil, nil
	}
	currentKeyID = strings.TrimSpace(currentKeyID)
	if currentKeyID == "" {
		return nil, fmt.Errorf("NORN_PRIVATE_INVOCATION_CURRENT_KEY_ID is required when NORN_PRIVATE_INVOCATION_ENABLED=true")
	}
	var encoded map[string]string
	if err := json.Unmarshal([]byte(keysJSON), &encoded); err != nil || len(encoded) == 0 {
		return nil, fmt.Errorf("NORN_PRIVATE_INVOCATION_KEYS must be a non-empty JSON key map when NORN_PRIVATE_INVOCATION_ENABLED=true")
	}
	keys := make(map[string][]byte, len(encoded))
	for id, encodedKey := range encoded {
		key, err := base64.StdEncoding.DecodeString(encodedKey)
		if err != nil {
			key, err = base64.RawStdEncoding.DecodeString(encodedKey)
		}
		if err != nil {
			return nil, fmt.Errorf("NORN_PRIVATE_INVOCATION_KEYS entry %q must be base64", id)
		}
		keys[id] = key
	}
	ring, err := store.NewPrivateInvocationKeyRing(currentKeyID, keys)
	if err != nil {
		return nil, fmt.Errorf("private invocation key ring: %w", err)
	}
	return ring, nil
}
