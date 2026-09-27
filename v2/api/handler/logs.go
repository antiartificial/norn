package handler

import (
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
)

func (h *Handler) StreamLogs(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if principal, ok := AccessPrincipalFromRequest(r); ok && principal.App != "" && principal.App != id {
		WriteControlProblem(w, r, http.StatusForbidden, "app_logs_forbidden", "this credential may read only its own app's logs")
		return
	}
	if h.workloads == nil {
		writeError(w, http.StatusServiceUnavailable, "workload connector not available")
		return
	}

	follow := r.URL.Query().Get("follow") == "true"

	reader, err := h.workloads.StreamLogs(r.Context(), id, follow)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer reader.Close()

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Transfer-Encoding", "chunked")

	flusher, ok := w.(http.Flusher)
	buf := make([]byte, 4096)
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				return
			}
			if ok {
				flusher.Flush()
			}
		}
		if err != nil {
			if err != io.EOF && r.Context().Err() == nil {
				writeError(w, http.StatusInternalServerError, err.Error())
			}
			return
		}
	}
}
