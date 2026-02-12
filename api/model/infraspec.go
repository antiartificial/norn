package model

import (
	"os"

	"gopkg.in/yaml.v3"
)

type InfraSpec struct {
	App         string       `yaml:"app" json:"app"`
	Role        string       `yaml:"role" json:"role"` // webserver, worker, cron, function
	Core        bool         `yaml:"core,omitempty" json:"core,omitempty"` // norn infrastructure component
	Port        int          `yaml:"port,omitempty" json:"port,omitempty"`
	Healthcheck string       `yaml:"healthcheck,omitempty" json:"healthcheck,omitempty"`
	Hosts       *Hosts       `yaml:"hosts,omitempty" json:"hosts,omitempty"`
	Build       *BuildSpec   `yaml:"build,omitempty" json:"build,omitempty"`
	Services    *ServiceDeps `yaml:"services,omitempty" json:"services,omitempty"`
	Secrets     []string     `yaml:"secrets,omitempty" json:"secrets,omitempty"`
	Migrations  *Migration   `yaml:"migrations,omitempty" json:"migrations,omitempty"`
	Artifacts   *Artifacts   `yaml:"artifacts,omitempty" json:"artifacts,omitempty"`
	Repo        *RepoSpec         `yaml:"repo,omitempty" json:"repo,omitempty"`
	Volumes     []VolumeSpec      `yaml:"volumes,omitempty" json:"volumes,omitempty"`
	Replicas    int               `yaml:"replicas,omitempty" json:"replicas,omitempty"`
	Env         map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
	Alerts      *AlertConfig      `yaml:"alerts,omitempty" json:"alerts,omitempty"`
	Schedule    string            `yaml:"schedule,omitempty" json:"schedule,omitempty"`   // cron expression e.g. "*/5 * * * *"
	Command     string            `yaml:"command,omitempty" json:"command,omitempty"`     // command to run in container
	Runtime     string            `yaml:"runtime,omitempty" json:"runtime,omitempty"`     // "docker" (default) or "incus"
	Timeout     int               `yaml:"timeout,omitempty" json:"timeout,omitempty"`     // max seconds per execution (default 300)
	Function    *FunctionSpec     `yaml:"function,omitempty" json:"function,omitempty"`
	Deploy      bool              `yaml:"deploy,omitempty" json:"deploy,omitempty"`       // must be true to appear in norn
}

func (s *InfraSpec) IsCron() bool     { return s.Role == "cron" }
func (s *InfraSpec) IsFunction() bool { return s.Role == "function" }

type FunctionSpec struct {
	Timeout int    `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	Trigger string `yaml:"trigger,omitempty" json:"trigger,omitempty"` // "http" (default)
	Memory  string `yaml:"memory,omitempty" json:"memory,omitempty"`   // e.g. "256m"
}

type RepoSpec struct {
	URL           string `yaml:"url" json:"url"`
	Branch        string `yaml:"branch,omitempty" json:"branch,omitempty"`
	WebhookSecret string `yaml:"webhookSecret,omitempty" json:"-"`
	AutoDeploy    bool   `yaml:"autoDeploy,omitempty" json:"autoDeploy,omitempty"`
	RepoWeb       string `yaml:"repoWeb,omitempty" json:"repoWeb,omitempty"`
}

type VolumeSpec struct {
	Name      string `yaml:"name" json:"name"`
	MountPath string `yaml:"mountPath" json:"mountPath"`
	Size      string `yaml:"size,omitempty" json:"size,omitempty"`         // for PVC
	HostPath  string `yaml:"hostPath,omitempty" json:"hostPath,omitempty"` // for host mounts
}

type Hosts struct {
	External string `yaml:"external,omitempty" json:"external,omitempty"`
	Internal string `yaml:"internal,omitempty" json:"internal,omitempty"`
}

type BuildSpec struct {
	Dockerfile string `yaml:"dockerfile" json:"dockerfile"`
	Test       string `yaml:"test,omitempty" json:"test,omitempty"`
}

type ServiceDeps struct {
	Postgres *PostgresDep `yaml:"postgres,omitempty" json:"postgres,omitempty"`
	KV       *KVDep       `yaml:"kv,omitempty" json:"kv,omitempty"`
	Events   *EventsDep   `yaml:"events,omitempty" json:"events,omitempty"`
	Storage  *StorageDep  `yaml:"storage,omitempty" json:"storage,omitempty"`
}

type PostgresDep struct {
	Database   string `yaml:"database" json:"database"`
	Migrations string `yaml:"migrations,omitempty" json:"migrations,omitempty"`
}

type KVDep struct {
	Namespace string `yaml:"namespace" json:"namespace"`
}

type EventsDep struct {
	Topics []string `yaml:"topics" json:"topics"`
}

type StorageDep struct {
	Bucket   string `yaml:"bucket" json:"bucket"`
	Provider string `yaml:"provider,omitempty" json:"provider,omitempty"` // minio, r2, s3, gcs, spaces
}

type Migration struct {
	Command  string `yaml:"command" json:"command"`
	Database string `yaml:"database" json:"database"`
}

type Artifacts struct {
	Retain int `yaml:"retain" json:"retain"`
}

func LoadInfraSpec(path string) (*InfraSpec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var spec InfraSpec
	if err := yaml.Unmarshal(data, &spec); err != nil {
		return nil, err
	}
	if spec.Replicas < 1 {
		spec.Replicas = 1
	}
	if spec.Artifacts == nil {
		spec.Artifacts = &Artifacts{Retain: 5}
	}
	if spec.Repo != nil && spec.Repo.Branch == "" {
		spec.Repo.Branch = "main"
	}
	if spec.Alerts == nil {
		spec.Alerts = &AlertConfig{Window: "5m", Threshold: 3}
	}
	if spec.IsFunction() && spec.Function == nil {
		spec.Function = &FunctionSpec{Timeout: 30, Trigger: "http", Memory: "256m"}
	}
	if spec.IsFunction() && spec.Function.Timeout == 0 {
		spec.Function.Timeout = 30
	}
	if spec.IsFunction() && spec.Function.Trigger == "" {
		spec.Function.Trigger = "http"
	}
	if spec.IsFunction() && spec.Function.Memory == "" {
		spec.Function.Memory = "256m"
	}
	return &spec, nil
}
