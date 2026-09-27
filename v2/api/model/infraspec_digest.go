package model

import (
	"crypto/sha256"
	"encoding/hex"

	"gopkg.in/yaml.v3"
)

// InfraSpecDigest binds an accepted operation to the complete source spec
// without copying its environment values into the operation record. YAML is
// used because the public JSON projection deliberately omits Env fields.
func InfraSpecDigest(spec *InfraSpec) (string, error) {
	encoded, err := yaml.Marshal(spec)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
