package engine

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"sync"
	"time"

	"norn/v2/api/beacon"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

type Engine struct {
	mu        sync.RWMutex
	instances map[string]*Instance
	cronJobs  map[string]*CronEntry
	deploys   map[string]*Deployment
	health    map[string]*healthState
	restarts  map[string]*restartTracker

	db      *store.DB
	beacon  *beacon.Service
	appsDir string
	stopCh  chan struct{}
}

type healthState struct {
	status      string // passing, warning, critical
	failures    int
	lastCheck   time.Time
	lastHealthy time.Time
}

type restartTracker struct {
	attempts    int
	windowStart time.Time
	lastRestart time.Time
}

func New(db *store.DB, b *beacon.Service, appsDir string) (*Engine, error) {
	e := &Engine{
		instances: make(map[string]*Instance),
		cronJobs:  make(map[string]*CronEntry),
		deploys:   make(map[string]*Deployment),
		health:    make(map[string]*healthState),
		restarts:  make(map[string]*restartTracker),
		db:        db,
		beacon:    b,
		appsDir:   appsDir,
		stopCh:    make(chan struct{}),
	}

	// Register known apps for name parsing
	if specs, err := model.DiscoverApps(appsDir); err == nil {
		apps := make([]string, 0, len(specs))
		for _, s := range specs {
			apps = append(apps, s.App)
		}
		SetKnownApps(apps)
	}

	// Initial reconciliation from running containers
	if err := e.reconcile(context.Background()); err != nil {
		log.Printf("engine: initial reconcile: %v", err)
	}

	return e, nil
}

// Start begins background goroutines for supervision, health checks, cron,
// and periodic reconciliation. Call Stop to shut them down.
func (e *Engine) Start(ctx context.Context) {
	go e.reconcileLoop(ctx)
	go e.supervisorLoop(ctx)
	go e.healthCheckLoop(ctx)
	go e.cronLoop(ctx)
	log.Println("engine started")
}

func (e *Engine) Stop() {
	close(e.stopCh)
	log.Println("engine stopped")
}

// Healthy returns nil if the engine is operational.
func (e *Engine) Healthy() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := containerCmd(ctx, "list", "--format", "json")
	if err != nil {
		return fmt.Errorf("container runtime unavailable: %w", err)
	}
	return nil
}

// NodeInfo returns static local node information.
func (e *Engine) NodeInfo() *NodeInfo {
	hostname, _ := os.Hostname()
	return &NodeInfo{
		Name:     hostname,
		Address:  localIP(),
		Provider: "local",
		Region:   "local",
	}
}

func localIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "127.0.0.1"
	}
	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() && ipnet.IP.To4() != nil {
			return ipnet.IP.String()
		}
	}
	return "127.0.0.1"
}

// reconcile syncs in-memory state from running containers.
func (e *Engine) reconcile(ctx context.Context) error {
	entries, err := containerList(ctx)
	if err != nil {
		return err
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if !IsNornContainer(entry.Name) {
			continue
		}
		seen[entry.Name] = true

		if existing, ok := e.instances[entry.Name]; ok {
			existing.Status = normalizeStatus(entry.Status)
			existing.IP = entry.IP
			continue
		}

		parsed, err := ParseContainerName(entry.Name)
		if err != nil {
			log.Printf("engine: reconcile: skip %s: %v", entry.Name, err)
			continue
		}

		created, _ := time.Parse(time.RFC3339, entry.Created)
		e.instances[entry.Name] = &Instance{
			ContainerName: entry.Name,
			App:           parsed.App,
			Process:       parsed.Process,
			Replica:       parsed.Replica,
			Kind:          parsed.Kind,
			Status:        normalizeStatus(entry.Status),
			IP:            entry.IP,
			ImageTag:      entry.Image,
			StartedAt:     created,
		}
	}

	// Mark instances that disappeared as failed
	for name, inst := range e.instances {
		if !seen[name] && inst.IsRunning() {
			inst.Status = "failed"
			inst.LastEvent = "container disappeared"
		}
	}

	return nil
}

func (e *Engine) reconcileLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-e.stopCh:
			return
		case <-ticker.C:
			if err := e.reconcile(ctx); err != nil {
				log.Printf("engine: reconcile: %v", err)
			}
		}
	}
}

func normalizeStatus(raw string) string {
	switch raw {
	case "running", "started":
		return "running"
	case "stopped", "exited":
		return "stopped"
	case "created":
		return "pending"
	default:
		return raw
	}
}

// Instances returns a snapshot of all tracked instances.
func (e *Engine) Instances() map[string]*Instance {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make(map[string]*Instance, len(e.instances))
	for k, v := range e.instances {
		copy := *v
		out[k] = &copy
	}
	return out
}

// JobInstances returns all instances for an app.
func (e *Engine) JobInstances(appID string) ([]Instance, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	var out []Instance
	for _, inst := range e.instances {
		if inst.App == appID {
			copy := *inst
			out = append(out, copy)
		}
	}
	return out, nil
}

// PollInstances returns non-terminal instances for an app (like Nomad's PollAllocations).
func (e *Engine) PollInstances(appID string) ([]Instance, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	var out []Instance
	for _, inst := range e.instances {
		if inst.App == appID && !inst.IsTerminal() {
			copy := *inst
			out = append(out, copy)
		}
	}
	return out, nil
}

// JobStatus returns the aggregate status for an app's instances.
func (e *Engine) JobStatus(appID string) (string, error) {
	instances, err := e.JobInstances(appID)
	if err != nil {
		return "", err
	}
	if len(instances) == 0 {
		return "dead", nil
	}
	allRunning := true
	for _, inst := range instances {
		if inst.Kind == "cron" || inst.Kind == "batch" {
			continue
		}
		if !inst.IsRunning() {
			allRunning = false
			break
		}
	}
	if allRunning {
		return "running", nil
	}
	return "pending", nil
}

// Background loops delegate to implementations in supervisor.go, health.go, scheduler.go.
func (e *Engine) supervisorLoop(ctx context.Context) { e.runSupervisor(ctx) }
func (e *Engine) healthCheckLoop(ctx context.Context) { e.runHealthChecker(ctx) }
func (e *Engine) cronLoop(ctx context.Context)        { e.runCronScheduler(ctx) }
