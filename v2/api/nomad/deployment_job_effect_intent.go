package nomad

// DeploymentJobEffectInput is the secret-free descriptor shared by the
// accepted deployment effect and its private Nomad execution step. The job
// digest is calculated from the full private job immediately before Reserve.
type DeploymentJobEffectInput struct {
	App                    string `json:"app"`
	JobID                  string `json:"jobId,omitempty"`
	DeploymentID           string `json:"deploymentId"`
	Region                 string `json:"region"`
	NomadRegion            string `json:"nomadRegion"`
	ImageTag               string `json:"imageTag"`
	SpecDigest             string `json:"specDigest"`
	JobDigest              string `json:"jobDigest"`
	ExpectedJobModifyIndex uint64 `json:"expectedJobModifyIndex"`
}
