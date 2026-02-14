package handler

import (
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"norn/v2/api/model"
)

func (h *Handler) ValidateAll(w http.ResponseWriter, r *http.Request) {
	specs, err := model.DiscoverApps(h.cfg.AppsDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var results []model.ValidationResult
	for _, spec := range specs {
		results = append(results, *model.ValidateSpec(spec))
	}
	writeJSON(w, results)
}

func (h *Handler) ValidateApp(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	specs, err := model.DiscoverApps(h.cfg.AppsDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	for _, spec := range specs {
		if spec.App == id {
			writeJSON(w, model.ValidateSpec(spec))
			return
		}
	}

	writeError(w, http.StatusNotFound, fmt.Sprintf("app %s not found", id))
}
