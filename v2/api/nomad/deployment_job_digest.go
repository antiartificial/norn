package nomad

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	nomadapi "github.com/hashicorp/nomad/api"
)

// DigestDeploymentJob hashes the exact submitted JSON without the digest's
// own marker. The job submission record retains that JSON by Nomad version;
// no server-default normalization is needed to compare the source intent.
func DigestDeploymentJob(job *nomadapi.Job) (string, error) {
	if job == nil {
		return "", fmt.Errorf("deployment job is missing")
	}
	encoded, err := json.Marshal(job)
	if err != nil {
		return "", fmt.Errorf("encode deployment job")
	}
	var copy nomadapi.Job
	if err := json.Unmarshal(encoded, &copy); err != nil {
		return "", fmt.Errorf("decode deployment job")
	}
	delete(copy.Meta, DeploymentJobDigestMeta)
	encoded, err = json.Marshal(&copy)
	if err != nil {
		return "", fmt.Errorf("encode deployment job digest material")
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}
