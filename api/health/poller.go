package health

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"norn/api/hub"
	"norn/api/model"
	"norn/api/store"
)

// Poller periodically checks app health endpoints and records results.
type Poller struct {
	DB       *store.DB
	WS       *hub.Hub
	AppsDir  string
	Interval time.Duration
	Client   *http.Client
}

// Run starts the polling loop. It blocks until ctx is cancelled.
func (p *Poller) Run(ctx context.Context) {
	if p.Interval == 0 {
		p.Interval = 30 * time.Second
	}
	if p.Client == nil {
		p.Client = &http.Client{Timeout: 5 * time.Second}
	}

	ticker := time.NewTicker(p.Interval)
	defer ticker.Stop()

	pruneTicker := time.NewTicker(1 * time.Hour)
	defer pruneTicker.Stop()

	// Run once immediately on start
	p.pollAll(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.pollAll(ctx)
		case <-pruneTicker.C:
			n, err := p.DB.PruneHealthChecks(ctx)
			if err != nil {
				log.Printf("health: prune error: %v", err)
			} else if n > 0 {
				log.Printf("health: pruned %d old checks", n)
			}
		}
	}
}

func (p *Poller) pollAll(ctx context.Context) {
	specs, err := model.DiscoverApps(p.AppsDir)
	if err != nil {
		log.Printf("health: discover error: %v", err)
		return
	}

	for _, spec := range specs {
		if spec.Healthcheck == "" || spec.Hosts == nil || spec.Hosts.Internal == "" {
			continue
		}
		go p.checkOne(ctx, spec)
	}
}

func (p *Poller) checkOne(ctx context.Context, spec *model.InfraSpec) {
	url := fmt.Sprintf("http://%s%s", spec.Hosts.Internal, spec.Healthcheck)

	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return
	}

	resp, err := p.Client.Do(req)
	elapsed := time.Since(start)

	healthy := false
	if err == nil {
		healthy = resp.StatusCode >= 200 && resp.StatusCode < 400
		resp.Body.Close()
	}

	hc := &model.HealthCheck{
		ID:         fmt.Sprintf("%s-%d", spec.App, time.Now().UnixMilli()),
		App:        spec.App,
		Healthy:    healthy,
		ResponseMs: int(elapsed.Milliseconds()),
		CheckedAt:  time.Now(),
	}

	if err := p.DB.InsertHealthCheck(ctx, hc); err != nil {
		log.Printf("health: insert error for %s: %v", spec.App, err)
		return
	}

	p.WS.Broadcast(hub.Event{
		Type:  "health.check",
		AppID: spec.App,
		Payload: map[string]interface{}{
			"id":         hc.ID,
			"healthy":    hc.Healthy,
			"responseMs": hc.ResponseMs,
			"checkedAt":  hc.CheckedAt.Format(time.RFC3339),
		},
	})
}
