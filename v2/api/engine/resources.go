package engine

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// InstanceResourceUsage returns live CPU/memory stats for a container.
func (e *Engine) InstanceResourceUsage(ctx context.Context, containerName string) (*ResourceUsage, error) {
	info, err := containerInspect(ctx, containerName)
	if err != nil {
		return nil, err
	}
	_ = info

	// Apple Containers doesn't expose stats directly through the CLI yet.
	// Fall back to reading /proc inside the VM.
	memBytes, memMax, err := readProcMemory(ctx, containerName)
	if err != nil {
		return nil, fmt.Errorf("read memory stats for %s: %w", containerName, err)
	}

	cpuPct, err := readProcCPU(ctx, containerName)
	if err != nil {
		cpuPct = 0
	}

	e.mu.RLock()
	inst := e.instances[containerName]
	taskGroup := ""
	if inst != nil {
		taskGroup = inst.Process
	}
	e.mu.RUnlock()

	return &ResourceUsage{
		ContainerName:    containerName,
		TaskGroup:        taskGroup,
		MemoryUsageBytes: memBytes,
		MemoryMaxBytes:   memMax,
		CPUPercent:       cpuPct,
	}, nil
}

// JobResourceUsage returns resource usage for all running instances of an app.
func (e *Engine) JobResourceUsage(ctx context.Context, appID string) ([]ResourceUsage, error) {
	instances, _ := e.PollInstances(appID)
	var out []ResourceUsage
	for _, inst := range instances {
		usage, err := e.InstanceResourceUsage(ctx, inst.ContainerName)
		if err != nil {
			continue
		}
		out = append(out, *usage)
	}
	return out, nil
}

// ClusterStats returns aggregate instance counts and an uptime leaderboard.
func (e *Engine) ClusterStats() (total int, running int, leaderboard []UptimeEntry, err error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	var entries []UptimeEntry
	for _, inst := range e.instances {
		total++
		if inst.IsRunning() {
			running++
			entries = append(entries, UptimeEntry{
				ContainerName: inst.ContainerName,
				App:           inst.App,
				Process:       inst.Process,
				StartedAt:     inst.StartedAt,
				Uptime:        formatDuration(time.Since(inst.StartedAt)),
			})
		}
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].StartedAt.Before(entries[j].StartedAt)
	})

	limit := 10
	if len(entries) < limit {
		limit = len(entries)
	}
	leaderboard = entries[:limit]
	return
}

// UsedPorts returns all ports in use by running instances.
// With per-container IPs, port conflicts are impossible between containers,
// but this is kept for API compatibility.
func (e *Engine) UsedPorts() ([]PortAllocation, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	seen := make(map[int]string)
	var out []PortAllocation
	for _, inst := range e.instances {
		if !inst.IsRunning() || inst.Port == 0 {
			continue
		}
		if _, exists := seen[inst.Port]; !exists {
			seen[inst.Port] = inst.App
			out = append(out, PortAllocation{
				App:  inst.App,
				Port: inst.Port,
			})
		}
	}
	return out, nil
}

// SuggestPort returns an available port. With per-container IPs, any port is
// available, so this mostly returns base directly.
func (e *Engine) SuggestPort(base int) (int, error) {
	return base, nil
}

func readProcMemory(ctx context.Context, containerName string) (usage, max uint64, err error) {
	out, err := containerCmd(ctx, "exec", containerName, "cat", "/proc/meminfo")
	if err != nil {
		return 0, 0, err
	}
	usage, max = parseMeminfo(string(out))
	return
}

func parseMeminfo(content string) (usage, total uint64) {
	lines := splitLines(content)
	var memTotal, memAvailable uint64
	for _, line := range lines {
		var key string
		var val uint64
		n, _ := fmt.Sscanf(line, "%s %d", &key, &val)
		if n < 2 {
			continue
		}
		switch key {
		case "MemTotal:":
			memTotal = val * 1024
		case "MemAvailable:":
			memAvailable = val * 1024
		}
	}
	if memTotal > 0 {
		usage = memTotal - memAvailable
	}
	return usage, memTotal
}

func readProcCPU(ctx context.Context, containerName string) (float64, error) {
	out, err := containerCmd(ctx, "exec", containerName, "cat", "/proc/stat")
	if err != nil {
		return 0, err
	}
	return parseProcStat(string(out)), nil
}

func parseProcStat(content string) float64 {
	lines := splitLines(content)
	if len(lines) == 0 {
		return 0
	}
	// First line: cpu  user nice system idle iowait irq softirq steal
	var tag string
	var user, nice, system, idle, iowait uint64
	n, _ := fmt.Sscanf(lines[0], "%s %d %d %d %d %d", &tag, &user, &nice, &system, &idle, &iowait)
	if n < 5 {
		return 0
	}
	total := user + nice + system + idle + iowait
	if total == 0 {
		return 0
	}
	active := user + nice + system
	return float64(active) / float64(total) * 100
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func formatDuration(d time.Duration) string {
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60

	if days > 0 {
		return fmt.Sprintf("%dd%dh", days, hours)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh%dm", hours, minutes)
	}
	return fmt.Sprintf("%dm", minutes)
}
