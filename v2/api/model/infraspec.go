package model

import (
	"os"

	"gopkg.in/yaml.v3"
)

type VolumeSpec struct {
	Name     string `yaml:"name" json:"name"`
	Mount    string `yaml:"mount" json:"mount"`
	ReadOnly bool   `yaml:"readOnly,omitempty" json:"readOnly,omitempty"`
}

type FunctionSpec struct {
	Timeout string `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	Memory  int    `yaml:"memory,omitempty" json:"memory,omitempty"`
}

type InfraSpec struct {
	App            string             `yaml:"name" json:"name"`
	Repo           *RepoSpec          `yaml:"repo,omitempty" json:"repo,omitempty"`
	Build          *BuildSpec         `yaml:"build,omitempty" json:"build,omitempty"`
	Processes      map[string]Process `yaml:"processes" json:"processes"`
	Services       []string           `yaml:"services,omitempty" json:"services,omitempty"`
	Secrets        []string           `yaml:"secrets,omitempty" json:"secrets,omitempty"`
	Migrations     string             `yaml:"migrations,omitempty" json:"migrations,omitempty"`
	Env            map[string]string  `yaml:"env,omitempty" json:"-"`
	Infrastructure *Infrastructure    `yaml:"infrastructure,omitempty" json:"infrastructure,omitempty"`
	Endpoints      []Endpoint         `yaml:"endpoints,omitempty" json:"endpoints,omitempty"`
	Volumes        []VolumeSpec       `yaml:"volumes,omitempty" json:"volumes,omitempty"`
	Snapshots      *SnapshotPolicy    `yaml:"snapshots,omitempty" json:"snapshots,omitempty"`
	Deploy         bool               `yaml:"deploy,omitempty" json:"deploy,omitempty"`
	DeployPolicy   *DeployPolicy      `yaml:"deployPolicy,omitempty" json:"deployPolicy,omitempty"`
}

type Endpoint struct {
	URL    string `yaml:"url" json:"url"`
	Region string `yaml:"region,omitempty" json:"region,omitempty"`
}

type Process struct {
	Port      int               `yaml:"port,omitempty" json:"port,omitempty"`
	Command   string            `yaml:"command,omitempty" json:"command,omitempty"`
	Schedule  string            `yaml:"schedule,omitempty" json:"schedule,omitempty"`
	Timezone  string            `yaml:"timezone,omitempty" json:"timezone,omitempty"`
	Function  *FunctionSpec     `yaml:"function,omitempty" json:"function,omitempty"`
	Health    *HealthSpec       `yaml:"health,omitempty" json:"health,omitempty"`
	Metrics   *MetricsSpec      `yaml:"metrics,omitempty" json:"metrics,omitempty"`
	Scaling   *Scaling          `yaml:"scaling,omitempty" json:"scaling,omitempty"`
	Drain     *Drain            `yaml:"drain,omitempty" json:"drain,omitempty"`
	Resources *Resources        `yaml:"resources,omitempty" json:"resources,omitempty"`
	Canary    *CanaryConfig     `yaml:"canary,omitempty" json:"canary,omitempty"`
	Env       map[string]string `yaml:"env,omitempty" json:"-"`
}

func ResolveProcessTimezone(spec *InfraSpec, proc Process) string {
	if proc.Timezone != "" {
		return proc.Timezone
	}
	if proc.Env != nil && proc.Env["TZ"] != "" {
		return proc.Env["TZ"]
	}
	if spec != nil && spec.Env != nil && spec.Env["TZ"] != "" {
		return spec.Env["TZ"]
	}
	return ""
}

type HealthSpec struct {
	Path     string `yaml:"path" json:"path"`
	Interval string `yaml:"interval,omitempty" json:"interval,omitempty"`
	Timeout  string `yaml:"timeout,omitempty" json:"timeout,omitempty"`
}

type MetricsSpec struct {
	Enabled bool   `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	Path    string `yaml:"path,omitempty" json:"path,omitempty"`
	Port    int    `yaml:"port,omitempty" json:"port,omitempty"`
}

type Scaling struct {
	Min       int        `yaml:"min,omitempty" json:"min,omitempty"`
	Max       int        `yaml:"max,omitempty" json:"max,omitempty"`
	PerRegion int        `yaml:"per_region,omitempty" json:"perRegion,omitempty"`
	Auto      *AutoScale `yaml:"auto,omitempty" json:"auto,omitempty"`
}

type AutoScale struct {
	Metric string `yaml:"metric" json:"metric"` // cpu, memory, kafka_lag, custom
	Target int    `yaml:"target" json:"target"`
	Topic  string `yaml:"topic,omitempty" json:"topic,omitempty"`
}

type Drain struct {
	Signal  string `yaml:"signal,omitempty" json:"signal,omitempty"`
	Timeout string `yaml:"timeout,omitempty" json:"timeout,omitempty"`
}

type Resources struct {
	CPU    int `yaml:"cpu,omitempty" json:"cpu,omitempty"`       // MHz
	Memory int `yaml:"memory,omitempty" json:"memory,omitempty"` // MB
}

