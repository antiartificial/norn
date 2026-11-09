package startup

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"norn/v2/api/store"
)

// WorkloadReleaseInputKeyRingFromRuntimeConfig constructs the distinct key
// ring used to encrypt accepted SOPS snapshots. keysJSON maps versioned key
// IDs to base64-encoded 32-byte keys.
func WorkloadReleaseInputKeyRingFromRuntimeConfig(enabled bool, currentKeyID, keysJSON string) (*store.WorkloadReleaseInputKeyRing, error) {
	if !enabled {
		return nil, nil
	}
	currentKeyID = strings.TrimSpace(currentKeyID)
	if currentKeyID == "" {
		return nil, fmt.Errorf("NORN_WORKLOAD_RELEASE_INPUT_CURRENT_KEY_ID is required when NORN_WORKLOAD_RELEASE_INPUTS_ENABLED=true")
	}
	var encoded map[string]string
	if err := json.Unmarshal([]byte(keysJSON), &encoded); err != nil || len(encoded) == 0 {
		return nil, fmt.Errorf("NORN_WORKLOAD_RELEASE_INPUT_KEYS must be a non-empty JSON key map when NORN_WORKLOAD_RELEASE_INPUTS_ENABLED=true")
	}
	keys := make(map[string][]byte, len(encoded))
	for id, value := range encoded {
		key, err := base64.StdEncoding.DecodeString(value)
		if err != nil {
			key, err = base64.RawStdEncoding.DecodeString(value)
		}
		if err != nil {
			return nil, fmt.Errorf("NORN_WORKLOAD_RELEASE_INPUT_KEYS entry %q must be base64", id)
		}
		keys[id] = key
	}
	ring, err := store.NewWorkloadReleaseInputKeyRing(currentKeyID, keys)
	if err != nil {
		return nil, fmt.Errorf("workload release input key ring: %w", err)
	}
	return ring, nil
}
