package database

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// TargetIdentitySHA256 is the stable, nonsecret digest of the resolver's
// accepted writable target. A migration effect records it before launch and
// compares it with the binding resolved for recovery. Endpoint changes are
// represented by the service generation; credential rotation does not change
// this identity or authorize a different target.
func TargetIdentitySHA256(target TargetIdentity) (string, error) {
	if target.ServiceID == "" || target.ServiceGeneration == 0 || target.BindingID == "" ||
		target.BindingGeneration == 0 || (target.Engine != EnginePostgreSQL && target.Engine != EngineMySQL) ||
		target.Database == "" || target.Role == "" {
		return "", fmt.Errorf("database target identity is incomplete")
	}
	encoded, err := json.Marshal(target)
	if err != nil {
		return "", fmt.Errorf("encode database target identity: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}