type RepoSpec struct {
	URL        string `yaml:"url" json:"url"`
	Branch     string `yaml:"branch,omitempty" json:"branch,omitempty"`
	AutoDeploy bool   `yaml:"autoDeploy,omitempty" json:"autoDeploy,omitempty"`
	RepoWeb    string `yaml:"repoWeb,omitempty" json:"repoWeb,omitempty"`
}

type BuildSpec struct {
	Dockerfile string `yaml:"dockerfile,omitempty" json:"dockerfile,omitempty"`
	Test       string `yaml:"test,omitempty" json:"test,omitempty"`
}

type SnapshotPolicy struct {
	Keep             int    `yaml:"keep,omitempty" json:"keep,omitempty"`
	PreRestore       bool   `yaml:"preRestore,omitempty" json:"preRestore,omitempty"`
	RetentionEnabled bool   `yaml:"retentionEnabled,omitempty" json:"retentionEnabled,omitempty"`
	ExportBucket     string `yaml:"exportBucket,omitempty" json:"exportBucket,omitempty"`
}

type DeployPolicy struct {
	AutoRollback bool `yaml:"autoRollback,omitempty" json:"autoRollback,omitempty"`
}

type CanaryConfig struct {
	Count         int    `yaml:"count,omitempty" json:"count,omitempty"`
	EvaluateAfter string `yaml:"evaluateAfter,omitempty" json:"evaluateAfter,omitempty"`
}

type Infrastructure struct {
	Kafka         *KafkaInfra         `yaml:"kafka,omitempty" json:"kafka,omitempty"`
	Postgres      *PostgresInfra      `yaml:"postgres,omitempty" json:"postgres,omitempty"`
	Redis         *RedisInfra         `yaml:"redis,omitempty" json:"redis,omitempty"`
	NATS          *NATSInfra          `yaml:"nats,omitempty" json:"nats,omitempty"`
	ObjectStorage *ObjectStorageInfra `yaml:"objectStorage,omitempty" json:"objectStorage,omitempty"`
}

type KafkaInfra struct {
	Topics []string `yaml:"topics,omitempty" json:"topics,omitempty"`
}

type PostgresInfra struct {
	Database string `yaml:"database" json:"database"`
}

type RedisInfra struct {
	Namespace string `yaml:"namespace,omitempty" json:"namespace,omitempty"`
}

type NATSInfra struct {
	Streams []string `yaml:"streams,omitempty" json:"streams,omitempty"`
}

type ObjectStorageInfra struct {
	Provider string                `yaml:"provider,omitempty" json:"provider,omitempty"`
	Buckets  []ObjectStorageBucket `yaml:"buckets,omitempty" json:"buckets,omitempty"`
}

type ObjectStorageBucket struct {
	Name   string `yaml:"name" json:"name"`
	Access string `yaml:"access,omitempty" json:"access,omitempty"`
	Public bool   `yaml:"public,omitempty" json:"public,omitempty"`
	Prefix string `yaml:"prefix,omitempty" json:"prefix,omitempty"`
	Env    string `yaml:"env,omitempty" json:"env,omitempty"`
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
	applyDefaults(&spec)
	return &spec, nil
}

func applyDefaults(spec *InfraSpec) {
	if spec.Repo != nil && spec.Repo.Branch == "" {
		spec.Repo.Branch = "main"
	}
	if spec.Infrastructure != nil && spec.Infrastructure.ObjectStorage != nil {
		if spec.Infrastructure.ObjectStorage.Provider == "" {
			spec.Infrastructure.ObjectStorage.Provider = "garage"
		}
		for i, bucket := range spec.Infrastructure.ObjectStorage.Buckets {
			if bucket.Access == "" {
				bucket.Access = "readWrite"
			}
			spec.Infrastructure.ObjectStorage.Buckets[i] = bucket
		}
	}
	for name, p := range spec.Processes {
		if p.Health != nil {
			if p.Health.Interval == "" {
				p.Health.Interval = "10s"
			}
			if p.Health.Timeout == "" {
				p.Health.Timeout = "5s"
			}
		}
		if p.Metrics != nil && p.Metrics.Enabled && p.Metrics.Path == "" {
			p.Metrics.Path = "/metrics"
		}
		if p.Scaling != nil && p.Scaling.Min == 0 {
			p.Scaling.Min = 1
		}
		if p.Resources == nil {
			p.Resources = &Resources{CPU: 100, Memory: 128}
		}
		spec.Processes[name] = p
	}
}

// HasScheduledProcess returns true if any process has a cron schedule.
func (s *InfraSpec) HasScheduledProcess() bool {
	for _, p := range s.Processes {
		if p.Schedule != "" {
			return true
		}
	}
	return false
}

// AutoRollbackEnabled returns true unless explicitly disabled via deployPolicy.
func (s *InfraSpec) AutoRollbackEnabled() bool {
	if s.DeployPolicy == nil {
		return true
	}
	return s.DeployPolicy.AutoRollback
}

// ProcessCount returns the total number of instances across all processes.
func (s *InfraSpec) ProcessCount() int {
	count := 0
	for _, p := range s.Processes {
		n := 1
		if p.Scaling != nil && p.Scaling.Min > 0 {
			n = p.Scaling.Min
		}
		count += n
	}
	return count
}
