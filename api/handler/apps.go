package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"norn/api/hub"
	"norn/api/model"
)

func (h *Handler) ListApps(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	specs, err := h.discoverApps()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var apps []model.AppStatus
	for _, spec := range specs {
		status := model.AppStatus{Spec: spec, Healthy: false, Ready: "0/0"}

		if h.kube != nil {
			pods, err := h.kube.GetPods(ctx, "default", spec.App)
			if err == nil {
				var readyCount int
				for _, p := range pods {
					pi := model.PodInfo{
						Name:     p.Name,
						Status:   string(p.Status.Phase),
						Restarts: 0,
					}
					if len(p.Status.ContainerStatuses) > 0 {
						pi.Ready = p.Status.ContainerStatuses[0].Ready
						pi.Restarts = p.Status.ContainerStatuses[0].RestartCount
					}
					if p.Status.StartTime != nil {
						pi.StartedAt = p.Status.StartTime.Format(time.RFC3339)
					}
					if pi.Ready {
						readyCount++
					}
					status.Pods = append(status.Pods, pi)
				}
				total := len(pods)
				status.Ready = formatReady(readyCount, total)
				status.Healthy = readyCount == total && total > 0
			}
		}

		deploys, _ := h.db.ListDeployments(ctx, spec.App, 1)
		if len(deploys) > 0 {
			status.CommitSHA = deploys[0].CommitSHA
			status.DeployedAt = deploys[0].StartedAt.Format(time.RFC3339)
		}

		forgeState, _ := h.db.GetForgeState(ctx, spec.App)
		status.ForgeState = forgeState

		if spec.IsCron() && h.scheduler != nil {
			status.CronState = h.scheduler.GetState(spec.App)
		}

		apps = append(apps, status)
	}

	// Populate remote heads concurrently
	var wg sync.WaitGroup
	for i := range apps {
		if apps[i].Spec.Repo != nil && apps[i].CommitSHA != "" {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				apps[idx].RemoteHeadSHA = h.getRemoteHead(ctx, apps[idx].Spec)
			}(i)
		}
	}
	wg.Wait()

	writeJSON(w, apps)
}

func (h *Handler) getRemoteHead(ctx context.Context, spec *model.InfraSpec) string {
	if spec.Repo == nil || h.pipeline == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "ls-remote", spec.Repo.URL, "refs/heads/"+spec.Repo.Branch)
	gitEnv, cleanup := h.pipeline.GitEnv(spec.Repo.URL)
	if cleanup != nil {
		defer cleanup()
	}
	cmd.Env = append(os.Environ(), gitEnv...)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	parts := strings.Fields(string(out))
	if len(parts) >= 1 {
		return parts[0]
	}
	return ""
}

func (h *Handler) GetApp(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	appID := chi.URLParam(r, "id")

	spec, err := h.loadSpec(appID)
	if err != nil {
		http.Error(w, "app not found", http.StatusNotFound)
		return
	}

	status := model.AppStatus{Spec: spec, Healthy: false, Ready: "0/0"}

	if h.kube != nil {
		pods, err := h.kube.GetPods(ctx, "default", spec.App)
		if err == nil {
			var readyCount int
			for _, p := range pods {
				pi := model.PodInfo{
					Name:   p.Name,
					Status: string(p.Status.Phase),
				}
				if len(p.Status.ContainerStatuses) > 0 {
					pi.Ready = p.Status.ContainerStatuses[0].Ready
					pi.Restarts = p.Status.ContainerStatuses[0].RestartCount
				}
				if p.Status.StartTime != nil {
					pi.StartedAt = p.Status.StartTime.Format(time.RFC3339)
				}
				if pi.Ready {
					readyCount++
				}
				status.Pods = append(status.Pods, pi)
			}
			status.Ready = formatReady(readyCount, len(pods))
			status.Healthy = readyCount == len(pods) && len(pods) > 0
		}
	}

	deploys, _ := h.db.ListDeployments(ctx, appID, 10)
	if len(deploys) > 0 {
		status.CommitSHA = deploys[0].CommitSHA
		status.DeployedAt = deploys[0].StartedAt.Format(time.RFC3339)
	}

	forgeState, _ := h.db.GetForgeState(ctx, appID)
	status.ForgeState = forgeState

	if spec.IsCron() && h.scheduler != nil {
		status.CronState = h.scheduler.GetState(appID)
	}

	writeJSON(w, map[string]interface{}{
		"status":      status,
		"deployments": deploys,
	})
}

func (h *Handler) StreamLogs(w http.ResponseWriter, r *http.Request) {
	if h.kube == nil {
		http.Error(w, "log streaming requires Kubernetes", http.StatusServiceUnavailable)
		return
	}

	appID := chi.URLParam(r, "id")
	podName := r.URL.Query().Get("pod")

	if podName == "" {
		pods, err := h.kube.GetPods(r.Context(), "default", appID)
		if err != nil || len(pods) == 0 {
			http.Error(w, "no pods found", http.StatusNotFound)
			return
		}
		podName = pods[0].Name
	}

	stream, err := h.kube.StreamLogs(r.Context(), "default", podName, r.URL.Query().Get("follow") == "true")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer stream.Close()

	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Transfer-Encoding", "chunked")

	buf := make([]byte, 4096)
	for {
		n, err := stream.Read(buf)
		if n > 0 {
			w.Write(buf[:n])
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
		if err != nil {
			break
		}
	}
}

func (h *Handler) Restart(w http.ResponseWriter, r *http.Request) {
	if h.kube == nil {
		http.Error(w, "restart requires Kubernetes", http.StatusServiceUnavailable)
		return
	}

	appID := chi.URLParam(r, "id")

	if err := h.kube.RestartDeployment(r.Context(), "default", appID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	h.ws.Broadcast(hub.Event{Type: "app.restarted", AppID: appID})
	writeJSON(w, map[string]string{"status": "restarting"})
}

func (h *Handler) GetHealthHistory(w http.ResponseWriter, r *http.Request) {
	appID := chi.URLParam(r, "id")

	rangeStr := r.URL.Query().Get("range")
	if rangeStr == "" {
		rangeStr = "1h"
	}
	dur, err := time.ParseDuration(rangeStr)
	if err != nil {
		http.Error(w, "invalid range parameter", http.StatusBadRequest)
		return
	}

	since := time.Now().Add(-dur)
	checks, err := h.db.ListHealthChecks(r.Context(), appID, since)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	spec, _ := h.loadSpec(appID)
	var alerts *model.AlertConfig
	if spec != nil {
		alerts = spec.Alerts
	}

	writeJSON(w, map[string]interface{}{
		"checks": checks,
		"alerts": alerts,
	})
}

func (h *Handler) discoverApps() ([]*model.InfraSpec, error) {
	return model.DiscoverApps(h.cfg.AppsDir)
}

func (h *Handler) loadSpec(appID string) (*model.InfraSpec, error) {
	return model.LoadInfraSpec(filepath.Join(h.cfg.AppsDir, appID, "infraspec.yaml"))
}

func formatReady(ready, total int) string {
	return fmt.Sprintf("%d/%d", ready, total)
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
