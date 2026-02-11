package model

type AppStatus struct {
	Spec       *InfraSpec  `json:"spec"`
	Healthy    bool        `json:"healthy"`
	Ready      string      `json:"ready"`       // "2/2"
	CommitSHA  string      `json:"commitSha"`
	DeployedAt string      `json:"deployedAt"`
	Pods       []PodInfo   `json:"pods"`
	ForgeState *ForgeState `json:"forgeState,omitempty"`
}

type PodInfo struct {
	Name      string `json:"name"`
	Status    string `json:"status"`
	Ready     bool   `json:"ready"`
	Restarts  int32  `json:"restarts"`
	StartedAt string `json:"startedAt"`
}
