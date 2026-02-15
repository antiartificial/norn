package nomad

import (
	"context"
	"fmt"
	"time"

	nomadapi "github.com/hashicorp/nomad/api"
)

// SubmitJob registers a job with Nomad.
func (c *Client) SubmitJob(job *nomadapi.Job) (string, error) {
	resp, _, err := c.api.Jobs().Register(job, nil)
	if err != nil {
		return "", fmt.Errorf("submit job: %w", err)
	}
	return resp.EvalID, nil
}

// StopJob stops a running Nomad job.
func (c *Client) StopJob(jobID string, purge bool) error {
	_, _, err := c.api.Jobs().Deregister(jobID, purge, nil)
	return err
}

// RestartJob forces a restart by creating a new evaluation.
func (c *Client) RestartJob(jobID string) error {
	job, _, err := c.api.Jobs().Info(jobID, nil)
	if err != nil {
		return fmt.Errorf("get job info: %w", err)
	}
	_, _, err = c.api.Jobs().Register(job, nil)
	return err
}

// JobStatus returns the status of a Nomad job.
func (c *Client) JobStatus(jobID string) (string, error) {
	job, _, err := c.api.Jobs().Info(jobID, nil)
	if err != nil {
		return "", err
	}
	if job.Status == nil {
		return "unknown", nil
	}
	return *job.Status, nil
}

// JobAllocations returns allocations for a job.
func (c *Client) JobAllocations(jobID string) ([]*nomadapi.AllocationListStub, error) {
	allocs, _, err := c.api.Jobs().Allocations(jobID, false, nil)
	if err != nil {
		return nil, err
	}
	return allocs, nil
}

// WaitHealthy waits until all allocations for a job report healthy.
// Returns an error if the timeout is exceeded.
func (c *Client) WaitHealthy(ctx context.Context, jobID string, timeout time.Duration) error {
	deadline := time.After(timeout)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return fmt.Errorf("timeout waiting for %s to become healthy", jobID)
		case <-ticker.C:
			allocs, err := c.JobAllocations(jobID)
			if err != nil {
				continue
			}
			if len(allocs) == 0 {
				continue
			}

			healthy := 0
			pending := 0
			for _, alloc := range allocs {
				// Skip terminal allocations from previous deploys
				if alloc.ClientStatus == "complete" || alloc.ClientStatus == "failed" || alloc.ClientStatus == "lost" {
					continue
				}
				if alloc.ClientStatus != "running" {
					pending++
					continue
				}
				if alloc.DeploymentStatus == nil || alloc.DeploymentStatus.Healthy == nil || !*alloc.DeploymentStatus.Healthy {
					pending++
					continue
				}
				healthy++
			}
			if healthy > 0 && pending == 0 {
				return nil
			}
		}
	}
}

// AllocStatus is a summary of a single non-terminal allocation.
type AllocStatus struct {
	ID           string `json:"id"`           // first 8 chars
	TaskGroup    string `json:"taskGroup"`
	ClientStatus string `json:"clientStatus"` // pending, running
	Healthy      *bool  `json:"healthy"`
	NodeID       string `json:"nodeId"`   // first 8 chars
	NodeName     string `json:"nodeName"`
}

// PollAllocations returns non-terminal allocations for a job (single poll).
func (c *Client) PollAllocations(jobID string) ([]AllocStatus, error) {
	allocs, err := c.JobAllocations(jobID)
	if err != nil {
		return nil, err
	}
	var out []AllocStatus
	for _, a := range allocs {
		if a.ClientStatus == "complete" || a.ClientStatus == "failed" || a.ClientStatus == "lost" {
			continue
		}
		as := AllocStatus{
			ID:           short(a.ID, 8),
			TaskGroup:    a.TaskGroup,
			ClientStatus: a.ClientStatus,
			NodeID:       a.NodeID,
			NodeName:     a.NodeID, // enrichable later via node cache
		}
		if a.DeploymentStatus != nil {
			as.Healthy = a.DeploymentStatus.Healthy
		}
		out = append(out, as)
	}
	return out, nil
}

