package handler

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

type statsResponse struct {
	TotalBuilds     int    `json:"totalBuilds"`
	TotalDeploys    int    `json:"totalDeploys"`
	TotalFailures   int    `json:"totalFailures"`
	Services        int    `json:"services"`
	Containers      int    `json:"containers"`
	MostPopularApp  string `json:"mostPopularApp"`
	MostPopularN    int    `json:"mostPopularN"`
	LongestPod      string `json:"longestPod"`
	LongestApp      string `json:"longestApp"`
	LongestDuration string `json:"longestDuration"`
}

func (h *Handler) GetStats(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	daily, err := h.db.GetDailyStats(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	resp := statsResponse{
		TotalBuilds:    daily.TotalBuilds,
		TotalDeploys:   daily.TotalDeploys,
		TotalFailures:  daily.TotalFailures,
		MostPopularApp: daily.MostPopularApp,
		MostPopularN:   daily.MostPopularN,
	}

	specs, err := h.discoverApps()
	if err == nil {
		resp.Services = len(specs)
	}

	if h.kube != nil && specs != nil {
		var totalPods int
		var oldestStart time.Time
		var oldestPod, oldestApp string

		for _, spec := range specs {
			pods, err := h.kube.GetPods(ctx, "default", spec.App)
			if err != nil {
				continue
			}
			totalPods += len(pods)
			for _, p := range pods {
				if p.Status.StartTime != nil {
					st := p.Status.StartTime.Time
					if oldestStart.IsZero() || st.Before(oldestStart) {
						oldestStart = st
						oldestPod = p.Name
						oldestApp = spec.App
					}
				}
			}
		}
		resp.Containers = totalPods
		if oldestPod != "" {
			resp.LongestPod = oldestPod
			resp.LongestApp = oldestApp
			resp.LongestDuration = formatDuration(time.Since(oldestStart))
		}
	}

	writeJSON(w, resp)
}

func formatDuration(d time.Duration) string {
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60

	if days > 0 {
		return fmt.Sprintf("%dd %dh", days, hours)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, minutes)
	}
	return fmt.Sprintf("%dm", minutes)
}
