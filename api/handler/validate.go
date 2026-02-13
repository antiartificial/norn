package handler

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"norn/api/model"
	"norn/api/validate"
)

func (h *Handler) ValidateApp(w http.ResponseWriter, r *http.Request) {
	appID := chi.URLParam(r, "id")

	spec, err := h.loadSpec(appID)
	if err != nil {
		http.Error(w, "app not found", http.StatusNotFound)
		return
	}

	v := &validate.Validator{Secrets: h.secrets, Kube: h.kube}
	result := v.Validate(r.Context(), spec)

	writeJSON(w, result)
}

func (h *Handler) ValidateAllApps(w http.ResponseWriter, r *http.Request) {
	specs, err := h.discoverApps()
	if err != nil {
		http.Error(w, "failed to discover apps: "+err.Error(), http.StatusInternalServerError)
		return
	}

	v := &validate.Validator{Secrets: h.secrets, Kube: h.kube}
	results := make([]*model.ValidationResult, 0, len(specs))
	for _, spec := range specs {
		results = append(results, v.Validate(r.Context(), spec))
	}

	writeJSON(w, results)
}
