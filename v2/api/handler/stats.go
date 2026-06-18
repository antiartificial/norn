package handler

import (
	"net/http"

	"norn/v2/api/model"
)

func (h *Handler) Stats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	daily, err := h.db.GetDailyStats(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	specs, _ := model.DiscoverApps(h.cfg.AppsDir)
	appCount := len(specs)

	result := map[string]interface{}{
		"deploys":  daily,
		"appCount": appCount,
	}

	if h.engine != nil {
		totalAllocs, runningAllocs, leaderboard, err := h.engine.ClusterStats()
		if err == nil {
			result["totalAllocs"] = totalAllocs
			result["runningAllocs"] = runningAllocs
			result["uptimeLeaderboard"] = leaderboard
		}
	}

	writeJSON(w, result)
}
