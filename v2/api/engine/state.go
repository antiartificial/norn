package engine

import (
	"time"

	"norn/v2/api/model"
)

type Instance struct {
	ContainerName string    `json:"containerName"`
	App           string    `json:"app"`
	Process       string    `json:"process"`
	Replica       int       `json:"replica"`
	Kind          string    `json:"kind"`
	Status        string    `json:"status"`
	Healthy       *bool     `json:"healthy,omitempty"`
	IP            string    `json:"ip"`
	Port          int       `json:"port"`
	ImageTag      string    `json:"imageTag"`
	StartedAt     time.Time `json:"startedAt"`
	Restarts      int       `json:"restarts"`
	OOMKilled     bool      `json:"oomKilled"`
	LastEvent     string    `json:"lastEvent,omitempty"`
	ExitCode      *int      `json:"exitCode,omitempty"`
}

func (inst *Instance) IsRunning() bool {
	return inst.Status == "running"
}

func (inst *Instance) IsTerminal() bool {
	return inst.Status == "stopped" || inst.Status == "failed"
}

func (inst *Instance) ToAllocation() model.Allocation {
	a := model.Allocation{
		ID:           ShortID(inst.ContainerName),
		TaskGroup:    inst.Process,
		Status:       inst.Status,
		Healthy:      inst.Healthy,
		NodeName:     "local",
		NodeAddress:  inst.IP,
		NodeProvider: "local",
		NodeRegion:   "local",
	}
	if !inst.StartedAt.IsZero() {
		a.StartedAt = inst.StartedAt.Format(time.RFC3339)
	}
	if inst.IsRunning() {
		a.Lifecycle = "active"
	} else {
		a.Lifecycle = "retained"
	}
	return a
}

type Deployment struct {
	ID        string    `json:"id"`
	App       string    `json:"app"`
	ImageTag  string    `json:"imageTag"`
	OldImage  string    `json:"oldImage,omitempty"`
	Status    string    `json:"status"` // running, canary, promoted, failed, complete
	IsCanary  bool      `json:"isCanary"`
	CreatedAt time.Time `json:"createdAt"`
}

type DeploymentInfo struct {
	ID         string `json:"id"`
	App        string `json:"app"`
	Status     string `json:"status"`
	StatusDesc string `json:"statusDesc"`
	IsCanary   bool   `json:"isCanary"`
}

type CronRun struct {
	ID         string `json:"id"`
	App        string `json:"app"`
	Process    string `json:"process"`
	Container  string `json:"container"`
	Status     string `json:"status"`
	ExitCode   *int   `json:"exitCode,omitempty"`
	StartedAt  string `json:"startedAt"`
	FinishedAt string `json:"finishedAt,omitempty"`
	DurationMs int64  `json:"durationMs,omitempty"`
}

type CronJobInfo struct {
	JobID       string `json:"jobId"`
	App         string `json:"app"`
	Process     string `json:"process"`
	Schedule    string `json:"schedule"`
	TimeZone    string `json:"timeZone"`
	Paused      bool   `json:"paused"`
	Status      string `json:"status"`
	SubmittedAt string `json:"submittedAt,omitempty"`
}

type TaskRestartInfo struct {
	App         string `json:"app"`
	TaskGroup   string `json:"taskGroup"`
	AllocID     string `json:"allocId"`
	Task        string `json:"task"`
	Restarts    uint64 `json:"restarts"`
	LastRestart time.Time `json:"lastRestart"`
	OOMKilled   bool   `json:"oomKilled"`
	LastEvent   string `json:"lastEvent"`
}

type ResourceUsage struct {
	ContainerName    string  `json:"containerName"`
	TaskGroup        string  `json:"taskGroup"`
	MemoryUsageBytes uint64  `json:"memoryUsageBytes"`
	MemoryMaxBytes   uint64  `json:"memoryMaxBytes"`
	CPUPercent       float64 `json:"cpuPercent"`
}

type UptimeEntry struct {
	ContainerName string    `json:"containerName"`
	App           string    `json:"app"`
	Process       string    `json:"process"`
	Uptime        string    `json:"uptime"`
	StartedAt     time.Time `json:"startedAt"`
}

type PortAllocation struct {
	App  string `json:"app"`
	Port int    `json:"port"`
}

type NodeInfo struct {
	Name     string `json:"name"`
	Address  string `json:"address"`
	Provider string `json:"provider"`
	Region   string `json:"region"`
}

type ServiceHealth struct {
	ServiceName string `json:"serviceName"`
	Node        string `json:"node"`
	Address     string `json:"address"`
	Port        int    `json:"port"`
	Status      string `json:"status"` // passing, warning, critical
}

type CronEntry struct {
	JobID    string
	App      string
	Process  string
	Schedule string
	Timezone string
	ImageTag string
	Env      map[string]string
	Spec     *model.InfraSpec
	Paused   bool
}
