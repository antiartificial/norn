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
	App            string               `yaml:"name" json:"name"`
	Repo           *RepoSpec            `yaml:"repo,omitempty" json:"repo,omitempty"`
	Build          *BuildSpec           `yaml:"build,omitempty" json:"build,omitempty"`
	Processes      map[string]Process   `yaml:"processes" json:"processes"`
	Services       []string             `yaml:"services,omitempty" json:"services,omitempty"`
	Secrets        []string             `yaml:"secrets,omitempty" json:"secrets,omitempty"`
	Migrations     string               `yaml:"migrations,omitempty" json:"migrations,omitempty"`
	Env            map[string]string    `yaml:"env,omitempty" json:"env,omitempty"`
	Infrastructure *Infrastructure      `yaml:"infrastructure,omitempty" json:"infrastructure,omitempty"`
	Endpoints      []Endpoint           `yaml:"endpoints,omitempty" json:"endpoints,omitempty"`
	Volumes        []VolumeSpec         `yaml:"volumes,omitempty" json:"volumes,omitempty"`
	Deploy         bool                 `yaml:"deploy,omitempty" json:"deploy,omitempty"`
}

type Endpoint struct {
	URL    string `yaml:"url" json:"url"`
	Region string `yaml:"region,omitempty" json:"region,omitempty"`
}

type Process struct {
	Port      int           `yaml:"port,omitempty" json:"port,omitempty"`
	Command   string        `yaml:"command,omitempty" json:"command,omitempty"`
	Schedule  string        `yaml:"schedule,omitempty" json:"schedule,omitempty"`
	Function  *FunctionSpec `yaml:"function,omitempty" json:"function,omitempty"`
	Health    *HealthSpec   `yaml:"health,omitempty" json:"health,omitempty"`
	Scaling   *Scaling      `yaml:"scaling,omitempty" json:"scaling,omitempty"`
	Drain     *Drain        `yaml:"drain,omitempty" json:"drain,omitempty"`
	Resources *Resources    `yaml:"resources,omitempty" json:"resources,omitempty"`
}

type HealthSpec struct {
	Path     string `yaml:"path" json:"path"`
	Interval string `yaml:"interval,omitempty" json:"interval,omitempty"`
	Timeout  string `yaml:"timeout,omitempty" json:"timeout,omitempty"`
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

type Infrastructure struct {
	Kafka    *KafkaInfra    `yaml:"kafka,omitempty" json:"kafka,omitempty"`
	Postgres *PostgresInfra `yaml:"postgres,omitempty" json:"postgres,omitempty"`
	Redis    *RedisInfra    `yaml:"redis,omitempty" json:"redis,omitempty"`
	NATS     *NATSInfra     `yaml:"nats,omitempty" json:"nats,omitempty"`
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
	for name, p := range spec.Processes {
		if p.Health != nil {
			if p.Health.Interval == "" {
				p.Health.Interval = "10s"
			}
			if p.Health.Timeout == "" {
				p.Health.Timeout = "5s"
			}
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