func short(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// NodeInfo describes where a Nomad node is running.
type NodeInfo struct {
	Name     string `json:"name"`
	Address  string `json:"address"`
	Provider string `json:"provider"` // local, do, hz, remote
	Region   string `json:"region"`   // sfo3, fsn1, etc.
}

// NodeInfo returns location information for a Nomad node.
func (c *Client) NodeInfo(nodeID string) (*NodeInfo, error) {
	node, _, err := c.api.Nodes().Info(nodeID, nil)
	if err != nil {
		return nil, err
	}

	addr := node.HTTPAddr
	if ip, ok := node.Attributes["unique.network.ip-address"]; ok {
		addr = ip
	}

	info := &NodeInfo{
		Name:    node.Name,
		Address: addr,
	}

	// Detect provider from node attributes
	if _, ok := node.Attributes["unique.platform.digitalocean.id"]; ok {
		info.Provider = "do"
		info.Region = node.Attributes["platform.digitalocean.region"]
	} else if _, ok := node.Attributes["unique.platform.hetzner.id"]; ok {
		info.Provider = "hz"
		info.Region = node.Attributes["platform.hetzner.datacenter"]
	} else if osName, ok := node.Attributes["os.name"]; ok && osName == "darwin" {
		info.Provider = "local"
	} else {
		info.Provider = "remote"
	}

	return info, nil
}

// ScaleJob updates the count for a specific task group.
func (c *Client) ScaleJob(jobID, group string, count int) error {
	_, _, err := c.api.Jobs().Scale(jobID, group, &count, "scaled via norn", false, nil, nil)
	return err
}

// UptimeEntry describes a long-running allocation for the uptime leaderboard.
type UptimeEntry struct {
	AllocID   string `json:"allocId"`
	JobID     string `json:"jobId"`
	TaskGroup string `json:"taskGroup"`
	Uptime    string `json:"uptime"`
	NodeName  string `json:"nodeName"`
	StartedAt string `json:"startedAt"`
}

// ClusterStats gathers allocation counts and an uptime leaderboard across all jobs.
func (c *Client) ClusterStats() (totalAllocs, runningAllocs int, leaderboard []UptimeEntry, err error) {
	jobs, _, err := c.api.Jobs().List(nil)
	if err != nil {
		return 0, 0, nil, fmt.Errorf("list jobs: %w", err)
	}

	type allocInfo struct {
		allocID   string
		jobID     string
		taskGroup string
		nodeID    string
		createTime int64
	}

	var running []allocInfo

	for _, job := range jobs {
		if job.Status != "running" {
			continue
		}
		allocs, _, listErr := c.api.Jobs().Allocations(job.ID, false, nil)
		if listErr != nil {
			continue
		}
		for _, a := range allocs {
			totalAllocs++
			if a.ClientStatus == "running" {
				runningAllocs++
				running = append(running, allocInfo{
					allocID:    short(a.ID, 8),
					jobID:      job.ID,
					taskGroup:  a.TaskGroup,
					nodeID:     a.NodeID,
					createTime: a.CreateTime,
				})
			}
		}
	}

	// Sort by createTime ascending (longest running first)
	for i := 0; i < len(running); i++ {
		for j := i + 1; j < len(running); j++ {
			if running[j].createTime < running[i].createTime {
				running[i], running[j] = running[j], running[i]
			}
		}
	}

	// Take top 10
	limit := 10
	if len(running) < limit {
		limit = len(running)
	}

	nodeCache := make(map[string]string)
	now := time.Now()

	for _, a := range running[:limit] {
		started := time.Unix(0, a.createTime)
		uptime := now.Sub(started)

		nodeName := a.nodeID
		if cached, ok := nodeCache[a.nodeID]; ok {
			nodeName = cached
		} else if ni, niErr := c.NodeInfo(a.nodeID); niErr == nil {
			nodeName = ni.Name
			nodeCache[a.nodeID] = ni.Name
		}

		leaderboard = append(leaderboard, UptimeEntry{
			AllocID:   a.allocID,
			JobID:     a.jobID,
			TaskGroup: a.taskGroup,
			Uptime:    formatDuration(uptime),
			NodeName:  nodeName,
			StartedAt: started.Format(time.RFC3339),
		})
	}

	return totalAllocs, runningAllocs, leaderboard, nil
}

// ListJobs returns the count of registered jobs.
func (c *Client) ListJobs() (int, error) {
	jobs, _, err := c.api.Jobs().List(nil)
	if err != nil {
		return 0, err
	}
	return len(jobs), nil
}

// PeriodicForce triggers a periodic job.
func (c *Client) PeriodicForce(jobID string) (string, error) {
	evalID, _, err := c.api.Jobs().PeriodicForce(jobID, nil)
	if err != nil {
		return "", fmt.Errorf("periodic force: %w", err)
	}
	return evalID, nil
}

// JobInfo returns the full job specification.
func (c *Client) JobInfo(jobID string) (*nomadapi.Job, error) {
	job, _, err := c.api.Jobs().Info(jobID, nil)
	if err != nil {
		return nil, err
	}
	return job, nil
}

// PeriodicChildren lists child dispatches of a periodic job.
func (c *Client) PeriodicChildren(parentJobID string) ([]CronRun, error) {
	jobs, _, err := c.api.Jobs().List(&nomadapi.QueryOptions{
		Prefix: parentJobID + "/periodic-",
	})
	if err != nil {
		return nil, fmt.Errorf("list periodic children: %w", err)
	}

	var runs []CronRun
	for _, j := range jobs {
		run := CronRun{
			JobID:     j.ID,
			Status:    j.Status,
			StartedAt: time.Unix(0, j.SubmitTime).Format(time.RFC3339),
		}
		runs = append(runs, run)
	}
	return runs, nil
}

// CronRun describes a single execution of a periodic job.
type CronRun struct {
	JobID      string `json:"jobId"`
	Status     string `json:"status"`
	StartedAt  string `json:"startedAt"`
	FinishedAt string `json:"finishedAt,omitempty"`
	ExitCode   int    `json:"exitCode,omitempty"`
}

// WaitBatchComplete polls a batch job until it reaches a terminal state.
func (c *Client) WaitBatchComplete(ctx context.Context, jobID string, timeout time.Duration) (string, int, error) {
	deadline := time.After(timeout)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return "", 0, ctx.Err()
		case <-deadline:
			return "timeout", 0, fmt.Errorf("timeout waiting for batch job %s", jobID)
		case <-ticker.C:
			allocs, err := c.JobAllocations(jobID)
			if err != nil {
				continue
			}
			for _, a := range allocs {
				if a.ClientStatus == "complete" {
					exitCode := 0
					for _, ts := range a.TaskStates {
						if ts.State == "dead" && ts.Failed {
							exitCode = 1
						}
					}
					// Purge the job
					c.StopJob(jobID, true)
					return "complete", exitCode, nil
				}
				if a.ClientStatus == "failed" {
					exitCode := 1
					for _, ts := range a.TaskStates {
						if ts.State == "dead" && ts.Failed {
							exitCode = 1
						}
					}
					c.StopJob(jobID, true)
					return "failed", exitCode, nil
				}
			}
		}
	}
}

func formatDuration(d time.Duration) string {
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	if days > 0 {
		return fmt.Sprintf("%dd %dh %dm", days, hours, mins)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, mins)
	}
	return fmt.Sprintf("%dm", mins)
}
