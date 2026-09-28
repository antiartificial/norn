package nomad

import (
	"crypto/sha256"
	"encoding/hex"
)

// DeploymentEffectExecutionID is the stable marker for one signed operation,
// accepted region and exact Nomad job revision. It is independent of the
// worker claim generation so recovery can observe a prior submit attempt.
func DeploymentEffectExecutionID(operationID, region, jobDigest string) string {
	sum := sha256.Sum256([]byte(operationID + "\x00" + region + "\x00" + jobDigest))
	return "nomad-deployment-" + hex.EncodeToString(sum[:16])
}
