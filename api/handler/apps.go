package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
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

		apps = append(apps, status)
	}

	writeJSON(w, apps)
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

func (h *Handler) discoverApps() ([]*model.InfraSpec, error) {
	entries, err := os.ReadDir(h.cfg.AppsDir)
	if err != nil {
		return nil, err
	}

	var specs []*model.InfraSpec
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		specPath := filepath.Join(h.cfg.AppsDir, entry.Name(), "infraspec.yaml")
		spec, err := model.LoadInfraSpec(specPath)
		if err != nil {
			continue
		}
		specs = append(specs, spec)
	}
	return specs, nil
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
