package dispatch

import "net/http"

type WSHandler struct {
	registry   *Registry
	dispatcher *Dispatcher
}

func NewWSHandler(registry *Registry, dispatcher *Dispatcher) *WSHandler {
	return &WSHandler{
		registry:   registry,
		dispatcher: dispatcher,
	}
}

func (h *WSHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "worker websocket dispatch is not implemented", http.StatusNotImplemented)
}
