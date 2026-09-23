package handler

import (
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
)

func (h *Handler) StreamLogs(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	// Runtime logs follow the same app binding as history reads: an
	// app-bound credential may read only its own app's logs.
	if principal, ok := AccessPrincipalFromRequest(r); ok && principal.App != "" && principal.App != id {
		WriteControlProblem(w, r, http.StatusForbidden, "app_logs_forbidden", "this credential may read only its own app's logs")
		return
	}
	if h.nomad == nil {
		writeError(w, http.StatusServiceUnavailable, "nomad not connected")
		return
	}

	follow := r.URL.Query().Get("follow") == "true"

	// The stream ends when the client disconnects; the upstream Nomad
	// requests are cancelled with it.
	reader, err := h.nomad.StreamLogs(r.Context(), id, follow)
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
